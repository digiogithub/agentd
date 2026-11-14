package daemon_test

import (
	"bufio"
	"encoding/json"
	"errors"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/digiogithub/agentd/internal/client"
	"github.com/digiogithub/agentd/internal/config"
	"github.com/digiogithub/agentd/internal/daemon"
	"github.com/digiogithub/agentd/internal/proto"
	"github.com/digiogithub/agentd/internal/testutil"
)

// Helper mode of this test binary (see TestMain): dial the hc socket, send
// hello, write the first reply line (or the error) to the out file.
const (
	hcHelperSock = "AGENTD_TEST_HC_DIAL"
	hcHelperOut  = "AGENTD_TEST_HC_OUT"
)

func hcDialHelper(sock, out string) int {
	reply := ""
	conn, err := net.DialTimeout("unix", sock, 5*time.Second)
	if err != nil {
		reply = "dial error: " + err.Error()
	} else {
		_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
		_, _ = io.WriteString(conn, `{"jsonrpc":"2.0","id":1,"method":"hello","params":{"generation":1}}`+"\n")
		line, err := bufio.NewReader(conn).ReadString('\n')
		reply = line
		if err != nil && line == "" {
			reply = "read error: " + err.Error()
		}
		_ = conn.Close()
	}
	_ = os.WriteFile(out+".tmp", []byte(reply), 0o600)
	_ = os.Rename(out+".tmp", out)
	return 0
}

// hcConfig points herdr-core's places (XDG_RUNTIME_DIR, XDG_STATE_HOME) at
// a short temp root and returns a config with [herdr] listen = true.
func hcConfig(t *testing.T) (string, *config.Config) {
	t.Helper()
	root, err := os.MkdirTemp("", "hcd")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	t.Setenv("XDG_RUNTIME_DIR", filepath.Join(root, "run"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(root, "state"))
	cfg := config.Default()
	cfg.Herdr.Listen = true
	return root, cfg
}

func hcStatus(t *testing.T, c *client.Client, action string) proto.HCListenResult {
	t.Helper()
	r, err := c.HCListener(action)
	if err != nil {
		t.Fatalf("hc_listener %s: %v", action, err)
	}
	return r
}

func waitFile(t *testing.T, p string) string {
	t.Helper()
	var b []byte
	testutil.WaitFor(t, 15*time.Second, func() bool {
		var err error
		b, err = os.ReadFile(p)
		return err == nil
	}, "helper output "+p, nil)
	return string(b)
}

// helperCmd runs this test binary in helper mode as a plain subprocess.
func helperCmd(t *testing.T, sock string, extraEnv ...string) string {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(t.TempDir(), "reply")
	cmd := exec.Command(exe)
	var env []string
	for _, kv := range os.Environ() {
		if !strings.HasPrefix(kv, "AGENTD_TOKEN=") && !strings.HasPrefix(kv, "AGENTD_ID=") {
			env = append(env, kv)
		}
	}
	cmd.Env = append(env, append([]string{hcHelperSock + "=" + sock, hcHelperOut + "=" + out}, extraEnv...)...)
	if b, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("helper: %v: %s", err, b)
	}
	return waitFile(t, out)
}

func TestHCListenerLifecycleAndIdentity(t *testing.T) {
	root, cfg := hcConfig(t)
	e := startDaemonWith(t, cfg)
	c := e.dial(t)
	mustRun(t, c, "worker")

	var r proto.HCListenResult
	testutil.WaitFor(t, timeout, func() bool { r = hcStatus(t, c, proto.HCListenStatus); return r.Listening },
		"hc listener up", func() string { return r.Reason })
	wantSock := filepath.Join(root, "run", "herdr-core", "default.sock")
	wantLock := filepath.Join(root, "state", "herdr-core", "default", "core.lock")
	if !r.Enabled || r.Socket != wantSock || r.Lock != wantLock || r.Session != "default" || r.PID != os.Getpid() {
		t.Fatalf("status = %+v", r)
	}
	if b, err := os.ReadFile(wantLock); err != nil || string(b) != wantSock {
		t.Fatalf("core.lock = %q %v", b, err)
	}
	if fi, err := os.Stat(wantSock); err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("socket: %v %v", fi, err)
	}

	// A plain client of the user: hello and terminal.list.
	conn, err := net.Dial("unix", wantSock)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.WriteString(conn, `{"jsonrpc":"2.0","id":1,"method":"hello","params":{"generation":1}}`+"\n"+
		`{"jsonrpc":"2.0","id":2,"method":"terminal.list","params":{}}`+"\n")
	_ = conn.SetReadDeadline(time.Now().Add(timeout))
	dec := json.NewDecoder(conn)
	var hello, list map[string]any
	if err := dec.Decode(&hello); err != nil || hello["error"] != nil {
		t.Fatalf("hello: %v %v", hello, err)
	}
	if err := dec.Decode(&list); err != nil || !strings.Contains(string(mustJSON(list)), `"address":"worker"`) {
		t.Fatalf("terminal.list: %v %v", list, err)
	}
	_ = conn.Close()

	// Another process of the user (not in a session): served.
	if got := helperCmd(t, wantSock); !strings.Contains(got, `"result"`) {
		t.Fatalf("plain subprocess: %s", got)
	}
	if runtime.GOOS == "linux" {
		// A process that left a session but carries its token: refused.
		if got := helperCmd(t, wantSock, "AGENTD_TOKEN=feedface"); !strings.Contains(got, `"forbidden"`) {
			t.Fatalf("subprocess with AGENTD_TOKEN: %s", got)
		}
	}
	// A program running inside a session (ancestry): refused.
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(t.TempDir(), "reply")
	if _, err := c.Run(proto.RunArgs{Name: "probe", Cmd: []string{exe},
		Env: map[string]string{hcHelperSock: wantSock, hcHelperOut: out}}); err != nil {
		t.Fatal(err)
	}
	if got := waitFile(t, out); !strings.Contains(got, `"forbidden"`) || !strings.Contains(got, `agentd session \"probe\"`) {
		t.Fatalf("session program: %s", got)
	}
	// Sessions may not manage the listener either.
	if _, err := dialAs(t, e, "worker").HCListener(proto.HCListenStop); client.ErrorCode(err) != proto.CodeForbidden {
		t.Fatalf("session hc_listener stop: %v", err)
	}

	// stop: only the listener; the daemon and its sessions keep running.
	if r := hcStatus(t, c, proto.HCListenStop); !r.WasListening || r.Listening || !strings.Contains(r.Reason, "stopped") {
		t.Fatalf("stop = %+v", r)
	}
	for _, p := range []string{wantSock, wantLock} {
		if _, err := os.Lstat(p); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("%s after stop: %v", p, err)
		}
	}
	if err := daemon.Ping(e.sock, time.Second); err != nil {
		t.Fatal(err)
	}
	findSession(t, c, "worker")
	if r := hcStatus(t, c, proto.HCListenStop); r.WasListening {
		t.Fatalf("second stop = %+v", r)
	}
	if r := hcStatus(t, c, proto.HCListenStart); !r.Listening {
		t.Fatalf("start = %+v", r)
	}

	// Daemon shutdown removes the socket and core.lock.
	e.srv.Shutdown()
	for _, p := range []string{wantSock, wantLock} {
		if _, err := os.Lstat(p); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("%s after shutdown: %v", p, err)
		}
	}
}

func TestHCListenerOffByDefault(t *testing.T) {
	root, cfg := hcConfig(t)
	cfg.Herdr.Listen = false
	e := startDaemonWith(t, cfg)
	c := e.dial(t)
	r := hcStatus(t, c, proto.HCListenStart)
	if r.Enabled || r.Listening || !strings.Contains(r.Reason, "listen = false") {
		t.Fatalf("default = %+v", r)
	}
	if _, err := os.Lstat(filepath.Join(root, "run")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("created herdr-core paths while disabled")
	}
}

// A live herdr-core already on the socket: the daemon starts, the listener
// stays off and the foreign socket is untouched.
func TestHCListenerLeavesAnOwnedSocket(t *testing.T) {
	root2, cfg2 := hcConfig(t)
	sock := filepath.Join(root2, "run", "herdr-core", "default.sock")
	if err := os.MkdirAll(filepath.Dir(sock), 0o700); err != nil {
		t.Fatal(err)
	}
	foreign, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	defer foreign.Close()
	go func() {
		for {
			conn, err := foreign.Accept()
			if err != nil {
				return
			}
			_ = conn.Close()
		}
	}()
	e2 := startDaemonWith(t, cfg2)
	r := hcStatus(t, e2.dial(t), proto.HCListenStart)
	if !r.Enabled || r.Listening || !strings.Contains(r.Reason, "owned") {
		t.Fatalf("owned = %+v", r)
	}
	if conn, err := net.Dial("unix", sock); err != nil {
		t.Fatalf("foreign socket disturbed: %v", err)
	} else {
		_ = conn.Close()
	}
}
