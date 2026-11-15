// Command agentd-desktop is the native desktop client of agentd (MyGo): the
// agents of every host, their chat and terminal, hosts, mailbox and
// settings.
//
// By default it runs on the real backend (internal/desktopui/core): this
// computer's daemon socket (config.SocketPath, $AGENTD_SOCKET), saved SSH
// hosts, zellij/tmux (internal/desktopui/mux) and provisioning
// (internal/desktopui/provision). AGENTD_DESKTOP_FAKE=1 runs it on the
// in-memory fake backend with the data of the mockups (no host is
// contacted). AGENTD_DESKTOP_LOG=debug|info|warn|error sets the stderr log
// level (default warn).
package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/digiogithub/agentd/internal/config"
	"github.com/digiogithub/agentd/internal/desktopui"
	"github.com/digiogithub/agentd/internal/desktopui/app"
	"github.com/digiogithub/agentd/internal/desktopui/backend"
	"github.com/digiogithub/agentd/internal/desktopui/backend/fake"
	"github.com/digiogithub/agentd/internal/desktopui/core"
	"github.com/digiogithub/agentd/internal/desktopui/mux"
	"github.com/digiogithub/agentd/internal/desktopui/provision"
)

func main() { os.Exit(run()) }

func run() int {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: logLevel(os.Getenv("AGENTD_DESKTOP_LOG"))}))
	opts := desktopui.Options{Ctx: ctx, Logger: log}
	if os.Getenv("AGENTD_DESKTOP_FAKE") == "1" {
		opts.Backend = fake.New()
		opts.PrefsPath = "-" // fake runs never touch the real settings
		opts.Seed = func(s *app.Settings) {
			s.Muted = append(s.Muted, app.Mute{Session: "estudio/agentd/claude@docs", Label: "claude@docs",
				Until: time.Now().Add(24 * time.Hour)})
		}
	} else {
		b := newCore(log)
		// Deferred, so it also runs when Run returns an error.
		defer func() { _ = b.Close() }()
		opts.Backend = b
	}
	if err := desktopui.Run(opts); err != nil {
		fmt.Fprintln(os.Stderr, "agentd-desktop:", err)
		return 1
	}
	return 0
}

// newCore is the real backend. Unknown host keys and passwords are not
// prompted for here: Connect fails with backend.HostKeyError /
// backend.ErrPasswordRequired and the UI asks (TrustHostKey,
// ConnectWithPassword).
func newCore(log *slog.Logger) *core.Backend {
	return core.New(core.Options{
		LocalSocket: config.SocketPath(),
		Mux:         muxAdapters,
		Provisioner: provision.New(),
		AutoConnect: true,
		Logger:      log.With("component", "core"),
	})
}

// muxAdapters adapts mux.ForHost to core.MuxFactory (same method set,
// different slice element type).
func muxAdapters(sh backend.Shell, hostID string) []core.MuxAdapter {
	list := mux.ForHost(sh, hostID)
	out := make([]core.MuxAdapter, len(list))
	for i, a := range list {
		out[i] = a
	}
	return out
}

func logLevel(s string) slog.Level {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "debug":
		return slog.LevelDebug
	case "info":
		return slog.LevelInfo
	case "error":
		return slog.LevelError
	}
	return slog.LevelWarn
}

var _ core.Provisioner = (*provision.Service)(nil)
