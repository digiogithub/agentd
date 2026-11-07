// Package delivery injects queued mailbox messages into the terminals of idle
// agent sessions (AGENTS.md, "Idle detection", "Injection", "Mailbox states").
//
// The Loop wakes up every Interval and whenever Kick is called. For every
// recipient with queued messages it looks the target up in the Registry and,
// if the target is running and idle, pastes a notice for the oldest queued
// message (bracketed paste), waits the profile's submit delay and writes the
// profile's submit sequence. On success the message is marked delivered; on
// error it stays queued.
//
// One message per idle window: after an injection attempt at time T (taken
// after the submit write), the recipient is not injected again until either
//
//   - its LastOutput is after T (the agent reacted to the notice) and it is
//     idle again (Idle: quiet for idle_ms, no human input for human_grace_ms,
//     prompt matches), or
//   - the cooldown has elapsed since T: Loop.Cooldown if set, otherwise
//     max(10s, 4×idle_ms). This covers agents that swallow a notice without
//     producing output, so a queue can never get stuck.
//
// The same gate applies after a failed attempt, so a broken target is retried
// at most once per cooldown instead of on every tick.
//
// Injections to the same recipient are serialized (at most one in flight);
// different recipients are served concurrently.
//
// Boot and human drafts: a target that has never produced output is not idle
// (the harness may still be booting), and no injection happens before
// max(idle_ms, MinBoot) has elapsed since the session started. A target whose
// human has typed something without submitting it (HumanDraftSince) is not
// idle either, unless that draft is older than DraftTimeout (abandoned).
//
// Ghost text: the prompt_regex check runs on the screen with the harness's
// ghost text (placeholder / suggested next prompt, see GhostOpts) blanked,
// and drafts are keystroke-based, so a suggestion never counts as input.
// Because some harnesses submit the suggestion when Enter is pressed on an
// empty input line (Claude Code), the paste is verified before submitting:
// the loop polls the ghost-filtered screen for up to PasteVerifyTimeout for
// the start of the notice ("[agentd msg #<id> from", whitespace-insensitive so
// wrapped lines match). If it does not show up, no submit is sent, the message
// stays queued and is retried after the cooldown.
//
// Race with a typing human: human input is re-checked right before the paste
// (any keystroke since the idle check aborts the attempt) and right before
// the submit. If the human typed during the submit delay, the submit is sent
// anyway and a warning is logged: the paste is already in the input line, and
// withholding the submit would leave the notice glued to whatever the human
// types next, which is worse than submitting a line that may contain a few of
// their keystrokes.
package delivery

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/digiogithub/agentd/internal/config"
	"github.com/digiogithub/agentd/internal/mailbox"
	"github.com/digiogithub/agentd/internal/session"
)

// DefaultInterval is the polling interval used when Loop.Interval is zero.
const DefaultInterval = 500 * time.Millisecond

// MinCooldown is the lower bound of the default per-recipient cooldown.
const MinCooldown = 10 * time.Second

// MinBoot is the minimum time after a session started before the first
// injection (the effective bound is max(idle_ms, MinBoot)).
const MinBoot = 3 * time.Second

// DraftTimeout is how long a pending human draft blocks injection; an older
// draft is considered abandoned.
const DraftTimeout = 10 * time.Minute

// PasteVerifyTimeout bounds how long inject waits for the pasted notice to
// show up on the (ghost-filtered) screen before giving up without submitting.
const PasteVerifyTimeout = time.Second

// pastePoll is the polling interval of the paste verification.
const pastePoll = 25 * time.Millisecond

// ErrPasteNotVisible is returned by an injection whose pasted notice never
// appeared on screen; nothing was submitted.
var ErrPasteNotVisible = errors.New("pasted notice not visible on screen; not submitted")

// Input states reported by InputState.
const (
	InputEmpty      = "empty"
	InputDraft      = "draft"
	InputSuggestion = "suggestion"
)

const stateRunning = session.StateRunning

// Target is an injectable terminal. *session.Session satisfies it.
type Target interface {
	Name() string
	State() string
	LastOutput() time.Time
	LastHumanInput() time.Time
	// StartedAt is when the session started.
	StartedAt() time.Time
	// HumanDraftSince is when a human last typed uncommitted input (zero =
	// no pending draft).
	HumanDraftSince() time.Time
	// ScreenFiltered returns the visible screen (ghost text blanked when
	// opts.HideGhost) and the ghost text found on the input line.
	ScreenFiltered(opts session.ScreenOpts) (screen, ghost string)
	Paste(text string) error
	Write(p []byte, human bool) (int, error)
}

// Registry resolves a recipient name to its target and harness profile.
type Registry interface {
	Lookup(name string) (Target, config.Profile, bool)
}

// Loop is the delivery loop. Store and Reg are required; the other fields
// are optional. Fields must not be changed once Run has been called.
type Loop struct {
	Store    *mailbox.Store
	Reg      Registry
	Interval time.Duration // 0 = DefaultInterval
	Log      *slog.Logger  // nil = discard

	// Now is the clock (nil = time.Now). Used for idle checks and the
	// one-per-window gate; tests inject a fake clock.
	Now func() time.Time
	// Cooldown overrides the default cooldown max(MinCooldown, 4×idle).
	Cooldown time.Duration

	once sync.Once
	kick chan struct{}
	wg   sync.WaitGroup

	mu   sync.Mutex
	recs map[string]*recState
}

type recState struct {
	busy     bool          // an injection goroutine is running
	attempt  time.Time     // time of the last injection attempt (zero = none)
	cooldown time.Duration // cooldown in force after attempt
	attempts int           // number of attempts (diagnostics)
}

func (l *Loop) init() {
	l.once.Do(func() {
		l.kick = make(chan struct{}, 1)
		l.recs = make(map[string]*recState)
	})
}

func (l *Loop) now() time.Time {
	if l.Now != nil {
		return l.Now()
	}
	return time.Now()
}

func (l *Loop) log() *slog.Logger {
	if l.Log != nil {
		return l.Log
	}
	return slog.New(slog.DiscardHandler)
}

// Kick asks the loop to check recipient soon. It never blocks and is safe to
// call from any goroutine, before or during Run. The recipient name is a hint:
// a kick triggers a pass over all recipients with queued messages.
func (l *Loop) Kick(recipient string) {
	_ = recipient
	l.init()
	select {
	case l.kick <- struct{}{}:
	default:
	}
}

// Run runs the loop until ctx is done, then waits for in-flight injections
// to finish and returns ctx.Err().
func (l *Loop) Run(ctx context.Context) error {
	l.init()
	iv := l.Interval
	if iv <= 0 {
		iv = DefaultInterval
	}
	t := time.NewTicker(iv)
	defer t.Stop()
	defer l.wg.Wait()
	l.sweep(ctx)
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-t.C:
		case <-l.kick:
		}
		l.sweep(ctx)
	}
}

// sweep starts an injection goroutine for every recipient with queued
// messages that is not already being served.
func (l *Loop) sweep(ctx context.Context) {
	l.init()
	if ctx.Err() != nil {
		return
	}
	names, err := l.Store.QueuedRecipients(ctx)
	if err != nil {
		if ctx.Err() == nil {
			l.log().Warn("delivery: list queued recipients", "err", err)
		}
		return
	}
	l.prune(names)
	for _, name := range names {
		l.mu.Lock()
		rs := l.recs[name]
		if rs == nil {
			rs = &recState{}
			l.recs[name] = rs
		}
		if rs.busy {
			l.mu.Unlock()
			continue
		}
		rs.busy = true
		l.mu.Unlock()

		l.wg.Add(1)
		go func() {
			defer l.wg.Done()
			defer func() {
				l.mu.Lock()
				rs.busy = false
				l.mu.Unlock()
			}()
			l.deliver(ctx, name, rs)
		}()
	}
}

// prune drops the state of recipients that have no queued messages, no
// injection in flight and whose last cooldown has expired (so the
// one-per-window gate is not lost for a recipient that gets a new message
// right after its queue drained).
func (l *Loop) prune(queued []string) {
	keep := make(map[string]bool, len(queued))
	for _, n := range queued {
		keep[n] = true
	}
	now := l.now()
	l.mu.Lock()
	defer l.mu.Unlock()
	for n, rs := range l.recs {
		if keep[n] || rs.busy {
			continue
		}
		if rs.attempt.IsZero() || now.Sub(rs.attempt) >= rs.cooldown {
			delete(l.recs, n)
		}
	}
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

// InputState classifies t's input line under profile p at time now:
// InputDraft when a human typed something not yet submitted (younger than
// DraftTimeout), else InputSuggestion when ghost text is visible, else
// InputEmpty (which may still hold text pasted by agentd or typed with
// `agentd type`).
func InputState(t Target, p config.Profile, now time.Time) string {
	if d := t.HumanDraftSince(); !d.IsZero() && now.Sub(d) < DraftTimeout {
		return InputDraft
	}
	if _, g := t.ScreenFiltered(GhostOpts(p)); g != "" {
		return InputSuggestion
	}
	return InputEmpty
}

// Idle reports whether t is idle under profile p at time now: it has produced
// output and has been running for at least max(p.Idle(), MinBoot), no output
// for p.Idle(), no human input for p.HumanGrace(), no pending human draft
// younger than DraftTimeout, and the prompt check passes on the screen with
// the ghost text hidden.
func Idle(t Target, p config.Profile, now time.Time) bool {
	last := t.LastOutput()
	if last.IsZero() || now.Sub(last) < p.Idle() {
		return false
	}
	if now.Sub(t.StartedAt()) < max(p.Idle(), MinBoot) {
		return false
	}
	if now.Sub(t.LastHumanInput()) < p.HumanGrace() {
		return false
	}
	if d := t.HumanDraftSince(); !d.IsZero() && now.Sub(d) < DraftTimeout {
		return false
	}
	scr, _ := t.ScreenFiltered(GhostOpts(p))
	return p.PromptMatches(scr)
}

// CooldownFor returns the cooldown applied to a recipient with profile p.
func (l *Loop) CooldownFor(p config.Profile) time.Duration {
	if l.Cooldown > 0 {
		return l.Cooldown
	}
	return max(MinCooldown, 4*p.Idle())
}

// deliver injects at most one queued message into recipient name. It runs
// with rs.busy set, so it is the only goroutine touching this recipient.
func (l *Loop) deliver(ctx context.Context, name string, rs *recState) {
	if strings.HasPrefix(name, "human:") {
		return // humans read their mail via the CLI
	}
	tgt, prof, ok := l.Reg.Lookup(name)
	if !ok || tgt == nil || tgt.State() != stateRunning {
		return
	}
	now := l.now()
	if !Idle(tgt, prof, now) {
		return
	}
	l.mu.Lock()
	last := rs.attempt
	l.mu.Unlock()
	if !last.IsZero() && !tgt.LastOutput().After(last) && now.Sub(last) < l.CooldownFor(prof) {
		return // already injected in this idle window
	}

	msgs, err := l.Store.Queued(ctx, name)
	if err != nil {
		if ctx.Err() == nil {
			l.log().Warn("delivery: list queued", "recipient", name, "err", err)
		}
		return
	}
	if len(msgs) == 0 {
		return
	}
	m := msgs[0]
	log := l.log().With("recipient", name, "id", m.ID)

	// A human may have started typing while the queue was read.
	if tgt.LastHumanInput().After(now) || !Idle(tgt, prof, l.now()) {
		return
	}
	err = l.inject(ctx, tgt, prof, m, log)
	l.mu.Lock()
	rs.attempt = l.now()
	rs.cooldown = l.CooldownFor(prof)
	rs.attempts++
	l.mu.Unlock()
	if errors.Is(err, ErrPasteNotVisible) {
		log.Warn("delivery: paste did not show up in the input line; not submitted, message stays queued (retry after cooldown)",
			"cooldown", l.CooldownFor(prof))
		return
	}
	if err != nil {
		log.Warn("delivery: inject failed; message stays queued", "err", err)
		return
	}
	if err := l.Store.MarkDelivered(context.WithoutCancel(ctx), m.ID); err != nil {
		log.Warn("delivery: mark delivered", "err", err)
		return
	}
	log.Info("delivery: injected")
}

// inject pastes the notice, verifies that it landed in the input line,
// waits the rest of the submit delay and writes the submit sequence.
func (l *Loop) inject(ctx context.Context, tgt Target, p config.Profile, m mailbox.Message, log *slog.Logger) error {
	human := tgt.LastHumanInput()
	pasted := time.Now()
	if err := tgt.Paste(FormatNotice(m, p)); err != nil {
		return err
	}
	// Never submit unless the paste is visible: Enter on an empty input line
	// may submit the harness's suggested prompt instead.
	needle := noticePrefix(m)
	opts := GhostOpts(p)
	for {
		scr, _ := tgt.ScreenFiltered(opts)
		if PasteVisible(scr, needle) {
			break
		}
		if time.Since(pasted) >= PasteVerifyTimeout {
			return ErrPasteNotVisible
		}
		if err := sleepCtx(ctx, pastePoll); err != nil {
			return err
		}
	}
	if err := sleepCtx(ctx, p.SubmitDelay()-time.Since(pasted)); err != nil {
		return err
	}
	if tgt.LastHumanInput().After(human) {
		// Trade-off documented in the package comment: the paste is in, so
		// submit anyway rather than leave it glued to the human's next input.
		log.Warn("delivery: human typed during the submit delay; submitting anyway")
	}
	submit := p.Submit
	if submit == "" {
		submit = config.DefaultSubmit
	}
	_, err := tgt.Write([]byte(submit), false)
	return err
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

// noticePrefix is the start of m's notice, used to verify a paste.
func noticePrefix(m mailbox.Message) string {
	return fmt.Sprintf("[agentd msg #%d from", m.ID)
}

// PasteVisible reports whether needle appears in screen, ignoring all
// whitespace (including no-break spaces and line breaks), so a notice that
// the harness wrapped or indented over several rows still matches.
func PasteVisible(screen, needle string) bool {
	return strings.Contains(stripSpace(screen), stripSpace(needle))
}

func stripSpace(s string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsSpace(r) || r == '\u00a0' {
			return -1
		}
		return r
	}, s)
}
