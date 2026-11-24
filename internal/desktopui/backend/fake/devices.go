package fake

import (
	"context"
	"slices"
	"time"

	"github.com/digiogithub/agentd/internal/desktopui/backend"
	"github.com/digiogithub/agentd/internal/proto"
)

// RevokeCall is one recorded RevokeRelayDevice.
type RevokeCall struct {
	HostID string
	Device string
}

// deviceState is the fake's paired devices per host (guarded by Fake.mu).
type deviceState struct {
	byHost  map[string][]proto.RelayDeviceInfo
	revoked []RevokeCall
}

func (f *Fake) devicesLocked(hostID string) []proto.RelayDeviceInfo {
	if f.devs.byHost == nil {
		f.devs.byHost = map[string][]proto.RelayDeviceInfo{}
	}
	if d, ok := f.devs.byHost[hostID]; ok {
		return d
	}
	now := time.Now()
	ago := func(d time.Duration) *time.Time { t := now.Add(-d); return &t }
	in := func(d time.Duration) *time.Time { t := now.Add(d); return &t }
	d := []proto.RelayDeviceInfo{
		{ID: "d9m2k4x7q1", Name: "Móvil de Ana", Platform: "android", Scope: backend.ScopeOperate, Kind: "owner",
			CreatedAt: now.Add(-20 * 24 * time.Hour), LastSeen: ago(0), Online: true},
		{ID: "d3p8w5n2c6", Name: "Portátil", Platform: "linux", Scope: backend.ScopeAdmin, Kind: "owner",
			CreatedAt: now.Add(-60 * 24 * time.Hour), LastSeen: ago(3 * time.Hour)},
		{ID: "d7t1v6b9z4", Name: "Tablet de invitado", Platform: "ios", Scope: backend.ScopeView, Kind: "guest",
			Sessions: []string{"claude@docs", "codex@*"}, CreatedAt: now.Add(-2 * time.Hour),
			ExpiresAt: in(22 * time.Hour), LastSeen: ago(30 * time.Minute)},
	}
	if hostID == "estudio" { // one host without the guest
		d = d[:2]
	}
	if h := f.hostLocked(hostID); h != nil && h.st.Host.Relay != nil {
		// This install's own device, as its host knows it.
		d[1] = proto.RelayDeviceInfo{ID: h.st.Host.Relay.DeviceID, Name: "Este portátil", Platform: "linux",
			Scope: h.st.Host.Relay.Scope, Kind: h.st.Host.Relay.Kind, CreatedAt: now.Add(-9 * 24 * time.Hour),
			LastSeen: ago(0), Online: true}
	}
	f.devs.byHost[hostID] = d
	return d
}

// RelayDevices implements backend.Backend: every device, or for a relay host
// without CapDevices only this install's own entry (what the daemon does).
func (f *Fake) RelayDevices(ctx context.Context, hostID string) ([]proto.RelayDeviceInfo, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	h := f.hostLocked(hostID)
	if h == nil {
		return nil, proto.Errorf(proto.CodeNotFound, "no such host %q", hostID)
	}
	all := f.devicesLocked(hostID)
	if h.st.Host.Relay != nil && !h.st.Host.Allows(backend.CapDevices) {
		own := []proto.RelayDeviceInfo{}
		for _, d := range all {
			if d.ID == h.st.Host.Relay.DeviceID {
				own = append(own, d)
			}
		}
		return own, nil
	}
	return slices.Clone(all), nil
}

// RevokeRelayDevice implements backend.Backend and records the call. A relay
// host without CapDevices only lets this install revoke itself.
func (f *Fake) RevokeRelayDevice(ctx context.Context, hostID, device string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.devs.revoked = append(f.devs.revoked, RevokeCall{HostID: hostID, Device: device})
	h := f.hostLocked(hostID)
	if h == nil {
		return proto.Errorf(proto.CodeNotFound, "no such host %q", hostID)
	}
	if h.st.Host.Relay != nil && !h.st.Host.Allows(backend.CapDevices) &&
		device != "" && device != h.st.Host.Relay.DeviceID {
		return proto.Errorf(proto.CodeForbidden, "this device may only revoke itself")
	}
	if device == "" && h.st.Host.Relay != nil {
		device = h.st.Host.Relay.DeviceID
	}
	all := f.devicesLocked(hostID)
	i := slices.IndexFunc(all, func(d proto.RelayDeviceInfo) bool { return d.ID == device || d.Name == device })
	if i < 0 {
		return proto.Errorf(proto.CodeNotFound, "no such device %q", device)
	}
	f.devs.byHost[hostID] = slices.Delete(slices.Clone(all), i, i+1)
	return nil
}

// Revoked returns the recorded RevokeRelayDevice calls.
func (f *Fake) Revoked() []RevokeCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.devs.revoked)
}
