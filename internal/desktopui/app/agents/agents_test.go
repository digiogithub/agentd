package agents_test

import (
	"testing"
	"time"

	_ "github.com/digiogithub/agentd/internal/desktopui/app/agents"
	"github.com/digiogithub/agentd/internal/desktopui/app/apptest"
	"github.com/digiogithub/agentd/internal/desktopui/backend/fake"
)

func TestInboxGroupsAndCopy(t *testing.T) {
	e := apptest.New(t, "/agents", 1400, 900)
	e.Has("Agentes", "3 hosts · 7 sesiones", "NECESITAN RESPUESTA · 2", "TRABAJANDO · 2", "LISTOS · 3",
		"claude@webapp", "Permiso", "Bash · go test ./internal/... -race", "Permitir", "Denegar",
		"codex@api", "Pregunta", "3 opciones · haz clic para responder", "pando@agentd", "zellij · main",
		"portatil · SSH · 3 panes · claude en pane 2", "activo", "claude@docs", "Elige una sesión",
		"Todos · 7", "Esperan · 2", "Trabajando · 2", "Listos · 3")
}

func TestApprovalSendsChooseWithRevision(t *testing.T) {
	e := apptest.New(t, "/agents", 1400, 900)
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
	e.Click("Denegar")
	acts := e.F.Acts()
	if len(acts) != 1 || acts[0].Args.OptionID != "w3" {
		t.Fatalf("acts %+v", acts)
	}
}

func TestFilterChips(t *testing.T) {
	e := apptest.New(t, "/agents", 1400, 900)
	e.Click("Trabajando · 2")
	e.Has("pando@agentd", "zellij · main")
	if e.UI.HasText("claude@docs") || e.UI.HasText("claude@webapp") {
		t.Errorf("filter Trabajando shows other groups: %q", e.UI.Texts())
	}
	e.Click("Listos · 3")
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
