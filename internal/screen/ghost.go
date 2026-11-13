package screen

import (
	"strconv"
	"strings"

	"github.com/digiogithub/agentd/internal/termcolor"
)

// placed is a cell with its starting column (wide-character continuation
// cells are not stored in Row, so slice indexes are not columns).
type placed struct {
	col, w int
	c      Cell
}

// place lists the cells with their starting columns. Two Row layouts are
// understood: wide-character continuation cells kept as Width 0 cells (cell
// index == column; they are skipped here), or dropped (columns accumulate
// the cell widths).
func place(r Row) []placed {
	cont := false
	for _, c := range r.Cells {
		if c.Width == 0 {
			cont = true
			break
		}
	}
	out := make([]placed, 0, len(r.Cells))
	col := 0
	for i, c := range r.Cells {
		if cont {
			col = i
			if c.Width == 0 {
				continue
			}
		}
		w := int(c.Width)
		if w < 1 {
			w = 1
		}
		out = append(out, placed{col, w, c})
		if !cont {
			col += w
		}
	}
	return out
}

// blankText reports whether t shows nothing (empty, spaces, NBSP, tabs).
func blankText(t string) bool { return strings.Trim(t, "  \t") == "" }

// cellText is the text of a cell, an empty cell being a space.
func cellText(c Cell) string {
	if c.Text == "" {
		return " "
	}
	return c.Text
}

// GhostColumns returns the set of columns of row classified as ghost text
// when the cursor is at column curX (nil if none). cols is the terminal
// width; a cursor outside [0, cols) classifies nothing. This is the single
// implementation shared by session.ScreenFiltered and Split.
func GhostColumns(row Row, curX, cols int, r GhostRules) map[int]bool {
	if !r.Enabled() || curX < 0 || curX >= cols {
		return nil
	}
	cells := place(row)
	if r.RightOfCursor && r.PromptMarker != "" {
		var b strings.Builder
		for _, p := range cells {
			if p.col >= curX {
				break
			}
			b.WriteString(cellText(p.c))
		}
		if !strings.HasSuffix(strings.TrimRight(b.String(), "  \t"), r.PromptMarker) {
			return nil
		}
	}
	var out map[int]bool
	for _, p := range cells {
		if p.col < curX || blankText(p.c.Text) {
			continue
		}
		ghost := r.RightOfCursor
		if r.StyleRules() {
			ghost = styleGhost(p.c, r)
		}
		if ghost {
			if out == nil {
				out = map[int]bool{}
			}
			out[p.col] = true
		}
	}
	return out
}

func styleGhost(c Cell, r GhostRules) bool {
	if r.Faint && c.Attrs.Has(Faint) {
		return true
	}
	return len(r.Colors) > 0 && ColorMatches(c.Fg, r.Colors)
}

// ColorMatches reports whether a Cell colour ("" default, "N" palette index,
// "#rrggbb") matches one of specs.
func ColorMatches(c string, specs []termcolor.Spec) bool {
	switch {
	case c == "":
		return false
	case c[0] == '#':
		n, err := strconv.ParseUint(c[1:], 16, 32)
		return err == nil && termcolor.MatchRGB(specs, uint32(n))
	default:
		n, err := strconv.Atoi(c)
		return err == nil && termcolor.MatchIndex(specs, n)
	}
}

// GhostText joins the ghost cells of row (columns in cols, from column x0),
// keeping the blanks between them, trimmed.
func GhostText(row Row, cols map[int]bool, x0 int) string {
	var b strings.Builder
	last := -1
	for _, p := range place(row) {
		if p.col < x0 || !cols[p.col] {
			continue
		}
		if last >= 0 && p.col > last+1 {
			b.WriteString(strings.Repeat(" ", p.col-last-1))
		}
		b.WriteString(p.c.Text)
		last = p.col + p.w - 1
	}
	return strings.TrimSpace(b.String())
}

// WithoutColumns returns a copy of row with the cells starting at the given
// columns blanked (keeping their width).
func WithoutColumns(row Row, cols map[int]bool) Row {
	if len(cols) == 0 {
		return row
	}
	out := Row{Cells: make([]Cell, len(row.Cells)), Wrapped: row.Wrapped}
	copy(out.Cells, row.Cells)
	cont := false
	for _, c := range row.Cells {
		if c.Width == 0 {
			cont = true
			break
		}
	}
	col := 0
	for i, c := range out.Cells {
		if cont {
			col = i
		}
		if c.Width != 0 && cols[col] {
			out.Cells[i] = Cell{Width: c.Width}
		}
		if !cont {
			w := int(c.Width)
			if w < 1 {
				w = 1
			}
			col += w
		}
	}
	return out
}
