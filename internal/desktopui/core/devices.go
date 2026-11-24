package core

import (
	"context"

	"github.com/digiogithub/agentd/internal/client"
	"github.com/digiogithub/agentd/internal/proto"
)

// RelayDevices lists the devices paired with the host (op relay devices) on
// a fresh daemon connection. Local and SSH hosts answer every device; a
// relay device gets what the daemon allows it (all for an unrestricted owner
// admin, else only itself). A daemon without relay answers an error or an
// empty list: the caller shows it quietly.
func (b *Backend) RelayDevices(ctx context.Context, hostID string) ([]proto.RelayDeviceInfo, error) {
	var out []proto.RelayDeviceInfo
	err := b.withClient(ctx, hostID, func(c *client.Client) error {
		r, err := c.Relay(proto.RelayArgs{Action: proto.RelayDevices})
		out = r.Devices
		return err
	})
	if err != nil {
		return nil, err
	}
	if out == nil {
		out = []proto.RelayDeviceInfo{}
	}
	return out, nil
}

// RevokeRelayDevice revokes a device (op relay revoke; id or name). A relay
// device may revoke itself, which ends its own access to the host.
func (b *Backend) RevokeRelayDevice(ctx context.Context, hostID, device string) error {
	return b.withClient(ctx, hostID, func(c *client.Client) error {
		_, err := c.Relay(proto.RelayArgs{Action: proto.RelayRevoke, Device: device})
		return err
	})
}
