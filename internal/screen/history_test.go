package screen_test

import (
	"bytes"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/digiogithub/agentd/internal/screen"
	"github.com/digiogithub/agentd/internal/testutil/screenfix"
)

func tl(texts ...string) screen.Regions {
	var r screen.Regions
	for _, t := range texts {
		r.Transcript = append(r.Transcript, screen.Line{Text: t, Role: screen.RoleTranscript, Rows: 1})
	}
	return r
}

func texts(ls []screen.Line) []string {
	out := make([]string, len(ls))
	for i, l := range ls {
		out[i] = l.Text
		if l.Role == screen.RoleGap {
			out[i] = "<gap>"
		}
	}
	return out
}

func eq(t *testing.T, h *screen.History, want ...string) {
	t.Helper()
	ls, first := h.Lines(0, 0)
	got := texts(ls)
	if first != h.First() || strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("history (first %d)\n got  %q\n want %q", first, got, want)
	}
}

func TestHistoryFixtureRedraw(t *testing.T) {
	f := screenfix.MustLoad(t, "fake-redraw-claude")
	r := screenfix.NewReplayer(f.Meta.Cols, f.Meta.Rows)
	defer r.Close()
	h := screen.NewHistory(screen.HistoryOpts{})
	end := []byte("\x1b[?2026l")
	rest := f.Log
	frames := 0
	for {
		i := bytes.Index(rest, end)
		if i < 0 {
			break
		}
		r.Feed(rest[:i+len(end)])
		rest = rest[i+len(end):]
		rules := claudeRules()
		rules.SpinnerChars = "✻✶✳✢✽"
		reg := screen.Split(r.Snapshot(-1), rules)
		h.Observe(reg, uint64(frames+1), time.Unix(int64(frames), 0))
		frames++
	}
	ls, first := h.Lines(0, 0)
	t.Logf("%d frames, %d lines, %d gaps", frames, len(ls), h.Gaps())
	if h.Gaps() != 0 || frames < 50 || len(ls) <= f.Meta.Rows {
		t.Fatalf("gaps %d frames %d lines %d (viewport %d rows)", h.Gaps(), frames, len(ls), f.Meta.Rows)
	}
	if first != 0 {
		t.Fatalf("first %d", first)
	}
	var want []string
	for turn := 1; turn <= 5; turn++ {
		want = append(want, fmt.Sprintf("> question %02d", turn))
		for l := 1; l <= 3; l++ {
			w := make([]string, 5)
			for i := range w {
				w[i] = []string{"alpha", "bravo", "charlie", "delta", "echo", "foxtrot", "golf", "hotel", "india", "juliet", "kilo", "lima", "mike", "november", "oscar", "papa", "quebec", "romeo", "sierra", "tango"}[(turn*7+l*3+i)%20]
			}
			want = append(want, fmt.Sprintf("reply %02d.%d: %s", turn, l, strings.Join(w, " ")))
		}
		want = append(want, "")
	}
	for len(want) > 0 && want[len(want)-1] == "" {
		want = want[:len(want)-1]
	}
	got := texts(ls)
	for i := range got {
		got[i] = strings.TrimSpace(got[i])
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("transcript mismatch\n got:\n%s\n want:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

func TestHistoryScrollGrowthSpinnerAndRedraw(t *testing.T) {
	h := screen.NewHistory(screen.HistoryOpts{})
	now := time.Unix(0, 0)
	h.Observe(tl("", "a", "b", "c", ""), 1, now) // padding and trailing blanks trimmed
	eq(t, h, "a", "b", "c")
	if n := h.Observe(tl("a", "b", "c"), 2, now); n != 0 { // identical full redraw
		t.Fatalf("redraw added %d", n)
	}
	h.Observe(tl("a", "b", "c", "d"), 0, now)
	h.Observe(tl("b", "c", "d", "e wor"), 0, now) // a scrolled out, tail streaming
	h.Observe(tl("c", "d", "e world", "f"), 0, now)
	eq(t, h, "a", "b", "c", "d", "e world", "f")
	// spinner / elapsed counter on the live tail line does not duplicate.
	h.Observe(tl("d", "e world", "f", "✻ Thinking… (3s · esc)"), 0, now)
	h.Observe(tl("d", "e world", "f", "✶ Thinking… (4s · esc)"), 0, now)
	eq(t, h, "a", "b", "c", "d", "e world", "f", "✶ Thinking… (4s · esc)")
	// same gen is a no-op.
	h.Observe(tl("zzz"), 7, now)
	if n := h.Observe(tl("yyy"), 7, now); n != 0 {
		t.Fatalf("same gen added %d", n)
	}
	// a frame covering only the top of the viewport (dialog) adds nothing.
	before := h.Len()
	h.Observe(tl("d", "e world"), 0, now)
	if h.Len() != before {
		t.Fatalf("partial frame changed history")
	}
}

func TestHistoryGap(t *testing.T) {
	h := screen.NewHistory(screen.HistoryOpts{})
	h.Observe(tl("a", "b", "c"), 0, time.Time{})
	h.Observe(tl("x", "y", "z"), 0, time.Time{}) // /clear, nothing in common
	eq(t, h, "a", "b", "c", "<gap>", "x", "y", "z")
	if h.Gaps() != 1 {
		t.Fatalf("gaps %d", h.Gaps())
	}
	ls, _ := h.Lines(0, 0)
	if ls[3].Role != screen.RoleGap {
		t.Fatalf("role %q", ls[3].Role)
	}
	h.Observe(tl("y", "z", "w"), 0, time.Time{})
	eq(t, h, "a", "b", "c", "<gap>", "x", "y", "z", "w")
}

func TestHistoryBoundedAndAbsoluteIndex(t *testing.T) {
	h := screen.NewHistory(screen.HistoryOpts{MaxLines: 100})
	const win = 20
	total := 1000
	for n := win; n <= total; n++ {
		var fr []string
		for i := n - win; i < n; i++ {
			fr = append(fr, fmt.Sprintf("line %d", i))
		}
		h.Observe(tl(fr...), 0, time.Time{})
	}
	ls, first := h.Lines(0, 0)
	if len(ls) > 100+100/8 || h.Len() != total {
		t.Fatalf("stored %d, Len %d", len(ls), h.Len())
	}
	if first != h.First() || first != total-len(ls) {
		t.Fatalf("first %d stored %d", first, len(ls))
	}
	for i, l := range ls {
		if l.Text != fmt.Sprintf("line %d", first+i) {
			t.Fatalf("line %d is %q", first+i, l.Text)
		}
	}
	got, f2 := h.Lines(first+10, 5)
	if f2 != first+10 || len(got) != 5 || got[0].Text != fmt.Sprintf("line %d", first+10) {
		t.Fatalf("page: first %d %v", f2, texts(got))
	}
	if got, f3 := h.Lines(total+5, 0); len(got) != 0 || f3 != total {
		t.Fatalf("past end: %d lines first %d", len(got), f3)
	}
	h.Reset()
	if h.Len() != 0 || h.Epoch() != 1 {
		t.Fatalf("reset: len %d epoch %d", h.Len(), h.Epoch())
	}
}

func TestHistoryMaxBytes(t *testing.T) {
	h := screen.NewHistory(screen.HistoryOpts{MaxBytes: 1000, MaxLines: -1})
	for n := 10; n < 400; n++ {
		var fr []string
		for i := n - 10; i < n; i++ {
			fr = append(fr, fmt.Sprintf("%040d", i))
		}
		h.Observe(tl(fr...), 0, time.Time{})
	}
	ls, _ := h.Lines(0, 0)
	if len(ls)*40 > 1000+1000/8+40 || h.Len() != 399 {
		t.Fatalf("stored %d lines, Len %d", len(ls), h.Len())
	}
}

func TestShouldObserve(t *testing.T) {
	t0 := time.Unix(100, 0)
	ms := func(n int) time.Time { return t0.Add(time.Duration(n) * time.Millisecond) }
	cases := []struct {
		name          string
		out, obs, now time.Time
		want          bool
	}{
		{"never output", time.Time{}, time.Time{}, ms(1000), false},
		{"already observed", ms(0), ms(10), ms(1000), false},
		{"still streaming", ms(100), ms(0), ms(120), false},
		{"quiet 60ms", ms(100), ms(0), ms(160), true},
		{"continuous, 250ms since observe", ms(240), ms(0), ms(250), true},
		{"first output", ms(0), time.Time{}, ms(60), true},
	}
	for _, c := range cases {
		if got := screen.ShouldObserve(c.out, c.obs, c.now); got != c.want {
			t.Errorf("%s: %v", c.name, got)
		}
	}
}

func BenchmarkHistoryObserve2000(b *testing.B) {
	mk := func(from, n int) screen.Regions {
		fr := make([]string, n)
		for i := range fr {
			fr[i] = fmt.Sprintf("line %d with some text to compare", from+i)
		}
		return tl(fr...)
	}
	for _, c := range []struct {
		name string
		step int
	}{{"redraw", 0}, {"scroll1", 1}, {"gap", -1}} {
		b.Run(c.name, func(b *testing.B) {
			h := screen.NewHistory(screen.HistoryOpts{MaxLines: -1, MaxBytes: -1})
			for n := 0; n < 2000; n += 40 {
				h.Observe(mk(n, 60), 0, time.Time{})
			}
			pos := 1960
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				switch {
				case c.step < 0:
					h.Observe(mk(1_000_000+i*100, 50), 0, time.Time{})
					if i%1000 == 999 { // keep the history near 2000 lines
						h.Reset()
						for n := 0; n < 2000; n += 40 {
							h.Observe(mk(n, 60), 0, time.Time{})
						}
					}
				default:
					pos += c.step
					h.Observe(mk(pos, 50), 0, time.Time{})
				}
			}
		})
	}
}

func TestHistoryTransientRowsDropped(t *testing.T) {
	busy := func(s string) bool {
		s = strings.TrimSpace(s)
		return strings.HasPrefix(s, "✻") || strings.Contains(s, "? for shortcuts")
	}
	h := screen.NewHistory(screen.HistoryOpts{Transient: busy})
	h.Observe(tl("a", "b", "✻ Thinking… (3s)", "  ? for shortcuts"), 0, time.Time{})
	h.Observe(tl("a", "b", "c", "✻ Thinking… (4s)"), 0, time.Time{})
	h.Observe(tl("b", "c", "d", "  ? for shortcuts"), 0, time.Time{})
	eq(t, h, "a", "b", "c", "d")
}

func TestHistoryForcedDefersUnalignedFrame(t *testing.T) {
	h := screen.NewHistory(screen.HistoryOpts{})
	now := time.Unix(0, 0)
	h.Observe(tl("a", "b", "c"), 1, now)
	// A mid-repaint frame with nothing in common is deferred, not a gap.
	for i := 0; i < screen.MaxDeferrals; i++ {
		n, def := h.ObserveForced(tl("x", "y", "z"), uint64(10+i), now)
		if !def || n != 0 {
			t.Fatalf("attempt %d: added %d deferred %v", i, n, def)
		}
	}
	eq(t, h, "a", "b", "c")
	if h.Gaps() != 0 || h.Deferred() != screen.MaxDeferrals {
		t.Fatalf("gaps %d deferred %d", h.Gaps(), h.Deferred())
	}
	// The bound is reached: the gap is accepted.
	if _, def := h.ObserveForced(tl("x", "y", "z"), 20, now); def {
		t.Fatal("deferred past the bound")
	}
	eq(t, h, "a", "b", "c", "<gap>", "x", "y", "z")
	if h.Deferred() != 0 {
		t.Fatalf("deferred counter not reset: %d", h.Deferred())
	}
}

func TestHistoryForcedDeferralThenAlignedRetry(t *testing.T) {
	h := screen.NewHistory(screen.HistoryOpts{})
	now := time.Unix(0, 0)
	h.Observe(tl("a", "b", "c"), 1, now)
	if _, def := h.ObserveForced(tl("q", "r"), 2, now); !def {
		t.Fatal("expected deferral")
	}
	// Same generation retried later must not be skipped as a no-op.
	if _, def := h.ObserveForced(tl("b", "c", "d"), 2, now); def {
		t.Fatal("aligned frame deferred")
	}
	eq(t, h, "a", "b", "c", "d")
	if h.Deferred() != 0 {
		t.Fatalf("deferred counter %d", h.Deferred())
	}
	// An aligned forced frame on an empty history is stored directly.
	h2 := screen.NewHistory(screen.HistoryOpts{})
	if n, def := h2.ObserveForced(tl("a"), 1, now); def || n != 1 {
		t.Fatalf("empty history: %d %v", n, def)
	}
}
