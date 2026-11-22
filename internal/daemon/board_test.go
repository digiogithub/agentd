package daemon_test

import (
	"strings"
	"testing"
	"time"

	"github.com/digiogithub/agentd/internal/client"
	"github.com/digiogithub/agentd/internal/config"
	"github.com/digiogithub/agentd/internal/proto"
	"github.com/digiogithub/agentd/internal/relayclient"
)

func TestBoard(t *testing.T) {
	e := startServer(t, config.Default(), nil)
	h := e.dial(t)
	mustRun(t, h, "a")
	mustRun(t, h, "b")
	ca, cb := dialAs(t, e, "a"), dialAs(t, e, "b")
	me, err := h.Whoami()
	if err != nil {
		t.Fatal(err)
	}

	b0, err := h.Board("")
	if err != nil || len(b0.Sessions) != 2 || b0.Sessions[0].Name != "a" || b0.Sessions[1].Name != "b" || b0.Digest == "" {
		t.Fatalf("board = %+v %v", b0, err)
	}
	if b0.Sessions[0].Tasks == nil || b0.Sessions[0].Questions == nil {
		t.Errorf("tasks/questions must be empty arrays: %+v", b0.Sessions[0])
	}
	if nm, err := h.Board(b0.Digest); err != nil || !nm.NotModified || len(nm.Sessions) != 0 || nm.Digest != b0.Digest {
		t.Fatalf("not modified = %+v %v", nm, err)
	}

	// Tasks change the digest and show in full.
	if _, err := ca.TasksSet([]proto.Task{{Title: "plan"}, {Title: "ship", Status: "in_progress"}}); err != nil {
		t.Fatal(err)
	}
	b1, err := h.Board(b0.Digest)
	if err != nil || b1.NotModified || b1.Digest == b0.Digest || len(b1.Sessions[0].Tasks) != 2 {
		t.Fatalf("after tasks = %+v %v", b1, err)
	}

	// A question appears under its sender, with unread count, and closes on reply.
	q, err := ca.Send(proto.SendArgs{To: me.Identity, Body: "deploy?", Kind: "question", Options: []string{"yes", "no"}, Task: "t2"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := cb.Send(proto.SendArgs{To: me.Identity, Body: "fyi"}); err != nil {
		t.Fatal(err)
	}
	b2, err := h.Board(b1.Digest)
	if err != nil || b2.NotModified || b2.UnreadHuman != 2 {
		t.Fatalf("after question = %+v %v", b2, err)
	}
	qs := b2.Sessions[0].Questions
	if len(qs) != 1 || qs[0].ID != q.ID || qs[0].Body != "deploy?" || len(qs[0].Options) != 2 || qs[0].Task != "t2" || len(b2.Sessions[1].Questions) != 0 {
		t.Fatalf("questions = %+v", b2.Sessions)
	}
	if _, err := h.Reply(q.ID, "yes"); err != nil {
		t.Fatal(err)
	}
	b3, err := h.Board("")
	if err != nil || len(b3.Sessions[0].Questions) != 0 || b3.Digest == b2.Digest {
		t.Fatalf("after reply = %+v %v", b3, err)
	}

	// Sessions never see the board.
	_, err = ca.Board("")
	wantCode(t, "session board", err, proto.CodeForbidden)
}

func TestBoardOverRelay(t *testing.T) {
	e, _ := relayEnv(t)
	c := e.dial(t)
	mustRun(t, c, "codex@web", "cat")
	mustRun(t, c, "claude@api", "cat")
	me, err := c.Whoami()
	if err != nil {
		t.Fatal(err)
	}
	ca := dialAs(t, e, "codex@web")
	cb := dialAs(t, e, "claude@api")
	if _, err := ca.Send(proto.SendArgs{To: me.Identity, Body: "q web", Kind: "question"}); err != nil {
		t.Fatal(err)
	}
	if _, err := cb.Send(proto.SendArgs{To: me.Identity, Body: "q api", Kind: "question"}); err != nil {
		t.Fatal(err)
	}

	// Owner, operate scope, session-limited: filtered sessions, questions kept.
	tg, _ := pairDevice(t, c, proto.RelayArgs{Scope: "operate", Sessions: []string{"codex@*"}}, true)
	d := &relayclient.Dialer{Target: tg}
	defer func() { _ = d.Close() }()
	rc := remoteClient(t, d)
	b, err := rc.Board("")
	if err != nil || len(b.Sessions) != 1 || b.Sessions[0].Name != "codex@web" || len(b.Sessions[0].Questions) != 1 || b.UnreadHuman != 2 {
		t.Fatalf("owner board = %+v %v", b, err)
	}

	// Guest: sessions yes, questions and unread no.
	gt, _ := pairDevice(t, c, proto.RelayArgs{Guest: true, Scope: "view", Sessions: []string{"codex@*"}}, true)
	gd := &relayclient.Dialer{Target: gt}
	defer func() { _ = gd.Close() }()
	gb, err := remoteClient(t, gd).Board("")
	if err != nil || len(gb.Sessions) != 1 || len(gb.Sessions[0].Questions) != 0 || gb.UnreadHuman != 0 {
		t.Fatalf("guest board = %+v %v", gb, err)
	}

	// A device never asks a question, and never writes task lists.
	for _, rc := range []*client.Client{rc} {
		if _, err := rc.Send(proto.SendArgs{To: me.Identity, Body: "x", Kind: "question"}); client.ErrorCode(err) != proto.CodeInvalid {
			t.Errorf("device question: %v", err)
		}
		if _, err := rc.TaskUpdate(proto.TaskUpdateArgs{ID: "t1", Status: ptr("done")}); client.ErrorCode(err) != proto.CodeForbidden {
			t.Errorf("device task_update: %v", err)
		}
		if _, err := rc.TasksSet([]proto.Task{{Title: "x"}}); client.ErrorCode(err) != proto.CodeForbidden {
			t.Errorf("device tasks_set: %v", err)
		}
	}
}

// A reply with notify=false closes the question and is stored as delivered:
// the delivery loop never types it into the asking session.
func TestReplyNotifyFalse(t *testing.T) {
	e := startServer(t, config.Default(), nil)
	h := e.dial(t)
	mustRun(t, h, "a", "cat")
	ca := dialAs(t, e, "a")
	me, err := h.Whoami()
	if err != nil {
		t.Fatal(err)
	}
	q, err := ca.Send(proto.SendArgs{To: me.Identity, Body: "deploy?", Kind: "question", Options: []string{"yes", "no"}})
	if err != nil {
		t.Fatal(err)
	}
	r, err := h.ReplyQuiet(q.ID, "quiet-answer-xyz")
	if err != nil || r.Recipient != "a" || r.State != "delivered" || r.ReplyTo == nil || *r.ReplyTo != q.ID {
		t.Fatalf("quiet reply = %+v %v", r, err)
	}
	time.Sleep(600 * time.Millisecond)
	in, err := h.InboxWith(proto.InboxArgs{As: "a", All: true})
	if err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, m := range in.Messages {
		if m.ID == r.ID {
			found = true
			if m.State != "delivered" {
				t.Errorf("reply state %q", m.State)
			}
		}
	}
	if !found {
		t.Errorf("reply missing from inbox: %+v", in.Messages)
	}
	if out, err := h.Dump(proto.DumpArgs{Name: "a"}); err != nil || strings.Contains(out, "quiet-answer-xyz") || strings.Contains(out, "agentd msg") {
		t.Errorf("reply injected into the session: %q %v", out, err)
	}
	b, err := h.Board("")
	if err != nil || len(b.Sessions[0].Questions) != 0 {
		t.Fatalf("board after quiet reply = %+v %v", b, err)
	}
	// The default still queues.
	q2, _ := ca.Send(proto.SendArgs{To: me.Identity, Body: "again?", Kind: "question"})
	if r2, err := h.Reply(q2.ID, "loud"); err != nil || r2.State != "queued" {
		t.Fatalf("loud reply = %+v %v", r2, err)
	}
}
