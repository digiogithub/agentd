package panel

import (
	"fmt"
	"net/url"
	"strconv"
	"time"

	"github.com/digiogithub/agentd/internal/desktopui/app"
	"github.com/digiogithub/agentd/internal/desktopui/backend"
	"github.com/digiogithub/agentd/internal/desktopui/kit"
	"github.com/digiogithub/agentd/internal/proto"
	"github.com/egoist/mygo/ui"
)

// routeParam returns an unescaped router parameter.
func routeParam(r *ui.Route, key string) string {
	if r == nil {
		return ""
	}
	p := r.Param(key)
	if u, err := url.PathUnescape(p); err == nil {
		return u
	}
	return p
}

// DetailMaxWidth caps the width of the agent detail content.
const DetailMaxWidth = 880

// openDetail shows the work of one agent (a card click).
func (v *View) openDetail(s backend.Session) { v.a.Navigate(AgentPath(s.HostID, s.Name)) }

// detail is the page of one agent: its tasks grouped by state and its open
// questions (the mobile app's agent screen). A page, not a split: "Abrir
// agente" goes to the chat.
func (v *View) detail(c *ui.Context, hostID, name string) {
	t := kit.Current()
	a := v.a
	now := v.Now()
	ag, ok := FindAgent(a.Live, hostID, name)
	ui.Column(c).Grow(1).MinHeight(0).AlignItems(ui.Stretch).Children(func() {
		sub := v.hostLabel(hostID)
		if ok && ag.Session.Info != nil && ag.Session.Info.Cwd != "" {
			sub += " · " + ag.Session.Info.Cwd
		}
		back := kit.RouteHeader(c, name, sub, func() {
			if !ok {
				return
			}
			kit.StatusPill(c, ag.Status())
			if kit.Button(c.Key("open-chat"), kit.ButtonOpts{Variant: kit.Primary, Label: "Abrir agente", Height: 36, FontSize: 13,
				Aria: "Abrir " + name}).Clicked() {
				v.open(ag.Session)
			}
		})
		if back {
			a.Router.Replace(app.PanelPath)
		}
		if !ok {
			if !a.Live.SessionsLoaded {
				kit.EmptyState(c, "", "", "Cargando…")
				return
			}
			kit.EmptyState(c, kit.IconPanel, "Esta sesión ya no está en el panel", "Puede haber terminado o haberse eliminado.")
			return
		}
		tasks := ag.Tasks()
		done, total := Progress(tasks)
		views := 0
		ui.Scroll(c.Key("detail")).Grow(1).MinHeight(0).Padding(16, 24, 24, 24).Children(func() {
			ui.Column(c).MaxWidth(DetailMaxWidth).Gap(16).Children(func() {
				if total > 0 {
					ui.Row(c).Gap(10).AlignItems(ui.Center).Children(func() {
						ui.Row(c).Height(5).Grow(1).Shrink(1).Radius(3).Background(t.Raised2).Children(func() {
							if done > 0 {
								ui.Box(c).Height(5).Radius(3).Background(t.Working).WidthPercent(float32(done) * 100 / float32(total))
							}
						})
						txt := fmt.Sprintf("%d / %d", done, total)
						if last := lastUpdate(tasks); !last.IsZero() {
							txt += " · " + app.RelativeTime(last, now)
						}
						ui.Text(c, txt).FontSize(12).TextColor(t.Secondary).SingleLine()
					})
				}
				if qs := ag.Questions(); len(qs) > 0 {
					ui.Column(c).Gap(10).Children(func() {
						kit.SectionLabel(c, "Te pregunta", t.Waiting)
						for _, q := range qs {
							it := Item{Kind: ItemQuestion, Agent: ag, Question: q, Since: q.CreatedAt}
							v.questionCard(c.Key(it.Key()), t, it, now, &views)
						}
					})
				}
				for _, g := range TaskGroups(tasks) {
					v.taskGroup(c.Key("g:"+g.Status), t, ag, g, now)
				}
				if len(tasks) == 0 {
					ui.Column(c).Gap(6).Children(func() {
						kit.SectionLabel(c, "Sin tareas informadas", t.Muted)
						ui.Text(c, "Este agente no ha informado de tareas.").FontSize(13.5).TextColor(t.Muted)
						if p := ag.Session.Preview; p != "" {
							ui.Text(c, p).Font(kit.MonoFamily()).FontSize(12.5).TextColor(t.Secondary).LineHeight(1.5).Selectable()
						}
					})
				}
			})
		})
	})
}

func lastUpdate(ts []proto.Task) time.Time {
	var last time.Time
	for _, t := range ts {
		if t.UpdatedAt.After(last) {
			last = t.UpdatedAt
		}
	}
	return last
}

// taskGroup is one section of the detail. Canceladas stays folded.
func (v *View) taskGroup(c *ui.Context, t *kit.Tokens, ag Agent, g Group, now time.Time) {
	colors := map[string]ui.Color{
		proto.TaskInProgress: t.Working, proto.TaskBlocked: t.Danger, proto.TaskPending: t.Muted,
		proto.TaskDone: t.SuccessText, proto.TaskCancelled: t.Faint,
	}
	color := colors[g.Status]
	folded := g.Status == proto.TaskCancelled && !v.expanded["detail:cancelled"]
	ui.Column(c).Gap(8).Children(func() {
		head := fmt.Sprintf("%s · %d", g.Title, len(g.Tasks))
		if g.Status == proto.TaskCancelled {
			b := ui.ButtonBase(c.Key("fold")).Label(head).Padding(2, 0).AlignItems(ui.Center).Gap(6).Cursor(ui.CursorPointer)
			b.Children(func() {
				kit.SectionLabel(c, head, color)
				icon := kit.IconChevronR
				if !folded {
					icon = kit.IconChevronDown
				}
				kit.Icon(c, icon).FontSize(13).TextColor(t.Faint)
			})
			if b.Clicked() {
				v.expanded["detail:cancelled"] = folded
			}
		} else {
			kit.SectionLabel(c, head, color)
		}
		if folded {
			return
		}
		for i, tk := range g.Tasks {
			v.taskRow(c.Key("t:"+strconv.Itoa(i)), t, ag, tk, color, now)
		}
	})
}

func (v *View) taskRow(c *ui.Context, t *kit.Tokens, ag Agent, tk proto.Task, color ui.Color, now time.Time) {
	dim := tk.Status == proto.TaskCancelled || tk.Status == proto.TaskDone
	text := t.Text
	if dim {
		text = t.Muted
	}
	if tk.Status == proto.TaskCancelled {
		text = t.Faint
	}
	ui.Row(c).Gap(10).Padding(10, 12).Radius(10).Background(t.Surface).Border(1, t.Border).AlignItems(ui.Start).Label("Tarea: " + tk.Title).Children(func() {
		ui.Box(c).Size(8, 8).Radius(4).Background(color).Margin(6, 0, 0, 0).Shrink(0)
		ui.Column(c).Gap(3).Grow(1).Shrink(1).MinWidth(0).Children(func() {
			ui.Row(c).Gap(8).AlignItems(ui.Start).Children(func() {
				ui.Text(c, tk.Title).FontSize(13.5).LineHeight(1.4).TextColor(text).Grow(1).Shrink(1).MinWidth(0)
				if FromBacklog(tk) {
					kit.Pill(c, "del backlog", t.Secondary, t.Raised)
				}
			})
			if tk.Note != "" {
				ui.Text(c, tk.Note).FontSize(12.5).LineHeight(1.4).TextColor(t.Secondary)
			}
			for _, q := range ag.Questions() {
				if q.Task == tk.ID {
					ui.Text(c, "Pregunta abierta: "+oneLine(q.Body, 140)).FontSize(12).TextColor(t.Waiting)
				}
			}
		})
		ui.Column(c).Gap(4).Shrink(0).AlignItems(ui.End).Children(func() {
			if rt := app.RelativeTime(tk.UpdatedAt, now); rt != "" {
				ui.Text(c, rt).FontSize(11.5).TextColor(t.Muted).SingleLine()
			}
		})
	})
}
