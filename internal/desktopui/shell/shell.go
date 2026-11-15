// Package shell bootstraps agentd-desktop on MyGo: the application, the main
// window with native decorations, the application menu and desktop
// notifications. It owns the MyGo event loop, which must run on the main
// goroutine.
package shell

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
)

// ErrNoDisplay is returned by Run when no display server is reachable.
var ErrNoDisplay = errors.New("no display available for the agentd-desktop window")

// Options configures Run.
type Options struct {
	// Name is the application name (menu bar, notifications).
	Name string
	// Title of the main window.
	Title string
	// Width and Height of the main window in DIPs (default 1280x820).
	Width, Height int
	// View builds the window's content every frame.
	View func(c *ui.Context)
	// OnWindow runs on the main thread once the main window exists, with
	// the controller: connect dispatchers here.
	OnWindow func(w *mygo.Window, c *Controller)
	// Ctx, when canceled, quits the app.
	Ctx context.Context
}

// Controller is the window shell handed to the app: focus state and
// notifications.
type Controller struct {
	mu      sync.Mutex
	win     *mygo.Window
	focused atomic.Bool
}

// Focused reports whether the main window has the keyboard focus.
func (c *Controller) Focused() bool { return c.focused.Load() }

// Show brings the window to the front.
func (c *Controller) Show() {
	c.mu.Lock()
	w := c.win
	c.mu.Unlock()
	if w != nil && !w.IsDestroyed() {
		w.Show()
		w.Restore()
		w.Focus()
	}
}

// Notify shows a desktop notification; a click brings the window up and
// runs onClick on the main thread. MyGo notifications have no action
// buttons, so "responder desde el aviso" opens the session instead.
func (c *Controller) Notify(id, title, body string, onClick func()) {
	if !mygo.NotificationsSupported() {
		return
	}
	n := mygo.NewNotification(mygo.NotificationOptions{ID: id, Title: title, Body: body, Group: "agentd"})
	n.OnClick(func() {
		c.Show()
		n.Close()
		if onClick != nil {
			onClick()
		}
	})
	go func() {
		if err := n.Show(); err != nil {
			slog.Debug("desktopui: notification not shown", "error", err)
		}
	}()
}

// Run starts the event loop and shows the main window. It blocks until the
// app quits and must be called from the main goroutine.
func Run(o Options) error {
	if o.View == nil {
		return errors.New("shell: Options.View is required")
	}
	if o.Name == "" {
		o.Name = "agentd"
	}
	if o.Title == "" {
		o.Title = o.Name
	}
	if o.Width <= 0 {
		o.Width = 1280
	}
	if o.Height <= 0 {
		o.Height = 820
	}
	app := mygo.App
	app.SetName(o.Name)
	app.SetMenu(DefaultMenu())
	c := &Controller{}
	app.WhenReady(func() {
		w := mygo.NewWindow(mygo.WindowOptions{
			Title:     o.Title,
			Width:     o.Width,
			Height:    o.Height,
			MinWidth:  720,
			MinHeight: 520,
			StateKey:  "main",
			Content:   ui.View(o.View),
		})
		c.mu.Lock()
		c.win = w
		c.mu.Unlock()
		c.focused.Store(true)
		w.OnFocus(func() { c.focused.Store(true) })
		w.OnBlur(func() { c.focused.Store(false) })
		if o.OnWindow != nil {
			o.OnWindow(w, c)
		}
	})
	app.OnWindowAllClosed(func() { app.Quit() })
	app.OnActivate(func(hasVisibleWindows bool) {
		if !hasVisibleWindows {
			c.Show()
		}
	})
	if o.Ctx != nil {
		stop := context.AfterFunc(o.Ctx, func() { app.Quit() })
		defer stop()
	}
	if err := app.Run(); err != nil {
		if isNoDisplay(err) {
			return fmt.Errorf("%w: %v", ErrNoDisplay, err)
		}
		return err
	}
	return nil
}

func isNoDisplay(err error) bool {
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "cannot open display") || strings.Contains(msg, "no display") ||
		strings.Contains(msg, "failed to init gtk") || strings.Contains(msg, "gtk_init")
}

// DefaultMenu is the application menu: the app menu (macOS), File, the Edit
// role menu (copy and paste keys need it on macOS), View and Window.
func DefaultMenu() *mygo.Menu {
	file := []*mygo.MenuItem{{Role: mygo.RoleClose}}
	if runtime.GOOS != "darwin" {
		file = append(file, &mygo.MenuItem{Role: mygo.RoleQuit})
	}
	items := []*mygo.MenuItem{}
	if runtime.GOOS == "darwin" {
		items = append(items, &mygo.MenuItem{Role: mygo.RoleAppMenu})
	}
	items = append(items,
		&mygo.MenuItem{Label: "Archivo", Submenu: file},
		&mygo.MenuItem{Role: mygo.RoleEditMenu},
		&mygo.MenuItem{Label: "Ver", Submenu: []*mygo.MenuItem{{Role: mygo.RoleToggleFullScreen}}},
		&mygo.MenuItem{Role: mygo.RoleWindowMenu},
	)
	return mygo.NewMenu(items)
}
