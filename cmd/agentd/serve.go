package main

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/digiogithub/agentd/internal/apitoken"
	"github.com/digiogithub/agentd/internal/client"
	"github.com/digiogithub/agentd/internal/config"
	"github.com/digiogithub/agentd/internal/httpapi"
	"github.com/digiogithub/agentd/internal/tlsutil"
	"github.com/digiogithub/agentd/internal/version"
)

func serveCmd() *cobra.Command {
	var listen, cfgPath, level string
	var noAutostart bool
	cmd := &cobra.Command{
		Use:   "serve",
		Short: "Run the HTTP API gateway (config.toml [api])",
		Long: `Run the HTTP/JSON + SSE gateway in front of the daemon, in the foreground.
It is a separate process: it talks to the daemon over the unix socket (and
starts the daemon if it is not running unless --no-autostart) and stops on
SIGINT/SIGTERM without touching the daemon or its sessions. Logs go to stderr.

Settings come from the [api] table of config.toml; --listen overrides
api.listen. A non-loopback address requires TLS; the gateway refuses to start
otherwise.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			var lv slog.Level
			if err := lv.UnmarshalText([]byte(level)); err != nil {
				return fmt.Errorf("invalid --log-level %q", level)
			}
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
			ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
			defer stop()
			dial := connect
			if noAutostart || !cfg.API.AutostartEnabled() {
				dial = func() (*client.Client, error) { return client.Dial(config.SocketPath()) }
			}
			return runServe(ctx, serveOptions{
				API: cfg.API, ConfigPath: cfg.Path, Listen: listen, Dial: dial,
				// The background event hub never starts a daemon.
				HubDial: func() (*client.Client, error) { return client.Dial(config.SocketPath()) }, Log: slog.New(slog.NewTextHandler(cmd.ErrOrStderr(), &slog.HandlerOptions{Level: lv})),
			})
		},
	}
	cmd.Flags().StringVar(&listen, "listen", "", "listen address (overrides api.listen)")
	cmd.Flags().StringVar(&cfgPath, "config", "", "config file (default: the usual config.toml)")
	cmd.Flags().StringVar(&level, "log-level", "info", "log level: debug, info, warn, error")
	cmd.Flags().BoolVar(&noAutostart, "no-autostart", false, "do not start the daemon when it is not running")
	cmd.AddCommand(devicesCmd(), pairCmd(), tlsCmd())
	return cmd
}

type serveOptions struct {
	API    config.APIConfig
	Listen string // overrides API.Listen when set
	// ConfigPath is the config.toml the settings came from; it is permission
	// checked when it holds a plain notification secret ("" = skip).
	ConfigPath string
	// Getenv reads the *_env notification secrets (default os.Getenv).
	Getenv func(string) string
	Dial   func() (*client.Client, error)
	// HubDial is the non-autostarting dialer of the event hub (default: Dial).
	HubDial func() (*client.Client, error)
	Log     *slog.Logger
	// DevicesDir overrides <state>/api (devices.json), for tests.
	DevicesDir string
	// TLSDir overrides <state>/api/tls (self-signed), for tests.
	TLSDir string
	// Ready, if set, is called with the bound address before serving.
	Ready func(addr net.Addr)
}

// runServe validates the settings, checks the daemon, binds and serves until
// ctx ends.
func runServe(ctx context.Context, o serveOptions) error {
	api := o.API
	if o.Listen != "" {
		api.Listen = o.Listen
	}
	if err := api.Validate(); err != nil {
		return err
	}
	getenv := o.Getenv
	if getenv == nil {
		getenv = os.Getenv
	}
	if err := api.Notify.ResolveSecrets(getenv, o.ConfigPath); err != nil {
		return err
	}
	if o.Dial == nil {
		return errors.New("serve: no daemon dialer configured")
	}
	if o.Log == nil {
		o.Log = slog.New(slog.DiscardHandler)
	}
	tlsSrc, err := serveTLS(api, o.TLSDir)
	if err != nil {
		return err
	}
	var tlsCfg *tls.Config
	if tlsSrc != nil {
		tlsCfg = tlsSrc.TLSConfig()
		o.Log.Info("tls", "mode", api.TLS, "pin", tlsSrc.Pin())
		if api.TLS == config.APITLSFiles {
			hup := make(chan os.Signal, 1)
			signal.Notify(hup, syscall.SIGHUP)
			defer signal.Stop(hup)
			go func() {
				for {
					select {
					case <-ctx.Done():
						return
					case <-hup:
						if err := tlsSrc.Reload(); err != nil {
							o.Log.Warn("tls reload failed, keeping the previous certificate", "err", err)
						} else {
							o.Log.Info("tls certificate reloaded", "pin", tlsSrc.Pin())
						}
					}
				}
			}()
		}
	}
	// Device tokens: the store must be usable before anything is exposed;
	// a broken one is fatal on a non-loopback bind, deny-all (logged) on
	// loopback.
	dir := o.DevicesDir
	if dir == "" {
		if dir, err = devicesDir(); err != nil {
			return err
		}
	}
	store, err := apitoken.Open(dir, apitoken.Options{Log: o.Log})
	if err != nil {
		return fmt.Errorf("device tokens: %w", err)
	}
	if serr := store.Err(); serr != nil && !config.IsLoopbackAddr(api.Listen) {
		return fmt.Errorf("device tokens: %w", serr)
	}
	defer store.FlushTouches()
	auth := apitoken.NewAuth(store)
	go store.Run(ctx)
	// Reach (and, with the autostarting dialer, start) the daemon first.
	c, err := o.Dial()
	if err != nil {
		return fmt.Errorf("daemon: %w", err)
	}
	if _, err := c.Ping(); err != nil {
		_ = c.Close()
		return fmt.Errorf("daemon: %w", err)
	}
	_ = c.Close()

	ln, err := net.Listen("tcp", api.Listen)
	if err != nil {
		return err
	}
	scheme := "http"
	if tlsCfg != nil {
		ln = tls.NewListener(ln, tlsCfg)
		scheme = "https"
	}
	// Audit trail: <state>/api/audit.log (0600, size-capped, one rotation).
	audit, err := httpapi.OpenAuditFile(filepath.Join(dir, "audit.log"), auditMaxBytes(api))
	if err != nil {
		return fmt.Errorf("audit log: %w", err)
	}
	defer audit.Close()
	hubDial := o.HubDial
	if hubDial == nil {
		hubDial = o.Dial
	}
	srv := &httpapi.Server{
		Config: httpapi.Config{
			Version:             version.Version,
			CORSOrigins:         api.CORSOrigins,
			AllowedHosts:        api.AllowedHosts,
			BindAddr:            ln.Addr().String(),
			TLSHosts:            api.TLSHosts,
			MaxBodyKB:           api.MaxBodyKB,
			SSEHeartbeat:        time0(api.SSEHeartbeat),
			EventBuffer:         api.EventBuffer,
			IdempotencyTTL:      time0(api.IdempotencyTTL),
			RunHarnesses:        api.RunHarnesses,
			RateLimit:           api.RateLimit,
			RateBurst:           api.RateBurst,
			ActRateLimit:        api.ActRateLimit,
			MaxStreamsPerDevice: api.MaxStreamsPerDevice,
			MaxStreams:          api.MaxStreams,
			Notify: httpapi.NotifyConfig{
				Enabled: api.Notify.Enabled, NtfyURL: api.Notify.NtfyURL, NtfyToken: api.Notify.NtfyToken,
				WebhookURL: api.Notify.WebhookURL, WebhookSecret: api.Notify.WebhookSecret,
				On: api.Notify.On, Debounce: time0(api.Notify.Debounce), IncludePreview: api.Notify.IncludePreview,
				HostLabel: api.Notify.HostLabel, DeepLinkBase: api.Notify.DeepLinkBase,
			},
		},
		Dial:    o.Dial,
		HubDial: hubDial,
		Log:     o.Log,
		Auth:    auth,
		Guard:   auth.Guard,
		Audit:   audit,
	}
	// Pairing: POST /v1/pair (unauthenticated; the one-time code is the
	// credential) shares api/pairing.json with `agentd serve pair`. It is only
	// mounted when [api] pairing = true (default).
	if api.PairingEnabled() {
		pairing, err := apitoken.OpenPairing(dir, apitoken.PairOptions{Log: o.Log})
		if err != nil {
			return fmt.Errorf("pairing: %w", err)
		}
		if err := srv.AddRoute(apitoken.PairRoute(pairing, store, apitoken.PairHandlerOptions{Log: o.Log, Audit: audit})); err != nil {
			return err
		}
	} else {
		o.Log.Info("pairing disabled: POST /v1/pair is not mounted")
	}
	o.Log.Info("serving", "url", scheme+"://"+ln.Addr().String())
	if l, _ := store.List(); len(l) == 0 && store.Err() == nil {
		o.Log.Warn("no devices configured: only /v1/health is available; create a token with `agentd serve devices add --name NAME`")
	}
	if o.Ready != nil {
		o.Ready(ln.Addr())
	}
	return srv.Serve(ctx, ln)
}

// serveTLS opens the TLS source for api.tls (nil for "off"). dir overrides
// <state>/api/tls (tests).
func serveTLS(a config.APIConfig, dir string) (*tlsutil.Source, error) {
	cfg, err := tlsConfigFor(a, dir)
	if err != nil {
		return nil, err
	}
	return tlsutil.Open(a.TLS, cfg)
}

// tlsConfigFor maps [api] to tlsutil.Config; dir "" = <state>/api/tls.
func tlsConfigFor(a config.APIConfig, dir string) (tlsutil.Config, error) {
	if dir == "" && a.TLS == config.APITLSSelfSigned {
		d, err := devicesDir()
		if err != nil {
			return tlsutil.Config{}, err
		}
		dir = filepath.Join(d, "tls")
	}
	return tlsutil.Config{Dir: dir, Cert: a.TLSCert, Key: a.TLSKey, Hosts: a.TLSHosts}, nil
}

func time0(d config.Duration) time.Duration { return time.Duration(d) }
