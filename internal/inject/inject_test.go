package inject

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/digiogithub/agentd/internal/config"
	"github.com/digiogithub/agentd/internal/session"
)

type fake struct {
	mu       sync.Mutex
	human    time.Time
	screen   string
	showText bool // paste appears on screen
	writes   []string
	onPaste  func()
}

func (f *fake) State() string              { return session.StateRunning }
func (f *fake) LastHumanInput() time.Time  { f.mu.Lock(); defer f.mu.Unlock(); return f.human }
func (f *fake) HumanDraftSince() time.Time { return time.Time{} }
func (f *fake) ScreenFiltered(session.ScreenOpts) (string, string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.screen, ""
}
func (f *fake) Paste(text string) error {
	if f.onPaste != nil {
		f.onPaste()
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.writes = append(f.writes, "paste:"+text)
	if f.showText {
		f.screen += "> " + text
	}
	return nil
}
func (f *fake) Write(p []byte, human bool) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.writes = append(f.writes, "write:"+string(p))
	return len(p), nil
}

func prof() config.Profile {
	p := config.Profile{}
	p.SubmitDelayMS = 10
	return p
}

func TestInputSubmits(t *testing.T) {
	f := &fake{showText: true}
	res, err := Input(context.Background(), f, prof(), Request{Text: "hello  world", Submit: true})
	if err != nil || !res.Pasted || !res.Submitted || res.HumanRaced {
		t.Fatalf("res=%+v err=%v", res, err)
	}
	if len(f.writes) != 2 || f.writes[1] != "write:\r" {
		t.Fatalf("writes=%q", f.writes)
	}
}

func TestInputGuardAbort(t *testing.T) {
	f := &fake{showText: true}
	want := errors.New("busy")
	res, err := Input(context.Background(), f, prof(), Request{Text: "x", Submit: true, Guard: func() error { return want }})
	if !errors.Is(err, want) || res.Pasted || len(f.writes) != 0 {
		t.Fatalf("res=%+v err=%v writes=%q", res, err, f.writes)
	}
}

func TestInputNotVisibleNoSubmit(t *testing.T) {
	f := &fake{}
	start := time.Now()
	res, err := Input(context.Background(), f, prof(), Request{Text: "ghost", Submit: true})
	if !errors.Is(err, ErrPasteNotVisible) || res.Submitted || !res.Pasted {
		t.Fatalf("res=%+v err=%v", res, err)
	}
	if time.Since(start) < PasteVerifyTimeout {
		t.Fatal("gave up too early")
	}
	for _, w := range f.writes {
		if strings.HasPrefix(w, "write:") {
			t.Fatalf("submit written: %q", f.writes)
		}
	}
}

func TestInputHumanRacedSubmitsAnyway(t *testing.T) {
	f := &fake{showText: true}
	p := prof()
	p.SubmitDelayMS = 100
	go func() {
		time.Sleep(40 * time.Millisecond)
		f.mu.Lock()
		f.human = time.Now()
		f.mu.Unlock()
	}()
	res, err := Input(context.Background(), f, p, Request{Text: "hi", Submit: true})
	if err != nil || !res.HumanRaced || !res.Submitted {
		t.Fatalf("res=%+v err=%v", res, err)
	}
}

func TestInputNoSubmit(t *testing.T) {
	f := &fake{showText: true}
	res, err := Input(context.Background(), f, prof(), Request{Text: "hi"})
	if err != nil || res.Submitted || len(f.writes) != 1 {
		t.Fatalf("res=%+v err=%v writes=%q", res, err, f.writes)
	}
}

func TestInputLockSerializes(t *testing.T) {
	f := &fake{showText: true}
	var mu sync.Mutex
	inFlight, maxFlight := 0, 0
	f.onPaste = func() {
		mu.Lock()
		inFlight++
		maxFlight = max(maxFlight, inFlight)
		mu.Unlock()
	}
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			g := func() error { return nil }
			_, _ = Input(context.Background(), f, prof(), Request{Text: "m", Submit: true, Guard: g})
			mu.Lock()
			inFlight--
			mu.Unlock()
		}()
	}
	wg.Wait()
	if maxFlight != 1 {
		t.Fatalf("interleaved inputs: max in flight %d", maxFlight)
	}
}

func TestLockContextCancel(t *testing.T) {
	f := &fake{}
	un, err := Lock(context.Background(), f)
	if err != nil {
		t.Fatal(err)
	}
	defer un()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	if _, err := Input(ctx, f, prof(), Request{Text: "x"}); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err=%v", err)
	}
}

func TestDefaultNeedleAndVisible(t *testing.T) {
	if n := DefaultNeedle(strings.Repeat("a", 60) + "\nb"); len(n) != 48 {
		t.Fatalf("needle %d", len(n))
	}
	if !PasteVisible("[agentd msg #1\n   from x]", "[agentd msg #1 from") {
		t.Fatal("whitespace-insensitive match failed")
	}
}

// valTarget is a non-comparable value type (it has a slice field).
type valTarget struct {
	log []string
	*fake
}

func TestLockKeyNoPanicOnNonComparable(t *testing.T) {
	v := valTarget{fake: &fake{showText: true}}
	unlock, err := Lock(context.Background(), v) // must not panic
	if err != nil {
		t.Fatal(err)
	}
	unlock()
	if _, err := Input(context.Background(), v, prof(), Request{Text: "hello", Submit: true}); err != nil {
		t.Fatal(err)
	}
	// Pointer identity: different pointers do not share a lock, the same does.
	a, b := &fake{}, &fake{}
	if lockFor(a) == lockFor(b) || lockFor(a) != lockFor(a) {
		t.Fatal("pointer identity lock keys")
	}
}

// draftFake is a fake that records draft marks like *session.Session does.
type draftFake struct {
	fake
	marked, cleared int
}

func (f *draftFake) MarkDraft()  { f.mu.Lock(); f.marked++; f.mu.Unlock() }
func (f *draftFake) ClearDraft() { f.mu.Lock(); f.cleared++; f.mu.Unlock() }

// A needle that was already on the screen before the paste must not verify a
// paste the harness dropped (the old check matched any occurrence).
func TestPasteVerificationTiedToPaste(t *testing.T) {
	f := &fake{screen: "earlier output: hello world\n", showText: false}
	_, err := Input(context.Background(), f, prof(), Request{Text: "hello world", Submit: true})
	if !errors.Is(err, ErrPasteNotVisible) {
		t.Fatalf("stale occurrence verified a dropped paste: %v", err)
	}
	for _, w := range f.writes {
		if strings.HasPrefix(w, "write:") {
			t.Fatalf("submitted: %v", f.writes)
		}
	}
	// The same text visible before AND a paste that adds another occurrence
	// is fine (the count grew).
	f = &fake{screen: "earlier output: hello world\n", showText: true}
	if res, err := Input(context.Background(), f, prof(), Request{Text: "hello world", Submit: true}); err != nil || !res.Submitted {
		t.Fatalf("grown count: %+v %v", res, err)
	}
	// The input region confirms a paste whose echo does not change the count
	// (needle only in the region).
	region := ""
	g := &fake{screen: "x hello world x", showText: false, onPaste: func() { region = "hello world" }}
	if res, err := Input(context.Background(), g, prof(), Request{Text: "hello world", Submit: true,
		InputRegion: func() string { return region }}); err != nil || !res.Submitted {
		t.Fatalf("input region: %+v %v", res, err)
	}
	// ...but not when the region already held it.
	region = "hello world"
	h := &fake{screen: "x hello world x", showText: false}
	if _, err := Input(context.Background(), h, prof(), Request{Text: "hello world", Submit: true,
		InputRegion: func() string { return region }}); !errors.Is(err, ErrPasteNotVisible) {
		t.Fatalf("region that already held the text verified: %v", err)
	}
}

func TestEmptyNeedleRefused(t *testing.T) {
	f := &fake{showText: true}
	for _, text := range []string{"", "\n  \n\t\n", "   "} {
		if _, err := Input(context.Background(), f, prof(), Request{Text: text, Submit: true}); !errors.Is(err, ErrEmptyNeedle) {
			t.Errorf("text %q: err = %v", text, err)
		}
	}
	if len(f.writes) != 0 {
		t.Fatalf("wrote despite an empty needle: %v", f.writes)
	}
	// First NON-BLANK line is the needle.
	if got := DefaultNeedle("\n\n  \nreal line\nsecond"); got != "real line" {
		t.Errorf("DefaultNeedle = %q", got)
	}
}

func TestBeforeSubmitAbortLeavesDraft(t *testing.T) {
	f := &draftFake{fake: fake{showText: true}}
	boom := errors.New("menu appeared")
	res, err := Input(context.Background(), f, prof(), Request{Text: "hello", Submit: true,
		BeforeSubmit: func() error { return boom }})
	if !errors.Is(err, boom) || !res.Pasted || res.Submitted {
		t.Fatalf("res=%+v err=%v", res, err)
	}
	for _, w := range f.writes {
		if strings.HasPrefix(w, "write:") {
			t.Fatalf("submit written after BeforeSubmit failed: %v", f.writes)
		}
	}
	if f.marked != 1 || f.cleared != 0 {
		t.Errorf("draft marked=%d cleared=%d, want 1/0", f.marked, f.cleared)
	}
}

func TestDraftMarkedWhenNotSubmitted(t *testing.T) {
	f := &draftFake{fake: fake{showText: true}}
	if res, err := Input(context.Background(), f, prof(), Request{Text: "hello"}); err != nil || res.Submitted {
		t.Fatalf("%+v %v", res, err)
	}
	if f.marked != 1 {
		t.Errorf("marked = %d", f.marked)
	}
	g := &draftFake{fake: fake{showText: true}}
	if res, err := Input(context.Background(), g, prof(), Request{Text: "hello", Submit: true}); err != nil || !res.Submitted {
		t.Fatalf("%+v %v", res, err)
	}
	if g.marked != 0 || g.cleared != 1 {
		t.Errorf("submitted: marked=%d cleared=%d", g.marked, g.cleared)
	}
}
