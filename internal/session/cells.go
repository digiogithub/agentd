package session

import (
	"fmt"
	"image/color"

	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/ansi"
)

// CellInfo describes one screen cell, for diagnostics (`agentd dump --cells`).
type CellInfo struct {
	X       int      `json:"x"`
	Content string   `json:"content"`
	Rune    string   `json:"rune"`            // "U+XXXX" of the first rune
	Fg      string   `json:"fg,omitempty"`    // "" default, "N" palette index, "#rrggbb"
	Bg      string   `json:"bg,omitempty"`    // same encoding as Fg
	Attrs   []string `json:"attrs,omitempty"` // bold faint italic blink rapid-blink reverse conceal strike underline
	Width   int      `json:"width"`
}

// RowCells returns the cursor position, the row inspected (row < 0 means the
// cursor's row) and the cells of that row that are not plain blanks (blank
// cells with any style are included; wide-character continuations are not).
func (s *Session) RowCells(row int) (cursor uv.Position, y int, cells []CellInfo) {
	s.outMu.RLock()
	defer s.outMu.RUnlock()
	return rowCells(s.emu.Emulator, row)
}

type cursorGrid interface {
	cellGrid
	CursorPosition() uv.Position
}

func rowCells(g cursorGrid, row int) (uv.Position, int, []CellInfo) {
	cur := g.CursorPosition()
	if row < 0 {
		row = cur.Y
	}
	if row >= g.Height() {
		return cur, row, nil
	}
	var out []CellInfo
	for x := 0; x < g.Width(); x++ {
		c := g.CellAt(x, row)
		if c == nil || c.IsZero() {
			continue
		}
		if (c.Content == "" || c.Content == " ") && c.Style.IsZero() {
			continue
		}
		content := c.Content
		if content == "" {
			content = " "
		}
		out = append(out, CellInfo{
			X: x, Content: content, Rune: fmt.Sprintf("U+%04X", []rune(content)[0]),
			Fg: colorString(c.Style.Fg), Bg: colorString(c.Style.Bg),
			Attrs: attrNames(c.Style), Width: c.Width,
		})
	}
	return cur, row, out
}

func colorString(c color.Color) string {
	switch v := c.(type) {
	case nil:
		return ""
	case ansi.BasicColor:
		return fmt.Sprint(int(v))
	case ansi.IndexedColor:
		return fmt.Sprint(int(v))
	default:
		r, g, b, a := c.RGBA()
		if a == 0 {
			return ""
		}
		return fmt.Sprintf("#%02x%02x%02x", r>>8, g>>8, b>>8)
	}
}

var attrList = []struct {
	bit  uint8
	name string
}{
	{uv.AttrBold, "bold"}, {uv.AttrFaint, "faint"}, {uv.AttrItalic, "italic"},
	{uv.AttrBlink, "blink"}, {uv.AttrRapidBlink, "rapid-blink"}, {uv.AttrReverse, "reverse"},
	{uv.AttrConceal, "conceal"}, {uv.AttrStrikethrough, "strike"},
}

func attrNames(st uv.Style) []string {
	var out []string
	for _, a := range attrList {
		if st.Attrs&a.bit != 0 {
			out = append(out, a.name)
		}
	}
	if st.Underline != 0 {
		out = append(out, "underline")
	}
	return out
}
