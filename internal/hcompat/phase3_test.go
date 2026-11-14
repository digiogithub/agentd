package hcompat_test

import (
	"strings"
	"testing"
	"time"

	"github.com/digiogithub/agentd/internal/hcompat"
	"github.com/digiogithub/agentd/internal/testutil"
)

// terminal.send pastes and submits through the daemon's act prompt (paste
// verified), keys are translated from hc names, wait returns WaitResult.
func TestSendKeysWait(t *testing.T) {
	sock := startDaemon(t)
	runSession(t, sock, "cat@proj", "sh", "-c", "echo ready; exec cat")
	c := newHCClient(t, sock)
	c.helloFramed()
	d := loadSchema(t)

	var r map[string]any
	testutil.WaitFor(t, 15*time.Second, func() bool {
		m := c.call("terminal.send", map[string]any{"terminal": "cat@proj", "text": "hello from send"})
		if m["error"] != nil {
			return false // still starting: retry until the daemon sees it idle
		}
		r = m["result"].(map[string]any)
		return true
	}, "send accepted", nil)
	checkResult(t, d, "terminal.send", r)
	testutil.WaitFor(t, 10*time.Second, func() bool {
		return strings.Count(screen(t, sock, "cat@proj"), "hello from send") >= 2 // echo + cat output
	}, "sent text submitted", func() string { return screen(t, sock, "cat@proj") })

	k := result(t, c.call("terminal.keys", map[string]any{"terminal": "cat@proj", "keys": []string{"a", "b", "Return"}}))
	checkResult(t, d, "terminal.keys", k)
	testutil.WaitFor(t, 10*time.Second, func() bool { return strings.Contains(screen(t, sock, "cat@proj"), "ab\n") },
		"keys typed", func() string { return screen(t, sock, "cat@proj") })
	if kind := errKind(c.call("terminal.keys", map[string]any{"terminal": "cat@proj", "keys": []string{"hyper+q"}})); kind != "invalid_params" {
		t.Fatalf("unknown key: kind %q", kind)
	}

	w := result(t, c.call("terminal.wait", map[string]any{"terminal": "cat@proj", "timeout": 5}))
	checkResult(t, d, "terminal.wait", w)
	if w["ended"] != true || w["reason"] != "status" {
		t.Fatalf("wait on an idle terminal = %v", w)
	}
	if kind := errKind(c.call("terminal.wait", map[string]any{})); kind != "needs_target" {
		t.Fatalf("wait without terminal: kind %q", kind)
	}
	if kind := errKind(c.call("terminal.send", map[string]any{"terminal": "cat@proj", "text": "x", "if": "working"})); kind != "status_mismatch" {
		t.Fatalf("send if working: kind %q", kind)
	}
}

func TestWaitTimeoutAndExit(t *testing.T) {
	sock := startDaemon(t)
	runSession(t, sock, "busy@proj", "sh", "-c", "while :; do echo tick; sleep 0.1; done")
	runSession(t, sock, "short@proj", "sh", "-c", "sleep 1.5; exit 4")
	c := newHCClient(t, sock)
	c.helloFramed()
	w := result(t, c.call("terminal.wait", map[string]any{"terminal": "busy@proj", "timeout": 1.5}))
	if w["ended"] != false || w["reason"] != "timeout" {
		t.Fatalf("wait on a working terminal = %v", w)
	}
	w = result(t, c.call("terminal.wait", map[string]any{"terminal": "short@proj", "timeout": 20}))
	if w["ended"] != true || w["reason"] != "exit" || w["exit_code"].(float64) != 4 {
		t.Fatalf("wait until exit = %v", w)
	}
	checkResult(t, loadSchema(t), "terminal.wait", w)
}

func TestInterrupt(t *testing.T) {
	sock := startDaemon(t)
	runSession(t, sock, "loop@proj", "sh", "-c", "trap 'echo got-int; exit 130' INT; while :; do echo tick; sleep 0.1; done")
	c := newHCClient(t, sock)
	c.helloFramed()
	testutil.WaitFor(t, 10*time.Second, func() bool { return sessionInfo(t, sock, "loop@proj").Status == "working" }, "working", nil)
	r := result(t, c.call("terminal.interrupt", map[string]any{"terminal": "loop@proj", "timeout": 5}))
	checkResult(t, loadSchema(t), "terminal.interrupt", r)
	if r["key"] != "ctrl+c" || (r["outcome"] != "exited" && r["outcome"] != "stopped") {
		t.Fatalf("interrupt = %v", r)
	}
}

func TestReadOnlyAndAllowRun(t *testing.T) {
	sock := startDaemon(t)
	runSession(t, sock, "cat@proj", "cat")
	ro := newHCClient(t, sock, func(s *hcompat.Server) { s.ReadOnly = true })
	ro.helloFramed()
	for _, m := range []string{"terminal.send", "terminal.keys", "terminal.kill", "terminal.label", "doc.set", "terminal.run"} {
		if k := errKind(ro.call(m, map[string]any{"terminal": "cat@proj", "text": "x", "keys": []string{"a"}, "labels": map[string]string{"a": "b"}, "name": "d", "value": 1, "version": 0})); k != "forbidden" {
			t.Errorf("%s read-only: kind %q", m, k)
		}
	}
	result(t, ro.call("terminal.list", map[string]any{}))
	ch := attach(t, ro, map[string]any{"terminal": "cat@proj", "cols": 100, "rows": 30})
	ro.readStream(ch, func(b []byte) bool { return len(b) > 0 })
	ro.send(ch, hcompat.KindData, []byte("ro-typed\r"))
	time.Sleep(300 * time.Millisecond)
	if strings.Contains(screen(t, sock, "cat@proj"), "ro-typed") || sessionInfo(t, sock, "cat@proj").Cols != 80 {
		t.Fatal("read-only attach typed or resized")
	}

	rw := newHCClient(t, sock, func(s *hcompat.Server) { s.AllowRun = func(p string) bool { return p == "sleep" } })
	rw.helloFramed()
	if k := errKind(rw.call("terminal.run", map[string]any{"argv": []string{"sh"}, "cwd": t.TempDir()})); k != "forbidden" {
		t.Fatalf("run not allowed: kind %q", k)
	}
	result(t, rw.call("terminal.run", map[string]any{"argv": []string{"sleep", "30"}, "cwd": t.TempDir()}))
}
