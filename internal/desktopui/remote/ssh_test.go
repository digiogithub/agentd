package remote_test

import (
	"bufio"
	"context"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"

	"github.com/digiogithub/agentd/internal/desktopui/backend"
	"github.com/digiogithub/agentd/internal/desktopui/remote"
	"github.com/digiogithub/agentd/internal/desktopui/remote/sshtest"
)

const timeout = 10 * time.Second

func testHost(srv *sshtest.Server) backend.Host {
	return backend.Host{ID: "box", Name: "box", Addr: srv.Host, Port: srv.Port, User: "tester", Auth: backend.AuthPassword}
}

// isolate points HOME and the desktop dir at temp dirs (no user profile, no
// ~/.ssh/known_hosts) and disables the ssh-agent.
func isolate(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("SSH_AUTH_SOCK", "")
	dir := filepath.Join(home, "desktop")
	t.Setenv(remote.EnvDir, dir)
	return dir
}

func pwConfig(dir string, trust bool) remote.SSHConfig {
	return remote.SSHConfig{
		Dir:      dir,
		Trust:    func(backend.Host, string) bool { return trust },
		Password: func(backend.Host) (string, error) { return "secret", nil },
	}
}

func dial(t *testing.T, srv *sshtest.Server, h backend.Host, cfg remote.SSHConfig) *remote.SSHShell {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	s, err := remote.DialSSH(ctx, h, cfg)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func TestTOFU(t *testing.T) {
	dir := isolate(t)
	srv := sshtest.New(t, sshtest.Options{Password: "secret"})
	h := testHost(srv)
	ctx := context.Background()

	// Rejected: nothing stored.
	_, err := remote.DialSSH(ctx, h, pwConfig(dir, false))
	if !errors.Is(err, remote.ErrHostKeyRejected) {
		t.Fatalf("reject: got %v", err)
	}
	var hke *backend.HostKeyError
	if !errors.As(err, &hke) || hke.Fingerprint != ssh.FingerprintSHA256(srv.HostKey) || hke.KeyType != srv.HostKey.Type() || hke.HostID != h.ID {
		t.Fatalf("reject carries %+v", hke)
	}
	if _, err := os.Stat(filepath.Join(dir, remote.KnownHostsFile)); !os.IsNotExist(err) {
		t.Fatalf("known_hosts written after a rejection: %v", err)
	}

	// Accepted: callback sees the fingerprint, known_hosts gets a line.
	var asked, stored string
	cfg := pwConfig(dir, true)
	cfg.Trust = func(_ backend.Host, fp string) bool { asked = fp; return true }
	cfg.Accepted = func(_ backend.Host, fp string) { stored = fp }
	s, err := remote.DialSSH(ctx, h, cfg)
	if err != nil {
		t.Fatal(err)
	}
	_ = s.Close()
	want := ssh.FingerprintSHA256(srv.HostKey)
	if asked != want || stored != want {
		t.Fatalf("fingerprints: asked %q stored %q want %q", asked, stored, want)
	}

	// Known now: the trust callback is not asked again.
	cfg.Trust = func(backend.Host, string) bool { t.Error("asked again"); return false }
	s, err = remote.DialSSH(ctx, h, cfg)
	if err != nil {
		t.Fatal(err)
	}
	_ = s.Close()

	// Host.HostKey mismatch is a hard error even if trust would say yes.
	bad := h
	bad.HostKey = "SHA256:not-the-key"
	_, err = remote.DialSSH(ctx, bad, pwConfig(dir, true))
	var mm *remote.HostKeyMismatch
	if !errors.As(err, &mm) || mm.Got != want || !errors.Is(err, backend.ErrHostKeyChanged) {
		t.Fatalf("HostKey mismatch: got %v", err)
	}
	// Host.HostKey match.
	good := h
	good.HostKey = want
	s, err = remote.DialSSH(ctx, good, pwConfig(dir, false))
	if err != nil {
		t.Fatalf("matching HostKey: %v", err)
	}
	_ = s.Close()

	// A new server on the same address key: known_hosts mismatch.
	srv2 := sshtest.New(t, sshtest.Options{Password: "secret"})
	hk := remote.NewHostKeys(dir)
	line := knownhosts.Line([]string{knownhosts.Normalize(srv2.Addr)}, srv.HostKey) // srv's key recorded for srv2's address
	if err := os.WriteFile(hk.AppFile, []byte(line+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err = remote.DialSSH(ctx, testHost(srv2), pwConfig(dir, true))
	if !errors.As(err, &mm) || len(mm.Want) != 1 || mm.Want[0] != want {
		t.Fatalf("known_hosts mismatch: got %v", err)
	}
	// Forget, then TOFU again works.
	if err := hk.Forget(srv2.Host, srv2.Port); err != nil {
		t.Fatal(err)
	}
	s, err = remote.DialSSH(ctx, testHost(srv2), pwConfig(dir, true))
	if err != nil {
		t.Fatalf("after Forget: %v", err)
	}
	_ = s.Close()
}

func TestUserKnownHostsHonoured(t *testing.T) {
	dir := isolate(t)
	srv := sshtest.New(t, sshtest.Options{Password: "secret"})
	home, _ := os.UserHomeDir()
	if err := os.MkdirAll(filepath.Join(home, ".ssh"), 0o700); err != nil {
		t.Fatal(err)
	}
	line := knownhosts.Line([]string{knownhosts.Normalize(srv.Addr)}, srv.HostKey)
	if err := os.WriteFile(filepath.Join(home, ".ssh", "known_hosts"), []byte(line+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := pwConfig(dir, false) // would reject an unknown key
	s := dial(t, srv, testHost(srv), cfg)
	_ = s.Close()
}

func TestAppKeyAuth(t *testing.T) {
	dir := isolate(t)
	signer, err := remote.AppKey(dir)
	if err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(filepath.Join(dir, remote.KeyFile))
	if err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("key file mode: %v %v", fi, err)
	}
	line, err := remote.AppPublicKey(dir)
	if err != nil || !strings.HasPrefix(line, "ssh-ed25519 ") || !strings.HasSuffix(line, " agentd-desktop") {
		t.Fatalf("public line %q %v", line, err)
	}
	again, _ := remote.AppKey(dir)
	if string(again.PublicKey().Marshal()) != string(signer.PublicKey().Marshal()) {
		t.Fatal("app key regenerated")
	}
	srv := sshtest.New(t, sshtest.Options{AuthorizedKeys: []ssh.PublicKey{signer.PublicKey()}})
	h := testHost(srv)
	h.Auth = backend.AuthGenerated
	s := dial(t, srv, h, remote.SSHConfig{Dir: dir, Trust: func(backend.Host, string) bool { return true }})
	res, err := s.Exec(context.Background(), "echo ok", nil)
	if err != nil || strings.TrimSpace(res.Stdout) != "ok" {
		t.Fatalf("exec with app key: %+v %v", res, err)
	}
	// Wrong password host: auth fails cleanly.
	h.Auth = backend.AuthPassword
	_, err = remote.DialSSH(context.Background(), h, pwConfig(dir, true))
	if err == nil {
		t.Fatal("password login accepted by a key-only server")
	}
}

func TestEncryptedKeyFile(t *testing.T) {
	isolate(t)
	path := filepath.Join(t.TempDir(), "id")
	if err := writeEncryptedKey(path, "pw"); err != nil {
		t.Fatal(err)
	}
	var asked atomic.Int32
	s, err := remote.LoadKeyFile(path, func(string) ([]byte, error) { asked.Add(1); return []byte("pw"), nil })
	if err != nil || s == nil || asked.Load() != 1 {
		t.Fatalf("encrypted key: %v asked=%d", err, asked.Load())
	}
	if _, err := remote.LoadKeyFile(path, nil); err == nil {
		t.Fatal("encrypted key loaded without a prompt")
	}
}

func TestExec(t *testing.T) {
	dir := isolate(t)
	srv := sshtest.New(t, sshtest.Options{Password: "secret"})
	s := dial(t, srv, testHost(srv), pwConfig(dir, true))
	ctx := context.Background()

	res, err := s.Exec(ctx, `echo out; echo err >&2; exit 3`, nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.Exit != 3 || res.Stdout != "out\n" || res.Stderr != "err\n" {
		t.Fatalf("exec: %+v", res)
	}
	res, err = s.Exec(ctx, `cat; printf '%s' "it's"`, []byte("in:"))
	if err != nil || res.Exit != 0 || res.Stdout != "in:it's" {
		t.Fatalf("stdin/quoting: %+v %v", res, err)
	}

	cctx, cancel := context.WithTimeout(ctx, 300*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err = s.Exec(cctx, "sleep 30", nil)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("cancel: %v", err)
	}
	if d := time.Since(start); d > 5*time.Second {
		t.Fatalf("cancel took %v", d)
	}
	// The connection is still usable.
	if res, err := s.Exec(ctx, "true", nil); err != nil || res.Exit != 0 {
		t.Fatalf("after cancel: %+v %v", res, err)
	}
}

func readUntil(t *testing.T, r *bufio.Reader, want string) string {
	t.Helper()
	var got strings.Builder
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		b, err := r.ReadByte()
		if err != nil {
			t.Fatalf("read: %v (got %q)", err, got.String())
		}
		got.WriteByte(b)
		if strings.Contains(got.String(), want) {
			return got.String()
		}
	}
	t.Fatalf("timeout waiting for %q, got %q", want, got.String())
	return ""
}

func testPTY(t *testing.T, sh backend.Shell) {
	t.Helper()
	ctx := context.Background()
	p, err := sh.PTY(ctx, `echo "v=$FOO"; stty size; read x; stty size; exit 7`, 80, 24, map[string]string{"FOO": "a b'c"})
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	r := bufio.NewReader(p)
	readUntil(t, r, "v=a b'c")
	readUntil(t, r, "24 80")
	if err := p.Resize(100, 40); err != nil {
		t.Fatal(err)
	}
	time.Sleep(50 * time.Millisecond)
	if _, err := p.Write([]byte("\r")); err != nil {
		t.Fatal(err)
	}
	readUntil(t, r, "40 100")
	_, _ = io.Copy(io.Discard, r)
	code, err := p.Wait()
	if err != nil || code != 7 {
		t.Fatalf("wait: %d %v", code, err)
	}
}

func TestSSHPTY(t *testing.T) {
	dir := isolate(t)
	srv := sshtest.New(t, sshtest.Options{Password: "secret"})
	testPTY(t, dial(t, srv, testHost(srv), pwConfig(dir, true)))
}

func TestLocalShell(t *testing.T) {
	isolate(t)
	l := &remote.LocalShell{}
	res, err := l.Exec(context.Background(), "echo hi; exit 4", nil)
	if err != nil || res.Exit != 4 || res.Stdout != "hi\n" {
		t.Fatalf("local exec: %+v %v", res, err)
	}
	cctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	if _, err := l.Exec(cctx, "sleep 30", nil); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("local cancel: %v", err)
	}
	testPTY(t, l)
}

func echoServer(t *testing.T) string {
	t.Helper()
	d, err := os.MkdirTemp("", "rtest")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(d) })
	sock := filepath.Join(d, "echo.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() { _, _ = io.Copy(c, c); _ = c.Close() }()
		}
	}()
	return sock
}

func TestDialUnix(t *testing.T) {
	dir := isolate(t)
	sock := echoServer(t)
	srv := sshtest.New(t, sshtest.Options{Password: "secret"})
	for name, sh := range map[string]backend.Shell{
		"ssh":   dial(t, srv, testHost(srv), pwConfig(dir, true)),
		"local": &remote.LocalShell{},
	} {
		c, err := sh.DialUnix(context.Background(), sock)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		_, _ = c.Write([]byte("ping\n"))
		line, err := bufio.NewReader(c).ReadString('\n')
		if err != nil || line != "ping\n" {
			t.Fatalf("%s echo: %q %v", name, line, err)
		}
		_ = c.Close()
	}
	s := dial(t, srv, testHost(srv), pwConfig(dir, true))
	if _, err := s.DialUnix(context.Background(), sock+".missing"); err == nil {
		t.Fatal("dial of a missing socket succeeded")
	}
}

func TestKeepaliveAndDone(t *testing.T) {
	dir := isolate(t)
	srv := sshtest.New(t, sshtest.Options{Password: "secret"})
	cfg := pwConfig(dir, true)
	cfg.Keepalive = 50 * time.Millisecond
	s := dial(t, srv, testHost(srv), cfg)
	deadline := time.Now().Add(timeout)
	for srv.Keepalives.Load() < 2 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if srv.Keepalives.Load() < 2 {
		t.Fatal("no keepalives")
	}
	srv.DropConns()
	select {
	case <-s.Done():
	case <-time.After(timeout):
		t.Fatal("Done not closed after the connection dropped")
	}
}
