package session

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"regexp"
	"strconv"
	"strings"

	"github.com/digiogithub/agentd/internal/desktopui/app"
	"github.com/digiogithub/agentd/internal/desktopui/backend"
	"github.com/digiogithub/agentd/internal/desktopui/kit"
	"github.com/digiogithub/agentd/internal/proto"
	"github.com/egoist/mygo/ui"
)

// ItemKind is the role of a chat entry derived from transcript lines.
type ItemKind int

// Item kinds.
const (
	ItemUser ItemKind = iota
	ItemAgent
	ItemTool
	ItemSystem
)

// Item is one chat entry: a user prompt, agent prose, a tool call with its
// output, or a status line.
type Item struct {
	Kind  ItemKind
	Title string // tool: the call ("go test ./...")
	Meta  string // tool: "4,1 s", "+42 −7"
	Text  string // prose (agent: chat markup, CommonMark), prompt, or the tool's output
	OK    *bool  // tool outcome when the marker tells it (✓ / ✗)
}

var (
	userPrefix = []string{"❯ ", "› ", "> "}
	toolMarks  = []string{"● ", "• ", "⏺ ", "✓ ", "✗ ", "✔ ", "✘ "}
	toolCallRe = regexp.MustCompile(`^(?:[A-Z][A-Za-z]+\(.*\)|(?:Edited|Ran|Read|Wrote|Updated|Created|Deleted|Searched|Explored)\b.*|[a-z][\w.-]* .*)$`)
	durationRe = regexp.MustCompile(`\s+(\d+(?:[.,]\d+)?\s?(?:ms|s|min))$`)
	diffRe     = regexp.MustCompile(`\s*\((\+\d+\s*[−-]\d+)\)$`)
)

// Items turns view lines into chat entries. Dialog lines are left out when
// the dialog shows as the question card. Agent prose is built from the lines'
// chat markup (ViewLine.Md, else the text), with a blank line kept as a
// paragraph break.
func Items(lines []proto.ViewLine, hideDialog bool) []Item {
	var out []Item
	last := func() *Item {
		if len(out) == 0 {
			return nil
		}
		return &out[len(out)-1]
	}
	blank := false // a blank line since the last agent text
	addAgent := func(it *Item, md string) {
		switch {
		case it.Text == "":
		case blank:
			it.Text += "\n\n"
		default:
			it.Text += "\n"
		}
		it.Text += md
		blank = false
	}
	for _, l := range lines {
		text := strings.TrimRight(l.Text, " ")
		md := l.Md
		if md == "" {
			md = l.Text
		}
		md = strings.TrimRight(md, " ")
		switch l.Role {
		case "dialog":
			if hideDialog {
				continue
			}
		case "gap":
			out = append(out, Item{Kind: ItemSystem, Text: "…"})
			continue
		case "status":
			if strings.TrimSpace(text) != "" {
				out = append(out, Item{Kind: ItemSystem, Text: strings.TrimSpace(text)})
			}
			continue
		}
		if strings.TrimSpace(text) == "" {
			blank = true
			continue
		}
		if p, ok := cutAny(text, userPrefix); ok {
			out = append(out, Item{Kind: ItemUser, Text: p})
			blank = false
			continue
		}
		indented := strings.HasPrefix(text, " ") || strings.HasPrefix(text, "⎿")
		if indented {
			body := strings.TrimLeft(strings.TrimSpace(text), "⎿ ")
			mdBody := strings.TrimLeft(strings.TrimSpace(md), "⎿ ")
			if it := last(); it != nil && it.Kind == ItemAgent {
				addAgent(it, mdBody)
				continue
			}
			if it := last(); it != nil && it.Kind == ItemTool {
				if it.Text != "" {
					it.Text += "\n"
				}
				it.Text += body
				continue
			}
			out = append(out, Item{Kind: ItemAgent, Text: mdBody})
			blank = false
			continue
		}
		mark := ""
		for _, m := range toolMarks {
			if strings.HasPrefix(text, m) {
				mark = m
				break
			}
		}
		rest := strings.TrimPrefix(text, mark)
		if mark != "" && (toolCallRe.MatchString(rest) || strings.HasPrefix(mark, "✓") || strings.HasPrefix(mark, "✗") ||
			strings.HasPrefix(mark, "✔") || strings.HasPrefix(mark, "✘")) && len(rest) < 160 {
			it := Item{Kind: ItemTool, Title: rest}
			if m := durationRe.FindStringSubmatch(it.Title); m != nil {
				it.Meta, it.Title = m[1], strings.TrimSpace(strings.TrimSuffix(it.Title, m[0]))
			} else if m := diffRe.FindStringSubmatch(it.Title); m != nil {
				it.Meta, it.Title = strings.ReplaceAll(m[1], "-", "−"), strings.TrimSpace(strings.TrimSuffix(it.Title, m[0]))
			}
			switch {
			case strings.HasPrefix(mark, "✓"), strings.HasPrefix(mark, "✔"):
				ok := true
				it.OK = &ok
			case strings.HasPrefix(mark, "✗"), strings.HasPrefix(mark, "✘"):
				ok := false
				it.OK = &ok
			}
			out = append(out, it)
			blank = false
			continue
		}
		mdRest := strings.TrimPrefix(md, mark)
		if it := last(); it != nil && it.Kind == ItemAgent && mark == "" {
			addAgent(it, mdRest)
			continue
		}
		out = append(out, Item{Kind: ItemAgent, Text: mdRest})
		blank = false
	}
	return out
}

func cutAny(s string, prefixes []string) (string, bool) {
	for _, p := range prefixes {
		if after, ok := strings.CutPrefix(s, p); ok {
			return strings.TrimSpace(after), true
		}
	}
	return "", false
}

// followSlack is how far (DIPs) from the end still counts as the bottom.
const followSlack = 24

func (v *View) chatTab(c *ui.Context, t *kit.Tokens, s backend.Session, view *proto.View, viewErr error) {
	if view == nil {
		ui.Column(c).Grow(1).AlignItems(ui.Center).Justify(ui.Center).Children(func() {
			if viewErr != nil {
				ui.Text(c, "No se pudo cargar la vista").FontSize(14).TextColor(t.Danger)
				if kit.Button(c.Key("retry-view"), kit.ButtonOpts{Variant: kit.Outline, Label: "Reintentar"}).Clicked() {
					v.a.Views.Restart(s)
				}
				return
			}
			kit.Spinner(c).FontSize(22).TextColor(t.Muted)
		})
		return
	}
	pending := app.Pending(view)
	items := Items(view.Lines, pending)
	// Follow the newest line while the user is at the bottom; once they
	// scroll up the view stays where it is, until they come back down (or use
	// the button). The state of the last frame tells where they are.
	v.following = v.scroll.MaxY-v.scroll.Y <= followSlack || v.jump
	v.jump = false
	if v.following {
		v.scroll.Y = math.MaxFloat32
	}
	ui.Scroll(c.Key("chat")).TrackScroll(&v.scroll).Grow(1).MinHeight(0).Padding(16, 20, 8, 20).Gap(14).Children(func() {
		ui.Column(c).Gap(14).MaxWidth(860).FillWidth().AlignSelf(ui.Center).Children(func() {
			for i, it := range items {
				v.item(c.Key(i), t, i, it)
			}
			if pending {
				v.pendingCard(c.Key("pending:"+v.pendingIdentity(view)), t, s, view, v.a.Live.Allows(s.HostID, backend.CapOperate))
			}
		})
	})
	if !v.following {
		ui.Row(c).Shrink(0).Justify(ui.Center).Padding(0, 0, 6, 0).Children(func() {
			if kit.Button(c.Key("to-bottom"), kit.ButtonOpts{Variant: kit.Outline, Icon: kit.IconChevronDown, Tooltip: "Ir al final",
				Label: "Ir al final", Height: 34}).Clicked() {
				v.jump = true
			}
		})
	}
	if !v.a.Live.Allows(s.HostID, backend.CapOperate) {
		ui.Row(c).Shrink(0).Padding(12, 20, 14, 20).Gap(8).BorderWidth(1, 0, 0, 0).BorderColor(t.Divider).Justify(ui.Center).Children(func() {
			kit.Icon(c, kit.IconLock).FontSize(14).TextColor(t.Muted)
			ui.Text(c, "Solo ver: este dispositivo no puede escribir ni responder en este host.").FontSize(12.5).TextColor(t.Muted)
		})
		return
	}
	v.composer(c, t, s, view)
}

func (v *View) item(c *ui.Context, t *kit.Tokens, i int, it Item) {
	switch it.Kind {
	case ItemUser:
		ui.Row(c).Justify(ui.End).Children(func() {
			ui.Column(c).MaxWidthPercent(82).Padding(10, 14).Radius(18, 18, 6, 18).Background(t.AccentBg).Children(func() {
				v.prose(c, t, it.Text, v.a.Prefs.Get().ChatSize(), 1.45, false)
			})
		})
	case ItemAgent:
		v.agentBody(c, t, i, it.Text)
	case ItemSystem:
		ui.Text(c, it.Text).FontSize(12).TextColor(t.Muted).TextAlign(ui.Center).FillWidth()
	case ItemTool:
		hasBody := strings.TrimSpace(it.Text) != ""
		open := v.expanded[i]
		card := ui.ButtonBase(c).Label(it.Title).Column().Padding(0, 12).Radius(12).Background(t.Surface).
			Border(1, t.Border).MaxWidthPercent(92).AlignItems(ui.Stretch).Expanded(open)
		if hasBody {
			card.Cursor(ui.CursorPointer)
			if card.Clicked() {
				v.expanded[i] = !open
				open = !open
			}
		}
		card.Children(func() {
			ui.Row(c).Height(44).Gap(8).AlignItems(ui.Center).Children(func() {
				icon, col := kit.IconFile, t.Muted
				if it.OK != nil && *it.OK {
					icon, col = kit.IconCheck, t.Success
				} else if it.OK != nil {
					icon, col = kit.IconClose, t.Danger
				}
				kit.Icon(c, icon).FontSize(16).TextColor(col)
				ui.Text(c, it.Title).Font(kit.MonoFamily()).FontSize(13).TextColor(t.Secondary).SingleLine().Ellipsis("…").Grow(1).Shrink(1)
				if it.Meta != "" {
					ui.Text(c, it.Meta).FontSize(12).TextColor(t.Muted).SingleLine()
				}
				if hasBody {
					ic := kit.IconChevronDown
					if open {
						ic = kit.IconChevronUp
					}
					kit.Icon(c, ic).FontSize(16).TextColor(t.Muted)
				}
			})
			if open && hasBody {
				ui.Column(c).Margin(0, 0, 10, 0).AlignItems(ui.Stretch).Children(func() {
					v.prose(c, t, it.Text, v.a.Prefs.Get().ChatSize()*0.83, 1.45, true)
				})
			}
		})
	}
}

// pendingIdentity is what the selection of the pending card belongs to.
func (v *View) pendingIdentity(view *proto.View) string {
	var b strings.Builder
	if view.Dialog != nil {
		b.WriteString(view.Dialog.Question)
	}
	for _, o := range view.Options {
		b.WriteString("|" + o.ID)
	}
	return b.String()
}

// pendingCard shows the pending question; canAnswer false (a view-only
// relay device) shows it read only.
func (v *View) pendingCard(c *ui.Context, t *kit.Tokens, s backend.Session, view *proto.View, canAnswer bool) {
	if id := v.pendingIdentity(view); id != v.pendingKey {
		v.pendingKey, v.selected = id, ""
		for _, o := range view.Options {
			if o.Selected {
				v.selected = o.ID
			}
		}
	}
	permission := app.IsPermission(view)
	question := "Esperando una respuesta"
	if view.Dialog != nil && view.Dialog.Question != "" {
		question = view.Dialog.Question
	}
	ui.Column(c).Gap(10).Padding(14).Radius(18).Background(t.QuestionBg).Border(1, t.QuestionBorder).Label("Pregunta del agente").Children(func() {
		label := "Pregunta"
		if permission {
			label = "Permiso"
		}
		kit.SectionLabel(c, label, t.Waiting)
		q := ui.Text(c, question).LineHeight(1.4).Selectable()
		if permission {
			q.Font(kit.MonoFamily()).FontSize(13)
		} else {
			q.FontSize(15).FontWeight(500)
		}
		for i, o := range view.Options {
			cc := c.Key("opt:" + o.ID)
			n := o.Index
			if n <= 0 {
				n = i + 1
			}
			on := o.ID == v.selected
			b := ui.ButtonBase(cc).Label(o.Label).MinHeight(48).Padding(10, 12).Gap(10).Radius(12).AlignItems(ui.Center).
				Cursor(ui.CursorPointer).Selected(on).Disabled(v.answering || !canAnswer).TextColor(t.Text)
			if on {
				b.Border(1, t.Waiting).Background(t.WaitingBg)
			} else {
				b.Border(1, t.Border).Background(t.Surface)
			}
			b.Children(func() {
				ui.Row(cc).Size(26, 26).Shrink(0).Radius(8).Background(t.Raised).AlignItems(ui.Center).Justify(ui.Center).Children(func() {
					ui.Text(cc, strconv.Itoa(n)).Font(kit.MonoFamily()).FontSize(12.5).FontWeight(600).TextColor(t.Secondary)
				})
				ui.Text(cc, o.Label).FontSize(14).LineHeight(1.35).Grow(1).Shrink(1)
			})
			if b.Clicked() && !v.answering && canAnswer {
				v.selected = o.ID
			}
		}
		if !canAnswer {
			return
		}
		ui.Row(c).Gap(8).Children(func() {
			var sel *proto.ViewOption
			for i := range view.Options {
				if view.Options[i].ID == v.selected {
					sel = &view.Options[i]
				}
			}
			label := "Responder"
			if v.answering {
				label = "Respondiendo…"
			}
			if kit.Button(c.Key("answer"), kit.ButtonOpts{Variant: kit.Waiting, Label: label, Grow: true, Height: 44,
				Disabled: sel == nil || v.answering}).Clicked() && sel != nil && !v.answering {
				v.choose(s, view, *sel)
			}
			if kit.Button(c.Key("other"), kit.ButtonOpts{Variant: kit.Outline, Label: "Escribir otra", Height: 44}).Clicked() {
				v.focusDraft = true
			}
		})
	})
}

// Choose answers the pending question with option o of view (act choose
// with the view's revision and epoch).
func Choose(a *app.Context, s backend.Session, view *proto.View, o proto.ViewOption, done func(err error)) {
	args := proto.ActArgs{Name: s.Name, Action: "choose", OptionID: o.ID, Label: o.Label, Revision: view.Revision,
		Epoch: view.Epoch, Client: "agentd-desktop"}
	a.Go(func(ctx context.Context) {
		_, err := a.Backend.Act(ctx, s, args)
		a.Update(func() {
			if err != nil {
				if cv := app.ConflictView(err); cv != nil {
					a.Views.Set(s, cv)
				}
			}
			done(err)
		})
	})
}

func (v *View) choose(s backend.Session, view *proto.View, o proto.ViewOption) {
	v.answering = true
	Choose(v.a, s, view, o, func(err error) {
		v.answering = false
		if err != nil {
			v.a.Toasts.Error("No se pudo responder", app.ErrorText(err))
		}
	})
}

func (v *View) composer(c *ui.Context, t *kit.Tokens, s backend.Session, view *proto.View) {
	ui.Column(c).Shrink(0).Padding(8, 20, 14, 20).Gap(8).BorderWidth(1, 0, 0, 0).BorderColor(t.Divider).Children(func() {
		ui.Column(c).MaxWidth(860).FillWidth().AlignSelf(ui.Center).Gap(8).Children(func() {
			if v.queued > 0 {
				ui.Row(c).Gap(6).Padding(0, 0, 0, 6).AlignItems(ui.Center).Children(func() {
					kit.Icon(c, kit.IconClock).FontSize(14).TextColor(t.Muted)
					msg := "1 mensaje en cola"
					if v.queued > 1 {
						msg = strconv.Itoa(v.queued) + " mensajes en cola"
					}
					ui.Text(c, msg+": se entrega cuando el agente quede libre").FontSize(12).TextColor(t.Muted).Grow(1)
					if ui.ButtonBase(c.Key("see-queued")).Label("Ver mensajes en cola").Cursor(ui.CursorPointer).Padding(4, 8).
						Children(func() { ui.Text(c, "Ver").FontSize(12).TextColor(t.Accent) }).Clicked() {
						v.a.Navigate("/mailbox/" + s.HostID)
					}
				})
			}
			ui.Row(c).Gap(8).AlignItems(ui.End).Children(func() {
				if kit.IconButton(c.Key("attach"), kit.IconPlus, "Adjuntar imagen").Clicked() {
					v.a.Toasts.Info("Adjuntar imágenes llegará pronto")
				}
				ui.Row(c).Grow(1).Shrink(1).MinWidth(0).MinHeight(44).Padding(4, 6, 4, 16).Radius(22).Background(t.Surface).
					Border(1, t.Border).AlignItems(ui.Center).Gap(4).Children(func() {
					in := ui.TextAreaBase(c.Key("composer"), &v.draft).Lines(1, 5).Placeholder("Mensaje, / comandos, @ ficheros").
						Label("Mensaje para el agente").FontSize(14.5).Grow(1).Shrink(1).Padding(8, 0).
						OnTextKey(func(k ui.TextKey) bool {
							if k.Key == ui.KeyEnter && k.Mods == 0 {
								v.send(s, view, k.Text())
								return true
							}
							return false
						})
					if v.focusDraft {
						v.focusDraft = false
						in.Focus()
					}
					if ui.ButtonBase(c.Key("mic")).Label("Dictar").Tooltip("Dictar").Size(32, 32).Radius(16).AlignItems(ui.Center).
						Justify(ui.Center).Cursor(ui.CursorPointer).TextColor(t.Muted).
						Children(func() { kit.Icon(c, kit.IconMic).FontSize(17) }).Clicked() {
						v.a.Toasts.Info("El dictado llegará pronto")
					}
				})
				if kit.Button(c.Key("send"), kit.ButtonOpts{Variant: kit.Primary, Icon: kit.IconArrowUp, Tooltip: "Enviar", Height: 44,
					Round: true, Disabled: v.sending || strings.TrimSpace(v.draft) == ""}).Clicked() {
					v.send(s, view, v.draft)
				}
			})
		})
	})
}

func (v *View) send(s backend.Session, view *proto.View, text string) {
	text = strings.TrimSpace(text)
	if text == "" || v.sending {
		return
	}
	v.sending = true
	args := proto.ActArgs{Name: s.Name, Action: "prompt", Text: text, Client: "agentd-desktop"}
	if view != nil {
		args.Revision, args.Epoch = view.Revision, view.Epoch
		// Most agents take a message in the middle of a turn: do not wait for idle.
		args.Force = view.Status == "working"
	}
	v.jump = true
	a := v.a
	a.Go(func(ctx context.Context) {
		_, err := a.Backend.Act(ctx, s, args)
		a.Update(func() {
			v.sending = false
			if err != nil {
				if cv := app.ConflictView(err); cv != nil {
					a.Views.Set(s, cv)
				}
				title, detail := SendErrorText(err)
				a.Toasts.Error(title, detail)
				return
			}
			if strings.TrimSpace(v.draft) == text {
				v.draft = ""
			}
		})
	})
}

// SendErrorText maps the error of a failed prompt act to the toast title and
// detail, by the daemon's error code.
func SendErrorText(err error) (title, detail string) {
	switch proto.CodeOf(err) {
	case proto.CodeBusy:
		return busyText(err), ""
	case proto.CodeNotReady:
		return "El agente está trabajando o esperando una respuesta: espera a que quede libre", ""
	case proto.CodeVerifyFailed:
		return "El agente no mostró el texto pegado y no se pulsó Enter. Puede haber quedado en su entrada: revisa la pestaña Terminal antes de reintentar", ""
	case proto.CodeExited:
		return "La sesión ha terminado", ""
	case proto.CodeInvalid:
		return "El texto está vacío o es demasiado largo (máx. 64 KiB)", ""
	case proto.CodeConflict:
		return "La pantalla del agente cambió: revisa la vista y reintenta", ""
	case proto.CodeTimeout:
		return "El agente no responde", ""
	}
	return "No se pudo enviar", app.ErrorText(err)
}

// busyText explains a busy act error by its reason (proto.ActBusy; daemons
// before 0.5.1 send none).
func busyText(err error) string {
	var re *proto.RemoteError
	var b proto.ActBusy
	if errors.As(err, &re) && len(re.Data) > 0 && json.Unmarshal(re.Data, &b) == nil {
		switch b.Reason {
		case proto.BusyInput:
			return "El agente ya tiene texto en su entrada (quizá de un envío anterior). Envíalo o bórralo en la pestaña Terminal y reintenta"
		case proto.BusyDraft:
			return "Hay un borrador sin enviar en la entrada del agente (tecleado en la terminal o pegado sin Enter). Envíalo o bórralo en la Terminal"
		case proto.BusyHuman:
			return "Acabas de teclear en la terminal; espera unos segundos y reintenta"
		case proto.BusyLock:
			return "El agente está recibiendo otra entrada (un mensaje o acción); reintenta en un momento"
		}
	}
	return "Hay texto pendiente en la entrada del agente o acabas de teclear en la terminal"
}
