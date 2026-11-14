package main

import (
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"

	"github.com/digiogithub/agentd/internal/proto"
)

func execCmd(t *testing.T, cmd *cobra.Command, args ...string) (string, error) {
	t.Helper()
	var out strings.Builder
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs(args)
	err := cmd.Execute()
	return out.String(), err
}

func TestClaimCmd(t *testing.T) {
	var got proto.ClaimArgs
	cmd := func() *cobra.Command {
		return newClaimCmd(func(a proto.ClaimArgs) (proto.Message, error) {
			got = a
			return proto.Message{ID: 7, Sender: "human:alice", Recipient: "codex@x", Queue: "build",
				Subject: "lint", Body: "fix lint\n", State: "delivered"}, nil
		})
	}
	out, err := execCmd(t, cmd(), "queue:build")
	if err != nil || got != (proto.ClaimArgs{Queue: "build"}) {
		t.Fatalf("args = %+v, %v", got, err)
	}
	if want := "claimed #7 from queue:build (posted by human:alice)\nSubject: lint\n\nfix lint\n"; out != want {
		t.Fatalf("output %q", out)
	}
	out, err = execCmd(t, cmd(), "build", "7", "--inject")
	if err != nil || got != (proto.ClaimArgs{Queue: "build", ID: 7, Inject: true}) || !strings.Contains(out, "queued for delivery") {
		t.Fatalf("args = %+v, %q, %v", got, out, err)
	}
	for _, bad := range [][]string{{}, {"build", "x"}, {"build", "1", "2"}} {
		if _, err := execCmd(t, cmd(), bad...); err == nil {
			t.Errorf("%v accepted", bad)
		}
	}
}

func TestQueueCmd(t *testing.T) {
	var got proto.QueueArgs
	res := proto.QueueResult{}
	cmd := func() *cobra.Command {
		return newQueueCmd(func(a proto.QueueArgs) (proto.QueueResult, error) {
			got = a
			return res, nil
		})
	}
	res = proto.QueueResult{Queues: []proto.QueueCount{{Name: "build", Unclaimed: 2}}}
	out, err := execCmd(t, cmd())
	if err != nil || got != (proto.QueueArgs{}) || !strings.Contains(out, "queue:build  2") {
		t.Fatalf("all queues: %+v %q %v", got, out, err)
	}
	res = proto.QueueResult{Name: "build", Items: []proto.Message{{ID: 3, Sender: "lead", Subject: "lint", Body: "body text",
		CreatedAt: time.Now()}}}
	out, err = execCmd(t, cmd(), "queue:build", "--bodies")
	if err != nil || got != (proto.QueueArgs{Name: "build", Bodies: true}) || !strings.Contains(out, "lint body text") {
		t.Fatalf("items: %+v %q %v", got, out, err)
	}
	res = proto.QueueResult{Name: "build", NotModified: true, Digest: "d1"}
	out, err = execCmd(t, cmd(), "build", "--since-digest", "d1")
	if err != nil || got.SinceDigest != "d1" || out != "not modified\n" {
		t.Fatalf("not modified: %+v %q %v", got, out, err)
	}
	out, err = execCmd(t, cmd(), "build", "--json")
	if err != nil || !strings.Contains(out, `"digest": "d1"`) {
		t.Fatalf("json: %q %v", out, err)
	}
}
