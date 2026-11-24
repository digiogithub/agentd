package settings_test

import (
	"testing"
	"time"

	"github.com/digiogithub/agentd/internal/desktopui/app/apptest"
	"github.com/digiogithub/agentd/internal/desktopui/backend/fake"
)

func TestDevicesListAndRevoke(t *testing.T) {
	e := apptest.New(t, "/settings", 1400, 2400)
	if !e.Eventually(3*time.Second, func() bool { return e.UI.HasText("Móvil de Ana") }) {
		t.Fatalf("devices not shown; texts %q", e.UI.Texts())
	}
	e.Has("DISPOSITIVOS", "Actualizar", "Portátil", "Tablet de invitado", "invitado", "admin", "operar", "solo ver",
		"en línea", "Sesiones: claude@docs, codex@*", "este equipo")

	e.Click("Revocar acceso de Tablet de invitado")
	e.Has("Revocar acceso de «Tablet de invitado»: el dispositivo dejará de poder conectarse a")
	if len(e.F.Revoked()) != 0 {
		t.Fatal("revoked before confirming")
	}
	e.Click("Cancelar")
	if len(e.F.Revoked()) != 0 {
		t.Fatal("revoked after cancel")
	}

	e.Click("Revocar acceso de Tablet de invitado")
	e.Click("Confirmar revocar")
	if r := e.F.Revoked(); len(r) != 1 || r[0].Device != "d7t1v6b9z4" || r[0].HostID == "" {
		t.Fatalf("revoked %+v", r)
	}
	e.Has("Acceso revocado: Tablet de invitado")
	e.Eventually(2*time.Second, func() bool { return !e.UI.HasText("Revocar acceso de Tablet de invitado") })
	if e.UI.HasText("Revocar acceso de Tablet de invitado") {
		t.Errorf("revoked device still listed; texts %q", e.UI.Texts())
	}
}

func TestDevicesRelayHostOnlyOwnEntry(t *testing.T) {
	e := apptest.New(t, "/settings", 1400, 2400)
	if !e.Eventually(3*time.Second, func() bool { return e.UI.HasText("Este portátil") }) {
		t.Fatalf("relay host device not shown; texts %q", e.UI.Texts())
	}
	// nube is an operate device: only its own entry, labelled, revocable.
	e.Has("Este portátil", "este equipo")
	e.Click("Revocar acceso de Este portátil")
	e.Has("perderá el acceso a nube")
	e.Click("Confirmar revocar")
	found := false
	for _, r := range e.F.Revoked() {
		if r.HostID == fake.RelayHost.ID && r.Device == fake.RelayHost.Relay.DeviceID {
			found = true
		}
	}
	if !found {
		t.Fatalf("own device not revoked: %+v", e.F.Revoked())
	}
	e.Eventually(2*time.Second, func() bool { return !e.UI.HasText("Revocar acceso de Este portátil") })
	e.Has("Sin dispositivos")
}
