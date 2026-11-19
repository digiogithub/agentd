package newsession_test

import (
	"testing"
	"time"

	"github.com/digiogithub/agentd/internal/desktopui/app/apptest"
	"github.com/digiogithub/agentd/internal/desktopui/backend"
)

func TestRelayOperateCannotLaunch(t *testing.T) {
	e := apptest.New(t, "/new/nube", 1400, 900)
	e.Has("nube", "Este dispositivo no puede lanzar sesiones en nube (permiso operar).")
	e.Click("Lanzar")
	if l := e.F.Launched(); len(l) != 0 {
		t.Errorf("launched %+v", l)
	}
}

func TestRelayAdminLaunchesAgentOnly(t *testing.T) {
	e := apptest.New(t, "/agents", 1400, 900)
	e.F.SetRelayScope("nube", backend.ScopeAdmin)
	e.Settle()
	e.A.Navigate("/new/nube")
	e.Settle()
	e.Has("agentd 0.6.2 · relay · admin", "Por relay: solo sesiones agentd con un agente de los perfiles del host (sin terminal ni carpetas).",
		"landing", "Claude Code", "en el host")
	if e.UI.HasText("Explorar…") {
		t.Errorf("folder browser offered over the relay")
	}
	e.Click("Solo terminal") // disabled: stays on Agente
	e.Has("PRIMER MENSAJE (OPCIONAL)")
	e.Click("Codex, en el host")
	e.Click("Lanzar")
	l := e.F.Launched()
	want := backend.LaunchSpec{HostID: "nube", Where: backend.SourceAgentd, Project: "landing", Harness: "codex"}
	if len(l) != 1 || l[0] != want {
		t.Fatalf("launched %+v, want %+v", l, want)
	}
	if !e.Eventually(time.Second, func() bool { return e.A.Router.Path() == "/agents/s/nube/agentd/codex@landing" }) {
		t.Errorf("path %q", e.A.Router.Path())
	}
}
