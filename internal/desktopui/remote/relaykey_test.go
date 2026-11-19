package remote_test

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/digiogithub/agentd/internal/desktopui/backend"
	"github.com/digiogithub/agentd/internal/desktopui/remote"
)

func TestRelayKey(t *testing.T) {
	dir := t.TempDir()
	k1, err := remote.RelayKey(dir)
	if err != nil || len(k1) != 32 {
		t.Fatalf("generate: %d %v", len(k1), err)
	}
	fi, err := os.Stat(filepath.Join(dir, remote.RelayKeyFile))
	if err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("mode: %v %v", fi, err)
	}
	k2, err := remote.RelayKey(dir)
	if err != nil || !bytes.Equal(k1, k2) {
		t.Fatalf("reload differs: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, remote.RelayKeyFile), []byte("short\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := remote.RelayKey(dir); err == nil {
		t.Fatal("a broken key file must be an error, never silently replaced")
	}
}

func TestHostStoreRelayHost(t *testing.T) {
	s := remote.NewHostStore(t.TempDir())
	if _, err := s.Save(backend.Host{Name: "box", Relay: &backend.RelayTarget{URL: "https://r.example"}}); err == nil {
		t.Fatal("relay host without route/key accepted")
	}
	rt := &backend.RelayTarget{URL: "https://r.example", Route: "abcdefghijklmnopqrstuvwxyz", HostKey: "k",
		DeviceID: "d123", Scope: backend.ScopeOperate, Sessions: []string{"codex@*"}, Kind: "owner"}
	h, err := s.Save(backend.Host{Name: "Caja", Relay: rt})
	if err != nil || h.ID != "caja" {
		t.Fatalf("save: %+v %v", h, err)
	}
	got, err := s.Get("caja")
	if err != nil || !got.IsRelay() || got.Relay.Scope != backend.ScopeOperate || got.Relay.Sessions[0] != "codex@*" ||
		got.Transport() != backend.TransportRelay || got.Addr != "" {
		t.Fatalf("get: %+v %v", got, err)
	}
}
