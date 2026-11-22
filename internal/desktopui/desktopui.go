// Package desktopui is the entry point of agentd-desktop, the native desktop
// client on MyGo: Run builds the app context on a backend.Backend, the root
// view with every registered area, and runs the window on the main
// goroutine.
//
// Layout of the tree:
//
//	backend/      contract with the hosts (+ backend/fake, mockup data)
//	kit/          palette, fonts, icons, base components
//	shell/        MyGo app, window, menu, notifications
//	app/          Context, area registry, router, layout, notifications
//	app/<area>/   agents, session, newsession, hosts, addhost, provision,
//	              mailbox, settings
//	natives/      libghostty-vt embedded per platform
package desktopui

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"

	"github.com/digiogithub/agentd/internal/desktopui/app"
	"github.com/digiogithub/agentd/internal/desktopui/backend"
	"github.com/digiogithub/agentd/internal/desktopui/kit"
	"github.com/digiogithub/agentd/internal/desktopui/natives"
	"github.com/digiogithub/agentd/internal/desktopui/shell"
	"github.com/egoist/mygo"

	// Areas register themselves; add each new area package here.
	_ "github.com/digiogithub/agentd/internal/desktopui/app/addhost"
	_ "github.com/digiogithub/agentd/internal/desktopui/app/agents"
	_ "github.com/digiogithub/agentd/internal/desktopui/app/hosts"
	_ "github.com/digiogithub/agentd/internal/desktopui/app/mailbox"
	_ "github.com/digiogithub/agentd/internal/desktopui/app/newsession"
	_ "github.com/digiogithub/agentd/internal/desktopui/app/panel"
	_ "github.com/digiogithub/agentd/internal/desktopui/app/provision"
	_ "github.com/digiogithub/agentd/internal/desktopui/app/settings"
)

// ErrNoDisplay is returned by Run when no display server is reachable.
var ErrNoDisplay = shell.ErrNoDisplay

// Options configures Run.
type Options struct {
	// Backend reaches the hosts (required).
	Backend backend.Backend
	// Ctx ends the app when canceled.
	Ctx context.Context
	// PrefsPath is the settings file; "" uses app.DefaultPrefsPath(), "-"
	// keeps the settings in memory.
	PrefsPath string
	// Path is the route opened first (default /agents; AGENTD_DESKTOP_PATH).
	Path string
	// Seed changes the settings once at start (the fake mode seeds a muted
	// session, as the mockup shows).
	Seed func(s *app.Settings)
	// Logger receives UI logs (default slog.Default()).
	Logger *slog.Logger
}

// Run shows the window and blocks until it quits. It must be called from
// the main goroutine.
func Run(o Options) error {
	if o.Backend == nil {
		return errors.New("desktopui: Options.Backend is required")
	}
	if o.Ctx == nil {
		o.Ctx = context.Background()
	}
	ctx, cancel := context.WithCancel(o.Ctx)
	defer cancel()
	path := o.PrefsPath
	switch path {
	case "":
		path = app.DefaultPrefsPath()
	case "-":
		path = ""
	}
	prefs := app.NewPrefs(path)
	if o.Seed != nil {
		_ = prefs.Update(o.Seed)
	}
	a := app.NewContext(ctx, o.Backend, prefs)
	if o.Logger != nil {
		a.Log = o.Logger.With("component", "desktopui")
	}
	if err := kit.RegisterFonts(); err != nil {
		a.Log.Warn("embedded fonts not registered; using the system's", "error", err)
	}
	// libghostty-vt travels inside the binary: extract it before the
	// terminal first loads (MyGo reads $MYGO_GHOSTTY_VT then).
	if p, err := natives.Install(); err != nil {
		a.Log.Warn("embedded libghostty-vt not installed; terminal falls back to MyGo's lookup", "error", err)
	} else if p != "" {
		a.Log.Debug("libghostty-vt extracted", "path", p)
	}
	if o.Path == "" {
		o.Path = os.Getenv("AGENTD_DESKTOP_PATH")
	}
	if o.Path != "" {
		a.Router.Replace(o.Path)
	}
	app.StartNotifier(a)
	root := app.NewRoot(a)
	defer root.Close()

	w, h := devWindowSize()
	err := shell.Run(shell.Options{
		Name:   "agentd",
		Title:  "agentd",
		Width:  w,
		Height: h,
		Ctx:    ctx,
		View:   root.View,
		OnWindow: func(win *mygo.Window, ctl *shell.Controller) {
			a.SetDispatcher(win)
			a.SetShell(ctl)
			startCapture(win, func(p string) { a.Router.Push(p) })
		},
	})
	if errors.Is(err, context.Canceled) {
		return nil
	}
	return err
}

// devWindowSize reads AGENTD_DESKTOP_SIZE=<w>x<h> (development aid).
func devWindowSize() (w, h int) {
	if _, err := fmt.Sscanf(os.Getenv("AGENTD_DESKTOP_SIZE"), "%dx%d", &w, &h); err != nil || w < 0 || h < 0 {
		return 0, 0
	}
	return w, h
}
