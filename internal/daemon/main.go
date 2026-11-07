package daemon

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"github.com/digiogithub/agentd/internal/config"
	"github.com/digiogithub/agentd/internal/mailbox"
)

// Options configures Main.
type Options struct {
	LogLevel slog.Level
	// Stderr also writes the log to stderr (foreground use).
	Stderr bool
}

// Main runs a daemon in the foreground until SIGTERM/SIGINT/SIGHUP or the
// shutdown op: it loads the config and aliases, opens the mailbox, logs to
// config.DaemonLogPath() and serves on config.SocketPath().
func Main(ctx context.Context, opts Options) error {
	// The daemon runs from "/" so it never pins the directory it was started
	// from (and sessions never inherit it); path overrides given relative to
	// the starting directory are made absolute first.
	if err := absPathEnv(config.EnvSocket, config.EnvStateDir, config.EnvConfig, config.EnvAliasesFile); err != nil {
		return err
	}
	if err := os.Chdir("/"); err != nil {
		return fmt.Errorf("chdir /: %w", err)
	}
	if err := config.EnsureDirs(); err != nil {
		return err
	}
	logPath, err := config.DaemonLogPath()
	if err != nil {
		return err
	}
	logf, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return fmt.Errorf("open daemon log: %w", err)
	}
	defer func() { _ = logf.Close() }()
	var w io.Writer = logf
	if opts.Stderr {
		w = io.MultiWriter(logf, os.Stderr)
	}
	log := slog.New(slog.NewTextHandler(w, &slog.HandlerOptions{Level: opts.LogLevel}))

	cfg, err := config.LoadDefault()
	if err != nil {
		log.Error("load config", "err", err)
		return err
	}
	aliases, err := config.LoadDefaultAliases()
	if err != nil {
		log.Warn("load aliases", "err", err) // aliases are only informative here
	}
	dbPath, err := config.MailboxPath()
	if err != nil {
		return err
	}

	// Listen first: it takes the daemon lock, so a losing concurrent starter
	// exits with ErrAlreadyRunning before touching the mailbox.
	srv := New(cfg, aliases, nil, log)
	if err := srv.Listen(); err != nil {
		log.Error("listen", "err", err)
		return err
	}
	store, err := mailbox.Open(dbPath)
	if err != nil {
		log.Error("open mailbox", "err", err)
		srv.Shutdown()
		return err
	}
	srv.Store = store
	srv.Delivery = srv.NewDeliveryLoop(log.With("component", "delivery"))
	ctx, stop := signal.NotifyContext(ctx, syscall.SIGTERM, syscall.SIGINT, syscall.SIGHUP)
	defer stop()
	return srv.Serve(ctx)
}

// absPathEnv rewrites each set, relative environment variable as an absolute
// path (relative to the current directory).
func absPathEnv(names ...string) error {
	for _, n := range names {
		v := os.Getenv(n)
		if v == "" || filepath.IsAbs(v) {
			continue
		}
		abs, err := filepath.Abs(v)
		if err != nil {
			return fmt.Errorf("%s: %w", n, err)
		}
		if err := os.Setenv(n, abs); err != nil {
			return err
		}
	}
	return nil
}
