// Command record captures the screenfix fixtures by running
// internal/testutil/fakeagent modes in a PTY and saving the raw output.
// It never runs a real agent CLI.
//
//	go run ./internal/testutil/screenfix/record [-out internal/screen/testdata] [name...]
//
// After regenerating logs run `go test ./internal/testutil/screenfix -update`
// to refresh the goldens.
package main

import (
	"bytes"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"sync"
	"time"

	"github.com/creack/pty"

	"github.com/digiogithub/agentd/internal/testutil"
	"github.com/digiogithub/agentd/internal/testutil/screenfix"
)

const (
	up    = "\x1b[A"
	down  = "\x1b[B"
	ss3Dn = "\x1bOB"
)

type step struct {
	send string        // bytes typed into the PTY
	wait time.Duration // extra pause after sending (default 400ms)
}

type recipe struct {
	name        string
	desc        string
	cols, rows  int
	args        []string
	steps       []step
	profile     string
	waitStartup time.Duration
}

var recipes = []recipe{
	{name: "fake-menu", desc: "pointer menu, pointer moved down once (Beta selected)", cols: 120, rows: 40,
		args: []string{"-menu", "Alpha|Beta|Gamma"}, steps: []step{{send: down}}},
	{name: "fake-numbered", desc: "numbered menu, pointer moved to item 2 by a digit (-digitmove)", cols: 120, rows: 40,
		args: []string{"-numbered", "-digitmove"}, steps: []step{{send: "2"}}},
	{name: "fake-yesno", desc: "yes/no prompt waiting for an answer", cols: 120, rows: 40,
		args: []string{"-yesno", "Proceed with the change?"}},
	{name: "fake-dialog", desc: "boxed permission-style dialog with numbered options", cols: 120, rows: 40,
		args: []string{"-dialog", "-digitmove"}, steps: []step{{send: down}}},
	{name: "fake-box-ghost", desc: "Claude-like input box with a faint ghost suggestion", cols: 120, rows: 40,
		args: []string{"-box", "-ghost", "Try \"fix lint errors\"", "-osc9", "turn done"}},
	{name: "fake-wrap-120", desc: "long paragraphs wrapped by the terminal autowrap at 120 columns", cols: 120, rows: 40,
		args: []string{"-wrap"}},
	{name: "fake-wrap-80", desc: "long paragraphs wrapped by the terminal autowrap at 80x24", cols: 80, rows: 24,
		args: []string{"-wrap"}},
	{name: "fake-wrapat-80", desc: "paragraphs hard-wrapped by the app at 60 columns (Ink-like), 80x24", cols: 80, rows: 24,
		args: []string{"-wrapat", "60"}},
	{name: "fake-appcursor-menu", desc: "menu with DECCKM on, pointer moved with an SS3 arrow", cols: 120, rows: 40,
		args: []string{"-menu", "Alpha|Beta|Gamma", "-appcursor"}, steps: []step{{send: ss3Dn}}},
	{name: "fake-redraw-claude", desc: "Claude-like alt-screen TUI repainting everything per token; 9 transcript rows visible of a 40-line conversation (history fixture)", cols: 60, rows: 14,
		args: []string{"-alt", "-redraw", "-redrawturns", "5", "-redrawlines", "3", "-redrawwords", "5"}, profile: "claude"},
	{name: "fake-bell", desc: "plain prompt, one message submitted, BEL after the turn", cols: 80, rows: 24,
		args: []string{"-bell"}, steps: []step{{send: "hello\r"}}},
}

// claudeCaptures are the byte fixtures of internal/testutil/claude_fixtures.go
// (real Claude Code 2.1.319 turn ends, 60x6) stored as regular fixtures.
var claudeCaptures = []struct{ name, desc, data string }{
	{"claude-post-turn-suggestion", "Claude Code turn end with a faint suggested prompt on the input line", testutil.ClaudePostTurnSuggestion},
	{"claude-post-turn-empty", "Claude Code turn end with an empty input line", testutil.ClaudePostTurnEmpty},
}

func main() {
	out := flag.String("out", screenfix.Dir(), "fixture output directory")
	flag.Parse()
	bin, cleanup, err := build()
	if err != nil {
		fatal(err)
	}
	defer cleanup()
	want := map[string]bool{}
	for _, a := range flag.Args() {
		want[a] = true
	}
	for _, c := range claudeCaptures {
		if len(want) > 0 && !want[c.name] {
			continue
		}
		meta := screenfix.Meta{Cols: 60, Rows: 6, Profile: "claude", Description: c.desc,
			Cmd: []string{"converted from internal/testutil/claude_fixtures.go"}}
		if err := screenfix.Save(*out, c.name, meta, []byte(c.data)); err != nil {
			fatal(err)
		}
		fmt.Println("converted", c.name)
	}
	for _, r := range recipes {
		if len(want) > 0 && !want[r.name] {
			continue
		}
		if err := record(bin, *out, r); err != nil {
			fatal(fmt.Errorf("%s: %w", r.name, err))
		}
		fmt.Println("recorded", r.name)
	}
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, "record:", err)
	os.Exit(1)
}

func build() (string, func(), error) {
	dir, err := os.MkdirTemp("", "agentd-record-")
	if err != nil {
		return "", nil, err
	}
	bin := dir + "/fakeagent"
	cmd := exec.Command("go", "build", "-o", bin, "github.com/digiogithub/agentd/internal/testutil/fakeagent")
	if b, err := cmd.CombinedOutput(); err != nil {
		_ = os.RemoveAll(dir)
		return "", nil, fmt.Errorf("build fakeagent: %v\n%s", err, b)
	}
	return bin, func() { _ = os.RemoveAll(dir) }, nil
}

func record(bin, outDir string, r recipe) error {
	cmd := exec.Command(bin, r.args...)
	cmd.Env = append(os.Environ(), "TERM=xterm-256color")
	ptmx, err := pty.StartWithSize(cmd, &pty.Winsize{Cols: uint16(r.cols), Rows: uint16(r.rows)})
	if err != nil {
		return err
	}
	defer ptmx.Close()
	var (
		mu  sync.Mutex
		log bytes.Buffer
	)
	done := make(chan struct{})
	go func() {
		defer close(done)
		buf := make([]byte, 4096)
		for {
			n, err := ptmx.Read(buf)
			mu.Lock()
			log.Write(buf[:n])
			mu.Unlock()
			if err != nil {
				return
			}
		}
	}()
	var frames []int
	mark := func() {
		mu.Lock()
		frames = append(frames, log.Len())
		mu.Unlock()
	}
	time.Sleep(600 * time.Millisecond)
	mark()
	for _, s := range r.steps {
		if _, err := ptmx.WriteString(s.send); err != nil {
			return err
		}
		w := s.wait
		if w == 0 {
			w = 500 * time.Millisecond
		}
		time.Sleep(w)
		mark()
	}
	_ = cmd.Process.Kill()
	_ = cmd.Wait()
	<-done
	mu.Lock()
	defer mu.Unlock()
	// The last mark is the end of the log; ReplayFrames appends it anyway.
	frames = frames[:len(frames)-1]
	meta := screenfix.Meta{
		Cols: r.cols, Rows: r.rows, Profile: r.profile, Description: r.desc,
		Frames: frames, Cmd: append([]string{"fakeagent"}, r.args...),
	}
	if meta.Profile == "" {
		meta.Profile = "default"
	}
	return screenfix.Save(outDir, r.name, meta, log.Bytes())
}
