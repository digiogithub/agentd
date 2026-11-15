package app

import (
	"context"
	"errors"

	"github.com/digiogithub/agentd/internal/desktopui/backend"
	"github.com/digiogithub/agentd/internal/desktopui/kit"
	"github.com/egoist/mygo/ui"
)

// ConnectFlow connects to a host from a page and asks the user what the
// background SSH layer cannot decide alone: an unknown host key (fingerprint
// dialog: Aceptar = TrustHostKey, then connect again) and a password
// (password dialog = ConnectWithPassword, never stored). A changed host key
// is a hard error (toast). State is UI thread only; call Dialogs from Build.
type ConnectFlow struct {
	a *Context
	// OnConnected runs on the UI thread once the host is connected.
	OnConnected func(h backend.Host)
	// InstallKey asks ConnectWithPassword to install the app key and switch
	// the host to it (default true in NewConnectFlow).
	InstallKey bool

	host     backend.Host
	busy     bool
	password string // set while a password connect retries after a host key
	fp, kind string // pending host key
	askPw    bool
	pwInput  string
}

// NewConnectFlow returns a flow for page state.
func NewConnectFlow(a *Context, onConnected func(h backend.Host)) *ConnectFlow {
	return &ConnectFlow{a: a, OnConnected: onConnected, InstallKey: true}
}

// Busy reports whether a connection attempt runs.
func (f *ConnectFlow) Busy() bool { return f.busy }

// Connect connects to h (already saved). password != "" logs in with it
// once (ConnectWithPassword).
func (f *ConnectFlow) Connect(h backend.Host, password string) {
	if f.busy || f.a.Backend == nil {
		return
	}
	f.host, f.busy, f.password = h, true, password
	a := f.a
	install := f.InstallKey
	a.Go(func(ctx context.Context) {
		var err error
		if password != "" {
			err = a.Backend.ConnectWithPassword(ctx, h.ID, password, install)
		} else {
			err = a.Backend.Connect(ctx, h.ID)
		}
		a.Update(func() { f.result(err) })
	})
}

func (f *ConnectFlow) result(err error) {
	f.busy = false
	var hk *backend.HostKeyError
	switch {
	case err == nil:
		f.password = ""
		if f.OnConnected != nil {
			f.OnConnected(f.host)
		}
	case errors.As(err, &hk) && !errors.Is(err, backend.ErrHostKeyChanged):
		f.fp, f.kind = hk.Fingerprint, hk.KeyType
	case errors.Is(err, backend.ErrPasswordRequired):
		f.password = ""
		f.askPw, f.pwInput = true, ""
	default:
		f.password = ""
		f.a.Toasts.Error("No se pudo conectar con "+hostLabel(f.host), ErrorText(err))
	}
}

func hostLabel(h backend.Host) string {
	if h.Name != "" {
		return h.Name
	}
	return h.Addr
}

// Dialogs shows the pending host key or password dialog.
func (f *ConnectFlow) Dialogs(c *ui.Context) {
	f.fingerprintDialog(c)
	f.passwordDialog(c)
}

func (f *ConnectFlow) fingerprintDialog(c *ui.Context) {
	if f.fp == "" {
		return
	}
	open := true
	h := f.host
	fp, kind := f.fp, f.kind
	kit.Dialog(c.Key("connect-fp"), &open, kit.DialogOpts{Title: "Confirma la huella del host", Width: 480,
		Description: "Es la primera vez que conectas con " + h.Addr + ". Compara la huella con la del host (ssh-keygen -lf /etc/ssh/ssh_host_ed25519_key.pub) antes de confiar.",
		Body: func() {
			if kind != "" {
				ui.Text(c, kind).FontSize(12).TextColor(kit.Current().Muted)
			}
			kit.CodeBox(c, fp)
		},
		Footer: func() {
			if kit.Button(c.Key("fp-cancel"), kit.ButtonOpts{Variant: kit.Outline, Label: "Cancelar", Height: 36}).Clicked() {
				open = false
				f.password = ""
			}
			if kit.Button(c.Key("fp-ok"), kit.ButtonOpts{Variant: kit.Primary, Label: "Aceptar", Height: 36}).Clicked() {
				open = false
				f.trust(h, fp)
			}
		}})
	if !open {
		f.fp, f.kind = "", ""
	}
}

func (f *ConnectFlow) trust(h backend.Host, fp string) {
	a := f.a
	password := f.password
	f.busy = true
	a.Go(func(ctx context.Context) {
		err := a.Backend.TrustHostKey(ctx, h.ID, fp)
		a.Update(func() {
			f.busy = false
			if err != nil {
				f.password = ""
				a.Toasts.Error("No se pudo aceptar la huella", ErrorText(err))
				return
			}
			f.Connect(h, password)
		})
	})
}

func (f *ConnectFlow) passwordDialog(c *ui.Context) {
	if !f.askPw {
		return
	}
	open := true
	h := f.host
	desc := "La contraseña se usa una vez y no se guarda."
	if f.InstallKey {
		desc = "Se conecta una vez con la contraseña, añade la clave de la app a ~/.ssh/authorized_keys y a partir de ahí usa la clave. La contraseña no se guarda."
	}
	submit := false
	kit.Dialog(c.Key("connect-pw"), &open, kit.DialogOpts{Title: "Contraseña de " + hostLabel(h), Width: 440,
		Description: desc,
		Body: func() {
			kit.Field(c, "Contraseña", func() {
				if kit.Input(c.Key("connect-pw-in"), &f.pwInput, "Contraseña de "+h.User, false).Password().Label("Contraseña").Submitted() {
					submit = true
				}
			})
		},
		Footer: func() {
			if kit.Button(c.Key("pw-cancel"), kit.ButtonOpts{Variant: kit.Outline, Label: "Cancelar", Height: 36}).Clicked() {
				open = false
			}
			if kit.Button(c.Key("pw-ok"), kit.ButtonOpts{Variant: kit.Primary, Label: "Conectar", Height: 36, Disabled: f.pwInput == ""}).Clicked() {
				submit = true
			}
		}})
	if submit && f.pwInput != "" {
		pw := f.pwInput
		open = false
		f.askPw, f.pwInput = false, ""
		f.Connect(h, pw)
		return
	}
	if !open {
		f.askPw, f.pwInput = false, ""
	}
}
