package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestPushDefaultsAndLoad(t *testing.T) {
	p := Default().Push
	if p.Enabled || strings.Join(p.On, ",") != "question,permission" || time.Duration(p.Debounce) != 30*time.Second ||
		p.CredentialsFile != DefaultPushCredentials || p.IncludePreview {
		t.Fatalf("defaults = %+v", p)
	}
	c, err := loadAPI(t, "[push]\nenabled = true\ncredentials_file = \"/x/key.json\"\non = [\"question\", \"exited\"]\ndebounce = \"5s\"\nhost_label = \"h\"\n")
	if err != nil {
		t.Fatal(err)
	}
	if p := c.Push; !p.Enabled || p.CredentialsFile != "/x/key.json" || len(p.On) != 2 || time.Duration(p.Debounce) != 5*time.Second || p.HostLabel != "h" {
		t.Fatalf("loaded = %+v", p)
	}
	if _, err := loadAPI(t, "[push]\non = [\"nope\"]\n"); err == nil || !strings.Contains(err.Error(), "push.on: unknown reason") {
		t.Fatalf("err = %v", err)
	}
}

func TestPushCredentialsPath(t *testing.T) {
	t.Setenv("HOME", "/home/u")
	got, err := PushConfig{CredentialsFile: "~/k.json"}.CredentialsPath()
	if err != nil || got != "/home/u/k.json" {
		t.Fatalf("got %q, %v", got, err)
	}
	if _, err := (PushConfig{CredentialsFile: "rel/k.json"}).CredentialsPath(); err == nil {
		t.Fatal("relative path accepted")
	}
}

func TestCheckCredentialsFile(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "k.json")
	if err := os.WriteFile(p, []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := CheckCredentialsFile(p); err == nil {
		t.Fatal("0644 accepted")
	}
	if err := os.Chmod(p, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := CheckCredentialsFile(p); err != nil {
		t.Fatal(err)
	}
	if err := CheckCredentialsFile(dir); err == nil {
		t.Fatal("directory accepted")
	}
	if err := CheckCredentialsFile(filepath.Join(dir, "none")); err == nil {
		t.Fatal("missing accepted")
	}
}
