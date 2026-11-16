package core

import (
	"net"
	"testing"

	"github.com/digiogithub/agentd/internal/proto"
)

// Focus reports become F frames; everything else, a lone CSI I inside other
// input included, stays a D frame.
func TestAttachPTYFocusFrames(t *testing.T) {
	a, b := net.Pipe()
	defer a.Close()
	defer b.Close()
	p := newAttachPTY(a, false)
	writes := []string{"\x1b[I", "\x1b[O", "abc", "x\x1b[I"}
	go func() {
		for _, w := range writes {
			if _, err := p.Write([]byte(w)); err != nil {
				return
			}
		}
	}()
	want := []struct {
		typ     byte
		payload string
	}{
		{proto.FrameFocus, "\x01"}, {proto.FrameFocus, "\x00"},
		{proto.FrameData, "abc"}, {proto.FrameData, "x\x1b[I"},
	}
	for i, w := range want {
		f, err := proto.ReadFrame(b)
		if err != nil {
			t.Fatal(err)
		}
		if f.Type != w.typ || string(f.Payload) != w.payload {
			t.Errorf("frame %d = %c %q, want %c %q", i, f.Type, f.Payload, w.typ, w.payload)
		}
	}
}
