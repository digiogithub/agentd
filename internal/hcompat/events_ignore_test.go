package hcompat

import (
	"encoding/json"
	"testing"

	"github.com/digiogithub/agentd/internal/proto"
)

// Events the translator does not know (task.*, the kind field of mail events,
// anything future) are ignored: the stream stays open and nothing is emitted.
func TestTranslateIgnoresTaskAndMailKinds(t *testing.T) {
	sub := &subscription{known: map[string]proto.SessionInfo{}, visible: map[string]bool{"a": true},
		out: make(chan []byte, 4), done: make(chan struct{})}
	for _, ev := range []proto.Event{
		{Kind: proto.EvTaskChanged, Session: "a", Data: json.RawMessage(`{"revision":1,"counts":{},"changed":[]}`)},
		{Kind: proto.EvMailQueued, Data: json.RawMessage(`{"id":1,"from":"a","to":"human:x","kind":"question"}`)},
		{Kind: "task.future", Session: "a", Data: json.RawMessage(`{`)},
	} {
		if !sub.translate(ev) {
			t.Errorf("%s ended the stream", ev.Kind)
		}
	}
	if len(sub.out) != 0 {
		t.Errorf("%d notifications emitted", len(sub.out))
	}
}
