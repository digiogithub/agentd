package settings

import (
	"context"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/digiogithub/agentd/internal/desktopui/app"
	"github.com/digiogithub/agentd/internal/desktopui/backend"
	"github.com/digiogithub/agentd/internal/desktopui/kit"
	"github.com/digiogithub/agentd/internal/proto"
	"github.com/egoist/mygo/ui"
)

// hostDevices is the device list of one host (UI thread only).
type hostDevices struct {
	loading bool
	loaded  bool
	err     string
	list    []proto.RelayDeviceInfo
}

// revokeAsk is the pending confirmation.
type revokeAsk struct {
	host backend.Host
	dev  proto.RelayDeviceInfo
	own  bool
	busy bool
}

// agentdHosts are the live hosts whose daemon is up, ordered as reported.
func (v *View) agentdHosts() []backend.HostStatus {
	var out []backend.HostStatus
	for _, h := range v.a.Live.Hosts {
		if h.State != backend.HostOnline {
			continue
		}
		if h.AgentdVer == "" && (h.Capabilities == nil || !h.Capabilities.Has("agentd")) {
			continue
		}
		out = append(out, h)
	}
	return out
}

// loadDevices fetches the devices of one host.
func (v *View) loadDevices(id string) {
	if v.devs == nil {
		v.devs = map[string]*hostDevices{}
	}
	d := v.devs[id]
	if d == nil {
		d = &hostDevices{}
		v.devs[id] = d
	}
	if d.loading {
		return
	}
	d.loading = true
	a := v.a
	a.Go(func(ctx context.Context) {
		ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
		defer cancel()
		list, err := a.Backend.RelayDevices(ctx, id)
		a.Update(func() {
			d.loading, d.loaded = false, true
			if err != nil {
				d.err, d.list = app.ErrorText(err), nil
				return
			}
			d.err, d.list = "", list
		})
	})
}

// ensureDevices loads the hosts not asked yet (page open, host came up).
func (v *View) ensureDevices(hosts []backend.HostStatus) {
	for _, h := range hosts {
		if d := v.devs[h.Host.ID]; d == nil {
			v.loadDevices(h.Host.ID)
		}
	}
}

func (v *View) refreshDevices() {
	for _, h := range v.agentdHosts() {
		v.loadDevices(h.Host.ID)
	}
}

func deviceKind(d proto.RelayDeviceInfo) string { return d.Kind }

// canRevoke says whether the daemon would accept revoking d from host h.
func canRevoke(h backend.Host, d proto.RelayDeviceInfo) bool {
	if h.Relay == nil {
		return true
	}
	return h.Allows(backend.CapDevices) || d.ID == h.Relay.DeviceID
}

func isOwn(h backend.Host, d proto.RelayDeviceInfo) bool {
	return h.Relay != nil && h.Relay.DeviceID != "" && d.ID == h.Relay.DeviceID
}

func (v *View) devicesSection(c *ui.Context, t *kit.Tokens) {
	hosts := v.agentdHosts()
	v.ensureDevices(hosts)
	ui.Row(c).AlignItems(ui.Center).Margin(12, 0, 0, 0).Children(func() {
		kit.SectionLabel(c, "Dispositivos", t.Muted).Grow(1)
		if kit.Button(c.Key("dev-refresh"), kit.ButtonOpts{Variant: kit.Outline, Icon: kit.IconRefresh, Label: "Actualizar", Height: 32}).Clicked() {
			v.refreshDevices()
		}
	})
	group(c, t, func() {
		if len(hosts) == 0 {
			ui.Text(c, "Ningún host con agentd conectado").FontSize(12).TextColor(t.Muted).Padding(14)
		}
		for _, hs := range hosts {
			v.hostDevices(c.Key("dh:"+hs.Host.ID), t, hs.Host)
		}
	})
	ui.Text(c, "Equipos emparejados con cada host a través del relay. Revocar corta su acceso al instante.").FontSize(12).TextColor(t.Muted)
	v.revokeDialog(c, t)
}

func (v *View) hostDevices(c *ui.Context, t *kit.Tokens, h backend.Host) {
	d := v.devs[h.ID]
	ui.Column(c).Children(func() {
		ui.Row(c).Padding(10, 14, 4, 14).Gap(8).AlignItems(ui.Center).Children(func() {
			ui.Text(c, h.Name).FontSize(13).FontWeight(600)
			if h.Relay != nil {
				ui.Text(c, "relay").FontSize(12).TextColor(t.Muted)
			}
		})
		switch {
		case d == nil || (d.loading && !d.loaded):
			ui.Text(c, "Cargando…").FontSize(12).TextColor(t.Muted).Padding(4, 14, 12, 14)
		case d.err != "":
			ui.Text(c, "No se pudo leer: "+d.err).FontSize(12).TextColor(t.Muted).Padding(4, 14, 12, 14)
		case len(d.list) == 0:
			ui.Text(c, "Sin dispositivos").FontSize(12).TextColor(t.Muted).Padding(4, 14, 12, 14)
		default:
			for _, dev := range d.list {
				v.deviceRow(c.Key("d:"+dev.ID), t, h, dev)
			}
		}
	})
}

func (v *View) deviceRow(c *ui.Context, t *kit.Tokens, h backend.Host, d proto.RelayDeviceInfo) {
	own := isOwn(h, d)
	now := v.Now()
	ui.Row(c).MinHeight(56).Padding(8, 14).Gap(12).AlignItems(ui.Center).Children(func() {
		kit.Icon(c, kit.IconMonitor).FontSize(18).TextColor(t.Muted)
		ui.Column(c).Grow(1).Shrink(1).Gap(3).Children(func() {
			ui.Row(c).Gap(6).AlignItems(ui.Center).Wrap().Children(func() {
				ui.Text(c, d.Name).FontSize(14)
				if own {
					kit.Pill(c, "este equipo", t.OnAccent, t.Accent)
				}
				if deviceKind(d) == "guest" {
					kit.Pill(c, "invitado", t.Secondary, t.Raised)
				}
				kit.Pill(c, app.ScopeLabel(d.Scope), t.Secondary, t.Raised)
			})
			ui.Text(c, deviceDetail(d, now)).FontSize(12).TextColor(t.Muted)
			if len(d.Sessions) > 0 {
				ui.Text(c, "Sesiones: "+strings.Join(d.Sessions, ", ")).FontSize(12).Font(kit.MonoFamily()).TextColor(t.Muted)
			}
		})
		if canRevoke(h, d) {
			label := "Revocar acceso de " + d.Name
			if kit.Button(c.Key("revoke"), kit.ButtonOpts{Variant: kit.Danger, Label: "Revocar", Height: 34,
				Tooltip: label, Aria: label}).Clicked() {
				v.ask = &revokeAsk{host: h, dev: d, own: own}
			}
		}
	})
}

// deviceDetail is "Android · en línea · caduca en 22 h" style text.
func deviceDetail(d proto.RelayDeviceInfo, now time.Time) string {
	var parts []string
	if d.Platform != "" {
		parts = append(parts, d.Platform)
	}
	switch {
	case d.Online:
		parts = append(parts, "en línea")
	case d.LastSeen != nil:
		parts = append(parts, "visto "+app.RelativeTime(*d.LastSeen, now))
	default:
		parts = append(parts, "sin conexiones")
	}
	if d.ExpiresAt != nil {
		if d.ExpiresAt.After(now) {
			parts = append(parts, "caduca "+expiresIn(d.ExpiresAt.Sub(now)))
		} else {
			parts = append(parts, "caducado")
		}
	}
	return strings.Join(parts, " · ")
}

func (v *View) revokeDialog(c *ui.Context, t *kit.Tokens) {
	ask := v.ask
	if ask == nil {
		return
	}
	open := true
	desc := "Revocar acceso de «" + ask.dev.Name + "»: el dispositivo dejará de poder conectarse a " + ask.host.Name +
		" y tendrá que emparejarse de nuevo."
	if ask.own {
		desc += " Es este equipo: perderá el acceso a " + ask.host.Name + " y tendrás que emparejarlo otra vez con una invitación."
	}
	kit.Dialog(c.Key("revoke-dialog"), &open, kit.DialogOpts{Title: "Revocar dispositivo", Description: desc, Width: 480,
		Footer: func() {
			if kit.Button(c.Key("revoke-cancel"), kit.ButtonOpts{Variant: kit.Outline, Label: "Cancelar", Height: 36}).Clicked() {
				open = false
			}
			label := "Revocar"
			if ask.busy {
				label = "Revocando…"
			}
			if kit.Button(c.Key("revoke-confirm"), kit.ButtonOpts{Variant: kit.Danger, Label: label, Aria: "Confirmar revocar",
				Height: 36, Disabled: ask.busy}).Clicked() && !ask.busy {
				v.revoke(ask)
			}
		}})
	if !open && !ask.busy {
		v.ask = nil
	}
}

func (v *View) revoke(ask *revokeAsk) {
	ask.busy = true
	a := v.a
	hostID, dev := ask.host.ID, ask.dev
	a.Go(func(ctx context.Context) {
		ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
		defer cancel()
		err := a.Backend.RevokeRelayDevice(ctx, hostID, dev.ID)
		a.Update(func() {
			ask.busy = false
			if v.ask == ask {
				v.ask = nil
			}
			if err != nil {
				a.Toasts.Error("No se pudo revocar el dispositivo", app.ErrorText(err))
				return
			}
			a.Toasts.Success("Acceso revocado: " + dev.Name)
			if d := v.devs[hostID]; d != nil {
				d.list = slices.DeleteFunc(slices.Clone(d.list), func(x proto.RelayDeviceInfo) bool { return x.ID == dev.ID })
			}
			v.loadDevices(hostID)
		})
	})
}

// expiresIn is "en 40 min", "en 22 h", "en 3 días".
func expiresIn(d time.Duration) string {
	switch {
	case d < time.Hour:
		return "en " + strconv.Itoa(max(1, int(d.Minutes()))) + " min"
	case d < 48*time.Hour:
		return "en " + strconv.Itoa(int(d.Hours())) + " h"
	}
	return "en " + strconv.Itoa(int(d.Hours()/24)) + " días"
}
