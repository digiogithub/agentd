package daemon_test

import (
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/digiogithub/agentd/internal/client"
	"github.com/digiogithub/agentd/internal/config"
	"github.com/digiogithub/agentd/internal/daemon"
	"github.com/digiogithub/agentd/internal/mailbox"
	"github.com/digiogithub/agentd/internal/proto"
	"github.com/digiogithub/agentd/internal/testutil"
)

// deafCmd never reads its stdin, so PTY input writes stop making progress.
var deafCmd = []string{"sh", "-c", "stty raw -echo; echo deaf-ready; exec sleep 1000"}

// tempEnv points the config paths at a fresh short temp dir and returns the
// socket path.
func tempEnv(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "agentd-l")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	sock := filepath.Join(dir, "agentd.sock")
	t.Setenv(config.EnvSocket, sock)
	t.Setenv(config.EnvStateDir, filepath.Join(dir, "state"))
	t.Setenv(config.EnvConfig, filepath.Join(dir, "config.toml"))
	if err := config.EnsureDirs(); err != nil {
		t.Fatal(err)
	}
	return sock
}

// startServer runs an in-process daemon with cfg (nil = defaults); tweak may
// adjust the server before it listens.
func startServer(t *testing.T, cfg *config.Config, tweak func(*daemon.Server)) *env {
	t.Helper()
	sock := tempEnv(t)
	dbPath, _ := config.MailboxPath()
	store, err := mailbox.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	srv := daemon.New(cfg, nil, store, nil)
	if tweak != nil {
		tweak(srv)
	}
	if err := srv.Listen(); err != nil {
		t.Fatal(err)
	}
	e := &env{srv: srv, sock: sock, errc: make(chan error, 1)}
	go func() { e.errc <- srv.Serve(context.Background()) }()
	t.Cleanup(srv.Shutdown)
	return e
}

func shutdownWithin(t *testing.T, srv *daemon.Server, d time.Duration) {
	t.Helper()
	done := make(chan struct{})
	go func() { srv.Shutdown(); close(done) }()
	select {
	case <-done:
	case <-time.After(d):
		t.Fatalf("Shutdown did not complete within %v", d)
	}
}

// Regression (review R1): a child that stops reading stdin, with a type op
// and an injection blocked on its input, used to hang Shutdown forever.
func TestShutdownWithBlockedInjection(t *testing.T) {
	e := startServer(t, nil, func(s *daemon.Server) {
		s.WriteTimeout = time.Minute // prove Close aborts, not the timeout
		l := s.NewDeliveryLoop(nil)
		l.Interval = 50 * time.Millisecond
		s.Delivery = l
	})
	c := e.dial(t)
	if _, err := c.Run(proto.RunArgs{Name: "deaf", Cmd: deafCmd}); err != nil {
		t.Fatal(err)
	}
	waitDump(t, c, "deaf", "deaf-ready")
	typeDone := make(chan error, 1)
	go func() {
		c2, err := client.Dial(e.sock)
		if err != nil {
			typeDone <- err
			return
		}
		defer func() { _ = c2.Close() }()
		typeDone <- c2.Type(proto.TypeArgs{Name: "deaf", Text: strings.Repeat("x", 1<<20)})
	}()
	if _, err := e.srv.Store.Send(context.Background(), mailbox.Message{Sender: "human:t", Recipient: "deaf", Body: "hello"}); err != nil {
		t.Fatal(err)
	}
	time.Sleep(2500 * time.Millisecond) // past idle_ms: delivery is now blocked on the input
	shutdownWithin(t, e.srv, 8*time.Second)
	select {
	case <-typeDone:
	case <-time.After(5 * time.Second):
		t.Fatal("type op still blocked after shutdown")
	}
}

// A type op on a deaf child fails after the write timeout instead of hanging.
func TestTypeTimesOutOnDeafChild(t *testing.T) {
	e := startServer(t, nil, func(s *daemon.Server) { s.WriteTimeout = 300 * time.Millisecond })
	c := e.dial(t)
	if _, err := c.Run(proto.RunArgs{Name: "deaf", Cmd: deafCmd}); err != nil {
		t.Fatal(err)
	}
	waitDump(t, c, "deaf", "deaf-ready")
	begin := time.Now()
	err := c.Type(proto.TypeArgs{Name: "deaf", Text: strings.Repeat("x", 1<<20)})
	if err == nil || !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("type err = %v", err)
	}
	if d := time.Since(begin); d > 5*time.Second {
		t.Fatalf("type took %v", d)
	}
	if _, err := c.Ping(); err != nil {
		t.Fatal(err)
	}
}

// The attach reader keeps processing resize and detach while the child does
// not read the client's input.
func TestAttachResizeWhileInputBlocked(t *testing.T) {
	e := startServer(t, nil, func(s *daemon.Server) { s.WriteTimeout = time.Minute })
	c := e.dial(t)
	if _, err := c.Run(proto.RunArgs{Name: "deaf", Cmd: deafCmd, Cols: 80, Rows: 24}); err != nil {
		t.Fatal(err)
	}
	waitDump(t, c, "deaf", "deaf-ready")
	ac, err := client.Dial(e.sock)
	if err != nil {
		t.Fatal(err)
	}
	conn, err := ac.Attach("deaf", 80, 24)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	go func() { // drain output
		buf := make([]byte, 32<<10)
		for {
			if _, err := conn.Read(buf); err != nil {
				return
			}
		}
	}()
	fw := proto.NewFrameWriter(conn)
	for i := 0; i < 8; i++ {
		if err := fw.Data([]byte(strings.Repeat("k", 256<<10))); err != nil {
			t.Fatal(err)
		}
	}
	if err := fw.Resize(100, 30); err != nil {
		t.Fatal(err)
	}
	testutil.WaitFor(t, 3*time.Second, func() bool {
		info := findSession(t, c, "deaf")
		return info.Cols == 100 && info.Rows == 30
	}, "resize not applied while input is blocked", nil)
	if err := fw.Detach(); err != nil {
		t.Fatal(err)
	}
	testutil.WaitFor(t, 3*time.Second, func() bool { return findSession(t, c, "deaf").Attached == 0 },
		"detach not processed while input is blocked", nil)
}

// An attached client that stops reading is dropped after the frame write
// deadline instead of pinning a daemon goroutine.
func TestAttachSlowClientDropped(t *testing.T) {
	e := startServer(t, nil, func(s *daemon.Server) { s.AttachWriteTimeout = 300 * time.Millisecond })
	c := e.dial(t)
	if _, err := c.Run(proto.RunArgs{Name: "spam", Cmd: []string{"sh", "-c", "while :; do echo spam-spam-spam-spam-spam-spam-spam; done"}}); err != nil {
		t.Fatal(err)
	}
	ac, err := client.Dial(e.sock)
	if err != nil {
		t.Fatal(err)
	}
	conn, err := ac.Attach("spam", 80, 24)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	// Never read.
	testutil.WaitFor(t, 10*time.Second, func() bool { return findSession(t, c, "spam").Attached == 0 },
		"non-reading attach client was not dropped", nil)
}

// Regression (review R2): fd exhaustion on accept used to shut the daemon
// down (killing every session).
func TestAcceptErrorsDoNotShutdown(t *testing.T) {
	e := startServer(t, nil, nil)
	c := e.dial(t)
	if _, err := c.Run(proto.RunArgs{Name: "victim", Cmd: []string{"sleep", "1000"}}); err != nil {
		t.Fatal(err)
	}
	var old syscall.Rlimit
	if err := syscall.Getrlimit(syscall.RLIMIT_NOFILE, &old); err != nil {
		t.Fatal(err)
	}
	lim := old
	lim.Cur = 80
	if err := syscall.Setrlimit(syscall.RLIMIT_NOFILE, &lim); err != nil {
		t.Skipf("setrlimit: %v", err)
	}
	var conns []net.Conn
	for i := 0; i < 200; i++ {
		cn, err := net.Dial("unix", e.sock)
		if err != nil {
			break
		}
		conns = append(conns, cn)
	}
	select {
	case <-e.srv.Done():
		_ = syscall.Setrlimit(syscall.RLIMIT_NOFILE, &old)
		t.Fatal("daemon shut down on accept errors")
	case <-time.After(1500 * time.Millisecond):
	}
	for _, cn := range conns {
		_ = cn.Close()
	}
	if err := syscall.Setrlimit(syscall.RLIMIT_NOFILE, &old); err != nil {
		t.Fatal(err)
	}
	// Recovered: new connections are served and the session survived.
	testutil.WaitFor(t, 5*time.Second, func() bool {
		c2, err := client.Dial(e.sock)
		if err != nil {
			return false
		}
		defer func() { _ = c2.Close() }()
		_, err = c2.Ping()
		return err == nil
	}, "daemon does not accept after fd pressure", nil)
	if st := findSession(t, c, "victim").State; st != "running" {
		t.Fatalf("victim state %q", st)
	}
}

// Regression (review R4): two daemons racing on a stale socket could both
// end up listening. With the lock file exactly one wins every round.
func TestListenRaceExactlyOneWins(t *testing.T) {
	sock := tempEnv(t)
	for i := 0; i < 50; i++ {
		l, err := net.Listen("unix", sock) // leave a stale socket file behind
		if err != nil {
			t.Fatal(err)
		}
		l.(*net.UnixListener).SetUnlinkOnClose(false)
		_ = l.Close()
		var wg sync.WaitGroup
		errs := make([]error, 2)
		srvs := make([]*daemon.Server, 2)
		for j := range srvs {
			srvs[j] = daemon.New(nil, nil, nil, nil)
			wg.Add(1)
			go func() { defer wg.Done(); errs[j] = srvs[j].Listen() }()
		}
		wg.Wait()
		wins := 0
		for j, err := range errs {
			if err == nil {
				wins++
				srvs[j].Shutdown()
			} else if !errors.Is(err, daemon.ErrAlreadyRunning) {
				t.Fatalf("round %d: unexpected error %v", i, err)
			}
		}
		if wins != 1 {
			t.Fatalf("round %d: %d daemons listened", i, wins)
		}
	}
}

// A second daemon fails with ErrAlreadyRunning while the first holds the
// lock, even if the socket file was removed behind its back.
func TestSecondDaemonRefused(t *testing.T) {
	sock := tempEnv(t)
	a := daemon.New(nil, nil, nil, nil)
	if err := a.Listen(); err != nil {
		t.Fatal(err)
	}
	defer a.Shutdown()
	_ = os.Remove(sock)
	b := daemon.New(nil, nil, nil, nil)
	if err := b.Listen(); !errors.Is(err, daemon.ErrAlreadyRunning) {
		t.Fatalf("second Listen err = %v", err)
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(sock), daemon.LockName)); err != nil {
		t.Fatalf("lock file: %v", err)
	}
}

// Regression (review R5): Shutdown must not unlink a socket file that is no
// longer the one it bound.
func TestShutdownKeepsForeignSocket(t *testing.T) {
	sock := tempEnv(t)
	a := daemon.New(nil, nil, nil, nil)
	if err := a.Listen(); err != nil {
		t.Fatal(err)
	}
	_ = os.Remove(sock)
	foreign, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = foreign.Close() }()
	a.Shutdown()
	if _, err := os.Lstat(sock); err != nil {
		t.Fatalf("foreign socket removed by Shutdown: %v", err)
	}
}

// Concurrent runs of the same name: exactly one starts.
func TestRunNameReservation(t *testing.T) {
	e := startServer(t, nil, nil)
	var wg sync.WaitGroup
	var mu sync.Mutex
	ok := 0
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			c, err := client.Dial(e.sock)
			if err != nil {
				return
			}
			defer func() { _ = c.Close() }()
			if _, err := c.Run(proto.RunArgs{Name: "dup", Cmd: []string{"sleep", "30"}}); err == nil {
				mu.Lock()
				ok++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if ok != 1 {
		t.Fatalf("%d runs of the same name succeeded", ok)
	}
}

// run uses the CLI's environment (EnvBase) as the child's base, strips
// AGENTD_*, Claude Code markers and [run] strip_env, then applies --env and
// the daemon's AGENTD_* vars.
func TestRunEnvBase(t *testing.T) {
	cfgPath := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(cfgPath, []byte("[run]\nstrip_env = [\"SECRET_*\"]\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("DAEMON_ONLY", "daemon")
	e := startServer(t, cfg, nil)
	c := e.dial(t)
	base := []string{
		"PATH=" + os.Getenv("PATH"), "FROM_CLI=cli", "OVER=base", "AGENTD_ID=spoof", "AGENTD_TOKEN=leak",
		"CLAUDECODE=1", "CLAUDE_CODE_ENTRYPOINT=cli", "SECRET_KEY=s3",
	}
	script := `echo "[$FROM_CLI|$OVER|${DAEMON_ONLY:-none}|$AGENTD_ID|${CLAUDECODE:-none}|${CLAUDE_CODE_ENTRYPOINT:-none}|${SECRET_KEY:-none}|${AGENTD_TOKEN:-}]"; sleep 30`
	if _, err := c.Run(proto.RunArgs{Name: "envt", Cmd: []string{"sh", "-c", script}, EnvBase: base,
		Env: map[string]string{"OVER": "flag", "AGENTD_ID": "flag"}}); err != nil {
		t.Fatal(err)
	}
	waitDump(t, c, "envt", "[cli|flag|none|envt|")
	got, _ := c.Dump(proto.DumpArgs{Name: "envt"})
	if !strings.Contains(got, "|envt|none|none|none|") || strings.Contains(got, "leak") {
		t.Fatalf("stripped vars leaked: %q", got)
	}

	// Without EnvBase the daemon's own environment is the (stripped) base.
	t.Setenv("CLAUDECODE", "1")
	if _, err := c.Run(proto.RunArgs{Name: "envd", Cmd: []string{"sh", "-c", script}}); err != nil {
		t.Fatal(err)
	}
	waitDump(t, c, "envd", "|daemon|envd|")
	got, _ = c.Dump(proto.DumpArgs{Name: "envd"})
	if !strings.Contains(got, "|envd|none|none|none|") {
		t.Fatalf("CLAUDECODE leaked from the daemon env: %q", got)
	}
}
