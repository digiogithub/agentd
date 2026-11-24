package daemon_test

import (
	"testing"
	"time"

	"github.com/digiogithub/agentd/internal/client"
	"github.com/digiogithub/agentd/internal/proto"
	"github.com/digiogithub/agentd/internal/relayclient"
)

// A paired device may look at and revoke itself; only an owner device with
// the admin scope sees and revokes the others.
func TestRelayDeviceListsAndRevokes(t *testing.T) {
	e, _ := relayEnv(t)
	c := e.dial(t)

	opTg, opDev := pairDevice(t, c, proto.RelayArgs{Scope: "operate"}, true)
	adTg, adDev := pairDevice(t, c, proto.RelayArgs{Scope: "admin"}, true)
	_, viewDev := pairDevice(t, c, proto.RelayArgs{Scope: "view"}, true)

	opDial := &relayclient.Dialer{Target: opTg}
	defer func() { _ = opDial.Close() }()
	op := remoteClient(t, opDial)
	adDial := &relayclient.Dialer{Target: adTg}
	defer func() { _ = adDial.Close() }()
	ad := remoteClient(t, adDial)

	// An operate device sees only itself and cannot touch another device.
	res, err := op.Relay(proto.RelayArgs{Action: proto.RelayDevices})
	if err != nil || len(res.Devices) != 1 || res.Devices[0].ID != opDev.ID || len(res.Invites) != 0 {
		t.Fatalf("operate devices: %+v %v", res, err)
	}
	if _, err := op.Relay(proto.RelayArgs{Action: proto.RelayRevoke, Device: viewDev.ID}); client.ErrorCode(err) != proto.CodeForbidden {
		t.Fatalf("operate revoking another device: %v", err)
	}
	for _, action := range []string{proto.RelayStatus, proto.RelayInvite, proto.RelayPending, proto.RelayApprove, proto.RelayCancel} {
		if _, err := op.Relay(proto.RelayArgs{Action: action}); client.ErrorCode(err) != proto.CodeForbidden {
			t.Fatalf("relay %s over the relay: %v", action, err)
		}
	}

	// An admin owner device sees every device and revokes another one.
	res, err = ad.Relay(proto.RelayArgs{Action: proto.RelayDevices})
	if err != nil || len(res.Devices) != 3 || len(res.Invites) != 0 {
		t.Fatalf("admin devices: %+v %v", res, err)
	}
	if _, err := ad.Relay(proto.RelayArgs{Action: proto.RelayRevoke, Device: viewDev.ID}); err != nil {
		t.Fatalf("admin revoke: %v", err)
	}
	if _, err := ad.Relay(proto.RelayArgs{Action: proto.RelayRevoke, Device: "nobody"}); client.ErrorCode(err) != proto.CodeNotFound {
		t.Fatalf("admin revoking an unknown device: %v", err)
	}

	// Self-revoke: the answer arrives, then the device is gone and its
	// tunnel is closed.
	got, err := op.Relay(proto.RelayArgs{Action: proto.RelayRevoke})
	if err != nil || got.Device == nil || got.Device.ID != opDev.ID {
		t.Fatalf("self revoke: %+v %v", got, err)
	}
	host, err := c.Relay(proto.RelayArgs{Action: proto.RelayDevices})
	if err != nil || len(host.Devices) != 1 || host.Devices[0].ID != adDev.ID {
		t.Fatalf("devices left on the host: %+v %v", host.Devices, err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, err := op.Ping(); err != nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the revoked device still reaches the daemon")
		}
		time.Sleep(50 * time.Millisecond)
	}
}
