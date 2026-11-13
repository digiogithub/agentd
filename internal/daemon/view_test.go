package daemon_test

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/digiogithub/agentd/internal/client"
	"github.com/digiogithub/agentd/internal/config"
	"github.com/digiogithub/agentd/internal/daemon"
	"github.com/digiogithub/agentd/internal/mailbox"
	"github.com/digiogithub/agentd/internal/proto"
	"github.com/digiogithub/agentd/internal/testutil"
)

const viewConfig = `
[profiles.fake]
idle_ms = 300
human_grace_ms = 0
prompt_marker = "❯"
ghost_right_of_cursor = true

[profiles.redraw]
idle_ms = 300
human_grace_ms = 0
prompt_marker = "❯"
spinner_chars = "✻✶✳✢✽"
busy_regex = "esc to interrupt|for shortcuts"
ghost_right_of_cursor = true

[profiles.fakebox]
idle_ms = 300
human_grace_ms = 0
prompt_marker = "❯"
prompt_regex = '(?m)^❯([ \x{a0}]|$)'
ghost_right_of_cursor = true
`

// startViewServer runs an in-process daemon (no delivery loop) with the given
// extra config.toml text appended to viewConfig.
func startViewServer(t *testing.T, extra string) *env {
	t.Helper()
	dir, err := os.MkdirTemp("", "agentd-v")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	sock := filepath.Join(dir, "agentd.sock")
	cfgPath := filepath.Join(dir, "config.toml")
	if err := os.WriteFile(cfgPath, []byte(viewConfig+extra), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(config.EnvSocket, sock)
	t.Setenv(config.EnvStateDir, filepath.Join(dir, "state"))
	t.Setenv(config.EnvConfig, cfgPath)
	clearIdentityEnv(t)
	if err := config.EnsureDirs(); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.LoadDefault()
	if err != nil {
		t.Fatal(err)
	}
	dbPath, _ := config.MailboxPath()
	store, err := mailbox.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	srv := daemon.New(cfg, nil, store, nil)
	if err := srv.Listen(); err != nil {
		t.Fatal(err)
	}
	e := &env{srv: srv, sock: sock, errc: make(chan error, 1)}
	go func() { e.errc <- srv.Serve(context.Background()) }()
	t.Cleanup(srv.Shutdown)
	return e
}

func runView(t *testing.T, c *client.Client, name, profile string, cmd ...string) {
	t.Helper()
	if _, err := c.Run(proto.RunArgs{Name: name, Cmd: cmd, Profile: profile, Cwd: t.TempDir()}); err != nil {
		t.Fatal(err)
	}
}

func viewOf(t *testing.T, c *client.Client, a proto.ViewArgs) proto.View {
	t.Helper()
	v, err := c.View(a)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func lineTexts(v proto.View) []string {
	out := make([]string, len(v.Lines))
	for i, l := range v.Lines {
		out[i] = l.Text
	}
	return out
}

func TestViewStatusTransitionsAttentionAndRevisions(t *testing.T) {
	e := startViewServer(t, "")
	c := e.dial(t)
	runView(t, c, "sh1", "fake", "sh", "-c",
		"echo ready; sleep 3.6; for i in 1 2 3 4 5 6; do echo step $i; sleep 0.15; done; sleep 60")

	seen := map[string]bool{}
	var v proto.View
	testutil.WaitFor(t, 15*time.Second, func() bool {
		v = viewOf(t, c, proto.ViewArgs{Name: "sh1"})
		seen[v.Status] = true
		return v.Status == "idle" && v.Attention.Set && seen["working"]
	}, "sh1 should go working then idle with attention", func() string { return fmt.Sprintf("seen=%v view=%+v", seen, v) })
	if !seen["starting"] {
		t.Errorf("never saw starting: %v", seen)
	}
	if v.Attention.Reason != "turn_done" || v.Attention.Since.IsZero() {
		t.Errorf("attention = %+v", v.Attention)
	}
	if v.Epoch != e.srv.Epoch() || v.Revision == 0 || v.Name != "sh1" {
		t.Errorf("view header = %q %q rev %d", v.Name, v.Epoch, v.Revision)
	}
	texts := lineTexts(v)
	if !slices.Contains(texts, "ready") || !slices.Contains(texts, "step 6") {
		t.Errorf("lines = %q", texts)
	}
	for _, l := range v.Lines {
		if l.Role != "transcript" {
			t.Errorf("line %+v role", l)
		}
	}

	// ls summary reflects the same view.
	info := findSession(t, c, "sh1")
	if info.Status != "idle" || !info.Attention || info.Revision == 0 || info.Preview != "step 6" {
		t.Errorf("ls summary = %+v", info)
	}

	// first_line paging: pages concatenate to the full list.
	full := viewOf(t, c, proto.ViewArgs{Name: "sh1"})
	if full.FirstLine != 0 || len(full.Lines) < 7 {
		t.Fatalf("full view first=%d lines=%q", full.FirstLine, lineTexts(full))
	}
	tail := viewOf(t, c, proto.ViewArgs{Name: "sh1", LinesLimit: 2})
	if len(tail.Lines) != 2 || tail.FirstLine != len(full.Lines)-2 || !slices.Equal(lineTexts(tail), lineTexts(full)[len(full.Lines)-2:]) {
		t.Errorf("tail = first %d %q", tail.FirstLine, lineTexts(tail))
	}
	two := 2
	page := viewOf(t, c, proto.ViewArgs{Name: "sh1", FromLine: &two, LinesLimit: 3})
	if page.FirstLine != 2 || !slices.Equal(lineTexts(page), lineTexts(full)[2:5]) {
		t.Errorf("page = first %d %q", page.FirstLine, lineTexts(page))
	}

	// not_modified, and the revision is stable with no output.
	rev := full.Revision
	nm := viewOf(t, c, proto.ViewArgs{Name: "sh1", SinceRevision: rev, Epoch: e.srv.Epoch()})
	if !nm.NotModified || nm.Revision != rev || len(nm.Lines) != 0 {
		t.Errorf("not modified = %+v", nm)
	}
	if other := viewOf(t, c, proto.ViewArgs{Name: "sh1", SinceRevision: rev, Epoch: "other"}); other.NotModified {
		t.Error("a different epoch must not be not_modified")
	}
	time.Sleep(1500 * time.Millisecond)
	if v2 := viewOf(t, c, proto.ViewArgs{Name: "sh1"}); v2.Revision != rev {
		t.Errorf("revision moved without output: %d -> %d", rev, v2.Revision)
	}

	// ack clears attention and bumps the revision once.
	r2, err := c.ViewAck("sh1")
	if err != nil || r2 <= rev {
		t.Fatalf("ack = %d, %v (was %d)", r2, err, rev)
	}
	v3 := viewOf(t, c, proto.ViewArgs{Name: "sh1"})
	if v3.Attention.Set || v3.Revision != r2 {
		t.Errorf("after ack: %+v rev %d want %d", v3.Attention, v3.Revision, r2)
	}
	if r3, _ := c.ViewAck("sh1"); r3 != r2 {
		t.Errorf("second ack changed the revision: %d -> %d", r2, r3)
	}
}

func TestViewMenuOptions(t *testing.T) {
	e := startViewServer(t, "")
	c := e.dial(t)
	runView(t, c, "menu", "fake", testutil.BuildFakeAgent(t), "-menu", "Alpha|Beta|Gamma")
	var v proto.View
	testutil.WaitFor(t, 15*time.Second, func() bool {
		v = viewOf(t, c, proto.ViewArgs{Name: "menu"})
		return v.Status == "waiting"
	}, "menu session should be waiting", func() string { return fmt.Sprintf("%+v", v) })
	if len(v.Options) != 3 || v.Options[0].Label != "Alpha" || !v.Options[0].Selected || v.Options[2].Label != "Gamma" {
		t.Fatalf("options = %+v", v.Options)
	}
	if !v.Attention.Set || v.Attention.Reason != "waiting" {
		t.Errorf("attention = %+v", v.Attention)
	}
	for i, o := range v.Options {
		if o.ID == "" || o.Index != i+1 || o.Kind == "" || o.Confidence <= 0 {
			t.Errorf("option %d = %+v", i, o)
		}
	}
	// Options never expose key sequences on the wire.
	var raw struct {
		Options []map[string]any `json:"options"`
	}
	var rawAll json.RawMessage
	if err := c.Call(proto.OpView, proto.ViewArgs{Name: "menu"}, &rawAll); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(rawAll, &raw); err != nil {
		t.Fatal(err)
	}
	allowed := map[string]bool{"id": true, "label": true, "index": true, "kind": true, "selected": true, "checked": true, "confidence": true}
	for _, o := range raw.Options {
		for k := range o {
			if !allowed[k] {
				t.Errorf("option exposes field %q", k)
			}
		}
	}
	if strings.Contains(string(rawAll), `"keys"`) || strings.Contains(string(rawAll), `"submit"`) {
		t.Errorf("wire JSON leaks keys: %s", rawAll)
	}
}

func TestViewInputGhost(t *testing.T) {
	e := startViewServer(t, "")
	c := e.dial(t)
	runView(t, c, "box", "fakebox", testutil.BuildFakeAgent(t), "-box", "-ghost", "try this")
	var v proto.View
	testutil.WaitFor(t, 15*time.Second, func() bool {
		v = viewOf(t, c, proto.ViewArgs{Name: "box"})
		return v.Status == "idle"
	}, "box session should be idle", func() string { return fmt.Sprintf("%+v", v) })
	if v.Input.Ghost != "try this" || v.Input.State != "suggestion" || v.Input.Text != "" {
		t.Errorf("input = %+v", v.Input)
	}
	if info := findSession(t, c, "box"); info.Status != "idle" {
		t.Errorf("ls status = %q", info.Status)
	}
	// A human draft is reported as input state draft.
	if err := c.Type(proto.TypeArgs{Name: "box", Text: "hello"}); err != nil {
		t.Fatal(err)
	}
	testutil.WaitFor(t, 5*time.Second, func() bool {
		v = viewOf(t, c, proto.ViewArgs{Name: "box"})
		return v.Input.Text == "hello"
	}, "typed text should show in the input", func() string { return fmt.Sprintf("%+v", v) })
}

func TestViewRedrawHistoryAndExit(t *testing.T) {
	e := startViewServer(t, "")
	c := e.dial(t)
	runView(t, c, "rd", "redraw", testutil.BuildFakeAgent(t), "-alt", "-redraw", "-redrawturns", "10", "-redrawlines", "4", "-redrawdelay", "4ms")
	var v proto.View
	testutil.WaitFor(t, 30*time.Second, func() bool {
		v = viewOf(t, c, proto.ViewArgs{Name: "rd"})
		return v.Status == "exited"
	}, "redraw session should exit", func() string { return fmt.Sprintf("%+v", v) })
	if v.ExitCode == nil || *v.ExitCode != 0 {
		t.Errorf("exit code = %v", v.ExitCode)
	}
	// 10 turns x 6 lines far exceeds the viewport: the history keeps them.
	if len(v.Lines) < 40 {
		t.Fatalf("history has %d lines: %q", len(v.Lines), lineTexts(v))
	}
	texts := lineTexts(v)
	// The script repaints far faster than the settle policy samples (frames
	// caught mid-repaint can leak status rows or leave gaps: a screen package
	// limitation), so only the coarse shape is asserted: the transcript
	// outlives the viewport and covers the whole script.
	late := false
	for _, tx := range texts {
		if strings.HasPrefix(tx, "> question 0") && tx > "> question 05" || strings.HasPrefix(tx, "> question 10") {
			late = true
		}
	}
	if !late {
		t.Errorf("late turns missing: %q", texts)
	}
	if !slices.Contains(texts, "> question 01") {
		t.Errorf("first question missing: %q", texts)
	}
	// Paging with first_line.
	n := len(v.Lines)
	from := n - 10
	page := viewOf(t, c, proto.ViewArgs{Name: "rd", FromLine: &from, LinesLimit: 5})
	if page.FirstLine != from || !slices.Equal(lineTexts(page), texts[from:from+5]) {
		t.Errorf("page first %d %q", page.FirstLine, lineTexts(page))
	}
	// The final view is stable: not_modified at its revision.
	if nm := viewOf(t, c, proto.ViewArgs{Name: "rd", SinceRevision: v.Revision, Epoch: v.Epoch}); !nm.NotModified {
		t.Errorf("exited view moved: %+v", nm)
	}
}

func TestViewPermissions(t *testing.T) {
	e := startViewServer(t, "")
	c := e.dial(t)
	runView(t, c, "a1", "fake", "sh", "-c", "echo one; sleep 60")
	runView(t, c, "a2", "fake", "sh", "-c", "echo two; sleep 60")
	a1 := dialAs(t, e, "a1")
	if _, err := a1.View(proto.ViewArgs{Name: "a1"}); err != nil {
		t.Errorf("own view: %v", err)
	}
	_, err := a1.View(proto.ViewArgs{Name: "a2"})
	if proto.CodeOf(err) != proto.CodeForbidden {
		t.Errorf("cross view err = %v (code %q)", err, proto.CodeOf(err))
	}
	if _, err := a1.ViewAck("a1"); proto.CodeOf(err) != proto.CodeForbidden {
		t.Errorf("agent ack err = %v", err)
	}
	if _, err := c.View(proto.ViewArgs{Name: "nope"}); proto.CodeOf(err) != proto.CodeNotFound {
		t.Errorf("unknown session err = %v", err)
	}
	if _, err := c.View(proto.ViewArgs{Name: "a1", LinesLimit: 100000}); proto.CodeOf(err) != proto.CodeInvalid {
		t.Errorf("limit err = %v", err)
	}

	e2 := startViewServer(t, "\n[security]\nagent_view = true\n")
	c2 := e2.dial(t)
	runView(t, c2, "b1", "fake", "sh", "-c", "sleep 60")
	runView(t, c2, "b2", "fake", "sh", "-c", "sleep 60")
	if _, err := dialAs(t, e2, "b1").View(proto.ViewArgs{Name: "b2"}); err != nil {
		t.Errorf("agent_view=true cross view: %v", err)
	}
}

func TestViewWatcherLifecycle(t *testing.T) {
	e := startViewServer(t, "")
	c := e.dial(t)
	runView(t, c, "quick", "fake", "sh", "-c", "echo hi; exit 3")
	runView(t, c, "long", "fake", "sh", "-c", "sleep 60")
	runView(t, c, "killed", "fake", "sh", "-c", "sleep 60")
	quick, long, killed := e.srv.ViewDoneForTest("quick"), e.srv.ViewDoneForTest("long"), e.srv.ViewDoneForTest("killed")
	if quick == nil || long == nil || killed == nil {
		t.Fatal("no watcher")
	}
	select {
	case <-quick:
	case <-time.After(10 * time.Second):
		t.Fatal("watcher of an exited session did not stop")
	}
	select {
	case <-long:
		t.Fatal("watcher of a running session stopped")
	default:
	}
	if err := c.Kill("killed", "KILL"); err != nil {
		t.Fatal(err)
	}
	select {
	case <-killed:
	case <-time.After(10 * time.Second):
		t.Fatal("watcher did not stop after kill")
	}
	if err := c.Rm("killed"); err != nil {
		t.Fatal(err)
	}
	if _, err := c.View(proto.ViewArgs{Name: "killed"}); proto.CodeOf(err) != proto.CodeNotFound {
		t.Errorf("view after rm = %v", err)
	}
	v := viewOf(t, c, proto.ViewArgs{Name: "quick"})
	if v.Status != "exited" || v.ExitCode == nil || *v.ExitCode != 3 || !slices.Contains(lineTexts(v), "hi") {
		t.Errorf("quick view = %+v", v)
	}
	shutdownWithin(t, e.srv, 10*time.Second)
	select {
	case <-long:
	default:
		t.Fatal("watcher still running after daemon shutdown")
	}
}

func TestViewRaceConcurrentOutput(t *testing.T) {
	e := startViewServer(t, "")
	c0 := e.dial(t)
	runView(t, c0, "busy", "fake", "sh", "-c", "i=0; while [ $i -lt 400 ]; do echo line $i; i=$((i+1)); sleep 0.01; done; sleep 60")
	var wg sync.WaitGroup
	for g := 0; g < 4; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			c, err := client.Dial(e.sock)
			if err != nil {
				t.Error(err)
				return
			}
			defer c.Close()
			var rev uint64
			for i := 0; i < 80; i++ {
				v, err := c.View(proto.ViewArgs{Name: "busy", SinceRevision: rev, LinesLimit: 50})
				if err != nil {
					t.Error(err)
					return
				}
				if !v.NotModified {
					rev = v.Revision
				}
				if _, err := c.Ls(); err != nil {
					t.Error(err)
					return
				}
				time.Sleep(15 * time.Millisecond)
			}
		}()
	}
	wg.Wait()
	testutil.WaitFor(t, 10*time.Second, func() bool {
		v := viewOf(t, c0, proto.ViewArgs{Name: "busy", LinesLimit: 3})
		return slices.Contains(lineTexts(v), "line 399")
	}, "last line should reach the view", nil)
	s := findSession(t, c0, "busy")
	if s.Preview != "line 399" || s.Revision == 0 {
		t.Errorf("ls = %+v", s)
	}
}

// assertCleanRedrawTranscript checks the history of a finished -redraw
// session: the script's lines (user + reply lines, in order), no gap marker,
// no duplicated or transient (status/footer) rows.
//
// With lossy (children without synchronized updates) a sample may land on a
// half drawn frame, which History can only record as a gap, and lines may
// scroll out between two samples when repaints outpace sampling (a loaded
// machine; a torn frame can also be stitched in as junk or the final frames
// be missed). Then the transcript is only checked for what must always hold:
// no transient row, script lines in script order (duplicates tolerated) and at
// least half of the script captured. The exact transcript is required with
// synchronized updates.
func assertCleanRedrawTranscript(t *testing.T, v proto.View, turns, replyLines int, lossy bool) {
	t.Helper()
	var want, got []string
	for turn := 1; turn <= turns; turn++ {
		want = append(want, fmt.Sprintf("> question %02d", turn))
		for l := 1; l <= replyLines; l++ {
			want = append(want, fmt.Sprintf("  reply %02d.%d:", turn, l))
		}
	}
	seen := map[string]bool{}
	hasGap := false
	for _, l := range v.Lines {
		tx := strings.TrimSpace(l.Text)
		if l.Role == "gap" {
			if lossy {
				hasGap = true
				continue
			}
			t.Errorf("gap marker in history: %q", lineTexts(v))
			return
		}
		if strings.Contains(tx, "Thinking") || strings.Contains(tx, "for shortcuts") || strings.HasPrefix(tx, "✻") {
			t.Errorf("transient row in history: %q", l.Text)
		}
		if tx == "" {
			continue
		}
		if seen[tx] && !hasGap {
			t.Errorf("duplicate line %q in %q", tx, lineTexts(v))
			return
		}
		seen[tx] = true
		got = append(got, l.Text)
	}
	if lossy { // script lines in order, most of the script captured
		idx := func(g string) int {
			for i, w := range want {
				if strings.HasPrefix(g, w) {
					return i
				}
			}
			return -1
		}
		prev, hit := -1, map[int]bool{}
		for _, g := range got {
			if i := idx(g); i >= 0 {
				if i < prev {
					t.Fatalf("script line %q out of order\n got %q", g, got)
				}
				prev, hit[i] = i, true
			}
		}
		if len(hit) < len(want)/2 {
			t.Fatalf("only %d/%d script lines captured\n got %q", len(hit), len(want), got)
		}
		return
	}
	if len(got) != len(want) {
		t.Fatalf("history has %d content lines, want %d\n got  %q", len(got), len(want), got)
	}
	for i := range want {
		if !strings.HasPrefix(got[i], want[i]) {
			t.Fatalf("line %d = %q, want prefix %q (order/gap)\n got %q", i, got[i], want[i], got)
		}
	}
}

// TestViewRedrawCleanHistory (AGED-US-0036): a full-screen repainting child
// yields a duplicate-free, gap-free, ordered transcript, with synchronized
// updates (the watcher never samples inside one) and without (forced samples
// skip transient rows and defer unaligned frames).
func TestViewRedrawCleanHistory(t *testing.T) {
	for _, tc := range []struct {
		name, delay string
		args        []string
	}{
		{"sync2026", "2ms", nil},
		// Slower repaints without 2026: a forced sample still sees
		// mid-repaint frames, and only so many rows fit the viewport
		// between two samples.
		{"nosync", "4ms", []string{"-redrawnosync"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := startViewServer(t, "")
			c := e.dial(t)
			// Pace the child by the machine load so the watcher keeps
			// sampling at the same lines-per-sample rate as when idle.
			d, _ := time.ParseDuration(tc.delay)
			t.Logf("load factor %.2f, redraw delay %v", testutil.Slow(), testutil.Scale(d))
			args := append([]string{"-alt", "-redraw", "-redrawturns", "8", "-redrawlines", "4", "-redrawdelay", testutil.Scale(d).String()}, tc.args...)
			runView(t, c, "rd", "redraw", append([]string{testutil.BuildFakeAgent(t)}, args...)...)
			var v proto.View
			testutil.WaitFor(t, testutil.Scale(60*time.Second), func() bool {
				v = viewOf(t, c, proto.ViewArgs{Name: "rd"})
				return v.Status == "exited"
			}, "redraw session should exit", func() string { return fmt.Sprintf("%+v", v) })
			assertCleanRedrawTranscript(t, v, 8, 4, len(tc.args) > 0)
		})
	}
}
