package daemon_test

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/digiogithub/agentd/internal/client"
	"github.com/digiogithub/agentd/internal/config"
	"github.com/digiogithub/agentd/internal/proto"
	"github.com/digiogithub/agentd/internal/testutil"
)

func wantCode(t *testing.T, what string, err error, code string) {
	t.Helper()
	if !client.IsCode(err, code) {
		t.Fatalf("%s: got %v (code %q), want code %q", what, err, client.ErrorCode(err), code)
	}
}

func queueConfig() *config.Config {
	cfg := config.Default()
	cfg.ACL = map[string][]string{"queue:locked": {"human:*"}}
	cfg.Claim = map[string][]string{"build": {"a"}, "locked": {"a", "b"}}
	return cfg
}

// Posting, ACL, identity and the claim itself.
func TestQueueClaimFlow(t *testing.T) {
	e := startServer(t, queueConfig(), nil)
	h := e.dial(t)
	mustRun(t, h, "a")
	mustRun(t, h, "b")
	ca, cb := dialAs(t, e, "a"), dialAs(t, e, "b")
	me, err := h.Whoami()
	if err != nil {
		t.Fatal(err)
	}

	p1, err := h.Send(proto.SendArgs{To: "queue:build", Subject: "lint", Body: "fix the lint errors"})
	if err != nil {
		t.Fatal(err)
	}
	if p1.State != "unclaimed" || p1.Recipient != "queue:build" || p1.Queue != "build" {
		t.Fatalf("post = %+v", p1)
	}
	p2, err := cb.Send(proto.SendArgs{To: "queue:build", Body: "second task"})
	if err != nil {
		t.Fatal(err)
	}
	// Bad addresses; [acl] on the queue address.
	_, err = h.Send(proto.SendArgs{To: "queue:bad name", Body: "x"})
	wantCode(t, "bad queue name", err, proto.CodeInvalid)
	_, err = cb.Send(proto.SendArgs{To: "queue:locked", Body: "x"})
	wantCode(t, "post denied by acl", err, proto.CodeForbidden)
	if _, err := h.Send(proto.SendArgs{To: "queue:locked", Body: "x"}); err != nil {
		t.Fatalf("human post to locked: %v", err)
	}

	// Humans never claim; b is not in [claim] build.
	_, err = h.Claim(proto.ClaimArgs{Queue: "build"})
	wantCode(t, "human claim", err, proto.CodeForbidden)
	_, err = cb.Claim(proto.ClaimArgs{Queue: "build"})
	wantCode(t, "claim denied", err, proto.CodeForbidden)
	_, err = ca.Claim(proto.ClaimArgs{Queue: "queue:build"})
	wantCode(t, "prefixed queue name", err, proto.CodeInvalid)

	// Listing: humans any queue (no bodies by default), sessions only queues they may claim.
	l, err := h.Queue(proto.QueueArgs{Name: "build"})
	if err != nil || len(l.Items) != 2 || l.Items[0].Body != "" || l.Items[0].Subject != "lint" || l.Digest == "" {
		t.Fatalf("human queue list = %+v %v", l, err)
	}
	if l, err := ca.Queue(proto.QueueArgs{Name: "build", Bodies: true}); err != nil || l.Items[0].Body != "fix the lint errors" {
		t.Fatalf("claimer list with bodies = %+v %v", l, err)
	}
	_, err = cb.Queue(proto.QueueArgs{Name: "build"})
	wantCode(t, "list denied", err, proto.CodeForbidden)
	if all, err := cb.Queue(proto.QueueArgs{}); err != nil || len(all.Queues) != 1 || all.Queues[0].Name != "locked" {
		t.Fatalf("b's queues = %+v %v", all, err)
	}
	if all, err := h.Queue(proto.QueueArgs{}); err != nil || len(all.Queues) != 2 || all.Queues[0] != (proto.QueueCount{Name: "build", Unclaimed: 2}) {
		t.Fatalf("human queues = %+v %v", all, err)
	}
	if nm, err := h.Queue(proto.QueueArgs{Name: "build", SinceDigest: l.Digest}); err != nil || !nm.NotModified || len(nm.Items) != 0 {
		t.Fatalf("queue since_digest = %+v %v", nm, err)
	}

	// a claims the oldest: it is now a's message, already fetched.
	c1, err := ca.Claim(proto.ClaimArgs{Queue: "build"})
	if err != nil {
		t.Fatal(err)
	}
	if c1.ID != p1.ID || c1.Recipient != "a" || c1.State != "delivered" || c1.Queue != "build" ||
		c1.ClaimedAt == nil || c1.Body != "fix the lint errors" || c1.Sender != me.Identity {
		t.Fatalf("claim = %+v", c1)
	}
	// A given id: first claim wins, the second is a conflict.
	if _, err := ca.Claim(proto.ClaimArgs{Queue: "build", ID: p2.ID}); err != nil {
		t.Fatal(err)
	}
	_, err = ca.Claim(proto.ClaimArgs{Queue: "build", ID: p2.ID})
	wantCode(t, "double claim", err, proto.CodeConflict)
	_, err = ca.Claim(proto.ClaimArgs{Queue: "build"})
	wantCode(t, "empty queue", err, proto.CodeNotFound)
	_, err = ca.Claim(proto.ClaimArgs{Queue: "locked", ID: p1.ID})
	wantCode(t, "item of another queue", err, proto.CodeNotFound)

	// Normal message from here: inbox, read, reply to the original sender.
	inbox, err := ca.Inbox("", false)
	if err != nil || len(inbox) != 2 || inbox[0].ID != p1.ID {
		t.Fatalf("a's inbox = %+v %v", inbox, err)
	}
	if _, err := ca.Read("", p1.ID); err != nil {
		t.Fatal(err)
	}
	r, err := ca.Reply(p1.ID, "lint fixed")
	if err != nil || r.Recipient != me.Identity || r.Warning != "" {
		t.Fatalf("reply = %+v %v", r, err)
	}
	r2, err := ca.Reply(p2.ID, "done")
	if err != nil || r2.Recipient != "b" {
		t.Fatalf("reply to session poster = %+v %v", r2, err)
	}
	if l, err := h.Queue(proto.QueueArgs{Name: "build"}); err != nil || len(l.Items) != 0 {
		t.Fatalf("queue after claims = %+v %v", l, err)
	}

	// Purge covers queue items (humans).
	if res, err := h.Purge(proto.PurgeArgs{Recipient: "queue:locked", States: []string{"unclaimed"}}); err != nil || res.Count != 1 {
		t.Fatalf("purge queue = %+v %v", res, err)
	}
}

// Events: posts are mail.queued with no session, seen by humans and by the
// sessions that may claim; claims are mail.claimed then mail.delivered.
func TestQueueEvents(t *testing.T) {
	e := startServer(t, queueConfig(), nil)
	h := e.dial(t)
	mustRun(t, h, "a")
	mustRun(t, h, "b")
	ca := dialAs(t, e, "a")
	mail := proto.SubscribeArgs{Kinds: []string{"mail"}}
	hsub := subscribe(t, e.sock, mail)
	// Subscribe takes over its connection: separate clients for the streams.
	asub, err := dialAs(t, e, "a").Subscribe(mail, 256)
	if err != nil {
		t.Fatal(err)
	}
	defer asub.Close()
	bsub, err := dialAs(t, e, "b").Subscribe(mail, 256)
	if err != nil {
		t.Fatal(err)
	}
	defer bsub.Close()

	p, err := h.Send(proto.SendArgs{To: "queue:build", Body: "SECRET-TASK"})
	if err != nil {
		t.Fatal(err)
	}
	for _, sub := range []*client.Subscription{hsub, asub} {
		ev := nextKind(t, sub, proto.EvMailQueued)
		var d proto.MailEventData
		_ = json.Unmarshal(ev.Data, &d)
		if ev.Session != "" || d.ID != p.ID || d.Queue != "build" || d.To != "queue:build" || strings.Contains(string(ev.Data), "SECRET") {
			t.Fatalf("post event = %+v %s", ev, ev.Data)
		}
	}
	if _, err := ca.Claim(proto.ClaimArgs{Queue: "build"}); err != nil {
		t.Fatal(err)
	}
	for _, sub := range []*client.Subscription{hsub, asub} {
		ev := nextKind(t, sub, proto.EvMailClaimed)
		var d proto.MailEventData
		_ = json.Unmarshal(ev.Data, &d)
		if ev.Session != "a" || d.ID != p.ID || d.By != "a" || d.Queue != "build" || d.To != "a" {
			t.Fatalf("claim event = %+v %s", ev, ev.Data)
		}
		if ev := next(t, sub); ev.Kind != proto.EvMailDelivered {
			t.Fatalf("after claim: %+v", ev)
		}
	}
	// b may not claim from build: it saw neither the post nor the claim. Its
	// first mail event is one about itself.
	if _, err := h.Send(proto.SendArgs{To: "b", Body: "for b"}); err != nil {
		t.Fatal(err)
	}
	for {
		ev := next(t, bsub)
		if ev.Kind == proto.EvHello {
			continue
		}
		var d proto.MailEventData
		_ = json.Unmarshal(ev.Data, &d)
		if ev.Kind != proto.EvMailQueued || d.To != "b" {
			t.Fatalf("b saw %+v %s", ev, ev.Data)
		}
		break
	}
}

func TestInboxSinceDigest(t *testing.T) {
	e := startDaemon(t)
	h := e.dial(t)
	mustRun(t, h, "a")
	ca := dialAs(t, e, "a")
	if _, err := h.Send(proto.SendArgs{To: "a", Body: "one"}); err != nil {
		t.Fatal(err)
	}
	r1, err := ca.InboxWith(proto.InboxArgs{})
	if err != nil || len(r1.Messages) != 1 || r1.Digest == "" || r1.NotModified {
		t.Fatalf("first = %+v %v", r1, err)
	}
	// Unchanged (the queued message became delivered with the first fetch).
	r2, err := ca.InboxWith(proto.InboxArgs{SinceDigest: r1.Digest})
	if err != nil || !r2.NotModified || r2.Digest != r1.Digest || len(r2.Messages) != 0 {
		t.Fatalf("unchanged = %+v %v", r2, err)
	}
	if _, err := h.Send(proto.SendArgs{To: "a", Body: "two"}); err != nil {
		t.Fatal(err)
	}
	r3, err := ca.InboxWith(proto.InboxArgs{SinceDigest: r1.Digest})
	if err != nil || r3.NotModified || len(r3.Messages) != 2 || r3.Digest == r1.Digest {
		t.Fatalf("changed = %+v %v", r3, err)
	}
	if _, err := ca.Read("", r3.Messages[0].ID); err != nil {
		t.Fatal(err)
	}
	if r4, err := ca.InboxWith(proto.InboxArgs{SinceDigest: r3.Digest}); err != nil || r4.NotModified || len(r4.Messages) != 1 {
		t.Fatalf("after read = %+v %v", r4, err)
	}
	// A human peek reports the digest of the peeked mailbox too.
	if p, err := h.InboxWith(proto.InboxArgs{As: "a", All: true}); err != nil || p.Digest == "" || len(p.Messages) != 2 {
		t.Fatalf("peek = %+v %v", p, err)
	}
	// Old clients: Inbox still works.
	if msgs, err := ca.Inbox("", true); err != nil || len(msgs) != 2 {
		t.Fatalf("old inbox = %+v %v", msgs, err)
	}
}

// An unclaimed item is never injected; once claimed with inject it is
// delivered like any message.
func TestQueueDeliveryAfterClaim(t *testing.T) {
	sock, srv := startDeliveryServer(t)
	bin := testutil.BuildFakeAgent(t)
	c, err := client.Dial(sock)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if _, err := c.Run(proto.RunArgs{Name: "fake@a", Cwd: t.TempDir(), Cmd: []string{bin}, Profile: "fake"}); err != nil {
		t.Fatal(err)
	}
	waitDump(t, c, "fake@a", "fakeagent ready")
	p, err := c.Send(proto.SendArgs{To: "queue:work", Body: "build the docs"})
	if err != nil {
		t.Fatal(err)
	}
	// Several delivery sweeps (50 ms) with an idle target: nothing typed.
	time.Sleep(600 * time.Millisecond)
	if out, err := c.Dump(proto.DumpArgs{Name: "fake@a"}); err != nil || strings.Contains(out, "agentd msg") {
		t.Fatalf("unclaimed item injected: %q %v", out, err)
	}
	ca := dialAsSrv(t, sock, srv, "fake@a")
	m, err := ca.Claim(proto.ClaimArgs{Queue: "work", Inject: true})
	if err != nil || m.ID != p.ID || m.State != "queued" {
		t.Fatalf("claim = %+v %v", m, err)
	}
	waitDump(t, c, "fake@a", "build the docs")
	testutil.WaitFor(t, timeout, func() bool { return messageState(t, c, "fake@a", p.ID) == "delivered" },
		"claimed item should be delivered", nil)
}
