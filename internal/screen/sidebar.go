package screen

// Side panel detection. Some TUIs (Pando, many Ink apps) draw the conversation
// on the left and a status/info panel on the right, split by a vertical rule
// that runs the full height of the screen. Read as one text, the two columns
// interleave line by line. Like Herdr, which keeps pinned chrome out of the
// text it reads back, Split cuts the panel off: the transcript, input and
// dialog are found on the left columns only and the panel text is returned
// apart (Regions.Side) so a client can still show it as information.

// sideMinRows is the fewest rows the divider must cross.
const sideMinRows = 6

// isVertRule reports whether a cell is a vertical frame stroke.
func isVertRule(c Cell) bool {
	switch c.Text {
	case "│", "┃", "║", "▏", "▕":
		return true
	}
	return false
}

// findSideColumn returns the column of a full-height vertical divider: the
// same column holds a vertical rule on at least 60 % of the screen rows (and
// at least sideMinRows), and it lies inside the screen (a framed input box
// has its rules at the edges, so edge columns never count).
func findSideColumn(s *Snapshot) (int, bool) {
	n := len(s.Screen)
	if n < sideMinRows || s.Cols < 40 {
		return 0, false
	}
	counts := map[int]int{}
	for _, r := range s.Screen {
		for x, c := range r.Cells {
			if isVertRule(c) && x >= s.Cols/5 && x <= s.Cols*9/10 {
				counts[x]++
			}
		}
	}
	col, best := -1, 0
	for x, k := range counts {
		if k > best || k == best && x < col {
			col, best = x, k
		}
	}
	if col < 0 || best < sideMinRows || best*10 < n*6 {
		return 0, false
	}
	return col, true
}

// cutSidePanel returns a copy of s whose rows are cut at the divider column
// (rows without the divider there are kept whole: a full-width status bar), and
// the text of the right-hand parts, blanks dropped. s itself is unchanged.
func cutSidePanel(s *Snapshot) (*Snapshot, []string) {
	col, ok := findSideColumn(s)
	if !ok {
		return s, nil
	}
	cp := *s
	cp.Screen = make([]Row, len(s.Screen))
	var side []string
	for y, r := range s.Screen {
		if col < len(r.Cells) && isVertRule(r.Cells[col]) {
			left := Row{Cells: append([]Cell(nil), r.Cells[:col]...), Wrapped: r.Wrapped}
			cp.Screen[y] = left
			right := Row{Cells: r.Cells[col+1:]}
			if t := right.Text(); trimSpaces(t) != "" {
				side = append(side, trimSpaces(t))
			}
			continue
		}
		cp.Screen[y] = r
	}
	return &cp, side
}

func trimSpaces(t string) string {
	lo, hi := 0, len(t)
	for lo < hi && (t[lo] == ' ' || t[lo] == '\t') {
		lo++
	}
	for hi > lo && (t[hi-1] == ' ' || t[hi-1] == '\t') {
		hi--
	}
	return t[lo:hi]
}
