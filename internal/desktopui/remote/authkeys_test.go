package remote_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/digiogithub/agentd/internal/desktopui/remote"
)

func TestInstallAuthorizedKey(t *testing.T) {
	dir := isolate(t)
	home := os.Getenv("HOME")
	ctx := context.Background()
	sh := &remote.LocalShell{}
	line, err := remote.AppPublicKey(dir)
	if err != nil {
		t.Fatal(err)
	}

	// An existing file without a final newline keeps its line.
	ssh := filepath.Join(home, ".ssh")
	if err := os.MkdirAll(ssh, 0o755); err != nil {
		t.Fatal(err)
	}
	ak := filepath.Join(ssh, "authorized_keys")
	if err := os.WriteFile(ak, []byte("ssh-ed25519 AAAAother other"), 0o644); err != nil {
		t.Fatal(err)
	}
	added, err := remote.InstallAuthorizedKey(ctx, sh, line)
	if err != nil || !added {
		t.Fatalf("first install: added=%v err=%v", added, err)
	}
	added, err = remote.InstallAuthorizedKey(ctx, sh, line+"\n")
	if err != nil || added {
		t.Fatalf("second install: added=%v err=%v", added, err)
	}
	data, _ := os.ReadFile(ak)
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) != 2 || lines[0] != "ssh-ed25519 AAAAother other" || lines[1] != line {
		t.Fatalf("authorized_keys %q", data)
	}
	for p, want := range map[string]os.FileMode{ssh: 0o700, ak: 0o600} {
		if fi, err := os.Stat(p); err != nil || fi.Mode().Perm() != want {
			t.Errorf("%s mode %v (%v), want %v", p, fi.Mode().Perm(), err, want)
		}
	}

	for _, bad := range []string{"", "-----BEGIN OPENSSH PRIVATE KEY-----", line + "\n" + line,
		`command="rm -rf /" ` + line, "hello world"} {
		if _, err := remote.InstallAuthorizedKey(ctx, sh, bad); !errors.Is(err, remote.ErrNotPublicKey) {
			t.Errorf("%q: got %v", bad, err)
		}
	}
}
