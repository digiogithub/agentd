package dirlist_test

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/digiogithub/agentd/internal/desktopui/backend"
	"github.com/digiogithub/agentd/internal/desktopui/dirlist"
)

func run(t *testing.T, home, path string, hidden bool) (backend.DirListing, error) {
	t.Helper()
	cmd := exec.Command("sh", "-c", "echo motd noise; "+dirlist.Script(path, hidden))
	cmd.Env = append(os.Environ(), "HOME="+home)
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("sh: %v", err)
	}
	return dirlist.Parse(string(out))
}

func names(l backend.DirListing) []string {
	var n []string
	for _, e := range l.Entries {
		n = append(n, e.Name)
	}
	return n
}

func mk(t *testing.T, root string, dirs ...string) {
	t.Helper()
	for _, d := range dirs {
		if err := os.MkdirAll(filepath.Join(root, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
}

func TestListTree(t *testing.T) {
	root := t.TempDir()
	mk(t, root, "beta", "Alpha", "with space", "it's \"q\"", "new\nline", "-dash", ".hidden", "..dots", "locked", "$HOME;x")
	os.WriteFile(filepath.Join(root, "file.txt"), nil, 0o644)
	os.Symlink(filepath.Join(root, "beta"), filepath.Join(root, "link-to-dir"))
	os.Symlink(filepath.Join(root, "file.txt"), filepath.Join(root, "link-to-file"))
	if err := os.Chmod(filepath.Join(root, "locked"), 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(filepath.Join(root, "locked"), 0o755) })

	l, err := run(t, "/h", root, false)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"$HOME;x", "-dash", "Alpha", "beta", "it's \"q\"", "link-to-dir", "locked", "new\nline", "with space"}
	if got := names(l); strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("entries %q, want %q", got, want)
	}
	if l.Path != root || l.Home != "/h" || l.Truncated {
		t.Errorf("listing %+v", l)
	}
	for _, e := range l.Entries {
		if e.Enterable != (e.Name != "locked") && os.Getuid() != 0 {
			t.Errorf("%q enterable=%v", e.Name, e.Enterable)
		}
	}

	l, err = run(t, "/h", root, true)
	if err != nil {
		t.Fatal(err)
	}
	got := strings.Join(names(l), "|")
	if !strings.Contains(got, ".hidden") || !strings.Contains(got, "..dots") {
		t.Errorf("hidden entries missing: %q", got)
	}
}

func TestHomeTildeAndErrors(t *testing.T) {
	home := t.TempDir()
	mk(t, home, "src/api", "Documentos")
	l, err := run(t, home, "", false)
	if err != nil || l.Path != home || strings.Join(names(l), "|") != "Documentos|src" {
		t.Fatalf("home: %+v %v", l, err)
	}
	l, err = run(t, home, "~/src", false)
	if err != nil || l.Path != filepath.Join(home, "src") || strings.Join(names(l), "|") != "api" {
		t.Fatalf("tilde: %+v %v", l, err)
	}
	if _, err := run(t, home, filepath.Join(home, "nope"), false); !errors.Is(err, backend.ErrDirNotFound) {
		t.Errorf("missing: %v", err)
	}
	if os.Getuid() != 0 {
		locked := filepath.Join(home, "src")
		os.Chmod(locked, 0)
		defer os.Chmod(locked, 0o755)
		if _, err := run(t, home, locked, false); !errors.Is(err, backend.ErrDirDenied) {
			t.Errorf("denied: %v", err)
		}
		// Traversable but unreadable (mode 111): cannot be listed either.
		os.Chmod(locked, 0o111)
		if _, err := run(t, home, locked, false); !errors.Is(err, backend.ErrDirDenied) {
			t.Errorf("unreadable: %v", err)
		}
	}
	// A file is not a directory.
	os.WriteFile(filepath.Join(home, "f"), nil, 0o644)
	if _, err := run(t, home, filepath.Join(home, "f"), false); !errors.Is(err, backend.ErrDirNotFound) {
		t.Errorf("file: %v", err)
	}
}

func TestCapAndQuoting(t *testing.T) {
	root := t.TempDir()
	for i := 0; i < backend.MaxDirEntries+5; i++ {
		os.Mkdir(filepath.Join(root, "d"+strings.Repeat("0", 5-len(itoa(i)))+itoa(i)), 0o755)
	}
	l, err := run(t, "/h", root, false)
	if err != nil || !l.Truncated || len(l.Entries) != backend.MaxDirEntries {
		t.Fatalf("cap: %d entries truncated=%v err=%v", len(l.Entries), l.Truncated, err)
	}
	// A hostile path is data, never code.
	evil := filepath.Join(root, "x'; touch "+filepath.Join(root, "pwned")+"; '")
	if _, err := run(t, "/h", evil, false); !errors.Is(err, backend.ErrDirNotFound) {
		t.Errorf("evil: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "pwned")); err == nil {
		t.Fatal("path was interpolated into the script")
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	s := ""
	for ; n > 0; n /= 10 {
		s = string(rune('0'+n%10)) + s
	}
	return s
}

func TestParseRejectsGarbage(t *testing.T) {
	for _, in := range []string{"", "no marker", "@@agentd-ls\x00", "@@agentd-ls\x00ok\x00/a\x00/h\x00D\x00x\x001\x00", "@@agentd-ls\x00weird\x00"} {
		if _, err := dirlist.Parse(in); err == nil {
			t.Errorf("Parse(%q) succeeded", in)
		}
	}
}
