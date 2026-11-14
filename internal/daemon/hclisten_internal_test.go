package daemon

import (
	"os"
	"strings"
	"testing"
)

func TestAdmitHCPeer(t *testing.T) {
	s := newTestServer(t)
	uid := os.Getuid()
	if err := s.admitHCPeer(peerInfo{pid: os.Getpid(), uid: uid}); err != nil {
		t.Fatalf("own pid refused: %v", err)
	}
	if err := s.admitHCPeer(peerInfo{pid: os.Getpid(), uid: uid + 1}); err == nil || !strings.Contains(err.Error(), "uid") {
		t.Fatalf("another uid: %v", err)
	}
	if err := s.admitHCPeer(peerInfo{pid: 0, uid: uid}); err == nil || !strings.Contains(err.Error(), "unknown") {
		t.Fatalf("unknown pid: %v", err)
	}
	if _, err := os.Stat("/proc/self/stat"); err != nil {
		t.Skip("no /proc")
	}
	g := startWithGrandchild(t, s, "agent")
	if err := s.admitHCPeer(peerInfo{pid: g, uid: uid}); err == nil || !strings.Contains(err.Error(), `session "agent"`) {
		t.Fatalf("session grandchild: %v", err)
	}
	// The daemon's own requests (its hc listener) resolve as the user even
	// if its pid were somehow below a session.
	c, err := s.resolveCaller(peerInfo{pid: os.Getpid(), uid: uid}, nil)
	if err != nil || c.session {
		t.Fatalf("own pid: %+v %v", c, err)
	}
}
