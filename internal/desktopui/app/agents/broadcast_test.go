package agents_test

import (
	"context"
	"sort"
	"testing"
	"time"

	"github.com/digiogithub/agentd/internal/desktopui/app/apptest"
	"github.com/digiogithub/agentd/internal/desktopui/backend"
	"github.com/digiogithub/agentd/internal/proto"
	"github.com/egoist/mygo/ui"
)

const broadcastField = "Prompt para los agentes seleccionados"

// promptActs returns the session keys that got a prompt act with text, sorted,
// and which of them were forced.
func promptActs(e *apptest.Env, text string) (keys []string, forced map[string]bool) {
	forced = map[string]bool{}
	for _, a := range e.F.Acts() {
		if a.Args.Action == proto.ActPrompt && a.Args.Text == text {
			keys = append(keys, a.Key)
			forced[a.Key] = a.Args.Force
		}
	}
	sort.Strings(keys)
	return keys, forced
}

func TestBroadcastToAllOfAHost(t *testing.T) {
	e := apptest.New(t, "/agents", 1400, 900)
	e.Click("Seleccionar varios")
	// The exited session and the zellij one cannot take a prompt.
	e.Has("Todos · 6", "estudio · 4", "vps-hetzner · 1", "nube · 1", "Ninguno", "Elige a quién enviar el prompt")
	e.Click("estudio · 4")
	e.Has("4 agentes seleccionados", "Enviar a 4")
	e.Click(broadcastField)
	e.UI.Type("Haz commit de lo que tengas")
	e.UI.Key(0, ui.KeyEnter)
	e.Settle()
	keys, forced := promptActs(e, "Haz commit de lo que tengas")
	want := []string{"estudio/agentd/claude@docs", "estudio/agentd/claude@webapp", "estudio/agentd/codex@webapp", "estudio/agentd/pando@agentd"}
	if len(keys) != len(want) {
		t.Fatalf("prompt acts %v, want %v", keys, want)
	}
	for i := range want {
		if keys[i] != want[i] {
			t.Fatalf("prompt acts %v, want %v", keys, want)
		}
	}
	// Forced only for the session that is working (the chat's rule).
	for k, f := range forced {
		if f != (k == "estudio/agentd/pando@agentd") {
			t.Errorf("force=%v for %s", f, k)
		}
	}
	if !e.Eventually(time.Second, func() bool { return e.UI.HasText("Prompt enviado a 4 agentes") }) {
		t.Errorf("no success toast; texts %q", e.UI.Texts())
	}
	// All delivered: selection mode ends and the list is back.
	if e.UI.HasText("Ninguno") || !e.UI.HasText("Nueva sesión") {
		t.Errorf("selection mode still on; texts %q", e.UI.Texts())
	}
}

func TestBroadcastPicksRowsWithoutOpeningThem(t *testing.T) {
	e := apptest.New(t, "/agents", 1400, 1300)
	e.Click("Seleccionar varios")
	e.Click("Seleccionar claude@docs")
	e.Click("Seleccionar codex@api")
	e.Has("2 agentes seleccionados", "Enviar a 2", "Elige una sesión")
	// Rows that cannot take a prompt do not toggle.
	e.Click("Seleccionar zellij · main")
	e.Click("Seleccionar claude@infra")
	e.Has("2 agentes seleccionados", "portatil · no admite prompts desde aquí")
	e.Click("Seleccionar claude@docs")
	e.Has("1 agente seleccionado", "Enviar a 1")
	// Nothing is sent without text.
	e.Click("Enviar a 1")
	if keys, _ := promptActs(e, ""); len(e.F.Acts()) != 0 {
		t.Fatalf("acts sent with an empty prompt: %v %+v", keys, e.F.Acts())
	}
	e.Click("Salir de la selección")
	e.Click("Seleccionar varios")
	e.Has("Elige a quién enviar el prompt") // leaving the mode clears the selection
}

func TestBroadcastFollowsFilterAndReadOnlyHosts(t *testing.T) {
	e := apptest.New(t, "/agents", 1400, 900)
	e.F.SetRelayScope("nube", backend.ScopeView)
	e.Settle()
	e.Click("Seleccionar varios")
	e.Has("Todos · 5", "nube · Listo · solo lectura en este dispositivo")
	e.Click("Esperan · 2")
	e.Has("Todos · 2")
	e.Click("Todos · 2")
	e.Has("2 agentes seleccionados")
}

// failFor refuses prompts to one session.
type failFor struct {
	backend.Backend
	name string
}

func (b failFor) Act(ctx context.Context, s backend.Session, args proto.ActArgs) (*proto.ActResult, error) {
	if s.Name == b.name {
		return nil, proto.Errorf(proto.CodeNotReady, "session %s is waiting on a menu", s.Name)
	}
	return b.Backend.Act(ctx, s, args)
}

func TestBroadcastKeepsRefusalsSelected(t *testing.T) {
	e := apptest.New(t, "/agents", 1400, 1300)
	e.A.Backend = failFor{Backend: e.A.Backend, name: "claude@docs"}
	e.Click("Seleccionar varios")
	e.Click("Seleccionar claude@docs")
	e.Click("Seleccionar codex@webapp")
	e.Click(broadcastField)
	e.UI.Type("Resume tu estado")
	e.Click("Enviar a 2")
	if !e.Eventually(time.Second, func() bool {
		return e.UI.HasText("Prompt enviado a 1 agente") && e.UI.HasText("No se pudo enviar a claude@docs")
	}) {
		t.Fatalf("toasts missing; texts %q", e.UI.Texts())
	}
	// The refused session stays selected, with the text, for a retry.
	e.Has("1 agente seleccionado", "Enviar a 1")
	if keys, _ := promptActs(e, "Resume tu estado"); len(keys) != 1 || keys[0] != "estudio/agentd/codex@webapp" {
		t.Errorf("prompt acts %v", keys)
	}
}
