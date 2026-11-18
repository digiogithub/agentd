package provision

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// A tool installed in a user-level directory that is not on the (service or
// non-interactive SSH) PATH, e.g. a bun or nvm global, must be detected.
func TestDetectFindsUserLevelBinDirs(t *testing.T) {
	home := t.TempDir()
	put := func(dir, name string) {
		d := filepath.Join(home, dir)
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(d, name), []byte("#!/bin/sh\necho 1.2.3\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	put(".bun/bin", "codex")
	put(".nvm/versions/node/v22.1.0/bin", "grok")
	cmd := exec.Command("sh", "-c", DetectScript())
	cmd.Env = []string{"HOME=" + home, "PATH=/usr/bin:/bin"}
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("script: %v", err)
	}
	c, err := ParseDetect(string(out))
	if err != nil {
		t.Fatal(err)
	}
	for _, n := range []string{"codex", "grok"} {
		if !c.Has(n) || c.Tools[n].Version != "1.2.3" {
			t.Errorf("%s not detected: %+v", n, c.Tools[n])
		}
	}
	if c.Has("gemini") {
		t.Error("gemini reported installed")
	}
}
