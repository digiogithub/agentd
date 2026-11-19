// Package newsession is the Nueva sesión page (mockup 4): what to open
// (Agente or Solo terminal = the login shell, no agent), host, where it runs
// (agentd, zellij, tmux or a plain SSH shell), project (the host's agentd
// aliases, folders typed in "+ Carpeta…" or picked with the folder browser
// "Explorar…", or prefilled by ?project=), agent grid (missing CLIs offer
// "Instalar…", which opens Preparar host; hidden for Solo terminal) and an
// optional first message; Lanzar calls backend.Launch and opens the session.
// A relay host only starts agentd agent sessions, and only with an admin
// device: no zellij/tmux/SSH, no Solo terminal, no folder browser (they need
// a shell on the host); its agents are not detected, the daemon checks them.
package newsession

import (
	"context"
	"path"
	"slices"
	"strconv"
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
	{"opencode", "opencode"}, {"gemini", "Gemini CLI"}, {"grok", "Grok"},
}

// What to open (first choice of the form).
const (
	OpenAgent = iota
	OpenTerminal
)

var terminalHints = []string{
	"Shell de login en la carpeta del proyecto, gestionado por agentd (buzón, vista y terminal).",
	"Terminal sin agente: abre el shell por defecto de zellij en la carpeta.",
	"Terminal sin agente: abre el shell por defecto de tmux en la carpeta.",
	"Shell de login por SSH en la carpeta: se pierde al desconectar.",
}

// View is the page state (UI thread only).
type View struct {
	a         *app.Context
	hostID    string
	routeHost string
	// routeProject is the ?project= value already applied.
	routeProject string
	// openKind is OpenAgent or OpenTerminal.
	openKind  int
	target    int
	project   string
	agent     string
	extra     []string
	prompt    string
	launching bool

	folderOpen bool
	folder     string
	browse     *browser

	projects map[string][]proto.AliasInfo
	loading  map[string]bool
}

// New returns the page.
func New(a *app.Context) *View {
	v := &View{a: a, agent: "claude", projects: map[string][]proto.AliasInfo{}, loading: map[string]bool{}}
	v.browse = newBrowser(a, v.addPath)
	return v
}

// addPath makes an absolute folder (from the browser) the project.
func (v *View) addPath(p string) {
	if !slices.Contains(v.extra, p) {
		v.extra = append(v.extra, p)
	}
	v.project = p
}

func (v *View) terminal() bool { return v.openKind == OpenTerminal }

// label is the project label agentd and the multiplexers name the session
// with: the alias, else the base name of the directory.
func label(project string) string {
	l := path.Base(strings.TrimRight(project, "/"))
	if l == "." || l == "/" || l == "~" || l == "" {
		return "home"
	}
	return l
}

// dirOf resolves an alias of host to its directory (zellij, tmux and SSH
// take a directory, only agentd resolves aliases itself).
func (v *View) dirOf(hostID, project string) string {
	for _, p := range v.projects[hostID] {
		if p.Name == project && p.Dir != "" {
			return p.Dir
		}
	}
	return project
}

func (v *View) host() (backend.HostStatus, bool) {
	hosts := v.a.Live.Hosts
	for _, h := range hosts {
		if h.Host.ID == v.hostID {
			return h, true
		}
	}
	var online []backend.HostStatus
	for _, h := range v.a.Live.Online() {
		if h.Host.Allows(backend.CapLaunch) {
			online = append(online, h)
		}
	}
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
	if h.State != backend.HostOnline || !h.Host.Allows(backend.CapLaunch) {
		return false
	}
	if h.Host.IsRelay() {
		return target == TargetAgentd
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
	if v.terminal() {
		switch v.target {
		case TargetAgentd, TargetZellij, TargetTmux:
			return "shell@" + label(project)
		}
		return "ssh · terminal en " + project
	}
	switch v.target {
	case TargetAgentd:
		if strings.ContainsAny(project, "/~") {
			project = label(project)
		}
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
	if r != nil && r.Query("project") != v.routeProject {
		v.routeProject = r.Query("project")
		if v.routeProject != "" {
			v.addPath(v.routeProject)
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
	relay := hasHost && h.Host.IsRelay()
	canLaunch := hasHost && h.Host.Allows(backend.CapLaunch)
	if relay && v.terminal() {
		v.openKind = OpenAgent // the daemon refuses a shell command from a relay device
	}
	installed := v.terminal() || relay || caps != nil && caps.Has(v.agent)

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
				kit.SectionLabel(c, "Abrir", t.Muted)
				open := v.openKind
				if kit.Segmented(c.Key("open"), &open, []string{"Agente", "Solo terminal"}, func(i int) bool {
					return i == OpenAgent || !relay
				}) {
					v.openKind = open
				}
				kit.SectionLabel(c, "Host", t.Muted).Margin(8, 0, 0, 0)
				v.hostPicker(c, t, h, hasHost)
				kit.SectionLabel(c, "Dónde corre", t.Muted).Margin(8, 0, 0, 0)
				sel := v.target
				if kit.Segmented(c.Key("target"), &sel, []string{"agentd", "zellij", "tmux", "SSH"}, func(i int) bool {
					return hasHost && available(h, i)
				}) {
					v.target = sel
				}
				hint := targetHints[v.target]
				switch {
				case hasHost && !canLaunch:
					hint = "Este dispositivo no puede lanzar sesiones en " + h.Host.Name + " (permiso " + app.ScopeLabel(h.Host.Relay.Scope) + ")."
				case relay:
					hint = "Por relay: solo sesiones agentd con un agente de los perfiles del host (sin terminal ni carpetas)."
				case v.terminal():
					hint = terminalHints[v.target]
				}
				ui.Text(c, hint).FontSize(12).TextColor(t.Muted).LineHeight(1.4)
				kit.SectionLabel(c, "Proyecto", t.Muted).Margin(8, 0, 0, 0)
				ui.Row(c).Gap(8).GapY(8).Wrap().Children(func() {
					for _, p := range projects {
						if kit.Chip(c.Key("p:"+p), p, p == v.project, true).Clicked() {
							v.project = p
						}
					}
					if v.a.Backend != nil && hasHost && !relay {
						eb := ui.ButtonBase(c.Key("explore")).Label("Explorar carpetas").Height(36).Padding(0, 12).Radius(10).Gap(6).
							Border(1, t.Border).AlignItems(ui.Center).Cursor(ui.CursorPointer).TextColor(t.Muted)
						eb.Children(func() {
							kit.Icon(c.Key("explore"), kit.IconFolder).FontSize(15)
							ui.Text(c, "Explorar…").FontSize(13)
						})
						if eb.Clicked() {
							start := ""
							if v.project != "" {
								start = v.dirOf(h.Host.ID, v.project)
							}
							v.browse.Open(h.Host.ID, start)
						}
					}
					b := ui.ButtonBase(c.Key("folder")).Label("Añadir carpeta").Height(36).Padding(0, 12).Radius(10).
						Border(1, t.Border).AlignItems(ui.Center).Cursor(ui.CursorPointer).TextColor(t.Muted).
						Children(func() { ui.Text(c, "+ Carpeta…").FontSize(13) })
					if b.Clicked() {
						v.folder, v.folderOpen = "", true
					}
				})
				if !v.terminal() {
					kit.SectionLabel(c, "Agente", t.Muted).Margin(8, 0, 0, 0)
					ui.Grid(c).Columns(3).Gap(8).Children(func() {
						for _, ag := range Agents {
							v.agentTile(c.Key("ag:"+ag.ID), t, ag, caps, h, hasHost)
						}
					})
					kit.SectionLabel(c, "Primer mensaje (opcional)", t.Muted).Margin(8, 0, 0, 0)
					kit.Input(c.Key("prompt"), &v.prompt, "Qué debe hacer el agente", false).Label("Primer mensaje")
				}
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
					Disabled: !canLaunch || v.project == "" || !installed || v.launching}).Clicked() &&
					canLaunch && v.project != "" && installed && !v.launching {
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
	v.browse.Build(c)
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
			if m.Item(label).Disabled(hs.State != backend.HostOnline || !hs.Host.Allows(backend.CapLaunch)).Checked(has && hs.Host.ID == h.Host.ID).Chosen() {
				v.hostID = hs.Host.ID
			}
		}
	})
}

func (v *View) agentTile(c *ui.Context, t *kit.Tokens, ag Agent, caps *backend.Capabilities, h backend.HostStatus, has bool) {
	missing := caps == nil || !caps.Has(ag.ID)
	sub := "Instalar…"
	switch {
	case h.Host.IsRelay():
		// No detection over the relay: the host's daemon checks the profile.
		missing, sub = false, "en el host"
	case !missing:
		sub = "instalado"
		if ver := caps.Tools[ag.ID].Version; ver != "" {
			sub += " · " + ver
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
	project := v.project
	if v.target != TargetAgentd {
		project = v.dirOf(h.Host.ID, project)
	}
	spec := backend.LaunchSpec{HostID: h.Host.ID, Where: targetSources[v.target], Project: project, Harness: v.agent,
		FirstPrompt: strings.TrimSpace(v.prompt)}
	if v.terminal() {
		spec.TerminalOnly, spec.Harness, spec.FirstPrompt = true, "", ""
	}
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
			p := app.SessionPath(s)
			if spec.TerminalOnly {
				p += "?tab=terminal"
			}
			a.Router.Replace(p)
		})
	})
}

func itoa(n int) string { return strconv.Itoa(n) }
