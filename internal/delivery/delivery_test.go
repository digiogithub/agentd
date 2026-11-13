package delivery

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/digiogithub/agentd/internal/config"
	"github.com/digiogithub/agentd/internal/mailbox"
	"github.com/digiogithub/agentd/internal/session"
	"github.com/digiogithub/agentd/internal/testutil"
)

func TestMain(m *testing.M) {
	code := m.Run()
	testutil.CleanupFakeAgent()
	os.Exit(code)
}

// clock is a controllable fake clock.
type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) Now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *clock) Advance(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

// fakeTarget is a Target whose timestamps and screen are set by the test.
type fakeTarget struct {
	mu         sync.Mutex
	name       string
	state      string
	lastOut    time.Time
	lastHuman  time.Time
	started    time.Time
	draft      time.Time
	screen     string
	ghost      string // ghost text reported by ScreenFiltered
	input      string // last paste, shown on screen until the next Write
	dropPastes int    // number of upcoming pastes that never show up
	pasteErr   error
	writeErr   error
	pastes     []string
	writes     []string
	inFlight   int
	maxFlight  int
	pasteBlock chan struct{} // if non-nil, Paste waits on it
}

func (f *fakeTarget) Name() string { return f.name }
func (f *fakeTarget) State() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.state
}
func (f *fakeTarget) LastOutput() time.Time {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.lastOut
}
func (f *fakeTarget) LastHumanInput() time.Time {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.lastHuman
}
func (f *fakeTarget) StartedAt() time.Time {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.started
}
func (f *fakeTarget) HumanDraftSince() time.Time {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.draft
}
func (f *fakeTarget) ScreenFiltered(session.ScreenOpts) (string, string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.input != "" {
		return f.screen + "\n" + f.input, f.ghost
	}
	return f.screen, f.ghost
}
func (f *fakeTarget) Paste(text string) error {
	f.mu.Lock()
	f.inFlight++
	f.maxFlight = max(f.maxFlight, f.inFlight)
	block := f.pasteBlock
	f.mu.Unlock()
	if block != nil {
		<-block
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.inFlight--
	if f.pasteErr != nil {
		return f.pasteErr
	}
	f.pastes = append(f.pastes, text)
	if f.dropPastes > 0 {
		f.dropPastes--
	} else {
		f.input += text // a paste appends to what is already in the input line
	}
	return nil
}
func (f *fakeTarget) Write(p []byte, human bool) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.writeErr != nil {
		return 0, f.writeErr
	}
	f.writes = append(f.writes, string(p))
	f.input = ""
	return len(p), nil
}
func (f *fakeTarget) set(fn func(f *fakeTarget)) { f.mu.Lock(); fn(f); f.mu.Unlock() }
func (f *fakeTarget) pasted() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.pastes...)
}

type fakeReg struct {
	mu      sync.Mutex
	targets map[string]Target
	prof    config.Profile
}

func (r *fakeReg) Lookup(name string) (Target, config.Profile, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	t, ok := r.targets[name]
	return t, r.prof, ok
}

func openStore(t *testing.T) *mailbox.Store {
	t.Helper()
	st, err := mailbox.Open(filepath.Join(t.TempDir(), "mailbox.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

func send(t *testing.T, st *mailbox.Store, to, body string) mailbox.Message {
	t.Helper()
	m, err := st.Send(context.Background(), mailbox.Message{Sender: "peer", Recipient: to, Body: body})
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func state(t *testing.T, st *mailbox.Store, id int64) mailbox.State {
	t.Helper()
	m, err := st.Get(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	return m.State
}

type rig struct {
	st  *mailbox.Store
	clk *clock
	tgt *fakeTarget
	reg *fakeReg
	l   *Loop
}

func newRig(t *testing.T) *rig {
	t.Helper()
	clk := &clock{t: time.Date(2026, 11, 6, 12, 0, 0, 0, time.UTC)}
	prof := config.Default().Profile("")
	prof.IdleMS = 1000
	prof.HumanGraceMS = 5000
	prof.SubmitDelayMS = 1
	tgt := &fakeTarget{name: "a", state: "running", screen: "> ",
		lastOut: clk.Now().Add(-time.Hour), lastHuman: time.Time{}, started: clk.Now().Add(-2 * time.Hour)}
	reg := &fakeReg{targets: map[string]Target{"a": tgt}, prof: prof}
	st := openStore(t)
	l := &Loop{Store: st, Reg: reg, Now: clk.Now}
	return &rig{st: st, clk: clk, tgt: tgt, reg: reg, l: l}
}

// step runs one synchronous pass of the loop.
func (r *rig) step() {
	r.l.sweep(context.Background())
	r.l.wg.Wait()
}

func TestIdle(t *testing.T) {
	now := time.Date(2026, 11, 6, 12, 0, 0, 0, time.UTC)
	p := config.Default().Profile("")
	p.IdleMS, p.HumanGraceMS = 1000, 5000
	tg := &fakeTarget{screen: "> ", lastOut: now.Add(-2 * time.Second)}
	if !Idle(tg, p, now) {
		t.Fatal("should be idle")
	}
	tg.lastOut = now.Add(-500 * time.Millisecond)
	if Idle(tg, p, now) {
		t.Fatal("recent output: not idle")
	}
	tg.lastOut = now.Add(-2 * time.Second)
	tg.lastHuman = now.Add(-4 * time.Second)
	if Idle(tg, p, now) {
		t.Fatal("recent human input: not idle")
	}
	tg.lastHuman = now.Add(-5 * time.Second)
	if !Idle(tg, p, now) {
		t.Fatal("human grace elapsed: idle")
	}
	p.PromptRegex = `(?m)^> $`
	p.Prompt = mustRe(t, p.PromptRegex)
	tg.screen = "working..."
	if Idle(tg, p, now) {
		t.Fatal("prompt not shown: not idle")
	}
	tg.screen = "done\n> "
	if !Idle(tg, p, now) {
		t.Fatal("prompt shown: idle")
	}
}

func mustRe(t *testing.T, re string) *regexp.Regexp {
	t.Helper()
	c, err := regexp.Compile(re)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestNotIdleWhileOutputRecent(t *testing.T) {
	r := newRig(t)
	m := send(t, r.st, "a", "hi")
	r.tgt.set(func(f *fakeTarget) { f.lastOut = r.clk.Now().Add(-500 * time.Millisecond) })
	r.step()
	if len(r.tgt.pasted()) != 0 || state(t, r.st, m.ID) != mailbox.StateQueued {
		t.Fatal("must not inject while output is recent")
	}
	r.clk.Advance(600 * time.Millisecond)
	r.step()
	if len(r.tgt.pasted()) != 1 || state(t, r.st, m.ID) != mailbox.StateDelivered {
		t.Fatalf("expected delivery once idle; pastes=%q", r.tgt.pasted())
	}
	if !strings.HasPrefix(r.tgt.pasted()[0], "[agentd msg #1 from peer] hi — ") {
		t.Fatalf("notice = %q", r.tgt.pasted()[0])
	}
	if got := r.tgt.writes; len(got) != 1 || got[0] != "\r" {
		t.Fatalf("submit writes = %q", got)
	}
}

func TestNotIdleWhileHumanTyped(t *testing.T) {
	r := newRig(t)
	m := send(t, r.st, "a", "hi")
	r.tgt.set(func(f *fakeTarget) { f.lastHuman = r.clk.Now().Add(-1 * time.Second) })
	r.step()
	if len(r.tgt.pasted()) != 0 {
		t.Fatal("must not inject while a human is typing")
	}
	r.clk.Advance(4 * time.Second)
	r.step()
	if state(t, r.st, m.ID) != mailbox.StateDelivered {
		t.Fatal("expected delivery after human grace")
	}
}

func TestPromptRegexGating(t *testing.T) {
	r := newRig(t)
	r.reg.prof.PromptRegex = `(?m)^> $`
	r.reg.prof.Prompt = mustRe(t, r.reg.prof.PromptRegex)
	m := send(t, r.st, "a", "hi")
	r.tgt.set(func(f *fakeTarget) { f.screen = "thinking..." })
	r.step()
	if len(r.tgt.pasted()) != 0 {
		t.Fatal("must not inject without prompt")
	}
	r.tgt.set(func(f *fakeTarget) { f.screen = "answer\n> " })
	r.step()
	if state(t, r.st, m.ID) != mailbox.StateDelivered {
		t.Fatal("expected delivery with prompt")
	}
}

func TestSkipMissingExitedAndHuman(t *testing.T) {
	r := newRig(t)
	m1 := send(t, r.st, "ghost", "x")
	m2 := send(t, r.st, "human:alice", "x")
	r.reg.targets["human:alice"] = r.tgt // even if a registry returned one
	r.tgt.set(func(f *fakeTarget) { f.state = "exited" })
	m3 := send(t, r.st, "a", "x")
	r.step()
	for _, id := range []int64{m1.ID, m2.ID, m3.ID} {
		if state(t, r.st, id) != mailbox.StateQueued {
			t.Fatalf("msg %d should stay queued", id)
		}
	}
	if len(r.tgt.pasted()) != 0 {
		t.Fatal("nothing should be injected")
	}
}

func TestOnePerWindow(t *testing.T) {
	r := newRig(t)
	m1 := send(t, r.st, "a", "one")
	m2 := send(t, r.st, "a", "two")
	r.step()
	if state(t, r.st, m1.ID) != mailbox.StateDelivered || state(t, r.st, m2.ID) != mailbox.StateQueued {
		t.Fatal("first pass must deliver exactly the oldest message")
	}

	// Still idle, no reaction, cooldown not elapsed: nothing more.
	r.clk.Advance(5 * time.Second)
	r.step()
	if n := len(r.tgt.pasted()); n != 1 {
		t.Fatalf("pastes = %d, want 1 (same idle window)", n)
	}

	// Agent reacts (output after the injection) but is still busy.
	r.tgt.set(func(f *fakeTarget) { f.lastOut = r.clk.Now() })
	r.step()
	if n := len(r.tgt.pasted()); n != 1 {
		t.Fatal("must wait until idle again")
	}
	// Idle again after the reaction: next message.
	r.clk.Advance(1 * time.Second)
	r.step()
	if state(t, r.st, m2.ID) != mailbox.StateDelivered {
		t.Fatal("second message expected after the agent reacted and went idle")
	}
	if !strings.Contains(r.tgt.pasted()[1], "#2 from peer] two") {
		t.Fatalf("order: %q", r.tgt.pasted())
	}
}

func TestCooldownWithoutReaction(t *testing.T) {
	r := newRig(t)
	send(t, r.st, "a", "one")
	m2 := send(t, r.st, "a", "two")
	r.step()
	cd := r.l.CooldownFor(r.reg.prof)
	if cd != MinCooldown {
		t.Fatalf("cooldown = %v, want %v (max(10s, 4×1s))", cd, MinCooldown)
	}
	r.clk.Advance(cd - time.Millisecond)
	r.step()
	if state(t, r.st, m2.ID) != mailbox.StateQueued {
		t.Fatal("before cooldown: queued")
	}
	r.clk.Advance(time.Millisecond)
	r.step()
	if state(t, r.st, m2.ID) != mailbox.StateDelivered {
		t.Fatal("after cooldown without reaction: delivered")
	}
	r.reg.prof.IdleMS = 5000
	if got := r.l.CooldownFor(r.reg.prof); got != 20*time.Second {
		t.Fatalf("cooldown for idle 5s = %v", got)
	}
}

func TestErrorKeepsQueued(t *testing.T) {
	for _, which := range []string{"paste", "write"} {
		t.Run(which, func(t *testing.T) {
			r := newRig(t)
			m := send(t, r.st, "a", "hi")
			boom := errors.New("boom")
			r.tgt.set(func(f *fakeTarget) {
				if which == "paste" {
					f.pasteErr = boom
				} else {
					f.writeErr = boom
				}
			})
			r.step()
			if state(t, r.st, m.ID) != mailbox.StateQueued {
				t.Fatal("failed injection must leave the message queued")
			}
			r.tgt.set(func(f *fakeTarget) { f.pasteErr, f.writeErr = nil, nil })
			r.step() // same window: no immediate retry
			if state(t, r.st, m.ID) != mailbox.StateQueued {
				t.Fatal("no retry within the window")
			}
			r.clk.Advance(r.l.CooldownFor(r.reg.prof))
			r.step()
			if state(t, r.st, m.ID) != mailbox.StateDelivered {
				t.Fatal("retry after cooldown should deliver")
			}
		})
	}
}

func TestPerRecipientSerialization(t *testing.T) {
	r := newRig(t)
	send(t, r.st, "a", "one")
	block := make(chan struct{})
	r.tgt.set(func(f *fakeTarget) { f.pasteBlock = block })
	ctx := context.Background()
	for range 5 {
		r.l.sweep(ctx) // first starts a goroutine that blocks in Paste
	}
	time.Sleep(20 * time.Millisecond)
	close(block)
	r.l.wg.Wait()
	r.tgt.mu.Lock()
	defer r.tgt.mu.Unlock()
	if r.tgt.maxFlight != 1 || len(r.tgt.pastes) != 1 {
		t.Fatalf("maxFlight=%d pastes=%d", r.tgt.maxFlight, len(r.tgt.pastes))
	}
}

func TestRunKick(t *testing.T) {
	r := newRig(t)
	r.l.Interval = time.Hour // only kicks wake the loop
	r.l.Now = nil            // real clock
	r.tgt.set(func(f *fakeTarget) {
		f.lastOut = time.Now().Add(-time.Hour)
		f.started = time.Now().Add(-2 * time.Hour)
	})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- r.l.Run(ctx) }()
	time.Sleep(20 * time.Millisecond)
	m := send(t, r.st, "a", "hi")
	var wg sync.WaitGroup
	for range 20 {
		wg.Add(1)
		go func() { defer wg.Done(); r.l.Kick("a") }()
	}
	wg.Wait()
	testutil.WaitFor(t, 5*time.Second, func() bool { return state(t, r.st, m.ID) == mailbox.StateDelivered },
		"kick should trigger delivery", nil)
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Run = %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return")
	}
}

func TestKickBeforeRunDoesNotBlock(t *testing.T) {
	l := &Loop{}
	for range 10 {
		l.Kick("x")
	}
}

// TestIntegrationFakeAgent injects into a real session running fakeagent.
func TestIntegrationFakeAgent(t *testing.T) {
	bin := testutil.BuildFakeAgent(t)
	s, err := session.Start(session.Spec{Name: "fa", Cmd: []string{bin}, Cols: 200, Rows: 24})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	testutil.WaitFor(t, 10*time.Second, func() bool { return strings.Contains(s.Screen(), "fakeagent ready") },
		"fakeagent ready", func() string { return s.Screen() })

	prof := config.Default().Profile("")
	prof.IdleMS = 200
	prof.HumanGraceMS = 200
	prof.PromptRegex = `(?m)^>\s*$` // Screen trims trailing spaces
	prof.Prompt = mustRe(t, prof.PromptRegex)
	reg := &fakeReg{targets: map[string]Target{"fa": s}, prof: prof}
	st := openStore(t)
	l := &Loop{Store: st, Reg: reg, Interval: 50 * time.Millisecond}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { _ = l.Run(ctx); close(done) }()
	t.Cleanup(func() { cancel(); <-done })

	m := send(t, st, "fa", "please run the tests")
	l.Kick("fa")
	want := "GOT: [agentd msg #1 from peer] please run the tests — untrusted peer message"
	testutil.WaitFor(t, 10*time.Second, func() bool { return strings.Contains(s.Screen(), want) },
		"notice on screen", func() string { return s.Screen() })
	testutil.WaitFor(t, 5*time.Second, func() bool { return state(t, st, m.ID) == mailbox.StateDelivered },
		"state delivered", nil)
	if n := strings.Count(s.Screen(), "GOT: [agentd msg"); n != 1 {
		t.Fatalf("notices on screen = %d:\n%s", n, s.Screen())
	}
}

var _ Target = (*session.Session)(nil)
