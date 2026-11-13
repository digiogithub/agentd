package session_test

import (
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/digiogithub/agentd/internal/screen"
	"github.com/digiogithub/agentd/internal/session"
	"github.com/digiogithub/agentd/internal/testutil"
)

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(testutil.Scale(5 * time.Second))
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timeout waiting for %s", what)
}

func TestSnapshotContent(t *testing.T) {
	s := start(t, session.Spec{
		Name: "snap", Cols: 40, Rows: 6, Cwd: t.TempDir(),
		Cmd: []string{"sh", "-c", `printf '\033[1;31mred\033[0m \033]0;my title\007\033[?2004h\033[?1h\033[2;1H\344\270\255x\033[3;1H'; sleep 5`},
	})
	waitFor(t, "title", func() bool { return s.Snapshot(screen.SnapOpts{}).Title == "my title" })
	snap := s.Snapshot(screen.SnapOpts{})
	if snap.Cols != 40 || snap.Rows != 6 || len(snap.Screen) != 6 {
		t.Fatalf("size %dx%d rows=%d", snap.Cols, snap.Rows, len(snap.Screen))
	}
	c := snap.Screen[0].Cells[0]
	if c.Text != "r" || !c.Attrs.Has(screen.Bold) || c.Fg != "1" {
		t.Fatalf("cell0 = %+v", c)
	}
	if got := snap.Screen[0].Text(); got != "red" {
		t.Fatalf("row0 = %q", got)
	}
	if got := snap.Screen[1].Text(); got != "中x" {
		t.Fatalf("row1 = %q", got)
	}
	if w := snap.Screen[1].Cells[0].Width; w != 2 || len(snap.Screen[1].Cells) != 40 {
		t.Fatalf("wide width=%d len=%d", w, len(snap.Screen[1].Cells))
	}
	if c := snap.Screen[1].Cells[1]; c.Width != 0 || c.Text != "" || snap.Screen[1].Cells[2].Text != "x" {
		t.Fatalf("continuation cell %+v / next %+v: index must equal column", c, snap.Screen[1].Cells[2])
	}
	if snap.Cursor != (screen.Pos{X: 0, Y: 2}) {
		t.Fatalf("cursor %+v", snap.Cursor)
	}
	if !snap.AppCursor || !snap.BracketedPaste || snap.AltScreen {
		t.Fatalf("modes %+v", snap)
	}
	if snap.State != screen.StateRunning || snap.ExitCode != nil || snap.StartedAt.IsZero() || snap.LastOutput.IsZero() {
		t.Fatalf("activity %+v", snap)
	}
	if snap.OutputSeq == 0 || snap.OutputSeq != s.OutputGen() {
		t.Fatalf("seq %d gen %d", snap.OutputSeq, s.OutputGen())
	}
	if snap.Cwd == "" {
		t.Fatal("empty cwd")
	}
}

func TestSnapshotScrollbackAndExit(t *testing.T) {
	s := start(t, session.Spec{
		Name: "sb", Cols: 20, Rows: 4,
		Cmd: []string{"sh", "-c", `for i in 1 2 3 4 5 6 7 8 9 10; do echo line$i; done; exit 3`},
	})
	waitDone(t, s)
	snap := s.Snapshot(screen.SnapOpts{ScrollbackTail: 3})
	if snap.State != screen.StateExited || snap.ExitCode == nil || *snap.ExitCode != 3 {
		t.Fatalf("state %q code %v", snap.State, snap.ExitCode)
	}
	if snap.ScrollbackLen < 7 || len(snap.Scrollback) != 3 {
		t.Fatalf("sblen=%d tail=%d", snap.ScrollbackLen, len(snap.Scrollback))
	}
	last := snap.Scrollback[2].Text()
	if !strings.HasPrefix(last, "line") {
		t.Fatalf("last scrollback = %q", last)
	}
	if n := len(s.Snapshot(screen.SnapOpts{}).Scrollback); n != 0 {
		t.Fatalf("default tail should be none, got %d", n)
	}
	if n := len(s.Snapshot(screen.SnapOpts{ScrollbackTail: -1}).Scrollback); n != snap.ScrollbackLen {
		t.Fatalf("negative tail = default: %d", n)
	}
}

func TestSnapshotNotifications(t *testing.T) {
	s := start(t, session.Spec{
		Name: "notify", Cwd: t.TempDir(),
		Cmd: []string{"sh", "-c", `printf '\007\033]9;hello world\007\033]9;4;1;50\007\033]9;9;/some/dir\007\033]9;12\007\033]777;notify;T;B;with;semi\033\\\033]7;file://host/tmp/x%%20y\007'; sleep 5`},
	})
	waitFor(t, "notifications", func() bool { return s.Snapshot(screen.SnapOpts{}).Notifies >= 2 })
	snap := s.Snapshot(screen.SnapOpts{})
	if snap.Bells != 1 || snap.LastBell.IsZero() {
		t.Fatalf("bells %d %v", snap.Bells, snap.LastBell)
	}
	if snap.Notifies != 2 { // ConEmu/WT sub-commands 9;4, 9;9, 9;12 ignored
		t.Fatalf("notifies %d", snap.Notifies)
	}
	if n := snap.LastNotify; n.Title != "T" || n.Body != "B;with;semi" || n.At.IsZero() {
		t.Fatalf("last notify %+v", n)
	}
	if snap.Cwd != "/tmp/x y" {
		t.Fatalf("cwd %q", snap.Cwd)
	}
}

func TestOutputGenAndChanged(t *testing.T) {
	s := start(t, session.Spec{Name: "gen", Cmd: []string{"cat"}})
	ch := s.Changed()
	g := s.OutputGen()
	if _, err := s.Write([]byte("hello\r"), false); err != nil {
		t.Fatal(err)
	}
	select {
	case <-ch:
	case <-time.After(5 * time.Second):
		t.Fatal("Changed not closed")
	}
	if s.OutputGen() <= g {
		t.Fatalf("gen %d -> %d", g, s.OutputGen())
	}
	if s.Changed() == ch {
		t.Fatal("Changed must return a fresh channel after a signal")
	}
	// Exit also wakes waiters.
	ch = s.Changed()
	_ = s.Close()
	select {
	case <-ch:
	case <-time.After(5 * time.Second):
		t.Fatal("Changed not closed on exit")
	}
}

// TestSnapshotConsistentUnderOutput: each output line is one chunk carrying a
// counter on every row of a full-screen repaint; a torn snapshot would show
// rows from different repaints. Run with -race.
func TestSnapshotConsistentUnderOutput(t *testing.T) {
	const rows = 8
	script := `i=0; while [ $i -lt 400 ]; do i=$((i+1)); out=$(printf '\033[H'"$(printf 'gen%05d\033[K\r\n' ` + strings.TrimSpace(strings.Repeat("$i ", rows)) + `)"); printf '%s' "$out"; done; sleep 5`
	s := start(t, session.Spec{Name: "race", Cols: 30, Rows: rows + 1, Cmd: []string{"sh", "-c", script}})
	var wg sync.WaitGroup
	stop := make(chan struct{})
	var bad string
	var mu sync.Mutex
	for g := 0; g < 4; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			var lastSeq uint64
			for {
				select {
				case <-stop:
					return
				default:
				}
				snap := s.Snapshot(screen.SnapOpts{ScrollbackTail: 10})
				if snap.OutputSeq < lastSeq {
					mu.Lock()
					bad = "output seq went backwards"
					mu.Unlock()
				}
				lastSeq = snap.OutputSeq
				// Chunks may split a repaint, so rows can differ by at most
				// one boundary: the set of generations must be contiguous.
				seen := map[string]bool{}
				for _, r := range snap.Screen[:rows] {
					if txt := r.Text(); txt != "" {
						seen[txt] = true
					}
				}
				if len(seen) > 2 {
					mu.Lock()
					bad = fmt.Sprintf("torn snapshot: %v", seen)
					mu.Unlock()
				}
			}
		}()
	}
	waitFor(t, "output", func() bool { return strings.Contains(s.Screen(), "gen00400") })
	close(stop)
	wg.Wait()
	if bad != "" {
		t.Fatal(bad)
	}
}

func TestSnapshotSanitizesChildStrings(t *testing.T) {
	long := strings.Repeat("a", 300)
	cwd := "/" + strings.Repeat("d", 5000)
	s := start(t, session.Spec{
		Name: "sanitize", Cwd: t.TempDir(),
		Cmd: []string{"sh", "-c", `printf '\033]0;` + long + `\007\033]7;file://h` + cwd + `\007\033]9;ti\011ck\001\007'; sleep 5`},
	})
	waitFor(t, "notify", func() bool { return s.Snapshot(screen.SnapOpts{}).Notifies >= 1 })
	snap := s.Snapshot(screen.SnapOpts{})
	if len([]rune(snap.Title)) != screen.MaxTitleLen {
		t.Fatalf("title len %d", len(snap.Title))
	}
	if len([]rune(snap.Cwd)) != screen.MaxCwdLen {
		t.Fatalf("cwd len %d", len(snap.Cwd))
	}
	if snap.LastNotify.Body != "tick" {
		t.Fatalf("notify body %q", snap.LastNotify.Body)
	}
	_, _, _, n := s.Notifications()
	if n.Body != "tick" {
		t.Fatalf("Notifications body %q", n.Body)
	}
}

func BenchmarkSnapshot(b *testing.B) {
	s, err := session.Start(session.Spec{
		Name: "bench", Cols: 160, Rows: 50,
		Cmd: []string{"sh", "-c", `i=0; while [ $i -lt 200 ]; do i=$((i+1)); printf '\033[1;3%dmline %d \344\270\255\033[0m\r\n' $((i%8)) $i; done; sleep 30`},
	})
	if err != nil {
		b.Fatal(err)
	}
	defer s.Close()
	for s.OutputGen() == 0 {
		time.Sleep(10 * time.Millisecond)
	}
	time.Sleep(300 * time.Millisecond)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = s.Snapshot(screen.SnapOpts{ScrollbackTail: 100})
	}
}

// Snapshots taken without new output share the immutable Screen slice but are
// distinct values with their own activity fields; a different tail or new
// output yields a fresh capture; a resize invalidates the cache even though no
// output arrived.
func TestSnapshotCacheReuseAndInvalidation(t *testing.T) {
	s := start(t, session.Spec{
		Name: "snapcache", Cols: 40, Rows: 6, Cwd: t.TempDir(),
		Cmd: []string{"sh", "-c", `printf 'one\ntwo\nthree\nfour\nfive\nsix\nseven\neight\n'; sleep 5`},
	})
	waitFor(t, "output", func() bool { return strings.Contains(s.Snapshot(screen.SnapOpts{}).Text(), "eight") })
	time.Sleep(100 * time.Millisecond)
	a := s.Snapshot(screen.SnapOpts{})
	b := s.Snapshot(screen.SnapOpts{})
	if a == b {
		t.Fatal("Snapshot must return a fresh value each call")
	}
	if a.OutputSeq != b.OutputSeq || &a.Screen[0] != &b.Screen[0] {
		t.Fatal("snapshots without new output must share the cached Screen")
	}
	if !a.Taken.Before(b.Taken) && !a.Taken.Equal(b.Taken) {
		t.Error("Taken must be per call")
	}
	if len(a.Scrollback) != 0 {
		t.Fatalf("tail 0 copied %d scrollback rows", len(a.Scrollback))
	}
	c := s.Snapshot(screen.SnapOpts{ScrollbackTail: 1})
	if len(c.Scrollback) != 1 || &c.Screen[0] == &a.Screen[0] {
		t.Fatalf("tail 1: %d rows, shared=%v", len(c.Scrollback), &c.Screen[0] == &a.Screen[0])
	}
	if err := s.Resize(30, 5); err != nil {
		t.Fatal(err)
	}
	d := s.Snapshot(screen.SnapOpts{})
	if d.Cols != 30 || d.Rows != 5 || len(d.Screen) != 5 {
		t.Fatalf("resize not reflected: %dx%d rows=%d", d.Cols, d.Rows, len(d.Screen))
	}
}
