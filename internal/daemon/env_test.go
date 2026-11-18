package daemon

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestResolveCmd(t *testing.T) {
	home := t.TempDir()
	dir := filepath.Join(home, ".bun", "bin")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(dir, "fakeharness-xyz")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	base := []string{"HOME=" + home, "PATH=/usr/bin:/bin"}
	env := map[string]string{}
	got := resolveCmd([]string{"fakeharness-xyz", "--flag"}, base, env)
	if got[0] != bin || got[1] != "--flag" {
		t.Fatalf("cmd = %v, want %s first", got, bin)
	}
	if !strings.HasPrefix(env["PATH"], dir+":") {
		t.Errorf("child PATH = %q, want %s prepended", env["PATH"], dir)
	}
	// A user-set PATH is kept; unknown names and absolute paths are untouched.
	env = map[string]string{"PATH": "/x"}
	resolveCmd([]string{"fakeharness-xyz"}, base, env)
	if env["PATH"] != "/x" {
		t.Errorf("explicit PATH overridden: %q", env["PATH"])
	}
	if g := resolveCmd([]string{"nope-xyz"}, base, map[string]string{}); g[0] != "nope-xyz" {
		t.Errorf("unknown command changed: %v", g)
	}
	if g := resolveCmd([]string{"/bin/sh", "-c", "x"}, base, map[string]string{}); g[0] != "/bin/sh" {
		t.Errorf("absolute path changed: %v", g)
	}
}
