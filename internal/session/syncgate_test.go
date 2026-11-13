package session

import (
	"bytes"
	"testing"
	"time"
)

func TestOpenSyncTail(t *testing.T) {
	bsu, esu := string(syncBegin), string(syncEnd)
	cases := []struct {
		name string
		in   string
		want int
	}{
		{"plain", "hello", -1},
		{"empty", "", -1},
		{"complete frame", "a" + bsu + "xx" + esu, -1},
		{"ends inside frame", "ab" + bsu + "xx", 2},
		{"complete then open", bsu + "x" + esu + "yy" + bsu + "z", 0},
		{"only begin", bsu, 0},
	}
	// "complete then open": index of the second BSU.
	cases[4].want = len(bsu) + 1 + len(esu) + 2
	for _, c := range cases {
		if got := openSyncTail([]byte(c.in)); got != c.want {
			t.Errorf("%s: openSyncTail = %d, want %d", c.name, got, c.want)
		}
	}
}

// TestFeedHoldsOpenFrame drives feed directly on a silent child (sleep) so
// OutputGen counts exactly the chunks handed to handleOutput.
func TestFeedHoldsOpenFrame(t *testing.T) {
	s, err := Start(Spec{Name: "gate", Cmd: []string{"sleep", "60"}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	bsu, esu := string(syncBegin), string(syncEnd)

	// Complete frame: nothing held.
	s.feed([]byte("a" + bsu + "x" + esu))
	if len(heldOf(s)) != 0 {
		t.Fatal("complete frame was held")
	}
	// Chunk ending inside a frame: the tail is held, the head processed.
	before := s.OutputGen()
	s.feed([]byte("b" + bsu + "yy"))
	if got := heldOf(s); !bytes.Equal(got, []byte(bsu+"yy")) {
		t.Fatalf("held = %q", got)
	}
	if s.OutputGen() != before+1 {
		t.Fatalf("head not processed: gen %d -> %d", before, s.OutputGen())
	}
	// The rest of the frame arrives: released together, nothing held.
	s.feed([]byte("zz" + esu))
	if len(heldOf(s)) != 0 || s.OutputGen() != before+2 {
		t.Fatalf("frame not released: held=%q gen=%d", heldOf(s), s.OutputGen())
	}
	// Timeout release: an unterminated frame is flushed after syncHold.
	s.feed([]byte(bsu + "never ends"))
	if len(heldOf(s)) == 0 {
		t.Fatal("open frame not held")
	}
	gen := s.OutputGen()
	deadline := time.Now().Add(syncHold + 2*time.Second)
	for len(heldOf(s)) != 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if len(heldOf(s)) != 0 || s.OutputGen() != gen+1 {
		t.Fatalf("timeout release failed: held=%q gen %d -> %d", heldOf(s), gen, s.OutputGen())
	}
	// Size release: a frame above syncHoldMax is not held.
	big := append([]byte(bsu), bytes.Repeat([]byte("x"), syncHoldMax)...)
	gen = s.OutputGen()
	s.feed(big)
	if len(heldOf(s)) != 0 || s.OutputGen() != gen+1 {
		t.Fatalf("oversized frame held: gen %d -> %d", gen, s.OutputGen())
	}
}

func heldOf(s *Session) []byte {
	s.readMu.Lock()
	defer s.readMu.Unlock()
	return append([]byte(nil), s.held...)
}
