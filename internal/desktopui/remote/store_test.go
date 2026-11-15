package remote_test

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/digiogithub/agentd/internal/desktopui/backend"
	"github.com/digiogithub/agentd/internal/desktopui/remote"
)

func writeEncryptedKey(path, pass string) error {
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return err
	}
	b, err := ssh.MarshalPrivateKeyWithPassphrase(priv, "c", []byte(pass))
	if err != nil {
		return err
	}
	return os.WriteFile(path, pem.EncodeToMemory(b), 0o600)
}

func TestHostStore(t *testing.T) {
	dir := t.TempDir()
	s := remote.NewHostStore(dir)
	hosts, err := s.List()
	if err != nil || len(hosts) != 1 || !hosts[0].Local() {
		t.Fatalf("empty store: %+v %v", hosts, err)
	}
	a, err := s.Save(backend.Host{Name: "Dev Box", Addr: "10.0.0.2", User: "me"})
	if err != nil || a.ID != "dev-box" {
		t.Fatalf("save: %+v %v", a, err)
	}
	b, err := s.Save(backend.Host{Name: "Dev Box", Addr: "10.0.0.3"})
	if err != nil || b.ID != "dev-box-2" {
		t.Fatalf("unique id: %+v %v", b, err)
	}
	if _, err := s.Save(backend.Host{ID: backend.LocalHostID, Addr: "x"}); !errors.Is(err, remote.ErrLocalHost) {
		t.Fatalf("local save: %v", err)
	}
	if err := s.Update("dev-box", func(h *backend.Host) { h.HostKey = "SHA256:x" }); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(s.Path())
	if err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("mode: %v %v", fi, err)
	}
	// A second store on the same file sees the same data.
	hosts, _ = remote.NewHostStore(dir).List()
	if len(hosts) != 3 || hosts[0].ID != "local" || hosts[1].HostKey != "SHA256:x" || hosts[2].ID != "dev-box-2" {
		t.Fatalf("reload: %+v", hosts)
	}
	if err := s.Remove("dev-box"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Get("dev-box"); !errors.Is(err, remote.ErrUnknownHost) {
		t.Fatalf("get removed: %v", err)
	}
	if err := s.Remove(backend.LocalHostID); !errors.Is(err, remote.ErrLocalHost) {
		t.Fatalf("remove local: %v", err)
	}
	// No temp files left behind.
	ents, _ := os.ReadDir(dir)
	for _, e := range ents {
		if strings.Contains(e.Name(), ".tmp") {
			t.Fatalf("temp file left: %s", e.Name())
		}
	}
}

func TestParseSSHConfig(t *testing.T) {
	home := t.TempDir()
	sshDir := filepath.Join(home, ".ssh")
	if err := os.MkdirAll(filepath.Join(sshDir, "conf.d"), 0o700); err != nil {
		t.Fatal(err)
	}
	inc := "Host included\n  HostName inc.example.com\n"
	if err := os.WriteFile(filepath.Join(sshDir, "conf.d", "a.conf"), []byte(inc), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := `# comment
Include conf.d/*.conf
Host web web-alias
    HostName web.example.com
    User deploy
    Port 2222
    IdentityFile ~/.ssh/id_web

Host *.internal bastion?
    User nobody

Host db
  HostName=%h.example.org
  IdentityFile "/keys/db key"
  Port 22

Match host foo
  User matched

Host !web *
  User fallback
`
	file := filepath.Join(sshDir, "config")
	if err := os.WriteFile(file, []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	hosts, err := remote.ImportSSHConfigFile(file, home)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]backend.Host{}
	var order []string
	for _, h := range hosts {
		got[h.SSHAlias] = h
		order = append(order, h.SSHAlias)
	}
	if strings.Join(order, ",") != "included,web,web-alias,db" {
		t.Fatalf("aliases: %v", order)
	}
	web := got["web"]
	if web.Addr != "web.example.com" || web.User != "deploy" || web.Port != 2222 ||
		web.KeyFile != filepath.Join(home, ".ssh/id_web") || web.Auth != backend.AuthKeyFile || web.ID != "web" {
		t.Fatalf("web: %+v", web)
	}
	db := got["db"]
	if db.Addr != "db.example.org" || db.Port != 0 || db.KeyFile != "/keys/db key" || db.User != "fallback" {
		t.Fatalf("db: %+v", db)
	}
	if got["included"].Addr != "inc.example.com" || got["included"].User != "fallback" {
		t.Fatalf("included: %+v", got["included"])
	}
	if got["web-alias"].User != "deploy" {
		t.Fatalf("web-alias: %+v", got["web-alias"])
	}
	// Missing file = no hosts.
	if hs, err := remote.ImportSSHConfigFile(filepath.Join(home, "nope"), home); err != nil || hs != nil {
		t.Fatalf("missing: %v %v", hs, err)
	}
}

type fakeShell struct {
	backend.Shell
	closed atomic.Bool
	done   chan struct{}
}

func (f *fakeShell) Close() error {
	if f.closed.CompareAndSwap(false, true) {
		close(f.done)
	}
	return nil
}
func (f *fakeShell) Done() <-chan struct{} { return f.done }

func TestPool(t *testing.T) {
	var dials atomic.Int32
	fail := atomic.Bool{}
	fail.Store(true)
	var last *fakeShell
	p := remote.NewPool(func(id string) (backend.Host, error) {
		return backend.Host{ID: id, Addr: "x"}, nil
	}, func(ctx context.Context, h backend.Host) (backend.Shell, error) {
		dials.Add(1)
		if fail.Load() {
			return nil, errors.New("refused")
		}
		last = &fakeShell{done: make(chan struct{})}
		return last, nil
	})
	var changes atomic.Int32
	p.OnChange = func(string, bool, error) { changes.Add(1) }
	ctx := context.Background()
	if _, err := p.Get(ctx, "h"); err == nil || errors.Is(err, remote.ErrBackoff) {
		t.Fatalf("first: %v", err)
	}
	if _, err := p.Get(ctx, "h"); !errors.Is(err, remote.ErrBackoff) {
		t.Fatalf("backoff: %v", err)
	}
	if dials.Load() != 1 {
		t.Fatalf("dialed during backoff: %d", dials.Load())
	}
	fail.Store(false)
	p.Reset("h")
	s, err := p.Get(ctx, "h")
	if err != nil {
		t.Fatal(err)
	}
	s2, _ := p.Get(ctx, "h")
	if s != s2 || dials.Load() != 2 || !p.Connected("h") {
		t.Fatalf("not reused: dials=%d", dials.Load())
	}
	// Connection lost: dropped, next Get reconnects at once.
	_ = last.Close()
	waitFor(t, func() bool { return !p.Connected("h") })
	if _, err := p.Get(ctx, "h"); err != nil || dials.Load() != 3 {
		t.Fatalf("reconnect: %v dials=%d", err, dials.Load())
	}
	p.Close()
	if _, err := p.Get(ctx, "h"); err == nil {
		t.Fatal("get after close")
	}
	if changes.Load() < 3 {
		t.Fatalf("changes: %d", changes.Load())
	}
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	for i := 0; i < 500; i++ {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("condition not met")
}

func TestShellQuote(t *testing.T) {
	for in, want := range map[string]string{"": "''", "abc/d-e.f": "abc/d-e.f", "a b": "'a b'", "it's": `'it'\''s'`} {
		if got := remote.ShellQuote(in); got != want {
			t.Errorf("ShellQuote(%q) = %q, want %q", in, got, want)
		}
	}
}
