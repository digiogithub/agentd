package daemon_test

import (
	"bytes"
	"encoding/base64"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/digiogithub/agentd/internal/client"
	"github.com/digiogithub/agentd/internal/config"
	"github.com/digiogithub/agentd/internal/proto"
)

var pngBytes = append([]byte("\x89PNG\r\n\x1a\n"), bytes.Repeat([]byte{7}, 64)...)

func b64(b []byte) string { return base64.StdEncoding.EncodeToString(b) }

func TestUploadStoresFile(t *testing.T) {
	e := startDaemon(t)
	c := e.dial(t)
	mustRun(t, c, "up")

	info, err := c.Info()
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(info.Features, proto.FeatureUpload) {
		t.Fatalf("info.features = %v", info.Features)
	}

	r1, err := c.Upload("up", "png", b64(pngBytes))
	if err != nil {
		t.Fatal(err)
	}
	r2, err := c.Upload("up", "png", b64(pngBytes))
	if err != nil {
		t.Fatal(err)
	}
	root, _ := config.UploadsDir()
	if filepath.Dir(r1.Path) != filepath.Join(root, "up") || r1.Path == r2.Path || r1.Size != len(pngBytes) {
		t.Fatalf("paths %q %q (root %q), size %d", r1.Path, r2.Path, root, r1.Size)
	}
	if !strings.HasSuffix(r1.Path, ".png") || !filepath.IsAbs(r1.Path) {
		t.Fatalf("path %q", r1.Path)
	}
	got, err := os.ReadFile(r1.Path)
	if err != nil || !bytes.Equal(got, pngBytes) {
		t.Fatalf("content: %v", err)
	}
	for p, want := range map[string]os.FileMode{r1.Path: 0o600, filepath.Dir(r1.Path): 0o700, root: 0o700} {
		fi, err := os.Stat(p)
		if err != nil || fi.Mode().Perm() != want {
			t.Fatalf("%s mode %v (%v), want %v", p, fi.Mode().Perm(), err, want)
		}
	}
}

func TestUploadRejects(t *testing.T) {
	e := startDaemon(t)
	c := e.dial(t)
	mustRun(t, c, "up")
	big := append([]byte{0xFF, 0xD8, 0xFF}, make([]byte, proto.MaxUploadBytes)...)
	cases := []struct {
		name, ext, data, code string
	}{
		{"up", "bmp", b64(pngBytes), proto.CodeInvalid},
		{"up", "PNG", b64(pngBytes), proto.CodeInvalid},
		{"up", "jpg", b64(pngBytes), proto.CodeInvalid}, // content does not match
		{"up", "png", "not base64!", proto.CodeInvalid},
		{"up", "png", "", proto.CodeInvalid},
		{"up", "jpg", b64(big), proto.CodeInvalid},
		{"", "png", b64(pngBytes), proto.CodeInvalid},
		{"nope", "png", b64(pngBytes), proto.CodeNotFound},
	}
	for _, tc := range cases {
		if _, err := c.Upload(tc.name, tc.ext, tc.data); client.ErrorCode(err) != tc.code {
			t.Errorf("upload(%q, %q, len %d) = %v, want %s", tc.name, tc.ext, len(tc.data), err, tc.code)
		}
	}
	if r, err := c.Upload("up", "jpg", b64(big[:proto.MaxUploadBytes])); err != nil || r.Size != proto.MaxUploadBytes {
		t.Fatalf("upload of exactly MaxUploadBytes: %+v %v", r, err)
	}
	for _, sig := range []struct {
		ext  string
		data []byte
	}{
		{"jpeg", []byte{0xFF, 0xD8, 0xFF, 0xE0, 1}},
		{"gif", []byte("GIF89a....")},
		{"webp", []byte("RIFF\x00\x00\x00\x00WEBPVP8 ")},
	} {
		if _, err := c.Upload("up", sig.ext, b64(sig.data)); err != nil {
			t.Errorf("%s: %v", sig.ext, err)
		}
	}
}

func TestUploadSessionRulesAndRm(t *testing.T) {
	e := startDaemon(t)
	c := e.dial(t)
	mustRun(t, c, "self")
	mustRun(t, c, "other")
	as := dialAs(t, e, "self")
	if _, err := as.Upload("self", "png", b64(pngBytes)); err != nil {
		t.Fatal(err)
	}
	if _, err := as.Upload("other", "png", b64(pngBytes)); client.ErrorCode(err) != proto.CodeForbidden {
		t.Fatalf("upload for another session: %v", err)
	}
	r, err := c.Upload("other", "png", b64(pngBytes))
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Kill("other", "KILL"); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, err := c.Upload("other", "png", b64(pngBytes)); client.ErrorCode(err) == proto.CodeExited {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("upload to an exited session not refused")
		}
		time.Sleep(20 * time.Millisecond)
	}
	if err := c.Rm("other"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Dir(r.Path)); !os.IsNotExist(err) {
		t.Fatalf("uploads dir after rm: %v", err)
	}
}
