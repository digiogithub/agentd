// Package addhost is the Añadir host page (mockup 6): a relay invitation
// (paste the `agentd relay invite` link, the host owner approves the device,
// then the host is reached through agentd-relay with the device's scope),
// import from ~/.ssh/config, the
// manual form (name, host, port, user, authentication), the app's public
// key with a copy button, and the host-key fingerprint confirmation (trust
// on first use, app.ConnectFlow) before Preparar host opens. With password
// authentication the password is used once to install the app key.
package addhost

import (
	"context"
	"regexp"
	"strconv"
	"strings"

	"github.com/digiogithub/agentd/internal/desktopui/app"
	"github.com/digiogithub/agentd/internal/desktopui/backend"
	"github.com/digiogithub/agentd/internal/desktopui/kit"
	"github.com/egoist/mygo/ui"
)

func init() {
	app.Register(app.Area{
		ID: "addhost", Path: "/hosts/add", Title: "Añadir host", Icon: kit.IconPlus, Section: app.SectionHidden, Parent: "hosts",
		New: func(a *app.Context) app.View { return New(a) },
	})
}

// AuthOption is an entry of the authentication radio.
type AuthOption struct {
	Kind       backend.AuthKind
	Label, Sub string
}

// AuthOptions of the form.
var AuthOptions = []AuthOption{
	{backend.AuthGenerated, "Clave generada en este equipo", "ed25519 propia de la app; copia la pública con un clic"},
	{backend.AuthAgent, "Agente SSH", "Usa las claves cargadas en tu agente SSH"},
	{backend.AuthKeyFile, "Clave existente", "Un fichero de clave privada, como ~/.ssh/id_ed25519"},
	{backend.AuthPassword, "Contraseña", "Solo para instalar la clave; no se guarda"},
}

// View is the page state (UI thread only).
type View struct {
	a *app.Context

	name, addr, port, user string
	auth                   backend.AuthKind
	keyFile, password      string
	pubKey                 string
	pubKeyLoading          bool
	busy                   bool

	// fingerprint and password dialogs
	flow *app.ConnectFlow

	// relay invitation
	relayOpen   bool
	relayLink   string
	relayName   string
	relayCancel context.CancelFunc // set while pairing

	// import
	importOpen bool
	imported   []backend.Host
	importErr  string
	importing  bool
}

// New returns the page.
func New(a *app.Context) *View {
	v := &View{a: a, port: "22", auth: backend.AuthGenerated}
	v.flow = app.NewConnectFlow(a, v.done)
	return v
}

var slugRe = regexp.MustCompile(`[^a-z0-9._-]+`)

// Slug is the host ID made from its name.
func Slug(s string) string {
	return strings.Trim(slugRe.ReplaceAllString(strings.ToLower(strings.TrimSpace(s)), "-"), "-")
}

func (v *View) loadKey() {
	if v.pubKey != "" || v.pubKeyLoading || v.a.Backend == nil {
		return
	}
	v.pubKeyLoading = true
	a := v.a
	a.Go(func(ctx context.Context) {
		k, err := a.Backend.PublicKey(ctx)
		a.Update(func() {
			v.pubKeyLoading = false
			if err != nil {
				v.pubKey = "No se pudo generar la clave: " + err.Error()
				return
			}
			v.pubKey = k
		})
	})
}

// Build shows the form.
func (v *View) Build(c *ui.Context, r *ui.Route) {
	t := kit.Current()
	if v.auth == backend.AuthGenerated {
		v.loadKey()
	}
	ui.Column(c).Grow(1).MinHeight(0).AlignItems(ui.Stretch).Children(func() {
		if kit.RouteHeader(c, "Añadir host", "", nil) {
			v.a.Navigate("/hosts")
		}
		ui.Scroll(c.Key("form")).Grow(1).MinHeight(0).Padding(16, 24, 24, 24).Children(func() {
			ui.Column(c).Gap(12).MaxWidth(640).FillWidth().Children(func() {
				ui.Row(c).Gap(10).Children(func() {
					if v.quick(c.Key("pair"), t, kit.IconPaste, "Relay (invitación)", "Pega el enlace de agentd relay invite") {
						v.relayOpen = true
					}
					if v.quick(c.Key("import"), t, kit.IconImport, "Importar", "~/.ssh/config o Tailscale") {
						v.startImport()
					}
				})
				kit.SectionLabel(c, "O a mano", t.Muted).Margin(8, 0, 0, 0)
				kit.Field(c, "Nombre", func() { kit.Input(c.Key("name"), &v.name, "portatil", false).Label("Nombre") })
				ui.Row(c).Gap(10).Children(func() {
					ui.Column(c).Grow(1).Children(func() {
						kit.Field(c, "Host", func() { kit.Input(c.Key("addr"), &v.addr, "192.168.1.40", true).Label("Host") })
					})
					ui.Column(c).Width(110).Children(func() {
						kit.Field(c, "Puerto", func() { kit.Input(c.Key("port"), &v.port, "22", true).Label("Puerto") })
					})
				})
				kit.Field(c, "Usuario", func() { kit.Input(c.Key("user"), &v.user, "jose", false).Label("Usuario") })
				ui.Text(c, "Autenticación").FontSize(13).TextColor(t.Secondary).Margin(4, 0, 0, 0)
				for _, o := range AuthOptions {
					if kit.SelectTile(c.Key("auth:"+string(o.Kind)), o.Label+", "+o.Sub, v.auth == o.Kind, true, func() {
						ui.Text(c, o.Label).FontSize(14).FontWeight(500)
						ui.Text(c, o.Sub).FontSize(12).TextColor(t.Muted)
					}).Clicked() {
						v.auth = o.Kind
					}
				}
				switch v.auth {
				case backend.AuthGenerated:
					v.publicKeyBox(c, t)
				case backend.AuthKeyFile:
					kit.Field(c, "Fichero de clave", func() {
						kit.Input(c.Key("keyfile"), &v.keyFile, "~/.ssh/id_ed25519", true).Label("Fichero de clave")
					})
				case backend.AuthPassword:
					kit.Field(c, "Contraseña del host", func() {
						kit.Input(c.Key("password"), &v.password, "Solo para instalar la clave", false).Password().Label("Contraseña del host")
					})
				}
				ui.Text(c, "Al conectar verás la huella del host para confirmarla. Luego la app detecta agentd, zellij, tmux y los CLIs de agentes.").
					FontSize(12).TextColor(t.Muted).LineHeight(1.4)
				label := "Conectar y detectar"
				busy := v.busy || v.flow.Busy()
				if busy {
					label = "Conectando…"
				}
				if kit.Button(c.Key("connect"), kit.ButtonOpts{Variant: kit.Primary, Label: label, Height: 52, FontSize: 15, Disabled: busy}).Clicked() && !busy {
					v.submit()
				}
			})
		})
	})
	v.flow.Dialogs(c)
	v.importDialog(c, t)
	v.relayDialog(c, t)
}

// Pairing reports whether a relay pairing waits for the host (tests).
func (v *View) Pairing() bool { return v.relayCancel != nil }

// relayDialog pastes an invite link and pairs: the call blocks until the
// host owner approves; Cancelar abandons it.
func (v *View) relayDialog(c *ui.Context, t *kit.Tokens) {
	if !v.relayOpen {
		return
	}
	open := true
	pairing := v.relayCancel != nil
	kit.Dialog(c.Key("relay"), &open, kit.DialogOpts{Title: "Añadir host por relay", Width: 520,
		Description: "En el host ejecuta agentd relay invite y pega aquí el enlace agentd://relay?… Sin SSH: solo sesiones agentd, con el permiso que conceda la invitación.",
		Body: func() {
			if pairing {
				ui.Column(c).Gap(8).Padding(12).Radius(12).Background(t.Bg).Border(1, t.Divider).Children(func() {
					ui.Text(c, "Aprueba el dispositivo en el host").FontSize(14).FontWeight(600)
					ui.Text(c, "agentd relay invite pregunta allí si acepta «"+v.deviceName()+"». Esperando respuesta…").
						FontSize(12.5).TextColor(t.Muted).LineHeight(1.4)
				})
				return
			}
			kit.Field(c, "Enlace de invitación", func() {
				kit.Input(c.Key("relay-link"), &v.relayLink, "agentd://relay?v=1&…", true).Label("Enlace de invitación").AutoFocus()
			})
			kit.Field(c, "Nombre de este dispositivo", func() {
				kit.Input(c.Key("relay-name"), &v.relayName, v.a.DeviceName(), false).Label("Nombre de este dispositivo")
			})
			ui.Text(c, "El enlace contiene un código de un solo uso: no lo compartas.").FontSize(12).TextColor(t.Muted)
		},
		Footer: func() {
			if kit.Button(c.Key("relay-cancel"), kit.ButtonOpts{Variant: kit.Outline, Label: "Cancelar", Height: 36}).Clicked() {
				open = false
			}
			if !pairing && kit.Button(c.Key("relay-pair"), kit.ButtonOpts{Variant: kit.Primary, Label: "Emparejar", Height: 36,
				Disabled: strings.TrimSpace(v.relayLink) == ""}).Clicked() && strings.TrimSpace(v.relayLink) != "" {
				v.pair()
			}
		}})
	if !open {
		if v.relayCancel != nil {
			v.relayCancel()
			v.relayCancel = nil
		}
		v.relayOpen = false
	}
}

func (v *View) deviceName() string {
	if n := strings.TrimSpace(v.relayName); n != "" {
		return n
	}
	return v.a.DeviceName()
}

// pair runs Backend.PairRelay; the host appears in the list once approved.
func (v *View) pair() {
	a := v.a
	link, name := strings.TrimSpace(v.relayLink), strings.TrimSpace(v.relayName)
	ctx, cancel := context.WithCancel(a.Ctx)
	v.relayCancel = cancel
	a.Go(func(context.Context) {
		h, err := a.Backend.PairRelay(ctx, link, name)
		a.Update(func() {
			if ctx.Err() != nil { // cancelled: the dialog is already closed
				return
			}
			cancel()
			v.relayCancel = nil
			if err != nil {
				a.Toasts.Error("No se pudo emparejar", app.ErrorText(err))
				return
			}
			v.relayOpen, v.relayLink, v.relayName = false, "", ""
			msg := "Host añadido por relay: " + h.Name
			if h.Relay != nil {
				msg += " (" + app.ScopeLabel(h.Relay.Scope) + ")"
			}
			a.Toasts.Success(msg)
			a.Router.Replace("/hosts")
		})
	})
}

func (v *View) quick(c *ui.Context, t *kit.Tokens, icon, title, sub string) bool {
	b := ui.ButtonBase(c).Label(title+", "+sub).Grow(1).Basis(0).Padding(14).Gap(12).Radius(14).Background(t.Surface).
		Border(1, t.Border).AlignItems(ui.Center).Cursor(ui.CursorPointer)
	if b.Hovered() {
		b.Background(t.Raised)
	}
	b.Children(func() {
		ui.Row(c).Size(40, 40).Radius(12).Background(t.AccentBg).AlignItems(ui.Center).Justify(ui.Center).Children(func() {
			kit.Icon(c, icon).FontSize(20).TextColor(t.Accent)
		})
		ui.Column(c).Gap(2).Shrink(1).Children(func() {
			ui.Text(c, title).FontSize(14).FontWeight(600)
			ui.Text(c, sub).FontSize(12).TextColor(t.Muted).SingleLine().Ellipsis("…")
		})
	})
	return b.Clicked()
}

func (v *View) publicKeyBox(c *ui.Context, t *kit.Tokens) {
	ui.Column(c).Gap(8).Padding(12).Radius(12).Background(t.Bg).Border(1, t.Divider).Children(func() {
		ui.Text(c, "Añade esta clave a ~/.ssh/authorized_keys del host:").FontSize(12).TextColor(t.Muted)
		key := v.pubKey
		if key == "" {
			key = "Generando clave…"
		}
		ui.Text(c, key).Font(kit.MonoFamily()).FontSize(11.5).TextColor(t.Secondary).LineHeight(1.4).Selectable()
		if kit.Button(c.Key("copy"), kit.ButtonOpts{Variant: kit.Outline, Icon: kit.IconCopy, Label: "Copiar clave pública", Height: 36,
			Disabled: v.pubKey == ""}).Clicked() && v.pubKey != "" {
			c.WriteClipboard(v.pubKey)
			v.a.Toasts.Success("Clave pública copiada")
		}
	})
}

func (v *View) submit() {
	addr, user := strings.TrimSpace(v.addr), strings.TrimSpace(v.user)
	port, err := strconv.Atoi(strings.TrimSpace(v.port))
	if addr == "" || user == "" || err != nil || port < 1 || port > 65535 {
		v.a.Toasts.Info("Rellena host, puerto y usuario")
		return
	}
	name := strings.TrimSpace(v.name)
	if name == "" {
		name = addr
	}
	h := backend.Host{ID: Slug(name), Name: name, Addr: addr, Port: port, User: user, Auth: v.auth}
	password := ""
	switch v.auth {
	case backend.AuthKeyFile:
		h.KeyFile = strings.TrimSpace(v.keyFile)
	case backend.AuthPassword:
		if v.password == "" {
			v.a.Toasts.Info("Escribe la contraseña")
			return
		}
		password = v.password
	}
	v.connect(h, password)
}

// connect saves h and connects through the flow (fingerprint, password).
func (v *View) connect(h backend.Host, password string) {
	v.busy = true
	a := v.a
	a.Go(func(ctx context.Context) {
		err := a.Backend.SaveHost(ctx, h)
		a.Update(func() {
			v.busy = false
			if err != nil {
				a.Toasts.Error("No se pudo guardar "+h.Addr, app.ErrorText(err))
				return
			}
			v.flow.Connect(h, password)
		})
	})
}

func (v *View) done(h backend.Host) {
	v.name, v.addr, v.user, v.port, v.password, v.keyFile = "", "", "", "22", "", ""
	v.a.Router.Replace("/hosts/provision/" + h.ID)
}

func (v *View) startImport() {
	v.importOpen, v.importing, v.importErr, v.imported = true, true, "", nil
	a := v.a
	a.Go(func(ctx context.Context) {
		hs, err := a.Backend.ImportSSHConfig(ctx)
		a.Update(func() {
			v.importing = false
			if err != nil {
				v.importErr = err.Error()
			}
			v.imported = hs
		})
	})
}

func (v *View) importDialog(c *ui.Context, t *kit.Tokens) {
	if !v.importOpen {
		return
	}
	open := true
	known := map[string]bool{}
	for _, h := range v.a.Live.Hosts {
		known[h.Host.ID] = true
	}
	kit.Dialog(c.Key("import"), &open, kit.DialogOpts{Title: "Importar hosts", Width: 520,
		Description: "Entradas de ~/.ssh/config (y nombres de Tailscale que aparecen en él).",
		Body: func() {
			switch {
			case v.importing:
				ui.Text(c, "Leyendo ~/.ssh/config…").FontSize(13).TextColor(t.Muted)
			case v.importErr != "":
				ui.Text(c, "No se pudo leer: "+v.importErr).FontSize(13).TextColor(t.Danger)
			case len(v.imported) == 0:
				ui.Text(c, "No hay hosts que importar").FontSize(13).TextColor(t.Muted)
			}
			ui.Scroll(c.Key("imp-list")).MaxHeight(360).Gap(8).Children(func() {
				for _, h := range v.imported {
					cc := c.Key("imp:" + h.ID)
					ui.Row(cc).Gap(10).Padding(10, 12).Radius(12).Background(t.Bg).Border(1, t.Divider).AlignItems(ui.Center).Children(func() {
						ui.Column(cc).Grow(1).Shrink(1).MinWidth(0).Gap(2).Children(func() {
							ui.Text(cc, h.Name).FontSize(14).FontWeight(600)
							ui.Text(cc, h.User+"@"+h.Addr).Font(kit.MonoFamily()).FontSize(12).TextColor(t.Muted).SingleLine().Ellipsis("…")
						})
						if known[h.ID] {
							ui.Text(cc, "Ya añadido").FontSize(12).TextColor(t.Muted)
						} else if kit.Button(cc.Key("add"), kit.ButtonOpts{Variant: kit.Outline, Label: "Añadir", Height: 34, Disabled: v.busy}).Clicked() && !v.busy {
							open = false
							v.connect(h, "")
						}
					})
				}
			})
		},
		Footer: func() {
			if kit.Button(c.Key("imp-close"), kit.ButtonOpts{Variant: kit.Outline, Label: "Cerrar", Height: 36}).Clicked() {
				open = false
			}
		}})
	if !open {
		v.importOpen = false
	}
}
