package main

import (
	"reflect"
	"strings"
	"testing"

	"github.com/digiogithub/agentd/internal/proto"
)

func TestAskCmd(t *testing.T) {
	var got proto.SendArgs
	c := newAskCmd(func(a proto.SendArgs) (proto.Message, error) {
		got = a
		return proto.Message{ID: 9, Recipient: "human:alice"}, nil
	})
	out, err := execCmd(t, c, "deploy", "now?", "--option", "yes", "--option", "no", "--task", "t1", "--to", "human:alice")
	want := proto.SendArgs{To: "human:alice", Body: "deploy now?", Kind: "question", Options: []string{"yes", "no"}, Task: "t1"}
	if err != nil || !reflect.DeepEqual(got, want) || !strings.Contains(out, "message #9") {
		t.Fatalf("args = %+v, out %q, err %v", got, out, err)
	}
	// No text: refused before any call.
	c = newAskCmd(func(proto.SendArgs) (proto.Message, error) { t.Fatal("called"); return proto.Message{}, nil })
	if _, err := execCmd(t, c); err == nil {
		t.Fatal("ask without text accepted")
	}
}

func TestQuestionSuffix(t *testing.T) {
	if questionSuffix(proto.Message{}) != "" {
		t.Fatal("plain message has a suffix")
	}
	m := proto.Message{Kind: "question", Task: "t1", Options: []string{"a", "b"}}
	if got := questionSuffix(m); got != " [question task=t1] options: a | b" {
		t.Fatalf("suffix %q", got)
	}
}
