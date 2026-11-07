package session

import (
	"image/color"
	"strings"

	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/vt"

	"github.com/digiogithub/agentd/internal/termcolor"
)

// Ghost text: harnesses such as Claude Code and Codex draw a placeholder or a
// suggested next prompt in their input line (e.g. `❯ Try "fix lint errors"`).
// It is output, not user input, and must never be mistaken for a human draft
// by readers of the screen (agents dumping it) or by the delivery loop. See
// docs/ghost-text.md for the captures the rules below are based on.

// GhostRules classifies ghost cells. The scope is always the input line: the
// cursor's row, from the cursor column to the right edge (every capture put
// the cursor on the first cell of the ghost text). Rules are never applied to
// the rest of the screen: Codex, for one, draws ordinary secondary text faint.
type GhostRules struct {
	// Faint: faint (SGR 2) cells in scope are ghost.
	Faint bool
	// Colors: cells in scope whose foreground matches one of these are ghost.
	Colors []termcolor.Spec
	// RightOfCursor: the text in scope is ghost only when the cursor sits right
	// after PromptMarker (the text left of the cursor, ignoring spaces and
	// no-break spaces, ends with it; an empty marker always passes). If no
	// style rule (Faint, Colors) is set, every non-blank cell in scope is ghost;
	// otherwise a cell must match a style rule too.
	RightOfCursor bool
	// PromptMarker is the harness's prompt glyph, e.g. "❯" or "›".
	PromptMarker string
}

func (r GhostRules) styleRules() bool { return r.Faint || len(r.Colors) > 0 }

// enabled reports whether the rules can classify anything.
func (r GhostRules) enabled() bool { return r.styleRules() || r.RightOfCursor }

// ScreenOpts selects how ScreenFiltered renders the screen.
type ScreenOpts struct {
	// HideGhost blanks the cells classified as ghost text by Ghost.
	HideGhost bool
	Ghost     GhostRules
}

// ScreenFiltered returns the visible screen as plain text like Screen, but
// with the ghost text blanked when opts.HideGhost is set, plus the ghost text
// that was found ("" if none; it is reported even when HideGhost is false).
func (s *Session) ScreenFiltered(opts ScreenOpts) (screen, ghost string) {
	s.outMu.RLock()
	defer s.outMu.RUnlock()
	return s.screenFilteredLocked(opts)
}

// ScrollbackFiltered is Scrollback with the visible part rendered by
// ScreenFiltered (ghost text only ever lives on the visible input line).
func (s *Session) ScrollbackFiltered(opts ScreenOpts) (text, ghost string) {
	s.outMu.RLock()
	defer s.outMu.RUnlock()
	var b strings.Builder
	if sb := s.emu.Emulator.Scrollback(); sb != nil {
		for _, l := range sb.Lines() {
			b.WriteString(strings.TrimRight(l.String(), " "))
			b.WriteByte('\n')
		}
	}
	scr, ghost := s.screenFilteredLocked(opts)
	b.WriteString(scr)
	return b.String(), ghost
}

// GhostText returns the ghost text currently visible on the input line under
// rules ("" if none).
func (s *Session) GhostText(rules GhostRules) string {
	_, g := s.ScreenFiltered(ScreenOpts{Ghost: rules})
	return g
}

func (s *Session) screenFilteredLocked(opts ScreenOpts) (string, string) {
	return filterScreen(s.emu.Emulator, opts)
}

// filterScreen implements ScreenFiltered on an emulator the caller has
// exclusive (or read-locked) access to.
func filterScreen(e *vt.Emulator, opts ScreenOpts) (string, string) {
	raw := e.String()
	cur := e.CursorPosition()
	cells := ghostCells(e, cur, opts.Ghost)
	if len(cells) == 0 {
		return raw, ""
	}
	ghost := ghostString(e, cur.Y, cur.X, cells)
	if !opts.HideGhost {
		return raw, ghost
	}
	lines := strings.Split(raw, "\n")
	if cur.Y < len(lines) {
		lines[cur.Y] = rowString(e, cur.Y, cells)
	}
	return strings.Join(lines, "\n"), ghost
}

// cellGrid is the subset of the emulator used for classification (lets
// tests drive it without a PTY).
type cellGrid interface {
	CellAt(x, y int) *uv.Cell
	Width() int
	Height() int
}

// ghostCells returns the set of x positions on row cur.Y classified as ghost.
func ghostCells(g cellGrid, cur uv.Position, r GhostRules) map[int]bool {
	if !r.enabled() || cur.Y < 0 || cur.Y >= g.Height() || cur.X < 0 || cur.X >= g.Width() {
		return nil
	}
	if r.RightOfCursor && r.PromptMarker != "" {
		left := strings.TrimRight(rowText(g, cur.Y, 0, cur.X), "  \t")
		if !strings.HasSuffix(left, r.PromptMarker) {
			return nil
		}
	}
	var out map[int]bool
	for x := cur.X; x < g.Width(); x++ {
		c := g.CellAt(x, cur.Y)
		if c == nil || c.IsZero() || isBlank(c.Content) {
			continue
		}
		ghost := r.RightOfCursor
		if r.styleRules() {
			ghost = styleGhost(c, r)
		}
		if ghost {
			if out == nil {
				out = map[int]bool{}
			}
			out[x] = true
		}
	}
	return out
}

func isBlank(content string) bool {
	return strings.Trim(content, "  \t") == ""
}

func styleGhost(c *uv.Cell, r GhostRules) bool {
	if r.Faint && c.Style.Attrs&uv.AttrFaint != 0 {
		return true
	}
	return len(r.Colors) > 0 && colorMatches(c.Style.Fg, r.Colors)
}

func colorMatches(c color.Color, specs []termcolor.Spec) bool {
	switch v := c.(type) {
	case nil:
		return false
	case ansi.BasicColor:
		return termcolor.MatchIndex(specs, int(v))
	case ansi.IndexedColor:
		return termcolor.MatchIndex(specs, int(v))
	default:
		r, g, b, a := c.RGBA()
		if a == 0 {
			return false
		}
		return termcolor.MatchRGB(specs, (r>>8)<<16|(g>>8)<<8|b>>8)
	}
}

// rowText returns the text of cells [from, to) of row y (wide-character
// continuation cells skipped, empty cells as spaces).
func rowText(g cellGrid, y, from, to int) string {
	var b strings.Builder
	for x := from; x < to && x < g.Width(); x++ {
		c := g.CellAt(x, y)
		switch {
		case c == nil || c.Content == "":
			if c == nil || !c.IsZero() {
				b.WriteByte(' ')
			}
		default:
			b.WriteString(c.Content)
		}
	}
	return b.String()
}

// rowString renders row y like Screen does, with the cells in blank replaced
// by spaces, trailing spaces trimmed.
func rowString(g cellGrid, y int, blank map[int]bool) string {
	var b strings.Builder
	for x := 0; x < g.Width(); x++ {
		c := g.CellAt(x, y)
		switch {
		case c != nil && c.IsZero():
			// continuation of a wide character
		case blank[x]:
			b.WriteString(strings.Repeat(" ", max(1, c.Width)))
		case c == nil || c.Content == "":
			b.WriteByte(' ')
		default:
			b.WriteString(c.Content)
		}
	}
	return strings.TrimRight(b.String(), " ")
}

// ghostString joins the ghost cells of row y (from column x0), keeping the
// blanks between them, trimmed.
func ghostString(g cellGrid, y, x0 int, cells map[int]bool) string {
	var b strings.Builder
	last := -1
	for x := x0; x < g.Width(); x++ {
		if !cells[x] {
			continue
		}
		if last >= 0 && x > last+1 {
			gap := rowText(g, y, last+1, x)
			b.WriteString(strings.Repeat(" ", len([]rune(gap))))
		}
		c := g.CellAt(x, y)
		b.WriteString(c.Content)
		last = x + max(1, c.Width) - 1
	}
	return strings.TrimSpace(b.String())
}
