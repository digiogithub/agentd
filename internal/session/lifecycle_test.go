package session_test

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/digiogithub/agentd/internal/session"
	"github.com/digiogithub/agentd/internal/testutil"
)

// deafCmd is a child that never reads its stdin: the PTY input queue fills up
// and writes stop making progress.
var deafCmd = []string{"sh", "-c", "stty raw -echo; echo deaf-ready; exec sleep 1000"}

// Regression: a write to a child that stopped reading stdin used to block
// forever while holding the input lock.
func TestWriteTimesOutWhenChildStopsReading(t *testing.T) {
	s := start(t, session.Spec{Name: "deaf", Cmd: deafCmd, WriteTimeout: 300 * time.Millisecond})
	waitScreen(t, s, "deaf-ready")
	begin := time.Now()
	n, err := s.Write([]byte(strings.Repeat("x", 1<<20)), false)
	if !errors.Is(err, session.ErrWriteTimeout) {
		t.Fatalf("write err = %v (n=%d), want ErrWriteTimeout", err, n)
	}
	if n >= 1<<20 {
		t.Fatalf("n = %d, expected a partial write", n)
	}
	if d := time.Since(begin); d > 5*time.Second {
		t.Fatalf("write took %v", d)
	}
	// The input lock was released: a further write fails fast too. It must
	// be larger than any room the tty may have freed meanwhile, or a short
	// write can slip through and succeed.
	begin = time.Now()
	if _, err := s.Write([]byte(strings.Repeat("y", 1<<20)), false); !errors.Is(err, session.ErrWriteTimeout) {
		t.Fatalf("second write err = %v", err)
	}
	if d := time.Since(begin); d > 5*time.Second {
		t.Fatalf("second write took %v", d)
	}
}

// Regression: Close must abort a write blocked on a deaf child immediately,
// not only after the write timeout.
func TestCloseAbortsBlockedWrite(t *testing.T) {
	s := start(t, session.Spec{Name: "deaf", Cmd: deafCmd, WriteTimeout: time.Minute})
	waitScreen(t, s, "deaf-ready")
	errc := make(chan error, 1)
	go func() {
		_, err := s.Write([]byte(strings.Repeat("x", 1<<20)), false)
		errc <- err
	}()
	time.Sleep(300 * time.Millisecond) // let the write block
	closed := make(chan struct{})
	go func() { _ = s.Close(); close(closed) }()
	select {
	case err := <-errc:
		if !errors.Is(err, session.ErrExited) {
			t.Fatalf("write err = %v, want ErrExited", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("blocked write not aborted by Close")
	}
	select {
	case <-closed:
	case <-time.After(timeout):
		t.Fatal("Close hung")
	}
}

// A slow but steady reader never hits the (per-chunk) write timeout.
func TestWriteSlowReaderNoTimeout(t *testing.T) {
	s := start(t, session.Spec{Name: "slow", WriteTimeout: 2 * time.Second,
		Cmd: []string{"sh", "-c", "stty raw -echo; exec cat >/dev/null"}})
	time.Sleep(200 * time.Millisecond)
	if _, err := s.Write([]byte(strings.Repeat("z", 256<<10)), false); err != nil {
		t.Fatalf("write: %v", err)
	}
}

func TestLogRotation(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "r.log")
	s := start(t, session.Spec{Name: "r", LogPath: logPath, LogMaxBytes: 64 << 10,
		Cmd: []string{"sh", "-c", "i=0; while [ $i -lt 20000 ]; do echo line-$i; i=$((i+1)); done"}})
	waitDone(t, s)
	cur, err := os.Stat(logPath)
	if err != nil {
		t.Fatal(err)
	}
	old, err := os.Stat(logPath + ".1")
	if err != nil {
		t.Fatalf("no rotated log: %v", err)
	}
	if cur.Size() > 64<<10 || old.Size() > 64<<10 {
		t.Fatalf("log sizes %d / %d exceed cap", cur.Size(), old.Size())
	}
	if old.Mode().Perm() != 0o600 || cur.Mode().Perm() != 0o600 {
		t.Fatalf("log modes %v / %v", old.Mode().Perm(), cur.Mode().Perm())
	}
	data, _ := os.ReadFile(logPath)
	if !strings.Contains(string(data), "line-19999") {
		t.Fatal("current log does not end with the latest output")
	}
}

func TestBaseEnv(t *testing.T) {
	s := start(t, session.Spec{Name: "env", BaseEnv: []string{"PATH=" + os.Getenv("PATH"), "ONLY_BASE=b1", "OVER=base"},
		Env: map[string]string{"OVER": "extra"},
		Cmd: []string{"sh", "-c", `echo "[$ONLY_BASE|$OVER|$TERM|${HOME:-nohome}]"; sleep 10`}})
	testutil.WaitFor(t, timeout, func() bool { return strings.Contains(s.Screen(), "[b1|extra|xterm-256color|nohome]") },
		"child env should be BaseEnv + Env", func() string { return s.Screen() })
}
