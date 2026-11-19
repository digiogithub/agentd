package backend_test

import (
	"testing"

	"github.com/digiogithub/agentd/internal/desktopui/backend"
)

func TestHostAllows(t *testing.T) {
	relay := func(scope, kind string) backend.Host {
		return backend.Host{ID: "r", Relay: &backend.RelayTarget{Scope: scope, Kind: kind}}
	}
	caps := []backend.Capability{backend.CapOperate, backend.CapLaunch, backend.CapMailbox, backend.CapShell}
	for _, tc := range []struct {
		name string
		h    backend.Host
		want []bool // operate launch mailbox shell
	}{
		{"local", backend.Host{ID: backend.LocalHostID}, []bool{true, true, true, true}},
		{"ssh", backend.Host{ID: "s", Addr: "x"}, []bool{true, true, true, true}},
		{"view", relay(backend.ScopeView, "owner"), []bool{false, false, false, false}},
		{"operate", relay(backend.ScopeOperate, "owner"), []bool{true, false, true, false}},
		{"admin", relay(backend.ScopeAdmin, "owner"), []bool{true, true, true, false}},
		{"guest", relay(backend.ScopeOperate, "guest"), []bool{true, false, false, false}},
		{"unknown scope", relay("root", "owner"), []bool{false, false, false, false}},
	} {
		for i, c := range caps {
			if got := tc.h.Allows(c); got != tc.want[i] {
				t.Errorf("%s: Allows(%d) = %v", tc.name, c, got)
			}
		}
	}
	if (backend.Host{ID: "s"}).Transport() != backend.TransportSSH || relay("view", "").Transport() != backend.TransportRelay ||
		(backend.Host{ID: backend.LocalHostID}).Transport() != backend.TransportLocal {
		t.Error("transport")
	}
}
