package screen

import (
	"regexp"
	"time"
)

// Status is the coarse activity state of a session (semantic-model §5).
type Status string

// Statuses, in precedence order: the first rule that matches wins.
const (
	StatusExited   Status = "exited"
	StatusStarting Status = "starting"
	StatusWaiting  Status = "waiting"
	StatusWorking  Status = "working"
	StatusIdle     Status = "idle"
)

// Classifier constants.
const (
	// MinBoot mirrors delivery.MinBoot: a session younger than
	// max(idle, MinBoot) is still starting.
	MinBoot = 3 * time.Second
	// WaitingQuiet caps the quiet time a static dialog needs to count as
	// waiting: min(idle, WaitingQuiet).
	WaitingQuiet = 500 * time.Millisecond
	// EchoWindow: output that follows a human keystroke by less than this is
	// the terminal echoing the typing, never agent work.
	EchoWindow = 300 * time.Millisecond
	// ReasonNoPrompt marks an idle result whose prompt_regex does not match
	// (crashed into a shell, unknown screen).
	ReasonNoPrompt = "no_prompt"
)

// Attention reasons.
const (
	ReasonTurnDone = "turn_done"
	ReasonWaiting  = "waiting"
	ReasonBell     = "bell"
	ReasonNotify   = "notify"
)

// StatusInput carries what Classify needs besides the snapshot: the profile
// durations and regexes, and values the caller computes (the prompt match is
// on the ghost-filtered screen, as in delivery.Idle).
type StatusInput struct {
	Now time.Time
	// Idle is the profile idle_ms; HumanGrace human_grace_ms. MinBoot 0 means
	// the package MinBoot.
	Idle, HumanGrace, MinBoot time.Duration

	// PromptRegexSet: the profile has a prompt_regex; PromptMatch: it matches
	// the ghost-filtered screen.
	PromptRegexSet, PromptMatch bool
	// BusyRegex (busy_regex) is tested against the status and footer rows.
	BusyRegex *regexp.Regexp
	// WaitingRegex (waiting_regex) is tested against ScreenText.
	WaitingRegex *regexp.Regexp
	// ScreenText is the ghost-filtered screen text; empty = Snapshot.Text().
	ScreenText string
	// OptionMinConfidence filters the options passed to Classify (0 = all).
	OptionMinConfidence float64
}

// StatusResult is the classification of one snapshot plus the raw signals the
// attention tracker (Next) needs. It carries no history.
type StatusResult struct {
	Status     Status
	Reason     string  // "", ReasonNoPrompt, "options", "dialog", "waiting_regex", "busy", "spinner", "output", "boot"...
	Confidence float64 // 0..1
	Now        time.Time
	// IdleFor is the time since the last PTY output (0 when none yet).
	IdleFor time.Duration
	// HumanActive: a human keystroke within human_grace_ms or a pending draft.
	// It never makes the status working.
	HumanActive bool
	LastHuman   time.Time
	LastOutput  time.Time
	ExitCode    *int
	// Notification counters, copied from the snapshot.
	Bells, Notifies uint64
}

// Classify computes the status of s. reg is Split(s, rules) and opts the
// options detected on the same snapshot. Pure: no clock, no state.
//
// Order: exited > starting > waiting > working > idle. Human typing is never
// work: output that merely echoes a keystroke (EchoWindow) is ignored and
// keystrokes themselves only set HumanActive. Unlike delivery.Idle, neither
// human_grace_ms nor drafts delay idle.
func Classify(s *Snapshot, reg Regions, opts []Option, in StatusInput) StatusResult {
	r := StatusResult{
		Now: in.Now, Confidence: 1, LastHuman: s.LastHuman, LastOutput: s.LastOutput,
		ExitCode: s.ExitCode, Bells: s.Bells, Notifies: s.Notifies,
	}
	if !s.LastOutput.IsZero() {
		r.IdleFor = in.Now.Sub(s.LastOutput)
	}
	if !s.LastHuman.IsZero() && in.Now.Sub(s.LastHuman) < in.HumanGrace {
		r.HumanActive = true
	}
	if !s.DraftSince.IsZero() && in.Now.Sub(s.DraftSince) < DraftMaxAge {
		r.HumanActive = true
	}

	if s.State == StateExited {
		r.Status, r.Reason = StatusExited, "exited"
		return r
	}
	boot := in.MinBoot
	if boot <= 0 {
		boot = MinBoot
	}
	switch {
	case s.State == StateStarting:
		r.Status, r.Reason = StatusStarting, "registry"
		return r
	case s.LastOutput.IsZero():
		r.Status, r.Reason = StatusStarting, "no_output"
		return r
	case in.Now.Sub(s.StartedAt) < max(in.Idle, boot):
		r.Status, r.Reason = StatusStarting, "boot"
		return r
	}

	busy := busyRows(reg, in)
	output := r.IdleFor < in.Idle && !echo(s)

	if !busy {
		if reason, conf, ok := waitingSignal(s, reg, opts, in); ok {
			quiet := min(in.Idle, WaitingQuiet)
			if reason == "waiting_regex" {
				quiet = in.Idle
			}
			if r.IdleFor >= quiet {
				r.Status, r.Reason, r.Confidence = StatusWaiting, reason, conf
				return r
			}
		}
	}
	switch {
	case output:
		r.Status, r.Reason, r.Confidence = StatusWorking, "output", 1
	case busy:
		r.Status, r.Reason, r.Confidence = StatusWorking, "busy", 0.9
	default:
		r.Status = StatusIdle
		if in.PromptRegexSet && !in.PromptMatch {
			r.Reason, r.Confidence = ReasonNoPrompt, 0.4
		}
	}
	return r
}

// echo: the last output directly follows the last human keystroke.
func echo(s *Snapshot) bool {
	if s.LastHuman.IsZero() || s.LastOutput.Before(s.LastHuman) {
		return false
	}
	return s.LastOutput.Sub(s.LastHuman) < EchoWindow
}

// busyRows: status rows (busy_regex / spinner glyph, found by Split) exist, or
// busy_regex matches a footer row.
func busyRows(reg Regions, in StatusInput) bool {
	if len(reg.Status) > 0 {
		return true
	}
	if in.BusyRegex != nil {
		for _, f := range reg.Footer {
			if in.BusyRegex.MatchString(f) {
				return true
			}
		}
	}
	return false
}

// waitingSignal reports whether the screen asks the user something.
func waitingSignal(s *Snapshot, reg Regions, opts []Option, in StatusInput) (string, float64, bool) {
	best := 0.0
	for _, o := range opts {
		if o.Confidence < in.OptionMinConfidence {
			continue
		}
		// An option list in the transcript is prose, not a prompt.
		if reg.Dialog == nil && o.Row >= 0 && o.Row < len(reg.Of) && reg.Of[o.Row] == RegionTranscript {
			continue
		}
		best = max(best, o.Confidence)
	}
	if best > 0 {
		return "options", best, true
	}
	// A framed dialog counts when it owns the bottom of the screen.
	if reg.Dialog != nil && !reg.HasInput {
		return "dialog", 0.7, true
	}
	if in.WaitingRegex != nil {
		text := in.ScreenText
		if text == "" {
			text = s.Text()
		}
		if in.WaitingRegex.MatchString(text) {
			return "waiting_regex", 0.8, true
		}
	}
	return "", 0, false
}

// Attention is the per-session "look at me" flag. The daemon owns one per
// session and advances it with Next on every classification; Classify itself
// is stateless.
type Attention struct {
	Set    bool
	Reason string // ReasonTurnDone, ReasonWaiting, ReasonBell, ReasonNotify
	Since  time.Time
	// SeenBell / SeenNotify are the counters already accounted for.
	SeenBell, SeenNotify uint64
	// Last is the previous status ("" before the first observation) and
	// WorkSince the start of the current working phase; both feed edge
	// detection.
	Last      Status
	WorkSince time.Time
}

// Ack clears the flag (explicit acknowledgement, e.g. any act on the session).
func (a Attention) Ack() Attention {
	a.Set, a.Reason, a.Since = false, "", time.Time{}
	return a
}

// Next folds one classification into the attention state:
//   - first observation: counters are baselined (old bells never fire); a first
//     status of waiting sets the flag;
//   - entering working from another status starts a work phase and clears the
//     flag; a human keystroke after the flag was set clears it too;
//   - working -> idle sets turn_done; any other status -> waiting sets waiting;
//   - a BEL / OSC 9 / OSC 777 counter increase sets bell/notify while idle or
//     waiting; during working or starting it is consumed and ignored.
//
// A set flag keeps its first reason and Since.
func Next(prev Attention, r StatusResult) Attention {
	a := prev
	first := prev.Last == ""
	set := func(reason string) {
		if !a.Set {
			a.Set, a.Reason, a.Since = true, reason, r.Now
		}
	}
	if a.Set && !r.LastHuman.IsZero() && r.LastHuman.After(a.Since) {
		a = a.Ack()
	}
	switch r.Status {
	case StatusWorking:
		if prev.Last != StatusWorking {
			a.WorkSince = r.Now
			a = a.Ack()
		}
	case StatusWaiting:
		if prev.Last != StatusWaiting {
			set(ReasonWaiting)
		}
	case StatusIdle:
		if prev.Last == StatusWorking {
			set(ReasonTurnDone)
		}
	}
	if r.Status != StatusWorking {
		a.WorkSince = time.Time{}
	}
	switch {
	case first:
		a.SeenBell, a.SeenNotify = r.Bells, r.Notifies
	case r.Status == StatusIdle || r.Status == StatusWaiting:
		if r.Bells > a.SeenBell {
			set(ReasonBell)
		}
		if r.Notifies > a.SeenNotify {
			set(ReasonNotify)
		}
		a.SeenBell, a.SeenNotify = r.Bells, r.Notifies
	default: // working, starting, exited: consumed, never fires
		a.SeenBell, a.SeenNotify = r.Bells, r.Notifies
	}
	a.Last = r.Status
	return a
}
