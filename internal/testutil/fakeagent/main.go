// Command fakeagent is a minimal agent-like TUI used by tests. It never talks
// to any model. Behaviour:
//
//   - puts its terminal in raw mode and enables bracketed paste (ESC[?2004h);
//   - prints the prompt "> " and echoes typed characters;
//   - text between ESC[200~ and ESC[201~ is pasted content: newlines inside it
//     stay in the current message (echoed as "↵");
//   - Enter (\r, outside a paste) submits: prints "GOT: <message>" with
//     embedded newlines rendered as the two characters `\n`, then re-prompts;
//   - "/exit N" exits with code N; Ctrl-D on an empty line exits 0;
//     Ctrl-C exits 130;
//   - -alt uses the alternate screen; -busy=<duration> prints "working" plus a
//     dot every 100ms for that long after each message before re-prompting;
//   - -query sends a cursor position request (ESC[6n) at startup and prints
//     "CPR: <row>;<col>" or "CPR: timeout" (2s) before the first prompt;
//   - -ghost "text" draws a faint placeholder / suggested prompt after every
//     empty prompt with the cursor on its first cell, like Claude Code and
//     Codex (docs/ghost-text.md). It is erased by the first typed or pasted
//     byte; Enter on an empty line while it is shown submits the ghost text
//     (the harness behaviour agentd must never trigger);
//   - -droppaste ignores the content of the first bracketed paste (no echo,
//     the ghost stays), simulating a paste the TUI lost.
//
// Widget modes (semantic-model §8; see widgets.go) replace the line prompt by
// one interactive widget, resolve it, print "chose: <label>" (or "cancelled"),
// emit the turn-end signals and exit 0 (-loop redraws instead):
//
//   - -menu "A|B|C": pointer menu, "❯ " marks the selected item, Up/Down move
//     it, Enter selects. -numbered: items are numbered and digits select
//     (-digitmove: a digit only moves the pointer). -yesno "Proceed?": prints
//     "Proceed? (y/n)", y or n answers. -dialog [-title T]: the menu inside a
//     "╭─╮" frame with an "Esc to cancel" footer (digits select, Esc cancels).
//   - -box: Claude-like input box (rule, "❯"+NBSP prompt, rule, hint); with
//     -ghost the placeholder is faint. Enter prints "GOT: <message>".
//   - -appcursor enables DECCKM (ESC[?1h): widgets then accept only SS3
//     arrows (ESC O A); without it only CSI arrows (ESC [ A).
//   - -bell / -osc9 "text": after each turn (message, selection) emit BEL /
//     "OSC 9;text" (works in the plain prompt too).
//   - -redraw: scripted Claude-like alt-screen repainting TUI (see redraw.go).
//   - -wrap: print long paragraphs once at startup, relying on the terminal's
//     autowrap; -wrapat N hard-wraps them at N columns (Ink-like) instead.
package main

import (
	"bufio"
	"flag"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"golang.org/x/term"
)

const (
	pasteStart = "\x1b[200~"
	pasteEnd   = "\x1b[201~"
)

func main() {
	alt := flag.Bool("alt", false, "use the alternate screen")
	busy := flag.Duration("busy", 0, "simulate work for this long after each message")
	query := flag.Bool("query", false, "query the cursor position at startup")
	ghostText := flag.String("ghost", "", "faint suggested prompt shown on an empty input line")
	dropPaste := flag.Bool("droppaste", false, "ignore the first bracketed paste")
	registerWidgetFlags()
	registerRedrawFlags()
	flag.Parse()

	fd := int(os.Stdin.Fd())
	restore := func() {}
	if term.IsTerminal(fd) {
		if old, err := term.MakeRaw(fd); err == nil {
			restore = func() { _ = term.Restore(fd, old) }
		}
	}

	out := bufio.NewWriter(os.Stdout)
	flush := func() { _ = out.Flush() }
	exit := func(code int) {
		if *alt {
			out.WriteString("\x1b[?1049l")
		}
		out.WriteString("\x1b[?2004l")
		flush()
		restore()
		os.Exit(code)
	}

	in := make(chan byte, 4096)
	go func() {
		buf := make([]byte, 1024)
		for {
			n, err := os.Stdin.Read(buf)
			for _, c := range buf[:n] {
				in <- c
			}
			if err != nil {
				close(in)
				return
			}
		}
	}()

	if *alt {
		out.WriteString("\x1b[?1049h\x1b[H\x1b[2J")
	}
	out.WriteString("\x1b[?2004h")
	if *wAppCursor {
		out.WriteString("\x1b[?1h")
	}

	if *query {
		out.WriteString("\x1b[6n")
		flush()
		fmt.Fprintf(out, "CPR: %s\r\n", readCPR(in, 2*time.Second))
	}

	ghostShown := false
	prompt := func() {
		out.WriteString("> ")
		if *ghostText != "" {
			// Faint text, then move the cursor back onto its first cell.
			fmt.Fprintf(out, "\x1b[2m%s\x1b[22m\x1b[%dD", *ghostText, len([]rune(*ghostText)))
			ghostShown = true
		}
	}
	clearGhost := func() {
		if ghostShown {
			out.WriteString("\x1b[K")
			ghostShown = false
		}
	}

	out.WriteString("fakeagent ready\r\n")
	if *wWrap || *wWrapAt > 0 {
		writeParagraphs(out, *wWrapAt)
	}
	if *rdOn {
		runRedraw(out)
		exit(0)
	}
	if widgetMode() {
		runWidget(out, in, *ghostText, exit)
		exit(0)
	}
	prompt()
	flush()

	var line strings.Builder
	pasting := false
	dropping := false // inside the paste being dropped (-droppaste)
	dropped := false  // the first paste was already dropped
	var esc []byte    // pending escape sequence bytes

	for c := range in {
		if len(esc) > 0 || c == 0x1b {
			esc = append(esc, c)
			s := string(esc)
			switch {
			case s == pasteStart:
				pasting, esc = true, nil
				if *dropPaste && !dropped {
					dropping, dropped = true, true
				}
			case s == pasteEnd:
				pasting, dropping, esc = false, false, nil
			case strings.HasPrefix(pasteStart, s) || strings.HasPrefix(pasteEnd, s):
				// keep accumulating
			default:
				// Unknown sequence: swallow it once it looks complete.
				if len(esc) > 1 && (c >= 0x40 && c <= 0x7e) && c != '[' {
					esc = nil
				}
				if len(esc) > 32 {
					esc = nil
				}
			}
			continue
		}

		if dropping {
			continue
		}
		if pasting || c >= 0x20 || c == '\t' {
			clearGhost()
		}
		switch {
		case pasting && (c == '\r' || c == '\n'):
			line.WriteByte('\n')
			out.WriteString("↵")
		case c == '\r' || c == '\n':
			msg := line.String()
			if msg == "" && ghostShown {
				msg = *ghostText // the harness accepts the suggestion
				clearGhost()
			}
			line.Reset()
			fmt.Fprintf(out, "\r\nGOT: %s\r\n", strings.ReplaceAll(msg, "\n", `\n`))
			if strings.HasPrefix(msg, "/exit") {
				code, _ := strconv.Atoi(strings.TrimSpace(strings.TrimPrefix(msg, "/exit")))
				exit(code)
			}
			if *busy > 0 {
				out.WriteString("working")
				flush()
				deadline := time.Now().Add(*busy)
				for time.Now().Before(deadline) {
					time.Sleep(100 * time.Millisecond)
					out.WriteString(".")
					flush()
				}
				out.WriteString("\r\n")
			}
			turnEnd(out)
			prompt()
		case c == 0x03:
			exit(130)
		case c == 0x04 && line.Len() == 0:
			exit(0)
		case c == 0x7f || c == 0x08:
			if s := line.String(); len(s) > 0 {
				line.Reset()
				line.WriteString(s[:len(s)-1])
				out.WriteString("\b \b")
			}
		case c >= 0x20 || c == '\t' || c >= 0x80:
			line.WriteByte(c)
			out.WriteByte(c)
		}
		flush()
	}
	exit(0)
}

// readCPR waits for ESC [ row ; col R.
func readCPR(in <-chan byte, timeout time.Duration) string {
	t := time.NewTimer(timeout)
	defer t.Stop()
	var b []byte
	for {
		select {
		case c, ok := <-in:
			if !ok {
				return "eof"
			}
			b = append(b, c)
			if c == 'R' {
				s := string(b)
				if i := strings.LastIndex(s, "\x1b["); i >= 0 {
					return s[i+2 : len(s)-1]
				}
				return "bad"
			}
		case <-t.C:
			return "timeout"
		}
	}
}
