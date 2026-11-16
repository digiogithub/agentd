// Package chatfmt holds the pure text parsing of the desktop Chat tab:
// tables drawn as box-drawing text (Claude Code) or as markdown pipe tables,
// and URLs in prose. It never imports MyGo.
package chatfmt

import (
	"regexp"
	"strings"

	"github.com/charmbracelet/x/ansi"
)

// Align is the horizontal alignment of a table column.
type Align int

// Alignments.
const (
	AlignLeft Align = iota
	AlignCenter
	AlignRight
)

// Table is a parsed table. Header is nil when the source had none.
type Table struct {
	Header []string
	Rows   [][]string
	Align  []Align
}

// Cols is the number of columns.
func (t *Table) Cols() int { return len(t.Align) }

// Block is a piece of text: either plain lines or a table.
type Block struct {
	Text  string
	Table *Table
}

// Split cuts text into plain text and table blocks. Anything that does not
// parse as a consistent table stays plain text.
func Split(text string) []Block {
	lines := strings.Split(text, "\n")
	var out []Block
	var plain []string
	flush := func() {
		if len(plain) > 0 {
			out = append(out, Block{Text: strings.Join(plain, "\n")})
			plain = nil
		}
	}
	for i := 0; i < len(lines); {
		if classify(lines[i]) == lineOther {
			plain = append(plain, lines[i])
			i++
			continue
		}
		j := i
		for j < len(lines) && classify(lines[j]) != lineOther {
			j++
		}
		if t := parseRun(lines[i:j]); t != nil {
			flush()
			out = append(out, Block{Table: t})
		} else {
			plain = append(plain, lines[i:j]...)
		}
		i = j
	}
	flush()
	return out
}

type lineKind int

const (
	lineOther lineKind = iota
	lineBorder
	lineBoxRow
	linePipe
)

// Box-drawing classes. Heavy and double variants map to the light ones.
const (
	vertChars = "│┃║"
	horzChars = "─━═"
	junction  = "┌┬┐├┼┤└┴┘┏┳┓┣╋┫┗┻┛╔╦╗╠╬╣╚╩╝┍┎┑┒┕┖┙┚┝┞┟┠┡┢┥┦┧┨┩┪┭┮┯┰┱┲┵┶┷┸┹┺┽┾┿╀╁╂╃╄╅╆╇╈╉╊╒╓╕╖╘╙╛╜╞╟╡╢╤╥╧╨╪╫"
)

var markPrefixes = []string{"⏺", "●", "•", "⎿"}

// strip removes the indentation and a leading response mark.
func strip(line string) string {
	s := strings.TrimSpace(line)
	for _, m := range markPrefixes {
		if rest, ok := strings.CutPrefix(s, m); ok {
			s = strings.TrimSpace(rest)
			break
		}
	}
	return s
}

func isBorder(s string) bool {
	if s == "" || !strings.ContainsAny(s, horzChars) {
		return false
	}
	first := []rune(s)[0]
	if !strings.ContainsRune(junction, first) {
		return false
	}
	for _, r := range s {
		if !strings.ContainsRune(junction, r) && !strings.ContainsRune(horzChars, r) && r != ' ' {
			return false
		}
	}
	return true
}

func classify(line string) lineKind {
	s := strip(line)
	if s == "" {
		return lineOther
	}
	if isBorder(s) {
		return lineBorder
	}
	r := []rune(s)
	if len(r) >= 2 && strings.ContainsRune(vertChars, r[0]) && strings.ContainsRune(vertChars, r[len(r)-1]) {
		return lineBoxRow
	}
	if len(r) >= 3 && r[0] == '|' && r[len(r)-1] == '|' {
		return linePipe
	}
	return lineOther
}

func parseRun(lines []string) *Table {
	kinds := make([]lineKind, len(lines))
	box, pipe := false, false
	for i, l := range lines {
		kinds[i] = classify(l)
		switch kinds[i] {
		case lineBorder, lineBoxRow:
			box = true
		case linePipe:
			pipe = true
		}
	}
	switch {
	case box && !pipe:
		return parseBox(lines, kinds)
	case pipe && !box:
		return parsePipe(lines)
	}
	return nil
}

// cellPositions maps each rune of s to its display column.
func runeCols(rs []rune) []int {
	cols := make([]int, len(rs)+1)
	c := 0
	for i, r := range rs {
		cols[i] = c
		c += ansi.StringWidth(string(r))
	}
	cols[len(rs)] = c
	return cols
}

func parseBox(lines []string, kinds []lineKind) *Table {
	// Separator columns: the junctions of the first border line, else the
	// bars of the first row.
	var seps []int
	for i, l := range lines {
		s := []rune(strip(l))
		cols := runeCols(s)
		if kinds[i] == lineBorder {
			for k, r := range s {
				if strings.ContainsRune(junction, r) {
					seps = append(seps, cols[k])
				}
			}
			break
		}
		if kinds[i] == lineBoxRow {
			for k, r := range s {
				if strings.ContainsRune(vertChars, r) {
					seps = append(seps, cols[k])
				}
			}
			break
		}
	}
	if len(seps) < 2 {
		return nil
	}
	ncols := len(seps) - 1
	type group struct{ rows [][]string }
	var groups []group
	cur := group{}
	closeGroup := func() {
		if len(cur.rows) > 0 {
			groups = append(groups, cur)
			cur = group{}
		}
	}
	sepAfter := 0 // mid borders seen after content
	seenContent := false
	for i, l := range lines {
		s := []rune(strip(l))
		cols := runeCols(s)
		if kinds[i] == lineBorder {
			// Border junctions must sit on the separator columns.
			for k, r := range s {
				if strings.ContainsRune(junction, r) && !hasInt(seps, cols[k]) {
					return nil
				}
			}
			if seenContent {
				closeGroup()
				sepAfter++
			}
			continue
		}
		// Row: bars exactly on the separators, cells between them.
		if cols[len(s)] != seps[len(seps)-1]+1 {
			return nil
		}
		var cells []string
		var b strings.Builder
		for k, r := range s {
			if strings.ContainsRune(vertChars, r) {
				if k == 0 {
					continue
				}
				if !hasInt(seps, cols[k]) {
					b.WriteRune(r)
					continue
				}
				cells = append(cells, strings.TrimSpace(b.String()))
				b.Reset()
				continue
			}
			b.WriteRune(r)
		}
		if len(cells) != ncols {
			return nil
		}
		seenContent = true
		cur.rows = append(cur.rows, cells)
	}
	closeGroup()
	if len(groups) == 0 {
		return nil
	}
	t := &Table{Align: make([]Align, ncols)}
	mergeLines := func(rows [][]string) []string {
		out := make([]string, ncols)
		for _, r := range rows {
			for c, v := range r {
				if v == "" {
					continue
				}
				if out[c] != "" {
					out[c] += " "
				}
				out[c] += v
			}
		}
		return out
	}
	body := groups
	if sepAfter > 0 && len(groups) > 1 {
		t.Header = mergeLines(groups[0].rows)
		body = groups[1:]
	}
	splitRows := len(body) == 1 // only the header separator (or none): guess
	for _, g := range body {
		if !splitRows {
			t.Rows = append(t.Rows, mergeLines(g.rows))
			continue
		}
		var run [][]string
		for _, r := range g.rows {
			if r[0] != "" && len(run) > 0 {
				t.Rows = append(t.Rows, mergeLines(run))
				run = nil
			}
			run = append(run, r)
		}
		if len(run) > 0 {
			t.Rows = append(t.Rows, mergeLines(run))
		}
	}
	if len(t.Rows) == 0 && t.Header == nil {
		return nil
	}
	return t
}

func hasInt(s []int, v int) bool {
	for _, x := range s {
		if x == v {
			return true
		}
	}
	return false
}

var pipeSepCell = regexp.MustCompile(`^:?-{1,}:?$`)

func splitPipe(s string) []string {
	s = strings.TrimSpace(s)
	s = strings.TrimPrefix(s, "|")
	if strings.HasSuffix(s, "|") && !strings.HasSuffix(s, `\|`) {
		s = s[:len(s)-1]
	}
	var cells []string
	var b strings.Builder
	rs := []rune(s)
	for i := 0; i < len(rs); i++ {
		if rs[i] == '\\' && i+1 < len(rs) && rs[i+1] == '|' {
			b.WriteRune('|')
			i++
			continue
		}
		if rs[i] == '|' {
			cells = append(cells, strings.TrimSpace(b.String()))
			b.Reset()
			continue
		}
		b.WriteRune(rs[i])
	}
	return append(cells, strings.TrimSpace(b.String()))
}

func parsePipe(lines []string) *Table {
	if len(lines) < 2 {
		return nil
	}
	head := splitPipe(strip(lines[0]))
	sep := splitPipe(strip(lines[1]))
	if len(sep) != len(head) {
		return nil
	}
	t := &Table{Header: head, Align: make([]Align, len(head))}
	for i, c := range sep {
		if !pipeSepCell.MatchString(c) {
			return nil
		}
		l, r := strings.HasPrefix(c, ":"), strings.HasSuffix(c, ":")
		switch {
		case l && r:
			t.Align[i] = AlignCenter
		case r:
			t.Align[i] = AlignRight
		}
	}
	for _, l := range lines[2:] {
		cells := splitPipe(strip(l))
		if len(cells) > len(head) {
			return nil
		}
		for len(cells) < len(head) {
			cells = append(cells, "")
		}
		t.Rows = append(t.Rows, cells)
	}
	return t
}
