package hcompat_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/digiogithub/agentd/internal/client"
	"github.com/digiogithub/agentd/internal/hcompat"
	"github.com/digiogithub/agentd/internal/proto"
	"github.com/digiogithub/agentd/internal/testutil"
)

// hcPaths returns a short temp root (unix socket paths are limited to ~108
// bytes) and the socket/lock paths of an hc listener under it, in dirs that
// do not exist yet.
func hcPaths(t *testing.T) (root, sock, lock string) {
	t.Helper()
	root, err := os.MkdirTemp("", "hcl")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	return root, filepath.Join(root, "run", "herdr-core", "default.sock"),
		filepath.Join(root, "state", "herdr-core", "default", "core.lock")
}

func newListener(t *testing.T, daemonSock, sock, lock string) *hcompat.Listener {
	t.Helper()
	l := &hcompat.Listener{
		Server: &hcompat.Server{
			Dial: func() (*client.Client, error) { return client.Dial(daemonSock) },
			Core: hcompat.CoreInfo{ID: "test-core", Name: "testhost"},
		},
		Socket: sock,
		Lock:   lock,
	}
	t.Cleanup(l.Close)
	return l
}

func mode(t *testing.T, p string) os.FileMode {
	t.Helper()
	fi, err := os.Lstat(p)
	if err != nil {
		t.Fatal(err)
	}
	return fi.Mode()
}

// A plain client reaches agentd through the socket exactly as through `hc
// connect`: hello, terminal.list, attach; Close removes everything it made.
func TestListenerServesAndCleansUp(t *testing.T) {
	dsock := startDaemon(t)
	runSession(t, dsock, "cat@proj", "sh", "-c", "echo ready-sock; exec cat")
	root, sock, lock := hcPaths(t)
	l := newListener(t, dsock, sock, lock)
	if err := l.Listen(); err != nil {
		t.Fatal(err)
	}
	go l.Serve()

	if m := mode(t, sock); m.Type() != os.ModeSocket || m.Perm() != 0o600 {
		t.Fatalf("socket mode %v", m)
	}
	for _, d := range []string{filepath.Dir(sock), filepath.Dir(lock)} {
		if m := mode(t, d); m.Perm() != 0o700 {
			t.Fatalf("%s mode %v", d, m)
		}
	}
	if b, err := os.ReadFile(lock); err != nil || string(b) != sock {
		t.Fatalf("core.lock = %q, %v (want the socket path, as herdr-core writes it)", b, err)
	}

	conn, err := net.Dial("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	c := newHCClientConn(t, conn)
	if res := c.helloFramed(); res["generation"].(float64) != 1 {
		t.Fatalf("hello = %v", res)
	}
	list := result(t, c.call("terminal.list", map[string]any{}))
	terms := list["terminals"].([]any)
	if len(terms) != 1 || terms[0].(map[string]any)["address"] != "cat@proj" {
		t.Fatalf("terminal.list = %v", list)
	}
	testutil.WaitFor(t, 10*time.Second, func() bool {
		cl, err := client.Dial(dsock)
		if err != nil {
			return false
		}
		defer cl.Close()
		s, err := cl.Dump(proto.DumpArgs{Name: "cat@proj"})
		return err == nil && strings.Contains(s, "ready-sock")
	}, "program output", nil)
	att := result(t, c.call("terminal.attach", map[string]any{"terminal": "cat@proj", "cols": 80, "rows": 24}))
	ch := uint32(att["channel"].(float64))
	c.readStream(ch, func(b []byte) bool { return bytes.Contains(b, []byte("ready-sock")) })
	c.send(ch, hcompat.KindData, []byte("via-socket\r"))
	c.readStream(ch, func(b []byte) bool { return bytes.Count(b, []byte("via-socket")) >= 2 })

	// A plain-mode client (one request per connection, like the relay).
	pc, err := net.Dial("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	defer pc.Close()
	if _, err := io.WriteString(pc, `{"jsonrpc":"2.0","id":"hello","method":"hello","params":{"generation":1}}`+"\n"+
		`{"jsonrpc":"2.0","id":"request","method":"terminal.list","params":{}}`+"\n"); err != nil {
		t.Fatal(err)
	}
	dec := json.NewDecoder(pc)
	for _, want := range []string{"hello", "request"} {
		var m map[string]any
		if err := dec.Decode(&m); err != nil || m["id"] != want || m["error"] != nil {
			t.Fatalf("plain reply %v: %v", m, err)
		}
	}

	l.Close()
	for _, p := range []string{sock, lock, filepath.Join(root, "run"), filepath.Join(root, "state")} {
		if _, err := os.Lstat(p); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("%s left behind after Close (%v)", p, err)
		}
	}
	if _, err := os.Lstat(root); err != nil {
		t.Fatal("Close removed a directory it did not create")
	}
	l.Close() // idempotent
}

// A live socket that is not ours (a real herdr-core without our lock, or
// any program) is never replaced; neither is a regular file.
func TestListenerNeverBindsOverALiveSocket(t *testing.T) {
	_, sock, lock := hcPaths(t)
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
			c, err := foreign.Accept()
			if err != nil {
				return
			}
			_ = c.Close()
		}
	}()
	l := newListener(t, "/nonexistent", sock, lock)
	if err := l.Listen(); !errors.Is(err, hcompat.ErrOwned) {
		t.Fatalf("Listen over a live socket: %v", err)
	}
	if conn, err := net.Dial("unix", sock); err != nil {
		t.Fatalf("the foreign socket was disturbed: %v", err)
	} else {
		_ = conn.Close()
	}
	// No discovery file pointing at a socket we do not serve: the lock file
	// it made is gone with its (empty) directories.
	if _, err := os.Lstat(filepath.Dir(lock)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("core.lock dir left after a failed Listen: %v", err)
	}

	_, sock2, lock2 := hcPaths(t)
	if err := os.MkdirAll(filepath.Dir(sock2), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(sock2, []byte("not a socket"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := newListener(t, "/nonexistent", sock2, lock2).Listen(); !errors.Is(err, hcompat.ErrOwned) {
		t.Fatalf("Listen over a regular file: %v", err)
	}
	if b, _ := os.ReadFile(sock2); string(b) != "not a socket" {
		t.Fatal("the regular file was replaced")
	}
}

// A herdr-core holding core.lock owns the session, even before it binds.
func TestListenerRespectsCoreLock(t *testing.T) {
	_, sock, lock := hcPaths(t)
	if err := os.MkdirAll(filepath.Dir(lock), 0o700); err != nil {
		t.Fatal(err)
	}
	held, err := os.OpenFile(lock, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer held.Close()
	if err := syscall.Flock(int(held.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		t.Fatal(err)
	}
	if err := newListener(t, "/nonexistent", sock, lock).Listen(); !errors.Is(err, hcompat.ErrOwned) {
		t.Fatalf("Listen with core.lock held: %v", err)
	}
	if _, err := os.Lstat(sock); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("bound although core.lock is held")
	}
	if _, err := os.Lstat(lock); err != nil {
		t.Fatal("removed the lock file of the core that holds it")
	}
}

// A stale socket file (nothing answers) is replaced.
func TestListenerReplacesStaleSocket(t *testing.T) {
	_, sock, lock := hcPaths(t)
	if err := os.MkdirAll(filepath.Dir(sock), 0o700); err != nil {
		t.Fatal(err)
	}
	stale, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	stale.(*net.UnixListener).SetUnlinkOnClose(false)
	_ = stale.Close()
	l := newListener(t, "/nonexistent", sock, lock)
	if err := l.Listen(); err != nil {
		t.Fatalf("Listen over a stale socket: %v", err)
	}
	go l.Serve()
	if conn, err := net.Dial("unix", sock); err != nil {
		t.Fatal(err)
	} else {
		_ = conn.Close()
	}
	l.Close()
	// The socket dir existed before: it stays.
	if _, err := os.Stat(filepath.Dir(sock)); err != nil {
		t.Fatal("removed a pre-existing directory")
	}
}

// A refused peer gets a forbidden error for its first request, then EOF.
func TestListenerRefusal(t *testing.T) {
	_, sock, lock := hcPaths(t)
	l := newListener(t, "/nonexistent", sock, lock)
	l.Admit = func(net.Conn) error { return errors.New("agentd session \"x\" may not use the hc socket") }
	if err := l.Listen(); err != nil {
		t.Fatal(err)
	}
	go l.Serve()
	conn, err := net.Dial("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if _, err := io.WriteString(conn, `{"jsonrpc":"2.0","id":7,"method":"hello","params":{"framed":true}}`+"\n"); err != nil {
		t.Fatal(err)
	}
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	b, _ := io.ReadAll(conn)
	var m map[string]any
	if err := json.Unmarshal(bytes.TrimSpace(b), &m); err != nil {
		t.Fatalf("refusal %q: %v", b, err)
	}
	if errKind(m) != "forbidden" || m["id"].(float64) != 7 || !strings.Contains(string(b), "may not use") {
		t.Fatalf("refusal = %s", b)
	}
}
