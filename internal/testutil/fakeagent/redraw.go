package main

import (
	"bufio"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"golang.org/x/term"
)

// Redraw mode (-redraw): a scripted Claude-like alternate-screen TUI that
// repaints the whole screen on every update. The transcript starts at the top
// above a status row and an input box and, once full, only the last rows that fit are
// visible, so older lines scroll out of the viewport and are gone from the
// emulator (no scrollback on the alternate screen). Each of -redrawturns turns
// adds a user line and then streams a reply of -redrawlines lines word by
// word, repainting after every word; a spinner status row with an elapsed
// counter shows while streaming. Every repaint is wrapped in a synchronized
// update (ESC[?2026h … ESC[?2026l) so tests can split a recording into frames
// at the ESC[?2026l markers (-redrawnosync omits them). Use with -alt. It needs no input and exits 0
// when the script ends.
var (
	rdOn     = new(bool)
	rdTurns  = new(int)
	rdLines  = new(int)
	rdWords  = new(int)
	rdNoSync = new(bool)
	rdDelay  = new(time.Duration)
)

func registerRedrawFlags() {
	rdOn = flag.Bool("redraw", false, "scripted Claude-like alt-screen TUI that repaints everything per token (use with -alt)")
	rdTurns = flag.Int("redrawturns", 8, "with -redraw: number of turns")
	rdLines = flag.Int("redrawlines", 4, "with -redraw: reply lines per turn")
	rdNoSync = flag.Bool("redrawnosync", false, "with -redraw: do not wrap repaints in synchronized updates (DEC 2026)")
	rdDelay = flag.Duration("redrawdelay", 0, "with -redraw: pause after every repaint")
	rdWords = flag.Int("redrawwords", 6, "with -redraw: words per reply line")
}

const (
	syncBegin = "\x1b[?2026h"
	syncEnd   = "\x1b[?2026l"
)

var redrawVocab = strings.Fields("alpha bravo charlie delta echo foxtrot golf hotel india juliet kilo lima mike november oscar papa quebec romeo sierra tango")

// redrawScript returns the full transcript lines of turn t (1-based): a user
// line, the reply lines and a blank separator. Lines are unique so tests can
// look for duplicates and ordering.
func redrawScript(t int) (user string, reply []string) {
	user = fmt.Sprintf("> question %02d", t)
	for l := 1; l <= *rdLines; l++ {
		w := make([]string, *rdWords)
		for i := range w {
			w[i] = redrawVocab[(t*7+l*3+i)%len(redrawVocab)]
		}
		reply = append(reply, fmt.Sprintf("  reply %02d.%d: %s", t, l, strings.Join(w, " ")))
	}
	return
}

// runRedraw plays the script and returns.
func runRedraw(out *bufio.Writer) {
	cols, rows := 80, 24
	if w, h, err := term.GetSize(int(os.Stdout.Fd())); err == nil && w > 0 && h > 0 {
		cols, rows = w, h
	}
	rule := "\x1b[38;5;244m" + strings.Repeat("─", cols) + "\x1b[39m"
	var transcript []string
	status := ""
	paint := func() {
		const chrome = 5 // status + rule + input + rule + footer
		view := rows - chrome
		vis := transcript
		if len(vis) > view {
			vis = vis[len(vis)-view:]
		}
		bsu, esu := syncBegin, syncEnd
		if *rdNoSync {
			bsu, esu = "", ""
		}
		out.WriteString(bsu + "\x1b[?25l\x1b[H")
		for i := 0; i < view; i++ {
			if i < len(vis) {
				out.WriteString(vis[i])
			}
			out.WriteString("\x1b[K\r\n")
		}
		out.WriteString(status + "\x1b[K\r\n" + rule + "\r\n❯ \x1b[K\r\n" + rule + "\r\n  ? for shortcuts\x1b[K")
		out.WriteString("\x1b[" + fmt.Sprint(view+3) + ";3H\x1b[?25h" + esu)
		_ = out.Flush()
		if *rdDelay > 0 {
			time.Sleep(*rdDelay)
		}
	}
	paint()
	tick := 0
	for t := 1; t <= *rdTurns; t++ {
		user, reply := redrawScript(t)
		transcript = append(transcript, user)
		paint()
		for _, l := range reply {
			ws := strings.Fields(l)
			transcript = append(transcript, "")
			for i := range ws {
				tick++
				transcript[len(transcript)-1] = "  " + strings.Join(ws[:i+1], " ")
				status = fmt.Sprintf("\x1b[38;5;208m✻ Thinking… (%ds · esc to interrupt)\x1b[39m", tick/3)
				paint()
			}
		}
		status = ""
		transcript = append(transcript, "")
		paint()
	}
	if *rdDelay > 0 { // let the last frame settle before the alt screen is torn down
		time.Sleep(500 * time.Millisecond)
	}
}
