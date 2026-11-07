package session_test

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/charmbracelet/x/vt"

	"github.com/digiogithub/agentd/internal/session"
	"github.com/digiogithub/agentd/internal/testutil"
)

const timeout = 10 * time.Second

func TestMain(m *testing.M) {
	code := m.Run()
	testutil.CleanupFakeAgent()
	os.Exit(code)
}

func start(t *testing.T, spec session.Spec) *session.Session {
	t.Helper()
	s, err := session.Start(spec)
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func waitScreen(t *testing.T, s *session.Session, want string) {
	t.Helper()
	testutil.WaitFor(t, timeout, func() bool { return strings.Contains(s.Screen(), want) },
		"screen should contain "+want, func() string { return "screen:\n" + s.Screen() })
}

func waitDone(t *testing.T, s *session.Session) {
	t.Helper()
	select {
	case <-s.Done():
	case <-time.After(timeout):
		t.Fatalf("session did not exit; screen:\n%s", s.Screen())
	}
}

func fake(t *testing.T, args ...string) *session.Session {
	t.Helper()
	bin := testutil.BuildFakeAgent(t)
	s := start(t, session.Spec{Name: "fake", Cmd: append([]string{bin}, args...), Cols: 80, Rows: 24})
	waitScreen(t, s, "fakeagent ready")
	return s
}

func TestShellInputAndScreen(t *testing.T) {
	dir := t.TempDir()
	s := start(t, session.Spec{
		Name: "sh", Cwd: dir, Cmd: []string{"sh"},
		Env: map[string]string{"FOO": "bar-baz", "PS1": "$ "},
	})
	if cols, rows := s.Size(); cols != session.DefaultCols || rows != session.DefaultRows {
		t.Fatalf("default size = %dx%d", cols, rows)
	}
	if s.State() != session.StateRunning {
		t.Fatalf("state = %s", s.State())
	}
	if _, err := s.Write([]byte("echo $FOO-$TERM; pwd\r"), true); err != nil {
		t.Fatal(err)
	}
	waitScreen(t, s, "bar-baz-xterm-256color")
	waitScreen(t, s, dir)
	if s.LastHumanInput().IsZero() || s.LastOutput().IsZero() {
		t.Fatal("activity timestamps not updated")
	}
	if s.StartedAt().After(s.LastOutput()) {
		t.Fatal("LastOutput before StartedAt")
	}
	for _, l := range strings.Split(s.Screen(), "\n") {
		if strings.HasSuffix(l, " ") {
			t.Fatalf("line has trailing spaces: %q", l)
		}
	}
	if !bytes.Contains(s.RecentOutput(), []byte("bar-baz")) {
		t.Fatal("ring buffer missing output")
	}
	if !strings.Contains(s.ScreenANSI(), "bar-baz") {
		t.Fatal("ScreenANSI missing output")
	}
	if s.Name() != "sh" || s.Spec().Cwd != dir || s.Pid() <= 0 {
		t.Fatal("accessors")
	}
}

func TestResize(t *testing.T) {
	s := start(t, session.Spec{Name: "sh", Cmd: []string{"sh"}, Env: map[string]string{"PS1": "$ "}})
	if err := s.Resize(100, 30); err != nil {
		t.Fatal(err)
	}
	if c, r := s.Size(); c != 100 || r != 30 {
		t.Fatalf("size = %dx%d", c, r)
	}
	_, _ = s.Write([]byte("stty size\r"), false)
	waitScreen(t, s, "30 100")
	if err := s.Resize(0, 10); err == nil {
		t.Fatal("expected error for zero size")
	}
}

func TestExitCodeAndPostExit(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "logs", "x.log")
	s := start(t, session.Spec{Name: "x", Cmd: []string{"sh", "-c", "echo final-words; exit 7"}, LogPath: logPath})
	_, ch := s.Subscribe()
	waitDone(t, s)
	code, ok := s.ExitCode()
	if !ok || code != 7 {
		t.Fatalf("exit = %d, %v", code, ok)
	}
	if s.State() != session.StateExited {
		t.Fatal("state should be exited")
	}
	// subscriber channel closed on exit (after delivering what was buffered)
	for range ch {
	}
	if !strings.Contains(s.Screen(), "final-words") {
		t.Fatalf("screen lost after exit: %q", s.Screen())
	}
	if _, err := s.Write([]byte("x"), true); err != session.ErrExited {
		t.Fatalf("write after exit: %v", err)
	}
	if err := s.Signal(syscall.SIGTERM); err != session.ErrExited {
		t.Fatalf("signal after exit: %v", err)
	}
	if err := s.Resize(50, 10); err != nil {
		t.Fatalf("resize after exit: %v", err)
	}
	_, ch2 := s.Subscribe()
	if _, open := <-ch2; open {
		t.Fatal("subscribe after exit should return a closed channel")
	}
	b, err := os.ReadFile(logPath)
	if err != nil || !bytes.Contains(b, []byte("final-words")) {
		t.Fatalf("log file: %v %q", err, b)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestSignalAndClose(t *testing.T) {
	s := start(t, session.Spec{Name: "sleep", Cmd: []string{"sleep", "60"}})
	if err := s.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	waitDone(t, s)
	if code, _ := s.ExitCode(); code != 128+int(syscall.SIGTERM) {
		t.Fatalf("exit code = %d", code)
	}

	s2 := start(t, session.Spec{Name: "sh", Cmd: []string{"sh", "-c", "trap '' HUP; sleep 60"}})
	time.Sleep(100 * time.Millisecond)
	t0 := time.Now()
	if err := s2.Close(); err != nil {
		t.Fatal(err)
	}
	if s2.State() != session.StateExited || time.Since(t0) > 8*time.Second {
		t.Fatal("close did not terminate the child")
	}
}

// A grandchild keeping the PTY slave open must not keep the session alive.
func TestExitWithBackgroundGrandchild(t *testing.T) {
	s := start(t, session.Spec{Name: "bg", Cmd: []string{"sh", "-c", "(trap '' HUP; exec sleep 5) & sleep 0.1; exit 3"}})
	waitDone(t, s)
	if code, _ := s.ExitCode(); code != 3 {
		t.Fatalf("exit code = %d", code)
	}
}

func TestSubscriberFanOut(t *testing.T) {
	s := start(t, session.Spec{Name: "sh", Cmd: []string{"sh"}, Env: map[string]string{"PS1": "$ "}})
	id1, ch1 := s.Subscribe()
	_, ch2 := s.Subscribe()
	_, _ = s.Write([]byte("echo fan-out-marker\r"), false)
	for i, ch := range []<-chan []byte{ch1, ch2} {
		var got []byte
		deadline := time.After(timeout)
		for !bytes.Contains(got, []byte("fan-out-marker\r\n")) {
			select {
			case b, ok := <-ch:
				if !ok {
					t.Fatalf("sub %d closed", i)
				}
				got = append(got, b...)
			case <-deadline:
				t.Fatalf("sub %d got %q", i, got)
			}
		}
	}
	s.Unsubscribe(id1)
	if _, ok := <-drain(ch1); ok {
		t.Fatal("unsubscribed channel should be closed")
	}
	s.Unsubscribe(id1) // idempotent
}

// drain consumes buffered chunks and returns a channel that yields the
// final receive state.
func drain(ch <-chan []byte) <-chan []byte {
	for range ch {
	}
	return ch
}

func TestSlowSubscriberDropped(t *testing.T) {
	// Many small, separately flushed writes so the reader sees many chunks.
	s := start(t, session.Spec{Name: "spam", Cmd: []string{"sh", "-c",
		"i=0; while [ $i -lt 600 ]; do echo line-$i; sleep 0.002; i=$((i+1)); done; sleep 30"}})
	_, slow := s.Subscribe() // never read until the end
	_, fast := s.Subscribe()
	var got []byte
	chunks := 0
	deadline := time.After(30 * time.Second)
	for !bytes.Contains(got, []byte("line-599")) {
		select {
		case b, ok := <-fast:
			if !ok {
				t.Fatal("fast subscriber dropped")
			}
			chunks++
			got = append(got, b...)
		case <-deadline:
			t.Fatal("fast subscriber starved")
		}
	}
	if chunks <= session.SubscriberBuffer {
		t.Skipf("only %d chunks produced; cannot overflow the subscriber buffer", chunks)
	}
	waitScreen(t, s, "line-599")
	n := 0
	for range slow { // must be closed (dropped) while the child still runs
		n++
	}
	if n != session.SubscriberBuffer {
		t.Fatalf("slow subscriber received %d chunks before drop, want %d", n, session.SubscriberBuffer)
	}
	if s.State() != session.StateRunning {
		t.Fatal("session should still run")
	}
}

func TestFakeAgentTyping(t *testing.T) {
	s := fake(t)
	_, _ = s.Write([]byte("hello world\r"), true)
	waitScreen(t, s, "GOT: hello world")
	if !s.BracketedPasteEnabled() {
		t.Fatal("fakeagent enables bracketed paste")
	}
}

func TestBracketedPasteEmbeddedNewline(t *testing.T) {
	s := fake(t)
	// Markers inside the text must be stripped.
	if err := s.Paste("first line\nsecond\x1b[201~ line"); err != nil {
		t.Fatal(err)
	}
	_, _ = s.Write([]byte("\r"), false)
	waitScreen(t, s, `GOT: first line\nsecond line`)
	if strings.Count(s.Screen(), "GOT:") != 1 {
		t.Fatalf("expected one message:\n%s", s.Screen())
	}
}

func TestFakeAgentExitCode(t *testing.T) {
	s := fake(t)
	_, _ = s.Write([]byte("/exit 5\r"), false)
	waitDone(t, s)
	if code, _ := s.ExitCode(); code != 5 {
		t.Fatalf("exit code = %d", code)
	}
}

// The emulator answers the cursor position request through the reply pump.
func TestTerminalQueryReply(t *testing.T) {
	bin := testutil.BuildFakeAgent(t)
	s := start(t, session.Spec{Name: "q", Cmd: []string{bin, "-query"}, Cols: 80, Rows: 24})
	waitScreen(t, s, "fakeagent ready")
	if !strings.Contains(s.Screen(), "CPR: 1;1") {
		t.Fatalf("no CPR reply:\n%s", s.Screen())
	}
}

func TestRedraw(t *testing.T) {
	s := fake(t)
	_, _ = s.Write([]byte("redraw-me\r"), true)
	waitScreen(t, s, "GOT: redraw-me")
	r := string(s.Redraw())
	if !strings.Contains(r, "GOT: redraw-me") || !strings.Contains(r, "\x1b[2J") {
		t.Fatalf("redraw = %q", r)
	}
	if strings.Contains(r, "\x1b[?1049h") {
		t.Fatal("main-screen redraw must not switch to alt screen")
	}
	if !strings.Contains(r, "\x1b[?2004h") {
		t.Fatal("redraw should restore bracketed paste mode")
	}
	x, y := s.Cursor()
	if !strings.Contains(r, "\x1b["+itoa(y+1)+";"+itoa(x+1)+"H\x1b[?25h") {
		t.Fatalf("redraw lacks cursor position %d,%d: %q", x, y, r)
	}
}

func TestAltScreenRedraw(t *testing.T) {
	s := fake(t, "-alt")
	if !s.IsAltScreen() {
		t.Fatal("expected alt screen")
	}
	_, _ = s.Write([]byte("in-alt\r"), true)
	waitScreen(t, s, "GOT: in-alt")
	r := string(s.Redraw())
	if !strings.HasPrefix(r, "\x1b[?2026h\x1b[?1049h") || !strings.Contains(r, "GOT: in-alt") {
		t.Fatalf("alt redraw = %q", r)
	}
	_, _ = s.Write([]byte("/exit 0\r"), false)
	waitDone(t, s)
	if s.IsAltScreen() {
		t.Fatal("should have left alt screen")
	}
}

// Redraw followed by the subscription stream, replayed into a fresh emulator,
// must reproduce the session screen exactly (no gap, no duplication).
func TestSubscribeWithRedrawNoGap(t *testing.T) {
	s := fake(t, "-busy=400ms")
	_, _ = s.Write([]byte("one\r"), true)
	waitScreen(t, s, "working.") // mid-busy: dots still streaming
	redraw, id, ch := s.SubscribeWithRedraw()
	defer s.Unsubscribe(id)
	if !bytes.Contains(redraw, []byte("GOT: one")) {
		t.Fatalf("redraw = %q", redraw)
	}
	_, _ = s.Write([]byte("two\r"), true)
	waitScreen(t, s, "GOT: two")
	var stream []byte
	quiet := time.NewTimer(500 * time.Millisecond)
	deadline := time.After(timeout)
loop:
	for {
		select {
		case b := <-ch:
			stream = append(stream, b...)
			quiet.Reset(500 * time.Millisecond)
		case <-quiet.C:
			if time.Since(s.LastOutput()) > 400*time.Millisecond {
				break loop
			}
			quiet.Reset(100 * time.Millisecond)
		case <-deadline:
			t.Fatal("never quiesced")
		}
	}
	emu := vt.NewEmulator(80, 24)
	go func() { _, _ = io.Copy(io.Discard, emu) }() // drain replies
	_, _ = emu.Write(redraw)
	_, _ = emu.Write(stream)
	if got, want := emu.String(), s.Screen(); got != want {
		t.Fatalf("replayed screen differs\n--- replay\n%s\n--- session\n%s", got, want)
	}
	p := emu.CursorPosition()
	if x, y := s.Cursor(); p.X != x || p.Y != y {
		t.Fatalf("cursor replay %v vs %d,%d", p, x, y)
	}
}

func TestConcurrentAccess(t *testing.T) {
	s := fake(t)
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < 50; i++ {
			_ = s.Screen()
			_ = s.Redraw()
			_ = s.Scrollback()
			_, _ = s.Cursor()
			_ = s.Resize(uint16(60+i%20), 24)
		}
	}()
	for i := 0; i < 20; i++ {
		_, _ = s.Write([]byte("msg\r"), i%2 == 0)
		_ = s.Paste("p")
	}
	<-done
	waitScreen(t, s, "GOT: pmsg")
}

func TestScrollback(t *testing.T) {
	s := start(t, session.Spec{Name: "sb", Cmd: []string{"sh", "-c", "i=0; while [ $i -lt 100 ]; do echo row-$i; i=$((i+1)); done; sleep 30"}, Cols: 40, Rows: 10})
	waitScreen(t, s, "row-99")
	sb := s.Scrollback()
	if !strings.Contains(sb, "row-0\n") || !strings.Contains(sb, "row-99") {
		t.Fatalf("scrollback = %q", sb)
	}
	if strings.Contains(s.Screen(), "row-0\n") {
		t.Fatal("row-0 should have scrolled off")
	}
}

func TestStartErrors(t *testing.T) {
	if _, err := session.Start(session.Spec{Name: "x"}); err == nil {
		t.Fatal("empty cmd should fail")
	}
	if _, err := session.Start(session.Spec{Name: "x", Cmd: []string{"/nonexistent/binary"}}); err == nil {
		t.Fatal("bad binary should fail")
	}
}

func itoa(n int) string { return strconv.Itoa(n) }

func TestSetReplyForwarding(t *testing.T) {
	bin := testutil.BuildFakeAgent(t)
	// The delay lets the test toggle forwarding before the query is sent.
	cmd := []string{"sh", "-c", "sleep 0.3; exec " + bin + " -query"}

	off := start(t, session.Spec{Name: "off", Cmd: cmd, Cols: 80, Rows: 24})
	off.SetReplyForwarding(false)
	on := start(t, session.Spec{Name: "on", Cmd: cmd, Cols: 80, Rows: 24})

	waitScreen(t, on, "CPR: 1;1")
	waitScreen(t, off, "CPR: timeout")

}
