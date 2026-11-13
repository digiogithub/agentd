package config

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
)

func TestResolveRunMatchesResolveTarget(t *testing.T) {
	root := t.TempDir()
	web := filepath.Join(root, "web")
	acmeDevops := filepath.Join(root, "acme", "devops")
	for _, d := range []string{web, acmeDevops} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	a, err := LoadAliases("", "webapp="+web)
	if err != nil {
		t.Fatal(err)
	}
	t.Chdir(root)
	for _, target := range []string{"webapp", "web", "./web", acmeDevops, "acme/devops", "."} {
		wantDir, wantLabel, err := a.ResolveTarget(target)
		if err != nil {
			t.Fatal(err)
		}
		// Resolved in the daemon: process cwd differs, base = caller cwd.
		got, err := a.ResolveRun(RunRequest{Target: target, Base: root, Harness: "codex"})
		if err != nil {
			t.Fatal(err)
		}
		if got.Cwd != wantDir || got.Label != wantLabel || got.Name != "codex@"+wantLabel ||
			got.Profile != "codex" || !slices.Equal(got.Cmd, []string{"codex"}) {
			t.Fatalf("target %q: %+v, want dir %q label %q", target, got, wantDir, wantLabel)
		}
	}
}

func TestResolveRunDefaults(t *testing.T) {
	dir := t.TempDir()
	var a *Aliases
	got, err := a.ResolveRun(RunRequest{Target: dir})
	if err != nil || got.Harness != "claude" || got.Name != "claude@"+filepath.Base(dir) {
		t.Fatalf("default harness: %+v %v", got, err)
	}
	got, err = a.ResolveRun(RunRequest{Target: dir, Cmd: []string{"/usr/bin/bash", "-c", "x"}})
	if err != nil || got.Harness != "/usr/bin/bash" || got.Profile != "bash" ||
		got.Name != "bash@"+filepath.Base(dir) || len(got.Cmd) != 3 {
		t.Fatalf("cmd harness: %+v %v", got, err)
	}
	got, err = a.ResolveRun(RunRequest{Target: dir, Harness: "pando", Name: "n", Profile: "p"})
	if err != nil || got.Name != "n" || got.Profile != "p" || got.Cmd[0] != "pando" {
		t.Fatalf("overrides: %+v %v", got, err)
	}
	if _, err := a.ResolveRun(RunRequest{Target: filepath.Join(dir, "nope")}); err == nil {
		t.Fatal("missing dir should fail")
	}
	if _, _, err := a.ResolveTargetIn("rel", "relbase"); err == nil {
		t.Fatal("relative base should fail")
	}
}

func TestProfileNames(t *testing.T) {
	c := Default()
	c.Profiles = map[string]ProfileConfig{"mine": {}, "claude": {}}
	got := c.ProfileNames()
	want := []string{"claude", "codex", "default", "mine", "pando"}
	if !slices.Equal(got, want) {
		t.Fatalf("ProfileNames = %v, want %v", got, want)
	}
}
