package panel_test

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/digiogithub/agentd/internal/desktopui/app"
	_ "github.com/digiogithub/agentd/internal/desktopui/app/agents"
	"github.com/digiogithub/agentd/internal/desktopui/app/apptest"
	"github.com/digiogithub/agentd/internal/desktopui/app/panel"
	"github.com/digiogithub/agentd/internal/desktopui/backend/fake"
	"github.com/digiogithub/agentd/internal/proto"
)

func newPanel(t *testing.T) *apptest.Env {
	t.Helper()
	return newPanelSize(t, 1500, 1000)
}

func newPanelSize(t *testing.T, w, h int) *apptest.Env {
	t.Helper()
	e := apptest.New(t, "/panel", w, h)
	if !e.Eventually(2*time.Second, func() bool { return e.A.Live.BoardsLoaded && e.A.Live.SessionsLoaded }) {
		t.Fatal("boards never loaded")
	}
	e.Settle()
	return e
}

func TestPanelByAgentAndBadge(t *testing.T) {
	e := newPanel(t)
	// Seed: the question of pando@agentd, the menus of claude@webapp and
	// codex@api, the blocked task of codex@webapp.
	if n := panel.AttentionCount(e.A); n != 4 {
		t.Fatalf("attention %d, want 4", n)
	}
	e.Has("Panel", "Por agente", "Tablero", "Todos los hosts", "NECESITA TU ATENCIÓN", "TRABAJO POR AGENTE",
		"Esperan tu respuesta", "Tareas en curso", "Pendientes", "Hechas hoy",
		"¿Migro el esquema del buzón a v4 en este mismo cambio o lo separo en otro?", "Bloquea la tarea «Persistir tareas en SQLite»",
		"En este cambio", "Cambio aparte", "Buzón", "Prompt",
		"Menú en el terminal · hace 1 min", "Bash · go test ./internal/... -race", "1 · Yes",
		"Prueba de campo del relay en el VPS", "Falta el token de alta del VPS",
		"Op board: resumen agregado por sesión", "3 / 7", "Después: Eventos task.changed en el bus · Herramientas MCP para informar tareas",
		"1 pregunta abierta · hace 2 min", "Todo hecho")
	if _, ok := e.UI.Find("Esperan tu respuesta: 4"); !ok {
		t.Errorf("tile count; texts %q", e.UI.Texts())
	}
	if _, ok := e.UI.Find("Tareas en curso: 4"); !ok {
		t.Errorf("in progress tile; texts %q", e.UI.Texts())
	}
	// The sidebar badge: Panel first, with the attention count.
	all := app.Areas()
	if all[0].ID != "panel" || all[0].Badge == nil || all[0].Badge(e.A) != 4 {
		t.Errorf("first area %+v", all[0])
	}
	e.Has("4")
}

func TestFallbackCardWithoutTasks(t *testing.T) {
	e := newPanel(t)
	e.Has("Sin tareas informadas", "Hecho: la cabecera usa 24 px a cada lado.",
		"Este agente no informa de su plan: se muestra la última línea de su pantalla.", "turno 4 · hace 2 h")
	// Clicking an agent opens its session.
	e.Click("claude@landing: abrir agente")
	if p := e.A.Router.Path(); p != "/agents/s/nube/agentd/claude@landing" {
		t.Errorf("path %q", p)
	}
}

func TestToggleIsPersisted(t *testing.T) {
	e := newPanel(t)
	path := filepath.Join(t.TempDir(), "settings.json")
	e.A.Prefs = app.NewPrefs(path)
	e.Click("Tablero")
	if got := app.NewPrefs(path).Get().PanelView; got != app.PanelBoard {
		t.Fatalf("saved view %q", got)
	}
	e.Has("Pendiente", "En curso", "Bloqueada", "Hecha hoy", "4 esperan tu respuesta")
	e.Click("Por agente")
	if got := app.NewPrefs(path).Get().PanelView; got != app.PanelByAgent {
		t.Fatalf("saved view %q", got)
	}
}

func TestAnswerQuestionWithOption(t *testing.T) {
	e := newPanel(t)
	e.Click("Responder «Cambio aparte» a pando@agentd")
	reps := e.F.Replies()
	if len(reps) != 1 || reps[0] != (fake.ReplyCall{HostID: "estudio", ID: fake.QuestionID, Body: "Cambio aparte", Notify: true}) {
		t.Fatalf("replies %+v", reps)
	}
	if !e.Eventually(time.Second, func() bool { return !e.UI.HasText("Cambio aparte") }) {
		t.Errorf("question still shown")
	}
	if n := panel.AttentionCount(e.A); n != 3 {
		t.Errorf("attention after the answer %d, want 3", n)
	}
}

func TestFreeTextToPromptForcesWhileWorking(t *testing.T) {
	e := newPanel(t)
	r, ok := e.UI.Find("Respuesta a pando@agentd")
	if !ok {
		t.Fatalf("answer field; texts %q", e.UI.Texts())
	}
	e.UI.ClickAt(r.X+10, r.Y+r.H/2)
	e.Settle()
	e.UI.Type("Sepáralo")
	e.Settle()
	e.Click("Prompt")
	e.Click("Enviar respuesta a pando@agentd")
	acts := e.F.Acts()
	if len(acts) != 1 {
		t.Fatalf("acts %+v", acts)
	}
	a := acts[0].Args
	if acts[0].Key != "estudio/agentd/pando@agentd" || a.Action != proto.ActPrompt || !a.Force || a.Revision != 12 || a.Epoch != fake.Epoch ||
		!strings.HasSuffix(a.Text, ": Sepáralo") || !strings.Contains(a.Text, "#160") {
		t.Errorf("act %+v on %s", a, acts[0].Key)
	}
	// The question is closed by a quiet reply with the same answer.
	reps := e.F.Replies()
	if len(reps) != 1 || reps[0].ID != fake.QuestionID || reps[0].Body != "Sepáralo" || reps[0].Notify {
		t.Errorf("replies after the prompt: %+v", reps)
	}
	if !e.Eventually(time.Second, func() bool { return panel.AttentionCount(e.A) == 3 }) {
		t.Errorf("question still open after the prompt answer: %d", panel.AttentionCount(e.A))
	}
}

func TestFreeTextToMailboxReplies(t *testing.T) {
	e := newPanel(t)
	r, _ := e.UI.Find("Respuesta a pando@agentd")
	e.UI.ClickAt(r.X+10, r.Y+r.H/2)
	e.Settle()
	e.UI.Type("Mejor aparte")
	e.Settle()
	e.Click("Enviar respuesta a pando@agentd")
	reps := e.F.Replies()
	if len(reps) != 1 || reps[0].Body != "Mejor aparte" || reps[0].ID != fake.QuestionID || !reps[0].Notify {
		t.Fatalf("replies %+v", reps)
	}
}

func TestChooseOnTerminalMenu(t *testing.T) {
	e := newPanel(t)
	if !e.Eventually(time.Second, func() bool { return e.UI.HasText("1 · Yes") }) {
		t.Fatalf("menu options not shown; texts %q", e.UI.Texts())
	}
	e.Click("Elegir «Yes» en claude@webapp")
	acts := e.F.Acts()
	if len(acts) != 1 {
		t.Fatalf("acts %+v", acts)
	}
	a := acts[0].Args
	if acts[0].Key != "estudio/agentd/claude@webapp" || a.Action != "choose" || a.OptionID != "w1" || a.Revision != 31 || a.Epoch != fake.Epoch {
		t.Errorf("act %+v on %s", a, acts[0].Key)
	}
}

func TestChooseConflictRefreshesAndToasts(t *testing.T) {
	e := newPanel(t)
	s, _ := e.F.SessionByName("codex@api")
	if !e.Eventually(time.Second, func() bool { v, _ := e.A.Views.Get(s); return v != nil }) {
		t.Fatal("view never loaded")
	}
	// A view from an older daemon start: the fake answers conflict with the
	// current view.
	stale := e.F.View("codex@api")
	stale.Epoch = "old-epoch"
	e.A.Views.Set(s, stale)
	e.Settle()
	e.Click("Elegir «Migrar ahora con una migración reversible» en codex@api")
	if !e.Eventually(time.Second, func() bool { return e.UI.HasText("El menú cambió: revisa las opciones actualizadas") }) {
		t.Errorf("no conflict toast; texts %q", e.UI.Texts())
	}
	if v, _ := e.A.Views.Get(s); v == nil || v.Epoch != fake.Epoch {
		t.Errorf("view not refreshed: %+v", v)
	}
}

func TestBlockedTaskMessage(t *testing.T) {
	e := newPanelSize(t, 1500, 1800)
	e.Click("Responder a codex@webapp sobre «Prueba de campo del relay en el VPS»")
	e.UI.Type("El token está en el gestor")
	e.Settle()
	e.Click("Enviar mensaje a codex@webapp")
	sent := e.F.Sent()
	if len(sent) != 1 || sent[0].To != "codex@webapp" || sent[0].Body != "El token está en el gestor" ||
		!strings.Contains(sent[0].Subject, "Prueba de campo") {
		t.Fatalf("sent %+v", sent)
	}
}

func TestTableroColumns(t *testing.T) {
	e := newPanel(t)
	e.Click("Tablero")
	now := time.Now()
	cols := panel.Board(panel.Agents(e.A.Live, ""), now)
	done := 0
	for _, name := range []string{"pando@agentd", "claude@webapp", "codex@api", "claude@docs", "codex@webapp"} {
		for _, tk := range e.F.Tasks(name) {
			if tk.Status == proto.TaskDone && panel.SameDay(tk.UpdatedAt, now) {
				done++
			}
		}
	}
	if len(cols) != 4 || len(cols[0].Cards) != 5 || len(cols[1].Cards) != 4 || len(cols[2].Cards) != 1 || len(cols[3].Cards) != done {
		t.Fatalf("columns %d/%d/%d/%d, want 5/4/1/%d", len(cols[0].Cards), len(cols[1].Cards), len(cols[2].Cards), len(cols[3].Cards), done)
	}
	for _, cd := range cols[3].Cards {
		if cd.Task.Title == "Índice de la documentación" {
			t.Errorf("a task done yesterday is in Hecha hoy")
		}
	}
	next := map[string]int{}
	for _, cd := range cols[0].Cards {
		if cd.Next {
			next[cd.Agent.Session.Name]++
		}
		if cd.Task.Title == "Herramientas MCP para informar tareas" && cd.Next {
			t.Errorf("second pending task of pando@agentd marked siguiente")
		}
	}
	if len(next) != 4 || next["pando@agentd"] != 1 {
		t.Errorf("siguiente marks %v", next)
	}
	e.Has("Pendiente", "En curso", "Bloqueada", "Hecha hoy", "siguiente", "Prueba de campo del relay en el VPS")
	if e.UI.HasText("Índice de la documentación") {
		t.Errorf("yesterday's task shown")
	}
	// A click opens the agent.
	e.Click("Prueba de campo del relay en el VPS · codex@webapp")
	if p := e.A.Router.Path(); p != "/agents/s/estudio/agentd/codex@webapp" {
		t.Errorf("path %q", p)
	}
}

func TestHostFilter(t *testing.T) {
	e := newPanel(t)
	if n := len(panel.Collect(e.A.Live, "").Agents); n != 6 {
		t.Errorf("agents %d, want 6 (claude@infra exited without tasks)", n)
	}
	e.Click("Filtrar por host")
	if err := e.UI.ChooseMenuItem("vps-hetzner"); err != nil {
		t.Fatalf("%v; menu %q", err, e.UI.Menu())
	}
	e.Settle()
	e.Has("codex@api", "1 agente · 1 host · actualizado ahora")
	if e.UI.HasText("pando@agentd") {
		t.Errorf("filter shows other hosts")
	}
	if n := panel.AttentionCount(e.A); n != 4 {
		t.Errorf("the badge counts every host: %d", n)
	}
}

func TestNewTaskBlockedUpdatesPanel(t *testing.T) {
	e := newPanel(t)
	ts := e.F.Tasks("codex@api")
	ts[2].Status, ts[2].Note = proto.TaskBlocked, "Sin acceso a la base de datos"
	e.F.SetTasks("codex@api", ts)
	if !e.Eventually(time.Second, func() bool { return e.UI.HasText("Sin acceso a la base de datos") }) {
		t.Fatalf("blocked task not shown")
	}
	if n := panel.AttentionCount(e.A); n != 5 {
		t.Errorf("attention %d, want 5", n)
	}
}

func TestViewOnlyDeviceIsReadOnly(t *testing.T) {
	e := newPanelSize(t, 1500, 1800)
	e.F.SetRelayScope("nube", "view")
	e.F.Ask("claude@landing", "¿Publico la landing?", "Sí", "No")
	if !e.Eventually(time.Second, func() bool { return e.UI.HasText("¿Publico la landing?") }) {
		t.Fatalf("question not shown")
	}
	e.Has("Solo lectura en este dispositivo")
	if _, ok := e.UI.Find("Responder «Sí» a claude@landing"); ok {
		t.Errorf("a view-only device offers answers")
	}
}
