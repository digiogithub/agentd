package desktopui

import (
	"context"
	"testing"

	"github.com/digiogithub/agentd/internal/desktopui/app"
	"github.com/digiogithub/agentd/internal/desktopui/backend/fake"
	"github.com/egoist/mygo/ui"
)

// The sidebar reaches every area and Ctrl/Cmd+digit does too.
func TestSidebarNavigation(t *testing.T) {
	f := fake.New()
	f.Delay = 0
	a := app.NewContext(context.Background(), f, nil)
	q := &app.QueueDispatcher{}
	a.SetDispatcher(q)
	root := app.NewRoot(a)
	defer root.Close()
	tt := ui.NewTester(root.View, 1400, 900)
	settle := func() {
		for range 5 {
			q.Drain()
			tt.Frame()
		}
	}
	settle()
	for _, c := range []struct{ label, path, text string }{
		{"Hosts", "/hosts", "Añadir host"},
		{"Buzón", "/mailbox", "Enviar a agente o cola"},
		{"Ajustes", "/settings", "AVISARME CUANDO"},
		{"Agentes", "/agents", "Nueva sesión"},
	} {
		if err := tt.Click(c.label); err != nil {
			t.Fatal(err)
		}
		settle()
		if a.Router.Path() != c.path || !tt.HasText(c.text) {
			t.Errorf("%s: path %q, texts %q", c.label, a.Router.Path(), tt.Texts())
		}
	}
	tt.Key(ui.Cmd, ui.Key3)
	settle()
	if a.Router.Path() != "/mailbox" {
		t.Errorf("Ctrl+3 went to %q", a.Router.Path())
	}
	tt.Key(ui.Cmd, ui.KeyN)
	settle()
	if a.Router.Path() != "/new" {
		t.Errorf("Ctrl+N went to %q", a.Router.Path())
	}
	if !tt.HasText("3 de 4 hosts en línea") {
		t.Errorf("hosts summary missing")
	}
}

func TestRunNeedsBackend(t *testing.T) {
	if err := Run(Options{}); err == nil {
		t.Error("Run without a backend")
	}
}
