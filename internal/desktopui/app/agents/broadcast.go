package agents

import (
	"context"
	"sort"
	"strings"

	"github.com/digiogithub/agentd/internal/desktopui/app"
	"github.com/digiogithub/agentd/internal/desktopui/app/session"
	"github.com/digiogithub/agentd/internal/desktopui/backend"
	"github.com/digiogithub/agentd/internal/desktopui/kit"
	"github.com/digiogithub/agentd/internal/proto"
	"github.com/egoist/mygo/ui"
)

// broadcast is the selection mode of the list: several sessions picked and
// one prompt sent to all of them (UI thread only).
type broadcast struct {
	on      bool
	picked  map[string]bool // session keys
	draft   string
	sending bool
}

// Selecting reports whether the list is in selection mode (tests).
func (v *View) Selecting() bool { return v.sel.on }

// selectable reports whether a prompt can be sent to s from the list: an
// agentd session that is running, on a host whose device may operate.
func (v *View) selectable(s backend.Session) bool {
	return s.Source == backend.SourceAgentd && s.Status != "exited" && s.Status != "starting" &&
		v.a.Live.Allows(s.HostID, backend.CapOperate)
}

func (v *View) toggleSelecting() {
	v.sel.on = !v.sel.on
	if !v.sel.on {
		v.sel.picked = map[string]bool{}
	}
}

// targets returns the picked sessions that are still listed and selectable,
// in list order.
func (v *View) targets() []backend.Session {
	var out []backend.Session
	for _, s := range v.a.Live.Sessions {
		if v.sel.picked[s.Key()] && v.selectable(s) {
			out = append(out, s)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Key() < out[j].Key() })
	return out
}

// pickBar is the row of quick picks under the filter chips: all the shown
// sessions, those of one host, none.
func (v *View) pickBar(c *ui.Context, t *kit.Tokens, shown []entry) {
	var hosts []string
	perHost := map[string][]string{}
	var all []string
	for _, e := range shown {
		if !v.selectable(e.s) {
			continue
		}
		if _, ok := perHost[e.s.HostID]; !ok {
			hosts = append(hosts, e.s.HostID)
		}
		perHost[e.s.HostID] = append(perHost[e.s.HostID], e.s.Key())
		all = append(all, e.s.Key())
	}
	sort.Strings(hosts)
	pick := func(keys []string) {
		for _, k := range keys {
			v.sel.picked[k] = true
		}
	}
	ui.Row(c).Gap(8).Wrap().GapY(8).AlignItems(ui.Center).Children(func() {
		ui.Text(c, "Seleccionar:").FontSize(12).TextColor(t.Muted)
		if kit.Chip(c.Key("pick-all"), "Todos · "+itoa(len(all)), false, false).Clicked() {
			pick(all)
		}
		if len(hosts) > 1 {
			for _, h := range hosts {
				name := v.a.Live.HostName(h)
				if name == "" {
					name = h
				}
				if kit.Chip(c.Key("pick-host:"+h), name+" · "+itoa(len(perHost[h])), false, false).Clicked() {
					pick(perHost[h])
				}
			}
		}
		if kit.Chip(c.Key("pick-none"), "Ninguno", false, false).Clicked() {
			v.sel.picked = map[string]bool{}
		}
	})
}

// pickRow is a list row in selection mode: a checkbox instead of a link.
func (v *View) pickRow(c *ui.Context, t *kit.Tokens, e entry) {
	s := e.s
	ok := v.selectable(s)
	on := ok && v.sel.picked[s.Key()]
	sub := kit.StatusLabel(s.Status)
	switch {
	case s.Source != backend.SourceAgentd:
		sub = "no admite prompts desde aquí"
	case !v.a.Live.Allows(s.HostID, backend.CapOperate):
		sub += " · solo lectura en este dispositivo"
	}
	if h := v.a.Live.HostName(s.HostID); h != "" {
		sub = h + " · " + sub
	}
	border := t.Border
	if on {
		border = t.Accent
	}
	b := ui.ButtonBase(c).Label("Seleccionar "+app.SessionTitle(s)).Role(ui.RoleCheckBox).Checked(on).Padding(12, 14).Gap(12).
		Radius(18).Background(t.Surface).Border(1, border).AlignItems(ui.Center)
	if ok {
		b.Cursor(ui.CursorPointer)
		if b.Hovered() {
			b.Background(t.Raised)
		}
	} else {
		b.Opacity(0.5)
	}
	b.Children(func() {
		box := ui.Box(c).Size(22, 22).Radius(7).Shrink(0).AlignItems(ui.Center).Justify(ui.Center).Border(2, t.Border)
		if on {
			box.Background(t.Accent).Border(2, t.Accent).Children(func() {
				kit.Icon(c, kit.IconCheck).FontSize(14).TextColor(t.Bg)
			})
		}
		ui.Column(c).Grow(1).Shrink(1).MinWidth(0).Gap(3).Children(func() {
			ui.Text(c, app.SessionTitle(s)).FontSize(15).FontWeight(600).SingleLine().Ellipsis("…")
			ui.Text(c, sub).FontSize(12).TextColor(t.Muted).SingleLine().Ellipsis("…")
		})
	})
	if ok && b.Clicked() {
		if on {
			delete(v.sel.picked, s.Key())
		} else {
			v.sel.picked[s.Key()] = true
		}
	}
}

// broadcastBar is the composer under the list in selection mode.
func (v *View) broadcastBar(c *ui.Context, t *kit.Tokens) {
	if !v.sel.on {
		return
	}
	n := len(v.targets())
	ui.Column(c.Key("broadcast")).Shrink(0).Padding(10, 16, 14, 16).Gap(8).BorderWidth(1, 0, 0, 0).BorderColor(t.Divider).Children(func() {
		msg := "Elige a quién enviar el prompt"
		if n > 0 {
			msg = app.Plural(n, "agente seleccionado", "agentes seleccionados")
		}
		ui.Text(c, msg).FontSize(12).TextColor(t.Muted)
		ui.Row(c).Gap(8).AlignItems(ui.End).Children(func() {
			ui.Row(c).Grow(1).Shrink(1).MinWidth(0).MinHeight(44).Padding(4, 16).Radius(22).Background(t.Surface).
				Border(1, t.Border).AlignItems(ui.Center).Children(func() {
				ui.TextAreaBase(c.Key("broadcast-text"), &v.sel.draft).Lines(1, 5).Placeholder("Prompt para los seleccionados").
					Label("Prompt para los agentes seleccionados").FontSize(14.5).Grow(1).Shrink(1).Padding(8, 0).
					OnTextKey(func(k ui.TextKey) bool {
						if k.Key == ui.KeyEnter && k.Mods == 0 {
							v.sel.draft = k.Text()
							v.sendAll()
							return true
						}
						return false
					})
			})
			label := "Enviar"
			if n > 0 {
				label = "Enviar a " + itoa(n)
			}
			if kit.Button(c.Key("broadcast-send"), kit.ButtonOpts{Variant: kit.Primary, Icon: kit.IconArrowUp, Label: label, Height: 44,
				Round: true, Disabled: v.sel.sending || n == 0 || strings.TrimSpace(v.sel.draft) == ""}).Clicked() {
				v.sendAll()
			}
		})
	})
}

// sendAll sends the draft as an act prompt to every picked session, each on
// its own goroutine (forced while the agent works, as the chat does). The
// sessions that took it are unselected; refusals stay selected and are named
// in one toast.
func (v *View) sendAll() {
	text := strings.TrimSpace(v.sel.draft)
	targets := v.targets()
	if text == "" || len(targets) == 0 || v.sel.sending {
		return
	}
	v.sel.sending = true
	a := v.a
	left, sent := len(targets), 0
	var failed []string
	var firstErr error
	for _, s := range targets {
		args := proto.ActArgs{Name: s.Name, Action: proto.ActPrompt, Text: text, Client: "agentd-desktop", Force: s.Status == "working"}
		a.Go(func(ctx context.Context) {
			_, err := a.Backend.Act(ctx, s, args)
			a.Update(func() {
				left--
				if err != nil {
					failed = append(failed, app.SessionTitle(s))
					if firstErr == nil {
						firstErr = err
					}
				} else {
					sent++
					delete(v.sel.picked, s.Key())
				}
				if left > 0 {
					return
				}
				v.sel.sending = false
				if sent > 0 {
					a.Toasts.Success("Prompt enviado a " + app.Plural(sent, "agente", "agentes"))
				}
				if len(failed) == 0 {
					if strings.TrimSpace(v.sel.draft) == text {
						v.sel.draft = ""
					}
					v.sel.on = false
					v.sel.picked = map[string]bool{}
					return
				}
				sort.Strings(failed)
				_, detail := session.SendErrorText(firstErr)
				a.Toasts.Error("No se pudo enviar a "+strings.Join(failed, ", "), detail)
			})
		})
	}
}
