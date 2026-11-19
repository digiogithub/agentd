package agents_test

import (
	"testing"
	"time"

	"github.com/digiogithub/agentd/internal/desktopui/app"
	_ "github.com/digiogithub/agentd/internal/desktopui/app/agents"
	"github.com/digiogithub/agentd/internal/desktopui/app/apptest"
	_ "github.com/digiogithub/agentd/internal/desktopui/app/newsession"
	"github.com/digiogithub/agentd/internal/desktopui/backend/fake"
)

// stateMode switches the list to the Estado grouping (the default is
// Proyecto).
func stateMode(e *apptest.Env) {
	e.T.Helper()
	e.Click("Estado")
}

func TestInboxGroupsAndCopy(t *testing.T) {
	e := apptest.New(t, "/agents", 1400, 900)
	stateMode(e)
	e.Has("Agentes", "4 hosts · 8 sesiones", "NECESITAN RESPUESTA · 2", "TRABAJANDO · 2", "LISTOS · 4",
		"claude@webapp", "Permiso", "Bash · go test ./internal/... -race", "Permitir", "Denegar",
		"codex@api", "Pregunta", "3 opciones · haz clic para responder", "pando@agentd", "zellij · main",
		"portatil · SSH · 3 panes · claude en pane 2", "activo", "claude@docs", "Elige una sesión",
		"Todos · 8", "Esperan · 2", "Trabajando · 2", "Listos · 4")
}

func TestApprovalSendsChooseWithRevision(t *testing.T) {
	e := apptest.New(t, "/agents", 1400, 900)
	e.Click("Esperan · 2") // the list is alphabetical: filter to the waiting ones
	e.Click("Permitir")
	acts := e.F.Acts()
	if len(acts) != 1 {
		t.Fatalf("acts %+v", acts)
	}
	a := acts[0].Args
	if acts[0].Key != "estudio/agentd/claude@webapp" || a.Action != "choose" || a.OptionID != "w1" || a.Revision != 31 || a.Epoch != fake.Epoch {
		t.Errorf("act %+v on %s", a, acts[0].Key)
	}
	// The dialog is answered: the card leaves "Necesitan respuesta".
	if !e.Eventually(time.Second, func() bool { return !e.UI.HasText("Permiso") }) {
		t.Errorf("approval card still shown: %q", e.UI.Texts())
	}
}

func TestDenyPicksTheNoOption(t *testing.T) {
	e := apptest.New(t, "/agents", 1400, 900)
	e.Click("Esperan · 2")
	e.Click("Denegar")
	acts := e.F.Acts()
	if len(acts) != 1 || acts[0].Args.OptionID != "w3" {
		t.Fatalf("acts %+v", acts)
	}
}

func TestFilterChips(t *testing.T) {
	e := apptest.New(t, "/agents", 1400, 900)
	stateMode(e)
	e.Click("Trabajando · 2")
	e.Has("pando@agentd", "zellij · main")
	if e.UI.HasText("claude@docs") || e.UI.HasText("claude@webapp") {
		t.Errorf("filter Trabajando shows other groups: %q", e.UI.Texts())
	}
	e.Click("Listos · 4")
	e.Has("claude@docs", "codex@webapp", "claude@infra")
	if e.UI.HasText("pando@agentd") {
		t.Errorf("filter Listos shows working sessions")
	}
	e.Click("Esperan · 2")
	e.Has("claude@webapp", "codex@api")
}

func TestSearch(t *testing.T) {
	e := apptest.New(t, "/agents", 1400, 900)
	e.Click("Buscar")
	e.UI.Type("docs")
	e.Settle()
	e.Has("claude@docs")
	if e.UI.HasText("pando@agentd") {
		t.Errorf("search did not filter: %q", e.UI.Texts())
	}
}

func TestOpenSessionBesideListOnWideWindows(t *testing.T) {
	e := apptest.New(t, "/agents", 1400, 900)
	stateMode(e)
	e.Click("pando@agentd: Trabajando")
	if p := e.A.Router.Path(); p != "/agents/s/estudio/agentd/pando@agentd" {
		t.Fatalf("path %q", p)
	}
	e.Has("Chat", "Terminal", "NECESITAN RESPUESTA · 2", "Revisa cómo el listener hc gestiona el core.lock")
}

func TestNarrowWindowShowsSessionAlone(t *testing.T) {
	e := apptest.New(t, "/agents/s/estudio/agentd/claude@docs", 900, 800)
	e.Has("claude@docs", "Chat")
	if e.UI.HasText("NECESITAN RESPUESTA · 2") {
		t.Errorf("list shown on a narrow window")
	}
	e.Click("Volver")
	if p := e.A.Router.Path(); p != "/agents" {
		t.Errorf("back went to %q", p)
	}
}

func y(t *testing.T, e *apptest.Env, s string) float32 {
	t.Helper()
	r, ok := e.UI.Find(s)
	if !ok {
		t.Fatalf("%q not found; texts %q", s, e.UI.Texts())
	}
	return r.Y
}

func TestProjectGroupsAreTheDefault(t *testing.T) {
	e := apptest.New(t, "/agents", 1400, 900)
	e.Has("Proyecto", "Estado",
		"webapp", "estudio · 2 sesiones", "api", "vps-hetzner · 1 sesión", "agentd", "docs", "infra",
		"Nueva sesión en webapp", "claude@webapp", "codex@webapp", "claude@docs")
	if e.UI.HasText("NECESITAN RESPUESTA · 2") {
		t.Errorf("state sections shown in project mode")
	}
	// The order is alphabetical and never follows live status, so the list
	// does not jump on updates: groups by name, sessions by title.
	names := []string{"Nueva sesión en agentd", "Nueva sesión en api", "Nueva sesión en docs", "Nueva sesión en infra"}
	for i := 1; i < len(names); i++ {
		if !(y(t, e, names[i-1]) < y(t, e, names[i])) {
			t.Errorf("%q should be above %q", names[i-1], names[i])
		}
	}
	if !(y(t, e, "claude@webapp") < y(t, e, "codex@webapp: Listo")) {
		t.Errorf("sessions of a group not alphabetical")
	}
	// The same project on two hosts is two groups, named by host.
	if y(t, e, "estudio · 2 sesiones") == y(t, e, "portatil · 1 sesión") {
		t.Errorf("hosts merged")
	}
}

func TestGroupToggleIsPersisted(t *testing.T) {
	e := apptest.New(t, "/agents", 1400, 900)
	if g := e.A.Prefs.Get().AgentsGroup; g != app.GroupProject {
		t.Fatalf("default group %q", g)
	}
	stateMode(e)
	e.Has("NECESITAN RESPUESTA · 2", "TRABAJANDO · 2", "LISTOS · 4")
	if g := e.A.Prefs.Get().AgentsGroup; g != app.GroupState {
		t.Errorf("group %q after Estado", g)
	}
	e.Click("Proyecto")
	if g := e.A.Prefs.Get().AgentsGroup; g != app.GroupProject || e.UI.HasText("NECESITAN RESPUESTA · 2") {
		t.Errorf("group %q after Proyecto", g)
	}
}

func TestGroupPlusOpensNewSessionPrefilled(t *testing.T) {
	e := apptest.New(t, "/agents", 1400, 900)
	e.Click("Nueva sesión en api")
	if p := e.A.Router.Location(); p != "/new/vps-hetzner?project=api" {
		t.Fatalf("location %q", p)
	}
	e.Has("Nueva sesión", "Host: vps-hetzner", "Se creará", "claude@api")
}

func TestTerminateFromTheList(t *testing.T) {
	e := apptest.New(t, "/agents", 1400, 1600)
	e.Click("Terminar claude@docs")
	e.Has("Forzar cierre y borrar claude@docs?", "Cancelar")
	e.Click("Cancelar")
	if len(e.F.Terminated()) != 0 {
		t.Fatalf("terminated after Cancelar: %v", e.F.Terminated())
	}
	e.Click("Terminar claude@docs")
	e.Click("Confirmar terminar sesión")
	if got := e.F.Terminated(); len(got) != 1 || got[0] != "estudio/agentd/claude@docs" {
		t.Fatalf("terminated %v", got)
	}
	if !e.Eventually(time.Second, func() bool { return !e.UI.HasText("Terminar claude@docs") }) {
		t.Errorf("session still listed: %q", e.UI.Texts())
	}
}

func TestTerminateFromTheSessionHeaderGoesBack(t *testing.T) {
	e := apptest.New(t, "/agents/s/estudio/agentd/claude@docs", 900, 800)
	e.Click("Terminar sesión")
	e.Has("Forzar cierre y borrar claude@docs?", "El proceso se detiene a la fuerza")
	e.Click("Confirmar terminar sesión")
	if got := e.F.Terminated(); len(got) != 1 || got[0] != "estudio/agentd/claude@docs" {
		t.Fatalf("terminated %v", got)
	}
	if !e.Eventually(time.Second, func() bool { return e.A.Router.Path() == "/agents" }) {
		t.Errorf("path %q", e.A.Router.Path())
	}
}
