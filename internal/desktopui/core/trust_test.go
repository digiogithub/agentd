package core_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/crypto/ssh"

	"github.com/digiogithub/agentd/internal/desktopui/backend"
	"github.com/digiogithub/agentd/internal/desktopui/remote/sshtest"
)

// TestHostKeyErrorThenTrust: without TrustHost an unknown key fails Connect
// at once with a HostKeyError; TrustHostKey stores it and Connect succeeds.
func TestHostKeyErrorThenTrust(t *testing.T) {
	dir := isolate(t)
	opts := sshOpts(dir, "/nonexistent")
	opts.TrustHost = nil
	b := newBackend(t, opts)
	srv := sshHost(t, b)
	ctx := context.Background()

	err := b.Connect(ctx, "box")
	var hk *backend.HostKeyError
	if !errors.As(err, &hk) {
		t.Fatalf("want HostKeyError, got %v", err)
	}
	want := ssh.FingerprintSHA256(srv.HostKey)
	if hk.HostID != "box" || hk.Fingerprint != want || hk.KeyType != srv.HostKey.Type() || hk.HostKeyFingerprint() != want {
		t.Fatalf("host key error %+v", hk)
	}
	hosts := b.WatchHosts(ctx)
	waitHosts(t, hosts, "box key unknown", func(hs []backend.HostStatus) bool {
		h, ok := hostStatus(hs, "box")
		return ok && h.ErrCode == backend.HostErrKeyUnknown
	})

	if err := b.TrustHostKey(ctx, "box", "not-a-fingerprint"); err == nil {
		t.Fatal("bad fingerprint accepted")
	}
	if err := b.TrustHostKey(ctx, "box", want); err != nil {
		t.Fatal(err)
	}
	if err := b.Connect(ctx, "box"); err != nil {
		t.Fatalf("connect after trust: %v", err)
	}
	waitHosts(t, hosts, "box online with key", func(hs []backend.HostStatus) bool {
		h, ok := hostStatus(hs, "box")
		return ok && h.State == backend.HostOnline && h.Host.HostKey == want
	})
	// Same key again is fine; another one is a hard error.
	if err := b.TrustHostKey(ctx, "box", want); err != nil {
		t.Fatal(err)
	}
	if err := b.TrustHostKey(ctx, "box", "SHA256:other"); !errors.Is(err, backend.ErrHostKeyChanged) {
		t.Fatalf("replace key: %v", err)
	}
	if err := b.TrustHostKey(ctx, backend.LocalHostID, want); err == nil {
		t.Fatal("trust on local accepted")
	}
}

func TestHostKeyMismatchCode(t *testing.T) {
	dir := isolate(t)
	b := newBackend(t, sshOpts(dir, "/nonexistent"))
	srv := sshHost(t, b)
	h := backend.Host{ID: "box", Name: "box", Addr: srv.Host, Port: srv.Port, Auth: backend.AuthPassword, HostKey: "SHA256:old"}
	if err := b.SaveHost(context.Background(), h); err != nil {
		t.Fatal(err)
	}
	if err := b.Connect(context.Background(), "box"); !errors.Is(err, backend.ErrHostKeyChanged) {
		t.Fatalf("want ErrHostKeyChanged, got %v", err)
	}
	waitHosts(t, b.WatchHosts(context.Background()), "box key changed", func(hs []backend.HostStatus) bool {
		h, ok := hostStatus(hs, "box")
		return ok && h.ErrCode == backend.HostErrKeyChanged
	})
}

// TestConnectWithPasswordInstallsKey: no password prompt = ErrPasswordRequired;
// ConnectWithPassword logs in once, installs the app key idempotently and
// switches the host to the app key, which then logs in alone.
func TestConnectWithPasswordInstallsKey(t *testing.T) {
	dir := isolate(t)
	opts := sshOpts(dir, "/nonexistent")
	opts.Password = nil
	b := newBackend(t, opts)
	ctx := context.Background()
	line, err := b.PublicKey(ctx)
	if err != nil {
		t.Fatal(err)
	}
	appPub, _, _, _, err := ssh.ParseAuthorizedKey([]byte(line))
	if err != nil {
		t.Fatal(err)
	}
	srv := sshtest.New(t, sshtest.Options{Password: "secret", AuthorizedKeys: []ssh.PublicKey{appPub}})
	h := backend.Host{ID: "box", Name: "box", Addr: srv.Host, Port: srv.Port, User: "tester", Auth: backend.AuthPassword}
	if err := b.SaveHost(ctx, h); err != nil {
		t.Fatal(err)
	}

	if err := b.Connect(ctx, "box"); !errors.Is(err, backend.ErrPasswordRequired) {
		t.Fatalf("want ErrPasswordRequired, got %v", err)
	}
	hosts := b.WatchHosts(ctx)
	waitHosts(t, hosts, "box needs password", func(hs []backend.HostStatus) bool {
		h, ok := hostStatus(hs, "box")
		return ok && h.ErrCode == backend.HostErrPassword
	})
	if err := b.ConnectWithPassword(ctx, "box", "wrong", true); err == nil {
		t.Fatal("wrong password connected")
	}
	if err := b.ConnectWithPassword(ctx, "box", "", true); !errors.Is(err, backend.ErrPasswordRequired) {
		t.Fatalf("empty password: %v", err)
	}
	for range 2 {
		if err := b.ConnectWithPassword(ctx, "box", "secret", true); err != nil {
			t.Fatalf("connect with password: %v", err)
		}
	}
	// The sshtest server runs commands as this process: $HOME is the test's.
	ak := filepath.Join(os.Getenv("HOME"), ".ssh", "authorized_keys")
	data, err := os.ReadFile(ak)
	if err != nil {
		t.Fatal(err)
	}
	if n := strings.Count(string(data), strings.Fields(line)[1]); n != 1 {
		t.Fatalf("key installed %d times: %q", n, data)
	}
	if fi, _ := os.Stat(ak); fi.Mode().Perm() != 0o600 {
		t.Errorf("authorized_keys mode %v", fi.Mode().Perm())
	}
	waitHosts(t, hosts, "box on the app key", func(hs []backend.HostStatus) bool {
		h, ok := hostStatus(hs, "box")
		return ok && h.State == backend.HostOnline && h.Host.Auth == backend.AuthGenerated
	})
	// A new connection logs in with the app key, no password.
	if err := b.SaveHost(ctx, backend.Host{ID: "box", Name: "box", Addr: srv.Host, Port: srv.Port, User: "tester",
		Auth: backend.AuthGenerated, HostKey: ssh.FingerprintSHA256(srv.HostKey)}); err != nil {
		t.Fatal(err)
	}
	if err := b.Connect(ctx, "box"); err != nil {
		t.Fatalf("connect with app key: %v", err)
	}
	if err := b.ConnectWithPassword(ctx, backend.LocalHostID, "x", false); err == nil {
		t.Fatal("password on local accepted")
	}
}
