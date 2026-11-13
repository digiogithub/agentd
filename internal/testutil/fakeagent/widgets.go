package main

import (
	"bufio"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"
	"unicode/utf8"

	"golang.org/x/term"
)

// Flags of the widget modes (see the package comment in main.go).
var (
	wMenu      = new(string)
	wNumbered  = new(bool)
	wDigitMove = new(bool)
	wYesNo     = new(string)
	wDialog    = new(bool)
	wTitle     = new(string)
	wBox       = new(bool)
	wLoop      = new(bool)
	wAppCursor = new(bool)
	wBell      = new(bool)
	wOSC9      = new(string)
	wWrap      = new(bool)
	wWrapAt    = new(int)
)

func registerWidgetFlags() {
	wMenu = flag.String("menu", "", `pointer menu items, "A|B|C"`)
	wNumbered = flag.Bool("numbered", false, "number the menu items; digits select (default items without -menu)")
	wDigitMove = flag.Bool("digitmove", false, "with -numbered/-dialog a digit only moves the pointer")
	wYesNo = flag.String("yesno", "", `ask "<text> (y/n)"`)
	wDialog = flag.Bool("dialog", false, "boxed dialog with numbered options and an Esc footer")
	wTitle = flag.String("title", "Do you want to proceed?", "dialog title")
	wBox = flag.Bool("box", false, "Claude-like input box (use with -ghost)")
	wLoop = flag.Bool("loop", false, "redraw the widget after each resolution instead of exiting")
	wAppCursor = flag.Bool("appcursor", false, "enable DECCKM: widgets accept only SS3 arrows")
	wBell = flag.Bool("bell", false, "emit BEL after each turn")
	wOSC9 = flag.String("osc9", "", `emit "OSC 9;text" after each turn`)
	wWrap = flag.Bool("wrap", false, "print long paragraphs (terminal autowrap)")
	wWrapAt = flag.Int("wrapat", 0, "print the paragraphs hard-wrapped at N columns")
}

func widgetMode() bool {
	return *wMenu != "" || *wNumbered || *wYesNo != "" || *wDialog || *wBox
}

// turnEnd emits the end-of-turn notification signals.
func turnEnd(out *bufio.Writer) {
	if *wBell {
		out.WriteString("\a")
	}
	if *wOSC9 != "" {
		out.WriteString("\x1b]9;" + *wOSC9 + "\x07")
	}
	_ = out.Flush()
}

var paragraphs = []string{
	"The quick brown fox jumps over the lazy dog while the build pipeline compiles every package, runs the race detector and reports a summary of the results for the whole repository without any failure.",
	"Wrapped output has no newline where the terminal autowrap continued the line, so a reader that joins rows must decide by indentation, punctuation and the width of the previous row whether two rows belong together.",
	"Short paragraph.",
}

// writeParagraphs prints the paragraphs, each followed by a blank line. With
// at > 0 the text is word-wrapped at that many columns with explicit CRLFs.
func writeParagraphs(out *bufio.Writer, at int) {
	for _, p := range paragraphs {
		if at <= 0 {
			out.WriteString(p + "\r\n\r\n")
			continue
		}
		col := 0
		for i, w := range strings.Fields(p) {
			n := utf8.RuneCountInString(w)
			switch {
			case i == 0:
			case col+1+n > at:
				out.WriteString("\r\n")
				col = 0
			default:
				out.WriteString(" ")
				col++
			}
			out.WriteString(w)
			col += n
		}
		out.WriteString("\r\n\r\n")
	}
	_ = out.Flush()
}

// readKey returns the next key: "up", "down", "left", "right", "enter", "esc",
// "bs", "ctrl-c", "ctrl-d", "paste[", "paste]", "ss3-up".. (SS3 arrows),
// or the typed character. ok is false when input is closed.
func readKey(in <-chan byte) (string, bool) {
	c, ok := <-in
	if !ok {
		return "", false
	}
	switch {
	case c == '\r' || c == '\n':
		return "enter", true
	case c == 0x03:
		return "ctrl-c", true
	case c == 0x04:
		return "ctrl-d", true
	case c == 0x7f || c == 0x08:
		return "bs", true
	case c == 0x1b:
		return readEscape(in), true
	case c >= 0xc0:
		b := []byte{c}
		for n := utf8.UTFMax; n > 1 && !utf8.FullRune(b); n-- {
			x, ok := <-in
			if !ok {
				break
			}
			b = append(b, x)
		}
		return string(b), true
	}
	return string([]byte{c}), true
}

func next(in <-chan byte) (byte, bool) {
	select {
	case c, ok := <-in:
		return c, ok
	case <-time.After(60 * time.Millisecond):
		return 0, false
	}
}

func readEscape(in <-chan byte) string {
	c, ok := next(in)
	if !ok {
		return "esc"
	}
	arrow := map[byte]string{'A': "up", 'B': "down", 'C': "right", 'D': "left"}
	switch c {
	case 'O':
		f, ok := next(in)
		if !ok {
			return "esc"
		}
		if a, ok := arrow[f]; ok {
			return "ss3-" + a
		}
		return "other"
	case '[':
		var params []byte
		for {
			f, ok := next(in)
			if !ok {
				return "other"
			}
			if f >= 0x40 && f <= 0x7e {
				if a, ok := arrow[f]; ok && len(params) == 0 {
					return a
				}
				switch string(params) + string(f) {
				case "200~":
					return "paste["
				case "201~":
					return "paste]"
				}
				return "other"
			}
			params = append(params, f)
			if len(params) > 16 {
				return "other"
			}
		}
	}
	return "other"
}

// arrowKey normalises up/down honouring DECCKM: CSI arrows work only without
// -appcursor, SS3 arrows only with it.
func arrowKey(k string) string {
	if *wAppCursor {
		if strings.HasPrefix(k, "ss3-") {
			return strings.TrimPrefix(k, "ss3-")
		}
		switch k {
		case "up", "down", "left", "right":
			return "other"
		}
		return k
	}
	if strings.HasPrefix(k, "ss3-") {
		return "other"
	}
	return k
}

// runWidget runs the selected widget until it resolves (or forever with -loop).
func runWidget(out *bufio.Writer, in <-chan byte, ghost string, exit func(int)) {
	for {
		switch {
		case *wBox:
			runBox(out, in, ghost, exit)
			continue // the box is a loop on its own: only Ctrl-C/D leave it
		case *wYesNo != "":
			runYesNo(out, in, exit)
		default:
			runMenu(out, in, exit)
		}
		turnEnd(out)
		if !*wLoop {
			return
		}
	}
}

func menuItems() []string {
	if *wMenu != "" {
		return strings.Split(*wMenu, "|")
	}
	if *wDialog || *wNumbered {
		return []string{"Yes", "Yes, and don't ask again", "No, and tell the agent what to do differently"}
	}
	return []string{"Alpha", "Beta", "Gamma"}
}

func numberedMode() bool { return *wNumbered || *wDialog }

func runYesNo(out *bufio.Writer, in <-chan byte, exit func(int)) {
	fmt.Fprintf(out, "%s (y/n) ", *wYesNo)
	_ = out.Flush()
	for {
		k, ok := readKey(in)
		if !ok {
			exit(0)
		}
		switch k {
		case "y", "Y":
			out.WriteString("y\r\nchose: yes\r\n")
			return
		case "n", "N":
			out.WriteString("n\r\nchose: no\r\n")
			return
		case "esc":
			out.WriteString("\r\ncancelled\r\n")
			return
		case "ctrl-c":
			exit(130)
		}
	}
}

// menuLines renders the menu body (title excluded) with the pointer on sel.
func menuLines(items []string, sel int) []string {
	lines := make([]string, len(items))
	for i, it := range items {
		mark := "  "
		if i == sel {
			mark = "❯ "
		}
		if numberedMode() {
			it = fmt.Sprintf("%d. %s", i+1, it)
		}
		lines[i] = mark + it
	}
	return lines
}

const dialogWidth = 60

func boxLine(s string) string {
	pad := dialogWidth - 2 - utf8.RuneCountInString(s)
	if pad < 0 {
		pad = 0
	}
	return "│" + s + strings.Repeat(" ", pad) + "│"
}

// drawMenu returns the full lines of the widget for the current selection.
func drawMenu(items []string, sel int) []string {
	body := menuLines(items, sel)
	if !*wDialog {
		return append([]string{"Pick one:"}, body...)
	}
	lines := []string{"╭" + strings.Repeat("─", dialogWidth-2) + "╮", boxLine(" " + *wTitle), boxLine("")}
	for _, l := range body {
		lines = append(lines, boxLine(" "+l))
	}
	lines = append(lines, boxLine(""), "╰"+strings.Repeat("─", dialogWidth-2)+"╯", " Enter to confirm · Esc to cancel")
	return lines
}

func runMenu(out *bufio.Writer, in <-chan byte, exit func(int)) {
	items := menuItems()
	sel := 0
	out.WriteString("\x1b[?25l")
	paint := func(first bool) {
		lines := drawMenu(items, sel)
		if !first {
			fmt.Fprintf(out, "\x1b[%dA", len(lines))
		}
		for _, l := range lines {
			out.WriteString("\r\x1b[K" + l + "\r\n")
		}
		_ = out.Flush()
	}
	paint(true)
	finish := func(msg string) {
		out.WriteString(msg + "\r\n\x1b[?25h")
	}
	for {
		k, ok := readKey(in)
		if !ok {
			exit(0)
		}
		switch k = arrowKey(k); k {
		case "up":
			if sel > 0 {
				sel--
			}
			paint(false)
		case "down":
			if sel < len(items)-1 {
				sel++
			}
			paint(false)
		case "enter":
			finish("chose: " + items[sel])
			return
		case "esc":
			if *wDialog {
				finish("cancelled")
				return
			}
		case "ctrl-c":
			exit(130)
		default:
			if len(k) == 1 && k[0] >= '1' && k[0] <= '9' && numberedMode() {
				if i := int(k[0] - '1'); i < len(items) {
					sel = i
					if *wDigitMove {
						paint(false)
						continue
					}
					finish("chose: " + items[sel])
					return
				}
			}
		}
	}
}

// runBox draws the Claude-like input box and handles typing; it returns only
// through exit (Ctrl-C / Ctrl-D on an empty line / EOF).
func runBox(out *bufio.Writer, in <-chan byte, ghost string, exit func(int)) {
	width := 80
	if w, _, err := term.GetSize(int(os.Stdout.Fd())); err == nil && w > 0 {
		width = w
	}
	rule := "\x1b[38;5;244m" + strings.Repeat("─", width) + "\x1b[39m"
	const prompt = "❯ "
	var line strings.Builder
	ghostShown := false
	draw := func() {
		out.WriteString(rule + "\r\n" + prompt)
		if ghost != "" {
			fmt.Fprintf(out, "\x1b[2m%s\x1b[22m", ghost)
			ghostShown = true
		}
		out.WriteString("\x1b[K\r\n" + rule + "\r\n  ? for shortcuts\x1b[2A\r")
		fmt.Fprintf(out, "\x1b[%dC", utf8.RuneCountInString(prompt))
		_ = out.Flush()
	}
	clearGhost := func() {
		if ghostShown {
			out.WriteString("\x1b[K")
			ghostShown = false
		}
	}
	draw()
	pasting := false
	for {
		k, ok := readKey(in)
		if !ok {
			exit(0)
		}
		switch k {
		case "paste[":
			pasting = true
		case "paste]":
			pasting = false
		case "ctrl-c":
			exit(130)
		case "ctrl-d":
			if line.Len() == 0 {
				exit(0)
			}
		case "bs":
			if s := []rune(line.String()); len(s) > 0 {
				line.Reset()
				line.WriteString(string(s[:len(s)-1]))
				out.WriteString("\b \b")
			}
		case "enter":
			if pasting {
				line.WriteByte('\n')
				out.WriteString("↵")
				break
			}
			msg := line.String()
			if msg == "" && ghostShown {
				msg = ghost // the harness accepts the suggestion
			}
			line.Reset()
			ghostShown = false
			fmt.Fprintf(out, "\x1b[K\x1b[3B\rGOT: %s\r\n", strings.ReplaceAll(msg, "\n", `\n`))
			if strings.HasPrefix(msg, "/exit") {
				exit(0)
			}
			turnEnd(out)
			draw()
		case "up", "down", "left", "right", "ss3-up", "ss3-down", "ss3-left", "ss3-right", "esc", "other":
		default:
			clearGhost()
			line.WriteString(k)
			out.WriteString(k)
		}
		_ = out.Flush()
	}
}
