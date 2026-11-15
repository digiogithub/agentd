// Package hosts is the Hosts area (mockup 5): every saved host with its
// connection state, sessions and detected capabilities, Reintentar for
// offline ones, "Instalar agentd en <host>" for those without agentd, an
// update chip when another host runs a newer agentd, and Quitar (context
// menu) with confirmation.
package hosts

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/digiogithub/agentd/internal/desktopui/app"
	"github.com/digiogithub/agentd/internal/desktopui/backend"
	"github.com/digiogithub/agentd/internal/desktopui/kit"
	"github.com/egoist/mygo/ui"
)

func init() {
	app.Register(app.Area{
		ID: "hosts", Path: "/hosts", Title: "Hosts", Icon: kit.IconHosts, Section: app.SectionMain, Order: 1, Shortcut: 2,
		New: func(a *app.Context) app.View { return New(a) },
	})
}

// CLIs are the agent CLIs a host card lists.
var CLIs = []string{"claude", "codex", "pando", "opencode", "gemini"}

// View is the area state (UI thread only).
type View struct {
	a        *app.Context
	removing *backend.Host
	flow     *app.ConnectFlow // host key and password dialogs
}

// New returns the area.
func New(a *app.Context) *View { return &View{a: a, flow: app.NewConnectFlow(a, nil)} }

// Build shows the hosts.
func (v *View) Build(c *ui.Context, r *ui.Route) {
	t := kit.Current()
	a := v.a
	online := a.Live.Online()
	noAgentd := 0
	for _, h := range online {
		if h.Capabilities != nil && !h.Capabilities.Has("agentd") {
			noAgentd++
		}
	}
	over := fmt.Sprintf("%d en línea", len(online))
	if noAgentd > 0 {
		over += fmt.Sprintf(" · %d sin agentd", noAgentd)
	}
	latest := ""
	for _, h := range a.Live.Hosts {
		if ver := agentdVersion(h); newer(ver, latest) {
			latest = ver
		}
	}
	ui.Column(c).Grow(1).MinHeight(0).AlignItems(ui.Stretch).Children(func() {
		kit.ScreenHeader(c, over, "Hosts", func() {
			if kit.Button(c.Key("add"), kit.ButtonOpts{Variant: kit.Primary, Icon: kit.IconPlus, Label: "Añadir host", Height: 36, Round: true}).Clicked() {
				a.Navigate("/hosts/add")
			}
		}, nil)
		if len(a.Live.Hosts) == 0 {
			msg := "Añade un host por SSH para ver sus agentes."
			if !a.Live.HostsLoaded {
				msg = "Cargando…"
			}
			kit.EmptyState(c, kit.IconHosts, "", msg)
			return
		}
		w, _ := c.Size()
		cols := 1
		if w >= 1100 {
			cols = 2
		}
		ui.Scroll(c.Key("hosts")).Grow(1).MinHeight(0).Padding(4, 24, 24, 24).Children(func() {
			ui.Grid(c).Columns(cols).Gap(12).Children(func() {
				for _, h := range a.Live.Hosts {
					v.card(c.Key("h:"+h.Host.ID), t, h, latest)
				}
			})
		})
	})
	v.confirmRemove(c, t)
	v.flow.Dialogs(c)
}

func agentdVersion(h backend.HostStatus) string {
	if h.Capabilities != nil && h.Capabilities.Has("agentd") {
		return h.Capabilities.Tools["agentd"].Version
	}
	return h.AgentdVer
}

// newer reports whether version a is above b ("0.6.2" > "0.6.1").
func newer(a, b string) bool {
	if a == "" {
		return false
	}
	if b == "" {
		return true
	}
	pa, pb := strings.Split(a, "."), strings.Split(b, ".")
	for i := 0; i < max(len(pa), len(pb)); i++ {
		var x, y int
		if i < len(pa) {
			x, _ = strconv.Atoi(pa[i])
		}
		if i < len(pb) {
			y, _ = strconv.Atoi(pb[i])
		}
		if x != y {
			return x > y
		}
	}
	return false
}

func address(h backend.Host) string {
	if h.Local() {
		return "Este equipo"
	}
	addr := h.Addr
	if h.User != "" {
		addr = h.User + "@" + addr
	}
	if h.Port != 0 && h.Port != 22 {
		addr += ":" + strconv.Itoa(h.Port)
	}
	return addr + " · SSH"
}

func (v *View) card(c *ui.Context, t *kit.Tokens, h backend.HostStatus, latest string) {
	a := v.a
	host := h.Host
	var mux []backend.Session
	agentdN := 0
	for _, s := range a.Live.Sessions {
		if s.HostID != host.ID {
			continue
		}
		if s.Source == backend.SourceAgentd {
			agentdN++
		} else {
			mux = append(mux, s)
		}
	}
	card := kit.Card(c, ui.Color{}, func() {
		if h.State != backend.HostOnline {
			sub, col := "Sin conexión", t.Muted
			switch h.State {
			case backend.HostConnecting:
				sub = "Conectando…"
			case backend.HostError:
				sub, col = "Error de conexión", t.Danger
				if h.Err != "" {
					sub = app.HostErrText(h)
				}
			default:
				if h.Err != "" {
					sub = app.HostErrText(h)
				}
			}
			ui.Row(c).Gap(10).AlignItems(ui.Center).Children(func() {
				ui.Box(c).Size(10, 10).Radius(5).Border(1.5, t.Faint).Label("Sin conexión")
				ui.Column(c).Grow(1).Shrink(1).MinWidth(0).Gap(2).Children(func() {
					ui.Text(c, host.Name).FontSize(16).FontWeight(600).TextColor(t.Secondary)
					ui.Text(c, sub).FontSize(12).TextColor(col).SingleLine().Ellipsis("…")
				})
				label := "Reintentar"
				switch h.ErrCode {
				case backend.HostErrKeyUnknown:
					label = "Ver huella"
				case backend.HostErrPassword:
					label = "Contraseña"
				}
				if h.State != backend.HostConnecting && kit.Button(c.Key("retry"), kit.ButtonOpts{Variant: kit.Outline, Label: label, Height: 36,
					Disabled: v.flow.Busy()}).Clicked() {
					v.flow.Connect(host, "")
				}
			})
			return
		}
		caps := h.Capabilities
		// Capabilities come from Detect (Preparar host); a live daemon
		// (version from its ping, or agentd sessions) counts too.
		hasAgentd := (caps != nil && caps.Has("agentd")) || h.AgentdVer != "" || agentdN > 0
		count := "sin sesiones"
		switch {
		case hasAgentd:
			n := h.Sessions
			if n == 0 {
				n = agentdN
			}
			count = app.Plural(n, "sesión", "sesiones")
		case len(mux) > 0:
			count = fmt.Sprintf("%d %s", len(mux), mux[0].Source)
		}
		ui.Row(c).Gap(10).AlignItems(ui.Center).Children(func() {
			ui.Box(c).Size(10, 10).Radius(5).Background(t.Success).Label("En línea")
			ui.Column(c).Grow(1).Shrink(1).MinWidth(0).Gap(2).Children(func() {
				ui.Text(c, host.Name).FontSize(16).FontWeight(600)
				ui.Text(c, address(host)).FontSize(12).TextColor(t.Muted).SingleLine().Ellipsis("…")
			})
			ui.Text(c, count).FontSize(12).TextColor(t.Secondary)
		})
		ui.Row(c).Gap(6).GapY(6).Wrap().Children(func() {
			if hasAgentd {
				ver := h.AgentdVer
				if caps != nil && caps.Has("agentd") {
					ver = caps.Tools["agentd"].Version
				}
				kit.TagChip(c.Key("agentd"), strings.TrimSpace("agentd "+ver), t.AccentLight, t.AccentBg)
				if newer(latest, ver) {
					if ui.ButtonBase(c.Key("upd")).Label("Actualización " + latest).Cursor(ui.CursorPointer).
						Children(func() { kit.TagChip(c, "Actualización "+latest, t.Waiting, t.WaitingBg) }).Clicked() {
						a.Navigate("/hosts/provision/" + host.ID)
					}
				}
				if caps != nil && caps.Has("zellij") {
					kit.TagChip(c.Key("zellij"), "zellij", t.Secondary, t.Raised)
				}
				if caps != nil && caps.Has("tmux") {
					kit.TagChip(c.Key("tmux"), "tmux", t.Secondary, t.Raised)
				}
			}
			for _, s := range mux {
				kit.TagChip(c.Key("m:"+s.Key()), string(s.Source)+" · "+s.Name, t.Secondary, t.Raised)
			}
			var clis []string
			for _, n := range CLIs {
				if caps != nil && caps.Has(n) {
					clis = append(clis, n)
				}
			}
			if len(clis) > 0 {
				kit.TagChip(c.Key("clis"), strings.Join(clis, " · "), t.Secondary, t.Raised)
			}
		})
		if !hasAgentd {
			ui.Column(c).Gap(10).Padding(12).Radius(12).Background(t.Bg).Border(1, t.Divider).Children(func() {
				ui.Text(c, "Sin agentd solo hay terminal. Instálalo por SSH para tener chat, aprobaciones y buzón entre agentes.").
					FontSize(13).TextColor(t.Secondary).LineHeight(1.4)
				if kit.Button(c.Key("install"), kit.ButtonOpts{Variant: kit.Primary, Label: "Instalar agentd en " + host.Name, Height: 40}).Clicked() {
					a.Navigate("/hosts/provision/" + host.ID)
				}
			})
		}
	})
	card.AlignSelf(ui.Start).ContextMenu(func(m *ui.Menu) {
		if h.State == backend.HostOnline && m.Item("Preparar host").Chosen() {
			a.Navigate("/hosts/provision/" + host.ID)
		}
		if m.Item("Reconectar").Chosen() {
			v.flow.Connect(host, "")
		}
		m.Separator()
		if m.Item("Quitar").Chosen() {
			hh := host
			v.removing = &hh
		}
	})
}

func (v *View) confirmRemove(c *ui.Context, t *kit.Tokens) {
	open := v.removing != nil
	if !open {
		return
	}
	h := *v.removing
	kit.Dialog(c.Key("remove"), &open, kit.DialogOpts{Title: "¿Quitar " + h.Name + "?", Description: "Se borra de la app; el host no se toca.",
		Footer: func() {
			if kit.Button(c.Key("cancel"), kit.ButtonOpts{Variant: kit.Outline, Label: "Cancelar", Height: 36}).Clicked() {
				open = false
			}
			if kit.Button(c.Key("ok"), kit.ButtonOpts{Variant: kit.Danger, Label: "Quitar", Height: 36}).Clicked() {
				open = false
				a := v.a
				a.Go(func(ctx context.Context) {
					if err := a.Backend.RemoveHost(ctx, h.ID); err != nil {
						a.Toasts.Error("No se pudo quitar el host", err.Error())
					}
				})
			}
		}})
	if !open {
		v.removing = nil
	}
}
