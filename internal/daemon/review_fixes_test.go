package daemon_test

// Regression tests for the code review of ops act / view / subscribe
// (docs/changes/ops-review-fixes.md). Every test names the review item it
// covers.

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/digiogithub/agentd/internal/client"
	"github.com/digiogithub/agentd/internal/inject"
	"github.com/digiogithub/agentd/internal/proto"
	"github.com/digiogithub/agentd/internal/testutil"
)

const reviewExtra = `
[profiles.sel]
idle_ms = 300
human_grace_ms = 0
menu_digit_action = "select"

[profiles.grace]
idle_ms = 300
human_grace_ms = 60000
`

// twoDialogs shows the same dialog (same question, same labels) twice, for two
// different commands, the second one right after a key. second:<key> is
// printed only if the second dialog receives a key.
const twoDialogs = `printf '\033[2J\033[HCommand: rm a\r\n\r\nRun it?\r\n❯ 1. Yes\r\n  2. No\r\n'; read -rsn1 k; ` +
	`printf '\033[2J\033[HCommand: rm b\r\n\r\nRun it?\r\n❯ 1. Yes\r\n  2. No\r\n'; read -rsn1 k2; ` +
	`printf '\033[2J\033[H'; echo "second:$k2"; sleep 60`

func noSecond(t *testing.T, c *client.Client, name string) {
	t.Helper()
	time.Sleep(600 * time.Millisecond)
	if d, _ := c.Dump(proto.DumpArgs{Name: name}); strings.Contains(d, "second:") {
		t.Fatalf("the second dialog received a key:\n%s", d)
	}
}

// Review 1 (HIGH): the digit (and Enter) of a choose must never approve the
// next, identical-looking dialog, with either menu_digit_action.
func TestReviewChooseNeverApprovesIdenticalNextDialog(t *testing.T) {
	for _, profile := range []string{"sel", "fake"} { // select / move
		t.Run(profile, func(t *testing.T) {
			e := startViewServer(t, reviewExtra)
			c := e.dial(t)
			runView(t, c, "two", profile, "bash", "-c", twoDialogs)
			v1 := waitStatus(t, c, "two", "waiting")
			first := v1.Options[0]
			if _, err := c.Act(withLabel(chooseArgs("two", v1, first.ID), first.Label)); err != nil {
				t.Fatalf("first choose: %v", err)
			}
			var v2 proto.View
			testutil.WaitFor(t, actWait, func() bool {
				v2 = viewOf(t, c, proto.ViewArgs{Name: "two"})
				return v2.Dialog != nil && v2.Status == "waiting" && v2.Revision != v1.Revision
			}, "second dialog", func() string { return fmt.Sprintf("%+v", v2) })
			noSecond(t, c, "two")
			// Same fingerprint, same option id: the client's stale revision
			// refers to another instance, so it is refused without a key.
			if v2.Options[0].ID != first.ID {
				t.Fatalf("test premise: ids differ (%s %s)", first.ID, v2.Options[0].ID)
			}
			_, err := c.Act(withLabel(chooseArgs("two", v1, first.ID), first.Label))
			actCode(t, err, proto.CodeConflict)
			noSecond(t, c, "two")
			// With the fresh revision it works (select: digit only).
			if _, err := c.Act(chooseArgs("two", v2, v2.Options[0].ID)); err != nil {
				t.Fatal(err)
			}
			waitDump(t, c, "two", "second:1")
		})
	}
}

// Review 1a: select = the digit ONLY. A menu that stays up after the digit
// (the digit did not confirm) is not followed by an Enter.
func TestReviewDigitSelectSendsNoEnter(t *testing.T) {
	e := startViewServer(t, reviewExtra)
	c := e.dial(t)
	// Digits are ignored (menu stays); Enter would print "enter!".
	script := `printf 'Run it?\r\n❯ 1. Yes\r\n  2. No\r\n'; while read -rsn1 k; do if [ -z "$k" ]; then echo "enter!"; fi; done`
	runView(t, c, "sd", "sel", "bash", "-c", script)
	v := waitStatus(t, c, "sd", "waiting")
	_, err := c.Act(chooseArgs("sd", v, v.Options[1].ID))
	// The screen never changes after the digit: timeout, never an Enter.
	if err == nil {
		t.Fatal("expected a timeout/conflict, the menu did not react")
	}
	time.Sleep(300 * time.Millisecond)
	if d, _ := c.Dump(proto.DumpArgs{Name: "sd"}); strings.Contains(d, "enter!") {
		t.Fatalf("select mode sent an Enter:\n%s", d)
	}
}

// Review 1c / 14: revision and epoch are required; an old revision is fine
// only while the same menu instance stays up.
func TestReviewChooseRevisionAndEpochRules(t *testing.T) {
	e := startViewServer(t, "")
	c := e.dial(t)
	// The title changes while the dialog stays up: the revision moves, the
	// menu instance does not.
	script := `printf 'Run it?\r\n❯ 1. Yes\r\n  2. No\r\n'; sleep 4; printf '\033]0;other title\007'; read -rsn1 k; ` +
		`printf '\033[2J\033[H'; echo "chose:$k"; sleep 60`
	runView(t, c, "rv", "fake", "bash", "-c", script)
	v := waitStatus(t, c, "rv", "waiting")
	opt := v.Options[0]

	for _, a := range []proto.ActArgs{
		{Name: "rv", Action: proto.ActChoose, OptionID: opt.ID},                       // no revision
		{Name: "rv", Action: proto.ActChoose, OptionID: opt.ID, Revision: v.Revision}, // revision without epoch
		{Name: "rv", Action: proto.ActChoose, OptionID: opt.ID, Epoch: v.Epoch},       // epoch without revision
		{Name: "rv", Action: proto.ActKeys, Keys: []string{"down"}, Revision: 1},      // any action: revision needs epoch
	} {
		_, err := c.Act(a)
		actCode(t, err, proto.CodeInvalid)
	}
	_, err := c.Act(proto.ActArgs{Name: "rv", Action: proto.ActChoose, OptionID: opt.ID, Revision: v.Revision, Epoch: "old-epoch"})
	actCode(t, err, proto.CodeConflict)

	var v2 proto.View
	testutil.WaitFor(t, actWait, func() bool {
		v2 = viewOf(t, c, proto.ViewArgs{Name: "rv"})
		return v2.Revision != v.Revision && v2.Title == "other title" && v2.Status == "waiting"
	}, "revision should move on a title change", func() string { return fmt.Sprintf("%+v", v2) })
	// Stale revision, same instance: accepted.
	if _, err := c.Act(chooseArgs("rv", v, opt.ID)); err != nil {
		t.Fatalf("stale revision of the same menu instance refused: %v", err)
	}
	waitDump(t, c, "rv", "chose:1")
}

// Review 2: a yes/no answer typed as a word must not be followed by Enter when
// the dialog was replaced by another one after the word.
func TestReviewLaterStepNeedsSameInstance(t *testing.T) {
	e := startViewServer(t, "")
	c := e.dial(t)
	script := `printf '\033[2J\033[HCommand: rm a\r\n\r\nProceed? (yes/no) '; read -rn3 k; ` +
		`printf '\033[2J\033[HCommand: rm b\r\n\r\nProceed? (yes/no) '; read -rn1 k2; ` +
		`printf '\033[2J\033[H'; echo "second:$k2"; sleep 60`
	runView(t, c, "yn", "fake", "bash", "-c", script)
	v := waitStatus(t, c, "yn", "waiting")
	var yes proto.ViewOption
	for _, o := range v.Options {
		if o.Label == "Yes" {
			yes = o
		}
	}
	if yes.ID == "" {
		t.Skipf("yes/no word prompt not detected as an option list: %+v", v)
	}
	_, err := c.Act(chooseArgs("yn", v, yes.ID))
	actCode(t, err, proto.CodeConflict)
	noSecond(t, c, "yn")
}

// Review 4 / 5: a menu that appears between the paste and the Enter stops the
// submit (conflict), the text stays pasted and counts as a draft.
func TestReviewPromptBeforeSubmitRecheck(t *testing.T) {
	e := startViewServer(t, "")
	c := e.dial(t)
	script := `printf 'ready\r\n> '; read -rn1 first; read -rsn1000 -t 0.04 rest; sleep 0.02; ` +
		`printf '\033[2J\033[HRun it?\r\n❯ 1. Yes\r\n  2. No\r\n'; read -rsn1 k2; printf '\033[2J\033[H'; echo "second:$k2"; sleep 60`
	runView(t, c, "bs", "fake", "bash", "-c", script)
	waitStatus(t, c, "bs", "idle")
	_, err := c.Act(proto.ActArgs{Name: "bs", Action: proto.ActPrompt, Text: "do the thing"})
	actCode(t, err, proto.CodeConflict)
	noSecond(t, c, "bs")
	if st := findSession(t, c, "bs").InputState; st != "draft" {
		t.Errorf("input_state = %q, want draft (text stays pasted)", st)
	}
}

// Review 5: a prompt that was pasted but not submitted is a pending draft:
// later acts are busy until it is cleared.
func TestReviewPastedPromptIsADraft(t *testing.T) {
	e := startViewServer(t, "")
	c := e.dial(t)
	runView(t, c, "pd", "fakebox", testutil.BuildFakeAgent(t), "-box")
	waitStatus(t, c, "pd", "idle")
	no := false
	if r, err := c.Act(proto.ActArgs{Name: "pd", Action: proto.ActPrompt, Text: "half a thought", Submit: &no}); err != nil || r.Result != "pasted" {
		t.Fatalf("%+v %v", r, err)
	}
	if st := findSession(t, c, "pd").InputState; st != "draft" {
		t.Fatalf("input_state = %q, want draft", st)
	}
	_, err := c.Act(proto.ActArgs{Name: "pd", Action: proto.ActPrompt, Text: "second", Force: true})
	actCode(t, err, proto.CodeBusy)
	_, err = c.Act(proto.ActArgs{Name: "pd", Action: proto.ActKeys, Keys: []string{"enter"}})
	actCode(t, err, proto.CodeBusy)
	// Draft gone but the text still in the input line: busy as well.
	tgt := e.srv.SessionForTest("pd")
	tgt.ClearDraft()
	_, err = c.Act(proto.ActArgs{Name: "pd", Action: proto.ActPrompt, Text: "third", Force: true})
	actCode(t, err, proto.CodeBusy)
	// A forced Ctrl-C is the way out (it ends the fake, draft and all).
	if _, err := c.Act(proto.ActArgs{Name: "pd", Action: proto.ActInterrupt, Force: true}); err != nil {
		t.Errorf("forced interrupt over a pasted draft: %v", err)
	}
}

// Review 6: conflict data carries a view only for callers allowed to view.
func TestReviewConflictViewRespectsPermissions(t *testing.T) {
	e := startViewServer(t, "\n[security]\nagent_type = true\n")
	h := e.dial(t)
	runView(t, h, "b1", "fake", "sh", "-c", "sleep 60")
	runView(t, h, "b2", "fake", testutil.BuildFakeAgent(t), "-menu", "Alpha|Beta")
	v := waitStatus(t, h, "b2", "waiting")
	args := chooseArgs("b2", v, "o12345678")

	_, err := h.Act(args)
	re := actCode(t, err, proto.CodeConflict)
	var cd proto.ActConflict
	if json.Unmarshal(re.Data, &cd) != nil || cd.View.Name != "b2" {
		t.Fatalf("human conflict lacks the view: %s", re.Data)
	}
	_, err = dialAs(t, e, "b1").Act(args) // agent_type allows it, agent_view does not
	re = actCode(t, err, proto.CodeConflict)
	if len(re.Data) != 0 && string(re.Data) != "null" {
		t.Fatalf("conflict leaked the view to a caller without agent_view: %s", re.Data)
	}
}

// Review 7: ls does not hand other sessions' transcript preview to a session
// without agent_view.
func TestReviewLsStripsPreviewForSessions(t *testing.T) {
	e := startViewServer(t, "")
	h := e.dial(t)
	runView(t, h, "s1", "fake", "sh", "-c", "echo mine-1234; sleep 60")
	runView(t, h, "s2", "fake", "sh", "-c", "echo secret-5678; sleep 60")
	testutil.WaitFor(t, actWait, func() bool {
		return strings.Contains(findSession(t, h, "s1").Preview, "mine-1234") && strings.Contains(findSession(t, h, "s2").Preview, "secret-5678")
	}, "previews", func() string { return "" })
	s1 := dialAs(t, e, "s1")
	if p := findSession(t, s1, "s1").Preview; !strings.Contains(p, "mine-1234") {
		t.Errorf("own preview = %q", p)
	}
	if got := findSession(t, s1, "s2"); got.Preview != "" || got.Status == "" {
		t.Errorf("peer preview/status = %q / %q (want no preview, status kept)", got.Preview, got.Status)
	}
	if p := findSession(t, h, "s2").Preview; !strings.Contains(p, "secret-5678") {
		t.Errorf("human lost the preview: %q", p)
	}
	e2 := startViewServer(t, "\n[security]\nagent_view = true\n")
	h2 := e2.dial(t)
	runView(t, h2, "v1", "fake", "sh", "-c", "sleep 60")
	runView(t, h2, "v2", "fake", "sh", "-c", "echo allowed-9; sleep 60")
	testutil.WaitFor(t, actWait, func() bool { return strings.Contains(findSession(t, h2, "v2").Preview, "allowed-9") }, "preview", func() string { return "" })
	if p := findSession(t, dialAs(t, e2, "v1"), "v2").Preview; !strings.Contains(p, "allowed-9") {
		t.Errorf("agent_view caller lost the preview: %q", p)
	}
}

// Review 9: session.created only for a session that really started.
func TestReviewNoCreatedForFailedStart(t *testing.T) {
	e := startViewServer(t, "")
	c := e.dial(t)
	sub := subscribe(t, e.sock, proto.SubscribeArgs{Kinds: []string{"session"}})
	expectHello(t, sub, e)
	if _, err := c.Run(proto.RunArgs{Name: "bad", Cmd: []string{"/nonexistent/agentd-test-binary"}, Cwd: t.TempDir()}); err == nil {
		t.Fatal("run of a missing binary succeeded")
	}
	runView(t, c, "good", "fake", "sh", "-c", "sleep 60")
	for {
		ev := next(t, sub)
		if ev.Session == "bad" {
			t.Fatalf("event for a session that never started: %+v", ev)
		}
		if ev.Kind == proto.EvSessionCreated {
			if ev.Session != "good" {
				t.Fatalf("created for %q", ev.Session)
			}
			break
		}
	}
}

// Review 10: session.exited always precedes session.removed.
func TestReviewExitedBeforeRemoved(t *testing.T) {
	e := startViewServer(t, "")
	c := e.dial(t)
	sub := subscribe(t, e.sock, proto.SubscribeArgs{Kinds: []string{"session"}})
	expectHello(t, sub, e)
	const n = 12
	for i := 0; i < n; i++ {
		name := fmt.Sprintf("q%d", i)
		runView(t, c, name, "fake", "sh", "-c", "exit 0")
		testutil.WaitFor(t, actWait, func() bool { return findSession(t, c, name).State == "exited" }, "exit", func() string { return "" })
		if err := c.Rm(name); err != nil { // right at the state change: races the exit publisher
			t.Fatal(err)
		}
	}
	exited := map[string]bool{}
	removed := 0
	for removed < n {
		ev := next(t, sub)
		switch ev.Kind {
		case proto.EvSessionExited:
			exited[ev.Session] = true
		case proto.EvSessionRemoved:
			if !exited[ev.Session] {
				t.Fatalf("session.removed for %s before session.exited", ev.Session)
			}
			removed++
		}
	}
}

// Review 11 / 12: acts are not human keystrokes; a real keystroke makes
// choose busy within the grace period.
func TestReviewActIsNotHumanAndChooseHonoursGrace(t *testing.T) {
	e := startViewServer(t, reviewExtra)
	c := e.dial(t)
	runView(t, c, "g1", "grace", testutil.BuildFakeAgent(t), "-menu", "Alpha|Beta|Gamma")
	// human_grace 60s keeps the status "waiting" classification unaffected by act keys.
	v := waitStatus(t, c, "g1", "waiting")
	tgt := e.srv.SessionForTest("g1")
	// An act key (esc is human-flagged) leaves LastHumanInput alone...
	if _, err := c.Act(proto.ActArgs{Name: "g1", Action: proto.ActKeys, Keys: []string{"up"}}); err != nil {
		t.Fatal(err)
	}
	if !tgt.LastHumanInput().IsZero() || tgt.LastActInput().IsZero() {
		t.Fatalf("human=%v act=%v", tgt.LastHumanInput(), tgt.LastActInput())
	}
	// ...so the next act is not busy.
	v = viewOf(t, c, proto.ViewArgs{Name: "g1"})
	if _, err := c.Act(proto.ActArgs{Name: "g1", Action: proto.ActKeys, Keys: []string{"down"}}); err != nil {
		t.Fatalf("second act after an act: %v", err)
	}
	// A real keystroke from an attached client: choose is busy, force goes on.
	conn, err := e.dial(t).Attach("g1", 120, 40)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	go func() { _, _ = io.Copy(io.Discard, conn) }()
	if err := proto.WriteFrame(conn, 'D', []byte("\x1b[B")); err != nil { // draft-neutral key
		t.Fatal(err)
	}
	testutil.WaitFor(t, actWait, func() bool { return !tgt.LastHumanInput().IsZero() }, "human input", func() string { return "" })
	v = viewOf(t, c, proto.ViewArgs{Name: "g1"})
	_, err = c.Act(chooseArgs("g1", v, v.Options[2].ID))
	actCode(t, err, proto.CodeBusy)
	if _, err := c.Act(func() proto.ActArgs { a := chooseArgs("g1", v, v.Options[2].ID); a.Force = true; return a }()); err != nil {
		t.Fatalf("forced choose: %v", err)
	}
	waitDump(t, c, "g1", "chose: Gamma")
}

// Review 13: Enter on an empty input with a suggestion shown is refused;
// ctrl-c/ctrl-d keys are guarded like interrupt.
func TestReviewKeysGhostEnterAndStopKeys(t *testing.T) {
	e := startViewServer(t, "")
	c := e.dial(t)
	runView(t, c, "gh", "fakebox", testutil.BuildFakeAgent(t), "-box", "-ghost", "Try fixing lint")
	waitStatus(t, c, "gh", "idle")
	_, err := c.Act(proto.ActArgs{Name: "gh", Action: proto.ActKeys, Keys: []string{"enter"}, Force: true})
	actCode(t, err, proto.CodeNotReady)
	_, err = c.Act(proto.ActArgs{Name: "gh", Action: proto.ActKeys, Keys: []string{"tab", "enter"}})
	actCode(t, err, proto.CodeNotReady)
	time.Sleep(200 * time.Millisecond)
	if d, _ := c.Dump(proto.DumpArgs{Name: "gh", Ghost: true}); strings.Contains(d, "GOT:") {
		t.Fatalf("the suggestion was submitted:\n%s", d)
	}
	for _, k := range []string{"ctrl-c", "ctrl-d"} {
		_, err = c.Act(proto.ActArgs{Name: "gh", Action: proto.ActKeys, Keys: []string{k}})
		actCode(t, err, proto.CodeNotReady)
	}
	if findSession(t, c, "gh").State != "running" {
		t.Fatal("session ended")
	}
	// Typed text erases the suggestion first: that is allowed.
	if _, err := c.Act(proto.ActArgs{Name: "gh", Action: proto.ActKeys, Keys: []string{"y", "enter"}}); err != nil {
		t.Fatalf("typed text then enter: %v", err)
	}
	waitDump(t, c, "gh", "GOT: y")
}

// Review 14: view not_modified needs the epoch.
func TestReviewViewNotModifiedNeedsEpoch(t *testing.T) {
	e := startViewServer(t, "")
	c := e.dial(t)
	runView(t, c, "ne", "fake", "sh", "-c", "sleep 60")
	v := viewOf(t, c, proto.ViewArgs{Name: "ne"})
	if nm := viewOf(t, c, proto.ViewArgs{Name: "ne", SinceRevision: v.Revision}); nm.NotModified {
		t.Error("not_modified without an epoch")
	}
	if nm := viewOf(t, c, proto.ViewArgs{Name: "ne", SinceRevision: v.Revision, Epoch: "other"}); nm.NotModified {
		t.Error("not_modified with a foreign epoch")
	}
}

// Review 15: an act is cancelled when its client disconnects (it must not
// type later, after waiting for the input lock).
func TestReviewActCancelledOnDisconnect(t *testing.T) {
	e := startViewServer(t, "")
	c := e.dial(t)
	runView(t, c, "dc", "fake", testutil.BuildFakeAgent(t))
	waitStatus(t, c, "dc", "idle")
	tgt := e.srv.SessionForTest("dc")
	unlock, err := inject.Lock(context.Background(), tgt)
	if err != nil {
		t.Fatal(err)
	}
	conn, err := net.Dial("unix", e.sock)
	if err != nil {
		t.Fatal(err)
	}
	req, _ := json.Marshal(proto.Request{Op: proto.OpAct, Args: mustJSON(proto.ActArgs{Name: "dc", Action: proto.ActPrompt, Text: "after-disconnect"})})
	if _, err := conn.Write(append(req, '\n')); err != nil {
		t.Fatal(err)
	}
	time.Sleep(400 * time.Millisecond) // the act is waiting for the lock
	_ = conn.Close()
	time.Sleep(300 * time.Millisecond)
	unlock()
	time.Sleep(800 * time.Millisecond)
	if d, _ := c.Dump(proto.DumpArgs{Name: "dc"}); strings.Contains(d, "after-disconnect") {
		t.Fatalf("an act of a disconnected client typed:\n%s", d)
	}
}

func mustJSON(v any) json.RawMessage {
	b, _ := json.Marshal(v)
	return b
}

// Review 16: since_seq needs an epoch; no live event predates the snapshot.
func TestReviewSubscribeSinceNeedsEpochAndSnapshotOrdering(t *testing.T) {
	e := startViewServer(t, "")
	c, err := client.Dial(e.sock)
	if err != nil {
		t.Fatal(err)
	}
	_, err = c.Subscribe(proto.SubscribeArgs{SinceSeq: 3}, 0)
	actCode(t, err, proto.CodeInvalid)

	runView(t, e.dial(t), "o1", "fake", "sh", "-c", "sleep 60")
	stop := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; ; i++ {
			select {
			case <-stop:
				return
			default:
				e.srv.PublishForTest(proto.EvViewChanged, "o1", proto.ViewChangedData{Revision: uint64(i)})
				time.Sleep(time.Millisecond)
			}
		}
	}()
	defer func() { close(stop); <-done }()
	for i := 0; i < 15; i++ {
		sub := subscribe(t, e.sock, proto.SubscribeArgs{})
		deadline := time.After(2 * time.Second)
		for seen := 0; seen < 5; {
			select {
			case ev := <-sub.Events:
				if ev.Seq != 0 && ev.Seq <= sub.Info.Seq {
					t.Fatalf("event seq %d predates the snapshot (info seq %d)", ev.Seq, sub.Info.Seq)
				}
				seen++
			case <-deadline:
				t.Fatal("no events")
			}
		}
	}
}

// Review 17 / 18: a session-identity subscription ends (bye, stream closed)
// when its session is removed.
func TestReviewSubscriptionEndsWhenSessionRemoved(t *testing.T) {
	e := startViewServer(t, "")
	h := e.dial(t)
	runView(t, h, "me", "fake", "sh", "-c", "sleep 60")
	runView(t, h, "other", "fake", "sh", "-c", "sleep 60")
	cs := dialAs(t, e, "me")
	sub, err := cs.Subscribe(proto.SubscribeArgs{Kinds: []string{"mail"}}, 64) // removed is outside the kinds filter
	if err != nil {
		t.Fatal(err)
	}
	defer sub.Close()
	if err := h.Kill("me", "KILL"); err != nil {
		t.Fatal(err)
	}
	testutil.WaitFor(t, actWait, func() bool { return findSession(t, h, "me").State == "exited" }, "exit", func() string { return "" })
	if err := h.Rm("me"); err != nil {
		t.Fatal(err)
	}
	sawBye := false
	deadline := time.After(5 * time.Second)
loop:
	for {
		select {
		case ev, ok := <-sub.Events:
			if !ok {
				break loop
			}
			if ev.Kind == proto.EvBye {
				sawBye = true
			}
		case <-deadline:
			t.Fatal("subscription of a removed session did not end")
		}
	}
	if !sawBye {
		t.Error("no bye before the stream ended")
	}
	// Other sessions' subscriptions are unaffected.
	runView(t, h, "me2", "fake", "sh", "-c", "sleep 60")
	other := dialAs(t, e, "other")
	osub, err := other.Subscribe(proto.SubscribeArgs{}, 64)
	if err != nil {
		t.Fatal(err)
	}
	defer osub.Close()
	select {
	case ev, ok := <-osub.Events:
		if ok && ev.Kind == proto.EvBye {
			t.Fatal("bye for a session that still exists")
		}
	case <-time.After(300 * time.Millisecond):
	}
}
