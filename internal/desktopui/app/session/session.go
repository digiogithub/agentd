// Package session is the session page of agentd-desktop (mockups 2 and 3):
// a header with status, the Chat tab built from the agentd semantic view
// (transcript, the pending question with numbered options, a composer that
// sends act prompt) and the Terminal tab (MyGo's terminal plugin fed by
// backend.Terminal, with the key bar, Interrumpir and Control/Observar).
// The agents area shows it beside the list on wide windows and alone on
// narrow ones.
package session

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/digiogithub/agentd/internal/desktopui/app"
	"github.com/digiogithub/agentd/internal/desktopui/backend"
	"github.com/digiogithub/agentd/internal/desktopui/kit"
	"github.com/digiogithub/agentd/internal/proto"
	"github.com/egoist/mygo/ui"
)

// Tabs.
const (
	TabChat = iota
	TabTerminal
)

// View is the state of one open session (UI thread only).
type View struct {
	a   *app.Context
	key string

	tab int

	// chat
	draft       string
	sending     bool
	selected    string // option id picked in the pending card
	pendingKey  string // identity of the pending question the selection belongs to
	answering   bool
	focusDraft  bool
	expanded    map[int]bool
	scroll      ui.ScrollState
	lastLines   int
	queued      int
	inboxCancel context.CancelFunc

	term *termState
	// kill is the Terminar confirmation.
	kill *app.Terminator
	ctx  context.Context
	stop context.CancelFunc
}

// New returns the page of session s.
func New(a *app.Context, s backend.Session) *View {
	ctx, stop := context.WithCancel(a.Ctx)
	v := &View{a: a, key: s.Key(), expanded: map[int]bool{}, ctx: ctx, stop: stop, kill: app.NewTerminator(a)}
	if s.Source != backend.SourceAgentd {
		v.tab = TabTerminal
	}
	v.pollInbox(s)
	return v
}

// Key is the session key the view shows.
func (v *View) Key() string { return v.key }

// Close stops the terminal and the polling.
func (v *View) Close() {
	v.stop()
	if v.term != nil {
		v.term.close()
		v.term = nil
	}
}

// Tab returns the selected tab (tests).
func (v *View) Tab() int { return v.tab }

// SetTab selects a tab.
func (v *View) SetTab(t int) { v.tab = t }

// pollInbox counts the mailbox messages queued for the session (the chat's
// "1 mensaje en cola" hint), now and every 10 s.
func (v *View) pollInbox(s backend.Session) {
	if s.Source != backend.SourceAgentd || v.a.Backend == nil {
		return
	}
	a := v.a
	a.Go(func(context.Context) {
		t := time.NewTicker(10 * time.Second)
		defer t.Stop()
		for {
			msgs, err := a.Backend.Inbox(v.ctx, s.HostID)
			if err == nil {
				n := 0
				for _, m := range msgs {
					if m.Recipient == s.Name && m.State == "queued" {
						n++
					}
				}
				a.Update(func() { v.queued = n })
			}
			select {
			case <-v.ctx.Done():
				return
			case <-t.C:
			}
		}
	})
}

// Build shows the session. back, when set, adds a back button (narrow
// windows).
func (v *View) Build(c *ui.Context, s backend.Session, back func()) {
	t := kit.Current()
	view, viewErr := v.a.Views.Get(s)
	status := s.Status
	if view != nil && view.Status != "" {
		status = view.Status
	}
	ui.Column(c).Grow(1).MinHeight(0).MinWidth(0).AlignItems(ui.Stretch).Children(func() {
		v.header(c, t, s, view, status, back)
		switch {
		case v.tab == TabTerminal:
			v.terminalTab(c, t, s, view)
		case s.Source != backend.SourceAgentd:
			kit.EmptyState(c, kit.IconTerminal, "",
				"Las sesiones "+string(s.Source)+" no tienen vista chat: usa la terminal. Con agentd tendrías chat, aprobaciones y buzón.")
		default:
			v.chatTab(c, t, s, view, viewErr)
		}
	})
	v.kill.Build(c)
	if view != nil && view.Attention.Set && status != "waiting" && v.tab == TabChat {
		// Looking at a finished turn clears its attention, as the mobile app.
		v.ack(s)
	}
}

func (v *View) ack(s backend.Session) {
	a := v.a
	a.Go(func(ctx context.Context) { _ = a.Backend.ViewAck(ctx, s) })
}

func (v *View) header(c *ui.Context, t *kit.Tokens, s backend.Session, view *proto.View, status string, back func()) {
	host := v.a.Live.HostName(s.HostID)
	var parts []string
	if host != "" {
		parts = append(parts, host)
	}
	if v.tab == TabTerminal {
		if s.Info != nil && s.Info.Cols > 0 {
			parts = append(parts, fmt.Sprintf("%d×%d", s.Info.Cols, s.Info.Rows))
		}
		if s.Info != nil && s.Info.Attached > 0 {
			parts = append(parts, app.Plural(s.Info.Attached, "cliente", "clientes"))
		}
		if v.term != nil && v.term.observe {
			parts = append(parts, "observando")
		}
	} else {
		cwd := ""
		if view != nil {
			cwd = view.Cwd
		} else if s.Info != nil {
			cwd = s.Info.Cwd
		}
		if cwd != "" {
			parts = append(parts, cwd)
		}
		if s.Info != nil && s.Info.Turn > 0 {
			parts = append(parts, fmt.Sprintf("turno %d", s.Info.Turn))
		}
	}
	ui.Column(c).Shrink(0).Padding(14, 20, 10, 16).Gap(12).BorderWidth(0, 0, 1, 0).BorderColor(t.Divider).Children(func() {
		ui.Row(c).Gap(10).AlignItems(ui.Center).Children(func() {
			if back != nil && kit.IconButton(c, kit.IconBack, "Volver").Clicked() {
				back()
			}
			ui.Column(c).Grow(1).Shrink(1).MinWidth(0).Gap(2).Children(func() {
				ui.Text(c, app.SessionTitle(s)).FontSize(16).FontWeight(600).SingleLine().Ellipsis("…").Level(1)
				ui.Text(c, strings.Join(parts, " · ")).FontSize(12).TextColor(t.Muted).SingleLine().Ellipsis("…")
			})
			kit.StatusPill(c, status)
			if kit.Button(c.Key("terminate"), kit.ButtonOpts{Variant: kit.Danger, Label: "Terminar", Height: 34, Round: true, FontSize: 12,
				Tooltip: "Forzar cierre y borrar la sesión", Aria: "Terminar sesión"}).Clicked() {
				v.kill.Ask(s)
			}
			if v.tab == TabTerminal {
				observe := v.term != nil && v.term.observe
				label := "Control"
				if observe {
					label = "Observar"
				}
				if kit.Button(c.Key("mode"), kit.ButtonOpts{Variant: kit.Surface, Label: label, Height: 34, Round: true, FontSize: 12,
					Tooltip: "Cambiar modo de control", Aria: "Cambiar modo de control"}).Clicked() {
					v.setObserve(!observe)
				}
			} else if s.Source == backend.SourceAgentd {
				kit.IconButton(c.Key("more"), kit.IconMore, "Más acciones").Menu(func(m *ui.Menu) {
					if m.Item("Interrumpir").Chosen() {
						v.interrupt(s)
					}
					if m.Item("Ver buzón del host").Chosen() {
						v.a.Navigate("/mailbox/" + s.HostID)
					}
				})
			}
		})
		if s.Source == backend.SourceAgentd {
			tab := v.tab
			if kit.Segmented(c.Key("tabs"), &tab, []string{"Chat", "Terminal"}, nil) {
				v.tab = tab
			}
		}
	})
}

func (v *View) interrupt(s backend.Session) {
	a := v.a
	if s.Source != backend.SourceAgentd {
		if v.term != nil {
			v.term.send([]byte{0x03})
		}
		return
	}
	a.Go(func(ctx context.Context) {
		if _, err := a.Backend.Act(ctx, s, proto.ActArgs{Name: s.Name, Action: "interrupt", Client: "agentd-desktop"}); err != nil {
			a.Toasts.Error("No se pudo interrumpir", app.ErrorText(err))
		}
	})
}
