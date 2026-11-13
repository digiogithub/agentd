// Package inject is the safe input path into a session's terminal, shared by
// the delivery loop and any other caller that types on behalf of an agent
// (AGENTS.md, "Injection"). It pastes text as a bracketed paste, verifies
// that the paste shows up on the ghost-filtered screen before submitting
// (Enter on an empty input line may submit the harness's suggested prompt),
// waits the profile's submit delay measured from the paste and writes the
// profile's submit sequence.
//
// Human races: a Guard runs right before the paste and aborts the attempt
// when it fails (nothing is written). If a human typed during the submit
// delay, the submit is sent anyway and Result.HumanRaced is set: the paste is
// already in the input line, and withholding the submit would leave it glued
// to whatever the human types next.
//
// Input calls on the same target are serialized by a per-target lock held
// across guard, paste, verification and submit, so two callers cannot
// interleave pastes.
package inject

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/digiogithub/agentd/internal/config"
	"github.com/digiogithub/agentd/internal/session"
)

// DraftTimeout is how long a pending human draft blocks input; an older
// draft is considered abandoned.
const DraftTimeout = 10 * time.Minute

// PasteVerifyTimeout bounds how long Input waits for the pasted text to show
// up on the (ghost-filtered) screen before giving up without submitting.
const PasteVerifyTimeout = time.Second

// pastePoll is the polling interval of the paste verification.
const pastePoll = 25 * time.Millisecond

// needleRunes is the default needle length when Request.Needle is empty.
const needleRunes = 48

var (
	// ErrDraft is for guards that refuse to type over a pending human draft.
	ErrDraft = errors.New("pending human draft")
	// ErrPasteNotVisible is returned when the pasted text never appeared on
	// screen; nothing was submitted.
	ErrPasteNotVisible = errors.New("pasted notice not visible on screen; not submitted")
	// ErrEmptyNeedle is returned (before anything is written) when the text
	// has no non-blank line to verify the paste with.
	ErrEmptyNeedle = errors.New("nothing to verify the paste with (no non-blank line)")
)

// drafter is implemented by *session.Session: text left in the input line
// (pasted, not submitted) is recorded as a pending draft.
type drafter interface {
	MarkDraft()
	ClearDraft()
}

// Target is the subset of *session.Session the injector needs.
type Target interface {
	State() string
	LastHumanInput() time.Time
	HumanDraftSince() time.Time
	// ScreenFiltered returns the visible screen (ghost text blanked when
	// opts.HideGhost) and the ghost text found on the input line.
	ScreenFiltered(opts session.ScreenOpts) (screen, ghost string)
	Paste(text string) error
	Write(p []byte, human bool) (int, error)
}

// Request describes one input operation.
type Request struct {
	// Text is pasted as a bracketed paste (session.PasteSafe applies).
	Text string
	// Needle must show up on the ghost-filtered screen (whitespace
	// insensitive), tied to this paste: it counts only if it appears in the
	// input region (InputRegion) where it was not before, or if its number of
	// occurrences on the screen grew versus a baseline taken just before the
	// paste. Empty = the first 48 runes of the first non-blank line of the
	// paste-safe Text; if there is none, Input fails with ErrEmptyNeedle.
	Needle string
	// InputRegion, when set, returns the text of the input region (the
	// harness's input box / line, from screen.Split) of the live screen.
	InputRegion func() string
	// BeforeSubmit runs after the paste is verified and the submit delay has
	// passed, right before the submit sequence, with the target lock held. A
	// non-nil error means do not submit: the text stays pasted, is recorded
	// as a draft and the error is returned.
	BeforeSubmit func() error
	// Submit writes the profile submit sequence after the submit delay.
	Submit bool
	// Guard runs right before the paste, with the target lock held; a
	// non-nil error aborts the input (nothing written) and is returned.
	Guard func() error
}

// Result reports what Input did.
type Result struct {
	Pasted     bool // the paste was written
	Submitted  bool // the submit sequence was written
	HumanRaced bool // a human keystroke arrived between the guard and the submit
}

// GhostOpts returns the screen options that hide the ghost text of profile
// p's harness (ghost_faint, ghost_colors, ghost_right_of_cursor,
// prompt_marker).
func GhostOpts(p config.Profile) session.ScreenOpts {
	return session.ScreenOpts{HideGhost: true, Ghost: session.GhostRules{
		Faint:         p.GhostFaint,
		Colors:        p.GhostColorSpecs,
		RightOfCursor: p.GhostRightOfCursor,
		PromptMarker:  p.PromptMarker,
	}}
}

// DraftPending reports whether a human draft younger than DraftTimeout is
// pending on t at time now.
func DraftPending(t Target, now time.Time) bool {
	d := t.HumanDraftSince()
	return !d.IsZero() && now.Sub(d) < DraftTimeout
}

// PasteVisible reports whether needle appears in screen, ignoring all
// whitespace (including no-break spaces and line breaks), so text that the
// harness wrapped or indented over several rows still matches.
func PasteVisible(screen, needle string) bool {
	return strings.Contains(stripSpace(screen), stripSpace(needle))
}

func stripSpace(s string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsSpace(r) || r == ' ' {
			return -1
		}
		return r
	}, s)
}

// DefaultNeedle is the first 48 runes of the first non-blank line of text
// ("" when every line is blank).
func DefaultNeedle(text string) string {
	for _, line := range strings.Split(text, "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		if r := []rune(line); len(r) > needleRunes {
			line = string(r[:needleRunes])
		}
		return line
	}
	return ""
}

// countVisible is the number of occurrences of needle in screen, ignoring
// whitespace.
func countVisible(screen, needle string) int {
	n := stripSpace(needle)
	if n == "" {
		return 0
	}
	return strings.Count(stripSpace(screen), n)
}

var locks sync.Map // key -> chan struct{} (capacity 1)

type nameKey string

// sharedKey serializes every target that has neither a Name nor pointer
// identity (such targets cannot be map keys safely).
type sharedKey struct{}

// lockKey identifies a target: its Name when it has one, else its pointer
// identity. Anything else (non-pointer values, possibly non-comparable) shares
// one lock rather than panicking as a map key.
func lockKey(t Target) any {
	if n, ok := t.(interface{ Name() string }); ok {
		return nameKey(n.Name())
	}
	if reflect.ValueOf(t).Kind() == reflect.Pointer {
		return t
	}
	return sharedKey{}
}

func lockFor(t Target) chan struct{} {
	k := lockKey(t)
	if c, ok := locks.Load(k); ok {
		return c.(chan struct{})
	}
	c, _ := locks.LoadOrStore(k, make(chan struct{}, 1))
	return c.(chan struct{})
}

// Lock acquires t's input lock, waiting until it is free or ctx is done. The
// returned func releases it. Input takes it itself; Lock is for callers that
// need to hold it across several operations.
func Lock(ctx context.Context, t Target) (unlock func(), err error) {
	c := lockFor(t)
	select {
	case c <- struct{}{}:
		return func() { <-c }, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// Input pastes r.Text into t, verifies it landed in the input line and, when
// r.Submit, writes the profile's submit sequence after its submit delay.
func Input(ctx context.Context, t Target, p config.Profile, r Request) (Result, error) {
	var res Result
	unlock, err := Lock(ctx, t)
	if err != nil {
		return res, err
	}
	defer unlock()

	needle := r.Needle
	if needle == "" {
		needle = DefaultNeedle(session.PasteSafe(r.Text))
	}
	if stripSpace(needle) == "" {
		return res, ErrEmptyNeedle
	}
	human := t.LastHumanInput()
	if r.Guard != nil {
		if err := r.Guard(); err != nil {
			return res, err
		}
	}
	opts := GhostOpts(p)
	// Baseline just before the paste: the verification must be caused by it.
	scr0, _ := t.ScreenFiltered(opts)
	base := countVisible(scr0, needle)
	inRegion0 := r.InputRegion != nil && PasteVisible(r.InputRegion(), needle)
	pasted := time.Now()
	if err := t.Paste(r.Text); err != nil {
		return res, err
	}
	res.Pasted = true
	dr, _ := t.(drafter)
	// Never submit unless the paste is visible.
	for {
		scr, _ := t.ScreenFiltered(opts)
		if countVisible(scr, needle) > base ||
			(r.InputRegion != nil && !inRegion0 && PasteVisible(r.InputRegion(), needle)) {
			break
		}
		if time.Since(pasted) >= PasteVerifyTimeout {
			return res, ErrPasteNotVisible
		}
		if err := sleepCtx(ctx, pastePoll); err != nil {
			return res, err
		}
	}
	// From here the text is in the input line: any return without a submit
	// leaves a draft that delivery and later acts must respect.
	leaveDraft := func() {
		if dr != nil {
			dr.MarkDraft()
		}
	}
	if !r.Submit {
		leaveDraft()
		return res, nil
	}
	if err := sleepCtx(ctx, p.SubmitDelay()-time.Since(pasted)); err != nil {
		leaveDraft()
		return res, err
	}
	if r.BeforeSubmit != nil {
		if err := r.BeforeSubmit(); err != nil {
			leaveDraft()
			return res, err
		}
	}
	// The paste is in: submit anyway rather than leave it glued to the
	// human's next input.
	res.HumanRaced = t.LastHumanInput().After(human)
	submit := p.Submit
	if submit == "" {
		submit = config.DefaultSubmit
	}
	if _, err := t.Write([]byte(submit), false); err != nil {
		leaveDraft()
		return res, err
	}
	res.Submitted = true
	if dr != nil {
		dr.ClearDraft()
	}
	return res, nil
}

func sleepCtx(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return ctx.Err()
	}
	tm := time.NewTimer(d)
	defer tm.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-tm.C:
		return nil
	}
}
