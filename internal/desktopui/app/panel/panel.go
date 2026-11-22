// Package panel is the Panel area (AGED-US-0045, plan
// docs/plans/agentd/dashboard_plan.md): one screen with what every agent of
// every host is doing and has pending, and what needs the human: open
// questions (mailbox), terminal menus of waiting sessions and blocked
// tasks. The human answers from here (reply, act prompt, act choose with
// the view's revision and epoch, a message to the agent's mailbox) without
// opening the session. Two views behind a toggle saved in settings.json
// (also /panel?view=tablero|agente):
// "Por agente" (default: summary tiles, attention list, one card per agent)
// and "Tablero" (four columns from every task list). The human never adds
// or edits tasks: only agents do.
package panel

import (
	"context"
	"fmt"
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

func init() {
	app.Register(app.Area{
		ID: "panel", Path: app.PanelPath, Title: "Panel", Icon: kit.IconPanel, Section: app.SectionMain, Order: -1,
		Badge: AttentionCount,
		New:   func(a *app.Context) app.View { return New(a) },
	})
}

// WideWidth is the window width from which the attention list shows beside
// the agent cards (narrower windows stack them).
const WideWidth = 1180

// MaxMenuViews caps the view streams the Panel opens (menus of waiting
// sessions and the revision of agents with open questions).
const MaxMenuViews = 12

// ColumnLimit is how many cards a Tablero column shows before "Ver N más".
const ColumnLimit = 8

// View is the area state (UI thread only).
type View struct {
	a *app.Context
	// host filters the Panel ("" = every host).
	host string
	// drafts, dest (0 Buzón, 1 Prompt) and busy are per attention item.
	drafts map[string]*string
	dest   map[string]int
	busy   map[string]bool
	// replyOpen shows the Responder field of a blocked task.
	replyOpen map[string]bool
	// expanded columns of the Tablero.
	expanded map[string]bool
	// query is the last ?view= of the route applied (a deep link to a view).
	query string
	// Now is the clock (tests).
	Now func() time.Time
}

// New returns the Panel area.
func New(a *app.Context) *View {
	return &View{a: a, drafts: map[string]*string{}, dest: map[string]int{}, busy: map[string]bool{},
		replyOpen: map[string]bool{}, expanded: map[string]bool{}, Now: time.Now}
}

func (v *View) board() bool { return v.a.Prefs.Get().PanelView == app.PanelBoard }

func (v *View) draft(key string) *string {
	if d, ok := v.drafts[key]; ok {
		return d
	}
	d := new(string)
	v.drafts[key] = d
	return d
}

// Build shows the Panel.
func (v *View) Build(c *ui.Context, r *ui.Route) {
	t := kit.Current()
	a := v.a
	if v.host != "" {
		if _, ok := a.Live.Host(v.host); !ok && a.Live.HostsLoaded {
			v.host = ""
		}
	}
	if q := routeQuery(r, "view"); q != v.query {
		v.query = q
		switch q {
		case "tablero":
			v.setView(app.PanelBoard)
		case "agente":
			v.setView(app.PanelByAgent)
		}
	}
	m := Collect(a.Live, v.host)
	now := v.Now()
	board := v.board()
	ui.Column(c).Grow(1).MinHeight(0).AlignItems(ui.Stretch).Children(func() {
		kit.ScreenHeader(c, v.overline(m, now, board), "Panel", func() {
			if board && len(m.Items) > 0 {
				if kit.Button(c.Key("waiting"), kit.ButtonOpts{Variant: kit.Outline, Label: fmt.Sprintf("%d esperan tu respuesta", len(m.Items)),
					Height: 36}).Clicked() {
					v.setView(app.PanelByAgent)
				}
			}
			v.hostMenu(c, t)
			sel := 0
			if board {
				sel = 1
			}
			ui.Column(c).Width(260).AlignItems(ui.Stretch).Children(func() {
				if kit.Segmented(c.Key("view"), &sel, []string{"Por agente", "Tablero"}, nil) {
					mode := app.PanelByAgent
					if sel == 1 {
						mode = app.PanelBoard
					}
					v.setView(mode)
				}
			})
		}, nil)
		switch {
		case !a.Live.SessionsLoaded:
			kit.EmptyState(c, "", "", "Cargando…")
		case len(m.Agents) == 0:
			kit.EmptyState(c, kit.IconPanel, "Ningún agente", "Aquí aparecen las sesiones agentd de tus hosts, sus tareas y lo que necesita tu respuesta.")
		case board:
			v.tablero(c, t, m, now)
		default:
			v.byAgent(c, t, m, now)
		}
	})
}

func routeQuery(r *ui.Route, key string) string {
	if r == nil {
		return ""
	}
	return r.Query(key)
}

func (v *View) setView(mode string) {
	if err := v.a.Prefs.Update(func(s *app.Settings) { s.PanelView = mode }); err != nil {
		v.a.Toasts.Error("No se pudieron guardar los ajustes", err.Error())
	}
}

func (v *View) overline(m Model, now time.Time, board bool) string {
	if board {
		n, reporting := 0, 0
		for _, ag := range m.Agents {
			if len(ag.Tasks()) > 0 {
				reporting++
				n += len(ag.Tasks())
			}
		}
		s := app.Plural(n, "tarea", "tareas") + " de " + app.Plural(reporting, "agente", "agentes")
		if silent := len(m.Agents) - reporting; silent > 0 {
			s += " · " + app.Plural(silent, "agente sin informe", "agentes sin informe")
		}
		return s
	}
	s := app.Plural(len(m.Agents), "agente", "agentes") + " · " + app.Plural(m.Hosts, "host", "hosts")
	if rt := app.RelativeTime(m.Updated, now); rt != "" {
		s += " · actualizado " + rt
	}
	return s
}

func (v *View) hostMenu(c *ui.Context, t *kit.Tokens) {
	label := "Todos los hosts"
	if v.host != "" {
		label = v.a.Live.HostName(v.host)
	}
	b := ui.ButtonBase(c.Key("hosts")).Label("Filtrar por host").Height(36).Padding(0, 12).Gap(6).Radius(12).
		Background(t.Surface).Border(1, t.Border).AlignItems(ui.Center).Cursor(ui.CursorPointer).TextColor(t.Secondary)
	b.Children(func() {
		ui.Text(c, label).FontSize(13).SingleLine()
		kit.Icon(c, kit.IconChevronDown).FontSize(14).TextColor(t.Muted)
	})
	b.Menu(func(mn *ui.Menu) {
		if mn.Item("Todos los hosts").Checked(v.host == "").Chosen() {
			v.host = ""
		}
		for _, hb := range v.a.Live.Boards {
			id := hb.HostID
			name := v.a.Live.HostName(id)
			if name == "" {
				name = id
			}
			if mn.Item(name).Checked(v.host == id).Chosen() {
				v.host = id
			}
		}
	})
}

func (v *View) hostLabel(id string) string {
	if id == backend.LocalHostID {
		return "este equipo"
	}
	if n := v.a.Live.HostName(id); n != "" {
		return n
	}
	return id
}

// ---- Por agente ----

func (v *View) byAgent(c *ui.Context, t *kit.Tokens, m Model, now time.Time) {
	w, _ := c.Size()
	wide := w >= WideWidth
	counts := CountTasks(m.Agents, now)
	ui.Scroll(c.Key("body")).Grow(1).MinHeight(0).Padding(0, 24, 24, 24).Gap(18).Children(func() {
		ui.Grid(c).Columns(4).Gap(12).Children(func() {
			tile(c, t, len(m.Items), "Esperan tu respuesta", t.Waiting, true)
			tile(c, t, counts.InProgress, "Tareas en curso", t.Working, false)
			tile(c, t, counts.Pending, "Pendientes", t.Text, false)
			tile(c, t, counts.DoneToday, "Hechas hoy", t.SuccessText, false)
		})
		attention := func() {
			ui.Column(c).Gap(12).Children(func() {
				kit.SectionLabel(c, "Necesita tu atención", t.Muted)
				if len(m.Items) == 0 {
					ui.Text(c, "Nada pendiente: ningún agente espera tu respuesta.").FontSize(13).TextColor(t.Muted)
				}
				views := 0
				for _, it := range m.Items {
					v.item(c.Key(it.Key()), t, it, now, &views)
				}
			})
		}
		agents := func() {
			ui.Column(c).Gap(12).Grow(1).Shrink(1).MinWidth(0).Children(func() {
				kit.SectionLabel(c, "Trabajo por agente", t.Muted)
				cols := 2
				if !wide {
					cols = 1
				}
				ui.Grid(c).Columns(cols).Gap(12).Children(func() {
					for _, ag := range m.Agents {
						v.agentCard(c.Key("ag:"+ag.Session.Key()), t, ag, now)
					}
				})
			})
		}
		if wide {
			ui.Row(c).Gap(22).AlignItems(ui.Start).Children(func() {
				ui.Column(c).Width(430).Shrink(0).Children(attention)
				agents()
			})
			return
		}
		attention()
		agents()
	})
}

func tile(c *ui.Context, t *kit.Tokens, n int, label string, color ui.Color, warn bool) {
	bg, border := t.Surface, t.Border
	if warn && n > 0 {
		bg, border = t.WaitingBg, t.WaitingBorder
	}
	ui.Column(c.Key("tile:"+label)).Padding(12, 14).Gap(2).Radius(12).Background(bg).Border(1, border).Label(fmt.Sprintf("%s: %d", label, n)).
		Children(func() {
			ui.Text(c, strconv.Itoa(n)).FontSize(24).FontWeight(700).TextColor(color)
			ui.Text(c, label).FontSize(13).TextColor(t.Secondary)
		})
}

// itemHead is the first row of an attention card: agent, host, kind and age.
func (v *View) itemHead(c *ui.Context, t *kit.Tokens, ag Agent, kind string, since time.Time, now time.Time, color ui.Color) {
	ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
		ui.Row(c).Gap(8).AlignItems(ui.Center).Grow(1).Shrink(1).MinWidth(0).Children(func() {
			ui.Text(c, ag.Session.Name).Font(kit.MonoFamily()).FontSize(13.5).FontWeight(500).SingleLine().Ellipsis("…")
			ui.Text(c, v.hostLabel(ag.Session.HostID)).FontSize(12.5).TextColor(t.Muted).SingleLine()
		})
		label := kind
		if rt := app.RelativeTime(since, now); rt != "" {
			label += " · " + rt
		}
		ui.Text(c, label).FontSize(12).FontWeight(600).TextColor(color).SingleLine()
	})
}

func (v *View) item(c *ui.Context, t *kit.Tokens, it Item, now time.Time, views *int) {
	switch it.Kind {
	case ItemQuestion:
		v.questionCard(c, t, it, now, views)
	case ItemMenu:
		v.menuCard(c, t, it, now, views)
	default:
		v.blockedCard(c, t, it, now)
	}
}

func questionBox(c *ui.Context, t *kit.Tokens, label string, fn func()) {
	ui.Column(c).Gap(10).Padding(14).Radius(12).Background(t.QuestionBg).Border(1, t.QuestionBorder).Label(label).Children(fn)
}

// view returns the latest view of an agent, opening at most MaxMenuViews
// streams per frame.
func (v *View) view(ag Agent, views *int) *proto.View {
	if *views >= MaxMenuViews {
		return nil
	}
	*views++
	view, _ := v.a.Views.Get(ag.Session)
	return view
}

func (v *View) questionCard(c *ui.Context, t *kit.Tokens, it Item, now time.Time, views *int) {
	ag, q := it.Agent, it.Question
	key := it.Key()
	s := ag.Session
	mail := v.a.Live.Allows(s.HostID, backend.CapMailbox)
	operate := v.a.Live.Allows(s.HostID, backend.CapOperate)
	view := v.view(ag, views) // the revision a Prompt answer carries
	questionBox(c, t, "Pregunta de "+s.Name, func() {
		v.itemHead(c, t, ag, "Pregunta", q.CreatedAt, now, t.Waiting)
		ui.Text(c, q.Body).FontSize(14).LineHeight(1.45).Selectable()
		if q.Task != "" {
			title := q.Task
			for _, tk := range ag.Tasks() {
				if tk.ID == q.Task {
					title = tk.Title
				}
			}
			ui.Text(c, "Bloquea la tarea «"+title+"»").FontSize(12).TextColor(t.Muted)
		}
		if !mail && !operate {
			ui.Text(c, "Solo lectura en este dispositivo").FontSize(12).TextColor(t.Muted)
			return
		}
		busy := v.busy[key]
		if mail && len(q.Options) > 0 {
			ui.Row(c).Gap(8).Wrap().GapY(8).Children(func() {
				for i, o := range q.Options {
					variant := kit.Outline
					if i == 0 {
						variant = kit.Waiting
					}
					if kit.Button(c.Key("opt:"+strconv.Itoa(i)), kit.ButtonOpts{Variant: variant, Label: o, Height: 36, FontSize: 13, Disabled: busy,
						Aria: "Responder «" + o + "» a " + s.Name}).Clicked() && !busy {
						v.reply(key, s, q.ID, o)
					}
				}
			})
		}
		d := v.draft(key)
		dest := v.dest[key]
		if !mail {
			dest = 1
		} else if !operate {
			dest = 0
		}
		in := ui.TextInput(c.Key("draft"), d).Placeholder("O escribe una respuesta…").Label("Respuesta a "+s.Name).
			Height(44).Padding(0, 12).Background(t.Bg).Border(1, t.WaitingBorder).Radius(10).FontSize(13).FillWidth().MinWidth(0)
		ui.Row(c).Gap(8).AlignItems(ui.Center).Justify(ui.End).Children(func() {
			if mail && operate {
				ui.Column(c).Width(176).Height(44).Shrink(0).AlignItems(ui.Stretch).Children(func() {
					if kit.Segmented(c.Key("dest"), &dest, []string{"Buzón", "Prompt"}, nil) {
						v.dest[key] = dest
					}
				})
			}
			send := kit.Button(c.Key("send"), kit.ButtonOpts{Variant: kit.Primary, Label: "Enviar", Height: 38, FontSize: 13,
				Disabled: busy || strings.TrimSpace(*d) == "", Aria: "Enviar respuesta a " + s.Name}).Clicked()
			if (send || in.Submitted()) && !busy && strings.TrimSpace(*d) != "" {
				text := strings.TrimSpace(*d)
				if dest == 1 {
					v.promptAnswer(key, s, view, q, text)
				} else {
					v.reply(key, s, q.ID, text)
				}
			}
		})
	})
}

// reply answers a question through the mailbox (op reply).
func (v *View) reply(key string, s backend.Session, id int64, body string) {
	a := v.a
	v.busy[key] = true
	a.Go(func(ctx context.Context) {
		_, err := a.Backend.Reply(ctx, s.HostID, id, body, true)
		a.Update(func() {
			delete(v.busy, key)
			if err != nil {
				a.Toasts.Error("No se pudo responder", app.ErrorText(err))
				return
			}
			delete(v.drafts, key)
			a.Toasts.Success("Respuesta enviada a " + s.Name)
		})
	})
}

// promptAnswer types the answer into the agent's prompt (act prompt with
// the view's revision and epoch; forced while it works, as the chat does).
// On success the question is closed with a quiet reply (notify=false).
func (v *View) promptAnswer(key string, s backend.Session, view *proto.View, q proto.BoardQuestion, text string) {
	a := v.a
	args := proto.ActArgs{Name: s.Name, Action: proto.ActPrompt, Client: "agentd-desktop",
		Text: fmt.Sprintf("Respuesta a tu pregunta #%d (%s): %s", q.ID, oneLine(q.Body, 120), text)}
	if view != nil {
		args.Revision, args.Epoch = view.Revision, view.Epoch
		args.Force = view.Status == "working"
	} else {
		args.Force = s.Status == "working"
	}
	v.busy[key] = true
	a.Go(func(ctx context.Context) {
		_, err := a.Backend.Act(ctx, s, args)
		// The prompt reached the agent: close the question and keep the
		// answer on record without injecting it a second time.
		var rerr error
		if err == nil && q.ID != 0 {
			_, rerr = a.Backend.Reply(ctx, s.HostID, q.ID, text, false)
		}
		a.Update(func() {
			delete(v.busy, key)
			if err != nil {
				if cv := app.ConflictView(err); cv != nil {
					a.Views.Set(s, cv)
				}
				title, detail := session.SendErrorText(err)
				a.Toasts.Error(title, detail)
				return
			}
			delete(v.drafts, key)
			if rerr != nil {
				a.Toasts.Error("Enviado al prompt, pero no se pudo cerrar la pregunta", app.ErrorText(rerr))
				return
			}
			a.Toasts.Success("Enviado al prompt de " + s.Name)
		})
	})
}

func oneLine(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); len(r) > n {
		return string(r[:n-1]) + "…"
	}
	return s
}

func (v *View) menuCard(c *ui.Context, t *kit.Tokens, it Item, now time.Time, views *int) {
	ag := it.Agent
	s := ag.Session
	key := it.Key()
	view := v.view(ag, views)
	operate := v.a.Live.Allows(s.HostID, backend.CapOperate)
	questionBox(c, t, "Menú de "+s.Name, func() {
		v.itemHead(c, t, ag, "Menú en el terminal", it.Since, now, t.Waiting)
		q := s.Preview
		if view != nil && view.Dialog != nil && view.Dialog.Question != "" {
			q = view.Dialog.Question
		}
		if q == "" {
			q = "Espera una respuesta"
		}
		ui.Text(c, q).FontSize(14).LineHeight(1.45).Selectable()
		hasOptions := view != nil && len(view.Options) > 0
		switch {
		case s.Source != backend.SourceAgentd || view == nil && s.Info == nil:
		case view == nil:
			ui.Text(c, "Cargando el menú…").FontSize(12).TextColor(t.Muted)
		case hasOptions && operate:
			busy := v.busy[key]
			ui.Row(c).Gap(8).Wrap().GapY(8).Children(func() {
				for i, o := range view.Options {
					n := o.Index
					if n <= 0 {
						n = i + 1
					}
					label := fmt.Sprintf("%d · %s", n, o.Label)
					if kit.Button(c.Key("opt:"+o.ID), kit.ButtonOpts{Variant: kit.Outline, Label: label, Height: 36, FontSize: 13,
						Disabled: busy, Aria: "Elegir «" + o.Label + "» en " + s.Name}).Clicked() && !busy {
						v.choose(key, s, view, o)
					}
				}
			})
		case hasOptions:
			ui.Text(c, app.Plural(len(view.Options), "opción", "opciones")+" · solo lectura en este dispositivo").FontSize(12).TextColor(t.Muted)
		}
		if kit.Button(c.Key("open"), kit.ButtonOpts{Variant: kit.Ghost, Label: "Abrir agente", Height: 32, FontSize: 13,
			Aria: "Abrir " + s.Name}).Clicked() {
			v.open(s)
		}
	})
}

// choose picks a menu option: act choose with the revision and epoch of the
// latest view. A conflict refreshes the cached view (the act error carries
// the new one) and says so.
func (v *View) choose(key string, s backend.Session, view *proto.View, o proto.ViewOption) {
	if view.Revision == 0 || view.Epoch == "" {
		v.a.Toasts.Info("Sin revisión del menú: abre la sesión para responder")
		return
	}
	v.busy[key] = true
	session.Choose(v.a, s, view, o, func(err error) {
		delete(v.busy, key)
		if err == nil {
			return
		}
		if proto.CodeOf(err) == proto.CodeConflict {
			v.a.Toasts.Info("El menú cambió: revisa las opciones actualizadas")
			return
		}
		v.a.Toasts.Error("No se pudo responder", app.ErrorText(err))
	})
}

func (v *View) blockedCard(c *ui.Context, t *kit.Tokens, it Item, now time.Time) {
	ag, tk := it.Agent, it.Task
	s := ag.Session
	key := it.Key()
	ui.Column(c).Gap(8).Padding(14).Radius(12).Background(t.Surface).Border(1, t.Border).Label("Tarea bloqueada de " + s.Name).Children(func() {
		v.itemHead(c, t, ag, "Tarea bloqueada", tk.UpdatedAt, now, t.Danger)
		ui.Text(c, tk.Title).FontSize(14).LineHeight(1.45)
		if tk.Note != "" {
			ui.Text(c, tk.Note).FontSize(13).TextColor(t.Secondary).LineHeight(1.4)
		}
		mail := v.a.Live.Allows(s.HostID, backend.CapMailbox)
		if v.replyOpen[key] && mail {
			d := v.draft(key)
			busy := v.busy[key]
			ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
				in := ui.TextInput(c.Key("draft"), d).Placeholder("Mensaje para "+s.Name).Label("Mensaje para "+s.Name).
					Height(38).Padding(0, 10).Background(t.Bg).Border(1, t.Border).Radius(10).FontSize(13).Grow(1).Shrink(1).MinWidth(0).AutoFocus()
				send := kit.Button(c.Key("send"), kit.ButtonOpts{Variant: kit.Primary, Label: "Enviar", Height: 38, FontSize: 13,
					Disabled: busy || strings.TrimSpace(*d) == "", Aria: "Enviar mensaje a " + s.Name}).Clicked()
				if (send || in.Submitted()) && !busy && strings.TrimSpace(*d) != "" {
					v.message(key, s, tk, strings.TrimSpace(*d))
				}
			})
		}
		ui.Row(c).Gap(8).Children(func() {
			if mail && !v.replyOpen[key] && kit.Button(c.Key("reply"), kit.ButtonOpts{Variant: kit.Surface, Label: "Responder", Height: 34, FontSize: 13,
				Aria: "Responder a " + s.Name + " sobre «" + tk.Title + "»"}).Clicked() {
				v.replyOpen[key] = true
			}
			if kit.Button(c.Key("open"), kit.ButtonOpts{Variant: kit.Outline, Label: "Abrir agente", Height: 34, FontSize: 13,
				Aria: "Abrir " + s.Name}).Clicked() {
				v.open(s)
			}
		})
	})
}

// message sends feedback about a blocked task to the agent's mailbox.
func (v *View) message(key string, s backend.Session, tk proto.Task, body string) {
	a := v.a
	v.busy[key] = true
	args := proto.SendArgs{To: s.Name, Subject: "Tarea «" + oneLine(tk.Title, 80) + "»", Body: body}
	a.Go(func(ctx context.Context) {
		_, err := a.Backend.Send(ctx, s.HostID, args)
		a.Update(func() {
			delete(v.busy, key)
			if err != nil {
				a.Toasts.Error("No se pudo enviar", app.ErrorText(err))
				return
			}
			delete(v.drafts, key)
			delete(v.replyOpen, key)
			a.Toasts.Success("Mensaje enviado a " + s.Name)
		})
	})
}

func (v *View) open(s backend.Session) { v.a.Navigate(app.SessionPath(s)) }

// agentCard is one agent of "Trabajo por agente"; a click opens its session.
func (v *View) agentCard(c *ui.Context, t *kit.Tokens, ag Agent, now time.Time) {
	s := ag.Session
	tasks := ag.Tasks()
	done, total := Progress(tasks)
	label, labelColor, cur, next := summary(t, ag)
	b := ui.ButtonBase(c).Label(s.Name+": abrir agente").Column().Gap(8).Padding(13, 14).Radius(12).MinHeight(148).
		Background(t.Surface).Border(1, t.Border).AlignItems(ui.Stretch).Cursor(ui.CursorPointer)
	if b.Hovered() {
		b.Background(t.Raised)
	}
	b.Children(func() {
		ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
			ui.Row(c).Gap(8).AlignItems(ui.Center).Grow(1).Shrink(1).MinWidth(0).Children(func() {
				ui.Text(c, s.Name).Font(kit.MonoFamily()).FontSize(13.5).FontWeight(500).SingleLine().Ellipsis("…")
				ui.Text(c, v.hostLabel(s.HostID)).FontSize(12.5).TextColor(t.Muted).SingleLine()
			})
			kit.StatusPill(c, ag.Status())
		})
		ui.Column(c).Gap(2).Children(func() {
			ui.Text(c, label).FontSize(11.5).FontWeight(600).TextColor(labelColor)
			txt := ui.Text(c, cur).FontSize(13.5).LineHeight(1.4).MaxLines(2).Ellipsis("…")
			if len(tasks) == 0 {
				txt.Font(kit.MonoFamily()).FontSize(12.5).TextColor(t.Secondary)
			}
		})
		if total > 0 {
			ui.Row(c).Gap(10).AlignItems(ui.Center).Children(func() {
				ui.Row(c).Height(5).Grow(1).Shrink(1).Radius(3).Background(t.Raised2).Children(func() {
					if done > 0 {
						ui.Box(c).Height(5).Radius(3).Background(t.Working).WidthPercent(float32(done) * 100 / float32(total))
					}
				})
				ui.Text(c, fmt.Sprintf("%d / %d", done, total)).FontSize(12).TextColor(t.Secondary).SingleLine()
			})
		}
		ui.Text(c, next).FontSize(12).TextColor(t.Muted).LineHeight(1.5).Grow(1)
		ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
			ui.Text(c, v.footer(ag, now)).FontSize(11.5).TextColor(t.Muted).Grow(1).SingleLine().Ellipsis("…")
			kit.Icon(c, kit.IconChevronR).FontSize(14).TextColor(t.Muted)
		})
	})
	if b.Clicked() {
		v.open(s)
	}
}

// summary is the card's label, its colour, the main line and the line
// below: the current task, a blocked one, "Todo hecho", or the fallback
// for agents that report no tasks (status and the last screen line).
func summary(t *kit.Tokens, ag Agent) (label string, color ui.Color, cur, next string) {
	tasks := ag.Tasks()
	if len(tasks) == 0 {
		cur = ag.Session.Preview
		if cur == "" {
			cur = kit.StatusLabel(ag.Status())
		}
		return "Sin tareas informadas", t.Muted, cur,
			"Este agente no informa de su plan: se muestra la última línea de su pantalla."
	}
	var current, blocked, lastDone *proto.Task
	var pending []string
	for i := range tasks {
		tk := &tasks[i]
		switch tk.Status {
		case proto.TaskInProgress:
			if current == nil {
				current = tk
			}
		case proto.TaskBlocked:
			if blocked == nil {
				blocked = tk
			}
		case proto.TaskDone:
			if lastDone == nil || tk.UpdatedAt.After(lastDone.UpdatedAt) {
				lastDone = tk
			}
		case proto.TaskPending:
			pending = append(pending, tk.Title)
		}
	}
	after := func(list []string) string {
		switch {
		case len(list) == 0:
			return "Sin pendientes"
		case len(list) <= 2:
			return "Después: " + strings.Join(list, " · ")
		}
		return "Después: " + strings.Join(list[:2], " · ") + " · " + strconv.Itoa(len(list)-2) + " más"
	}
	switch {
	case current != nil:
		return "En curso", t.Working, current.Title, after(pending)
	case blocked != nil:
		n := blocked.Note
		if n == "" {
			n = after(pending)
		}
		return "Bloqueada", t.Danger, blocked.Title, n
	case len(pending) > 0:
		return "Siguiente", t.Secondary, pending[0], after(pending[1:])
	case lastDone != nil:
		return "Todo hecho", t.SuccessText, lastDone.Title, "Sin pendientes"
	}
	return "Sin tareas activas", t.Muted, tasks[len(tasks)-1].Title, "Sin pendientes"
}

func (v *View) footer(ag Agent, now time.Time) string {
	var parts []string
	if n := len(ag.Questions()); n > 0 {
		parts = append(parts, app.Plural(n, "pregunta abierta", "preguntas abiertas"))
	} else if ag.Status() == "waiting" {
		parts = append(parts, "Espera tu respuesta")
	}
	if len(ag.Tasks()) == 0 {
		turn := uint64(0)
		if ag.Board != nil {
			turn = ag.Board.Turn
		} else if ag.Session.Info != nil {
			turn = ag.Session.Info.Turn
		}
		if turn > 0 {
			parts = append(parts, "turno "+strconv.FormatUint(turn, 10))
		}
	}
	if rt := app.RelativeTime(ag.Session.Updated, now); rt != "" {
		parts = append(parts, rt)
	}
	return strings.Join(parts, " · ")
}

// ---- Tablero ----

func (v *View) tablero(c *ui.Context, t *kit.Tokens, m Model, now time.Time) {
	cols := Board(m.Agents, now)
	colors := []ui.Color{t.Secondary, t.Working, t.Danger, t.SuccessText}
	ui.Row(c.Key("tablero")).Grow(1).MinHeight(0).Gap(14).Padding(0, 24, 24, 24).AlignItems(ui.Stretch).Children(func() {
		for i, col := range cols {
			cc := c.Key("col:" + col.Status)
			color := colors[i]
			ui.Column(cc).Grow(1).Basis(0).MinWidth(0).MinHeight(0).Padding(12).Gap(10).Radius(12).Background(t.NavBg).
				Border(1, t.Divider).Label("Columna " + col.Title).Children(func() {
				ui.Row(cc).Padding(2, 4).AlignItems(ui.Center).Children(func() {
					ui.Text(cc, col.Title).FontSize(13).FontWeight(600).TextColor(color).Grow(1)
					ui.Text(cc, strconv.Itoa(len(col.Cards))).FontSize(12).TextColor(t.Muted)
				})
				ui.Scroll(cc.Key("cards")).Grow(1).MinHeight(0).Gap(10).Children(func() {
					cards := col.Cards
					if !v.expanded[col.Status] && len(cards) > ColumnLimit {
						cards = cards[:ColumnLimit]
					}
					if len(col.Cards) == 0 {
						ui.Text(cc, "Ninguna").FontSize(12).TextColor(t.Faint).Padding(4)
					}
					for j, cd := range cards {
						v.taskCard(cc.Key("card:"+strconv.Itoa(j)), t, cd, color, col.Status, now)
					}
					if extra := len(col.Cards) - len(cards); extra > 0 {
						b := ui.ButtonBase(cc.Key("more")).Label(fmt.Sprintf("Ver %d más", extra)).Padding(8).Radius(10).
							Border(1, t.Border).AlignItems(ui.Center).Justify(ui.Center).Cursor(ui.CursorPointer).TextColor(t.Muted)
						b.Children(func() { ui.Text(cc, fmt.Sprintf("Ver %d más", extra)).FontSize(12) })
						if b.Clicked() {
							v.expanded[col.Status] = true
						}
					}
				})
			})
		}
	})
}

func (v *View) taskCard(c *ui.Context, t *kit.Tokens, cd Card, color ui.Color, status string, now time.Time) {
	s := cd.Agent.Session
	b := ui.ButtonBase(c).Label(cd.Task.Title+" · "+s.Name).Column().Gap(7).Padding(10, 12).Radius(10).
		Background(t.Surface).Border(1, t.Border).AlignItems(ui.Stretch).Cursor(ui.CursorPointer)
	if b.Hovered() {
		b.Background(t.Raised)
	}
	b.Children(func() {
		ui.Text(c, cd.Task.Title).FontSize(13).LineHeight(1.4)
		if cd.Task.Note != "" {
			ui.Text(c, cd.Task.Note).FontSize(12).LineHeight(1.4).TextColor(color)
		}
		ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
			ui.Text(c, s.Name).Font(kit.MonoFamily()).FontSize(11.5).TextColor(t.Secondary).Grow(1).Shrink(1).SingleLine().Ellipsis("…")
			when := ""
			switch {
			case status == proto.TaskPending && cd.Next:
				when = "siguiente"
			case status != proto.TaskPending:
				when = app.RelativeTime(cd.Task.UpdatedAt, now)
			}
			if when != "" {
				ui.Text(c, when).FontSize(11.5).TextColor(t.Muted).SingleLine()
			}
		})
	})
	if b.Clicked() {
		v.open(s)
	}
}

// itoa64 formats an id.
func itoa64(n int64) string { return strconv.FormatInt(n, 10) }
