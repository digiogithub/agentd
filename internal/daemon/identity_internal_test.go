package daemon

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/digiogithub/agentd/internal/config"
	"github.com/digiogithub/agentd/internal/mailbox"
	"github.com/digiogithub/agentd/internal/proto"
)

func newTestServer(t *testing.T) *Server {
	t.Helper()
	dir := t.TempDir()
	t.Setenv(config.EnvStateDir, filepath.Join(dir, "state"))
	t.Setenv(config.EnvSocket, filepath.Join(dir, "s.sock"))
	if err := config.EnsureDirs(); err != nil {
		t.Fatal(err)
	}
	st, err := mailbox.Open(filepath.Join(dir, "m.db"))
	if err != nil {
		t.Fatal(err)
	}
	s := New(nil, nil, st, nil)
	t.Cleanup(s.Shutdown)
	return s
}

// startWithGrandchild runs a session whose shell starts a background
// grandchild and writes its pid to a file; it returns that pid.
func startWithGrandchild(t *testing.T, s *Server, name string) int {
	t.Helper()
	f := filepath.Join(t.TempDir(), "pid")
	if _, err := s.run(context.Background(), proto.RunArgs{Name: name,
		Cmd: []string{"sh", "-c", `sleep 60 & echo $! > "$1.tmp"; mv "$1.tmp" "$1"; wait`, "sh", f}}); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if b, err := os.ReadFile(f); err == nil {
			if pid, err := strconv.Atoi(strings.TrimSpace(string(b))); err == nil {
				return pid
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("no grandchild pid")
	return 0
}

func TestResolveByProcessAncestry(t *testing.T) {
	if _, err := os.Stat("/proc/self/stat"); err != nil {
		t.Skip("no /proc")
	}
	s := newTestServer(t)
	gA := startWithGrandchild(t, s, "a")
	gB := startWithGrandchild(t, s, "b")
	uid := os.Getuid()

	c, err := s.resolveCaller(peerInfo{pid: gA, uid: uid}, nil)
	if err != nil || c.id != "a" || !c.session || c.via != viaProcess {
		t.Fatalf("grandchild of a: %+v %v", c, err)
	}
	// Env stripped (no auth at all): still a. With the right hint: a.
	if c, err := s.resolveCaller(peerInfo{pid: gB, uid: uid}, &proto.Auth{ID: "b"}); err != nil || c.id != "b" {
		t.Fatalf("grandchild of b: %+v %v", c, err)
	}
	// Ancestry beats claims: hint or token of another session is rejected.
	if _, err := s.resolveCaller(peerInfo{pid: gA, uid: uid}, &proto.Auth{ID: "b"}); err == nil {
		t.Fatal("hint b accepted from a's process")
	}
	tokB := s.SessionTokenForTest("b")
	if _, err := s.resolveCaller(peerInfo{pid: gA, uid: uid}, &proto.Auth{Token: tokB}); err == nil ||
		!strings.Contains(err.Error(), "belongs to session") {
		t.Fatalf("token of b from a's process: %v", err)
	}
	// The test process itself is not in any session: human.
	c, err = s.resolveCaller(peerInfo{pid: os.Getpid(), uid: uid}, nil)
	if err != nil || c.session || c.id != humanAddr(uid) || c.via != viaUID {
		t.Fatalf("test process: %+v %v", c, err)
	}
	// Unknown pid (0 = macOS without LOCAL_PEERPID) falls back to the token.
	c, err = s.resolveCaller(peerInfo{pid: 0, uid: uid}, &proto.Auth{ID: "b", Token: tokB})
	if err != nil || c.id != "b" || c.via != viaToken {
		t.Fatalf("token fallback: %+v %v", c, err)
	}
}

func TestTokensAreUniqueAndRandom(t *testing.T) {
	a, b := newToken(), newToken()
	if len(a) != 64 || a == b {
		t.Fatalf("tokens %q %q", a, b)
	}
}

func TestHumanAddr(t *testing.T) {
	a := humanAddr(os.Getuid())
	if !validHumanAddr(a) {
		t.Fatalf("humanAddr %q is not a valid human address", a)
	}
	for _, bad := range []string{"human:", "human:a b", "human:a:b", "alice", "human:ü"} {
		if validHumanAddr(bad) {
			t.Errorf("%q accepted", bad)
		}
	}
}
