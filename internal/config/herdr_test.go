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

func TestHerdrListenPaths(t *testing.T) {
	if Default().Herdr.Listen {
		t.Fatal("listen must be off by default")
	}
	env := map[string]string{"HOME": "/home/u", "XDG_RUNTIME_DIR": "/run/user/7"}
	getenv := func(k string) string { return env[k] }
	p, err := HerdrConfig{}.Paths(getenv)
	if err != nil || p.Socket != "/run/user/7/herdr-core/default.sock" || p.Lock != "/home/u/.local/state/herdr-core/default/core.lock" {
		t.Fatalf("defaults: %+v %v", p, err)
	}
	env["XDG_STATE_HOME"] = "/st"
	p, _ = HerdrConfig{Session: "work"}.Paths(getenv)
	if p.Socket != "/run/user/7/herdr-core/work.sock" || p.Lock != "/st/herdr-core/work/core.lock" {
		t.Fatalf("session work: %+v", p)
	}
	p, _ = HerdrConfig{Socket: "~/hc.sock"}.Paths(getenv)
	if p.Socket != "/home/u/hc.sock" || p.Lock != "/st/herdr-core/default/core.lock" {
		t.Fatalf("socket override: %+v", p)
	}

	dir := t.TempDir()
	cp := filepath.Join(dir, "config.toml")
	for _, bad := range []string{"[herdr]\nsession = \"a/b\"\n", "[herdr]\nsession = \"..\"\n", "[herdr]\nsocket = \"rel.sock\"\n"} {
		if err := os.WriteFile(cp, []byte(bad), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := Load(cp); err == nil {
			t.Fatalf("accepted %q", bad)
		}
	}
	if err := os.WriteFile(cp, []byte("[herdr]\nlisten = true\nsocket = \"/tmp/x.sock\"\nsession = \"s1\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	c, err := Load(cp)
	if err != nil || !c.Herdr.Listen || c.Herdr.Socket != "/tmp/x.sock" || c.Herdr.SessionName() != "s1" {
		t.Fatalf("load: %+v %v", c, err)
	}
}
