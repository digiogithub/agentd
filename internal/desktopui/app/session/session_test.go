package session_test

import (
	"strings"
	"testing"
	"time"

	"github.com/digiogithub/agentd/internal/desktopui/app/agents"
	"github.com/digiogithub/agentd/internal/desktopui/app/apptest"
	"github.com/digiogithub/agentd/internal/desktopui/app/session"
	"github.com/digiogithub/agentd/internal/desktopui/backend/fake"
	"github.com/digiogithub/agentd/internal/proto"
	"github.com/egoist/mygo/ui"
)

func TestChatShowsTranscriptAndQuestion(t *testing.T) {
	e := apptest.New(t, "/agents/s/vps-hetzner/agentd/codex@api", 1400, 900)
	e.Has("codex@api", "vps-hetzner · ~/src/api · turno 14", "Esperando",
		"Añade expiración a los tokens de sesión y migra la base de datos.",
		"go test ./...", "4,1 s", "Edited auth/token.go", "+42 −7",
		"PREGUNTA", "¿Migro la tabla sessions ahora o dejo la migración para el despliegue?",
		"Migrar ahora con una migración reversible", "Dejarlo para el despliegue y documentarlo",
		"Parar y revisar el esquema conmigo", "Responder", "Escribir otra", "Mensaje para el agente", "Adjuntar imagen", "Dictar")
}

func TestChooseSendsSelectedOptionWithRevision(t *testing.T) {
	e := apptest.New(t, "/agents/s/vps-hetzner/agentd/codex@api", 1400, 900)
	e.Click("Migrar ahora con una migración reversible")
	e.Click("Responder")
	acts := e.F.Acts()
	if len(acts) != 1 {
		t.Fatalf("acts %+v", acts)
	}
	a := acts[0].Args
	if a.Action != "choose" || a.OptionID != "a1" || a.Revision != 57 || a.Epoch != fake.Epoch || a.Label != "Migrar ahora con una migración reversible" {
		t.Errorf("act %+v", a)
	}
	if !e.Eventually(time.Second, func() bool {
		return e.UI.HasText("● Entendido: Migrar ahora con una migración reversible") ||
			e.UI.HasText("De acuerdo. Dejo la migración para el despliegue y lo documento en MIGRATIONS.md.")
	}) {
		t.Errorf("answer not reflected: %q", e.UI.Texts())
	}
}

func TestComposerSendsPrompt(t *testing.T) {
	e := apptest.New(t, "/agents/s/estudio/agentd/claude@docs", 1400, 900)
	e.Click("Mensaje para el agente")
	e.UI.Type("Añade un índice")
	e.UI.Key(0, ui.KeyEnter)
	e.Settle()
	var got *proto.ActArgs
	for _, a := range e.F.Acts() {
		if a.Args.Action == "prompt" {
			got = &a.Args
		}
	}
	if got == nil || got.Text != "Añade un índice" || got.Name != "claude@docs" || got.Epoch != fake.Epoch || got.Revision == 0 {
		t.Fatalf("prompt act %+v (all %+v)", got, e.F.Acts())
	}
	if !e.Eventually(time.Second, func() bool { return e.UI.HasText("● Hecho: Añade un índice") }) {
		t.Errorf("reply not shown: %q", e.UI.Texts())
	}
}

func TestQueuedMailHint(t *testing.T) {
	// claude@agentd has no session in the fake; queue one for claude@docs.
	e := apptest.New(t, "/agents", 1400, 900)
	if _, err := e.F.Send(t.Context(), "estudio", proto.SendArgs{To: "claude@docs", Body: "hola"}); err != nil {
		t.Fatal(err)
	}
	e.A.Navigate("/agents/s/estudio/agentd/claude@docs")
	if !e.Eventually(2*time.Second, func() bool {
		return e.UI.HasText("1 mensaje en cola: se entrega cuando el agente quede libre")
	}) {
		t.Fatalf("queue hint missing: %q", e.UI.Texts())
	}
	e.Click("Ver mensajes en cola")
	if p := e.A.Router.Path(); p != "/mailbox/estudio" {
		t.Errorf("path %q", p)
	}
}

func TestItemsParsing(t *testing.T) {
	items := session.Items([]proto.ViewLine{
		{Text: "❯ hazlo", Role: "transcript"},
		{Text: "● Voy a ello.", Role: "transcript"},
		{Text: "● Bash(go test ./...)", Role: "transcript"},
		{Text: "  ⎿ ok  pkg 1.2s", Role: "transcript"},
		{Text: "✓ go vet ./...  2,5 s", Role: "transcript"},
		{Text: "Bash command", Role: "dialog"},
		{Text: "esperando", Role: "status"},
	}, true)
	want := []session.ItemKind{session.ItemUser, session.ItemAgent, session.ItemTool, session.ItemTool, session.ItemSystem}
	if len(items) != len(want) {
		t.Fatalf("items %+v", items)
	}
	for i, k := range want {
		if items[i].Kind != k {
			t.Errorf("item %d kind %d, want %d (%+v)", i, items[i].Kind, k, items[i])
		}
	}
	if items[2].Title != "Bash(go test ./...)" || !strings.Contains(items[2].Text, "ok  pkg 1.2s") {
		t.Errorf("tool %+v", items[2])
	}
	if items[3].Meta != "2,5 s" || items[3].OK == nil || !*items[3].OK {
		t.Errorf("tool meta %+v", items[3])
	}
}

func TestTerminalTabShowsPTYAndSendsKeys(t *testing.T) {
	if !apptest.UseGhosttyVT(t) {
		t.Skip("libghostty-vt not fetched (make desktop-natives-<goos>-<goarch>)")
	}
	e := apptest.New(t, "/agents/s/estudio/agentd/claude@webapp?tab=terminal", 1400, 900)
	v := agentsView(t, e)
	if msg := v.TerminalError(); msg != "" {
		t.Skipf("terminal unavailable: %s", msg)
	}
	e.Has("Interrumpir", "Control", "Control: tus teclas llegan al agente. El tamaño del PTY no cambia.", "160×50 · 2 clientes")
	if !e.Eventually(3*time.Second, func() bool {
		return v.Terminal() != nil && strings.Contains(v.Terminal().Text(), "Revisando la entrada del hijo")
	}) {
		t.Fatalf("screen %q", v.Terminal().Text())
	}
	v.Terminal().Send([]byte("ls\r"))
	if !e.Eventually(2*time.Second, func() bool { return strings.Contains(e.F.Typed(), "ls\r") }) {
		t.Errorf("typed %q", e.F.Typed())
	}
	e.Click("Escape")
	if !e.Eventually(time.Second, func() bool {
		for _, a := range e.F.Acts() {
			if a.Args.Action == "keys" && len(a.Args.Keys) == 1 && a.Args.Keys[0] == "esc" {
				return true
			}
		}
		return false
	}) {
		t.Errorf("esc not sent: %+v", e.F.Acts())
	}
	e.Click("Interrumpir")
	found := false
	for _, a := range e.F.Acts() {
		found = found || a.Args.Action == "interrupt"
	}
	if !found {
		t.Errorf("interrupt not sent")
	}
	// Observar: read only, the key bar is disabled.
	e.Click("Cambiar modo de control")
	e.Has("Observar: solo lectura, nadie ve tus pulsaciones.")
}

func agentsView(t *testing.T, e *apptest.Env) *session.View {
	t.Helper()
	v, _ := e.Root.AreaView("agents").(*agents.View)
	if v == nil || v.Detail() == nil {
		t.Fatalf("no session page open: %q", e.UI.Texts())
	}
	return v.Detail()
}
