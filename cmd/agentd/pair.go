package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	qrcode "github.com/skip2/go-qrcode"
	"github.com/spf13/cobra"

	"github.com/digiogithub/agentd/internal/apitoken"
	"github.com/digiogithub/agentd/internal/config"
	"github.com/digiogithub/agentd/internal/tlsutil"
)

// pairPayloadVersion is the `v` of the agentd://pair URL.
const pairPayloadVersion = "1"

// pairURL builds agentd://pair?v=1&u=<base>&c=<code>&fp=<pin>&h=<host>
// (security-remote 3.2). fp is omitted when empty.
func pairURL(base, code, fp, host string) string {
	q := url.Values{}
	q.Set("v", pairPayloadVersion)
	q.Set("u", base)
	q.Set("c", code)
	if fp != "" {
		q.Set("fp", fp)
	}
	if host != "" {
		q.Set("h", host)
	}
	// Fixed order (url.Values.Encode sorts by key).
	parts := []string{}
	for _, k := range []string{"v", "u", "c", "fp", "h"} {
		if v := q.Get(k); v != "" {
			parts = append(parts, k+"="+url.QueryEscape(v))
		}
	}
	return "agentd://pair?" + strings.Join(parts, "&")
}

// gatewayURL derives the base URL a device should use: the --url override,
// else scheme from api.tls and the listen address (an unspecified host becomes
// the machine's hostname).
func gatewayURL(api config.APIConfig, override string) (string, error) {
	if override != "" {
		u, err := url.Parse(override)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			return "", fmt.Errorf("invalid --url %q (want http(s)://host[:port])", override)
		}
		return strings.TrimRight(u.String(), "/"), nil
	}
	host, port, err := net.SplitHostPort(api.Listen)
	if err != nil {
		return "", fmt.Errorf("api.listen %q: %w", api.Listen, err)
	}
	if ip := net.ParseIP(host); host == "" || (ip != nil && ip.IsUnspecified()) {
		if host, err = os.Hostname(); err != nil || host == "" {
			host = "localhost"
		}
	}
	scheme := "http"
	if api.TLS != config.APITLSOff {
		scheme = "https"
	}
	return scheme + "://" + net.JoinHostPort(host, port), nil
}

// spkiPin is "sha256:<hex>" of the served certificate's SubjectPublicKeyInfo
// (self-signed: the persisted cert, generated on first use; files: the files;
// off: empty). See internal/tlsutil.
func spkiPin(api config.APIConfig) (string, error) {
	cfg, err := tlsConfigFor(api, "")
	if err != nil {
		return "", err
	}
	return tlsutil.CurrentPin(api.TLS, cfg)
}

type pairOptions struct {
	API       config.APIConfig
	URL       string // --url override
	Scope     string
	Name      string
	TTL       time.Duration
	NoApprove bool
	NoPin     bool
	Dir       string // api state dir ("" = default)
	Poll      time.Duration
	// Hostname overrides os.Hostname (tests).
	Hostname string
}

func pairCmd() *cobra.Command {
	var o pairOptions
	var cfgPath, ttl string
	cmd := &cobra.Command{
		Use:   "pair [--scope write|read|admin] [--name HINT] [--ttl 5m] [--url URL] [--no-approve]",
		Short: "Pair a device: print a QR / one-time code, wait for it and ask for approval",
		Long: `Create a one-time pairing code (60 bits, valid 5 minutes, 5 failed attempts burn
it), print it as a terminal QR (agentd://pair?v=1&u=...&c=...&fp=...&h=...)
plus the plain URL and code, and wait for a device to claim it through the
running 'agentd serve'. When a device claims the code you are shown its name,
platform and address and asked "Approve? [y/N]" (default no). The token is
delivered only to the device, once. --no-approve skips the question (scripted
setups only: anyone who sees the code in time gets a token).`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			var cfg *config.Config
			var err error
			if cfgPath != "" {
				cfg, err = config.Load(cfgPath)
			} else {
				cfg, err = config.LoadDefault()
			}
			if err != nil {
				return err
			}
			o.API = cfg.API
			if ttl != "" {
				if o.TTL, err = config.ParseDuration(ttl); err != nil {
					return fmt.Errorf("invalid --ttl: %w", err)
				}
			}
			ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
			defer stop()
			return runPair(ctx, o, cmd.InOrStdin(), cmd.OutOrStdout(), cmd.ErrOrStderr())
		},
	}
	cmd.Flags().StringVar(&o.Scope, "scope", apitoken.ScopeWrite, "scope of the issued token: read, write or admin")
	cmd.Flags().StringVar(&o.Name, "name", "", "suggested device name (shown to you; the device chooses its own)")
	cmd.Flags().StringVar(&ttl, "ttl", "5m", "life of the code (max 30m)")
	cmd.Flags().StringVar(&o.URL, "url", "", "gateway base URL as the device reaches it (default: from api.listen)")
	cmd.Flags().BoolVar(&o.NoApprove, "no-approve", false, "do not ask for confirmation (scripted setups)")
	cmd.Flags().BoolVar(&o.NoPin, "no-pin", false, "omit the TLS public-key pin (fp) from the payload")
	cmd.Flags().StringVar(&cfgPath, "config", "", "config file (default: the usual config.toml)")
	return cmd
}

// runPair issues the code, prints it and waits for the outcome.
func runPair(ctx context.Context, o pairOptions, in io.Reader, out, errw io.Writer) error {
	dir := o.Dir
	if dir == "" {
		var err error
		if dir, err = devicesDir(); err != nil {
			return err
		}
	}
	devs, err := apitoken.Open(dir, apitoken.Options{})
	if err != nil {
		return err
	}
	if devs.Err() != nil {
		return devs.Err()
	}
	pr, err := apitoken.OpenPairing(dir, apitoken.PairOptions{})
	if err != nil {
		return err
	}
	base, err := gatewayURL(o.API, o.URL)
	if err != nil {
		return err
	}
	pin := ""
	if !o.NoPin {
		if pin, err = spkiPin(o.API); err != nil {
			return err
		}
	}
	host := o.Hostname
	if host == "" {
		host, _ = os.Hostname()
	}
	code, id, exp, err := pr.Issue(apitoken.IssueArgs{Scope: o.Scope, NameHint: o.Name, TTL: o.TTL, NoApprove: o.NoApprove})
	if err != nil {
		return err
	}
	scope := o.Scope
	if scope == "" {
		scope = apitoken.ScopeWrite
	}
	link := pairURL(base, code, pin, host)
	if u, perr := url.Parse(base); perr == nil && config.IsLoopbackAddr(u.Host) {
		fmt.Fprintf(errw, "warning: %s is a loopback URL: only apps on this machine can use it (set --url or api.listen)\n", base)
	}
	if o.NoApprove {
		fmt.Fprintln(errw, "warning: --no-approve: the first device to present the code gets a token without confirmation")
	}
	if q, qerr := qrcode.New(link, qrcode.Low); qerr == nil {
		fmt.Fprintln(out, q.ToSmallString(false))
	} else {
		fmt.Fprintf(errw, "(QR not rendered: %v)\n", qerr)
	}
	fmt.Fprintf(out, "URL:   %s\nCode:  %s\n", base, code)
	if pin != "" {
		fmt.Fprintf(out, "Pin:   %s\n", pin)
	}
	fmt.Fprintf(out, "Link:  %s\n", link)
	fmt.Fprintf(out, "Scope: %s; valid until %s. Waiting for a device (Ctrl-C cancels)...\n", scope, exp.Local().Format("15:04:05"))

	poll := o.Poll
	if poll <= 0 {
		poll = 250 * time.Millisecond
	}
	asked := false
	t := time.NewTicker(poll)
	defer t.Stop()
	answers := make(chan bool, 1)
	for {
		cur, gerr := pr.Get(id)
		if gerr != nil {
			return fmt.Errorf("pairing record lost: %w", gerr)
		}
		switch cur.State {
		case apitoken.PairClaimed:
			if !asked && cur.Device != nil {
				asked = true
				d := cur.Device
				fmt.Fprintf(out, "\nDevice %q (%s, %s) wants %s access. Approve? [y/N] ", d.Name, orDash(d.Platform), orDash(d.Addr), cur.Scope)
				go func() { answers <- readYes(in) }()
			}
		case apitoken.PairApproved:
			if cur.NoApprove && !asked {
				asked = true
				fmt.Fprintf(out, "\nDevice %q (%s, %s) claimed the code (no approval requested).\n", cur.Device.Name, orDash(cur.Device.Platform), orDash(cur.Device.Addr))
			}
		case apitoken.PairDone:
			fmt.Fprintf(out, "Paired: device %s (%s) now has %s access.\n", cur.DeviceID, cur.Device.Name, cur.Scope)
			return nil
		case apitoken.PairDenied:
			fmt.Fprintln(out, "Pairing denied.")
			return errors.New("pairing denied")
		case apitoken.PairBurned:
			if time.Now().After(cur.ExpiresAt) {
				return errors.New("pairing code expired")
			}
			return errors.New("pairing code burned (too many wrong attempts, cancelled or replaced)")
		}
		select {
		case <-ctx.Done():
			_ = pr.Cancel(id)
			fmt.Fprintln(out, "\nCancelled.")
			return errors.New("pairing cancelled")
		case yes := <-answers:
			if err := pr.Decide(id, yes); err != nil {
				return err
			}
			if !yes {
			}
		case <-t.C:
		}
	}
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

// readYes reads one line; only y/yes (any case) approves. EOF or anything else
// is a no.
func readYes(in io.Reader) bool {
	line, _ := bufio.NewReader(in).ReadString('\n')
	switch strings.ToLower(strings.TrimSpace(line)) {
	case "y", "yes":
		return true
	}
	return false
}
