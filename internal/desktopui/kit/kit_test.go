package kit

import (
	"io/fs"
	"os"
	"path"
	"reflect"
	"regexp"
	"strings"
	"testing"
)

// Every Icon* constant has an embedded SVG.
func TestIconsEmbedded(t *testing.T) {
	src, err := os.ReadFile("icons.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range regexp.MustCompile(`Icon\w+\s+=\s+"([a-z0-9-]+)"`).FindAllStringSubmatch(string(src), -1) {
		if !HasIcon(m[1]) {
			t.Errorf("icon %q not embedded", m[1])
		}
	}
}

func TestFontsRegister(t *testing.T) {
	if err := RegisterFonts(); err != nil {
		t.Fatal(err)
	}
	if MonoFamily() != FontMono+", monospace" {
		t.Errorf("mono %q", MonoFamily())
	}
}

// Every embedded font is registered and ships with its license text.
func TestFontFilesAndLicenses(t *testing.T) {
	listed := map[string]bool{}
	for _, f := range fontFiles {
		listed[f.name] = true
		lic, err := fontFS.ReadFile("fonts/" + f.license)
		if err != nil || !strings.Contains(string(lic), "SIL Open Font License, Version 1.1") {
			t.Errorf("%s: license %s missing or not OFL 1.1 (%v)", f.name, f.license, err)
		}
	}
	ttfs, err := fs.Glob(fontFS, "fonts/*.ttf")
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range ttfs {
		if !listed[path.Base(p)] {
			t.Errorf("%s embedded but not registered", p)
		}
	}
	if len(ttfs) != len(fontFiles) {
		t.Errorf("%d embedded fonts, %d registered", len(ttfs), len(fontFiles))
	}
}

func TestThemes(t *testing.T) {
	d, l := Resolve(true), Resolve(false)
	if !d.Dark || l.Dark || d.Bg == l.Bg {
		t.Error("dark and light resolve the same")
	}
	// The dark palette is the mockups'.
	if d.Bg != hex("#0D1117") || d.Accent != hex("#9DB4FF") || d.Waiting != hex("#F0A04B") {
		t.Errorf("palette %+v", d)
	}
	if reflect.DeepEqual(d.UI(), l.UI()) {
		t.Error("ui themes equal")
	}
}
