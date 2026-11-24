package panel_test

import (
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/digiogithub/agentd/internal/desktopui/app/panel"
	"github.com/digiogithub/agentd/internal/proto"
)

func openTitles(e interface{ Notes(string) []proto.Note }, host string) []string {
	var out []string
	for _, n := range e.Notes(host) {
		out = append(out, n.Title)
	}
	return out
}

func TestBacklogSection(t *testing.T) {
	e := newPanelSize(t, 1500, 2200)
	e.Has("BACKLOG · 3 ABIERTAS", "Añadir nota",
		"Revisar las licencias de las dependencias", "Documentar el despliegue del relay",
		"Rotar el token de alta", // another host: its label is shown
		"→ claude@webapp · pendiente", "Ver hechas · 1")
	if e.UI.HasText("Añadir un ejemplo de cola de trabajo") {
		t.Errorf("done notes must be folded")
	}
	e.Click("Ver hechas · 1")
	e.Has("Añadir un ejemplo de cola de trabajo", "→ claude@docs · hecha", "Ocultar hechas")
}

func TestBacklogAddEditDelete(t *testing.T) {
	e := newPanelSize(t, 1500, 2200)
	e.Click("Añadir nota al backlog")
	e.Has("Nueva nota", "Título", "Detalle (opcional)")
	// With several hosts the dialog asks where; estudio is the first one.
	e.Fill("Título", "Probar el relay en frío")
	e.Click("Guardar nota")
	if !e.Eventually(time.Second, func() bool { return e.UI.HasText("Probar el relay en frío") }) {
		t.Fatalf("new note not shown; notes %v", openTitles(e.F, "estudio"))
	}
	var added proto.Note
	for _, n := range e.F.Notes("estudio") {
		if n.Title == "Probar el relay en frío" {
			added = n
		}
	}
	if added.ID == 0 || added.State != proto.NoteOpen {
		t.Fatalf("note not stored: %+v", e.F.Notes("estudio"))
	}
	// Edit the title.
	e.Click("Editar «Probar el relay en frío»")
	e.Has("Editar nota", "Guardar")
	r, ok := e.UI.Find("Título")
	if !ok {
		t.Fatal("no Título field")
	}
	e.UI.ClickAt(r.X+440, r.Y+r.H+6+22) // the end of the title
	e.Settle()
	e.UI.Type(" v2")
	e.Settle()
	e.Click("Guardar nota")
	if !e.Eventually(time.Second, func() bool { return e.UI.HasText("Probar el relay en frío v2") }) {
		t.Fatalf("edit not shown; notes %v", openTitles(e.F, "estudio"))
	}
	// Delete behind a confirmation.
	e.Click("Eliminar «Probar el relay en frío v2»")
	e.Has("Eliminar la nota?")
	e.Click("Confirmar eliminar nota")
	if !e.Eventually(time.Second, func() bool { return !e.UI.HasText("Probar el relay en frío v2") }) {
		t.Fatalf("note still shown")
	}
	for _, n := range e.F.Notes("estudio") {
		if strings.HasPrefix(n.Title, "Probar") {
			t.Errorf("note not removed: %+v", n)
		}
	}
}

func TestBacklogAssignAndReopen(t *testing.T) {
	e := newPanelSize(t, 1500, 2200)
	e.Click("Asignar «Ordenar la configuración de ejemplo»")
	e.Has("Asignar a un agente", "Pendiente", "En curso")
	e.Click("Elige un agente")
	if err := e.UI.ChooseMenuItem("pando@agentd"); err != nil {
		t.Fatalf("menu %q: %v", e.UI.Menu(), err)
	}
	e.Settle()
	// "En curso" is also a card label: click the segment next to Pendiente.
	r, ok := e.UI.Find("Pendiente")
	if !ok {
		t.Fatalf("no Pendiente segment; texts %q", e.UI.Texts())
	}
	e.UI.ClickAt(r.X+r.W+60, r.Y+r.H/2)
	e.Settle()
	e.Click("Confirmar asignar nota")
	calls := e.F.NoteAssigns()
	if len(calls) != 1 || calls[0].HostID != "estudio" || calls[0].Args.Session != "pando@agentd" || calls[0].Args.Status != proto.TaskInProgress {
		t.Fatalf("assign calls %+v", calls)
	}
	var found bool
	for _, tk := range e.F.Tasks("pando@agentd") {
		if tk.ID == "n"+itoa(calls[0].Args.ID) && tk.Origin == proto.TaskOriginBacklog && tk.Status == proto.TaskInProgress {
			found = true
		}
	}
	if !found {
		t.Fatalf("task not created: %+v", e.F.Tasks("pando@agentd"))
	}
	if !e.Eventually(time.Second, func() bool { return e.UI.HasText("→ pando@agentd · en curso") }) {
		t.Fatalf("assigned note not shown; texts %q", e.UI.Texts())
	}
	// The task shows in the detail marked as coming from the backlog.
	e.A.Navigate(panel.AgentPath("estudio", "pando@agentd"))
	e.Settle()
	e.Has("del backlog")
	e.Click("Volver")
	// Back to the backlog.
	e.Click("Devolver al backlog «Ordenar la configuración de ejemplo»")
	if !e.Eventually(time.Second, func() bool {
		for _, n := range e.F.Notes("estudio") {
			if n.Title == "Ordenar la configuración de ejemplo" {
				return n.State == proto.NoteOpen && n.AssignedTo == ""
			}
		}
		return false
	}) {
		t.Fatalf("note not reopened: %+v", e.F.Notes("estudio"))
	}
}

func itoa(n int64) string { return strconv.FormatInt(n, 10) }

func TestBacklogHiddenWhenHostCannotServeNotes(t *testing.T) {
	e := newPanel(t)
	boards := e.A.Live.Boards
	for i := range boards {
		boards[i].Notes, boards[i].NotesErr = nil, "el daemon no soporta el backlog"
	}
	e.Settle()
	if e.UI.HasText("BACKLOG") || e.UI.HasText("Añadir nota") {
		t.Errorf("backlog shown for hosts without notes; texts %q", e.UI.Texts())
	}
}
