package main

import (
	"strings"
	"testing"

	"github.com/digiogithub/agentd/internal/proto"
)

func TestPrintBoard(t *testing.T) {
	var b strings.Builder
	_ = printBoard(&b, proto.BoardResult{})
	if !strings.Contains(b.String(), "no sessions") {
		t.Fatalf("empty: %q", b.String())
	}
	b.Reset()
	_ = printBoard(&b, proto.BoardResult{UnreadHuman: 2, Sessions: []proto.BoardSession{{
		Name: "a", Status: "waiting", Attention: true,
		Tasks:     []proto.Task{{ID: "t1", Title: "ship", Status: "in_progress"}, {ID: "t2", Title: "x", Status: "done"}},
		Questions: []proto.BoardQuestion{{ID: 7, Body: "deploy?", Options: []string{"yes", "no"}, Task: "t1"}},
	}}})
	out := b.String()
	for _, want := range []string{"a  waiting  [attention]", "1 done, 1 in progress", "current: ship", "question #7: deploy?",
		"yes | no", "agentd reply 7 TEXT", "2 unread"} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
}
