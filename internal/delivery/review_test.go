package delivery

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/digiogithub/agentd/internal/config"
	"github.com/digiogithub/agentd/internal/mailbox"
)

// Regression (review R7): a target that never produced output, or that
// started less than max(idle, MinBoot) ago, is not idle.
func TestIdleRequiresOutputAndBootTime(t *testing.T) {
	now := time.Date(2026, 11, 6, 12, 0, 0, 0, time.UTC)
	p := config.Default().Profile("")
	p.IdleMS, p.HumanGraceMS = 1000, 0
	tg := &fakeTarget{screen: "> ", started: now.Add(-time.Hour)}
	if Idle(tg, p, now) {
		t.Fatal("zero LastOutput must not be idle")
	}
	tg.lastOut = now.Add(-2 * time.Second)
	tg.started = now.Add(-2 * time.Second)
	if Idle(tg, p, now) {
		t.Fatal("started 2s ago (< MinBoot): not idle")
	}
	tg.started = now.Add(-MinBoot)
	if !Idle(tg, p, now) {
		t.Fatal("started MinBoot ago: idle")
	}
	p.IdleMS = 5000
	tg.lastOut = now.Add(-6 * time.Second)
	tg.started = now.Add(-4 * time.Second)
	if Idle(tg, p, now) {
		t.Fatal("started < idle_ms ago: not idle")
	}
}

func TestNoInjectionBeforeBoot(t *testing.T) {
	r := newRig(t)
	r.tgt.set(func(f *fakeTarget) { f.lastOut = time.Time{}; f.started = r.clk.Now().Add(-time.Second) })
	m := send(t, r.st, "a", "hi")
	r.step()
	if got := r.tgt.pasted(); len(got) != 0 || state(t, r.st, m.ID) != mailbox.StateQueued {
		t.Fatalf("injected before boot: %q", got)
	}
	r.clk.Advance(5 * time.Second)
	r.tgt.set(func(f *fakeTarget) { f.lastOut = r.clk.Now().Add(-2 * time.Second) })
	r.step()
	if state(t, r.st, m.ID) != mailbox.StateDelivered {
		t.Fatal("should be delivered after boot")
	}
}

// A pending human draft blocks injection until it is committed or older
// than DraftTimeout.
func TestHumanDraftBlocksInjection(t *testing.T) {
	r := newRig(t)
	r.tgt.set(func(f *fakeTarget) {
		f.draft = r.clk.Now().Add(-time.Minute)
		f.lastHuman = f.draft
	})
	m := send(t, r.st, "a", "hi")
	r.step()
	if state(t, r.st, m.ID) != mailbox.StateQueued {
		t.Fatal("injected over a pending draft")
	}
	r.clk.Advance(DraftTimeout)
	r.step()
	if state(t, r.st, m.ID) != mailbox.StateDelivered {
		t.Fatal("abandoned draft (> DraftTimeout) should not block")
	}

	m2 := send(t, r.st, "a", "again")
	r.clk.Advance(time.Hour)
	r.tgt.set(func(f *fakeTarget) { f.draft = r.clk.Now() })
	r.step()
	if state(t, r.st, m2.ID) != mailbox.StateQueued {
		t.Fatal("injected over a fresh draft")
	}
	r.tgt.set(func(f *fakeTarget) { f.draft = time.Time{} }) // human submitted
	r.step()
	if state(t, r.st, m2.ID) != mailbox.StateDelivered {
		t.Fatal("should deliver once the draft is committed")
	}
}

// humanTypingTarget simulates a human keystroke landing during Paste.
type humanTypingTarget struct {
	*fakeTarget
}

func (h humanTypingTarget) Paste(text string) error {
	err := h.fakeTarget.Paste(text)
	h.set(func(f *fakeTarget) { f.lastHuman = time.Now().Add(time.Hour) })
	return err
}

func TestHumanTypedDuringSubmitDelayStillSubmits(t *testing.T) {
	r := newRig(t)
	var logBuf bytes.Buffer
	r.l.Log = slog.New(slog.NewTextHandler(&logBuf, nil))
	r.reg.targets["a"] = humanTypingTarget{r.tgt}
	m := send(t, r.st, "a", "hi")
	r.step()
	if state(t, r.st, m.ID) != mailbox.StateDelivered {
		t.Fatal("should be delivered")
	}
	r.tgt.mu.Lock()
	writes := append([]string(nil), r.tgt.writes...)
	r.tgt.mu.Unlock()
	if len(writes) != 1 || writes[0] != "\r" {
		t.Fatalf("submit writes = %q", writes)
	}
	if !strings.Contains(logBuf.String(), "human typed during the submit delay") {
		t.Fatalf("missing warning log: %s", logBuf.String())
	}
}

func TestRecsPruned(t *testing.T) {
	r := newRig(t)
	m := send(t, r.st, "a", "hi")
	r.step()
	if state(t, r.st, m.ID) != mailbox.StateDelivered {
		t.Fatal("should be delivered")
	}
	// Unknown recipient: state created, nothing injected.
	send(t, r.st, "ghost", "x")
	r.step()
	r.l.mu.Lock()
	n := len(r.l.recs)
	r.l.mu.Unlock()
	if n != 2 {
		t.Fatalf("recs = %d, want 2", n)
	}
	// Drain ghost's queue; "a" is still in its cooldown window.
	if _, err := r.st.Inbox(t.Context(), "ghost", false); err != nil {
		t.Fatal(err)
	}
	msgs, _ := r.st.Queued(t.Context(), "ghost")
	for _, q := range msgs {
		_ = r.st.MarkDelivered(t.Context(), q.ID)
	}
	r.step()
	r.l.mu.Lock()
	_, ghost := r.l.recs["ghost"]
	_, a := r.l.recs["a"]
	r.l.mu.Unlock()
	if ghost || !a {
		t.Fatalf("after drain: ghost kept=%v a kept=%v (want false, true)", ghost, a)
	}
	r.clk.Advance(time.Hour) // cooldown over
	r.step()
	r.l.mu.Lock()
	n = len(r.l.recs)
	r.l.mu.Unlock()
	if n != 0 {
		t.Fatalf("recs = %d after cooldown, want 0", n)
	}
}
