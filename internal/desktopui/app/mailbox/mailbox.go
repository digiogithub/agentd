// Package mailbox is the Buzón area (mockup 8): the agentd mailbox of one
// host, Mensajes (direct messages with their replies and states) and Colas
// (work queues with their unclaimed and claimed items), and "Enviar a agente
// o cola".
package mailbox

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/digiogithub/agentd/internal/desktopui/app"
	"github.com/digiogithub/agentd/internal/desktopui/backend"
	"github.com/digiogithub/agentd/internal/desktopui/kit"
	"github.com/digiogithub/agentd/internal/proto"
	"github.com/egoist/mygo/ui"
)

func init() {
	app.Register(app.Area{
		ID: "mailbox", Path: "/mailbox", Patterns: []string{"/mailbox", "/mailbox/{host}"}, Title: "Buzón",
		Icon: kit.IconMailbox, Section: app.SectionMain, Order: 2, Shortcut: 3,
		New: func(a *app.Context) app.View { return New(a) },
	})
}

// Refresh is how often the open mailbox reloads.
const Refresh = 10 * time.Second

// Tabs.
const (
	TabMessages = iota
	TabQueues
)

// View is the area state (UI thread only).
type View struct {
	a         *app.Context
	hostID    string
	routeHost string
	tab       int

	messages []proto.Message
	queues   []proto.QueueCount
	loaded   string // host the data belongs to
	loadedAt time.Time
	loading  bool
	err      string

	composeOpen bool
	to, body    string
	sending     bool
	// Now is the clock of relative times (tests).
	Now func() time.Time
}

// New returns the area.
func New(a *app.Context) *View { return &View{a: a, Now: time.Now} }

func (v *View) hosts() []backend.HostStatus {
	var out []backend.HostStatus
	for _, h := range v.a.Live.Online() {
		if h.Capabilities != nil && h.Capabilities.Has("agentd") || h.AgentdVer != "" {
			out = append(out, h)
		}
	}
	return out
}

func (v *View) load(hostID string) {
	if v.loading || v.a.Backend == nil {
		return
	}
	v.loading = true
	a := v.a
	a.Go(func(ctx context.Context) {
		msgs, err := a.Backend.Inbox(ctx, hostID)
		var qs []proto.QueueCount
		if err == nil {
			qs, err = a.Backend.Queues(ctx, hostID)
		}
		a.Update(func() {
			v.loading = false
			v.loadedAt = time.Now()
			if hostID != v.hostID {
				return
			}
			v.loaded = hostID
			if err != nil {
				v.err = app.ErrorText(err)
				return
			}
			v.err, v.messages, v.queues = "", msgs, qs
		})
	})
}

// Who is "tú" for the human, the name otherwise.
func Who(id string) string {
	if strings.HasPrefix(id, "human:") {
		return "tú"
	}
	return id
}

// StateLabel is the pill of a message state.
func StateLabel(state string) (label string, fg, bg ui.Color) {
	t := kit.Current()
	switch state {
	case "queued":
		return "En cola", t.OnWaiting, t.Waiting
	case "replied":
		return "Respondido", t.OnWorking, t.Accent
	case "read":
		return "Leído", t.Secondary, t.Raised
	case "delivered":
		return "Entregado", t.Working, t.WorkingBg
	case "unclaimed":
		return "Sin reclamar", t.Waiting, t.WaitingBg
	}
	return state, t.Secondary, t.Raised
}

// Build shows the mailbox.
func (v *View) Build(c *ui.Context, r *ui.Route) {
	t := kit.Current()
	if r != nil && r.Param("host") != v.routeHost {
		v.routeHost = r.Param("host")
		if v.routeHost != "" {
			v.hostID = v.routeHost
		}
	}
	hosts := v.hosts()
	var host *backend.HostStatus
	for i := range hosts {
		if hosts[i].Host.ID == v.hostID {
			host = &hosts[i]
		}
	}
	if host == nil && len(hosts) > 0 {
		host = &hosts[0]
		v.hostID = host.Host.ID
	}
	if host != nil && (v.loaded != v.hostID || time.Since(v.loadedAt) > Refresh) {
		if v.loaded != v.hostID {
			v.messages, v.queues = nil, nil
		}
		v.load(v.hostID)
	}
	if host != nil {
		c.After(Refresh)
	}
	replies := map[int64][]proto.Message{}
	var direct []proto.Message
	for _, m := range v.messages {
		if m.ReplyTo != nil {
			replies[*m.ReplyTo] = append(replies[*m.ReplyTo], m)
		}
	}
	for _, m := range v.messages {
		if m.Queue != "" || m.ReplyTo != nil && slices.ContainsFunc(v.messages, func(o proto.Message) bool { return o.ID == *m.ReplyTo }) {
			continue
		}
		direct = append(direct, m)
	}
	slices.SortStableFunc(direct, func(a, b proto.Message) int { return b.CreatedAt.Compare(a.CreatedAt) })
	ui.Column(c).Grow(1).MinHeight(0).AlignItems(ui.Stretch).Children(func() {
		name := "sin host"
		if host != nil {
			name = host.Host.Name
		}
		kit.ScreenHeader(c, "", "Buzón", func() {
			if host != nil && kit.Button(c.Key("compose"), kit.ButtonOpts{Variant: kit.Primary, Icon: kit.IconSend,
				Label: "Enviar a agente o cola", Height: 36, Round: true}).Clicked() {
				v.openCompose("")
			}
		}, func() {
			over := ui.ButtonBase(c.Key("hostpick")).Label("Host del buzón: "+name).Gap(4).AlignItems(ui.Center).Cursor(ui.CursorPointer).
				Margin(-8, 0, 0, 0).AlignSelf(ui.Start)
			over.Children(func() {
				ui.Text(c, "Mensajes entre agentes · "+name).FontSize(13).TextColor(t.Muted)
				if len(hosts) > 1 {
					kit.Icon(c, kit.IconChevronDown).FontSize(14).TextColor(t.Muted)
				}
			})
			if len(hosts) > 1 {
				over.Menu(func(m *ui.Menu) {
					for _, h := range hosts {
						if m.Item(h.Host.Name).Checked(h.Host.ID == v.hostID).Chosen() {
							v.hostID = h.Host.ID
						}
					}
				})
			}
			tab := v.tab
			if kit.Segmented(c.Key("tabs"), &tab, []string{fmt.Sprintf("Mensajes · %d", len(direct)), fmt.Sprintf("Colas · %d", len(v.queues))}, nil) {
				v.tab = tab
			}
		})
		switch {
		case host == nil:
			kit.EmptyState(c, kit.IconMailbox, "", "El buzón necesita un host con agentd en línea.")
			return
		case v.err != "":
			kit.EmptyState(c, kit.IconError, "No se pudo cargar el buzón", v.err)
			return
		}
		ui.Scroll(c.Key("list")).Grow(1).MinHeight(0).Padding(4, 24, 24, 24).Children(func() {
			ui.Column(c).Gap(10).MaxWidth(820).FillWidth().Children(func() {
				if v.tab == TabMessages {
					if len(direct) == 0 {
						kit.EmptyState(c, "", "", "No hay mensajes")
					}
					for _, m := range direct {
						v.messageCard(c.Key(m.ID), t, m, replies[m.ID])
					}
					return
				}
				if len(v.queues) == 0 {
					kit.EmptyState(c, "", "", "No hay colas")
				}
				for _, q := range v.queues {
					v.queueCard(c.Key("q:"+q.Name), t, q)
				}
			})
		})
	})
	v.composeDialog(c, t, host)
}

func (v *View) messageCard(c *ui.Context, t *kit.Tokens, m proto.Message, replies []proto.Message) {
	from := Who(m.Sender)
	label, fg, bg := StateLabel(m.State)
	foot := fmt.Sprintf("#%d · %s", m.ID, app.RelativeTime(m.CreatedAt, v.Now()))
	if m.State == "queued" {
		foot = fmt.Sprintf("#%d · se entregará cuando %s quede libre", m.ID, m.Recipient)
	}
	kit.Card(c, ui.Color{}, func() {
		ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
			col := t.AccentLight
			if from == "tú" {
				col = t.Secondary
			}
			ui.Text(c, from).Font(kit.MonoFamily()).FontSize(13).TextColor(col).SingleLine()
			kit.Icon(c, kit.IconChevronR).FontSize(14).TextColor(t.Faint)
			ui.Text(c, Who(m.Recipient)).Font(kit.MonoFamily()).FontSize(13).TextColor(t.Secondary).SingleLine().Grow(1).Ellipsis("…")
			kit.Pill(c, label, fg, bg)
		})
		text := m.Body
		if text == "" {
			text = m.Subject
		}
		ui.Text(c, text).FontSize(14).LineHeight(1.45).Selectable()
		for _, rp := range replies {
			ui.Column(c.Key(rp.ID)).Gap(4).Padding(10, 12).Radius(12).Background(t.Bg).BorderWidth(0, 0, 0, 2).BorderColor(t.Accent).Children(func() {
				ui.Text(c, Who(rp.Sender)).Font(kit.MonoFamily()).FontSize(12).TextColor(t.Muted)
				ui.Text(c, rp.Body).FontSize(13.5).TextColor(t.Secondary).LineHeight(1.4).Selectable()
			})
		}
		ui.Text(c, foot).FontSize(12).TextColor(t.Muted)
	})
}

func (v *View) queueCard(c *ui.Context, t *kit.Tokens, q proto.QueueCount) {
	var unclaimed, claimed []proto.Message
	for _, m := range v.messages {
		if m.Queue != q.Name {
			continue
		}
		if m.State == "unclaimed" {
			unclaimed = append(unclaimed, m)
		} else {
			claimed = append(claimed, m)
		}
	}
	kit.Card(c, ui.Color{}, func() {
		ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
			ui.Text(c, "queue:"+q.Name).Font(kit.MonoFamily()).FontSize(14).FontWeight(500).Grow(1)
			fg, bg := t.Muted, t.Raised
			if q.Unclaimed > 0 {
				fg, bg = t.Waiting, t.WaitingBg
			}
			kit.Pill(c, fmt.Sprintf("%d sin reclamar", q.Unclaimed), fg, bg)
		})
		for _, m := range unclaimed {
			what := m.Subject
			if what == "" {
				what = m.Body
			}
			ui.Text(c.Key(m.ID), fmt.Sprintf("#%d · %s · de %s", m.ID, what, Who(m.Sender))).FontSize(13).TextColor(t.Secondary).
				SingleLine().Ellipsis("…")
		}
		for _, m := range claimed {
			at := m.CreatedAt
			if m.ClaimedAt != nil {
				at = *m.ClaimedAt
			}
			ui.Text(c.Key(m.ID), fmt.Sprintf("#%d reclamado por %s %s", m.ID, Who(m.Recipient), app.RelativeTime(at, v.Now()))).
				FontSize(13).TextColor(t.Muted)
		}
	})
}

func (v *View) openCompose(to string) {
	v.composeOpen, v.to, v.body = true, to, ""
}

func (v *View) composeDialog(c *ui.Context, t *kit.Tokens, host *backend.HostStatus) {
	if !v.composeOpen || host == nil {
		return
	}
	var targets []string
	for _, s := range v.a.Live.Sessions {
		if s.HostID == host.Host.ID && s.Source == backend.SourceAgentd {
			targets = append(targets, s.Name)
		}
	}
	for _, q := range v.queues {
		targets = append(targets, "queue:"+q.Name)
	}
	if v.to == "" && len(targets) > 0 {
		v.to = targets[0]
	}
	open := true
	kit.Dialog(c.Key("compose-dialog"), &open, kit.DialogOpts{Title: "Enviar a agente o cola", Width: 520,
		Body: func() {
			ui.Row(c).Gap(8).GapY(8).Wrap().Children(func() {
				for _, tg := range targets {
					if kit.Chip(c.Key("t:"+tg), tg, v.to == tg, true).Clicked() {
						v.to = tg
					}
				}
			})
			kit.Field(c, "Para", func() { kit.Input(c.Key("to"), &v.to, "claude@webapp o queue:review", true).Label("Para") })
			kit.Field(c, "Mensaje", func() {
				ui.TextArea(c.Key("body"), &v.body).Lines(3, 8).Placeholder("Qué necesitas").Label("Mensaje").
					Padding(10, 12).Background(t.Bg).Border(1, t.Border).Radius(12).FontSize(14)
			})
		},
		Footer: func() {
			if kit.Button(c.Key("cancel"), kit.ButtonOpts{Variant: kit.Outline, Label: "Cancelar", Height: 40}).Clicked() {
				open = false
			}
			label := "Enviar"
			if v.sending {
				label = "Enviando…"
			}
			ok := strings.TrimSpace(v.to) != "" && strings.TrimSpace(v.body) != "" && !v.sending
			if kit.Button(c.Key("send"), kit.ButtonOpts{Variant: kit.Primary, Label: label, Height: 40, Disabled: !ok}).Clicked() && ok {
				v.send(host.Host.ID)
			}
		}})
	if !open {
		v.composeOpen = false
	}
}

func (v *View) send(hostID string) {
	to, body := strings.TrimSpace(v.to), strings.TrimSpace(v.body)
	v.sending = true
	a := v.a
	a.Go(func(ctx context.Context) {
		_, err := a.Backend.Send(ctx, hostID, proto.SendArgs{To: to, Body: body})
		a.Update(func() {
			v.sending = false
			if err != nil {
				a.Toasts.Error("No se pudo enviar a "+to, app.ErrorText(err))
				return
			}
			v.composeOpen = false
			a.Toasts.Success("Mensaje enviado")
			v.load(hostID)
		})
	})
}
