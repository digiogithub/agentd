package daemon_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/digiogithub/agentd/internal/config"
	"github.com/digiogithub/agentd/internal/proto"
)

// A session asks a human; the human's reply closes the question.
func TestQuestionFlow(t *testing.T) {
	e := startServer(t, config.Default(), nil)
	h := e.dial(t)
	mustRun(t, h, "a")
	mustRun(t, h, "b")
	ca, cb := dialAs(t, e, "a"), dialAs(t, e, "b")
	me, err := h.Whoami()
	if err != nil {
		t.Fatal(err)
	}
	sub := subscribe(t, e.sock, proto.SubscribeArgs{Kinds: []string{"mail"}})

	q, err := ca.Send(proto.SendArgs{To: me.Identity, Body: "deploy now?", Kind: "question",
		Options: []string{"  yes\x1b[2J ", "no"}, Task: "t1"})
	if err != nil {
		t.Fatal(err)
	}
	if q.Kind != "question" || len(q.Options) != 2 || q.Options[0] != "yes[2J" || q.Task != "t1" || q.Recipient != me.Identity {
		t.Fatalf("question = %+v", q)
	}
	ev := nextKind(t, sub, proto.EvMailQueued)
	var d proto.MailEventData
	_ = json.Unmarshal(ev.Data, &d)
	if d.Kind != "question" || d.From != "a" || d.To != me.Identity || ev.Session != "" {
		t.Fatalf("event %+v data %+v", ev, d)
	}

	// Default recipient: the daemon's own human.
	q2, err := ca.Send(proto.SendArgs{Body: "which db?", Kind: "question"})
	if err != nil || q2.Recipient != me.Identity {
		t.Fatalf("default recipient: %+v %v", q2, err)
	}
	if q3, err := ca.Send(proto.SendArgs{To: "human:", Body: "again?", Kind: "question"}); err != nil || q3.Recipient != me.Identity {
		t.Fatalf("bare human: %+v %v", q3, err)
	}

	// The human sees it with its options (inbox peek and own inbox).
	in, err := h.InboxWith(proto.InboxArgs{})
	if err != nil || len(in.Messages) != 3 || in.Messages[0].Kind != "question" || len(in.Messages[0].Options) != 2 {
		t.Fatalf("human inbox = %+v %v", in, err)
	}

	// Validation.
	for name, a := range map[string]proto.SendArgs{
		"unknown kind":    {To: me.Identity, Body: "x", Kind: "task"},
		"to a session":    {To: "b", Body: "x", Kind: "question"},
		"to a queue":      {To: "queue:q", Body: "x", Kind: "question"},
		"options no kind": {To: me.Identity, Body: "x", Options: []string{"a"}},
		"task no kind":    {To: me.Identity, Body: "x", Task: "t"},
		"nine options":    {To: me.Identity, Body: "x", Kind: "question", Options: strings.Split("a,b,c,d,e,f,g,h,i", ",")},
		"empty option":    {To: me.Identity, Body: "x", Kind: "question", Options: []string{" "}},
		"long option":     {To: me.Identity, Body: "x", Kind: "question", Options: []string{strings.Repeat("x", 81)}},
		"bad task id":     {To: me.Identity, Body: "x", Kind: "question", Task: "a b"},
		"bad human name":  {To: "human:a b", Body: "x", Kind: "question"},
	} {
		_, err := cb.Send(a)
		wantCode(t, name, err, proto.CodeInvalid)
	}
	// Humans cannot ask.
	_, err = h.Send(proto.SendArgs{To: me.Identity, Body: "x", Kind: "question"})
	wantCode(t, "human asks", err, proto.CodeInvalid)
	// 80 multi-byte runes are fine.
	if _, err := cb.Send(proto.SendArgs{To: me.Identity, Body: "x", Kind: "question", Options: []string{strings.Repeat("é", 80)}}); err != nil {
		t.Fatalf("80-rune option: %v", err)
	}

	// Replying closes the question and reaches the asking session.
	r, err := h.Reply(q.ID, "yes")
	if err != nil || r.Recipient != "a" {
		t.Fatalf("reply = %+v %v", r, err)
	}
	if got, err := ca.InboxWith(proto.InboxArgs{All: true}); err != nil || len(got.Messages) == 0 || got.Messages[0].Body != "yes" {
		t.Fatalf("asker inbox = %+v %v", got, err)
	}
}
