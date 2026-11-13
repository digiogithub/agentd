package screen_test

import (
	"regexp"
	"testing"
	"time"

	"github.com/digiogithub/agentd/internal/screen"
	"github.com/digiogithub/agentd/internal/testutil/screenfix"
)

var t0 = time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)

const (
	idleD  = 1500 * time.Millisecond
	graceD = 5 * time.Second
)

// timed returns the fixture snapshot with running-session timestamps: started
// 60 s before now, last output outputAgo before now.
func timed(t *testing.T, name string, now time.Time, outputAgo time.Duration) *screen.Snapshot {
	s := screenfix.MustLoad(t, name).Replay()
	s.State = screen.StateRunning
	s.StartedAt = now.Add(-60 * time.Second)
	s.LastOutput = now.Add(-outputAgo)
	s.LastHuman, s.DraftSince = time.Time{}, time.Time{}
	return s
}

func inputFor(now time.Time) screen.StatusInput {
	return screen.StatusInput{Now: now, Idle: idleD, HumanGrace: graceD, PromptRegexSet: true, PromptMatch: true}
}

func classifyFix(t *testing.T, s *screen.Snapshot, in screen.StatusInput) screen.StatusResult {
	rules := claudeRules()
	reg := screen.Split(s, rules)
	inputRow := -1
	if reg.HasInput {
		inputRow = reg.Input.Row
	}
	opts := screen.DetectOptions(s, inputRow, rules.Menu)
	return screen.Classify(s, reg, opts, in)
}

func TestClassifyFixtures(t *testing.T) {
	now := t0.Add(time.Hour)
	cases := []struct {
		name    string
		fixture string
		ago     time.Duration
		mut     func(*screen.Snapshot, *screen.StatusInput)
		want    screen.Status
		reason  string
	}{
		{"claude dialog quiet -> waiting", "fake-dialog", 600 * time.Millisecond, nil, screen.StatusWaiting, "options"},
		{"dialog still streaming -> working", "fake-dialog", 100 * time.Millisecond, nil, screen.StatusWorking, "output"},
		{"pointer menu -> waiting", "fake-menu", 2 * time.Second, nil, screen.StatusWaiting, "options"},
		{"yes/no prompt -> waiting", "fake-yesno", 2 * time.Second, nil, screen.StatusWaiting, "options"},
		{"idle input with ghost -> idle", "claude-post-turn-suggestion", 5 * time.Second, nil, screen.StatusIdle, ""},
		{"empty input -> idle", "claude-post-turn-empty", 5 * time.Second, nil, screen.StatusIdle, ""},
		{"recent output -> working", "claude-post-turn-empty", 200 * time.Millisecond, nil, screen.StatusWorking, "output"},
		{"exited beats waiting", "fake-dialog", 5 * time.Second, func(s *screen.Snapshot, _ *screen.StatusInput) { s.State = screen.StateExited }, screen.StatusExited, "exited"},
		{"registry starting", "fake-dialog", 5 * time.Second, func(s *screen.Snapshot, _ *screen.StatusInput) { s.State = screen.StateStarting }, screen.StatusStarting, "registry"},
		{"no output yet", "claude-post-turn-empty", 0, func(s *screen.Snapshot, _ *screen.StatusInput) { s.LastOutput = time.Time{} }, screen.StatusStarting, "no_output"},
		{"boot window beats waiting", "fake-dialog", 2 * time.Second, func(s *screen.Snapshot, _ *screen.StatusInput) { s.StartedAt = now.Add(-2 * time.Second) }, screen.StatusStarting, "boot"},
		{"boot window uses idle_ms when larger", "claude-post-turn-empty", 5 * time.Second, func(s *screen.Snapshot, in *screen.StatusInput) {
			s.StartedAt = now.Add(-4 * time.Second)
			in.Idle = 6 * time.Second
		}, screen.StatusStarting, "boot"},
		{"no prompt match -> idle low confidence", "claude-post-turn-empty", 5 * time.Second, func(_ *screen.Snapshot, in *screen.StatusInput) { in.PromptMatch = false }, screen.StatusIdle, screen.ReasonNoPrompt},
		{"human typing is not working", "claude-post-turn-empty", 5 * time.Second, func(s *screen.Snapshot, _ *screen.StatusInput) { s.LastHuman = now.Add(-time.Second) }, screen.StatusIdle, ""},
		{"echo of keystroke is not working", "claude-post-turn-empty", 100 * time.Millisecond, func(s *screen.Snapshot, _ *screen.StatusInput) { s.LastHuman = now.Add(-150 * time.Millisecond) }, screen.StatusIdle, ""},
		{"output long after keystroke is working", "claude-post-turn-empty", 100 * time.Millisecond, func(s *screen.Snapshot, _ *screen.StatusInput) { s.LastHuman = now.Add(-2 * time.Second) }, screen.StatusWorking, "output"},
		{"waiting_regex", "claude-post-turn-empty", 5 * time.Second, func(_ *screen.Snapshot, in *screen.StatusInput) { in.WaitingRegex = regexp.MustCompile(`❯`) }, screen.StatusWaiting, "waiting_regex"},
		{"busy footer beats waiting, quiet -> working", "fake-box-ghost", 5 * time.Second, func(_ *screen.Snapshot, in *screen.StatusInput) {
			in.BusyRegex = regexp.MustCompile(`for shortcuts`)
			in.WaitingRegex = regexp.MustCompile(`❯`)
		}, screen.StatusWorking, "busy"},
		{"option confidence floor falls back to dialog", "fake-dialog", 2 * time.Second, func(_ *screen.Snapshot, in *screen.StatusInput) { in.OptionMinConfidence = 1.1 }, screen.StatusWaiting, "dialog"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := timed(t, tc.fixture, now, tc.ago)
			in := inputFor(now)
			if tc.mut != nil {
				tc.mut(s, &in)
			}
			r := classifyFix(t, s, in)
			if r.Status != tc.want || r.Reason != tc.reason {
				t.Fatalf("got %s/%q (conf %.1f), want %s/%q", r.Status, r.Reason, r.Confidence, tc.want, tc.reason)
			}
			if tc.reason == screen.ReasonNoPrompt && r.Confidence != 0.4 {
				t.Errorf("confidence %v", r.Confidence)
			}
		})
	}
}

func TestClassifyHumanActive(t *testing.T) {
	now := t0.Add(time.Hour)
	s := timed(t, "claude-post-turn-empty", now, 5*time.Second)
	if classifyFix(t, s, inputFor(now)).HumanActive {
		t.Error("no human input: active")
	}
	s.LastHuman = now.Add(-time.Second)
	if !classifyFix(t, s, inputFor(now)).HumanActive {
		t.Error("recent keystroke: not active")
	}
	s.LastHuman = now.Add(-time.Minute)
	s.DraftSince = now.Add(-time.Minute)
	if !classifyFix(t, s, inputFor(now)).HumanActive {
		t.Error("pending draft: not active")
	}
	s.DraftSince = now.Add(-screen.DraftMaxAge - time.Second)
	if classifyFix(t, s, inputFor(now)).HumanActive {
		t.Error("stale draft: active")
	}
}

// A spinner row found by Split (status region) means working even when quiet.
func TestClassifySpinnerStatusRow(t *testing.T) {
	now := t0.Add(time.Hour)
	s := &screen.Snapshot{Cols: 40, Rows: 3, State: screen.StateRunning,
		StartedAt: now.Add(-time.Minute), LastOutput: now.Add(-10 * time.Second)}
	reg := screen.Regions{Status: []screen.Line{{Text: "✻ Thinking… (esc to interrupt)"}}}
	r := screen.Classify(s, reg, nil, inputFor(now))
	if r.Status != screen.StatusWorking || r.Reason != "busy" {
		t.Fatalf("%+v", r)
	}
}

// step is one observation fed to Next.
type step struct {
	r       screen.StatusResult
	set     bool
	reason  string
	comment string
}

func res(st screen.Status, at time.Duration) screen.StatusResult {
	return screen.StatusResult{Status: st, Now: t0.Add(at)}
}

func TestAttentionTransitions(t *testing.T) {
	bell := func(st screen.Status, at time.Duration, bells, notifies uint64) screen.StatusResult {
		r := res(st, at)
		r.Bells, r.Notifies = bells, notifies
		return r
	}
	typed := res(screen.StatusIdle, 40*time.Second)
	typed.LastHuman = t0.Add(39 * time.Second)

	cases := []struct {
		name  string
		steps []step
	}{
		{"working to idle sets turn_done", []step{
			{res(screen.StatusIdle, 0), false, "", "baseline"},
			{res(screen.StatusWorking, time.Second), false, "", ""},
			{res(screen.StatusIdle, 5*time.Second), true, screen.ReasonTurnDone, ""},
			{res(screen.StatusIdle, 9*time.Second), true, screen.ReasonTurnDone, "stays set"},
		}},
		{"idle to idle never sets", []step{
			{res(screen.StatusIdle, 0), false, "", ""},
			{res(screen.StatusIdle, time.Second), false, "", ""},
		}},
		{"working to waiting sets waiting", []step{
			{res(screen.StatusWorking, 0), false, "", ""},
			{res(screen.StatusWaiting, time.Second), true, screen.ReasonWaiting, ""},
		}},
		{"first observation waiting sets", []step{
			{res(screen.StatusWaiting, 0), true, screen.ReasonWaiting, ""},
		}},
		{"starting to idle does not", []step{
			{res(screen.StatusStarting, 0), false, "", ""},
			{res(screen.StatusIdle, time.Second), false, "", ""},
		}},
		{"new working phase clears", []step{
			{res(screen.StatusWorking, 0), false, "", ""},
			{res(screen.StatusIdle, time.Second), true, screen.ReasonTurnDone, ""},
			{res(screen.StatusWorking, 2*time.Second), false, "", "cleared"},
			{res(screen.StatusIdle, 3*time.Second), true, screen.ReasonTurnDone, "again"},
		}},
		{"waiting then answered then idle keeps flag until ack/human", []step{
			{res(screen.StatusWaiting, 0), true, screen.ReasonWaiting, ""},
			{res(screen.StatusIdle, time.Second), true, screen.ReasonWaiting, ""},
		}},
		{"bell while idle sets bell", []step{
			{bell(screen.StatusIdle, 0, 0, 0), false, "", ""},
			{bell(screen.StatusIdle, time.Second, 1, 0), true, screen.ReasonBell, ""},
		}},
		{"notify while waiting after waiting flag keeps first reason", []step{
			{bell(screen.StatusWaiting, 0, 0, 0), true, screen.ReasonWaiting, ""},
			{bell(screen.StatusWaiting, time.Second, 0, 1), true, screen.ReasonWaiting, ""},
		}},
		{"notify while idle sets notify", []step{
			{bell(screen.StatusIdle, 0, 0, 0), false, "", ""},
			{bell(screen.StatusIdle, time.Second, 0, 1), true, screen.ReasonNotify, ""},
		}},
		{"bell during working is ignored, even later", []step{
			{bell(screen.StatusWorking, 0, 0, 0), false, "", ""},
			{bell(screen.StatusWorking, time.Second, 1, 0), false, "", "ignored"},
			{bell(screen.StatusWorking, 2*time.Second, 1, 0), false, "", ""},
			{bell(screen.StatusIdle, 3*time.Second, 1, 0), true, screen.ReasonTurnDone, "turn_done, not bell"},
		}},
		{"pre-existing bells baselined on first observation", []step{
			{bell(screen.StatusIdle, 0, 7, 3), false, "", ""},
			{bell(screen.StatusIdle, time.Second, 7, 3), false, "", ""},
			{bell(screen.StatusIdle, 2*time.Second, 8, 3), true, screen.ReasonBell, ""},
		}},
		{"bell while starting consumed", []step{
			{bell(screen.StatusStarting, 0, 0, 0), false, "", ""},
			{bell(screen.StatusStarting, time.Second, 2, 0), false, "", ""},
			{bell(screen.StatusIdle, 2*time.Second, 2, 0), false, "", ""},
		}},
		{"human keystroke clears flag", []step{
			{res(screen.StatusWorking, 0), false, "", ""},
			{res(screen.StatusIdle, time.Second), true, screen.ReasonTurnDone, ""},
			{typed, false, "", "typing at 39s after flag at 1s"},
		}},
		{"human keystroke before the flag does not clear", []step{
			{func() screen.StatusResult {
				r := res(screen.StatusWorking, 0)
				r.LastHuman = t0.Add(-time.Second)
				return r
			}(), false, "", ""},
			{func() screen.StatusResult {
				r := res(screen.StatusIdle, time.Second)
				r.LastHuman = t0.Add(-time.Second)
				return r
			}(), true, screen.ReasonTurnDone, ""},
		}},
		{"exited does not fire", []step{
			{res(screen.StatusWorking, 0), false, "", ""},
			{res(screen.StatusExited, time.Second), false, "", ""},
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var a screen.Attention
			for i, st := range tc.steps {
				a = screen.Next(a, st.r)
				if a.Set != st.set || a.Reason != st.reason {
					t.Fatalf("step %d (%s %s): set=%v reason=%q, want %v %q", i, st.r.Status, st.comment, a.Set, a.Reason, st.set, st.reason)
				}
				if a.Last != st.r.Status {
					t.Fatalf("step %d: Last=%s", i, a.Last)
				}
			}
		})
	}
}

func TestAttentionAckAndSince(t *testing.T) {
	a := screen.Next(screen.Attention{}, res(screen.StatusWorking, 0))
	a = screen.Next(a, res(screen.StatusIdle, time.Second))
	if !a.Set || !a.Since.Equal(t0.Add(time.Second)) {
		t.Fatalf("%+v", a)
	}
	a = screen.Next(a, res(screen.StatusIdle, 5*time.Second))
	if !a.Since.Equal(t0.Add(time.Second)) {
		t.Error("Since moved while still set")
	}
	acked := a.Ack()
	if acked.Set || acked.Reason != "" || !acked.Since.IsZero() || acked.Last != screen.StatusIdle {
		t.Fatalf("ack: %+v", acked)
	}
	acked = screen.Next(acked, res(screen.StatusIdle, 6*time.Second))
	if acked.Set {
		t.Error("idle after ack re-set the flag")
	}
}

// End to end on a fixture: dialog -> waiting -> flag.
func TestClassifyThenNextDialog(t *testing.T) {
	now := t0.Add(time.Hour)
	s := timed(t, "fake-dialog", now, 100*time.Millisecond)
	a := screen.Next(screen.Attention{}, classifyFix(t, s, inputFor(now)))
	if a.Set {
		t.Fatal("working at first sight")
	}
	later := now.Add(2 * time.Second)
	s.LastOutput = now
	a = screen.Next(a, classifyFix(t, s, inputFor(later)))
	if !a.Set || a.Reason != screen.ReasonWaiting {
		t.Fatalf("%+v", a)
	}
}
