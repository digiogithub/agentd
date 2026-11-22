package app

import (
	"fmt"
	"strings"

	"github.com/digiogithub/agentd/internal/desktopui/backend"
	"github.com/digiogithub/agentd/internal/desktopui/kit"
	"github.com/egoist/mygo/ui"
)

// Root is the window's view: the sidebar (the mobile app's bottom bar) and
// the routed area.
type Root struct {
	App   *Context
	views map[string]View
}

// NewRoot returns the root view of the window.
func NewRoot(a *Context) *Root { return &Root{App: a, views: map[string]View{}} }

// View builds the window. Pass it to ui.View.
func (r *Root) View(c *ui.Context) {
	a := r.App
	a.Live.Start()
	t := kit.Apply(c, a.Prefs.Get().Theme)
	a.Toasts.Flush(c)
	all := Areas()
	r.shortcuts(c, all)
	host := r.windowHost()
	ui.Column(c).Fill().AlignItems(ui.Stretch).Background(t.Bg).Children(func() {
		if host != nil {
			r.titleBar(c, t, host)
		}
		ui.Row(c).Grow(1).Shrink(1).MinHeight(0).AlignItems(ui.Stretch).Children(func() {
			r.sidebar(c, t, all)
			ui.Column(c).Grow(1).Shrink(1).MinWidth(0).AlignItems(ui.Stretch).Children(func() {
				a.Router.View(c, func(route *ui.Route) {
					ar, ok := r.match(route, all)
					if !ok {
						kit.EmptyState(c, kit.IconError, "No encontrado", route.Path())
						return
					}
					route.Title(ar.Title)
					ui.Column(c).Grow(1).Shrink(1).MinHeight(0).AlignItems(ui.Stretch).Children(func() {
						r.view(ar).Build(c, route)
					})
				})
			})
		})
	})
	kit.ToastViewport(c)
	a.Views.Sweep()
}

func (r *Root) match(route *ui.Route, all []Area) (Area, bool) {
	for _, ar := range all {
		for _, p := range ar.Patterns {
			if route.Match(p) {
				return ar, true
			}
		}
	}
	return Area{}, false
}

func (r *Root) view(ar Area) View {
	if v, ok := r.views[ar.ID]; ok {
		return v
	}
	v := ar.New(r.App)
	r.views[ar.ID] = v
	return v
}

// AreaView returns the view of area id once built (tests), else nil.
func (r *Root) AreaView(id string) View { return r.views[id] }

// Close stops the views that own background work and the view streams.
func (r *Root) Close() {
	for _, v := range r.views {
		if cl, ok := v.(Closer); ok {
			cl.Close()
		}
	}
	r.App.Views.Close()
}

// active returns the sidebar entry of the current route.
func (r *Root) active(all []Area) string {
	path := r.App.Router.Path()
	best, bestLen := "", -1
	for _, ar := range all {
		match := path == ar.Path || strings.HasPrefix(path, strings.TrimSuffix(ar.Path, "/")+"/")
		if match && len(ar.Path) > bestLen {
			best, bestLen = ar.ID, len(ar.Path)
		}
	}
	if ar, ok := Lookup(best); ok && ar.Parent != "" {
		return ar.Parent
	}
	return best
}

func (r *Root) shortcuts(c *ui.Context, all []Area) {
	keys := []ui.Key{ui.Key1, ui.Key2, ui.Key3, ui.Key4, ui.Key5}
	for _, ar := range all {
		if ar.Shortcut > 0 && ar.Shortcut <= len(keys) {
			path := ar.Path
			c.OnShortcut(ui.Cmd, keys[ar.Shortcut-1], func() { r.App.Navigate(path) })
		}
	}
	c.OnShortcut(ui.Cmd, ui.KeyN, func() { r.App.Navigate("/new") })
}

// NeedsAnswer counts the sessions waiting for the user.
func (l *Live) NeedsAnswer() int {
	n := 0
	for _, s := range l.Sessions {
		if s.Status == "waiting" {
			n++
		}
	}
	return n
}

func (r *Root) sidebar(c *ui.Context, t *kit.Tokens, all []Area) {
	a := r.App
	active := r.active(all)
	ui.Column(c).Width(220).Shrink(0).Background(t.NavBg).BorderWidth(0, 1, 0, 0).BorderColor(t.Divider).
		Padding(16, 10).Gap(4).Children(func() {
		ui.Row(c).Height(36).Gap(10).Padding(0, 8).AlignItems(ui.Center).Margin(0, 0, 12, 0).Children(func() {
			ui.Row(c).Size(26, 26).Radius(8).Background(t.AccentBg).AlignItems(ui.Center).Justify(ui.Center).Children(func() {
				kit.Icon(c, kit.IconAgents).FontSize(15).TextColor(t.Accent)
			})
			ui.Text(c, "agentd").FontSize(17).FontWeight(700).LetterSpacing(-0.3)
		})
		for _, ar := range all {
			if ar.Section != SectionMain {
				continue
			}
			badge := 0
			switch {
			case ar.Badge != nil:
				badge = ar.Badge(a)
			case ar.ID == "agents":
				badge = a.Live.NeedsAnswer()
			}
			r.navItem(c, t, ar, ar.ID == active, badge)
		}
		ui.Column(c).Grow(1)
		online, total := len(a.Live.Online()), len(a.Live.Hosts)
		if a.Live.HostsLoaded {
			ui.Row(c).Padding(8, 10).Gap(8).AlignItems(ui.Center).Children(func() {
				dot := ui.Box(c).Size(8, 8).Radius(4)
				if online > 0 {
					dot.Background(t.Success)
				} else {
					dot.Border(1.5, t.Faint)
				}
				ui.Text(c, fmt.Sprintf("%d de %d hosts en línea", online, total)).FontSize(12).TextColor(t.Muted)
			})
		}
		for _, ar := range all {
			if ar.Section == SectionBottom {
				r.navItem(c, t, ar, ar.ID == active, 0)
			}
		}
	})
}

func (r *Root) navItem(c *ui.Context, t *kit.Tokens, ar Area, active bool, badge int) {
	cc := c.Key("nav:" + ar.ID)
	b := ui.ButtonBase(cc).Label(ar.Title).Height(40).Gap(12).Padding(0, 12).Radius(10).AlignItems(ui.Center).
		Cursor(ui.CursorPointer).FocusRing(true).Selected(active)
	switch {
	case active:
		b.Background(t.AccentBg).TextColor(t.Accent)
	case b.Hovered():
		b.Background(t.Surface).TextColor(t.Text)
	default:
		b.TextColor(t.Muted)
	}
	if ar.Shortcut > 0 {
		b.Tooltip(fmt.Sprintf("%s (Ctrl+%d)", ar.Title, ar.Shortcut))
	}
	b.Children(func() {
		kit.Icon(cc, ar.Icon).FontSize(18)
		txt := ui.Text(cc, ar.Title).FontSize(14).SingleLine().Grow(1)
		if active {
			txt.FontWeight(600)
		}
		if badge > 0 {
			kit.Pill(cc, fmt.Sprint(badge), t.OnWaiting, t.Waiting)
		}
	})
	if b.Clicked() && r.App.Router.Path() != ar.Path {
		r.App.Router.Push(ar.Path)
	}
}

// HostSubtitle is "agentd 0.6.2 · SSH", "SSH · sin agentd", "agentd 0.7.0 ·
// relay · operar" or "Sin conexión".
func HostSubtitle(h backend.HostStatus) string {
	if h.State != backend.HostOnline {
		return "Sin conexión"
	}
	via := "SSH"
	switch {
	case h.Host.Local():
		via = "este equipo"
	case h.Host.IsRelay():
		return strings.TrimSpace("agentd "+h.AgentdVer) + " · relay · " + ScopeLabel(h.Host.Relay.Scope)
	}
	if h.Capabilities != nil && h.Capabilities.Has("agentd") {
		return strings.TrimSpace("agentd "+h.Capabilities.Tools["agentd"].Version) + " · " + via
	}
	if h.AgentdVer != "" {
		return "agentd " + h.AgentdVer + " · " + via
	}
	return via + " · sin agentd"
}

// windowHost returns the shell when the window has no native title bar and
// the shell can drive it, else nil (macOS, tests, no shell yet).
func (r *Root) windowHost() WindowHost {
	if !CustomFrame() {
		return nil
	}
	h, _ := r.App.ShellHost().(WindowHost)
	return h
}

// titleBarHeight is the height of the custom title bar.
const titleBarHeight = 36

// titleBar draws the window's own title bar: the app name, a drag area and
// the minimise, maximise/restore and close buttons flush to the right edge.
func (r *Root) titleBar(c *ui.Context, t *kit.Tokens, h WindowHost) {
	ui.Row(c.Key("titlebar")).Height(titleBarHeight).Shrink(0).AlignItems(ui.Center).
		Background(t.NavBg).BorderWidth(0, 0, 1, 0).BorderColor(t.Divider).DragWindow().Children(func() {
		ui.Row(c).Grow(1).Padding(0, 0, 0, 14).Gap(8).AlignItems(ui.Center).Children(func() {
			kit.Icon(c, kit.IconAgents).FontSize(14).TextColor(t.Accent)
			ui.Text(c, "agentd").FontSize(13).FontWeight(600).TextColor(t.Secondary)
		})
		switch kit.WindowControls(c, kit.WindowControlsOpts{
			Maximized: h.IsMaximized(),
			Minimize:  "Minimizar",
			Maximize:  "Maximizar",
			Restore:   "Restaurar",
			Close:     "Cerrar",
		}) {
		case kit.ControlMinimize:
			h.Minimize()
		case kit.ControlMaximize:
			h.ToggleMaximize()
		case kit.ControlClose:
			h.CloseWindow()
		}
	})
}
