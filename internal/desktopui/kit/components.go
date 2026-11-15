package kit

import (
	"strings"
	"sync"
	"time"

	"github.com/egoist/mygo/ui"
)

// Variant is the look of a button (the mockups' button styles).
type Variant int

// Button variants.
const (
	Primary Variant = iota // accent fill: Lanzar, Conectar y detectar
	Light                  // text-colour fill: Permitir
	Outline                // border only: Denegar, Escribir otra
	Danger                 // soft red: Interrumpir
	Waiting                // amber fill: Responder
	Ghost                  // no fill until hovered
	Surface                // surface fill with border: round icon buttons
)

// ButtonOpts configures Button.
type ButtonOpts struct {
	Variant Variant
	Label   string
	// Icon is an embedded icon name shown before the label.
	Icon string
	// Tooltip shows on hover and names icon-only buttons.
	Tooltip  string
	Disabled bool
	// Active draws a selected state (toggles).
	Active bool
	// Height defaults to 40; Round makes an icon button a circle.
	Height float32
	Round  bool
	// Grow fills the room of a row.
	Grow bool
	// FontSize defaults to 14.
	FontSize float32
	// Aria names the button for assistive technology (and tests) when the
	// visible label is not enough.
	Aria string
}

// Button is a button of the mockups on MyGo's ButtonBase. Read Clicked on
// the returned element; key it (c.Key) when built in a loop.
func Button(c *ui.Context, o ButtonOpts) ui.Element {
	t := Current()
	h := o.Height
	if h <= 0 {
		h = 40
	}
	fs := o.FontSize
	if fs <= 0 {
		fs = 14
	}
	b := ui.ButtonBase(c)
	padX := float32(16)
	iconOnly := o.Label == "" && o.Icon != ""
	if iconOnly {
		padX = 0
		b.Width(h)
	}
	radius := float32(12)
	if o.Round || iconOnly && o.Variant == Surface {
		radius = h / 2
	}
	b.Height(h).Padding(0, padX).Gap(8).AlignItems(ui.Center).Justify(ui.Center).
		Radius(radius).FontSize(fs).Cursor(ui.CursorPointer).Disabled(o.Disabled)
	if o.Grow {
		b.Grow(1).Basis(0)
	}
	switch {
	case o.Aria != "":
		b.Label(o.Aria)
	case o.Label != "":
		b.Label(o.Label)
	case o.Tooltip != "":
		b.Label(o.Tooltip)
	}
	if o.Tooltip != "" {
		b.Tooltip(o.Tooltip)
	}
	hot := (b.Hovered() || b.Pressed()) && !o.Disabled
	switch o.Variant {
	case Primary:
		bg := t.Accent
		if hot {
			bg = t.AccentLight
		}
		b.Background(bg).TextColor(t.OnAccent)
	case Light:
		bg := t.Text
		if hot {
			bg = t.Secondary
		}
		b.Background(bg).TextColor(t.Bg)
	case Outline:
		b.Border(1, t.Border).TextColor(t.Text)
		if hot || o.Active {
			b.Background(t.Raised)
		}
	case Danger:
		b.Background(t.DangerBg).Border(1, t.DangerBorder).TextColor(t.Danger)
		if hot {
			b.Background(t.DangerBorder)
		}
	case Waiting:
		bg := t.Waiting
		if hot {
			bg = t.Waiting.Mix(t.Text, 0.85)
		}
		b.Background(bg).TextColor(t.OnWaiting)
	case Ghost:
		b.TextColor(t.Muted)
		if hot || o.Active {
			b.Background(t.Raised).TextColor(t.Text)
		}
	default: // Surface
		b.Background(t.Surface).Border(1, t.Border).TextColor(t.Text)
		if hot || o.Active {
			b.Background(t.Raised)
		}
	}
	if o.Disabled {
		b.Opacity(0.45).Cursor(ui.CursorNotAllowed)
	}
	b.Children(func() {
		if o.Icon != "" {
			Icon(c, o.Icon).FontSize(fs + 3)
		}
		if o.Label != "" {
			ui.Text(c, o.Label).SingleLine().FontWeight(600)
		}
	})
	return b
}

// IconButton is a round surface button showing an icon, named by label.
func IconButton(c *ui.Context, icon, label string) ui.Element {
	return Button(c, ButtonOpts{Variant: Surface, Icon: icon, Tooltip: label, Height: 36, Round: true})
}

// Pill is a small rounded label with its own colours (status pills,
// Permiso/Pregunta).
func Pill(c *ui.Context, text string, fg, bg ui.Color) ui.Element {
	return ui.Row(c).Padding(3, 8).Radius(999).Background(bg).AlignItems(ui.Center).Shrink(0).Children(func() {
		ui.Text(c, text).FontSize(11).FontWeight(600).TextColor(fg).SingleLine()
	})
}

// StatusColors returns the fill and text of a status pill.
func StatusColors(status string) (bg, fg ui.Color) {
	t := Current()
	switch status {
	case "waiting":
		return t.Waiting, t.OnWaiting
	case "working", "starting":
		return t.Working, t.OnWorking
	case "exited":
		return t.DangerBg, t.Danger
	}
	return t.Raised, t.Secondary
}

// StatusLabel is the Spanish label of an agentd view status.
func StatusLabel(status string) string {
	switch status {
	case "waiting":
		return "Esperando"
	case "working":
		return "Trabajando"
	case "starting":
		return "Arrancando"
	case "idle":
		return "Listo"
	case "exited":
		return "Terminada"
	}
	return "Desconocido"
}

// StatusPill is the pill of a session status.
func StatusPill(c *ui.Context, status string) ui.Element {
	bg, fg := StatusColors(status)
	return Pill(c, StatusLabel(status), fg, bg)
}

// TagChip is a small static tag (capabilities of a host).
func TagChip(c *ui.Context, text string, fg, bg ui.Color) ui.Element {
	t := Current()
	return ui.Row(c).Height(26).Padding(0, 10).Radius(8).Background(bg).Border(1, t.Border).AlignItems(ui.Center).
		Shrink(0).Children(func() {
		ui.Text(c, text).FontSize(12).TextColor(fg).SingleLine()
	})
}

// Chip is a selectable pill (filters, projects, recipients). Read Clicked.
func Chip(c *ui.Context, label string, selected, mono bool) ui.Element {
	t := Current()
	b := ui.ButtonBase(c).Label(label).Height(34).Padding(0, 14).Radius(17).AlignItems(ui.Center).
		Cursor(ui.CursorPointer).Shrink(0)
	if mono {
		b.Radius(10).Height(36)
	}
	b.Selected(selected)
	if selected {
		b.Border(1, t.Accent).Background(t.AccentBg).TextColor(t.AccentLight)
	} else {
		b.Border(1, t.Border).TextColor(t.Muted)
		if b.Hovered() {
			b.Background(t.Surface).TextColor(t.Text)
		}
	}
	b.Children(func() {
		txt := ui.Text(c, label).FontSize(13).FontWeight(500).SingleLine()
		if mono {
			txt.Font(MonoFamily())
		}
	})
	return b
}

// SectionLabel is the small uppercase heading of a list section.
func SectionLabel(c *ui.Context, text string, color ui.Color) ui.Element {
	return ui.Text(c, strings.ToUpper(text)).FontSize(12).FontWeight(600).LetterSpacing(0.7).TextColor(color)
}

// Avatar is the rounded square with a letter (or an icon) of a session.
func Avatar(c *ui.Context, letter, icon string, fg, bg ui.Color) ui.Element {
	return ui.Row(c).Size(36, 36).Shrink(0).Radius(10).Background(bg).AlignItems(ui.Center).Justify(ui.Center).
		Children(func() {
			if icon != "" {
				Icon(c, icon).FontSize(18).TextColor(fg)
				return
			}
			ui.Text(c, letter).FontSize(14).FontWeight(700).TextColor(fg)
		})
}

// Card is a surface panel with the mockups' 18 px radius. border may be
// zero for the default.
func Card(c *ui.Context, border ui.Color, fn func()) ui.Element {
	t := Current()
	if border == (ui.Color{}) {
		border = t.Border
	}
	return ui.Column(c).Gap(10).Padding(14).Radius(18).Background(t.Surface).Border(1, border).Children(fn)
}

// CodeBox is a mono text panel on the page background (commands, tool
// lines, public keys).
func CodeBox(c *ui.Context, text string) ui.Element {
	t := Current()
	return ui.Column(c).Padding(10, 12).Radius(10).Background(t.Bg).Border(1, t.Divider).Children(func() {
		ui.Text(c, text).Font(MonoFamily()).FontSize(12.5).TextColor(t.Secondary).LineHeight(1.5).Selectable()
	})
}

// Input is a single-line field with the mockups' look. Read Changed and
// Submitted on the returned element.
func Input(c *ui.Context, value *string, placeholder string, mono bool) ui.Element {
	t := Current()
	in := ui.TextInput(c, value).Placeholder(placeholder).Height(44).Padding(0, 12).
		Background(t.Surface).Border(1, t.Border).Radius(12).FontSize(14)
	if mono {
		in.Font(MonoFamily())
	}
	return in
}

// Field is a label above a control.
func Field(c *ui.Context, label string, fn func()) ui.Element {
	t := Current()
	return ui.Column(c).Gap(6).Children(func() {
		ui.Text(c, label).FontSize(13).TextColor(t.Secondary)
		fn()
	})
}

// Toggle is the mockups' switch (52x32 track). It flips *on when clicked
// and reports whether it did.
func Toggle(c *ui.Context, on *bool, label string) bool {
	t := Current()
	b := ui.ButtonBase(c).Label(label).Role(ui.RoleSwitch).Checked(*on).Size(52, 32).Shrink(0).Radius(16).Padding(3).
		Cursor(ui.CursorPointer).AlignItems(ui.Center)
	if *on {
		b.Background(t.Accent).Justify(ui.End)
	} else {
		b.Background(t.Border).Justify(ui.Start)
	}
	b.Children(func() {
		knob := t.Muted
		if *on {
			knob = t.Bg
		}
		ui.Box(c).Size(26, 26).Radius(13).Background(knob)
	})
	if b.Clicked() {
		*on = !*on
		return true
	}
	return false
}

// Segmented is the mockups' segmented control: options in a raised track,
// the selected one filled. enabled may be nil. It reports a change.
func Segmented(c *ui.Context, sel *int, options []string, enabled func(i int) bool) bool {
	t := Current()
	changed := false
	ui.Row(c).Height(46).Padding(4).Gap(4).Radius(12).Background(t.Surface).Border(1, t.Border).Children(func() {
		for i, o := range options {
			cc := c.Key(i)
			ok := enabled == nil || enabled(i)
			b := ui.ButtonBase(cc).Label(o).Grow(1).Basis(0).Height(36).Radius(9).AlignItems(ui.Center).
				Justify(ui.Center).Cursor(ui.CursorPointer).Disabled(!ok).Selected(i == *sel)
			on := i == *sel
			switch {
			case on:
				b.Background(t.Raised2).TextColor(t.Text)
			case !ok:
				b.TextColor(t.Faint).Opacity(0.6)
			case b.Hovered():
				b.TextColor(t.Text)
			default:
				b.TextColor(t.Muted)
			}
			b.Children(func() {
				w := 500
				if on {
					w = 600
				}
				ui.Text(cc, o).FontSize(13).FontWeight(w).SingleLine()
			})
			if b.Clicked() && ok && i != *sel {
				*sel = i
				changed = true
			}
		}
	})
	return changed
}

// SelectTile is a bordered choice tile (agent grid, auth radio). With radio
// it draws the radio dot first. Read Clicked.
func SelectTile(c *ui.Context, label string, selected, radio bool, fn func()) ui.Element {
	t := Current()
	b := ui.ButtonBase(c).Label(label).MinHeight(56).Padding(8, 12).Gap(12).Radius(12).AlignItems(ui.Center).
		Cursor(ui.CursorPointer).Selected(selected).TextColor(t.Text)
	if selected {
		b.Border(1, t.Accent).Background(t.AccentBg)
	} else {
		b.Border(1, t.Border).Background(t.Surface)
		if b.Hovered() {
			b.Background(t.Raised)
		}
	}
	b.Children(func() {
		if radio {
			dot := ui.Box(c).Size(18, 18).Radius(9).Shrink(0)
			if selected {
				dot.Border(5, t.Accent)
			} else {
				dot.Border(2, t.Faint)
			}
		}
		ui.Column(c).Grow(1).Shrink(1).MinWidth(0).Gap(2).Children(fn)
	})
	return b
}

// DialogOpts configures Dialog.
type DialogOpts struct {
	Title       string
	Description string
	// Width of the panel in DIPs (default 440).
	Width        float32
	Body, Footer func()
}

// Dialog shows a modal panel over a scrim while *open is true; Escape and a
// click outside close it.
func Dialog(c *ui.Context, open *bool, o DialogOpts) ui.Element {
	t := Current()
	w := o.Width
	if w <= 0 {
		w = 440
	}
	return ui.DialogBase(c, open, func(backdrop, panel ui.Element) {
		backdrop.Background(t.Scrim)
		panel.Width(w).Padding(20).Gap(12).Radius(18).Background(t.Surface).Border(1, t.Border).
			Shadow(0, 24, 56, 0, ui.RGBA(0, 0, 0, 0.35))
		if o.Title != "" {
			ui.Text(c, o.Title).FontSize(18).FontWeight(600)
		}
		if o.Description != "" {
			ui.Text(c, o.Description).FontSize(14).TextColor(t.Muted).LineHeight(1.4)
		}
		if o.Body != nil {
			o.Body()
		}
		if o.Footer != nil {
			ui.Row(c).Gap(8).Justify(ui.End).Margin(8, 0, 0, 0).Children(o.Footer)
		}
	})
}

// Toaster queues toasts from any goroutine; Flush adds them to the window
// on the UI thread. Invalidate, when set, asks the window for a frame.
type Toaster struct {
	mu         sync.Mutex
	pending    []ui.Toast
	Invalidate func()
}

// Toast types understood by ToastViewport.
const (
	ToastInfo    = ""
	ToastSuccess = "success"
	ToastError   = "error"
)

// Push queues a toast. Safe from any goroutine.
func (q *Toaster) Push(t ui.Toast) {
	q.mu.Lock()
	q.pending = append(q.pending, t)
	inv := q.Invalidate
	q.mu.Unlock()
	if inv != nil {
		inv()
	}
}

// Info queues an informative toast (the mobile app's snack bar).
func (q *Toaster) Info(title string) { q.Push(ui.Toast{Title: title, Type: ToastInfo}) }

// Success queues a success toast.
func (q *Toaster) Success(title string) { q.Push(ui.Toast{Title: title, Type: ToastSuccess}) }

// Error queues an error toast that stays 8 seconds.
func (q *Toaster) Error(title, desc string) {
	q.Push(ui.Toast{Title: title, Description: desc, Type: ToastError, Timeout: 8 * time.Second})
}

// Pending returns the queued toast titles (tests).
func (q *Toaster) Pending() []string {
	q.mu.Lock()
	defer q.mu.Unlock()
	var out []string
	for _, t := range q.pending {
		out = append(out, t.Title)
	}
	return out
}

// Flush adds the queued toasts to the window. Call it in the view.
func (q *Toaster) Flush(c *ui.Context) {
	q.mu.Lock()
	p := q.pending
	q.pending = nil
	q.mu.Unlock()
	for _, t := range p {
		c.AddToast(t)
	}
}

// ToastViewport shows the window's toasts bottom-right. Call it once.
func ToastViewport(c *ui.Context) {
	ui.ToastViewportBase(c, func(viewport ui.Element, toasts []ui.Toast) {
		t := Current()
		viewport.Padding(16).AlignItems(ui.End).Gap(8)
		for _, ts := range toasts {
			toast := ui.ToastBase(c.Key(ts.ID), ts)
			icon, color := IconInfo, t.Accent
			switch ts.Type {
			case ToastSuccess:
				icon, color = IconSuccess, t.Success
			case ToastError:
				icon, color = IconError, t.Danger
			}
			toast.Root.Row().Gap(10).Padding(10, 12).Radius(12).MaxWidth(420).
				Background(t.Raised).Border(1, t.Border).AlignItems(ui.Start).
				Shadow(0, 8, 24, 0, ui.RGBA(0, 0, 0, 0.3)).
				Transition(ui.ElementTransition{Enter: &ui.Motion{Y: 8}, Exit: &ui.Motion{}})
			toast.Root.Children(func() {
				Icon(c, icon).TextColor(color).FontSize(16).Margin(2, 0, 0, 0)
				ui.Column(c).Grow(1).Gap(2).Children(func() {
					ui.Text(c, ts.Title).FontSize(13).FontWeight(600)
					if ts.Description != "" {
						ui.Text(c, ts.Description).FontSize(12).TextColor(t.Muted).MaxLines(4)
					}
				})
				toast.CloseButton().Label("Cerrar").Padding(2).Radius(4).TextColor(t.Faint).
					Children(func() { Icon(c, IconClose).FontSize(14) })
			})
		}
	})
}

// Spinner is a rotating loader icon.
func Spinner(c *ui.Context) ui.Element {
	s := Icon(c, IconSpinner)
	return s.Rotate(s.Loop("spin", time.Second, ui.Linear) * 360)
}

// EmptyState is a centred icon and text for views with nothing to show.
func EmptyState(c *ui.Context, icon, title, description string) ui.Element {
	t := Current()
	return ui.Column(c).Grow(1).AlignItems(ui.Center).Justify(ui.Center).Gap(8).Padding(32).Children(func() {
		if icon != "" {
			Icon(c, icon).FontSize(32).TextColor(t.Faint)
		}
		if title != "" {
			ui.Text(c, title).FontSize(17).FontWeight(600).TextAlign(ui.Center)
		}
		if description != "" {
			ui.Text(c, description).FontSize(14).TextColor(t.Muted).TextAlign(ui.Center).MaxWidth(440).LineHeight(1.4)
		}
	})
}

// ScreenHeader is the header of an area page: an overline, a big title and
// trailing controls, with an optional block below (filter chips).
func ScreenHeader(c *ui.Context, overline, title string, trailing, below func()) {
	t := Current()
	ui.Column(c).Padding(20, 24, 12, 24).Gap(14).Shrink(0).Children(func() {
		ui.Row(c).AlignItems(ui.Center).Gap(12).Children(func() {
			ui.Column(c).Grow(1).Shrink(1).MinWidth(0).Gap(2).Children(func() {
				if overline != "" {
					ui.Text(c, overline).FontSize(13).TextColor(t.Muted).SingleLine().Ellipsis("…")
				}
				ui.Text(c, title).FontSize(28).FontWeight(700).LetterSpacing(-0.5).SingleLine().Ellipsis("…").Level(1)
			})
			if trailing != nil {
				ui.Row(c).Gap(8).AlignItems(ui.Center).Shrink(0).Children(trailing)
			}
		})
		if below != nil {
			below()
		}
	})
}

// RouteHeader is the header of a pushed page: back button, title and an
// optional subtitle. It reports whether back was clicked.
func RouteHeader(c *ui.Context, title, subtitle string, trailing func()) bool {
	t := Current()
	back := false
	ui.Row(c).Padding(14, 24, 10, 16).Gap(10).AlignItems(ui.Center).Shrink(0).BorderWidth(0, 0, 1, 0).
		BorderColor(t.Divider).Children(func() {
		back = IconButton(c, IconBack, "Volver").Clicked()
		ui.Column(c).Grow(1).Shrink(1).MinWidth(0).Gap(2).Children(func() {
			ui.Text(c, title).FontSize(18).FontWeight(600).SingleLine().Ellipsis("…").Level(1)
			if subtitle != "" {
				ui.Text(c, subtitle).FontSize(12).TextColor(t.Muted).SingleLine().Ellipsis("…")
			}
		})
		if trailing != nil {
			ui.Row(c).Gap(8).AlignItems(ui.Center).Shrink(0).Children(trailing)
		}
	})
	return back
}
