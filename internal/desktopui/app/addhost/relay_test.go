package addhost_test

import (
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/digiogithub/agentd/internal/desktopui/app/apptest"
	_ "github.com/digiogithub/agentd/internal/desktopui/app/hosts"
	"github.com/digiogithub/agentd/internal/desktopui/backend"
	"github.com/digiogithub/agentd/internal/desktopui/backend/fake"
)

const relayTile = "Relay (invitación), Pega el enlace de agentd relay invite"

func TestRelayInvitePairs(t *testing.T) {
	e := apptest.New(t, "/hosts/add", 1400, 1000)
	e.F.Delay = 300 * time.Millisecond // the owner takes a moment to approve
	e.Click(relayTile)
	e.Has("Añadir host por relay", "Enlace de invitación", "Nombre de este dispositivo",
		"El enlace contiene un código de un solo uso: no lo compartas.", "Emparejar")
	e.Fill("Enlace de invitación", fake.InviteLink)
	e.Fill("Nombre de este dispositivo", "mi-portatil")
	e.Click("Emparejar")
	if !e.Eventually(time.Second, func() bool { return e.UI.HasText("Aprueba el dispositivo en el host") }) {
		t.Fatalf("no approval wait: %q", e.UI.Texts())
	}
	e.Has("agentd relay invite pregunta allí si acepta «mi-portatil». Esperando respuesta…")
	if !e.Eventually(2*time.Second, func() bool { return e.A.Router.Path() == "/hosts" }) {
		t.Fatalf("path %q; texts %q", e.A.Router.Path(), e.UI.Texts())
	}
	if p := e.F.Paired(); !slices.Equal(p, []string{"mi-portatil"}) {
		t.Errorf("paired as %v", p)
	}
	if !e.Eventually(time.Second, func() bool { _, ok := e.A.Live.Host("caja-relay"); return ok }) {
		t.Fatal("relay host not listed")
	}
	h, _ := e.A.Live.Host("caja-relay")
	if !h.Host.IsRelay() || h.Host.Relay.Scope != backend.ScopeAdmin || h.State != backend.HostOnline {
		t.Errorf("host %+v", h)
	}
	e.Has("caja-relay", "relay.example.com · Relay", "Relay · admin")
}

func TestRelayInviteCancel(t *testing.T) {
	e := apptest.New(t, "/hosts/add", 1400, 1000)
	e.F.Delay = time.Hour
	e.Click(relayTile)
	e.Fill("Enlace de invitación", fake.InviteLink)
	e.Click("Emparejar")
	if !e.Eventually(time.Second, func() bool { return e.UI.HasText("Aprueba el dispositivo en el host") }) {
		t.Fatalf("no approval wait: %q", e.UI.Texts())
	}
	e.Click("Cancelar")
	if e.UI.HasText("Añadir host por relay") {
		t.Errorf("dialog still open")
	}
	if _, ok := e.A.Live.Host("caja-relay"); ok {
		t.Errorf("host added after cancel")
	}
	// The empty name means the hostname.
	if p := e.F.Paired(); len(p) != 1 || p[0] != "" {
		t.Errorf("paired %v", p)
	}
}

func TestRelayInviteErrors(t *testing.T) {
	for _, tc := range []struct{ link, want string }{
		{"https://example.com/not-an-invite", "No es un enlace de invitación de agentd relay válido (agentd://relay?…)"},
		{fake.DeniedInviteLink, "El dueño del host rechazó el emparejamiento"},
	} {
		e := apptest.New(t, "/hosts/add", 1400, 1000)
		e.Click(relayTile)
		e.Fill("Enlace de invitación", tc.link)
		e.Click("Emparejar")
		if !e.Eventually(time.Second, func() bool {
			for _, p := range e.A.Toasts.Pending() {
				if strings.Contains(p, tc.want) {
					return true
				}
			}
			return e.UI.HasText(tc.want)
		}) {
			t.Errorf("%s: no %q toast; texts %q", tc.link, tc.want, e.UI.Texts())
		}
		if e.A.Router.Path() != "/hosts/add" {
			t.Errorf("navigated to %q after a failed pairing", e.A.Router.Path())
		}
	}
}
