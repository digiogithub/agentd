package main

import (
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestShimConflicts(t *testing.T) {
	dir := t.TempDir()
	exe := filepath.Join(dir, "agentd")
	if err := os.WriteFile(exe, []byte("x"), 0o755); err != nil {
		t.Fatal(err)
	}
	ours, foreign, rt := filepath.Join(dir, "ours"), filepath.Join(dir, "foreign"), filepath.Join(dir, "rt")
	for _, d := range []string{ours, foreign, rt} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := linkShim(ours, exe); err != nil {
		t.Fatal(err)
	}
	if err := linkShim(ours, exe); err != nil {
		t.Fatalf("relinking our own shim: %v", err)
	}
	if err := os.WriteFile(filepath.Join(foreign, "herdr-core"), []byte("real"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := linkShim(foreign, exe); err == nil {
		t.Fatal("linkShim replaced a foreign herdr-core")
	}
	if got := shimConflicts(exe, []string{ours}, []string{rt}); len(got) != 0 {
		t.Fatalf("our own shim reported: %v", got)
	}
	got := shimConflicts(exe, []string{ours, foreign}, []string{rt})
	if len(got) != 1 || !strings.Contains(got[0], filepath.Join(foreign, "herdr-core")) {
		t.Fatalf("foreign binary: %v", got)
	}

	// A live herdr-core socket is reported; a stale one and holder sockets are not.
	l, err := net.Listen("unix", filepath.Join(rt, "default.sock"))
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	if err := os.WriteFile(filepath.Join(rt, "stale.sock"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	h, err := net.Listen("unix", filepath.Join(rt, "default.holder.abc.sock"))
	if err != nil {
		t.Fatal(err)
	}
	defer h.Close()
	got = shimConflicts(exe, []string{ours}, []string{rt})
	if len(got) != 1 || !strings.Contains(got[0], "default.sock") {
		t.Fatalf("running core: %v", got)
	}
}
