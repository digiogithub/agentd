package app_test

import (
	"testing"

	"github.com/digiogithub/agentd/internal/desktopui/app"
	_ "github.com/digiogithub/agentd/internal/desktopui/app/agents"
	"github.com/digiogithub/agentd/internal/desktopui/app/apptest"
)

// hostShell is a Shell that can also drive a frameless window.
type hostShell struct {
	min, max, closed int
	maximized        bool
}

func (*hostShell) Notify(string, string, string, func()) {}
func (*hostShell) Focused() bool                         { return true }
func (h *hostShell) Minimize()                           { h.min++ }
func (h *hostShell) ToggleMaximize()                     { h.max++; h.maximized = !h.maximized }
func (h *hostShell) CloseWindow()                        { h.closed++ }
func (h *hostShell) IsMaximized() bool                   { return h.maximized }

func TestTitleBar(t *testing.T) {
	old := app.CustomFrame
	t.Cleanup(func() { app.CustomFrame = old })

	// A custom frame with a window host draws the controls and forwards them.
	app.CustomFrame = func() bool { return true }
	e := apptest.New(t, "/agents", 1100, 700)
	h := &hostShell{}
	e.A.SetShell(h)
	e.Settle()
	e.Click("Minimizar")
	e.Click("Maximizar")
	e.Settle()
	e.Click("Restaurar") // the icon and label follow the maximised state
	e.Click("Cerrar")
	if h.min != 1 || h.max != 2 || h.closed != 1 {
		t.Fatalf("min=%d max=%d closed=%d, want 1 2 1", h.min, h.max, h.closed)
	}

	// Native frame (macOS): no title bar of ours.
	app.CustomFrame = func() bool { return false }
	e2 := apptest.New(t, "/agents", 1100, 700)
	e2.A.SetShell(&hostShell{})
	e2.Settle()
	if e2.UI.HasText("Minimizar") {
		t.Fatal("native frame must not draw window controls")
	}
}
