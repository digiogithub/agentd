package screen

import (
	"regexp"
	"strings"
	"sync"
	"time"
)

// History defaults and settle policy (semantic-model §3.5).
const (
	// DefaultHistoryMaxLines caps the stored lines (rules.HistoryMaxLines).
	DefaultHistoryMaxLines = 5000
	// DefaultHistoryMaxBytes caps the stored text bytes (1 MiB).
	DefaultHistoryMaxBytes = 1 << 20
	// DefaultTailSlack is how many of the newest stored lines may differ from
	// the incoming frame while still counting as aligned (the live tail mutates
	// while a reply streams).
	DefaultTailSlack = 3
	// SettleQuiet is the output quiet time after which a frame is settled.
	SettleQuiet = 60 * time.Millisecond
	// SettleMax is the longest the transcript goes unobserved during
	// continuous output.
	SettleMax = 250 * time.Millisecond
	// SyncSettleMax is SettleMax for a child that wraps its repaints in
	// synchronized updates (DEC 2026): every frame between boundaries is
	// complete, so sampling often is safe and keeps fast scrolling from
	// pushing lines out of the viewport between two samples.
	SyncSettleMax = 50 * time.Millisecond
	// MaxDeferrals bounds how many forced observations of a frame that does
	// not align with the stored transcript are deferred before the gap is
	// accepted (AGED-US-0036).
	MaxDeferrals = 3
)

// ShouldObserve is the settle policy: a frame is worth observing when there
// is output newer than the last observation and either the output has been
// quiet for SettleQuiet or the last observation is SettleMax old. lastOutput
// is the time of the session's last PTY output, lastObserved the now passed to
// the previous Observe (zero if never).
func ShouldObserve(lastOutput, lastObserved, now time.Time) bool {
	return shouldObserve(lastOutput, lastObserved, now, SettleMax)
}

// ShouldObserveSync is ShouldObserve for a child that uses synchronized
// updates: the SettleMax bound is SyncSettleMax.
func ShouldObserveSync(lastOutput, lastObserved, now time.Time) bool {
	return shouldObserve(lastOutput, lastObserved, now, SyncSettleMax)
}

func shouldObserve(lastOutput, lastObserved, now time.Time, max time.Duration) bool {
	if lastOutput.IsZero() || !lastOutput.After(lastObserved) {
		return false
	}
	return now.Sub(lastOutput) >= SettleQuiet || now.Sub(lastObserved) >= max
}

// HistoryOpts tunes a History. The zero value gives the defaults.
type HistoryOpts struct {
	// MaxLines caps the stored lines (0 = DefaultHistoryMaxLines, <0 = no cap).
	MaxLines int
	// MaxBytes caps the stored text bytes (0 = DefaultHistoryMaxBytes, <0 = no cap).
	MaxBytes int
	// TailSlack: see DefaultTailSlack (0 = default, <0 = none).
	TailSlack int
	// Normalize maps a line to its comparison key; nil = NormalizeLine.
	Normalize func(string) string
	// Transient reports lines that are never part of the transcript (status
	// rows: spinner_chars / busy_regex); they are dropped from every observed
	// frame. nil = keep all.
	Transient func(text string) bool
}

// History accumulates the transcript of a session whose TUI repaints a
// viewport (alternate screen, no scrollback): every settled frame's transcript
// region is merged into one ordered, bounded list of lines. Rows that scroll
// out of view stay; frames that cannot be aligned with what is stored add a
// RoleGap marker line. Lines carry absolute indices that only grow, so a
// client can page with first_line (ADR-A16) while old lines are dropped.
//
// A History is safe for concurrent use (one mutex; Lines copies).
type History struct {
	mu        sync.Mutex
	opt       HistoryOpts
	lines     []Line   // stored lines, oldest first
	keys      []string // normalised comparison keys, parallel to lines
	base      int      // absolute index of lines[0]
	bytes     int
	gaps      int
	epoch     int
	lastGen   uint64
	lastSeen  time.Time
	haveFrame bool
	deferred  int // consecutive deferred forced observations
}

// NewHistory returns an empty History.
func NewHistory(opt HistoryOpts) *History {
	if opt.MaxLines == 0 {
		opt.MaxLines = DefaultHistoryMaxLines
	}
	if opt.MaxBytes == 0 {
		opt.MaxBytes = DefaultHistoryMaxBytes
	}
	if opt.TailSlack == 0 {
		opt.TailSlack = DefaultTailSlack
	}
	if opt.Normalize == nil {
		opt.Normalize = NormalizeLine
	}
	return &History{opt: opt}
}

var (
	volatileParens = regexp.MustCompile(`\([^()]*\d[^()]*\)`)
	spaceRun       = regexp.MustCompile(`\s+`)
)

// NormalizeLine is the default comparison key: a leading spinner glyph
// (Claude's ✻✶✳✢✽ and braille spinners) is dropped, parenthesised groups with
// digits (elapsed time, token counters: "(12s · 1.2k tokens)") collapse to
// "(#)" and whitespace runs collapse to one space.
func NormalizeLine(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	if r := []rune(s); len(r) > 0 && (strings.ContainsRune("✻✶✳✢✽", r[0]) || (r[0] >= 0x2800 && r[0] <= 0x28ff)) {
		s = strings.TrimSpace(string(r[1:]))
	}
	s = volatileParens.ReplaceAllString(s, "(#)")
	return spaceRun.ReplaceAllString(s, " ")
}

// Observe merges the transcript lines of reg (a settled frame) into the
// history and returns how many lines were appended. gen is the session's
// output sequence at the time of the snapshot: an unchanged non-zero gen is a
// no-op. now is remembered for LastObserved.
func (h *History) Observe(reg Regions, gen uint64, now time.Time) int {
	n, _ := h.observe(reg, gen, now, false)
	return n
}

// ObserveForced is Observe for a frame sampled before the output settled
// (SettleMax on continuous output, no synchronized-update boundary). A frame
// that does not align with the stored transcript, which would insert a gap,
// is deferred instead (nothing stored, deferred = true) up to MaxDeferrals
// times in a row; the caller retries at the next settle.
func (h *History) ObserveForced(reg Regions, gen uint64, now time.Time) (added int, deferred bool) {
	return h.observe(reg, gen, now, true)
}

func (h *History) observe(reg Regions, gen uint64, now time.Time, forced bool) (int, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.lastSeen = now
	if gen != 0 && h.haveFrame && gen == h.lastGen {
		return 0, false
	}
	h.lastGen, h.haveFrame = gen, true

	// Frame lines: transcript role only, leading and trailing blanks trimmed
	// (padding above a bottom-anchored viewport, gap before the status row).
	cur := make([]Line, 0, len(reg.Transcript))
	for _, l := range reg.Transcript {
		if (l.Role == RoleTranscript || l.Role == "") && (h.opt.Transient == nil || !h.opt.Transient(l.Text)) {
			cur = append(cur, l)
		}
	}
	for len(cur) > 0 && strings.TrimSpace(cur[0].Text) == "" {
		cur = cur[1:]
	}
	for len(cur) > 0 && strings.TrimSpace(cur[len(cur)-1].Text) == "" {
		cur = cur[:len(cur)-1]
	}
	if len(cur) == 0 {
		return 0, false
	}
	ck := make([]string, len(cur))
	for i, l := range cur {
		ck[i] = h.opt.Normalize(l.Text)
	}

	added := 0
	switch s, o, ok := h.align(ck); {
	case !ok:
		if forced && len(h.lines) > 0 && h.deferred < MaxDeferrals {
			h.deferred++
			h.haveFrame = false // the retry must not be skipped as "same gen"
			return 0, true
		}
		if len(h.lines) > 0 {
			h.push(Line{Role: RoleGap, Rows: 1}, "")
			h.gaps++
			added++
		}
		for i := range cur {
			h.push(cur[i], ck[i])
		}
		added += len(cur)
	default:
		// Overlap [s, s+o): refresh the live tail, then append what is new.
		slack := h.opt.TailSlack
		if slack < 0 {
			slack = 0
		}
		n := len(h.lines)
		for i := 0; i < o; i++ {
			if ti := s + i; ti >= n-slack && h.lines[ti].Text != cur[i].Text {
				h.setLine(ti, cur[i], ck[i])
			}
		}
		if s+o == n {
			for i := o; i < len(cur); i++ {
				h.push(cur[i], ck[i])
			}
			added += len(cur) - o
		}
	}
	h.deferred = 0
	h.trim()
	return added, false
}

// align finds where the frame keys ck sit against the stored tail. It returns
// the start s in h.lines and the overlap o (ck[:o] against lines[s:s+o]).
// Candidates reaching the end of the stored lines (new rows scrolled in) are
// tried first, largest overlap first; then a frame lying entirely inside the
// stored tail (a dialog covered the lower rows, nothing new).
func (h *History) align(ck []string) (s, o int, ok bool) {
	n := len(h.lines)
	if n == 0 {
		return 0, 0, false
	}
	for s = max(0, n-len(ck)); s < n; s++ {
		if h.matches(s, n-s, ck) {
			return s, n - s, true
		}
	}
	for s = n - len(ck) - 1; s >= 0 && s >= n-len(ck)-8*len(ck)-64; s-- {
		if h.matches(s, len(ck), ck) {
			return s, len(ck), true
		}
	}
	return 0, 0, false
}

// matches reports whether ck[:o] aligns with keys[s:s+o]: equal keys, except
// that the newest TailSlack stored lines may differ as long as at least one
// non-blank line matches and the mismatches do not outnumber the matches.
func (h *History) matches(s, o int, ck []string) bool {
	n := len(h.keys)
	slack := h.opt.TailSlack
	if slack < 0 {
		slack = 0
	}
	anchors, mism := 0, 0
	for i := 0; i < o; i++ {
		k := h.keys[s+i]
		if k == ck[i] {
			if k != "" {
				anchors++
			}
			continue
		}
		if s+i < n-slack {
			return false
		}
		if mism++; mism > slack {
			return false
		}
	}
	return anchors >= 1 && anchors >= mism
}

func (h *History) push(l Line, key string) {
	l.Row = 0
	l.Role = roleOr(l.Role)
	h.lines = append(h.lines, l)
	h.keys = append(h.keys, key)
	h.bytes += len(l.Text)
}

func roleOr(r Role) Role {
	if r == "" {
		return RoleTranscript
	}
	return r
}

func (h *History) setLine(i int, l Line, key string) {
	h.bytes += len(l.Text) - len(h.lines[i].Text)
	l.Row = 0
	l.Role = roleOr(l.Role)
	h.lines[i], h.keys[i] = l, key
}

// trim drops the oldest lines past the caps. It runs only when a cap is
// exceeded by an eighth (amortised O(1) per line) and then compacts into
// fresh slices so dropped lines are garbage collected.
func (h *History) trim() {
	maxL, maxB := h.opt.MaxLines, h.opt.MaxBytes
	over := (maxL > 0 && len(h.lines) > maxL+maxL/8) || (maxB > 0 && h.bytes > maxB+maxB/8)
	if !over {
		return
	}
	drop := 0
	if maxL > 0 && len(h.lines) > maxL {
		drop = len(h.lines) - maxL
	}
	b := h.bytes
	for i := 0; i < drop; i++ {
		b -= len(h.lines[i].Text)
	}
	for maxB > 0 && b > maxB && drop < len(h.lines)-1 {
		b -= len(h.lines[drop].Text)
		drop++
	}
	h.lines = append([]Line(nil), h.lines[drop:]...)
	h.keys = append([]string(nil), h.keys[drop:]...)
	h.base += drop
	h.bytes = b
}

// Lines returns up to limit lines starting at absolute index from (limit <= 0:
// all) and the absolute index of the first returned line, which is greater
// than from when older lines were dropped (first_line, ADR-A16). The result is
// a copy.
func (h *History) Lines(from, limit int) (lines []Line, first int) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if from < h.base {
		from = h.base
	}
	i := from - h.base
	if i > len(h.lines) {
		i = len(h.lines)
	}
	j := len(h.lines)
	if limit > 0 && i+limit < j {
		j = i + limit
	}
	return append([]Line(nil), h.lines[i:j]...), h.base + i
}

// Len is the absolute index one past the newest line (total lines ever
// stored, dropped ones included).
func (h *History) Len() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.base + len(h.lines)
}

// First is the absolute index of the oldest stored line.
func (h *History) First() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.base
}

// Gaps is the number of discontinuities (unaligned frames) seen.
func (h *History) Gaps() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.gaps
}

// Epoch counts Reset calls; clients compare it to know absolute indices
// restarted.
func (h *History) Epoch() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.epoch
}

// Deferred is the number of consecutive deferred forced observations.
func (h *History) Deferred() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.deferred
}

// LastObserved is the now of the most recent Observe (zero if none).
func (h *History) LastObserved() time.Time {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.lastSeen
}

// Reset empties the history and restarts absolute indices at 0 (Epoch+1).
func (h *History) Reset() {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.lines, h.keys = nil, nil
	h.base, h.bytes, h.gaps, h.lastGen, h.haveFrame, h.deferred = 0, 0, 0, 0, false, 0
	h.epoch++
}
