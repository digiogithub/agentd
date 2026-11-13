// Package testutil holds helpers shared by agentd tests (session, delivery,
// daemon end-to-end tests).
package testutil

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
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

var (
	slowOnce sync.Once
	slowVal  float64
)

// Slow is the factor by which timing-sensitive tests should stretch their
// waits and the pace of their scripted children, so they keep passing on a
// loaded machine. It is the largest of env AGENTD_TEST_SLOW (a number, default
// 1, at most 20) and the 1-minute load average per CPU read from /proc/loadavg
// (Linux; 1 when unavailable or below 1; at most 8), computed once per test
// binary. Assertions must not be relaxed with it, only timeouts and child
// delays.
func Slow() float64 {
	slowOnce.Do(func() {
		slowVal = 1
		if v, err := strconv.ParseFloat(os.Getenv("AGENTD_TEST_SLOW"), 64); err == nil && v > 1 {
			slowVal = min(v, 20)
		}
		if b, err := os.ReadFile("/proc/loadavg"); err == nil {
			if f := strings.Fields(string(b)); len(f) > 0 {
				if l, err := strconv.ParseFloat(f[0], 64); err == nil {
					if per := l / float64(runtime.NumCPU()); per > slowVal {
						slowVal = min(per, 8)
					}
				}
			}
		}
	})
	return slowVal
}

// Scale multiplies d by Slow.
func Scale(d time.Duration) time.Duration { return time.Duration(float64(d) * Slow()) }

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
