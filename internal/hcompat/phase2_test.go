package hcompat_test

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/digiogithub/agentd/internal/client"
	"github.com/digiogithub/agentd/internal/hcompat"
	"github.com/digiogithub/agentd/internal/proto"
	"github.com/digiogithub/agentd/internal/testutil"
)

func sessionInfo(t *testing.T, sock, name string) proto.SessionInfo {
	t.Helper()
	cl, err := client.Dial(sock)
	if err != nil {
		t.Fatal(err)
	}
	defer cl.Close()
	all, err := cl.Ls()
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range all {
		if s.Name == name {
			return s
		}
	}
	t.Fatalf("no session %s", name)
	return proto.SessionInfo{}
}

func screen(t *testing.T, sock, name string) string {
	t.Helper()
	cl, err := client.Dial(sock)
	if err != nil {
		t.Fatal(err)
	}
	defer cl.Close()
	s, _ := cl.Dump(proto.DumpArgs{Name: name})
	return s
}

func attach(t *testing.T, c *hcClient, params map[string]any) uint32 {
	t.Helper()
	att := result(t, c.call("terminal.attach", params))
	checkResult(t, loadSchema(t), "terminal.attach", att)
	return uint32(att["channel"].(float64))
}

// automatic_input is negotiated in hello and attach (Desktop and Whip require
// both before sending kind 6) and never counts as a human draft.
func TestAutomaticInputNegotiatedAndNotHuman(t *testing.T) {
	sock := startDaemon(t)
	runSession(t, sock, "cat@proj", "cat")
	c := newHCClient(t, sock)
	hello := c.helloFramed()
	if !strings.Contains(strings.Join(anyStrings(hello["features"]), ","), "automatic_input") {
		t.Fatalf("features = %v", hello["features"])
	}
	att := result(t, c.call("terminal.attach", map[string]any{"terminal": "cat@proj", "cols": 80, "rows": 24}))
	if att["automatic_input"] != true {
		t.Fatalf("attach automatic_input = %v", att["automatic_input"])
	}
	ch := uint32(att["channel"].(float64))
	c.readStream(ch, func(b []byte) bool { return len(b) > 0 })
	c.send(ch, hcompat.KindAutomaticInput, []byte("auto-bytes"))
	c.readStream(ch, func(b []byte) bool { return bytes.Contains(b, []byte("auto-bytes")) })
	time.Sleep(100 * time.Millisecond)
	if st := sessionInfo(t, sock, "cat@proj").InputState; st == "draft" {
		t.Fatalf("automatic input made a draft")
	}
}

// An observer never types or resizes; a resumed viewer takes the size only
// once it acts.
func TestObserveAndResumed(t *testing.T) {
	sock := startDaemon(t)
	runSession(t, sock, "cat@proj", "cat") // runSession starts it at 80x24
	c := newHCClient(t, sock)
	c.helloFramed()

	obs := attach(t, c, map[string]any{"terminal": "cat@proj", "cols": 120, "rows": 40, "observe": true})
	c.readStream(obs, func(b []byte) bool { return len(b) > 0 })
	c.send(obs, hcompat.KindResize, hcompat.ResizePayload(100, 30))
	c.send(obs, hcompat.KindData, []byte("observer-typed\r"))
	time.Sleep(300 * time.Millisecond)
	if s := sessionInfo(t, sock, "cat@proj"); s.Cols != 80 || s.Rows != 24 {
		t.Fatalf("observer resized: %dx%d", s.Cols, s.Rows)
	}
	if strings.Contains(screen(t, sock, "cat@proj"), "observer-typed") {
		t.Fatal("observer input reached the program")
	}

	res := attach(t, c, map[string]any{"terminal": "cat@proj", "cols": 110, "rows": 35, "resumed": true})
	c.readStream(res, func(b []byte) bool { return len(b) > 0 })
	time.Sleep(200 * time.Millisecond)
	if s := sessionInfo(t, sock, "cat@proj"); s.Cols != 80 {
		t.Fatalf("resumed viewer took the size at attach: %dx%d", s.Cols, s.Rows)
	}
	c.send(res, hcompat.KindFocus, []byte{1})
	testutil.WaitFor(t, 10*time.Second, func() bool {
		s := sessionInfo(t, sock, "cat@proj")
		return s.Cols == 110 && s.Rows == 35
	}, "resumed viewer sets the size once focused", nil)
}

// history paints scrollback before the screen (Desktop asks for 2000 lines).
func TestAttachHistory(t *testing.T) {
	sock := startDaemon(t)
	runSession(t, sock, "log@proj", "sh", "-c", `i=1; while [ $i -le 200 ]; do echo hist-$i; i=$((i+1)); done; exec cat`)
	testutil.WaitFor(t, 10*time.Second, func() bool { return strings.Contains(screen(t, sock, "log@proj"), "hist-200") }, "output", nil)
	c := newHCClient(t, sock)
	c.helloFramed()
	ch := attach(t, c, map[string]any{"terminal": "log@proj", "cols": 80, "rows": 24, "history": 2000})
	paint := c.readStream(ch, func(b []byte) bool { return bytes.Contains(b, []byte("hist-200")) })
	if !bytes.Contains(paint, []byte("hist-5\x1b[0m")) {
		t.Fatalf("scrollback not painted (%d bytes)", len(paint))
	}
	if paint[0] != 0x18 {
		t.Fatal("paint does not start with CAN")
	}
}

// Focus reaches a program that asked for focus events (DEC 1004).
func TestFocusForwarded(t *testing.T) {
	sock := startDaemon(t)
	runSession(t, sock, "foc@proj", "sh", "-c", `printf '\033[?1004h'; exec cat -v`)
	c := newHCClient(t, sock)
	c.helloFramed()
	ch := attach(t, c, map[string]any{"terminal": "foc@proj", "cols": 80, "rows": 24})
	c.readStream(ch, func(b []byte) bool { return len(b) > 0 })
	time.Sleep(200 * time.Millisecond)
	c.send(ch, hcompat.KindFocus, []byte{1})
	c.send(ch, hcompat.KindData, []byte("\r"))
	testutil.WaitFor(t, 10*time.Second, func() bool { return strings.Contains(screen(t, sock, "foc@proj"), "^[[I") },
		"focus-in report", func() string { return screen(t, sock, "foc@proj") })
}

// Several terminals on one connection, and a viewer that reconnects (new
// connection, fresh attach) gets a new first paint and keeps typing.
func TestMultipleTerminalsAndReconnect(t *testing.T) {
	sock := startDaemon(t)
	runSession(t, sock, "one@proj", "sh", "-c", "echo first-term; exec cat")
	runSession(t, sock, "two@proj", "sh", "-c", "echo second-term; exec cat")
	testutil.WaitFor(t, 10*time.Second, func() bool {
		return strings.Contains(screen(t, sock, "one@proj"), "first-term") && strings.Contains(screen(t, sock, "two@proj"), "second-term")
	}, "both programs printed", nil)
	c := newHCClient(t, sock)
	c.helloFramed()
	a := attach(t, c, map[string]any{"terminal": "one@proj", "cols": 80, "rows": 24})
	b := attach(t, c, map[string]any{"terminal": "two@proj", "cols": 80, "rows": 24})
	if a == b {
		t.Fatal("same channel for two attaches")
	}
	c.readStream(a, func(x []byte) bool { return bytes.Contains(x, []byte("first-term")) })
	c.readStream(b, func(x []byte) bool { return bytes.Contains(x, []byte("second-term")) })
	c.send(b, hcompat.KindData, []byte("only-in-two\r"))
	c.readStream(b, func(x []byte) bool { return bytes.Count(x, []byte("only-in-two")) >= 2 })
	if strings.Contains(screen(t, sock, "one@proj"), "only-in-two") {
		t.Fatal("input crossed terminals")
	}

	// Reconnect: a second connection (Desktop restarts its core child).
	r := newHCClient(t, sock)
	r.helloFramed()
	ch := attach(t, r, map[string]any{"terminal": "two@proj", "cols": 80, "rows": 24, "resumed": true})
	paint := r.readStream(ch, func(x []byte) bool { return bytes.Contains(x, []byte("only-in-two")) })
	if paint[0] != 0x18 {
		t.Fatal("reattach paint does not start with CAN")
	}
	r.send(ch, hcompat.KindData, []byte("after-reconnect\r"))
	r.readStream(ch, func(x []byte) bool { return bytes.Count(x, []byte("after-reconnect")) >= 2 })
}

func anyStrings(v any) []string {
	var out []string
	for _, x := range v.([]any) {
		out = append(out, x.(string))
	}
	return out
}
