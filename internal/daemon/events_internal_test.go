package daemon

import (
	"testing"
	"time"

	"github.com/digiogithub/agentd/internal/proto"
)

// A subscriber that never reads must not block publishers: on overflow its
// queue is flushed and it gets exactly one resync, then continues live.
func TestBusOverflowResyncNeverBlocks(t *testing.T) {
	b := newBus("e1")
	sub, _ := b.subscribe(eventFilter{}, 0, "")
	for i := 0; i < SubscriberQueue; i++ {
		b.publish(proto.EvViewChanged, "a", "", "", nil)
	}
	if len(sub.ch) != SubscriberQueue {
		t.Fatalf("queue = %d, want full", len(sub.ch))
	}
	b.publish(proto.EvViewChanged, "a", "", "", nil) // overflow
	if len(sub.ch) != 1 {
		t.Fatalf("after overflow queue = %d, want exactly the resync", len(sub.ch))
	}
	if ev := <-sub.ch; ev.Kind != proto.EvResync || ev.Seq != 0 {
		t.Fatalf("got %+v, want resync", ev)
	}
	b.publish(proto.EvViewChanged, "a", "", "", nil)
	if ev := <-sub.ch; ev.Kind != proto.EvViewChanged || ev.Seq != SubscriberQueue+2 {
		t.Fatalf("live event after resync = %+v", ev)
	}

	start := time.Now()
	for i := 0; i < 100000; i++ {
		b.publish(proto.EvViewChanged, "a", "", "", nil)
	}
	if d := time.Since(start); d > 5*time.Second {
		t.Fatalf("publishing to a stuck subscriber took %v", d)
	}
	b.unsubscribe(sub)
}

func TestBusRingWrapReplay(t *testing.T) {
	b := newBus("e1")
	for i := 0; i < ReplayRing+10; i++ {
		b.publish(proto.EvViewChanged, "a", "", "", nil)
	}
	last := b.lastSeq()
	sub, _ := b.subscribe(eventFilter{}, last-5, "e1")
	if sub.firstResync != "" || len(sub.replay) != 5 || sub.replay[0].Seq != last-4 {
		t.Fatalf("replay = %d first %q", len(sub.replay), sub.firstResync)
	}
	b.unsubscribe(sub)
	sub, _ = b.subscribe(eventFilter{}, 9, "e1")
	if sub.firstResync != proto.ResyncSinceTooOld || len(sub.replay) != 0 {
		t.Fatalf("old since_seq: %q", sub.firstResync)
	}
	b.unsubscribe(sub)
}
