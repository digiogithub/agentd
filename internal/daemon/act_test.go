package daemon_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/digiogithub/agentd/internal/client"
	"github.com/digiogithub/agentd/internal/inject"
	"github.com/digiogithub/agentd/internal/proto"
	"github.com/digiogithub/agentd/internal/testutil"
)

const actWait = 20 * time.Second

func waitStatus(t *testing.T, c *client.Client, name, status string) proto.View {
	t.Helper()
	var v proto.View
	testutil.WaitFor(t, actWait, func() bool {
		v = viewOf(t, c, proto.ViewArgs{Name: name})
		return v.Status == status
	}, name+" should be "+status, func() string { return fmt.Sprintf("%+v", v) })
	return v
}

func actCode(t *testing.T, err error, want string) *proto.RemoteError {
	t.Helper()
	if proto.CodeOf(err) != want {
		t.Fatalf("err = %v (code %q), want code %q", err, proto.CodeOf(err), want)
	}
	re, _ := err.(*proto.RemoteError)
	return re
}

func optID(t *testing.T, v proto.View, label string) string {
	t.Helper()
	for _, o := range v.Options {
		if o.Label == label {
			return o.ID
		}
	}
	t.Fatalf("no option %q in %+v", label, v.Options)
	return ""
}

// chooseArgs builds a choose request from the view the option came from.
func chooseArgs(name string, v proto.View, optionID string) proto.ActArgs {
	return proto.ActArgs{Name: name, Action: proto.ActChoose, OptionID: optionID, Revision: v.Revision, Epoch: v.Epoch}
}

func withLabel(a proto.ActArgs, label string) proto.ActArgs { a.Label = label; return a }

func TestActPromptLandsAndSubmits(t *testing.T) {
	e := startViewServer(t, "")
	c := e.dial(t)
	runView(t, c, "p1", "fake", testutil.BuildFakeAgent(t))
	waitStatus(t, c, "p1", "idle")
	r, err := c.Act(proto.ActArgs{Name: "p1", Action: proto.ActPrompt, Text: "hello world"})
	if err != nil {
		t.Fatal(err)
	}
	if r.Result != "submitted" || !r.Submitted || r.Revision == 0 || r.Status == "" {
		t.Errorf("result = %+v", r)
	}
	waitDump(t, c, "p1", "GOT: hello world")

	// no-submit pastes only.
	no := false
	r, err = c.Act(proto.ActArgs{Name: "p1", Action: proto.ActPrompt, Text: "draft text", Submit: &no, Force: true})
	if err != nil {
		t.Fatal(err)
	}
	if r.Result != "pasted" || r.Submitted {
		t.Errorf("no-submit result = %+v", r)
	}
	if d, _ := c.Dump(proto.DumpArgs{Name: "p1"}); strings.Contains(d, "GOT: draft text") {
		t.Errorf("submitted despite submit=false:\n%s", d)
	}

	// Validation: nothing sent.
	_, err = c.Act(proto.ActArgs{Name: "p1", Action: proto.ActPrompt, Text: "\x1b\x00\x07 \t"})
	actCode(t, err, proto.CodeInvalid)
	_, err = c.Act(proto.ActArgs{Name: "p1", Action: proto.ActPrompt})
	actCode(t, err, proto.CodeInvalid)
	_, err = c.Act(proto.ActArgs{Name: "p1", Action: proto.ActPrompt, Text: strings.Repeat("x", proto.MaxActText+1)})
	actCode(t, err, proto.CodeInvalid)
	_, err = c.Act(proto.ActArgs{Name: "p1", Action: "teleport"})
	actCode(t, err, proto.CodeInvalid)
	_, err = c.Act(proto.ActArgs{Name: "nope", Action: proto.ActCancel})
	actCode(t, err, proto.CodeNotFound)
}

func TestActBusyOnHumanDraft(t *testing.T) {
	e := startViewServer(t, "")
	c := e.dial(t)
	runView(t, c, "d1", "fake", testutil.BuildFakeAgent(t))
	waitStatus(t, c, "d1", "idle")
	conn, err := e.dial(t).Attach("d1", 120, 40)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	go func() { _, _ = io.Copy(io.Discard, conn) }()
	if err := proto.WriteFrame(conn, 'D', []byte("half-typed")); err != nil {
		t.Fatal(err)
	}
	testutil.WaitFor(t, actWait, func() bool {
		return findSession(t, c, "d1").InputState == "draft"
	}, "draft should be pending", func() string { return "" })

	for _, a := range []proto.ActArgs{
		{Name: "d1", Action: proto.ActPrompt, Text: "hi", Force: true}, // force never overrides a draft
		{Name: "d1", Action: proto.ActKeys, Keys: []string{"y"}, Force: true},
		{Name: "d1", Action: proto.ActInterrupt},
	} {
		_, err := c.Act(a)
		actCode(t, err, proto.CodeBusy)
	}
	// Nothing was typed: the screen still has only the human draft.
	d, _ := c.Dump(proto.DumpArgs{Name: "d1"})
	if strings.Contains(d, "GOT:") {
		t.Errorf("act wrote despite draft:\n%s", d)
	}
	// Cancel with force is allowed over a draft (esc keeps it).
	if _, err := c.Act(proto.ActArgs{Name: "d1", Action: proto.ActCancel, Force: true}); err != nil {
		t.Errorf("forced cancel: %v", err)
	}
}

func TestActNotReadyWhileWorking(t *testing.T) {
	e := startViewServer(t, "")
	c := e.dial(t)
	runView(t, c, "w1", "fake", testutil.BuildFakeAgent(t), "-busy", "8s")
	waitStatus(t, c, "w1", "idle")
	if _, err := c.Act(proto.ActArgs{Name: "w1", Action: proto.ActPrompt, Text: "go"}); err != nil {
		t.Fatal(err)
	}
	waitStatus(t, c, "w1", "working")
	_, err := c.Act(proto.ActArgs{Name: "w1", Action: proto.ActPrompt, Text: "again"})
	actCode(t, err, proto.CodeNotReady)
	wv := viewOf(t, c, proto.ViewArgs{Name: "w1"})
	_, err = c.Act(chooseArgs("w1", wv, "o00000000"))
	actCode(t, err, proto.CodeConflict) // no menu: gone
	// Interrupt of a working session is allowed (ctrl-c: the fake exits).
	if _, err := c.Act(proto.ActArgs{Name: "w1", Action: proto.ActInterrupt}); err != nil {
		t.Fatal(err)
	}
	testutil.WaitFor(t, actWait, func() bool { return findSession(t, c, "w1").State == "exited" }, "ctrl-c should end the fake", func() string { return "" })
	_, err = c.Act(proto.ActArgs{Name: "w1", Action: proto.ActKeys, Keys: []string{"y"}})
	actCode(t, err, proto.CodeExited)
}

func TestActInterruptNeedsWorkingOrWaiting(t *testing.T) {
	e := startViewServer(t, "")
	c := e.dial(t)
	runView(t, c, "i1", "fake", testutil.BuildFakeAgent(t))
	waitStatus(t, c, "i1", "idle")
	_, err := c.Act(proto.ActArgs{Name: "i1", Action: proto.ActInterrupt})
	actCode(t, err, proto.CodeNotReady)
	// The session is untouched.
	if findSession(t, c, "i1").State != "running" {
		t.Errorf("session ended")
	}
}

func chooseFixture(t *testing.T, name string, args ...string) (*env, *client.Client, proto.View) {
	t.Helper()
	e := startViewServer(t, "")
	c := e.dial(t)
	runView(t, c, name, "fake", append([]string{testutil.BuildFakeAgent(t)}, args...)...)
	v := waitStatus(t, c, name, "waiting")
	return e, c, v
}

func TestActChoosePointer(t *testing.T) {
	_, c, v := chooseFixture(t, "m1", "-menu", "Alpha|Beta|Gamma")
	r, err := c.Act(withLabel(chooseArgs("m1", v, optID(t, v, "Gamma")), "Gamma"))
	if err != nil {
		t.Fatal(err)
	}
	if r.Result != "selected" {
		t.Errorf("result = %+v", r)
	}
	waitDump(t, c, "m1", "chose: Gamma")
}

func TestActChooseDigit(t *testing.T) {
	_, c, v := chooseFixture(t, "n1", "-numbered")
	if _, err := c.Act(chooseArgs("n1", v, v.Options[1].ID)); err != nil {
		t.Fatal(err)
	}
	waitDump(t, c, "n1", "chose: Yes, and don't ask again")
}

func TestActChooseDigitMoveThenEnter(t *testing.T) {
	_, c, v := chooseFixture(t, "dm1", "-numbered", "-digitmove")
	if _, err := c.Act(chooseArgs("dm1", v, v.Options[2].ID)); err != nil {
		t.Fatal(err)
	}
	waitDump(t, c, "dm1", "chose: No, and tell")
}

func TestActChooseDialog(t *testing.T) {
	_, c, v := chooseFixture(t, "dg1", "-dialog")
	if _, err := c.Act(withLabel(chooseArgs("dg1", v, v.Options[0].ID), v.Options[0].Label)); err != nil {
		t.Fatal(err)
	}
	waitDump(t, c, "dg1", "chose: Yes")
}

func TestActChooseAppCursor(t *testing.T) {
	// The widget accepts only SS3 arrows: a CSI arrow would be ignored.
	_, c, v := chooseFixture(t, "ac1", "-menu", "Alpha|Beta|Gamma", "-appcursor")
	if _, err := c.Act(chooseArgs("ac1", v, optID(t, v, "Beta"))); err != nil {
		t.Fatal(err)
	}
	waitDump(t, c, "ac1", "chose: Beta")
}

func TestActChooseConflicts(t *testing.T) {
	_, c, v := chooseFixture(t, "cf1", "-menu", "Alpha|Beta|Gamma")
	beta := optID(t, v, "Beta")

	_, err := c.Act(chooseArgs("cf1", v, "o12345678"))
	re := actCode(t, err, proto.CodeConflict)
	var cd proto.ActConflict
	if re == nil || json.Unmarshal(re.Data, &cd) != nil || cd.View.Name != "cf1" || len(cd.View.Options) != 3 || cd.Revision == 0 {
		t.Fatalf("conflict data = %s", re.Data)
	}
	_, err = c.Act(withLabel(chooseArgs("cf1", v, beta), "Gamma"))
	actCode(t, err, proto.CodeConflict)
	_, err = c.Act(proto.ActArgs{Name: "cf1", Action: proto.ActChoose})
	actCode(t, err, proto.CodeInvalid)
	// Nothing was sent by any of the refused requests.
	if d, _ := c.Dump(proto.DumpArgs{Name: "cf1"}); strings.Contains(d, "chose:") {
		t.Errorf("a refused choose sent keys:\n%s", d)
	}
}

// Two consecutive dialogs with the same labels but different questions: the
// option id of the first must never select in the second.
func TestActChooseDoesNotApproveSecondDialog(t *testing.T) {
	e := startViewServer(t, "")
	c := e.dial(t)
	script := `printf 'Do you want to edit a.go?\r\n❯ 1. Yes\r\n  2. No\r\n'; read -rsn1 k; ` +
		`printf '\r\nDo you want to edit b.go?\r\n❯ 1. Yes\r\n  2. No\r\n'; read -rsn1 k2; printf "\\x1b[2J\\x1b[H"; echo "second:$k2"; sleep 60`
	runView(t, c, "two", "fake", "bash", "-c", script)
	v1 := waitStatus(t, c, "two", "waiting")
	first := v1.Options[0]
	if v1.Dialog == nil || !strings.Contains(v1.Dialog.Question, "a.go") {
		t.Fatalf("first view = %+v", v1)
	}
	if _, err := c.Act(withLabel(chooseArgs("two", v1, first.ID), first.Label)); err != nil {
		t.Fatalf("first choose: %v", err)
	}
	var v2 proto.View
	testutil.WaitFor(t, actWait, func() bool {
		v2 = viewOf(t, c, proto.ViewArgs{Name: "two"})
		return v2.Status == "waiting" && v2.Dialog != nil && strings.Contains(v2.Dialog.Question, "b.go")
	}, "second dialog", func() string { return fmt.Sprintf("%+v", v2) })
	if v2.Options[0].ID == first.ID {
		t.Fatalf("ids of different questions collide: %s", first.ID)
	}
	// A client that still holds the first dialog's option.
	_, err := c.Act(withLabel(chooseArgs("two", v1, first.ID), first.Label))
	actCode(t, err, proto.CodeConflict)
	time.Sleep(300 * time.Millisecond)
	if d, _ := c.Dump(proto.DumpArgs{Name: "two"}); strings.Contains(d, "second:") {
		t.Fatalf("second dialog was answered:\n%s", d)
	}
	// With the current id it works.
	if _, err := c.Act(chooseArgs("two", v2, v2.Options[0].ID)); err != nil {
		t.Fatal(err)
	}
	waitDump(t, c, "two", "second:1")
}

func TestActKeysAndCancel(t *testing.T) {
	_, c, _ := chooseFixture(t, "k1", "-menu", "Alpha|Beta|Gamma")
	if _, err := c.Act(proto.ActArgs{Name: "k1", Action: proto.ActKeys, Keys: []string{"down", "down", "up", "enter"}}); err != nil {
		t.Fatal(err)
	}
	waitDump(t, c, "k1", "chose: Beta")

	_, c2, _ := chooseFixture(t, "k2", "-dialog")
	r, err := c2.Act(proto.ActArgs{Name: "k2", Action: proto.ActCancel})
	if err != nil || r.Result != "cancelled" {
		t.Fatalf("cancel = %+v, %v", r, err)
	}
	waitDump(t, c2, "k2", "cancelled")

	for _, keys := range [][]string{
		{"enter", "bogus"}, {"\x1b[A"}, {"ctrl-z"}, {"Enter"}, {}, make([]string, 33),
	} {
		_, err := c.Act(proto.ActArgs{Name: "k1", Action: proto.ActKeys, Keys: keys})
		actCode(t, err, proto.CodeInvalid)
	}
}

func TestActInterruptWaiting(t *testing.T) {
	_, c, _ := chooseFixture(t, "iw", "-menu", "Alpha|Beta")
	if _, err := c.Act(proto.ActArgs{Name: "iw", Action: proto.ActInterrupt}); err != nil {
		t.Fatal(err)
	}
	testutil.WaitFor(t, actWait, func() bool {
		s := findSession(t, c, "iw")
		return s.State == "exited" && s.ExitCode != nil && *s.ExitCode == 130
	}, "ctrl-c should exit 130", func() string { return fmt.Sprintf("%+v", findSession(t, c, "iw")) })
}

func TestActProfileInterruptKeys(t *testing.T) {
	e := startViewServer(t, "\n[profiles.escint]\nidle_ms = 300\nhuman_grace_ms = 0\ninterrupt_keys = [\"esc\"]\n")
	c := e.dial(t)
	runView(t, c, "ei", "escint", testutil.BuildFakeAgent(t), "-dialog")
	waitStatus(t, c, "ei", "waiting")
	if _, err := c.Act(proto.ActArgs{Name: "ei", Action: proto.ActInterrupt}); err != nil {
		t.Fatal(err)
	}
	waitDump(t, c, "ei", "cancelled") // esc, not ctrl-c
}

func TestActPermissions(t *testing.T) {
	e := startViewServer(t, "")
	c := e.dial(t)
	runView(t, c, "s1", "fake", "sh", "-c", "sleep 60")
	runView(t, c, "s2", "fake", "sh", "-c", "sleep 60")
	s1 := dialAs(t, e, "s1")
	_, err := s1.Act(proto.ActArgs{Name: "s2", Action: proto.ActCancel})
	actCode(t, err, proto.CodeForbidden)
	if _, err := s1.Act(proto.ActArgs{Name: "s1", Action: proto.ActCancel}); err != nil {
		t.Errorf("own act: %v", err)
	}
	if _, err := c.Act(proto.ActArgs{Name: "s2", Action: proto.ActCancel}); err != nil {
		t.Errorf("human act: %v", err)
	}

	e2 := startViewServer(t, "\n[security]\nagent_type = true\n")
	c2 := e2.dial(t)
	runView(t, c2, "b1", "fake", "sh", "-c", "sleep 60")
	runView(t, c2, "b2", "fake", "sh", "-c", "sleep 60")
	if _, err := dialAs(t, e2, "b1").Act(proto.ActArgs{Name: "b2", Action: proto.ActCancel}); err != nil {
		t.Errorf("agent_type=true cross act: %v", err)
	}
}

// Acts hold the session's input lock: they wait for the delivery loop (or any
// other injector) and never interleave with it or with each other.
func TestActSerializesWithInjectLock(t *testing.T) {
	e := startViewServer(t, "")
	c := e.dial(t)
	runView(t, c, "lk", "fake", testutil.BuildFakeAgent(t))
	waitStatus(t, c, "lk", "idle")
	tgt, _, ok := e.srv.Lookup("lk")
	if !ok {
		t.Fatal("lookup")
	}
	unlock, err := inject.Lock(context.Background(), tgt)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		c2 := e.dial(t)
		_, err := c2.Act(proto.ActArgs{Name: "lk", Action: proto.ActPrompt, Text: "after-lock"})
		done <- err
	}()
	select {
	case err := <-done:
		t.Fatalf("act did not wait for the input lock (err=%v)", err)
	case <-time.After(400 * time.Millisecond):
	}
	unlock()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(actWait):
		t.Fatal("act never completed")
	}
	waitDump(t, c, "lk", "GOT: after-lock")
}

func TestActConcurrentPromptsDoNotInterleave(t *testing.T) {
	e := startViewServer(t, "")
	c := e.dial(t)
	runView(t, c, "cc", "fake", testutil.BuildFakeAgent(t))
	waitStatus(t, c, "cc", "idle")
	var wg sync.WaitGroup
	results := make([]error, 2)
	for i, txt := range []string{"AAAAAAAAAAAAAAAAAAAA", "BBBBBBBBBBBBBBBBBBBB"} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			c2 := e.dial(t)
			// The second may legitimately find the agent not idle yet; it
			// retries until its turn so that both texts are delivered.
			deadline := time.Now().Add(actWait)
			for {
				_, err := c2.Act(proto.ActArgs{Name: "cc", Action: proto.ActPrompt, Text: txt})
				if err == nil || time.Now().After(deadline) || proto.CodeOf(err) != proto.CodeNotReady {
					results[i] = err
					return
				}
				time.Sleep(100 * time.Millisecond)
			}
		}()
	}
	wg.Wait()
	for _, err := range results {
		if err != nil {
			t.Fatal(err)
		}
	}
	waitDump(t, c, "cc", "GOT: AAAAAAAAAAAAAAAAAAAA")
	waitDump(t, c, "cc", "GOT: BBBBBBBBBBBBBBBBBBBB")
	d, _ := c.Dump(proto.DumpArgs{Name: "cc"})
	if strings.Contains(d, "GOT: AAAAAAAAAAAAAAAAAAAABBB") || strings.Contains(d, "GOT: BBBBBBBBBBBBBBBBBBBBAAA") {
		t.Errorf("interleaved:\n%s", d)
	}
}

// interactionScript prints "ready", waits for the trigger file, then runs
// after (shell commands) and finally reads one line / key into "got:<x>".
func interactionScript(trigger, after, read string) string {
	return fmt.Sprintf(`echo ready; until [ -e %s ]; do sleep 0.05; done; %s %s echo "got:$k"; sleep 60`, trigger, after, read)
}

func TestActRevisionKeysRefusedWhenMenuAppeared(t *testing.T) {
	e := startViewServer(t, "")
	c := e.dial(t)
	trig := t.TempDir() + "/go"
	menu := `printf 'Do you want to edit a.go?\r\n❯ 1. Yes\r\n  2. No\r\n';`
	runView(t, c, "rk", "fake", "bash", "-c", interactionScript(trig, menu, "read -rsn1 k;"))
	v1 := waitStatus(t, c, "rk", "idle")
	if err := os.WriteFile(trig, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	v2 := waitStatus(t, c, "rk", "waiting")

	// The user saw the idle view: Enter must not hit the menu that appeared.
	_, err := c.Act(proto.ActArgs{Name: "rk", Action: proto.ActKeys, Keys: []string{"enter"}, Revision: v1.Revision, Epoch: v1.Epoch})
	re := actCode(t, err, proto.CodeConflict)
	var conf proto.ActConflict
	if re.Data == nil || json.Unmarshal(re.Data, &conf) != nil || conf.View.Status != "waiting" {
		t.Fatalf("conflict data = %s", re.Data)
	}
	// Same for interrupt and cancel.
	for _, act := range []string{proto.ActInterrupt, proto.ActCancel} {
		_, err = c.Act(proto.ActArgs{Name: "rk", Action: act, Revision: v1.Revision, Epoch: v1.Epoch})
		actCode(t, err, proto.CodeConflict)
	}
	time.Sleep(300 * time.Millisecond)
	if d, _ := c.Dump(proto.DumpArgs{Name: "rk"}); strings.Contains(d, "got:") {
		t.Fatalf("a key reached the menu:\n%s", d)
	}
	// With the displayed (fresh) revision the key is sent.
	if _, err := c.Act(proto.ActArgs{Name: "rk", Action: proto.ActKeys, Keys: []string{"1"}, Revision: v2.Revision, Epoch: v2.Epoch}); err != nil {
		t.Fatal(err)
	}
	waitDump(t, c, "rk", "got:1")
}

func TestActRevisionPromptRefusedWhenStatusChanged(t *testing.T) {
	e := startViewServer(t, "")
	c := e.dial(t)
	trig := t.TempDir() + "/go"
	menu := `printf 'Do you want to edit a.go?\r\n❯ 1. Yes\r\n  2. No\r\n';`
	runView(t, c, "rp", "fake", "bash", "-c", interactionScript(trig, menu, "read -rsn1 k;"))
	v1 := waitStatus(t, c, "rp", "idle")
	if err := os.WriteFile(trig, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	waitStatus(t, c, "rp", "waiting")
	_, err := c.Act(proto.ActArgs{Name: "rp", Action: proto.ActPrompt, Text: "hello", Force: true, Revision: v1.Revision, Epoch: v1.Epoch})
	actCode(t, err, proto.CodeConflict)
	time.Sleep(300 * time.Millisecond)
	if d, _ := c.Dump(proto.DumpArgs{Name: "rp"}); strings.Contains(d, "hello") || strings.Contains(d, "got:") {
		t.Fatalf("prompt text reached the terminal:\n%s", d)
	}
}

func TestActRevisionTranscriptOnlyChangeAccepted(t *testing.T) {
	e := startViewServer(t, "")
	c := e.dial(t)
	trig := t.TempDir() + "/go"
	runView(t, c, "rt", "fake", "bash", "-c", interactionScript(trig, `echo "unrelated output line";`, "read -r k;"))
	v1 := waitStatus(t, c, "rt", "idle")
	if err := os.WriteFile(trig, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	var v2 proto.View
	testutil.WaitFor(t, actWait, func() bool {
		v2 = viewOf(t, c, proto.ViewArgs{Name: "rt"})
		return v2.Revision != v1.Revision && v2.Status == "idle"
	}, "transcript should change while idle", func() string { return fmt.Sprintf("%+v", v2) })
	if _, err := c.Act(proto.ActArgs{Name: "rt", Action: proto.ActPrompt, Text: "hello", Revision: v1.Revision, Epoch: v1.Epoch}); err != nil {
		t.Fatalf("transcript-only change must not conflict: %v", err)
	}
	waitDump(t, c, "rt", "got:hello")
}
