package core_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/digiogithub/agentd/internal/client"
	"github.com/digiogithub/agentd/internal/config"
	"github.com/digiogithub/agentd/internal/daemon"
	"github.com/digiogithub/agentd/internal/desktopui/backend"
	"github.com/digiogithub/agentd/internal/desktopui/core"
	"github.com/digiogithub/agentd/internal/desktopui/remote"
	"github.com/digiogithub/agentd/internal/desktopui/remote/sshtest"
	"github.com/digiogithub/agentd/internal/mailbox"
	"github.com/digiogithub/agentd/internal/proto"
)

const timeout = 15 * time.Second

func TestMain(m *testing.M) {
	for _, k := range []string{"AGENTD_TOKEN", "AGENTD_ID", "AGENTD_SOCKET"} {
		os.Unsetenv(k)
	}
	os.Exit(m.Run())
}

// isolate gives the test its own HOME, desktop dir and POSIX shell.
func isolate(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("SHELL", "/bin/sh")
	t.Setenv("SSH_AUTH_SOCK", "")
	dir := filepath.Join(home, "desktop")
	t.Setenv(remote.EnvDir, dir)
	return dir
}

// startDaemon runs an in-process agentd daemon on a temp socket and state
// dir (sessions run sh/cat only) and returns its socket.
func startDaemon(t *testing.T) string {
	t.Helper()
	return startDaemonCfg(t, config.Default())
}

// startDaemonCfg is startDaemon with a config.
func startDaemonCfg(t *testing.T, cfg *config.Config) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "agentd-c")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	sock := filepath.Join(dir, "agentd.sock")
	t.Setenv(config.EnvSocket, sock)
	t.Setenv(config.EnvStateDir, filepath.Join(dir, "state"))
	t.Setenv(config.EnvConfig, filepath.Join(dir, "config.toml"))
	t.Setenv("AGENTD_ALIASES_FILE", filepath.Join(dir, "aliases.toml"))
	if err := config.EnsureDirs(); err != nil {
		t.Fatal(err)
	}
	dbPath, _ := config.MailboxPath()
	store, err := mailbox.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	srv := daemon.New(cfg, nil, store, nil)
	if err := srv.Listen(); err != nil {
		t.Fatal(err)
	}
	go func() { _ = srv.Serve(context.Background()) }()
	t.Cleanup(srv.Shutdown)
	return sock
}

func dialDaemon(t *testing.T, sock string) *client.Client {
	t.Helper()
	c, err := client.Dial(sock)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return c
}

func runCat(t *testing.T, sock, name string, labels map[string]string) proto.SessionInfo {
	t.Helper()
	c := dialDaemon(t, sock)
	info, err := c.Run(proto.RunArgs{Name: name, Cwd: t.TempDir(), Cmd: []string{"cat"}, Labels: labels})
	if err != nil {
		t.Fatal(err)
	}
	return info
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

// waitSessions reads snapshots until cond holds.
func waitSessions(t *testing.T, ch <-chan []backend.Session, what string, cond func([]backend.Session) bool) []backend.Session {
	t.Helper()
	deadline := time.After(timeout)
	var last []backend.Session
	for {
		select {
		case ss, ok := <-ch:
			if !ok {
				t.Fatalf("%s: channel closed", what)
			}
			last = ss
			if cond(ss) {
				return ss
			}
		case <-deadline:
			t.Fatalf("timed out waiting for %s; last %+v", what, last)
		}
	}
}

func waitHosts(t *testing.T, ch <-chan []backend.HostStatus, what string, cond func([]backend.HostStatus) bool) []backend.HostStatus {
	t.Helper()
	deadline := time.After(timeout)
	var last []backend.HostStatus
	for {
		select {
		case hs := <-ch:
			last = hs
			if cond(hs) {
				return hs
			}
		case <-deadline:
			t.Fatalf("timed out waiting for %s; last %+v", what, last)
		}
	}
}

func find(ss []backend.Session, key string) (backend.Session, bool) {
	for _, s := range ss {
		if s.Key() == key {
			return s, true
		}
	}
	return backend.Session{}, false
}

func hostStatus(hs []backend.HostStatus, id string) (backend.HostStatus, bool) {
	for _, h := range hs {
		if h.Host.ID == id {
			return h, true
		}
	}
	return backend.HostStatus{}, false
}

func newBackend(t *testing.T, opts core.Options) *core.Backend {
	t.Helper()
	b := core.New(opts)
	t.Cleanup(func() { _ = b.Close() })
	return b
}

// sshHost starts an SSH server and saves it as host "box".
func sshHost(t *testing.T, b *core.Backend) *sshtest.Server {
	t.Helper()
	srv := sshtest.New(t, sshtest.Options{Password: "secret"})
	h := backend.Host{ID: "box", Name: "box", Addr: srv.Host, Port: srv.Port, User: "tester", Auth: backend.AuthPassword}
	if err := b.SaveHost(context.Background(), h); err != nil {
		t.Fatal(err)
	}
	return srv
}

func sshOpts(dir, sock string) core.Options {
	return core.Options{
		Dir:         dir,
		LocalSocket: sock,
		// Only $AGENTD_SOCKET (the test daemon): never the user's real one.
		SocketProbe:    `[ -n "$AGENTD_SOCKET" ] && [ -S "$AGENTD_SOCKET" ] && printf '%s\n' "$AGENTD_SOCKET"`,
		TrustHost:      func(backend.Host, string) bool { return true },
		Password:       func(backend.Host) (string, error) { return "secret", nil },
		AgentSocket:    "-",
		PreviewRefresh: 50 * time.Millisecond,
		ViewPoll:       time.Second,
		MuxPoll:        100 * time.Millisecond,
	}
}

func readUntil(t *testing.T, r interface{ Read([]byte) (int, error) }, want string) string {
	t.Helper()
	got := make(chan string, 1)
	go func() {
		var b strings.Builder
		buf := make([]byte, 4096)
		for {
			n, err := r.Read(buf)
			b.Write(buf[:n])
			if strings.Contains(b.String(), want) || err != nil {
				got <- b.String()
				return
			}
		}
	}()
	select {
	case s := <-got:
		if !strings.Contains(s, want) {
			t.Fatalf("stream ended before %q: %q", want, s)
		}
		return s
	case <-time.After(timeout):
		t.Fatalf("timed out reading %q", want)
	}
	return ""
}
