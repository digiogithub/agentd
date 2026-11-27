package daemon

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/digiogithub/agentd/internal/config"
	"github.com/digiogithub/agentd/internal/proto"
)

func TestUploadRelayGate(t *testing.T) {
	req := func() *proto.Request {
		return &proto.Request{Op: proto.OpUpload, Args: []byte(`{"name":"web","ext":"png","data":""}`)}
	}
	view := &relayDevice{ID: "v", Kind: relayDeviceKindOwner, Scope: config.RelayScopeView}
	if _, err := relayGate(view, req()); proto.CodeOf(err) != proto.CodeForbidden {
		t.Fatalf("view device: %v", err)
	}
	op := &relayDevice{ID: "o", Kind: relayDeviceKindOwner, Scope: config.RelayScopeOperate}
	if name, err := relayGate(op, req()); err != nil || name != "web" {
		t.Fatalf("operate device: %q %v", name, err)
	}
	guest := &relayDevice{ID: "g", Kind: relayDeviceKindGuest, Scope: config.RelayScopeOperate, Sessions: []string{"api*"}}
	if _, err := relayGate(guest, req()); proto.CodeOf(err) != proto.CodeNotFound {
		t.Fatalf("guest outside its sessions: %v", err)
	}
}

func TestUploadSweep(t *testing.T) {
	s := newTestServer(t)
	root := t.TempDir()
	s.UploadsDir = root
	dir := filepath.Join(root, "a")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	old, fresh := filepath.Join(dir, "old.png"), filepath.Join(dir, "new.png")
	for _, p := range []string{old, fresh} {
		if err := os.WriteFile(p, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	past := time.Now().Add(-UploadRetention - time.Hour)
	if err := os.Chtimes(old, past, past); err != nil {
		t.Fatal(err)
	}
	gone := filepath.Join(root, "b")
	_ = os.MkdirAll(gone, 0o700)
	only := filepath.Join(gone, "x.png")
	_ = os.WriteFile(only, []byte("x"), 0o600)
	_ = os.Chtimes(only, past, past)

	s.sweepUploads()
	if _, err := os.Stat(old); !os.IsNotExist(err) {
		t.Fatalf("old file kept: %v", err)
	}
	if _, err := os.Stat(fresh); err != nil {
		t.Fatalf("fresh file removed: %v", err)
	}
	if _, err := os.Stat(gone); !os.IsNotExist(err) {
		t.Fatalf("empty dir kept: %v", err)
	}
}
