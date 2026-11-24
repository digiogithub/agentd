package core_test

import (
	"context"
	"testing"

	"github.com/digiogithub/agentd/internal/desktopui/backend"
	"github.com/digiogithub/agentd/internal/proto"
)

func TestRelayDevicesAndRevoke(t *testing.T) {
	dir := isolate(t)
	fd := newFakeDaemon(t)
	b := newBackend(t, sshOpts(dir, fd.sock))
	ctx := context.Background()

	devs, err := b.RelayDevices(ctx, backend.LocalHostID)
	if err != nil || len(devs) != 1 || devs[0].ID != "d1" || devs[0].Name != "Móvil" {
		t.Fatalf("devices %+v err %v", devs, err)
	}
	if err := b.RevokeRelayDevice(ctx, backend.LocalHostID, "d1"); err != nil {
		t.Fatal(err)
	}
	fd.mu.Lock()
	got := append([]proto.RelayArgs(nil), fd.relayArgs...)
	fd.relayErr = true
	fd.mu.Unlock()
	if len(got) != 2 || got[0].Action != proto.RelayDevices || got[1].Action != proto.RelayRevoke || got[1].Device != "d1" {
		t.Errorf("relay calls %+v", got)
	}

	// A daemon that refuses the op: an error, no crash.
	if _, err := b.RelayDevices(ctx, backend.LocalHostID); err == nil {
		t.Error("expected an error from a daemon without relay")
	}
	if err := b.RevokeRelayDevice(ctx, "no-such-host", "d1"); err == nil {
		t.Error("expected an error for an unknown host")
	}
}
