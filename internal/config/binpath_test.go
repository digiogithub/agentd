package config

import (
	"os"
	"path/filepath"
	"testing"
)

func mkbin(t *testing.T, dir, name string) string {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestFindBinary(t *testing.T) {
	home := t.TempDir()
	bun := mkbin(t, filepath.Join(home, ".bun/bin"), "codex")
	nvm := mkbin(t, filepath.Join(home, ".nvm/versions/node/v20.1.0/bin"), "grok")
	mkbin(t, filepath.Join(home, ".nvm/versions/node/v18.0.0/bin"), "grok")
	onPath := mkbin(t, t.TempDir(), "tool")
	// not executable: ignored
	if err := os.WriteFile(filepath.Join(home, ".bun/bin/plain"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	if p, ok := FindBinary("codex", "/nonexistent", home); !ok || p != bun {
		t.Errorf("codex = %q %v, want %q", p, ok, bun)
	}
	if p, ok := FindBinary("grok", "", home); !ok || p != nvm {
		t.Errorf("grok = %q %v, want newest nvm %q", p, ok, nvm)
	}
	if p, ok := FindBinary("tool", filepath.Dir(onPath), home); !ok || p != onPath {
		t.Errorf("PATH tool = %q %v", p, ok)
	}
	if _, ok := FindBinary("plain", "", home); ok {
		t.Error("non-executable file found")
	}
	if _, ok := FindBinary("missing-xyz", "", home); ok {
		t.Error("missing binary found")
	}
	if p, ok := FindBinary(onPath, "", ""); !ok || p != onPath {
		t.Errorf("slash name = %q %v", p, ok)
	}
}
