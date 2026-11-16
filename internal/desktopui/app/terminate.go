package app

import (
	"context"

	"github.com/digiogithub/agentd/internal/desktopui/backend"
	"github.com/digiogithub/agentd/internal/desktopui/kit"
	"github.com/egoist/mygo/ui"
)

// Terminator is the "Terminar" flow shared by the session header and the
// agents list: a confirmation dialog, then Backend.Terminate, then back to
// the agents list when the terminated session was the open one. UI thread
// only; call Build every frame.
type Terminator struct {
	a    *Context
	s    backend.Session
	open bool
	busy bool
}

// NewTerminator returns the flow.
func NewTerminator(a *Context) *Terminator { return &Terminator{a: a} }

// Ask opens the confirmation for s.
func (t *Terminator) Ask(s backend.Session) {
	if t.busy {
		return
	}
	t.s, t.open = s, true
}

// Asking reports whether the dialog is open (tests).
func (t *Terminator) Asking() bool { return t.open }

// Build draws the dialog (nothing while closed).
func (t *Terminator) Build(c *ui.Context) {
	if !t.open {
		return
	}
	open := t.open
	kit.Dialog(c.Key("terminate-dialog"), &open, kit.DialogOpts{
		Title:       "Forzar cierre y borrar " + t.s.Name + "?",
		Description: "El proceso se detiene a la fuerza y la sesión y su pantalla se borran. No se puede deshacer.",
		Width:       460,
		Footer: func() {
			if kit.Button(c.Key("terminate-cancel"), kit.ButtonOpts{Variant: kit.Outline, Label: "Cancelar", Height: 36}).Clicked() {
				open = false
			}
			label := "Terminar"
			if t.busy {
				label = "Terminando…"
			}
			if kit.Button(c.Key("terminate-confirm"), kit.ButtonOpts{Variant: kit.Danger, Label: label, Aria: "Confirmar terminar sesión", Height: 36, Disabled: t.busy}).Clicked() && !t.busy {
				t.run()
			}
		},
	})
	if !open && !t.busy {
		t.open = false
	}
}

func (t *Terminator) run() {
	a, s := t.a, t.s
	t.busy = true
	a.Go(func(ctx context.Context) {
		err := a.Backend.Terminate(ctx, s)
		a.Update(func() {
			t.busy, t.open = false, false
			if err != nil {
				a.Toasts.Error("No se pudo terminar la sesión", ErrorText(err))
				return
			}
			a.Toasts.Info("Sesión terminada: " + s.Name)
			if a.Router.Path() == SessionPath(s) {
				a.Router.Replace("/agents")
			}
		})
	})
}
