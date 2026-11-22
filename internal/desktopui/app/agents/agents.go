// Package agents is the Agentes area (mockup 1): the inbox of sessions of
// every host grouped Necesitan respuesta / Trabajando / Listos, filter
// chips, search, inline Permitir/Denegar for permission dialogs (act choose
// with the revision and epoch of the latest view) and Nueva sesión. On wide
// windows the open session shows beside the list.
package agents

import (
	"context"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/digiogithub/agentd/internal/desktopui/app"
	"github.com/digiogithub/agentd/internal/desktopui/app/session"
	"github.com/digiogithub/agentd/internal/desktopui/backend"
	"github.com/digiogithub/agentd/internal/desktopui/kit"
	"github.com/digiogithub/agentd/internal/proto"
	"github.com/egoist/mygo/ui"
)

// WideWidth is the window width from which the session shows beside the
// list.
const WideWidth = 1080

func init() {
	app.Register(app.Area{
		ID: "agents", Path: "/agents", Patterns: []string{"/agents", "/agents/s/{host}/{source}/{name}"},
		Title: "Agentes", Icon: kit.IconAgents, Section: app.SectionMain, Order: 0, Shortcut: 1,
		New: func(a *app.Context) app.View { return New(a) },
	})
}

// Filters of the chips.
const (
	FilterAll = iota
	FilterWaiting
	FilterWorking
	FilterDone
)

// View is the area state (UI thread only).
type View struct {
	a         *app.Context
	filter    int
	searching bool
	query     string
	busy      map[string]bool
	detail    *session.View
	// kill is the Terminar confirmation of the list rows.
	kill *app.Terminator
	// sel is the selection mode (several sessions, one prompt).
	sel broadcast
	// aliases of each host (loaded once; grouping resolves labels with them).
	aliases map[string][]proto.AliasInfo
	loading map[string]bool
	// missingKey is the routed session not listed yet (a launch whose
	// session list update is still on its way), since missingSince.
	missingKey   string
	missingSince time.Time
	// Now is the clock of relative times (tests).
	Now func() time.Time
}

// New returns the agents area.
func New(a *app.Context) *View {
	return &View{a: a, busy: map[string]bool{}, Now: time.Now, kill: app.NewTerminator(a),
		aliases: map[string][]proto.AliasInfo{}, loading: map[string]bool{}, sel: broadcast{picked: map[string]bool{}}}
}

// byProject reports whether the list is grouped by project.
func (v *View) byProject() bool { return v.a.Prefs.Get().AgentsGroup != app.GroupState }

func (v *View) loadAliases(hostID string) {
	if _, ok := v.aliases[hostID]; ok || v.loading[hostID] || v.a.Backend == nil {
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
			v.aliases[hostID] = ps
		})
	})
}

// Close stops the open session.
func (v *View) Close() {
	if v.detail != nil {
		v.detail.Close()
		v.detail = nil
	}
}

// Detail returns the open session page (tests).
func (v *View) Detail() *session.View { return v.detail }

// Build shows the inbox, and the session of the route.
func (v *View) Build(c *ui.Context, r *ui.Route) {
	t := kit.Current()
	key := ""
	if r != nil && r.Param("name") != "" {
		key = app.SessionKeyFromRoute(r.Param("host"), r.Param("source"), r.Param("name"))
	}
	w, _ := c.Size()
	wide := w >= WideWidth
	if key == "" && v.detail != nil {
		v.Close()
	}
	tab := ""
	if r != nil {
		tab = r.Query("tab")
	}
	if key != "" && !wide {
		v.detailPane(c, t, key, tab, true)
		return
	}
	ui.Row(c).Grow(1).MinHeight(0).AlignItems(ui.Stretch).Children(func() {
		list := ui.Column(c).MinHeight(0).AlignItems(ui.Stretch)
		if wide {
			list.Width(420).Shrink(0).BorderWidth(0, 1, 0, 0).BorderColor(t.Divider)
		} else {
			list.Grow(1)
		}
		list.Children(func() {
			v.inbox(c, t, key)
			v.broadcastBar(c, t)
			v.kill.Build(c)
		})
		if wide {
			ui.Column(c).Grow(1).Shrink(1).MinWidth(0).AlignItems(ui.Stretch).Children(func() {
				if key == "" {
					kit.EmptyState(c, kit.IconAgents, "Elige una sesión", "Su chat y su terminal se abren aquí.")
					return
				}
				v.detailPane(c, t, key, tab, false)
			})
		}
	})
}

func (v *View) detailPane(c *ui.Context, t *kit.Tokens, key, tab string, back bool) {
	s, ok := v.a.Live.Session(key)
	if ok || v.missingKey != key {
		v.missingKey, v.missingSince = key, v.Now()
	}
	if !ok {
		ui.Column(c).Grow(1).Children(func() {
			if back && kit.RouteHeader(c, "Sesión", "", nil) {
				v.a.Navigate("/agents")
			}
			if v.a.Live.SessionsLoaded && v.Now().Sub(v.missingSince) > 5*time.Second {
				kit.EmptyState(c, kit.IconError, "La sesión ya no existe", key)
			} else {
				kit.EmptyState(c, "", "", "Abriendo la sesión…")
				c.After(time.Second)
			}
		})
		return
	}
	if v.detail == nil || v.detail.Key() != key {
		v.Close()
		v.detail = session.New(v.a, s)
		if tab == "terminal" {
			v.detail.SetTab(session.TabTerminal)
		}
	}
	var backFn func()
	if back {
		backFn = func() { v.a.Navigate("/agents") }
	}
	v.detail.Build(c.Key("detail:"+key), s, backFn)
}

// MaxInlineViews caps the WatchView streams the list opens for inline
// approvals (one per waiting agentd session, in list order); further
// waiting sessions show no inline options until one is answered.
const MaxInlineViews = 12

type entry struct {
	s    backend.Session
	view *proto.View
	g    app.Group
}

func (v *View) inbox(c *ui.Context, t *kit.Tokens, open string) {
	a := v.a
	var visible []entry
	q := strings.ToLower(strings.TrimSpace(v.query))
	streams := 0
	for _, s := range a.Live.Sessions {
		if q != "" && !strings.Contains(strings.ToLower(app.SessionTitle(s)), q) {
			continue
		}
		var view *proto.View
		if s.Source == backend.SourceAgentd && s.Status == "waiting" && streams < MaxInlineViews {
			streams++
			view, _ = a.Views.Get(s) // the options of the approval card
		}
		visible = append(visible, entry{s: s, view: view, g: app.GroupOf(s, view)})
	}
	sortEntries(visible) // alphabetical: a refresh never reorders the list
	count := func(g app.Group) int {
		n := 0
		for _, e := range visible {
			if e.g == g {
				n++
			}
		}
		return n
	}
	chips := []string{
		"Todos · " + itoa(len(visible)),
		"Esperan · " + itoa(count(app.GroupNeedsAnswer)),
		"Trabajando · " + itoa(count(app.GroupWorking)),
		"Listos · " + itoa(count(app.GroupDone)),
	}
	show := func(g app.Group) bool {
		switch v.filter {
		case FilterWaiting:
			return g == app.GroupNeedsAnswer
		case FilterWorking:
			return g == app.GroupWorking
		case FilterDone:
			return g == app.GroupDone
		}
		return true
	}
	var shown []entry
	for _, e := range visible {
		if show(e.g) {
			shown = append(shown, e)
		}
	}
	overline := app.Plural(len(a.Live.Online()), "host", "hosts") + " · " + app.Plural(len(a.Live.Sessions), "sesión", "sesiones")
	kit.ScreenHeader(c, overline, "Agentes", func() {
		icon, label := kit.IconSearch, "Buscar"
		if v.searching {
			icon, label = kit.IconClose, "Cerrar búsqueda"
		}
		if kit.IconButton(c.Key("search"), icon, label).Clicked() {
			v.searching = !v.searching
			if !v.searching {
				v.query = ""
			}
		}
		selIcon, selLabel := kit.IconPanel, "Seleccionar varios"
		if v.sel.on {
			selIcon, selLabel = kit.IconClose, "Salir de la selección"
		}
		if kit.IconButton(c.Key("select"), selIcon, selLabel).Clicked() {
			v.toggleSelecting()
		}
		if kit.Button(c.Key("new"), kit.ButtonOpts{Variant: kit.Primary, Icon: kit.IconPlus, Label: "Nueva sesión", Height: 36,
			Round: true, Tooltip: "Nueva sesión (Ctrl+N)"}).Clicked() {
			a.Navigate("/new")
		}
	}, func() {
		if v.searching {
			kit.Input(c.Key("query"), &v.query, "Buscar sesión", false).Label("Buscar sesión").AutoFocus()
		}
		sel := 0
		if !v.byProject() {
			sel = 1
		}
		if kit.Segmented(c.Key("group"), &sel, []string{"Proyecto", "Estado"}, nil) {
			mode := app.GroupProject
			if sel == 1 {
				mode = app.GroupState
			}
			_ = a.Prefs.Update(func(s *app.Settings) { s.AgentsGroup = mode })
		}
		ui.Row(c).Gap(8).Wrap().GapY(8).Children(func() {
			for i, label := range chips {
				if kit.Chip(c.Key(i), label, v.filter == i, false).Clicked() {
					v.filter = i
				}
			}
		})
		if v.sel.on {
			v.pickBar(c, t, shown)
		}
	})
	if len(shown) == 0 {
		msg := "No hay sesiones en este filtro. Lanza una con «Nueva sesión»."
		switch {
		case !a.Live.SessionsLoaded:
			msg = "Cargando…"
		case q != "":
			msg = "Ninguna sesión coincide"
		}
		kit.EmptyState(c, "", "", msg)
		return
	}
	if v.byProject() {
		v.projects(c, t, visible, show, open)
		return
	}
	ui.Scroll(c.Key("list")).Grow(1).MinHeight(0).Padding(4, 16, 24, 16).Gap(10).Children(func() {
		section := func(g app.Group, title string, color ui.Color) {
			if !show(g) || count(g) == 0 {
				return
			}
			kit.SectionLabel(c.Key("sec:"+title), title+" · "+itoa(count(g)), color).Padding(8, 4, 0, 4)
			for _, e := range visible {
				if e.g == g {
					v.card(c.Key("card:"+e.s.Key()), t, e, e.s.Key() == open)
				}
			}
		}
		section(app.GroupNeedsAnswer, "Necesitan respuesta", t.Waiting)
		section(app.GroupWorking, "Trabajando", t.Working)
		section(app.GroupDone, "Listos", t.Muted)
	})
}

// projects renders the Proyecto grouping.
func (v *View) projects(c *ui.Context, t *kit.Tokens, visible []entry, show func(app.Group) bool, open string) {
	a := v.a
	hosts := map[string]hostInfo{}
	for _, h := range a.Live.Hosts {
		hi := hostInfo{}
		if h.Capabilities != nil {
			hi.home = h.Capabilities.Home
		}
		hosts[h.Host.ID] = hi
	}
	var shown []entry
	for _, e := range visible {
		if show(e.g) {
			shown = append(shown, e)
			hi := hosts[e.s.HostID]
			hi.aliases = v.aliases[e.s.HostID]
			hosts[e.s.HostID] = hi
			v.loadAliases(e.s.HostID)
		}
	}
	groups := buildGroups(shown, hosts)
	multiHost := len(a.Live.Hosts) > 1
	ui.Scroll(c.Key("list")).Grow(1).MinHeight(0).Padding(4, 16, 24, 16).Gap(10).Children(func() {
		for _, g := range groups {
			gc := c.Key("grp:" + g.key)
			ui.Row(gc).Gap(8).AlignItems(ui.Center).Padding(8, 4, 0, 4).Children(func() {
				ui.Column(gc).Grow(1).Shrink(1).MinWidth(0).Gap(2).Children(func() {
					ui.Row(gc).Gap(8).AlignItems(ui.Center).Children(func() {
						kit.Icon(gc, kit.IconFolder).FontSize(14).TextColor(t.Muted)
						ui.Text(gc, g.label).FontSize(14).FontWeight(600).SingleLine().Ellipsis("…")
						if g.waiting > 0 {
							kit.Pill(gc, itoa(g.waiting), t.OnWaiting, t.Waiting)
						}
					})
					sub := app.Plural(len(g.entries), "sesión", "sesiones")
					if multiHost {
						sub = a.Live.HostName(g.host) + " · " + sub
					}
					ui.Text(gc, sub).FontSize(12).TextColor(t.Muted).SingleLine().Ellipsis("…")
				})
				if a.Live.Allows(g.host, backend.CapLaunch) && kit.IconButton(gc.Key("add"), kit.IconPlus, "Nueva sesión en "+g.label).Clicked() {
					p := "/new/" + url.PathEscape(g.host)
					if g.newArg != "" {
						p += "?project=" + url.QueryEscape(g.newArg)
					}
					a.Navigate(p)
				}
			})
			for _, e := range g.entries {
				v.card(c.Key("card:"+e.s.Key()), t, e, e.s.Key() == open)
			}
		}
	})
}

func itoa(n int) string { return strconv.Itoa(n) }

func (v *View) subtitle(s backend.Session) string {
	parts := []string{}
	if h := v.a.Live.HostName(s.HostID); h != "" {
		parts = append(parts, h)
	}
	parts = append(parts, app.SourceLabel(s.Source))
	if rt := app.RelativeTime(s.Updated, v.Now()); rt != "" {
		parts = append(parts, rt)
	}
	return strings.Join(parts, " · ")
}

func (v *View) open(s backend.Session) { v.a.Navigate(app.SessionPath(s)) }

// killButton is the row action Terminar (hidden when the host's relay
// device may not kill).
func (v *View) killButton(c *ui.Context, s backend.Session) {
	if !v.a.Live.Allows(s.HostID, backend.CapLaunch) {
		return
	}
	if kit.Button(c.Key("kill"), kit.ButtonOpts{Variant: kit.Ghost, Icon: kit.IconTrash, Height: 32, Round: true,
		Tooltip: "Terminar sesión", Aria: "Terminar " + s.Name}).Clicked() {
		v.kill.Ask(s)
	}
}

func (v *View) head(c *ui.Context, t *kit.Tokens, s backend.Session, sub, pill string, avFg, avBg ui.Color) {
	ui.Row(c).Gap(10).AlignItems(ui.Center).Children(func() {
		kit.Avatar(c, app.AvatarLetter(s), "", avFg, avBg)
		ui.Column(c).Grow(1).Shrink(1).MinWidth(0).Gap(2).Children(func() {
			ui.Text(c, app.SessionTitle(s)).FontSize(15).FontWeight(600).SingleLine().Ellipsis("…")
			ui.Text(c, sub).FontSize(12).TextColor(t.Muted).SingleLine().Ellipsis("…")
		})
		if pill != "" {
			kit.Pill(c, pill, t.OnWaiting, t.Waiting)
		}
		v.killButton(c, s)
	})
}

func (v *View) card(c *ui.Context, t *kit.Tokens, e entry, selected bool) {
	if v.sel.on {
		v.pickRow(c, t, e)
		return
	}
	s, view := e.s, e.view
	border := t.Border
	if selected {
		border = t.Accent
	}
	if e.g == app.GroupNeedsAnswer && app.IsPermission(view) && v.a.Live.Allows(s.HostID, backend.CapOperate) {
		if border == t.Border {
			border = t.WaitingBorder
		}
		kit.Card(c, border, func() {
			v.head(c, t, s, v.subtitle(s), "Permiso", t.Waiting, t.WaitingBg)
			kit.CodeBox(c, view.Dialog.Question)
			ui.Row(c).Gap(8).Children(func() {
				busy := v.busy[s.Key()]
				if kit.Button(c.Key("allow"), kit.ButtonOpts{Variant: kit.Light, Label: "Permitir", Grow: true, Height: 44, Disabled: busy}).Clicked() && !busy {
					v.answer(s, view, app.AllowOption(view))
				}
				if kit.Button(c.Key("deny"), kit.ButtonOpts{Variant: kit.Outline, Label: "Denegar", Grow: true, Height: 44, Disabled: busy}).Clicked() && !busy {
					v.answer(s, view, app.DenyOption(view))
				}
				if kit.Button(c.Key("open"), kit.ButtonOpts{Variant: kit.Outline, Icon: kit.IconChevronR, Tooltip: "Abrir sesión", Height: 44}).Clicked() {
					v.open(s)
				}
			})
		})
		return
	}
	if e.g == app.GroupNeedsAnswer {
		var b ui.Element
		ui.Row(c).Gap(6).AlignItems(ui.Center).Children(func() {
			b = ui.ButtonBase(c).Label(app.SessionTitle(s)+": pregunta").Column().Gap(8).Padding(14).Radius(18).Grow(1).Shrink(1).MinWidth(0).
				Background(t.Surface).Border(1, border).AlignItems(ui.Stretch).Cursor(ui.CursorPointer)
			if b.Hovered() {
				b.Background(t.Raised)
			}
			b.Children(func() {
				ui.Row(c).Gap(10).AlignItems(ui.Center).Children(func() {
					kit.Avatar(c, app.AvatarLetter(s), "", t.Accent, t.AccentBg)
					ui.Column(c).Grow(1).Shrink(1).MinWidth(0).Gap(2).Children(func() {
						ui.Text(c, app.SessionTitle(s)).FontSize(15).FontWeight(600).SingleLine().Ellipsis("…")
						ui.Text(c, v.subtitle(s)).FontSize(12).TextColor(t.Muted).SingleLine().Ellipsis("…")
					})
					kit.Pill(c, "Pregunta", t.OnWaiting, t.Waiting)
				})
				q := s.Preview
				n := 0
				if view != nil {
					n = len(view.Options)
					if view.Dialog != nil && view.Dialog.Question != "" {
						q = view.Dialog.Question
					}
				}
				if q == "" {
					q = "Esperando una respuesta"
				}
				ui.Text(c, q).FontSize(14).TextColor(t.Secondary).LineHeight(1.45)
				action := "haz clic para responder"
				if !v.a.Live.Allows(s.HostID, backend.CapOperate) {
					action = "solo lectura en este dispositivo"
				}
				hint := strings.ToUpper(action[:1]) + action[1:]
				if n > 0 {
					hint = app.Plural(n, "opción", "opciones") + " · " + action
				}
				ui.Text(c, hint).FontSize(12).TextColor(t.Muted)
			})
			v.killButton(c, s)
		})
		if b.Clicked() {
			v.open(s)
		}
		return
	}
	v.row(c, t, s, border)
}

func (v *View) row(c *ui.Context, t *kit.Tokens, s backend.Session, border ui.Color) {
	mux := s.Source != backend.SourceAgentd
	var sub string
	switch {
	case mux:
		parts := []string{}
		if h := v.a.Live.HostName(s.HostID); h != "" {
			parts = append(parts, h)
		}
		if s.HostID != backend.LocalHostID {
			parts = append(parts, "SSH")
		}
		if s.Detail != "" {
			parts = append(parts, s.Detail)
		}
		sub = strings.Join(parts, " · ")
	case s.Status == "starting":
		sub = "Arrancando…"
	case s.Status == "working":
		sub = s.Preview
		if sub == "" {
			sub = "Trabajando…"
		}
	case s.Status == "exited":
		sub = "Sesión terminada"
		if s.Preview != "" {
			sub += " · «" + s.Preview + "»"
		}
	default:
		sub = "Turno terminado"
		if s.Preview != "" {
			sub += " · «" + s.Preview + "»"
		}
	}
	var b ui.Element
	ui.Row(c).Gap(6).AlignItems(ui.Center).Children(func() {
		b = ui.ButtonBase(c).Label(app.SessionTitle(s)+": "+kit.StatusLabel(s.Status)).Padding(12, 14).Gap(10).Radius(18).
			Grow(1).Shrink(1).MinWidth(0).Background(t.Surface).Border(1, border).AlignItems(ui.Center).Cursor(ui.CursorPointer)
		if b.Hovered() {
			b.Background(t.Raised)
		}
		b.Children(func() {
			switch {
			case mux:
				kit.Avatar(c, "", kit.IconTerminal, t.Secondary, t.Raised)
			case s.Status == "working" || s.Status == "starting":
				kit.Avatar(c, app.AvatarLetter(s), "", t.Working, t.WorkingBg)
			default:
				kit.Avatar(c, app.AvatarLetter(s), "", t.Muted, t.Raised)
			}
			ui.Column(c).Grow(1).Shrink(1).MinWidth(0).Gap(3).Children(func() {
				ui.Text(c, app.SessionTitle(s)).FontSize(15).FontWeight(600).SingleLine().Ellipsis("…")
				st := ui.Text(c, sub).FontSize(12).TextColor(t.Muted).SingleLine().Ellipsis("…")
				if s.Status == "working" && !mux {
					st.Font(kit.MonoFamily())
				}
			})
			switch {
			case s.Status == "working" || s.Status == "starting":
				label := app.ShortDuration(s.Updated, v.Now())
				if mux {
					label = "activo"
				}
				ui.Text(c, label).FontSize(12).TextColor(t.Working).SingleLine()
			case s.Attention:
				ui.Box(c).Size(8, 8).Radius(4).Background(t.Accent).Label("Sin ver")
			}
		})
		v.killButton(c, s)
	})
	if b.Clicked() {
		v.open(s)
	}
}

// answer sends Permitir/Denegar: act choose with the option, the revision
// and the epoch of the latest view.
func (v *View) answer(s backend.Session, view *proto.View, o *proto.ViewOption) {
	if o == nil || view == nil {
		return
	}
	if view.Revision == 0 || view.Epoch == "" {
		v.a.Toasts.Info("Sin revisión del menú: abre la sesión para responder")
		return
	}
	key := s.Key()
	v.busy[key] = true
	session.Choose(v.a, s, view, *o, func(err error) {
		delete(v.busy, key)
		if err != nil {
			v.a.Toasts.Error("No se pudo responder: el menú cambió o la sesión no está lista", app.ErrorText(err))
		}
	})
}
