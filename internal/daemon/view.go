package daemon

// Op view: the semantic view of a session (docs/design/api/daemon-ops.md §2).
//
// Each registered session owns a viewState: the transcript History, the
// attention tracker, the interpretation of the last snapshot and the cached
// View with its revision. A per-session watcher goroutine refreshes it when
// output arrives (debounced) and on timers, so that silent transitions such
// as working -> idle are noticed. The revision bumps only when the published
// fingerprint (status, attention, input, options, line tail...) changes.

import (
	"crypto/sha1"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/digiogithub/agentd/internal/delivery"
	"github.com/digiogithub/agentd/internal/proto"
	"github.com/digiogithub/agentd/internal/screen"
	"github.com/digiogithub/agentd/internal/session"
)

// View limits and timings.
const (
	DefaultViewLines = 500
	MaxViewLines     = 10000
	// MaxPreview is the length cap (runes) of the ls preview line.
	MaxPreview = 160
	// viewDebounce coalesces bursts of output before the watcher recomputes.
	viewDebounce = 100 * time.Millisecond
	// viewSyncDebounce is the debounce for children that use synchronized
	// updates (complete frames): short, so history sampling keeps up with
	// fast scrolling (see screen.SyncSettleMax).
	viewSyncDebounce = 40 * time.Millisecond
	// viewMaxWake is the longest the watcher sleeps without a deadline.
	viewMaxWake = time.Second
	// fpTailLines is how many newest transcript lines feed the fingerprint.
	fpTailLines = 20
)

// viewState is the per-session view machinery. mu guards everything below it;
// hist has its own lock.
type viewState struct {
	srv  *Server
	e    *entry
	name string

	rules screen.Rules
	hist  *screen.History

	stop     chan struct{}
	stopOnce sync.Once
	done     chan struct{}

	mu       sync.Mutex
	att      screen.Attention
	have     bool // reg/opts valid
	gen      uint64
	cols     int
	rows     int
	reg      screen.Regions
	opts     []screen.Option
	hints    []screen.Hint
	rev      uint64
	fp       [32]byte
	cur      proto.View // published view without lines
	preview  string
	nextWake time.Duration

	// Last values published on the event bus (publishChangesLocked).
	pubInit    bool
	pubStatus  string
	pubAtt     bool
	pubInput   string
	pubRev     uint64
	turn       uint64                 // times the published status left working
	prog       screen.ProgramStatuses // last OSC 7501 reports seen
	pubProgGen uint64

	// Menu instances for op act (act.go). menuCtx is the screen-context hash
	// of the menu (screen.MenuContext), instance the identity published by
	// instanceFor; both "" while no menu is on screen.
	menu      menuTrack
	menuCtx   string
	instance  string
	appCursor bool
	// inter is the interaction fingerprint of the current view (interactionFP).
	inter string
}

func newViewState(s *Server, e *entry, name string) *viewState {
	return &viewState{
		srv: s, e: e, name: name,
		rules: screenRules(e.profile),
		hist:  screen.NewHistory(screen.HistoryOpts{Transient: transientFn(screenRules(e.profile))}),
		stop:  make(chan struct{}),
		done:  make(chan struct{}),
	}
}

// start launches the watcher goroutine.
func (v *viewState) start() { go v.watch() }

// shutdown stops the watcher and waits for it.
func (v *viewState) shutdown() {
	v.stopOnce.Do(func() { close(v.stop) })
	<-v.done
}

// watch refreshes the view when output arrives and when a status deadline
// passes; it ends when the session exits (after a final refresh) or on stop.
func (v *viewState) watch() {
	defer close(v.done)
	sess := v.e.sess
	for {
		ch := sess.Changed()
		wake := v.refresh()
		t := time.NewTimer(wake)
		select {
		case <-ch:
			t.Stop()
			debounce := viewDebounce
			if sess.SyncFrames() > 0 {
				debounce = viewSyncDebounce
			}
			select { // debounce: let a burst finish
			case <-time.After(debounce):
			case <-sess.Done():
			case <-v.stop:
				return
			}
		case <-t.C:
		case <-sess.Done():
			t.Stop()
			v.refresh() // exited: final screen, final observe
			return
		case <-v.stop:
			t.Stop()
			return
		}
	}
}

// refresh recomputes the view now and returns how long the watcher may sleep.
func (v *viewState) refresh() time.Duration {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.refreshLocked(time.Now())
	return v.nextWake
}

func (v *viewState) refreshLocked(now time.Time) {
	sess, p := v.e.sess, v.e.profile
	snap := sess.Snapshot(screen.SnapOpts{ScrollbackTail: -1})

	// Expensive part: region split and option detection, only on new output
	// or a resize.
	if !v.have || snap.OutputSeq != v.gen || snap.Cols != v.cols || snap.Rows != v.rows {
		v.reg = screen.Split(snap, v.rules)
		inputRow := -1
		if v.reg.HasInput {
			inputRow = v.reg.Input.Row
		}
		if v.reg.Dialog != nil && len(v.reg.Dialog.Options) > 0 {
			v.opts = v.reg.Dialog.Options
		} else {
			v.opts = screen.DetectOptions(snap, inputRow, v.rules.Menu)
		}
		v.hints = screen.DetectHints(snap, inputRow, v.rules.Menu)
		v.menuCtx = screen.MenuContext(snap, v.opts)
		v.gen, v.cols, v.rows, v.have = snap.OutputSeq, snap.Cols, snap.Rows, true
	}

	v.observeLocked(snap, now)

	v.appCursor = snap.AppCursor
	v.instance = v.instanceFor(v.opts, v.menuCtx)

	promptMatch := true
	if p.Prompt != nil {
		scr, _ := sess.ScreenFiltered(delivery.GhostOpts(p))
		promptMatch = p.PromptMatches(scr)
	}
	sr := screen.Classify(snap, v.reg, v.opts, statusInput(p, v.rules, now, promptMatch))
	// A program that reports its status (OSC 7501) is believed over the
	// screen heuristics, also while starting (an exit stays an exit).
	v.prog = sess.ProgramStatus()
	if v.prog.Root != nil && sr.Status != screen.StatusExited {
		sr.Status = screen.StatusFromProgram(v.prog.Root.State)
		sr.Reason = "reported:" + v.prog.Root.State
		sr.Confidence = 1
	}
	v.att = screen.Next(v.att, sr)

	view := proto.View{
		Name: v.name, Epoch: v.srv.epoch,
		Status: string(sr.Status), StatusReason: sr.Reason,
		Attention: proto.ViewAttention{Set: v.att.Set, Reason: v.att.Reason, Since: v.att.Since},
		Title:     snap.Title, Cwd: snap.Cwd,
		Input:    proto.ViewInput{State: screen.InputEmpty},
		Options:  make([]proto.ViewOption, 0, len(v.opts)),
		Hints:    make([]proto.ViewHint, 0, len(v.hints)),
		ExitCode: snap.ExitCode,
	}
	if v.reg.HasInput {
		view.Input.Text, view.Input.Ghost = v.reg.Input.Text, v.reg.Input.Ghost
		if view.Input.Ghost != "" {
			view.Input.State = screen.InputSuggestion
		}
	}
	if !snap.DraftSince.IsZero() && now.Sub(snap.DraftSince) < screen.DraftMaxAge {
		view.Input.State = screen.InputDraft
	}
	for _, o := range v.opts {
		view.Options = append(view.Options, proto.ViewOption{
			ID: o.ID, Label: o.Label, Index: o.Index, Kind: o.Kind, Selected: o.Selected,
			Checked: o.Checked, Confidence: o.Confidence,
		})
	}
	for _, h := range v.hints {
		view.Hints = append(view.Hints, proto.ViewHint{Key: h.Key, Action: h.Action})
	}
	if v.reg.Dialog != nil {
		view.Dialog = &proto.ViewDialog{Question: v.reg.Dialog.Question}
	}
	last := snap.LastOutput
	if last.IsZero() {
		last = snap.StartedAt
	}
	view.IdleMS = now.Sub(last).Milliseconds()

	tail, _ := v.tail(fpTailLines)
	v.preview = previewOf(tail)
	fp := fingerprint(view, tail, v.hist.Len(), v.rules.SpinnerChars)
	if v.rev == 0 || fp != v.fp {
		v.rev++
		v.fp = fp
	}
	view.Revision = v.rev
	view.FirstLine = v.hist.First()
	v.cur = view
	v.inter = interactionFP(view, v.instance)
	v.menu.note(v.rev, v.instance)
	v.menu.noteInter(v.rev, v.inter)
	v.publishChangesLocked()
	v.nextWake = v.wakeIn(snap, now)
}

// observeLocked feeds the transcript history from the current frame
// (AGED-US-0036). Never inside an open synchronized update (DEC 2026): the
// screen is half drawn. A child that uses 2026 always shows a complete frame
// otherwise, so the usual settle policy samples complete frames only. Without
// 2026 a sample taken before the output went quiet (SettleMax) is forced:
// transient rows are filtered by the History and a frame that does not align
// is deferred to the next settle rather than recorded as a gap.
func (v *viewState) observeLocked(snap *screen.Snapshot, now time.Time) {
	if snap.State == screen.StateExited {
		v.hist.Observe(v.reg, snap.OutputSeq, now)
		return
	}
	should := screen.ShouldObserve
	if snap.SyncFrames > 0 {
		should = screen.ShouldObserveSync
	}
	if snap.SyncOpen || !should(snap.LastOutput, v.hist.LastObserved(), now) {
		return
	}
	if snap.SyncFrames == 0 && now.Sub(snap.LastOutput) < screen.SettleQuiet {
		v.hist.ObserveForced(v.reg, snap.OutputSeq, now)
		return
	}
	v.hist.Observe(v.reg, snap.OutputSeq, now)
}

// transientFn builds the History filter for status rows of a profile: lines
// matching busy_regex or starting with a spinner_chars glyph.
func transientFn(r screen.Rules) func(string) bool {
	if r.BusyRegex == nil && r.SpinnerChars == "" {
		return nil
	}
	return func(t string) bool {
		t = strings.TrimSpace(t)
		if r.BusyRegex != nil && r.BusyRegex.MatchString(t) {
			return true
		}
		for _, c := range t {
			return strings.ContainsRune(r.SpinnerChars, c)
		}
		return false
	}
}

// tail returns the newest n history lines and the absolute index of the first.
func (v *viewState) tail(n int) ([]screen.Line, int) {
	from := max(v.hist.First(), v.hist.Len()-n)
	return v.hist.Lines(from, 0)
}

// wakeIn is the time to the next moment the status or the transcript can
// change without output: transcript settling, quiet-time thresholds, end of
// boot. Capped at viewMaxWake (also covers human-keystroke attention clears).
func (v *viewState) wakeIn(snap *screen.Snapshot, now time.Time) time.Duration {
	p := v.e.profile
	wake := viewMaxWake
	consider := func(at time.Time) {
		if d := at.Sub(now); d > 0 && d < wake {
			wake = d
		}
	}
	if !snap.LastOutput.IsZero() {
		if snap.LastOutput.After(v.hist.LastObserved()) {
			consider(snap.LastOutput.Add(screen.SettleQuiet))
		}
		consider(snap.LastOutput.Add(p.Idle()))
		consider(snap.LastOutput.Add(min(p.Idle(), screen.WaitingQuiet)))
		consider(snap.LastOutput.Add(screen.EchoWindow))
	}
	if snap.SyncOpen {
		consider(snap.SyncSince.Add(session.SyncTimeout))
	}
	consider(snap.StartedAt.Add(max(p.Idle(), screen.MinBoot)))
	return max(wake+time.Millisecond, 10*time.Millisecond)
}

func previewOf(lines []screen.Line) string {
	for i := len(lines) - 1; i >= 0; i-- {
		l := lines[i]
		if l.Role != screen.RoleTranscript {
			continue
		}
		if t := strings.TrimSpace(screen.Sanitize(l.Text, MaxPreview*4)); t != "" {
			return screen.Sanitize(t, MaxPreview)
		}
	}
	return ""
}

// titleSpinners are glyphs that animate a terminal title ("✳ topic"); they
// are dropped from the front of the title before fingerprinting.
const titleSpinners = "✳✶✻✽✢✺·•●*"

// stableTitle is title without a leading spinner glyph (braille dots, the
// profile's spinner_chars and the usual star set) and surrounding blanks, so
// a title that only animates its spinner does not bump the revision.
func stableTitle(title, spinners string) string {
	return strings.TrimLeftFunc(title, func(r rune) bool {
		return unicode.IsSpace(r) || (r >= 0x2800 && r <= 0x28FF) ||
			strings.ContainsRune(titleSpinners, r) || strings.ContainsRune(spinners, r)
	})
}

// instanceFor is the identity of the menu instance on screen: the menu
// fingerprint, the screen-context hash (rows above the question) and the
// length and tail of the transcript history. "" when there are no options.
// An identical-looking dialog shown later (another command, same question and
// labels) gets a different instance. Safe without v.mu (hist has its own).
func (v *viewState) instanceFor(opts []screen.Option, ctx string) string {
	if len(opts) == 0 {
		return ""
	}
	tail, _ := v.tail(3)
	h := sha1.New()
	fmt.Fprintf(h, "%s\x00%s\x00%d", opts[0].MenuFingerprint, ctx, v.hist.Len())
	for _, l := range tail {
		fmt.Fprintf(h, "\x00%s", l.Text)
	}
	return hex.EncodeToString(h.Sum(nil))[:16]
}

func fingerprint(v proto.View, tail []screen.Line, total int, spinners string) [32]byte {
	v.Title = stableTitle(v.Title, spinners)
	type fpLine struct{ T, R string }
	fl := make([]fpLine, len(tail))
	for i, l := range tail {
		fl[i] = fpLine{l.Text, string(l.Role)}
	}
	b, _ := json.Marshal(struct {
		Status, Reason, AttReason string
		AttSet                    bool
		Input                     proto.ViewInput
		Options                   []proto.ViewOption
		Hints                     []proto.ViewHint
		Dialog                    *proto.ViewDialog
		Title, Cwd                string
		Exit                      *int
		Total                     int
		Tail                      []fpLine
	}{v.Status, v.StatusReason, v.Attention.Reason, v.Attention.Set, v.Input, v.Options, v.Hints,
		v.Dialog, v.Title, v.Cwd, v.ExitCode, total, fl})
	return sha256.Sum256(b)
}

// summary returns the cached view fields shown by ls, refreshing once when
// the watcher has not computed anything yet.
func (v *viewState) summary() (status string, attention bool, rev uint64, preview string, turn uint64) {
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.rev == 0 {
		v.refreshLocked(time.Now())
	}
	return v.cur.Status, v.cur.Attention.Set, v.rev, v.preview, v.turn
}

// ack clears the attention flag and returns the resulting revision.
func (v *viewState) ack() uint64 {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.refreshLocked(time.Now())
	v.att = v.att.Ack()
	// Republish with the cleared flag (bumps the revision when it was set).
	v.refreshAfterAck()
	return v.rev
}

// refreshAfterAck recomputes the published view without advancing the
// attention tracker a second time on the same classification edge.
func (v *viewState) refreshAfterAck() {
	view := v.cur
	view.Attention = proto.ViewAttention{}
	tail, _ := v.tail(fpTailLines)
	fp := fingerprint(view, tail, v.hist.Len(), v.rules.SpinnerChars)
	if fp != v.fp {
		v.rev++
		v.fp = fp
	}
	view.Revision = v.rev
	v.cur = view
	v.publishChangesLocked()
}

// publishChangesLocked emits bus events for what changed in the published
// view since the last call: status, attention, input state and revision (in
// that order, view.changed last so a consumer refetching sees everything).
// One event per published change, never per output byte. The first call is
// the baseline: it publishes the initial status and revision only.
func (v *viewState) publishChangesLocked() {
	cur, bus := v.cur, v.srv
	if v.prog.Gen != v.pubProgGen {
		root, recs := programStatus(v.prog)
		bus.publish(proto.EvStatusReported, v.name, proto.ProgramStatusData{Status: root, Records: recs, Reported: v.prog.Reported})
		v.pubProgGen = v.prog.Gen
	}
	if cur.Status != v.pubStatus {
		// A turn ends each time the published status leaves working.
		now := time.Now()
		if cur.Status == "working" {
			v.e.turnStarted(now)
		}
		if v.pubStatus == "working" {
			v.turn++
			rec := v.e.turnEnded(v.turn, cur.Status, v.preview, now)
			defer bus.publish(proto.EvTurnCompleted, v.name, rec) // after status.changed
		}
		bus.publish(proto.EvStatusChanged, v.name, proto.StatusChangedData{
			From: v.pubStatus, Status: cur.Status, Reason: cur.StatusReason,
			Attention: cur.Attention.Set, Revision: cur.Revision, Turn: v.turn,
		})
	}
	if v.pubInit && cur.Attention.Set != v.pubAtt {
		bus.publish(proto.EvAttentionChanged, v.name, proto.AttentionChangedData{
			Attention: cur.Attention.Set, Reason: cur.Attention.Reason,
		})
	}
	if v.pubInit && cur.Input.State != v.pubInput {
		bus.publish(proto.EvInputChanged, v.name, proto.InputChangedData{State: cur.Input.State})
	}
	if cur.Revision != v.pubRev {
		bus.publish(proto.EvViewChanged, v.name, proto.ViewChangedData{Revision: cur.Revision})
	}
	v.pubInit, v.pubStatus, v.pubAtt, v.pubInput, v.pubRev = true, cur.Status, cur.Attention.Set, cur.Input.State, cur.Revision
}

// viewOp answers op view for caller who.
func (s *Server) viewOp(who caller, a proto.ViewArgs) (any, error) {
	if who.session && who.id != a.Name && !s.Cfg.Security.AgentView {
		return nil, proto.Errorf(proto.CodeForbidden, "view: session %q may not view session %q "+
			"(set [security] agent_view = true to allow)", who.id, a.Name)
	}
	// A session may view a peer only if [acl] lets it send to that peer.
	if who.session && who.id != a.Name && !s.Cfg.Allowed(a.Name, who.id) {
		return nil, proto.Errorf(proto.CodeForbidden, "view: session %q may not view session %q "+
			"(denied by [acl])", who.id, a.Name)
	}
	if a.LinesLimit < 0 || a.LinesLimit > MaxViewLines {
		return nil, proto.Errorf(proto.CodeInvalid, "view: lines_limit must be between 0 and %d", MaxViewLines)
	}
	if a.FromLine != nil && *a.FromLine < 0 {
		return nil, proto.Errorf(proto.CodeInvalid, "view: from_line must not be negative")
	}
	e, err := s.lookup(a.Name)
	if err != nil {
		return nil, err
	}
	v := e.view
	limit := a.LinesLimit
	if limit == 0 {
		limit = DefaultViewLines
		if a.Scrollback {
			limit = MaxViewLines
		}
	}

	v.mu.Lock()
	v.refreshLocked(time.Now())
	// not_modified only for a client that proves it knows this epoch: a
	// revision number alone means nothing across daemon restarts.
	if a.SinceRevision != 0 && a.SinceRevision == v.rev && a.Epoch == s.epoch {
		res := proto.ViewNotModified{NotModified: true, Epoch: s.epoch, Revision: v.rev}
		v.mu.Unlock()
		return res, nil
	}
	out := v.cur
	var lines []screen.Line
	if a.FromLine != nil {
		lines, out.FirstLine = v.hist.Lines(*a.FromLine, limit)
	} else {
		from := max(v.hist.First(), v.hist.Len()-limit)
		lines, out.FirstLine = v.hist.Lines(from, limit)
	}
	v.mu.Unlock()

	if a.Scrollback && a.FromLine == nil {
		snap := e.sess.Snapshot(screen.SnapOpts{ScrollbackTail: MaxViewLines})
		if !snap.AltScreen {
			tr := screen.Split(snap, v.rules).Transcript
			if len(tr) > limit {
				tr = tr[len(tr)-limit:]
			}
			lines, out.FirstLine = tr, 0
		}
	}
	out.Lines = make([]proto.ViewLine, len(lines))
	for i, l := range lines {
		out.Lines[i] = proto.ViewLine{Text: l.Text, Role: string(l.Role)}
	}
	return out, nil
}

// viewAck clears a session's attention flag. Humans only.
func (s *Server) viewAck(who caller, name string) (proto.ViewAckResult, error) {
	if who.session {
		return proto.ViewAckResult{}, proto.Errorf(proto.CodeForbidden, "view_ack: only humans may acknowledge attention")
	}
	e, err := s.lookup(name)
	if err != nil {
		return proto.ViewAckResult{}, err
	}
	return proto.ViewAckResult{Revision: e.view.ack()}, nil
}
