package session

import (
	"strconv"
	"strings"

	"github.com/digiogithub/agentd/internal/desktopui/app"
	"github.com/digiogithub/agentd/internal/desktopui/chatfmt"
	"github.com/digiogithub/agentd/internal/desktopui/kit"
	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/markdown"
)

// Labels of the button that switches a code block between code and text.
const (
	labelAsText = "Mostrar como texto"
	labelAsCode = "Mostrar como código"
)

// codeKey identifies a code block of the chat by its first two lines, so the
// choice made for it survives view refreshes and a block still being written.
func codeKey(code string) string {
	lines := strings.SplitN(code, "\n", 3)
	if len(lines) > 2 {
		lines = lines[:2]
	}
	return strings.Join(lines, "\n")
}

// codeBlock builds a code block of the agent prose (markdown.Options.CodeBlock):
// the code as MyGo draws it, with a second button next to the copy one that
// shows the block as normal text instead (chat font, wrapped, paragraphs
// rebuilt by chatfmt.Reflow). Prose that the daemon took for code does not
// wrap and is hard to read as code; the choice is kept per block for the life
// of the view. Copy always copies the block's original text.
func (v *View) codeBlock(c *ui.Context, b *markdown.CodeBlockView) {
	t := kit.Current()
	code := b.Code()
	key := codeKey(code)
	asText := v.asText[key]
	box := ui.Column(c).Direction(ui.LeftToRight).Border(1, t.Border).Radius(10).AlignSelf(ui.Stretch).Clip()
	if !asText {
		box.Background(t.Text.Alpha(0.04))
	}
	box.Children(func() {
		ui.Row(c).AlignItems(ui.Center).Gap(2).Padding(4, 6, 0, 12).Children(func() {
			ui.Text(c, b.Language).FontSize(11.5).TextColor(t.Muted).Unselectable()
			ui.Spacer(c)
			icon, label := kit.IconWrapText, labelAsText
			if asText {
				icon, label = kit.IconCode, labelAsCode
			}
			btn := ui.ButtonBase(c.Key("astext")).Padding(4, 7).Radius(5).Label(label).Tooltip(label).Cursor(ui.CursorPointer)
			if btn.Hovered() {
				btn.Background(t.Text.Alpha(0.08))
			}
			btn.Children(func() { kit.Icon(c, icon).Size(14, 14).TextColor(t.Muted) })
			if btn.Clicked() {
				if asText {
					delete(v.asText, key)
				} else {
					v.asText[key] = true
				}
			}
			b.CopyButton(c)
		})
		if !asText {
			b.Body(c)
			return
		}
		set := v.a.Prefs.Get()
		lh := app.ChatAgentLineHeight * set.ChatLineScale()
		ui.Column(c).Padding(2, 12, 12, 12).Gap(10 * set.ChatLineScale()).AlignItems(ui.Stretch).Children(func() {
			for i, p := range chatfmt.Reflow(code) {
				v.prose(c.Key("p"+strconv.Itoa(i)), t, p, set.ChatSize(), lh, false)
			}
		})
	})
}
