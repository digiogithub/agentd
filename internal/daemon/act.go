package daemon

// Op act (docs/design/api/daemon-ops.md §3): prompt, choose, keys, interrupt
// and cancel on a session's terminal. This op TYPES INTO AGENT TERMINALS, so
// every path prefers refusing with a coded error to sending keys on doubt.
//
// Decisions (normative, mirrored in AGENTS.md "Act"):
//   - Permissions are those of type: humans act on any session, a session only
//     on itself unless [security] agent_type = true.
//   - Every action serializes with the delivery loop and with other acts
//     through the per-session inject lock.
//   - prompt goes through inject.Input (paste verified, never submitted blind).
//     It needs status idle, or waiting without options (free text), unless
//     force. A pending human draft is always busy; a human keystroke within
//     human_grace_ms is busy unless force.
//   - prompt, keys, interrupt and cancel accept an optional revision (+ epoch):
//     if the interaction state (interactionFP: status, menu instance/options,
//     dialog, draft) differs from the one recorded for that revision, the
//     answer is conflict with the fresh view and nothing is sent.
//   - keys and choose refuse on a pending human draft (force does not
//     override); interrupt and cancel refuse it too unless force (they are the
//     "stop it" actions, but ctrl-c may destroy a draft).
//   - choose re-detects on a fresh snapshot, verifies id (which hashes the
//     question, kind and labels), label, epoch and revision (REQUIRED), then
//     runs the screen package's key plan step by step. The unit of identity is
//     the menu INSTANCE (fingerprint + screen context above the question +
//     transcript length/tail), not the fingerprint: an identical-looking dialog
//     for another command is another instance. Every step after the first
//     needs the same instance still on screen before its key is written, and
//     every Expect is judged against that instance. Any mismatch stops the plan
//     with conflict and no further keys. A digit in a numbered menu is sent
//     alone when the profile says menu_digit_action = "select" (never an Enter
//     afterwards); with "move" an Enter follows only if the same instance is
//     still up with the pointer on the target.
//   - API acts are human-like actions: draft-neutral keys (escape sequences,
//     Enter, Ctrl-C) are written with Session.WriteAct, which clears the
//     attention flag and holds back delivery for human_grace_ms (it updates
//     LastActInput, never LastHumanInput, so an act is not mistaken for a
//     person typing); printable keys are plain writes (a lone "y" would count
//     as a ten-minute draft). prompt is not human input (completed by its
//     submit); a pasted-but-not-submitted prompt is recorded as a draft.
//   - Never logged: prompt text. The audit line has session, action, caller,
//     option id/label, key names, outcome.

import (
	"context"
	"crypto/sha1"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/digiogithub/agentd/internal/inject"
	"github.com/digiogithub/agentd/internal/proto"
	"github.com/digiogithub/agentd/internal/screen"
	"github.com/digiogithub/agentd/internal/session"
)

// Act timings.
const (
	// actKeyGap is the pause between the keys of one request.
	actKeyGap = 30 * time.Millisecond
	// actStepTimeout bounds the wait for the screen to react to one key.
	actStepTimeout = 3 * time.Second
	// actVerifyTimeout bounds the wait for a step's Expect to hold once the
	// screen changed.
	actVerifyTimeout = time.Second
	// actQuiet is the output-quiet time before a snapshot is judged.
	actQuiet = 40 * time.Millisecond
	// actLockWait bounds the wait for the session's input lock.
	actLockWait = 10 * time.Second
	// actSettleMax bounds the best-effort wait after keys for the reply.
	actSettleMax = 250 * time.Millisecond
	// menuHistory is how many revisions the menu tracker remembers.
	menuHistory = 256
)

// ---- menu instances (used by view.go refreshLocked) ----

// menuTrack remembers, per view revision, the menu instance on screen then
// ("" = no menu; see viewState.instanceFor). choose uses it to tell "the
// revision moved but the same menu instance stayed up" (fine) from "the menu
// went away and an identical-looking one came back" (conflict). Guarded by
// viewState.mu.
type menuTrack struct {
	byRev map[uint64]string
	inter map[uint64]string
	order []uint64
}

// noteInter records the interaction fingerprint of revision rev (see
// interactionFP). Called right after note for the same revision.
func (m *menuTrack) noteInter(rev uint64, fp string) {
	if m.inter == nil {
		m.inter = map[uint64]string{}
	}
	m.inter[rev] = fp
}

// interAt returns the interaction fingerprint recorded for rev.
func (m *menuTrack) interAt(rev uint64) (string, bool) {
	i, ok := m.inter[rev]
	return i, ok
}

// interactionFP is the part of a view that decides whether a key or a prompt
// still means what the user saw: status, the menu instance and its options
// (ids, pointer, checks), dialog presence and question, and whether a human
// draft is pending. Transcript lines, title, cwd, idle time and a ghost
// suggestion appearing in the input line are deliberately NOT part of it.
func interactionFP(v proto.View, instance string) string {
	h := sha1.New()
	fmt.Fprintf(h, "%s|%s|", v.Status, instance)
	for _, o := range v.Options {
		chk := "-"
		if o.Checked != nil {
			chk = fmt.Sprint(*o.Checked)
		}
		fmt.Fprintf(h, "%s,%t,%s;", o.ID, o.Selected, chk)
	}
	if v.Dialog != nil {
		fmt.Fprintf(h, "|dlg:%s", v.Dialog.Question)
	}
	fmt.Fprintf(h, "|draft:%t", v.Input.State == screen.InputDraft)
	return fmt.Sprintf("%x", h.Sum(nil))
}

func (m *menuTrack) note(rev uint64, instance string) {
	if m.byRev == nil {
		m.byRev = map[uint64]string{}
	}
	if _, ok := m.byRev[rev]; !ok {
		m.order = append(m.order, rev)
		if len(m.order) > menuHistory {
			delete(m.byRev, m.order[0])
			delete(m.inter, m.order[0])
			m.order = m.order[1:]
		}
	}
	m.byRev[rev] = instance
}

// instanceAt returns the menu instance recorded for rev.
func (m *menuTrack) instanceAt(rev uint64) (string, bool) {
	i, ok := m.byRev[rev]
	return i, ok
}

// humanRecent reports a real human keystroke (an attached client) within the
// grace period. Keys written by acts do not count.
func humanRecent(e *entry, now time.Time) bool {
	grace := e.profile.HumanGrace()
	last := e.sess.LastHumanInput()
	return grace > 0 && !last.IsZero() && now.Sub(last) < grace
}

// ---- entry ----

func (s *Server) actOp(ctx context.Context, who caller, a proto.ActArgs) (res any, err error) {
	switch a.Action {
	case proto.ActPrompt, proto.ActChoose, proto.ActKeys, proto.ActInterrupt, proto.ActCancel:
	case "":
		return nil, proto.Errorf(proto.CodeInvalid, "act: missing action")
	default:
		return nil, proto.Errorf(proto.CodeInvalid, "act: unknown action %q", a.Action)
	}
	if a.Name == "" {
		return nil, proto.Errorf(proto.CodeInvalid, "act: missing session name")
	}
	if who.session && who.id != a.Name && !s.Cfg.Security.AgentType {
		return nil, proto.Errorf(proto.CodeForbidden, "act: session %q may not act on session %q "+
			"(set [security] agent_type = true to allow)", who.id, a.Name)
	}
	// Validate the arguments before looking at the session or sending anything.
	if a.Revision != 0 && a.Epoch == "" {
		return nil, proto.Errorf(proto.CodeInvalid, "act: epoch is required when revision is set (take both from the view)")
	}
	switch a.Action {
	case proto.ActPrompt:
		if a.Text == "" || len(a.Text) > proto.MaxActText || !utf8.ValidString(a.Text) {
			return nil, proto.Errorf(proto.CodeInvalid, "act prompt: text must be non-empty valid UTF-8 of at most %d bytes", proto.MaxActText)
		}
		if inject.DefaultNeedle(session.PasteSafe(a.Text)) == "" {
			return nil, proto.Errorf(proto.CodeInvalid, "act prompt: text is empty after removing control characters")
		}
	case proto.ActChoose:
		if a.OptionID == "" {
			return nil, proto.Errorf(proto.CodeInvalid, "act choose: missing option_id")
		}
		if a.Revision == 0 {
			return nil, proto.Errorf(proto.CodeInvalid, "act choose: revision (and epoch) of the view the option came from are required")
		}
	case proto.ActKeys:
		if _, err := resolveKeys(a.Keys, false, ""); err != nil {
			return nil, proto.Errorf(proto.CodeInvalid, "act keys: %v", err)
		}
	}
	e, err := s.lookup(a.Name)
	if err != nil {
		return nil, err
	}
	defer func() {
		attrs := []any{"session", a.Name, "action", a.Action, "caller", who.id, "via", who.via, "force", a.Force}
		switch a.Action {
		case proto.ActChoose:
			attrs = append(attrs, "option", a.OptionID, "label", a.Label, "revision", a.Revision)
		case proto.ActKeys:
			attrs = append(attrs, "keys", strings.Join(a.Keys, ","))
		case proto.ActPrompt:
			attrs = append(attrs, "text_len", len(a.Text))
		}
		if a.Client != "" {
			attrs = append(attrs, "client", a.Client)
		}
		if err != nil {
			attrs = append(attrs, "outcome", proto.CodeOf(classifyError(err)))
		} else {
			attrs = append(attrs, "outcome", "ok")
		}
		s.Log.Info("act", attrs...)
	}()
	if a.Epoch != "" && a.Epoch != s.epoch {
		return nil, s.conflict(e, who, "act: epoch %q is not this daemon's (%q): the daemon restarted; take a fresh view", a.Epoch, s.epoch)
	}

	stop := a.Action == proto.ActInterrupt || a.Action == proto.ActCancel ||
		slices.Contains(a.Keys, "ctrl-c") || slices.Contains(a.Keys, "esc")
	e.noteInput(OriginAct, stop)
	switch a.Action {
	case proto.ActPrompt:
		return s.actPrompt(ctx, e, who, a)
	case proto.ActChoose:
		return s.actChoose(ctx, e, who, a)
	default:
		return s.actKeys(ctx, e, who, a)
	}
}

// ---- shared helpers ----

// checkInteraction enforces the revision of prompt, keys, interrupt and
// cancel: when the request carries one, the interaction state (interactionFP)
// must be the same as when that revision was current. Transcript-only changes
// pass. An unknown revision (never seen, or evicted from the history) is a
// conflict, since there is nothing to compare. Nothing is sent on conflict.
func (s *Server) checkInteraction(e *entry, who caller, a proto.ActArgs) error {
	if a.Revision == 0 {
		return nil
	}
	v := e.view
	v.mu.Lock()
	v.refreshLocked(time.Now())
	rev, cur := v.rev, v.inter
	at, known := v.menu.interAt(a.Revision)
	v.mu.Unlock()
	if a.Revision == rev || (known && at == cur) {
		return nil
	}
	return s.conflict(e, who, "act %s: the interaction state changed since revision %d (current %d); review the fresh view and act again", a.Action, a.Revision, rev)
}

// aliveErr maps a session that cannot take input to its coded error.
func aliveErr(e *entry) error {
	if e.sess.State() != session.StateRunning {
		return proto.Errorf(proto.CodeExited, "session %q has exited", e.sess.Name())
	}
	return nil
}

// actState is what the act preconditions read from a freshly refreshed view.
type actState struct {
	status    string
	rev       uint64
	options   int
	inputText string
	ghost     string
	opts      []screen.Option
	instance  string
	appCursor bool
}

// current refreshes the view and returns what the preconditions need.
func (e *entry) current() actState {
	v := e.view
	v.mu.Lock()
	defer v.mu.Unlock()
	v.refreshLocked(time.Now())
	return actState{
		status: v.cur.Status, rev: v.rev, options: len(v.cur.Options),
		inputText: v.cur.Input.Text, ghost: v.cur.Input.Ghost,
		opts: slices.Clone(v.opts), instance: v.instance, appCursor: v.appCursor,
	}
}

// draftBusy is the busy error for a pending human draft.
func draftBusy(e *entry, now time.Time) error {
	if inject.DraftPending(e.sess, now) {
		return proto.Errorf(proto.CodeBusy, "session %q has a pending draft in its input line (typed by a human or pasted and not submitted); not acting", e.sess.Name())
	}
	return nil
}

func lockInput(ctx context.Context, e *entry) (func(), error) {
	lctx, cancel := context.WithTimeout(ctx, actLockWait)
	defer cancel()
	unlock, err := inject.Lock(lctx, e.sess)
	if err != nil {
		return nil, proto.Errorf(proto.CodeBusy, "session %q is busy with another input operation", e.sess.Name())
	}
	return unlock, nil
}

// writeKeys writes the resolved keys actKeyGap apart. Caller holds the lock.
// Human-flagged keys (draft-neutral) go through WriteAct.
func writeKeys(ctx context.Context, e *entry, ks []keySeq) error {
	for i, k := range ks {
		if i > 0 {
			if err := sleepCtx(ctx, actKeyGap); err != nil {
				return err
			}
		}
		var err error
		if k.human {
			_, err = e.sess.WriteAct(k.bytes)
		} else {
			_, err = e.sess.Write(k.bytes, false)
		}
		if err != nil {
			return err
		}
	}
	return nil
}

func sleepCtx(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// settle waits (best effort, bounded) for the session to react after gen0 and
// go quiet, then reports the view state.
func settle(e *entry, gen0 uint64) proto.ActResult {
	deadline := time.Now().Add(actSettleMax)
	for e.sess.OutputGen() == gen0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	waitQuiet(e.sess)
	st := e.current()
	return proto.ActResult{Revision: st.rev, Status: st.status}
}

// waitQuiet returns once no output was processed for actQuiet (at most 500 ms).
func waitQuiet(sess *session.Session) {
	end := time.Now().Add(500 * time.Millisecond)
	for time.Now().Before(end) {
		g := sess.OutputGen()
		time.Sleep(actQuiet)
		if sess.OutputGen() == g {
			return
		}
	}
}

// ---- prompt ----

func (s *Server) actPrompt(ctx context.Context, e *entry, who caller, a proto.ActArgs) (any, error) {
	if err := aliveErr(e); err != nil {
		return nil, err
	}
	submit := a.Submit == nil || *a.Submit
	gen0 := e.sess.OutputGen()
	guardOptions := 0
	res, err := inject.Input(ctx, e.sess, e.profile, inject.Request{
		Text: a.Text, Submit: submit,
		// Verify the paste in the input region too (screen.Split), not only
		// by a growing occurrence count on the whole screen.
		InputRegion: func() string {
			snap := e.sess.Snapshot(screen.SnapOpts{})
			if reg := screen.Split(snap, e.view.rules); reg.HasInput {
				return reg.Input.Text
			}
			return ""
		},
		Guard: func() error {
			if err := aliveErr(e); err != nil {
				return err
			}
			if err := s.checkInteraction(e, who, a); err != nil {
				return err
			}
			now := time.Now()
			if err := draftBusy(e, now); err != nil {
				return err
			}
			if !a.Force && humanRecent(e, now) {
				return proto.Errorf(proto.CodeBusy, "session %q: a human typed within the last %s; not acting (force overrides)", e.sess.Name(), e.profile.HumanGrace())
			}
			st := e.current()
			guardOptions = st.options
			// Never paste over text that is already in the input line (force
			// does not override: it would be submitted together with ours).
			if strings.TrimSpace(st.inputText) != "" {
				return proto.Errorf(proto.CodeBusy, "session %q: the input line is not empty; not acting", e.sess.Name())
			}
			if a.Force {
				return nil
			}
			switch {
			case st.status == string(screen.StatusIdle):
			case st.status == string(screen.StatusWaiting) && st.options == 0:
			case st.status == string(screen.StatusStarting):
				return proto.Errorf(proto.CodeNotReady, "session %q is still starting", e.sess.Name())
			default:
				return proto.Errorf(proto.CodeNotReady, "session %q is %s (a prompt needs idle or a free-text question; force overrides)", e.sess.Name(), st.status)
			}
			return nil
		},
		// The paste changes the screen and time passes: before the Enter make
		// sure no dialog took the screen and nobody started typing. The text
		// stays pasted (recorded as a draft by inject).
		BeforeSubmit: func() error {
			if err := aliveErr(e); err != nil {
				return err
			}
			now := time.Now()
			if err := draftBusy(e, now); err != nil {
				return s.conflict(e, who, "act prompt: a draft appeared before the submit; Enter not sent (the text stays pasted)")
			}
			if !a.Force && humanRecent(e, now) {
				return s.conflict(e, who, "act prompt: a human typed before the submit; Enter not sent (the text stays pasted)")
			}
			st := e.current()
			if st.options > guardOptions {
				return s.conflict(e, who, "act prompt: a menu appeared before the submit; Enter not sent (the text stays pasted)")
			}
			if st.status == string(screen.StatusExited) {
				return proto.Errorf(proto.CodeExited, "session %q has exited", e.sess.Name())
			}
			return nil
		},
	})
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
			return nil, proto.WithCode(proto.CodeBusy, fmt.Errorf("act prompt: input busy: %w", err))
		}
		return nil, err
	}
	out := settle(e, gen0)
	out.Result, out.Submitted = "pasted", res.Submitted
	if res.Submitted {
		out.Result = "submitted"
	}
	if res.HumanRaced {
		out.Warning = "human typed during the submit delay; submitted anyway"
	}
	return out, nil
}

// ---- keys, interrupt, cancel ----

// inputKeys are keys that put text into the input line (and so erase a ghost
// suggestion) before a following Enter.
var inputKeys = map[string]bool{"space": true}

func init() {
	for d := '0'; d <= '9'; d++ {
		inputKeys[string(d)] = true
	}
	for c := 'a'; c <= 'z'; c++ {
		inputKeys[string(c)] = true
	}
}

func (s *Server) actKeys(ctx context.Context, e *entry, who caller, a proto.ActArgs) (any, error) {
	names, result := a.Keys, "keys_sent"
	switch a.Action {
	case proto.ActInterrupt:
		names, result = e.profile.Act.InterruptKeys, "interrupted"
	case proto.ActCancel:
		names, result = e.profile.Act.CancelKeys, "cancelled"
	}
	unlock, err := lockInput(ctx, e)
	if err != nil {
		return nil, err
	}
	defer unlock()
	if err := aliveErr(e); err != nil {
		return nil, err
	}
	if err := s.checkInteraction(e, who, a); err != nil {
		return nil, err
	}
	now := time.Now()
	// Draft: keys always; interrupt/cancel unless forced.
	if a.Action == proto.ActKeys || !a.Force {
		if err := draftBusy(e, now); err != nil {
			return nil, err
		}
	}
	st := e.current()
	stopKey := a.Action == proto.ActKeys && (slices.Contains(names, "ctrl-c") || slices.Contains(names, "ctrl-d"))
	if (a.Action == proto.ActInterrupt || stopKey) && !a.Force {
		if st.status != string(screen.StatusWorking) && st.status != string(screen.StatusWaiting) {
			return nil, proto.Errorf(proto.CodeNotReady, "session %q is %s: an interrupt (ctrl-c/ctrl-d) is for a working or waiting session (it could quit the harness otherwise; force overrides)", e.sess.Name(), st.status)
		}
	}
	// Enter on an empty input line while the harness shows a suggested prompt
	// would submit that suggestion. Never overridden by force.
	if a.Action == proto.ActKeys && st.options == 0 && strings.TrimSpace(st.inputText) == "" && st.ghost != "" {
		for _, n := range names {
			if inputKeys[n] {
				break // typed text erases the suggestion
			}
			if n == "enter" {
				return nil, proto.Errorf(proto.CodeNotReady, "session %q: Enter on an empty input line would submit the suggested prompt; not sent", e.sess.Name())
			}
		}
	}
	ks, err := resolveKeys(names, st.appCursor, e.profile.Submit)
	if err != nil {
		return nil, proto.Errorf(proto.CodeInvalid, "act %s: %v", a.Action, err)
	}
	gen0 := e.sess.OutputGen()
	if err := writeKeys(ctx, e, ks); err != nil {
		return nil, err
	}
	out := settle(e, gen0)
	out.Result = result
	return out, nil
}

// ---- choose ----

// detectOptions runs the view's option detection on snap.
func (v *viewState) detectOptions(snap *screen.Snapshot) []screen.Option {
	reg := screen.Split(snap, v.rules)
	if reg.Dialog != nil && len(reg.Dialog.Options) > 0 {
		return reg.Dialog.Options
	}
	inputRow := -1
	if reg.HasInput {
		inputRow = reg.Input.Row
	}
	return screen.DetectOptions(snap, inputRow, v.rules.Menu)
}

// instanceNow is the menu instance of opts as detected on snap.
func (v *viewState) instanceNow(snap *screen.Snapshot, opts []screen.Option) string {
	return v.instanceFor(opts, screen.MenuContext(snap, opts))
}

func screenKey(snap *screen.Snapshot) [20]byte {
	return sha1.Sum([]byte(strings.Join(snap.Lines(), "\n") + fmt.Sprintf("\x00%d,%d", snap.Cursor.X, snap.Cursor.Y)))
}

// conflict builds a conflict error carrying the fresh view, as the caller
// would see it: a caller that may not view the session gets no view.
func (s *Server) conflict(e *entry, who caller, format string, args ...any) error {
	err := proto.Errorf(proto.CodeConflict, format, args...)
	if v, verr := s.viewOp(who, proto.ViewArgs{Name: e.sess.Name(), LinesLimit: 100}); verr == nil {
		if view, ok := v.(proto.View); ok {
			var ce *proto.CodedError
			if errors.As(err, &ce) {
				ce.Data = proto.ActConflict{Revision: view.Revision, View: view}
			}
		}
	}
	return err
}

// sameMenu returns the options of opts that belong to the menu with
// fingerprint fp AND instance wantInst (gotInst is the instance of opts; an
// empty wantInst only checks the fingerprint).
func sameMenu(opts []screen.Option, fp, gotInst, wantInst string) []screen.Option {
	if wantInst != "" && gotInst != wantInst {
		return nil
	}
	var out []screen.Option
	for _, o := range opts {
		if o.MenuFingerprint == fp {
			out = append(out, o)
		}
	}
	return out
}

// satisfied reports whether exp holds on opts (post-key detection, instance
// inst). A SelectedID / Checked check is evaluated only against the menu with
// exp's fingerprint and instance; Gone means no such menu is on screen (an
// exited session has no pending menu either).
func satisfied(exp screen.Expect, opts []screen.Option, inst string, exited bool) bool {
	if exp.SelectedID == "" && !exp.Gone && exp.Checked == nil {
		return true
	}
	same := sameMenu(opts, exp.Fingerprint, inst, exp.Instance)
	if exp.Gone && (len(same) == 0 || exited) {
		return true
	}
	for _, o := range same {
		if exp.SelectedID != "" && o.ID == exp.SelectedID && o.Selected {
			if exp.Checked != nil {
				return o.Checked != nil && *o.Checked == *exp.Checked
			}
			return true
		}
	}
	return false
}

func (s *Server) actChoose(ctx context.Context, e *entry, who caller, a proto.ActArgs) (any, error) {
	unlock, err := lockInput(ctx, e)
	if err != nil {
		return nil, err
	}
	defer unlock()
	if err := aliveErr(e); err != nil {
		return nil, err
	}
	if err := draftBusy(e, time.Now()); err != nil {
		return nil, err
	}
	if !a.Force && humanRecent(e, time.Now()) {
		return nil, proto.Errorf(proto.CodeBusy, "session %q: a human typed within the last %s; not acting (force overrides)", e.sess.Name(), e.profile.HumanGrace())
	}
	v := e.view

	// Fresh state, forced now (never the debounced watcher): options, menu
	// instance and the instance recorded at the client's revision all come
	// from one refresh under the view lock.
	v.mu.Lock()
	v.refreshLocked(time.Now())
	status, rev, curInst := v.cur.Status, v.rev, v.instance
	revInst, revKnown := v.menu.instanceAt(a.Revision)
	opts, appCursor := slices.Clone(v.opts), v.appCursor
	v.mu.Unlock()

	var target *screen.Option
	for i := range opts {
		if opts[i].ID == a.OptionID {
			target = &opts[i]
		}
	}
	if target == nil {
		return nil, s.conflict(e, who, "act choose: option %s is gone (the menu changed)", a.OptionID)
	}
	if a.Label != "" && strings.TrimSpace(a.Label) != strings.TrimSpace(target.Label) {
		return nil, s.conflict(e, who, "act choose: option %s is now labelled %q, not %q", a.OptionID, target.Label, a.Label)
	}
	// The option id hashes question, kind and labels; the instance adds the
	// context and the history. The client's revision must be the current one,
	// or the very same menu instance must have been on screen since it: a menu
	// that went away and an identical-looking one that came back is another
	// instance.
	if a.Revision != rev && (!revKnown || revInst == "" || revInst != curInst) {
		return nil, s.conflict(e, who, "act choose: revision %d is stale and the menu was replaced since (current %d)", a.Revision, rev)
	}
	if !a.Force && status != string(screen.StatusWaiting) && status != string(screen.StatusIdle) {
		return nil, proto.Errorf(proto.CodeNotReady, "session %q is %s (choose needs a waiting or idle session with options; force overrides)", e.sess.Name(), status)
	}
	plan := screen.PlanChooseKeys(opts, *target, appCursor, chooseKeys(e.profile))
	if len(plan) == 0 {
		return nil, proto.Errorf(proto.CodeInvalid, "act choose: option %s cannot be selected with keys (no known selection or key)", a.OptionID)
	}
	fp := target.MenuFingerprint
	for i := range plan {
		plan[i].Expect.Instance = curInst
	}

	cur, inst := opts, curInst // the last verified detection
	exited := false
	for i, st := range plan {
		if i > 0 {
			// Every later step needs the same menu instance still on screen.
			same := sameMenu(cur, fp, inst, curInst)
			switch {
			case exited || len(same) == 0:
				if st.IfNotGone {
					goto done // the previous key already chose
				}
				return nil, s.conflict(e, who, "act choose: the menu is gone or was replaced after step %d/%d; stopped, no further keys", i, len(plan))
			case st.IfNotGone:
				prev := plan[i-1].Expect.SelectedID
				onTarget := false
				for _, o := range same {
					if o.ID == prev && o.Selected {
						onTarget = true
					}
				}
				if !onTarget {
					return nil, s.conflict(e, who, "act choose: pointer is not on %s any more; Enter not sent", prev)
				}
			}
		}
		{
			ks := []keySeq{{name: "plan", bytes: []byte(st.Keys), human: planHuman(st.Keys)}}
			before, gen0 := screenKey(e.sess.Snapshot(screen.SnapOpts{})), e.sess.OutputGen()
			if err := writeKeys(ctx, e, ks); err != nil {
				return nil, err
			}
			snap, err := awaitChange(ctx, e.sess, gen0, before)
			if err != nil {
				return nil, err
			}
			// Judge the step: poll until Expect holds on the same instance or
			// time runs out.
			end := time.Now().Add(actVerifyTimeout)
			for {
				cur = v.detectOptions(snap)
				inst = v.instanceNow(snap, cur)
				exited = snap.State == screen.StateExited
				if satisfied(st.Expect, cur, inst, exited) {
					break
				}
				if time.Now().After(end) {
					return nil, s.conflict(e, who, "act choose: step %d/%d did not produce the expected state (menu changed or pointer elsewhere); stopped, no further keys", i+1, len(plan))
				}
				ch := e.sess.Changed()
				select {
				case <-ch:
				case <-e.sess.Done():
				case <-time.After(25 * time.Millisecond):
				case <-ctx.Done():
					return nil, ctx.Err()
				}
				waitQuiet(e.sess)
				snap = e.sess.Snapshot(screen.SnapOpts{ScrollbackTail: -1})
			}
		}
	}
done:
	out := settle(e, e.sess.OutputGen())
	out.Result = "selected"
	if otherMenu(cur, fp, inst, curInst) {
		out.Warning = "another menu is on screen now; no keys were sent to it"
	}
	return out, nil
}

// otherMenu reports a menu on screen that is not the chosen instance.
func otherMenu(opts []screen.Option, fp, inst, wantInst string) bool {
	for _, o := range opts {
		if o.MenuFingerprint != fp || inst != wantInst {
			return true
		}
	}
	return false
}

// planHuman reports whether a plan step's bytes may count as input activity
// (arrow escape sequences, Enter; digits and letters may not).
func planHuman(keys string) bool {
	return keys == "\r" || keys == "\n" || strings.HasPrefix(keys, "\x1b")
}

// awaitChange waits until output past gen0 was processed, went quiet and the
// screen differs from before. It returns the snapshot. A session that exited
// counts as changed.
func awaitChange(ctx context.Context, sess *session.Session, gen0 uint64, before [20]byte) (*screen.Snapshot, error) {
	timeout := time.NewTimer(actStepTimeout)
	defer timeout.Stop()
	for {
		ch := sess.Changed()
		if g := sess.OutputGen(); g > gen0 {
			waitQuiet(sess)
			snap := sess.Snapshot(screen.SnapOpts{ScrollbackTail: -1})
			if snap.State == screen.StateExited || screenKey(snap) != before {
				return snap, nil
			}
			gen0 = snap.OutputSeq
			continue
		}
		select {
		case <-ch:
		case <-sess.Done():
			return sess.Snapshot(screen.SnapOpts{ScrollbackTail: -1}), nil
		case <-timeout.C:
			return nil, proto.Errorf(proto.CodeTimeout, "act choose: the screen did not change after the key was sent")
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
}
