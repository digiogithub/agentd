// Package newsession is the Nueva sesión page (mockup 4): host, where it
// runs (agentd, zellij, tmux or a plain SSH shell), project (the host's
// agentd aliases plus folders typed in "+ Carpeta…"), agent grid (missing
// CLIs offer "Instalar…", which opens Preparar host) and an optional first
// message; Lanzar calls backend.Launch and opens the session.
package newsession

import (
	"context"
	"slices"
	"strings"

	"github.com/digiogithub/agentd/internal/desktopui/app"
	"github.com/digiogithub/agentd/internal/desktopui/backend"
	"github.com/digiogithub/agentd/internal/desktopui/kit"
	"github.com/digiogithub/agentd/internal/proto"
	"github.com/egoist/mygo/ui"
)

func init() {
	app.Register(app.Area{
		ID: "newsession", Path: "/new", Patterns: []string{"/new", "/new/{host}"}, Title: "Nueva sesión",
		Icon: kit.IconPlus, Section: app.SectionHidden, Parent: "agents",
		New: func(a *app.Context) app.View { return New(a) },
	})
}

// Targets, in the order of the segmented control.
const (
	TargetAgentd = iota
	TargetZellij
	TargetTmux
	TargetShell
)

var targetSources = []backend.Source{backend.SourceAgentd, backend.SourceZellij, backend.SourceTmux, backend.SourceShell}

var targetHints = []string{
	"Recomendado: vista chat, aprobaciones con revisión, buzón entre agentes y estado fiable.",
	"Pane nuevo en la sesión zellij del host. Terminal completa; el chat se deduce de la pantalla.",
	"Ventana nueva en tmux. Terminal completa; el chat se deduce de la pantalla.",
	"Shell SSH sin multiplexor: se pierde al desconectar.",
}

// Agent is an entry of the agent grid.
type Agent struct{ ID, Label string }

// Agents of the grid.
var Agents = []Agent{
	{"claude", "Claude Code"}, {"codex", "Codex"}, {"pando", "Pando"},
	{"opencode", "opencode"}, {"gemini", "Gemini CLI"}, {"bash", "Shell"},
}

// View is the page state (UI thread only).
type View struct {
	a         *app.Context
	hostID    string
	routeHost string
	target    int
	project   string
	agent     string
	extra     []string
	prompt    string
	launching bool

	folderOpen bool
	folder     string

	projects map[string][]proto.AliasInfo
	loading  map[string]bool
}

// New returns the page.
func New(a *app.Context) *View {
	return &View{a: a, agent: "claude", projects: map[string][]proto.AliasInfo{}, loading: map[string]bool{}}
}

func (v *View) host() (backend.HostStatus, bool) {
	hosts := v.a.Live.Hosts
	for _, h := range hosts {
		if h.Host.ID == v.hostID {
			return h, true
		}
	}
	online := v.a.Live.Online()
	for _, h := range online {
		if h.Capabilities != nil && h.Capabilities.Has("agentd") {
			return h, true
		}
	}
	if len(online) > 0 {
		return online[0], true
	}
	if len(hosts) > 0 {
		return hosts[0], true
	}
	return backend.HostStatus{}, false
}

func available(h backend.HostStatus, target int) bool {
	if h.State != backend.HostOnline {
		return false
	}
	caps := h.Capabilities
	if caps == nil {
		return target == TargetShell
	}
	switch target {
	case TargetAgentd:
		return caps.Has("agentd")
	case TargetZellij:
		return caps.Has("zellij")
	case TargetTmux:
		return caps.Has("tmux")
	}
	return true
}

func (v *View) loadProjects(hostID string) {
	if _, ok := v.projects[hostID]; ok || v.loading[hostID] || v.a.Backend == nil {
		return
	}
	v.loading[hostID] = true
	a := v.a
	a.Go(func(ctx context.Context) {
		ps, err := a.Backend.Projects(ctx, hostID)
		a.Update(func() {
			delete(v.loading, hostID)
			if err != nil {
				ps = nil
			}
			v.projects[hostID] = ps
		})
	})
}

func (v *View) name(project string) string {
	switch v.target {
	case TargetAgentd:
		return v.agent + "@" + project
	case TargetShell:
		return "ssh · " + v.agent + " en " + project
	}
	return string(targetSources[v.target]) + " · " + v.agent + " en " + project
}

// Build shows the form.
func (v *View) Build(c *ui.Context, r *ui.Route) {
	t := kit.Current()
	if r != nil && r.Param("host") != v.routeHost {
		v.routeHost = r.Param("host")
		if v.routeHost != "" {
			v.hostID = v.routeHost
		}
	}
	h, hasHost := v.host()
	if hasHost {
		v.loadProjects(h.Host.ID)
	}
	if hasHost && !available(h, v.target) {
		for i := range targetSources {
			if available(h, i) {
				v.target = i
				break
			}
		}
	}
	var projects []string
	if hasHost {
		for _, p := range v.projects[h.Host.ID] {
			projects = append(projects, p.Name)
		}
		for _, s := range v.a.Live.Sessions {
			if s.HostID == h.Host.ID && s.Project != "" && !slices.Contains(projects, s.Project) {
				projects = append(projects, s.Project)
			}
		}
	}
	for _, p := range v.extra {
		if !slices.Contains(projects, p) {
			projects = append(projects, p)
		}
	}
	if v.project == "" || !slices.Contains(projects, v.project) {
		v.project = ""
		if len(projects) > 0 {
			v.project = projects[0]
		}
	}
	var caps *backend.Capabilities
	if hasHost {
		caps = h.Capabilities
	}
	installed := v.agent == "bash" || caps != nil && caps.Has(v.agent)

	ui.Column(c).Grow(1).MinHeight(0).AlignItems(ui.Stretch).Children(func() {
		if kit.RouteHeader(c, "Nueva sesión", "", nil) {
			if v.a.Router.CanGoBack() {
				v.a.Router.Back()
			} else {
				v.a.Navigate("/agents")
			}
		}
		ui.Scroll(c.Key("form")).Grow(1).MinHeight(0).Padding(16, 24).Children(func() {
			ui.Column(c).Gap(10).MaxWidth(720).FillWidth().Children(func() {
				kit.SectionLabel(c, "Host", t.Muted)
				v.hostPicker(c, t, h, hasHost)
				kit.SectionLabel(c, "Dónde corre", t.Muted).Margin(8, 0, 0, 0)
				sel := v.target
				if kit.Segmented(c.Key("target"), &sel, []string{"agentd", "zellij", "tmux", "SSH"}, func(i int) bool {
					return hasHost && available(h, i)
				}) {
					v.target = sel
				}
				ui.Text(c, targetHints[v.target]).FontSize(12).TextColor(t.Muted).LineHeight(1.4)
				kit.SectionLabel(c, "Proyecto", t.Muted).Margin(8, 0, 0, 0)
				ui.Row(c).Gap(8).GapY(8).Wrap().Children(func() {
					for _, p := range projects {
						if kit.Chip(c.Key("p:"+p), p, p == v.project, true).Clicked() {
							v.project = p
						}
					}
					b := ui.ButtonBase(c.Key("folder")).Label("Añadir carpeta").Height(36).Padding(0, 12).Radius(10).
						Border(1, t.Border).AlignItems(ui.Center).Cursor(ui.CursorPointer).TextColor(t.Muted).
						Children(func() { ui.Text(c, "+ Carpeta…").FontSize(13) })
					if b.Clicked() {
						v.folder, v.folderOpen = "", true
					}
				})
				kit.SectionLabel(c, "Agente", t.Muted).Margin(8, 0, 0, 0)
				ui.Grid(c).Columns(3).Gap(8).Children(func() {
					for _, ag := range Agents {
						v.agentTile(c.Key("ag:"+ag.ID), t, ag, caps, h, hasHost)
					}
				})
				kit.SectionLabel(c, "Primer mensaje (opcional)", t.Muted).Margin(8, 0, 0, 0)
				kit.Input(c.Key("prompt"), &v.prompt, "Qué debe hacer el agente", false).Label("Primer mensaje")
			})
		})
		ui.Column(c).Shrink(0).Padding(12, 24, 18, 24).Gap(8).BorderWidth(1, 0, 0, 0).BorderColor(t.Divider).Children(func() {
			ui.Column(c).MaxWidth(720).FillWidth().Gap(8).Children(func() {
				if v.project != "" {
					ui.Row(c).Gap(4).Children(func() {
						ui.Text(c, "Se creará").FontSize(12.5).TextColor(t.Muted)
						ui.Text(c, v.name(v.project)).Font(kit.MonoFamily()).FontSize(12.5).TextColor(t.AccentLight)
					})
				}
				label := "Lanzar"
				if v.launching {
					label = "Lanzando…"
				}
				if kit.Button(c.Key("launch"), kit.ButtonOpts{Variant: kit.Primary, Label: label, Height: 52, FontSize: 15,
					Disabled: !hasHost || v.project == "" || !installed || v.launching}).Clicked() &&
					hasHost && v.project != "" && installed && !v.launching {
					v.launch(h)
				}
			})
		})
	})
	open := v.folderOpen
	kit.Dialog(c.Key("folder-dialog"), &open, kit.DialogOpts{Title: "Carpeta o alias", Width: 420,
		Body: func() {
			in := kit.Input(c.Key("folder-in"), &v.folder, "~/src/proyecto", true).AutoFocus()
			if in.Submitted() {
				v.addFolder()
				open = false
			}
		},
		Footer: func() {
			if kit.Button(c.Key("fcancel"), kit.ButtonOpts{Variant: kit.Outline, Label: "Cancelar", Height: 36}).Clicked() {
				open = false
			}
			if kit.Button(c.Key("fadd"), kit.ButtonOpts{Variant: kit.Primary, Label: "Añadir", Height: 36}).Clicked() {
				v.addFolder()
				open = false
			}
		}})
	v.folderOpen = open
}

func (v *View) addFolder() {
	p := strings.TrimSpace(v.folder)
	if p == "" {
		return
	}
	if !slices.Contains(v.extra, p) {
		v.extra = append(v.extra, p)
	}
	v.project = p
}

func (v *View) hostPicker(c *ui.Context, t *kit.Tokens, h backend.HostStatus, has bool) {
	name := "Sin hosts"
	if has {
		name = h.Host.Name
	}
	b := ui.ButtonBase(c.Key("host")).Label("Host: "+name).Height(52).Padding(0, 14).Gap(10).Radius(14).Background(t.Surface).
		Border(1, t.Border).AlignItems(ui.Center).Cursor(ui.CursorPointer)
	b.Children(func() {
		dot := ui.Box(c).Size(8, 8).Radius(4)
		if has && h.State == backend.HostOnline {
			dot.Background(t.Success)
		} else {
			dot.Border(1.5, t.Faint)
		}
		ui.Text(c, name).FontSize(15).FontWeight(600)
		sub := ""
		if has {
			sub = app.HostSubtitle(h)
		}
		ui.Text(c, sub).FontSize(12).TextColor(t.Muted).Grow(1).SingleLine().Ellipsis("…")
		kit.Icon(c, kit.IconChevronDown).FontSize(16).TextColor(t.Muted)
	})
	b.Menu(func(m *ui.Menu) {
		for _, hs := range v.a.Live.Hosts {
			label := hs.Host.Name + " — " + app.HostSubtitle(hs)
			if m.Item(label).Disabled(hs.State != backend.HostOnline).Checked(has && hs.Host.ID == h.Host.ID).Chosen() {
				v.hostID = hs.Host.ID
			}
		}
	})
}

func (v *View) agentTile(c *ui.Context, t *kit.Tokens, ag Agent, caps *backend.Capabilities, h backend.HostStatus, has bool) {
	shell := ag.ID == "bash"
	missing := !shell && (caps == nil || !caps.Has(ag.ID))
	sub := "bash"
	if !shell {
		if missing {
			sub = "Instalar…"
		} else {
			sub = "instalado"
			if ver := caps.Tools[ag.ID].Version; ver != "" {
				sub += " · " + ver
			}
		}
	}
	tile := kit.SelectTile(c, ag.Label+", "+sub, v.agent == ag.ID, false, func() {
		ui.Text(c, ag.Label).FontSize(14).FontWeight(600).SingleLine().Ellipsis("…")
		col := t.Muted
		if missing {
			col = t.Waiting
		}
		ui.Text(c, sub).FontSize(12).TextColor(col).SingleLine().Ellipsis("…")
	})
	if tile.Clicked() {
		if missing && has {
			v.a.Navigate("/hosts/provision/" + h.Host.ID + "?cli=" + ag.ID)
			return
		}
		v.agent = ag.ID
	}
}

func (v *View) launch(h backend.HostStatus) {
	spec := backend.LaunchSpec{HostID: h.Host.ID, Where: targetSources[v.target], Project: v.project, Harness: v.agent,
		FirstPrompt: strings.TrimSpace(v.prompt)}
	v.launching = true
	a := v.a
	a.Go(func(ctx context.Context) {
		s, err := a.Backend.Launch(ctx, spec)
		a.Update(func() {
			v.launching = false
			if err != nil {
				a.Toasts.Error("No se pudo lanzar la sesión", app.ErrorText(err))
				return
			}
			v.prompt = ""
			a.Router.Replace(app.SessionPath(s))
		})
	})
}
