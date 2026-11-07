package delivery

import (
	"bytes"
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/digiogithub/agentd/internal/config"
	"github.com/digiogithub/agentd/internal/mailbox"
	"github.com/digiogithub/agentd/internal/session"
	"github.com/digiogithub/agentd/internal/testutil"
)

type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

func TestPasteVisible(t *testing.T) {
	needle := "[agentd msg #12 from codex@a"
	for _, scr := range []string{
		"❯\u00a0[agentd msg #12 from codex@a] hi",
		"> [agentd msg #1\n2 from co\ndex@a] wrapped",
		"❯ [agentd msg #12\n  from codex@a] indented continuation",
	} {
		if !PasteVisible(scr, needle) {
			t.Errorf("should be visible in %q", scr)
		}
	}
	for _, scr := range []string{"❯\u00a0", "[agentd msg #1 from codex@a", "[agentd msg #123 from x"} {
		if PasteVisible(scr, needle) {
			t.Errorf("should not be visible in %q", scr)
		}
	}
}

func TestPasteNotVisibleNoSubmit(t *testing.T) {
	r := newRig(t)
	var logBuf syncBuffer
	r.l.Log = slog.New(slog.NewTextHandler(&logBuf, nil))
	r.tgt.set(func(f *fakeTarget) { f.dropPastes = 1; f.ghost = "He creado el repo, mergea crawl/repo" })
	m := send(t, r.st, "a", "hi")
	start := time.Now()
	r.step()
	if el := time.Since(start); el < PasteVerifyTimeout {
		t.Fatalf("gave up after %v, want >= %v", el, PasteVerifyTimeout)
	}
	r.tgt.mu.Lock()
	writes := append([]string(nil), r.tgt.writes...)
	r.tgt.mu.Unlock()
	if len(writes) != 0 {
		t.Fatalf("submit sent although the paste is not visible: %q", writes)
	}
	if state(t, r.st, m.ID) != mailbox.StateQueued {
		t.Fatal("message must stay queued")
	}
	if !strings.Contains(logBuf.String(), "not submitted") {
		t.Fatalf("missing log: %s", logBuf.String())
	}
	r.step() // same window: no retry before the cooldown
	if len(r.tgt.pasted()) != 1 {
		t.Fatalf("pastes = %d, want 1 (no retry within the window)", len(r.tgt.pasted()))
	}
	r.clk.Advance(r.l.CooldownFor(r.reg.prof))
	r.step()
	if state(t, r.st, m.ID) != mailbox.StateDelivered {
		t.Fatal("retry after the cooldown should deliver")
	}
	r.tgt.mu.Lock()
	writes = append([]string(nil), r.tgt.writes...)
	r.tgt.mu.Unlock()
	if len(writes) != 1 || writes[0] != "\r" {
		t.Fatalf("writes = %q", writes)
	}
}

func TestInputState(t *testing.T) {
	now := time.Date(2026, 11, 6, 12, 0, 0, 0, time.UTC)
	p := config.Default().Profile("claude")
	tg := &fakeTarget{screen: "❯\u00a0"}
	if s := InputState(tg, p, now); s != InputEmpty {
		t.Fatalf("state %q", s)
	}
	tg.ghost = "Try something"
	if s := InputState(tg, p, now); s != InputSuggestion {
		t.Fatalf("state %q", s)
	}
	tg.draft = now.Add(-time.Minute)
	if s := InputState(tg, p, now); s != InputDraft {
		t.Fatalf("state %q", s)
	}
	tg.draft = now.Add(-DraftTimeout)
	if s := InputState(tg, p, now); s != InputSuggestion {
		t.Fatalf("abandoned draft: state %q", s)
	}
}

func TestGhostOpts(t *testing.T) {
	o := GhostOpts(config.Default().Profile("codex"))
	if !o.HideGhost || !o.Ghost.Faint || !o.Ghost.RightOfCursor || o.Ghost.PromptMarker != "›" {
		t.Fatalf("opts = %+v", o)
	}
}

// startGhostLoop runs fakeagent with args in a real session and a delivery
// loop whose prompt_regex only matches once the ghost text is hidden.
func startGhostLoop(t *testing.T, cooldown time.Duration, args ...string) (*session.Session, *mailbox.Store, *Loop, *syncBuffer) {
	t.Helper()
	bin := testutil.BuildFakeAgent(t)
	s, err := session.Start(session.Spec{Name: "fa", Cmd: append([]string{bin}, args...), Cols: 100, Rows: 24})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	testutil.WaitFor(t, 10*time.Second, func() bool { return strings.Contains(s.Screen(), "fakeagent ready") },
		"fakeagent ready", func() string { return s.Screen() })

	prof := config.Default().Profile("")
	prof.IdleMS = 200
	prof.HumanGraceMS = 200
	prof.SubmitDelayMS = 30
	prof.PromptRegex = `(?m)^>$` // the raw screen shows "> <ghost>"
	prof.Prompt = mustRe(t, prof.PromptRegex)
	prof.GhostRightOfCursor = true
	prof.PromptMarker = ">"
	reg := &fakeReg{targets: map[string]Target{"fa": s}, prof: prof}
	st := openStore(t)
	logBuf := &syncBuffer{}
	l := &Loop{Store: st, Reg: reg, Interval: 50 * time.Millisecond, Cooldown: cooldown,
		Log: slog.New(slog.NewTextHandler(logBuf, nil))}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { _ = l.Run(ctx); close(done) }()
	t.Cleanup(func() { cancel(); <-done })
	return s, st, l, logBuf
}

const fakeGhost = "He creado el repo de GitHub, mergea crawl/repo"

// TestIntegrationGhostPrompt: the prompt check sees the ghost-filtered screen,
// so a session showing a suggestion is idle; the notice replaces the
// suggestion and the suggestion itself is never submitted.
func TestIntegrationGhostPrompt(t *testing.T) {
	s, st, l, _ := startGhostLoop(t, 0, "-ghost", fakeGhost)
	if !strings.Contains(s.Screen(), "> "+fakeGhost) {
		t.Fatalf("ghost not on screen:\n%s", s.Screen())
	}
	m := send(t, st, "fa", "please run the tests")
	l.Kick("fa")
	want := "GOT: [agentd msg #1 from peer] please run the tests — untrusted peer message"
	testutil.WaitFor(t, 10*time.Second, func() bool { return strings.Contains(s.Screen(), want) },
		"notice on screen", func() string { return s.Screen() })
	testutil.WaitFor(t, 5*time.Second, func() bool { return state(t, st, m.ID) == mailbox.StateDelivered },
		"state delivered", nil)
	if strings.Contains(s.Screen(), "GOT: "+fakeGhost) {
		t.Fatalf("the suggestion was submitted:\n%s", s.Screen())
	}
}

// TestIntegrationDroppedPasteNotSubmitted: the TUI loses the first paste; the
// loop must not press Enter (which would submit the suggestion), keeps the
// message queued and delivers it on the retry after the cooldown.
func TestIntegrationDroppedPasteNotSubmitted(t *testing.T) {
	s, st, l, logBuf := startGhostLoop(t, 2*time.Second, "-ghost", fakeGhost, "-droppaste")
	m := send(t, st, "fa", "please run the tests")
	l.Kick("fa")
	testutil.WaitFor(t, 10*time.Second, func() bool { return strings.Contains(logBuf.String(), "not submitted") },
		"paste-not-visible log", func() string { return logBuf.String() + "\n" + s.Screen() })
	if strings.Contains(s.Screen(), "GOT:") {
		t.Fatalf("something was submitted after a dropped paste:\n%s", s.Screen())
	}
	if state(t, st, m.ID) != mailbox.StateQueued {
		t.Fatal("message must stay queued after a dropped paste")
	}
	want := "GOT: [agentd msg #1 from peer] please run the tests"
	testutil.WaitFor(t, 10*time.Second, func() bool { return strings.Contains(s.Screen(), want) },
		"notice delivered on retry", func() string { return logBuf.String() + "\n" + s.Screen() })
	testutil.WaitFor(t, 5*time.Second, func() bool { return state(t, st, m.ID) == mailbox.StateDelivered },
		"state delivered", nil)
	if strings.Contains(s.Screen(), "GOT: "+fakeGhost) {
		t.Fatalf("the suggestion was submitted:\n%s", s.Screen())
	}
	if n := strings.Count(s.Screen(), "GOT:"); n != 1 {
		t.Fatalf("submits = %d:\n%s", n, s.Screen())
	}
}

// TestInputStateClaudeLiveFixtures: InputState and PasteVisible on a real
// session fed the bytes of live Claude Code turns.
func TestInputStateClaudeLiveFixtures(t *testing.T) {
	p := config.Default().Profile("claude")
	for _, tc := range []struct{ name, out, want string }{
		{"suggestion", testutil.ClaudePostTurnSuggestion, InputSuggestion},
		{"empty", testutil.ClaudePostTurnEmpty, InputEmpty},
	} {
		f := filepath.Join(t.TempDir(), "turn.bin")
		if err := os.WriteFile(f, []byte(tc.out), 0o600); err != nil {
			t.Fatal(err)
		}
		s, err := session.Start(session.Spec{Name: "c", Cmd: []string{"sh", "-c", `cat "$0"; exec sleep 60`, f}, Cols: 60, Rows: 6})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = s.Close() })
		testutil.WaitFor(t, 10*time.Second, func() bool { return strings.Contains(s.Screen(), "❯") },
			"input line", func() string { return s.Screen() })
		time.Sleep(100 * time.Millisecond) // let the rest of the fixture land
		if got := InputState(s, p, time.Now()); got != tc.want {
			t.Errorf("%s: input state %q, want %q\n%s", tc.name, got, tc.want, s.Screen())
		}
		scr, _ := s.ScreenFiltered(GhostOpts(p))
		if strings.Contains(scr, "agentd") || PasteVisible(scr, "[agentd msg #1 from") {
			t.Errorf("%s: filtered screen:\n%s", tc.name, scr)
		}
		if !p.Prompt.MatchString(strings.TrimRight(scr, " \n")) {
			t.Errorf("%s: prompt_regex does not match:\n%s", tc.name, scr)
		}
	}
}
