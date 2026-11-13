package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func loadTOML(t *testing.T, body string) (*Config, error) {
	t.Helper()
	p := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return Load(p)
}

func TestProfileSize(t *testing.T) {
	c := Default()
	for _, n := range []string{"default", "claude", "codex", "pando", "unknown"} {
		if p := c.Profile(n); p.Cols != 160 || p.Rows != 50 {
			t.Fatalf("%s size = %dx%d, want 160x50", n, p.Cols, p.Rows)
		}
	}
	if !c.Run.RestoreSize() {
		t.Fatal("restore_size_on_detach defaults to true")
	}
	c, err := loadTOML(t, "[profiles.default]\ncols = 200\n[profiles.codex]\nrows = 60\n[run]\nrestore_size_on_detach = false\n")
	if err != nil {
		t.Fatal(err)
	}
	if p := c.Profile("claude"); p.Cols != 200 || p.Rows != 50 {
		t.Fatalf("claude = %dx%d", p.Cols, p.Rows)
	}
	if p := c.Profile("codex"); p.Cols != 200 || p.Rows != 60 {
		t.Fatalf("codex = %dx%d", p.Cols, p.Rows)
	}
	if c.Run.RestoreSize() {
		t.Fatal("restore_size_on_detach = false ignored")
	}
}

func TestProfileSizeValidation(t *testing.T) {
	for _, body := range []string{"[profiles.x]\ncols = 0\n", "[profiles.x]\nrows = -1\n", "[profiles.x]\ncols = 5000\n"} {
		if _, err := loadTOML(t, body); err == nil || !strings.Contains(err.Error(), "must be between 1 and") {
			t.Fatalf("%q: err = %v", body, err)
		}
	}
}
