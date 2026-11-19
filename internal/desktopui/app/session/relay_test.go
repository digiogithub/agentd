package session_test

import (
	"testing"

	"github.com/digiogithub/agentd/internal/desktopui/app/apptest"
	"github.com/digiogithub/agentd/internal/desktopui/backend"
)

const landing = "/agents/s/nube/agentd/claude@landing"

// An operate relay device chats and answers but cannot terminate.
func TestRelayOperateHidesTerminate(t *testing.T) {
	e := apptest.New(t, landing, 1400, 900)
	e.Has("claude@landing", "Ajusta los márgenes de la cabecera.", "Mensaje para el agente", "Más acciones")
	if e.UI.HasText("Terminar sesión") {
		t.Errorf("Terminar offered to an operate device")
	}
	// Other agents of SSH hosts keep their Terminar row action.
	e.Has("Terminar codex@webapp")
	if e.UI.HasText("Terminar claude@landing") {
		t.Errorf("row Terminar offered to an operate device")
	}
}

// A view relay device reads only: no composer, no menu answers, a terminal
// that only observes.
func TestRelayViewIsReadOnly(t *testing.T) {
	e := apptest.New(t, "/agents", 1400, 900)
	e.F.SetRelayScope("nube", backend.ScopeView)
	e.Settle()
	e.A.Navigate(landing)
	e.Settle()
	e.Has("claude@landing", "Solo ver: este dispositivo no puede escribir ni responder en este host.")
	for _, s := range []string{"Mensaje para el agente", "Terminar sesión", "Más acciones"} {
		if e.UI.HasText(s) {
			t.Errorf("%q offered to a view device", s)
		}
	}
	e.Click("Terminal")
	e.Has("Solo ver", "Solo ver: este dispositivo tiene permiso de lectura en el host.")
	if e.UI.HasText("Cambiar modo de control") {
		t.Errorf("Control offered to a view device")
	}
	if len(e.F.Acts()) != 0 {
		t.Errorf("acts sent by a view device: %+v", e.F.Acts())
	}
}
