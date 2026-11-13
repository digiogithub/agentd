package daemon_test

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/digiogithub/agentd/internal/client"
	"github.com/digiogithub/agentd/internal/config"
	"github.com/digiogithub/agentd/internal/daemon"
	"github.com/digiogithub/agentd/internal/mailbox"
	"github.com/digiogithub/agentd/internal/proto"
	"github.com/digiogithub/agentd/internal/testutil"
)

const timeout = 10 * time.Second

func TestMain(m *testing.M) {
	// Session-scoped variables must not leak in when run inside an agentd
	// session.
	for _, k := range []string{"AGENTD_TOKEN", "AGENTD_ID", "AGENTD_SOCKET"} {
		os.Unsetenv(k)
	}
	code := m.Run()
	testutil.CleanupFakeAgent()
	os.Exit(code)
}

type env struct {
	srv  *daemon.Server
	sock string
	errc chan error
}

// startDaemon runs an in-process daemon on a temp socket and state dir,
// without the delivery loop (tests drive the mailbox directly).
func startDaemon(t *testing.T) *env {
	t.Helper()
	// Short dir: unix socket paths are limited to ~108 bytes.
	dir, err := os.MkdirTemp("", "agentd-d")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	sock := filepath.Join(dir, "agentd.sock")
	t.Setenv(config.EnvSocket, sock)
	t.Setenv(config.EnvStateDir, filepath.Join(dir, "state"))
	t.Setenv(config.EnvConfig, filepath.Join(dir, "config.toml")) // missing = defaults
	clearIdentityEnv(t)
	if err := config.EnsureDirs(); err != nil {
		t.Fatal(err)
	}
	dbPath, _ := config.MailboxPath()
	store, err := mailbox.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	srv := daemon.New(config.Default(), nil, store, nil)
	if err := srv.Listen(); err != nil {
		t.Fatal(err)
	}
	e := &env{srv: srv, sock: sock, errc: make(chan error, 1)}
	go func() { e.errc <- srv.Serve(context.Background()) }()
	t.Cleanup(srv.Shutdown)
	return e
}

func (e *env) dial(t *testing.T) *client.Client {
	t.Helper()
	c, err := client.Dial(e.sock)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return c
}

func waitDump(t *testing.T, c *client.Client, name, want string) {
	t.Helper()
	var last string
	testutil.WaitFor(t, timeout, func() bool {
		last, _ = c.Dump(proto.DumpArgs{Name: name})
		return strings.Contains(last, want)
	}, name+" screen should contain "+want, func() string { return last })
}

func findSession(t *testing.T, c *client.Client, name string) proto.SessionInfo {
	t.Helper()
	ss, err := c.Ls()
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range ss {
		if s.Name == name {
			return s
		}
	}
	t.Fatalf("session %s not listed: %+v", name, ss)
	return proto.SessionInfo{}
}

func TestEndToEnd(t *testing.T) {
	e := startDaemon(t)
	c := e.dial(t)
	bin := testutil.BuildFakeAgent(t)
	cwd := t.TempDir()

	if _, err := c.Ping(); err != nil {
		t.Fatal(err)
	}
	// run: validation, defaults, uniqueness.
	if _, err := c.Run(proto.RunArgs{Name: "bad name", Cmd: []string{bin}}); err == nil {
		t.Fatal("invalid name accepted")
	}
	info, err := c.Run(proto.RunArgs{Name: "fake@a", Cwd: cwd, Cmd: []string{bin}, Profile: "claude"})
	if err != nil {
		t.Fatal(err)
	}
	if info.State != "running" || info.Pid == 0 || info.Profile != "claude" || info.Cwd != cwd ||
		info.Cols != 160 || info.Rows != 50 {
		t.Fatalf("run info %+v", info)
	}
	if _, err := c.Run(proto.RunArgs{Name: "fake@a", Cmd: []string{bin}}); err == nil {
		t.Fatal("duplicate name accepted")
	}
	if _, err := c.Run(proto.RunArgs{Name: "fake@b", Cmd: []string{bin}, Cols: 90, Rows: 20}); err != nil {
		t.Fatal(err)
	}
	waitDump(t, c, "fake@a", "fakeagent ready")
	waitDump(t, c, "fake@b", "fakeagent ready")

	// type + dump.
	if err := c.Type(proto.TypeArgs{Name: "fake@a", Text: "hello daemon", Submit: true}); err != nil {
		t.Fatal(err)
	}
	waitDump(t, c, "fake@a", "GOT: hello daemon")
	if txt, err := c.Dump(proto.DumpArgs{Name: "fake@a", Scrollback: true}); err != nil || !strings.Contains(txt, "GOT: hello daemon") {
		t.Fatalf("scrollback dump: %v %q", err, txt)
	}
	if txt, err := c.Dump(proto.DumpArgs{Name: "fake@a", ANSI: true}); err != nil || !strings.Contains(txt, "hello daemon") {
		t.Fatalf("ansi dump: %v %q", err, txt)
	}

	// Child env: AGENTD_ID / AGENTD_SOCKET.
	if _, err := c.Run(proto.RunArgs{Name: "env", Cmd: []string{"sh", "-c", `echo "ID=$AGENTD_ID SOCK=$AGENTD_SOCKET"; sleep 30`}}); err != nil {
		t.Fatal(err)
	}
	waitDump(t, c, "env", "ID=env SOCK="+e.sock)

	// Mailbox between two sessions (authenticated with their tokens).
	ca, cb := dialAs(t, e, "fake@a"), dialAs(t, e, "fake@b")
	if _, err := ca.Send(proto.SendArgs{To: "nobody", Body: "x"}); err == nil {
		t.Fatal("unknown recipient accepted")
	}
	m, err := ca.Send(proto.SendArgs{To: "fake@b", Subject: "s", Body: "please review"})
	if err != nil {
		t.Fatal(err)
	}
	if m.ID == 0 || m.State != "queued" || m.Sender != "fake@a" {
		t.Fatalf("sent %+v", m)
	}
	if s := findSession(t, c, "fake@b"); s.Unread != 1 {
		t.Fatalf("unread = %d", s.Unread)
	}
	inbox, err := cb.Inbox("", false)
	if err != nil || len(inbox) != 1 || inbox[0].Body != "please review" || inbox[0].State != "delivered" {
		t.Fatalf("inbox %v %+v", err, inbox)
	}
	if _, err := ca.Read("", m.ID); err == nil {
		t.Fatal("read by non-recipient accepted")
	}
	got, err := cb.Read("", m.ID)
	if err != nil || got.State != "read" {
		t.Fatalf("read %v %+v", err, got)
	}
	if inbox, _ := cb.Inbox("", false); len(inbox) != 0 {
		t.Fatalf("unread inbox after read: %+v", inbox)
	}
	if all, _ := cb.Inbox("", true); len(all) != 1 {
		t.Fatalf("inbox --all: %+v", all)
	}
	rep, err := cb.Reply(m.ID, "done")
	if err != nil || rep.Recipient != "fake@a" || rep.Sender != "fake@b" || rep.ReplyTo == nil || *rep.ReplyTo != m.ID {
		t.Fatalf("reply %v %+v", err, rep)
	}
	if inbox, _ := ca.Inbox("", false); len(inbox) != 1 || inbox[0].Body != "done" {
		t.Fatalf("reply inbox %+v", inbox)
	}
	// Human recipients are always addressable.
	if _, err := ca.Send(proto.SendArgs{To: "human:alice", Body: "hi"}); err != nil {
		t.Fatal(err)
	}
	peers, err := c.Peers()
	if err != nil || len(peers) != 3 {
		t.Fatalf("peers %v %+v", err, peers)
	}

	// Attach via the client library.
	ac, err := client.Dial(e.sock)
	if err != nil {
		t.Fatal(err)
	}
	conn, err := ac.Attach("fake@a", 100, 30)
	if err != nil {
		t.Fatal(err)
	}
	frames := newFrameReader(conn)
	frames.waitFor(t, "GOT: hello daemon") // the redraw shows the current screen
	if s := findSession(t, c, "fake@a"); s.Attached != 1 {
		t.Fatalf("attached = %d", s.Attached)
	}
	fw := proto.NewFrameWriter(conn)
	if err := fw.Data([]byte("typed live")); err != nil {
		t.Fatal(err)
	}
	frames.waitFor(t, "typed live") // echo from the PTY stream
	// The attached client's size wins.
	if s := findSession(t, c, "fake@a"); s.Cols != 100 || s.Rows != 30 {
		t.Fatalf("size after attach %dx%d", s.Cols, s.Rows)
	}
	if err := fw.Resize(110, 33); err != nil {
		t.Fatal(err)
	}
	testutil.WaitFor(t, timeout, func() bool {
		s := findSession(t, c, "fake@a")
		return s.Cols == 110 && s.Rows == 33
	}, "resize frame should resize the session", nil)
	if err := fw.Data([]byte("\r")); err != nil {
		t.Fatal(err)
	}
	waitDump(t, c, "fake@a", "GOT: typed live")
	if err := fw.Detach(); err != nil {
		t.Fatal(err)
	}
	frames.waitClosed(t)
	_ = conn.Close()
	testutil.WaitFor(t, timeout, func() bool { return findSession(t, c, "fake@a").Attached == 0 },
		"client should be detached", nil)

	// Attach again, then the session exits: E frame with the exit code.
	ac2, err := client.Dial(e.sock)
	if err != nil {
		t.Fatal(err)
	}
	conn2, err := ac2.Attach("fake@a", 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	f2 := newFrameReader(conn2)
	f2.waitFor(t, "GOT: typed live")
	if err := c.Type(proto.TypeArgs{Name: "fake@a", Text: "/exit 7", Submit: true}); err != nil {
		t.Fatal(err)
	}
	if code := f2.waitExit(t); code != 7 {
		t.Fatalf("exit frame code = %d", code)
	}
	_ = conn2.Close()

	// kill → exit code in ls; rm.
	if err := c.Rm("fake@b"); err == nil {
		t.Fatal("rm of a running session accepted")
	}
	if err := c.Kill("fake@b", "KILL"); err != nil {
		t.Fatal(err)
	}
	testutil.WaitFor(t, timeout, func() bool { return findSession(t, c, "fake@b").State == "exited" },
		"fake@b should exit", nil)
	if s := findSession(t, c, "fake@b"); s.ExitCode == nil || *s.ExitCode != 128+9 {
		t.Fatalf("exit code %+v", s.ExitCode)
	}
	if s := findSession(t, c, "fake@a"); s.ExitCode == nil || *s.ExitCode != 7 {
		t.Fatalf("fake@a exit code %+v", s.ExitCode)
	}
	// Exited sessions stay dumpable and addressable until rm.
	waitDump(t, c, "fake@a", "GOT: /exit 7")
	if _, err := c.Send(proto.SendArgs{To: "fake@a", Body: "later"}); err != nil {
		t.Fatalf("send to exited: %v", err)
	}
	if err := c.Rm("fake@b"); err != nil {
		t.Fatal(err)
	}
	if err := c.Kill("fake@b", ""); err == nil {
		t.Fatal("kill after rm accepted")
	}

	// Session log file.
	logPath, _ := config.SessionLogPath("fake@a")
	if b, err := os.ReadFile(logPath); err != nil || !strings.Contains(string(b), "hello daemon") {
		t.Fatalf("session log: %v %q", err, b)
	}

	// shutdown op.
	if err := c.Shutdown(); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-e.errc:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(timeout):
		t.Fatal("daemon did not shut down")
	}
	if _, err := os.Stat(e.sock); !os.IsNotExist(err) {
		t.Fatalf("socket not removed: %v", err)
	}
}

func TestACLAndStaleSocket(t *testing.T) {
	e := startDaemon(t)
	// A second daemon on the same socket refuses to start.
	dbPath, _ := config.MailboxPath()
	store, err := mailbox.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := daemon.New(nil, nil, store, nil).Listen(); err == nil {
		t.Fatal("second daemon started")
	}
	e.srv.Shutdown()

	// Stale socket file (nobody listening) is replaced.
	if err := os.WriteFile(e.sock, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	cfgPath := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(cfgPath, []byte("[acl]\n\"locked\" = [\"human:*\"]\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	srv := daemon.New(cfg, nil, store, nil)
	if err := srv.Listen(); err != nil {
		t.Fatalf("stale socket: %v", err)
	}
	go func() { _ = srv.Serve(context.Background()) }()
	defer srv.Shutdown()
	fi, err := os.Stat(e.sock)
	if err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("socket mode %v %v", fi.Mode(), err)
	}
	c, err := client.Dial(e.sock)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	for _, n := range []string{"locked", "codex@x"} {
		if _, err := c.Run(proto.RunArgs{Name: n, Cmd: []string{"sleep", "30"}}); err != nil {
			t.Fatal(err)
		}
	}
	agent := dialAsSrv(t, e.sock, srv, "codex@x")
	if _, err := agent.Send(proto.SendArgs{To: "locked", Body: "hi"}); err == nil {
		t.Fatal("ACL not enforced")
	}
	if _, err := c.Send(proto.SendArgs{To: "locked", Body: "hi"}); err != nil {
		t.Fatalf("ACL allowed sender rejected: %v", err)
	}
	// Unknown op and bad args answer with errors, connection stays usable.
	if err := c.Call("bogus", nil, nil); err == nil || !client.IsRemote(err) {
		t.Fatalf("unknown op: %v", err)
	}
	if err := c.Call(proto.OpKill, map[string]any{"name": "locked", "signal": "NOPE"}, nil); err == nil {
		t.Fatal("bad signal accepted")
	}
	if _, err := c.Ping(); err != nil {
		t.Fatal(err)
	}
}

func TestDeliveryKick(t *testing.T) {
	e := startDaemon(t)
	k := &kicker{}
	// Delivery must be set before Serve; use a fresh server for this.
	e.srv.Shutdown()
	dbPath, _ := config.MailboxPath()
	store, err := mailbox.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	srv := daemon.New(nil, nil, store, nil)
	srv.Delivery = k
	if err := srv.Listen(); err != nil {
		t.Fatal(err)
	}
	go func() { _ = srv.Serve(context.Background()) }()
	defer srv.Shutdown()
	c, err := client.Dial(e.sock)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if _, err := c.Run(proto.RunArgs{Name: "a", Cmd: []string{"sleep", "30"}}); err != nil {
		t.Fatal(err)
	}
	me, err := c.Whoami()
	if err != nil {
		t.Fatal(err)
	}
	m, err := c.Send(proto.SendArgs{To: "a", Body: "hi"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := dialAsSrv(t, e.sock, srv, "a").Reply(m.ID, "ok"); err != nil {
		t.Fatal(err)
	}
	testutil.WaitFor(t, timeout, func() bool { return k.ran() }, "delivery loop should run", nil)
	if got := k.list(); strings.Join(got, ",") != "a,a,"+me.Identity {
		t.Fatalf("kicks = %v", got)
	}
	// Registry lookup used by delivery.
	if tgt, p, ok := srv.Lookup("a"); !ok || tgt.Name() != "a" || p.Name != "default" {
		t.Fatalf("lookup %v %+v %v", tgt, p, ok)
	}
	if _, _, ok := srv.Lookup("zzz"); ok {
		t.Fatal("lookup of unknown session")
	}
}

type kicker struct {
	mu      sync.Mutex
	kicks   []string
	running bool
}

func (k *kicker) Run(ctx context.Context) error {
	k.mu.Lock()
	k.running = true
	k.mu.Unlock()
	<-ctx.Done()
	return ctx.Err()
}

func (k *kicker) Kick(r string) {
	k.mu.Lock()
	k.kicks = append(k.kicks, r)
	k.mu.Unlock()
}

func (k *kicker) ran() bool {
	k.mu.Lock()
	defer k.mu.Unlock()
	return k.running
}

func (k *kicker) list() []string {
	k.mu.Lock()
	defer k.mu.Unlock()
	return append([]string(nil), k.kicks...)
}

// frameReader collects frames from an attach connection in the background.
type frameReader struct {
	mu     sync.Mutex
	data   strings.Builder
	exit   *int
	closed bool
}

func newFrameReader(conn net.Conn) *frameReader {
	fr := &frameReader{}
	go func() {
		for {
			f, err := proto.ReadFrame(conn)
			fr.mu.Lock()
			if err != nil {
				fr.closed = true
				fr.mu.Unlock()
				return
			}
			switch f.Type {
			case proto.FrameData:
				fr.data.Write(f.Payload)
			case proto.FrameExit:
				code, _ := proto.ParseExit(f.Payload)
				fr.exit = &code
			}
			fr.mu.Unlock()
		}
	}()
	return fr
}

func (fr *frameReader) snapshot() (string, *int, bool) {
	fr.mu.Lock()
	defer fr.mu.Unlock()
	return fr.data.String(), fr.exit, fr.closed
}

func (fr *frameReader) waitFor(t *testing.T, want string) {
	t.Helper()
	testutil.WaitFor(t, timeout, func() bool {
		d, _, _ := fr.snapshot()
		return strings.Contains(d, want)
	}, "attach stream should contain "+want, func() string { d, _, _ := fr.snapshot(); return d })
}

func (fr *frameReader) waitExit(t *testing.T) int {
	t.Helper()
	testutil.WaitFor(t, timeout, func() bool { _, e, _ := fr.snapshot(); return e != nil }, "exit frame", nil)
	_, e, _ := fr.snapshot()
	return *e
}

func (fr *frameReader) waitClosed(t *testing.T) {
	t.Helper()
	testutil.WaitFor(t, timeout, func() bool { _, _, c := fr.snapshot(); return c }, "attach connection should close", nil)
}

func TestDumpCells(t *testing.T) {
	e := startServer(t, nil, nil)
	c := e.dial(t)
	if _, err := c.Run(proto.RunArgs{Name: "cells", Cmd: []string{"sh", "-c",
		`printf '\033[2J\033[1;1Hab\033[2;1H\033[1mzz\033[22m\033[2;1H'; exec sleep 60`}, Cols: 20, Rows: 4}); err != nil {
		t.Fatal(err)
	}
	waitDump(t, c, "cells", "zz")
	res, err := c.DumpResult(proto.DumpArgs{Name: "cells", Cells: true})
	if err != nil {
		t.Fatal(err)
	}
	d := res.Cells
	if d == nil || d.CursorX != 0 || d.CursorY != 1 || d.Row != 1 || len(d.Cells) != 2 ||
		d.Cells[0].Rune != "U+007A" || len(d.Cells[0].Attrs) != 1 || d.Cells[0].Attrs[0] != "bold" {
		t.Fatalf("cells = %+v", d)
	}
	row := 0
	res, err = c.DumpResult(proto.DumpArgs{Name: "cells", Cells: true, Row: &row})
	if err != nil {
		t.Fatal(err)
	}
	if d := res.Cells; d == nil || d.Row != 0 || len(d.Cells) != 2 || d.Cells[1].Content != "b" {
		t.Fatalf("row 0 cells = %+v", res.Cells)
	}
}
