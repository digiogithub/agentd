package panel_test

import (
	"testing"
	"time"

	"github.com/digiogithub/agentd/internal/desktopui/app"
	"github.com/digiogithub/agentd/internal/desktopui/app/panel"
	"github.com/digiogithub/agentd/internal/desktopui/backend"
	"github.com/digiogithub/agentd/internal/proto"
)

func TestTaskGroups(t *testing.T) {
	at := func(m int) time.Time { return time.Unix(1000+int64(m)*60, 0) }
	gs := panel.TaskGroups([]proto.Task{
		{ID: "a", Status: proto.TaskDone, UpdatedAt: at(1)},
		{ID: "b", Status: proto.TaskPending},
		{ID: "c", Status: proto.TaskDone, UpdatedAt: at(5)},
		{ID: "d", Status: proto.TaskCancelled},
		{ID: "e", Status: proto.TaskBlocked},
		{ID: "f", Status: proto.TaskInProgress},
	})
	var order []string
	for _, g := range gs {
		order = append(order, g.Status)
	}
	want := []string{proto.TaskInProgress, proto.TaskBlocked, proto.TaskPending, proto.TaskDone, proto.TaskCancelled}
	if len(order) != len(want) {
		t.Fatalf("groups %v", order)
	}
	for i := range want {
		if order[i] != want[i] {
			t.Fatalf("groups %v, want %v", order, want)
		}
	}
	if done := gs[3].Tasks; done[0].ID != "c" || done[1].ID != "a" {
		t.Errorf("done not newest first: %+v", done)
	}
	if got := panel.TaskGroups(nil); len(got) != 0 {
		t.Errorf("empty list: %+v", got)
	}
}

func TestBacklogModel(t *testing.T) {
	op := backend.Host{ID: "r1", Name: "r1", Relay: &backend.RelayTarget{Scope: backend.ScopeOperate}}
	view := backend.Host{ID: "r2", Name: "r2", Relay: &backend.RelayTarget{Scope: backend.ScopeView}}
	l := &app.Live{
		Hosts: []backend.HostStatus{{Host: op}, {Host: view}},
		Boards: []backend.HostBoard{
			{HostID: "r1", Notes: []proto.Note{{ID: 1, State: proto.NoteOpen}, {ID: 2, State: proto.NoteAssigned}, {ID: 3, State: proto.NoteDone}}},
			{HostID: "r2", NotesErr: "este dispositivo no puede ver el backlog"},
			{HostID: "r3", Notes: []proto.Note{{ID: 4, State: proto.NoteOpen}}},
		},
	}
	got := panel.Backlog(l, "")
	if len(got) != 2 || got[0].HostID != "r1" || got[1].HostID != "r3" {
		t.Fatalf("hosts %+v", got)
	}
	if len(got[0].Open) != 1 || len(got[0].Assigned) != 1 || len(got[0].Done) != 1 || !got[0].Writable {
		t.Errorf("r1 %+v", got[0])
	}
	if one := panel.Backlog(l, "r3"); len(one) != 1 || one[0].HostID != "r3" {
		t.Errorf("filter %+v", one)
	}
	if hidden := panel.Backlog(l, "r2"); len(hidden) != 0 {
		t.Errorf("a host with NotesErr must be hidden: %+v", hidden)
	}
	l.Boards[1].NotesErr = ""
	if h := panel.Backlog(l, "r2"); len(h) != 1 || h[0].Writable {
		t.Errorf("a view device must not write: %+v", h)
	}
}

func TestDetailRoute(t *testing.T) {
	e := newPanel(t)
	e.A.Navigate(panel.AgentPath("estudio", "pando@agentd"))
	e.Settle()
	e.Has("pando@agentd", "Abrir agente", "Volver", "3 / 7",
		"TE PREGUNTA", "¿Migro el esquema del buzón a v4 en este mismo cambio o lo separo en otro?",
		"EN CURSO · 2", "PENDIENTES · 2", "HECHAS · 3",
		"Op board: resumen agregado por sesión", "Pregunta abierta: ¿Migro el esquema del buzón a v4 en este mismo cambio o lo separo en otro?",
		"Modelo de tareas y validación")
	if e.UI.HasText("BLOQUEADAS") || e.UI.HasText("CANCELADAS") {
		t.Errorf("empty sections shown; texts %q", e.UI.Texts())
	}
	// The question can be answered here too.
	e.Click("Responder «En este cambio» a pando@agentd")
	if reps := e.F.Replies(); len(reps) != 1 || reps[0].Body != "En este cambio" {
		t.Fatalf("replies %+v", reps)
	}
	// Back goes to the Panel.
	e.Click("Volver")
	if p := e.A.Router.Path(); p != app.PanelPath {
		t.Errorf("path after back %q", p)
	}
}

func TestDetailBlockedCancelledAndBacklogMarker(t *testing.T) {
	e := newPanel(t)
	e.F.SetTasks("codex@webapp", []proto.Task{
		{ID: "t1", Title: "Despliegue en el VPS", Status: proto.TaskBlocked, Note: "Falta el token", UpdatedAt: time.Now()},
		{ID: "n9", Title: "Revisar métricas", Status: proto.TaskPending, Origin: proto.TaskOriginBacklog, UpdatedAt: time.Now()},
		{ID: "t3", Title: "Idea descartada", Status: proto.TaskCancelled, UpdatedAt: time.Now()},
	})
	e.A.Navigate(panel.AgentPath("estudio", "codex@webapp"))
	e.Settle()
	e.Has("BLOQUEADAS · 1", "Falta el token", "del backlog", "CANCELADAS · 1")
	if e.UI.HasText("Idea descartada") {
		t.Errorf("cancelled tasks must start folded")
	}
	e.Click("CANCELADAS · 1")
	e.Has("Idea descartada")
}

func TestDetailGoneSession(t *testing.T) {
	e := newPanel(t)
	e.A.Navigate(panel.AgentPath("estudio", "no@existe"))
	e.Settle()
	e.Has("Esta sesión ya no está en el panel")
}

func TestTableroCardOpensDetail(t *testing.T) {
	e := newPanel(t)
	e.Click("Tablero")
	e.Click("Prueba de campo del relay en el VPS · codex@webapp")
	if p := e.A.Router.Path(); p != panel.AgentPath("estudio", "codex@webapp") {
		t.Fatalf("path %q", p)
	}
	e.Has("BLOQUEADAS · 1")
}
