package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestHerdrConfig(t *testing.T) {
	if !Default().Herdr.On() || !Default().Herdr.RunAllowed("/usr/bin/anything") {
		t.Fatal("defaults: enabled, any program")
	}
	dir := t.TempDir()
	p := filepath.Join(dir, "config.toml")
	if err := os.WriteFile(p, []byte("[herdr]\nenabled = false\nread_only = true\nallow_run = [\"claude\", \"codex*\", \"bash\"]\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	c, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	h := c.Herdr
	if h.On() || !h.ReadOnly || !h.RunAllowed("/usr/local/bin/claude") || !h.RunAllowed("codex-beta") || h.RunAllowed("python3") {
		t.Fatalf("herdr = %+v", h)
	}
	if err := os.WriteFile(p, []byte("[herdr]\nallow_run = [\"/bin/sh\"]\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(p); err == nil {
		t.Fatal("a path in allow_run was accepted")
	}
}
