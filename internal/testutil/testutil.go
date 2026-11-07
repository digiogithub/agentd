// Package testutil holds helpers shared by agentd tests (session, delivery,
// daemon end-to-end tests).
package testutil

import (
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

const fakeAgentPkg = "github.com/digiogithub/agentd/internal/testutil/fakeagent"

var (
	fakeOnce sync.Once
	fakePath string
	fakeErr  error
	fakeOut  []byte
)

// BuildFakeAgent compiles internal/testutil/fakeagent once per test binary and
// returns the path of the executable. The binary lives in a temp directory
// that is not removed automatically; call CleanupFakeAgent from TestMain if
// that matters.
func BuildFakeAgent(t testing.TB) string {
	t.Helper()
	fakeOnce.Do(func() {
		dir, err := os.MkdirTemp("", "agentd-fakeagent-")
		if err != nil {
			fakeErr = err
			return
		}
		bin := filepath.Join(dir, "fakeagent")
		cmd := exec.Command(goTool(), "build", "-o", bin, fakeAgentPkg)
		fakeOut, fakeErr = cmd.CombinedOutput()
		if fakeErr == nil {
			fakePath = bin
		}
	})
	if fakeErr != nil {
		t.Fatalf("build fakeagent: %v\n%s", fakeErr, fakeOut)
	}
	return fakePath
}

// CleanupFakeAgent removes the binary built by BuildFakeAgent, if any.
func CleanupFakeAgent() {
	if fakePath != "" {
		_ = os.RemoveAll(filepath.Dir(fakePath))
	}
}

func goTool() string {
	if p, err := exec.LookPath("go"); err == nil {
		return p
	}
	if root := os.Getenv("GOROOT"); root != "" {
		return filepath.Join(root, "bin", "go")
	}
	return "go" // let exec report the missing toolchain
}

// WaitFor polls cond every 10ms until it returns true or timeout elapses, in
// which case it fails the test with msg (and the output of describe, if set).
func WaitFor(t testing.TB, timeout time.Duration, cond func() bool, msg string, describe func() string) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		if cond() {
			return
		}
		if time.Now().After(deadline) {
			extra := ""
			if describe != nil {
				extra = "\n" + describe()
			}
			t.Fatalf("timeout after %v: %s%s", timeout, msg, extra)
		}
		time.Sleep(10 * time.Millisecond)
	}
}
