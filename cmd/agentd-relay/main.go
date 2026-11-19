// Command agentd-relay is the self-hosted rendezvous server of the agentd relay
// transport: agentd daemons register on it (outbound) and agentd-desktop /
// agentd-app reach them through it. Traffic is end-to-end encrypted between
// client and daemon; the relay only forwards. See docs/design/relay-transport.md.
package main

import (
	"context"
	"crypto/tls"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/digiogithub/agentd/internal/relayserver"
	"github.com/digiogithub/agentd/internal/version"
	"golang.org/x/crypto/acme/autocert"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "agentd-relay:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	fs := flag.NewFlagSet("agentd-relay", flag.ContinueOnError)
	listen := fs.String("listen", ":8443", "listen address")
	cert := fs.String("tls-cert", "", "TLS certificate file (with -tls-key)")
	key := fs.String("tls-key", "", "TLS key file")
	acmeDomains := fs.String("acme-domain", "", "comma-separated domains for an automatic Let's Encrypt certificate (needs :443 reachable)")
	acmeCache := fs.String("acme-cache", "", "ACME cache directory (default <user cache>/agentd-relay/acme)")
	insecure := fs.Bool("insecure-http", false, "serve plain HTTP (only behind a TLS reverse proxy or on loopback)")
	open := fs.Bool("open", false, "let any host register (no join token)")
	tokenFile := fs.String("join-token-file", "", "file with join tokens, one per line (also $AGENTD_RELAY_JOIN_TOKEN, comma-separated)")
	trustFwd := fs.Bool("trust-forwarded", false, "take client IPs from X-Forwarded-For (behind a reverse proxy)")
	maxTunnels := fs.Int("max-tunnels", 64, "tunnels per host")
	logLevel := fs.String("log-level", "info", "debug|info|warn|error")
	showVersion := fs.Bool("version", false, "print the version")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *showVersion {
		fmt.Println("agentd-relay", version.Version)
		return nil
	}
	var lvl slog.Level
	if err := lvl.UnmarshalText([]byte(*logLevel)); err != nil {
		return fmt.Errorf("bad -log-level: %w", err)
	}
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: lvl}))

	tokens, err := joinTokens(*tokenFile)
	if err != nil {
		return err
	}
	if len(tokens) == 0 && !*open {
		return errors.New("no join token: set $AGENTD_RELAY_JOIN_TOKEN or -join-token-file, or pass -open")
	}
	tlsModes := 0
	for _, on := range []bool{*cert != "" || *key != "", *acmeDomains != "", *insecure} {
		if on {
			tlsModes++
		}
	}
	if tlsModes != 1 {
		return errors.New("choose exactly one of -tls-cert/-tls-key, -acme-domain or -insecure-http")
	}

	srv := relayserver.New(relayserver.Config{
		JoinTokens: tokens, Open: *open, MaxTunnels: *maxTunnels, TrustForwarded: *trustFwd, Log: log,
	})
	hs := &http.Server{
		Addr:              *listen,
		Handler:           srv.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		MaxHeaderBytes:    16 << 10,
		ErrorLog:          slog.NewLogLogger(log.Handler(), slog.LevelWarn),
		// WebSockets need HTTP/1.1 upgrades: no h2 on this server.
		TLSNextProto: map[string]func(*http.Server, *tls.Conn, http.Handler){},
	}
	var acmeHTTP *http.Server
	switch {
	case *acmeDomains != "":
		dir := *acmeCache
		if dir == "" {
			base, err := os.UserCacheDir()
			if err != nil {
				return err
			}
			dir = base + "/agentd-relay/acme"
		}
		m := &autocert.Manager{
			Prompt:     autocert.AcceptTOS,
			Cache:      autocert.DirCache(dir),
			HostPolicy: autocert.HostWhitelist(strings.Split(*acmeDomains, ",")...),
		}
		hs.TLSConfig = &tls.Config{GetCertificate: m.GetCertificate, MinVersion: tls.VersionTLS12, NextProtos: []string{"http/1.1", "acme-tls/1"}}
		acmeHTTP = &http.Server{Addr: ":80", Handler: m.HTTPHandler(nil), ReadHeaderTimeout: 10 * time.Second}
	case *cert != "":
		hs.TLSConfig = &tls.Config{MinVersion: tls.VersionTLS12}
	case *insecure:
		host, _, _ := net.SplitHostPort(*listen)
		if ip := net.ParseIP(host); host == "" || (ip != nil && !ip.IsLoopback()) {
			log.Warn("plain HTTP on a non-loopback address: put a TLS reverse proxy in front")
		}
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	errc := make(chan error, 2)
	go func() {
		log.Info("agentd-relay listening", "addr", *listen, "version", version.Version, "open", *open)
		switch {
		case *insecure:
			errc <- hs.ListenAndServe()
		case *cert != "":
			errc <- hs.ListenAndServeTLS(*cert, *key)
		default:
			errc <- hs.ListenAndServeTLS("", "")
		}
	}()
	if acmeHTTP != nil {
		go func() { errc <- acmeHTTP.ListenAndServe() }()
	}
	select {
	case err := <-errc:
		if !errors.Is(err, http.ErrServerClosed) {
			return err
		}
	case <-ctx.Done():
	}
	sctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if acmeHTTP != nil {
		_ = acmeHTTP.Shutdown(sctx)
	}
	_ = hs.Shutdown(sctx)
	return nil
}

func joinTokens(file string) ([]string, error) {
	var out []string
	for _, t := range strings.Split(os.Getenv("AGENTD_RELAY_JOIN_TOKEN"), ",") {
		if t = strings.TrimSpace(t); t != "" {
			out = append(out, t)
		}
	}
	if file != "" {
		b, err := os.ReadFile(file)
		if err != nil {
			return nil, err
		}
		for _, line := range strings.Split(string(b), "\n") {
			if line = strings.TrimSpace(line); line != "" && !strings.HasPrefix(line, "#") {
				out = append(out, line)
			}
		}
	}
	for _, t := range out {
		if len(t) < 16 {
			return nil, errors.New("join tokens must be at least 16 characters")
		}
	}
	return out, nil
}
