package session

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/digiogithub/agentd/internal/desktopui/app"
	"github.com/digiogithub/agentd/internal/desktopui/chatfmt"
	"github.com/digiogithub/agentd/internal/desktopui/kit"
	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/markdown"
)

// agentBody shows an agent entry: its chat markup (CommonMark from the
// terminal's styles: headings, bold, inline code, lists) in the chat font,
// and the tables found in it as grids.
func (v *View) agentBody(c *ui.Context, t *kit.Tokens, i int, text string) {
	md := v.markdownOpts(t)
	blocks := chatfmt.Split(text)
	ui.Column(c).Gap(10 * v.a.Prefs.Get().ChatLineScale()).MaxWidthPercent(92).AlignItems(ui.Stretch).Children(func() {
		for bi, b := range blocks {
			bc := c.Key("blk" + strconv.Itoa(bi))
			if b.Table != nil {
				v.tableGrid(bc, t, i*1000+bi, chatfmt.TableText(b.Table))
				continue
			}
			ui.Column(bc).AlignItems(ui.Stretch).Children(func() {
				markdown.View(bc, b.Text, md)
			})
		}
	})
}

// markdownOpts are the options of the chat's Markdown: the chat font, size
// and line spacing (line height and paragraph gap) from the settings, links opened with openLink. They are kept on the
// view (MyGo keeps parse state per Options) and rebuilt when the settings
// or the palette change.
func (v *View) markdownOpts(t *kit.Tokens) *markdown.Options {
	s := v.a.Prefs.Get()
	key := fmt.Sprintf("%v|%s|%s|%p", s.ChatSize(), s.ChatFontOrDefault(), s.ChatLineHeightOrDefault(), t)
	if v.md == nil || v.mdKey != key {
		v.mdKey = key
		v.md = &markdown.Options{
			Theme: &markdown.Theme{
				FontSize:     s.ChatSize(),
				Font:         app.ChatFamily(s.ChatFontOrDefault()),
				CodeFont:     kit.MonoFamily(),
				Link:         t.Accent,
				LineHeight:   app.ChatAgentLineHeight * s.ChatLineScale(),
				BlockSpacing: chatBlockSpacing * s.ChatLineScale(),
			},
			OnLink: v.openLink,
		}
	}
	return v.md
}

// chatBlockSpacing is the Markdown gap between paragraphs at normal line
// spacing, in ems (MyGo's default).
const chatBlockSpacing float32 = 0.75

// prose is text in which http(s) URLs are links.
func (v *View) prose(c *ui.Context, t *kit.Tokens, text string, size, lh float32, mono bool) {
	segs := chatfmt.Linkify(text)
	plain := func(e ui.Element) ui.Element {
		e = e.FontSize(size).LineHeight(lh)
		if mono {
			e = e.Font(kit.MonoFamily()).TextColor(t.Muted)
		} else {
			e = e.Font(app.ChatFamily(v.a.Prefs.Get().ChatFontOrDefault()))
		}
		return e
	}
	if len(segs) == 1 && segs[0].URL == "" {
		plain(ui.Text(c, text)).Selectable()
		return
	}
	plain(ui.RichText(c)).Selectable().Children(func() {
		for _, sg := range segs {
			if sg.URL == "" {
				ui.Text(c, sg.Text)
				continue
			}
			l := ui.Link(c, sg.Text, "").TextColor(t.Accent).Underline().Cursor(ui.CursorPointer)
			if l.Clicked() {
				v.openLink(sg.URL)
			}
		}
	})
}

func (v *View) openLink(u string) {
	if err := chatfmt.OpenURL(u); err != nil {
		v.a.Toasts.Error("No se pudo abrir el enlace", err.Error())
	}
}

const (
	cellPx    = 7.4 // average glyph width at 13 px, used to size columns
	minColPx  = 56
	maxColPx  = 300
	cellPadPx = 10
)

func colWidths(tb *chatfmt.Table) []float32 {
	w := make([]int, tb.Cols())
	note := func(row []string) {
		for c, s := range row {
			if c >= len(w) {
				break
			}
			for _, ln := range strings.Split(s, "\n") {
				if n := displayWidth(ln); n > w[c] {
					w[c] = n
				}
			}
		}
	}
	note(tb.Header)
	for _, r := range tb.Rows {
		note(r)
	}
	out := make([]float32, len(w))
	for i, n := range w {
		px := float32(n)*cellPx + 2*cellPadPx + 2
		out[i] = min(max(px, minColPx), maxColPx)
	}
	return out
}

func displayWidth(s string) int {
	n := 0
	for _, r := range s {
		switch {
		case r >= 0x1100 && (r <= 0x115f || (r >= 0x2e80 && r <= 0xa4cf) || (r >= 0xac00 && r <= 0xd7a3) ||
			(r >= 0xf900 && r <= 0xfaff) || (r >= 0xfe30 && r <= 0xfe6f) || (r >= 0xff00 && r <= 0xff60) ||
			(r >= 0x1f300 && r <= 0x1faff) || (r >= 0x20000 && r <= 0x3fffd)):
			n += 2
		default:
			n++
		}
	}
	return n
}

// tableGrid draws tb as a grid: raised bold header, dividers from the
// palette, wrapping cells, scrolling sideways when wider than the chat.
func (v *View) tableGrid(c *ui.Context, t *kit.Tokens, id int, tb *chatfmt.Table) {
	widths := colWidths(tb)
	ncols := tb.Cols()
	row := func(rc *ui.Context, cells []string, header, last bool) {
		ui.Row(rc).AlignItems(ui.Stretch).Children(func() {
			for ci := 0; ci < ncols; ci++ {
				cc := rc.Key("c" + strconv.Itoa(ci))
				txt := ""
				if ci < len(cells) {
					txt = cells[ci]
				}
				col := ui.Column(cc).Width(widths[ci]).Shrink(0).Padding(7, cellPadPx).BorderColor(t.Border)
				bw := [4]float32{0, 1, 1, 0}
				if ci == ncols-1 {
					bw[1] = 0
				}
				if last {
					bw[2] = 0
				}
				col.BorderWidth(bw[0], bw[1], bw[2], bw[3])
				if header {
					col.Background(t.Raised)
				}
				col.Children(func() {
					e := ui.Text(cc, txt).FontSize(13).LineHeight(1.4).Selectable()
					if header {
						e.FontWeight(600)
					}
					switch tb.Align[ci] {
					case chatfmt.AlignCenter:
						e.TextAlign(ui.Center).FillWidth()
					case chatfmt.AlignRight:
						e.TextAlign(ui.End).FillWidth()
					}
				})
			}
		})
	}
	ui.ScrollHorizontal(c.Key("tbl" + strconv.Itoa(id))).Shrink(1).MinWidth(0).Children(func() {
		ui.Column(c).Radius(10).Border(1, t.Border).Background(t.Surface).Clip().Children(func() {
			if tb.Header != nil {
				row(c.Key("h"), tb.Header, true, len(tb.Rows) == 0)
			}
			for ri, r := range tb.Rows {
				row(c.Key("r"+strconv.Itoa(ri)), r, false, ri == len(tb.Rows)-1)
			}
		})
	})
}
