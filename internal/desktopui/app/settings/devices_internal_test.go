package settings

import (
	"testing"

	"github.com/digiogithub/agentd/internal/desktopui/backend"
	"github.com/digiogithub/agentd/internal/proto"
)

func TestCanRevoke(t *testing.T) {
	other := proto.RelayDeviceInfo{ID: "dx"}
	self := proto.RelayDeviceInfo{ID: "me"}
	ssh := backend.Host{ID: "a"}
	op := backend.Host{ID: "r", Relay: &backend.RelayTarget{DeviceID: "me", Scope: backend.ScopeOperate, Kind: "owner"}}
	admin := backend.Host{ID: "r", Relay: &backend.RelayTarget{DeviceID: "me", Scope: backend.ScopeAdmin, Kind: "owner"}}
	limited := backend.Host{ID: "r", Relay: &backend.RelayTarget{DeviceID: "me", Scope: backend.ScopeAdmin, Kind: "owner", Sessions: []string{"x@*"}}}
	for _, c := range []struct {
		name string
		h    backend.Host
		d    proto.RelayDeviceInfo
		want bool
	}{
		{"ssh any", ssh, other, true},
		{"operate other", op, other, false},
		{"operate self", op, self, true},
		{"admin other", admin, other, true},
		{"limited admin other", limited, other, false},
		{"limited admin self", limited, self, true},
	} {
		if got := canRevoke(c.h, c.d); got != c.want {
			t.Errorf("%s: canRevoke = %v, want %v", c.name, got, c.want)
		}
	}
}
