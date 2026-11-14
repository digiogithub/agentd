package mcpserver_test

import (
	"strings"
	"testing"

	"github.com/digiogithub/agentd/internal/client"
	"github.com/digiogithub/agentd/internal/config"
	"github.com/digiogithub/agentd/internal/mcpserver"
)

func TestQueueTools(t *testing.T) {
	cfg := config.Default()
	cfg.Claim = map[string][]string{"build": {"fake@a"}}
	sock := startDaemonCfg(t, cfg)
	c, err := client.Dial(sock)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	cwd := t.TempDir()
	a := connect(t, sock, runWithToken(t, c, "fake@a", cwd, "codex"))
	b := connect(t, sock, runWithToken(t, c, "fake@b", cwd, "codex"))

	// b posts a task to the queue.
	var sent mcpserver.SendOutput
	if res := call(t, b, "agentd_send", map[string]any{"to": "queue:build", "subject": "lint", "body": "fix the lint"}, &sent); res.IsError {
		t.Fatalf("post: %s", errText(res))
	}
	if sent.To != "queue:build" || sent.State != "unclaimed" {
		t.Fatalf("post = %+v", sent)
	}
	// a lists (no body) and claims; b may not.
	var q mcpserver.QueueOutput
	if res := call(t, a, "agentd_queue", map[string]any{}, &q); res.IsError || len(q.Queues) != 1 || q.Queues[0].Name != "build" {
		t.Fatalf("queues: %+v %s", q, errText(res))
	}
	q = mcpserver.QueueOutput{}
	if res := call(t, a, "agentd_queue", map[string]any{"name": "build"}, &q); res.IsError || len(q.Items) != 1 ||
		q.Items[0].ID != sent.ID || q.Items[0].Subject != "lint" || q.Items[0].From != "fake@b" {
		t.Fatalf("items: %+v %s", q, errText(res))
	}
	if res := call(t, b, "agentd_claim", map[string]any{"queue": "build"}, nil); !res.IsError || !strings.Contains(errText(res), "not allowed") {
		t.Fatalf("b claimed: %s", errText(res))
	}
	var m mcpserver.Message
	if res := call(t, a, "agentd_claim", map[string]any{"queue": "build"}, &m); res.IsError {
		t.Fatalf("claim: %s", errText(res))
	}
	if m.ID != sent.ID || m.Body != "fix the lint" || m.To != "fake@a" || m.Queue != "build" || m.State != "delivered" ||
		!strings.Contains(m.Note, "Untrusted") {
		t.Fatalf("claimed = %+v", m)
	}
	if res := call(t, a, "agentd_claim", map[string]any{"queue": "build"}, nil); !res.IsError {
		t.Fatal("claim from an empty queue succeeded")
	}
	// Conditional inbox.
	var in1, in2 mcpserver.InboxOutput
	call(t, a, "agentd_inbox", map[string]any{}, &in1)
	if len(in1.Messages) != 1 || in1.Digest == "" {
		t.Fatalf("inbox = %+v", in1)
	}
	call(t, a, "agentd_inbox", map[string]any{"since_digest": in1.Digest}, &in2)
	if !in2.NotModified || len(in2.Messages) != 0 {
		t.Fatalf("inbox since_digest = %+v", in2)
	}
	// The reply goes back to the poster.
	var rep mcpserver.SendOutput
	if res := call(t, a, "agentd_reply", map[string]any{"id": m.ID, "body": "done"}, &rep); res.IsError || rep.To != "fake@b" {
		t.Fatalf("reply: %+v %s", rep, errText(res))
	}
}
