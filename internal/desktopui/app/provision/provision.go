// Package provision is the Preparar host page (mockup 7): what Detect found,
// the agentd switch with the herdr-core listener and MCP checks, the agent
// CLIs to install or update, "Ver los comandos" (backend.Plan) and Instalar,
// which streams backend.Install events as progress rows.
package provision

import (
	"context"
	"slices"
	"strconv"
	"strings"

	"github.com/digiogithub/agentd/internal/desktopui/app"
	"github.com/digiogithub/agentd/internal/desktopui/backend"
	"github.com/digiogithub/agentd/internal/desktopui/kit"
	"github.com/egoist/mygo/ui"
)

func init() {
	app.Register(app.Area{
		ID: "provision", Path: "/hosts/provision", Patterns: []string{"/hosts/provision/{host}"}, Title: "Preparar host",
		Icon: kit.IconDownload, Section: app.SectionHidden, Parent: "hosts",
		New: func(a *app.Context) app.View { return New(a) },
	})
}

// CLI is an installable agent CLI.
type CLI struct{ ID, Label, Source string }

// CLIs of the page.
var CLIs = []CLI{
	{"claude", "Claude Code", "npm @anthropic-ai/claude-code"},
	{"codex", "Codex", "npm @openai/codex"},
	{"pando", "Pando", "release de GitHub"},
	{"opencode", "opencode", "npm opencode-ai"},
	{"gemini", "Gemini CLI", "npm @google/gemini-cli"},
}

// Progress is one row of the install log.
type Progress struct {
	Step, Message string
	Done, Skipped bool
	Err           string
}

// View is the page state for one host at a time (UI thread only).
type View struct {
	a      *app.Context
	hostID string

	caps      *backend.Capabilities
	detecting bool
	detectErr string

	agentd, hc, mcp bool
	clis            map[string]bool
	showCommands    bool

	running, finished bool
	progress          []Progress
	cancel            context.CancelFunc
}

// New returns the page.
func New(a *app.Context) *View { return &View{a: a, clis: map[string]bool{}} }

// Close stops a running install stream.
func (v *View) Close() {
	if v.cancel != nil {
		v.cancel()
	}
}

// Progress returns the install rows (tests).
func (v *View) Progress() []Progress { return v.progress }

func (v *View) reset(hostID, preselect string) {
	v.Close()
	*v = View{a: v.a, hostID: hostID, clis: map[string]bool{}, hc: true, mcp: true}
	if preselect != "" {
		v.clis[preselect] = true
	}
	v.detect()
}

func (v *View) detect() {
	v.detecting, v.detectErr = true, ""
	a, id := v.a, v.hostID
	a.Go(func(ctx context.Context) {
		caps, err := a.Backend.Detect(ctx, id)
		a.Update(func() {
			if v.hostID != id {
				return
			}
			v.detecting = false
			if err != nil {
				v.detectErr = "No se pudo detectar el host"
				return
			}
			v.caps = caps
			v.agentd = !caps.Has("agentd")
		})
	})
}

func (v *View) options() backend.ProvisionOptions {
	o := backend.ProvisionOptions{Agentd: v.agentd, HCListener: v.agentd && v.hc, MCP: v.agentd && v.mcp}
	for _, c := range CLIs {
		if v.clis[c.ID] {
			o.CLIs = append(o.CLIs, c.ID)
		}
	}
	return o
}

func (v *View) count() int {
	n := len(v.options().CLIs)
	if v.agentd {
		n++
	}
	return n
}

// Build shows the page.
func (v *View) Build(c *ui.Context, r *ui.Route) {
	t := kit.Current()
	id := ""
	if r != nil {
		id = r.Param("host")
	}
	if id != v.hostID {
		pre := ""
		if r != nil {
			pre = r.Query("cli")
		}
		v.reset(id, pre)
	}
	name := v.a.Live.HostName(id)
	if name == "" {
		name = id
	}
	sub := "Detectando…"
	switch {
	case v.detectErr != "":
		sub = v.detectErr
	case v.caps != nil:
		parts := []string{strings.TrimSpace(v.caps.OS + " " + v.caps.Arch)}
		if v.caps.Distro != "" {
			parts = append(parts, v.caps.Distro)
		}
		if v.caps.SystemdUser {
			parts = append(parts, "systemd de usuario")
		}
		sub = strings.Join(parts, " · ")
	}
	ui.Column(c).Grow(1).MinHeight(0).AlignItems(ui.Stretch).Children(func() {
		if kit.RouteHeader(c, "Preparar "+name, sub, nil) {
			v.a.Navigate("/hosts")
		}
		if v.caps == nil {
			ui.Column(c).Grow(1).AlignItems(ui.Center).Justify(ui.Center).Gap(12).Children(func() {
				if v.detectErr != "" {
					ui.Text(c, v.detectErr).FontSize(14).TextColor(t.Danger)
					if kit.Button(c.Key("redetect"), kit.ButtonOpts{Variant: kit.Outline, Label: "Reintentar"}).Clicked() {
						v.detect()
					}
					return
				}
				kit.Spinner(c).FontSize(22).TextColor(t.Muted)
			})
			return
		}
		ui.Scroll(c.Key("body")).Grow(1).MinHeight(0).Padding(16, 24).Children(func() {
			ui.Column(c).Gap(12).MaxWidth(720).FillWidth().Children(func() {
				v.detected(c, t)
				v.agentdCard(c, t)
				kit.SectionLabel(c, "Agentes de terminal", t.Muted).Margin(8, 0, 0, 0)
				for _, cli := range CLIs {
					v.cliRow(c.Key("cli:"+cli.ID), t, cli)
				}
				if len(v.progress) > 0 {
					v.progressList(c, t)
				}
			})
		})
		v.footer(c, t)
	})
}

func (v *View) detected(c *ui.Context, t *kit.Tokens) {
	kit.SectionLabel(c, "Detectado", t.Muted)
	ui.Row(c).Gap(6).GapY(6).Wrap().Children(func() {
		var missing []string
		names := []string{"agentd", "zellij", "tmux", "node"}
		for _, cl := range CLIs {
			names = append(names, cl.ID)
		}
		for _, n := range names {
			tool := v.caps.Tools[n]
			if tool.Installed {
				kit.TagChip(c.Key("d:"+n), strings.TrimSpace(n+" "+tool.Version), t.SuccessText, t.SuccessBg)
			} else if slices.Contains([]string{"agentd", "zellij", "tmux", "node"}, n) {
				missing = append(missing, n)
			}
		}
		for _, n := range missing {
			kit.TagChip(c.Key("m:"+n), "sin "+n, t.Muted, t.Surface)
		}
	})
}

func (v *View) agentdCard(c *ui.Context, t *kit.Tokens) {
	tool := v.caps.Tools["agentd"]
	kit.Card(c, ui.Color{}, func() {
		ui.Row(c).Gap(12).AlignItems(ui.Center).Children(func() {
			ui.Column(c).Grow(1).Shrink(1).Gap(2).Children(func() {
				title, sub := "agentd", "Binario en ~/.local/bin, servicio systemd de usuario, firma comprobada"
				if tool.Installed {
					title = strings.TrimSpace("agentd " + tool.Version + " instalado")
					sub = "Actívalo para reinstalar o actualizar desde la release"
				}
				ui.Text(c, title).FontSize(15).FontWeight(600)
				ui.Text(c, sub).FontSize(12).TextColor(t.Muted)
			})
			kit.Toggle(c.Key("agentd"), &v.agentd, "Instalar agentd")
		})
		check := func(key, label string, on *bool) {
			ui.Row(c).Gap(10).AlignItems(ui.Center).Children(func() {
				cb := ui.Checkbox(c.Key(key), on, label).Disabled(!v.agentd)
				if !v.agentd {
					cb.Opacity(0.5)
				}
			})
		}
		check("hc", "Activar el listener herdr-core (Herdr, Alma, Whip)", &v.hc)
		check("mcp", "Registrar el MCP de agentd en los CLIs instalados", &v.mcp)
	})
}

func (v *View) cliRow(c *ui.Context, t *kit.Tokens, cli CLI) {
	tool := v.caps.Tools[cli.ID]
	on := v.clis[cli.ID]
	sub := cli.Source
	action := "Instalar"
	switch {
	case tool.Installed && on:
		sub, action = tool.Version+" · instalado", "Se actualizará"
	case tool.Installed:
		sub, action = tool.Version+" · instalado", "Actualizar"
	case on:
		action = "Se instalará"
	}
	ui.Row(c).Gap(12).Padding(10, 14).Radius(14).Background(t.Surface).Border(1, t.Border).AlignItems(ui.Center).Children(func() {
		ui.Column(c).Grow(1).Shrink(1).Gap(2).Children(func() {
			ui.Text(c, cli.Label).FontSize(14).FontWeight(600)
			ui.Text(c, sub).FontSize(12).TextColor(t.Muted)
		})
		b := ui.ButtonBase(c.Key("act")).Label(action+" "+cli.Label).Height(40).Padding(0, 14).Radius(10).AlignItems(ui.Center).
			Cursor(ui.CursorPointer).Selected(on).Disabled(v.running)
		if on {
			b.Border(1, t.Accent).Background(t.AccentBg).TextColor(t.AccentLight)
		} else {
			b.Border(1, t.Border).TextColor(t.Text)
		}
		b.Children(func() { ui.Text(c, action).FontSize(13).FontWeight(600) })
		if b.Clicked() && !v.running {
			v.clis[cli.ID] = !on
		}
	})
}

func (v *View) progressList(c *ui.Context, t *kit.Tokens) {
	label := "Resultado"
	if v.running {
		label = "Instalando"
	}
	kit.SectionLabel(c, label, t.Muted).Margin(8, 0, 0, 0)
	for i, p := range v.progress {
		cc := c.Key(i)
		ui.Row(cc).Gap(10).AlignItems(ui.Start).Padding(4, 0).Children(func() {
			switch {
			case p.Err != "":
				kit.Icon(cc, kit.IconError).FontSize(16).TextColor(t.Danger)
			case p.Skipped:
				kit.Icon(cc, kit.IconCircle).FontSize(16).TextColor(t.Faint)
			case p.Done:
				kit.Icon(cc, kit.IconSuccess).FontSize(16).TextColor(t.Success)
			default:
				kit.Spinner(cc).FontSize(16).TextColor(t.Working)
			}
			ui.Column(cc).Grow(1).Gap(2).Children(func() {
				msg := p.Message
				if p.Skipped {
					msg += " (omitido)"
				}
				ui.Text(cc, msg).FontSize(13.5).TextColor(t.Secondary)
				if p.Err != "" {
					ui.Text(cc, p.Err).Font(kit.MonoFamily()).FontSize(12).TextColor(t.Danger)
				}
			})
		})
	}
}

func (v *View) footer(c *ui.Context, t *kit.Tokens) {
	failed := false
	for _, p := range v.progress {
		if p.Err != "" {
			failed = true
		}
	}
	ui.Column(c).Shrink(0).Padding(12, 24, 18, 24).Gap(10).BorderWidth(1, 0, 0, 0).BorderColor(t.Divider).Children(func() {
		ui.Column(c).MaxWidth(720).FillWidth().Gap(10).Children(func() {
			if !v.running && !v.finished {
				b := ui.ButtonBase(c.Key("cmds")).Label("Ver los comandos que se ejecutarán por SSH").Gap(6).AlignItems(ui.Center).AlignSelf(ui.Start).
					Cursor(ui.CursorPointer).Expanded(v.showCommands)
				b.Children(func() {
					ic := kit.IconChevronR
					if v.showCommands {
						ic = kit.IconChevronDown
					}
					kit.Icon(c, ic).FontSize(14).TextColor(t.Accent)
					ui.Text(c, "Ver los comandos que se ejecutarán por SSH").FontSize(13).TextColor(t.Accent)
				})
				if b.Clicked() {
					v.showCommands = !v.showCommands
				}
				if v.showCommands {
					var cmds []string
					for _, s := range v.a.Backend.Plan(v.caps, v.options()) {
						cmds = append(cmds, s.Command)
					}
					text := "# nada que ejecutar"
					if len(cmds) > 0 {
						text = strings.Join(cmds, "\n")
					}
					ui.Scroll(c.Key("plan")).MaxHeight(200).Children(func() { kit.CodeBox(c, text) })
				}
			}
			n := v.count()
			label := "Nada que instalar"
			if n > 0 {
				label = "Instalar " + Components(n)
			}
			disabled := n == 0
			action := v.install
			switch {
			case v.running:
				label, disabled = "Instalando…", true
			case v.finished && failed:
				label, disabled = "Volver a intentar", false
			case v.finished:
				label, disabled = "Listo", false
				action = func() { v.a.Navigate("/hosts") }
			}
			if kit.Button(c.Key("install"), kit.ButtonOpts{Variant: kit.Primary, Label: label, Height: 52, FontSize: 15, Disabled: disabled}).Clicked() && !disabled {
				action()
			}
		})
	})
}

// Components is "1 componente" / "3 componentes".
func Components(n int) string {
	if n == 1 {
		return "1 componente"
	}
	return strconv.Itoa(n) + " componentes"
}

func (v *View) install() {
	opts := v.options()
	if v.count() == 0 {
		return
	}
	v.running, v.finished, v.progress = true, false, nil
	ctx, cancel := context.WithCancel(v.a.Ctx)
	v.cancel = cancel
	a, id := v.a, v.hostID
	batch := app.NewBatch(a.Update, func(evs []backend.ProvisionEvent) {
		if v.hostID != id {
			return
		}
		for _, e := range evs {
			v.apply(e)
		}
	})
	a.Go(func(context.Context) {
		ch, err := a.Backend.Install(ctx, id, opts)
		if err != nil {
			a.Update(func() {
				v.progress = append(v.progress, Progress{Step: "install", Message: "La instalación falló", Err: err.Error()})
				v.running, v.finished = false, true
			})
			return
		}
		for e := range ch {
			batch.Push(e)
		}
		a.Update(func() {
			if v.hostID != id {
				return
			}
			v.running, v.finished = false, true
			a.Go(func(ctx context.Context) { _ = a.Backend.Connect(ctx, id) })
			v.detectAfterInstall()
		})
	})
}

func (v *View) detectAfterInstall() {
	a, id := v.a, v.hostID
	a.Go(func(ctx context.Context) {
		caps, err := a.Backend.Detect(ctx, id)
		if err == nil {
			a.Update(func() {
				if v.hostID == id {
					v.caps = caps
					v.agentd = false
					v.clis = map[string]bool{}
				}
			})
		}
	})
}

func (v *View) apply(e backend.ProvisionEvent) {
	for i := len(v.progress) - 1; i >= 0; i-- {
		p := &v.progress[i]
		if p.Step == e.Step && !p.Done && p.Err == "" {
			p.Done, p.Skipped, p.Err = e.Done || e.Skipped, e.Skipped, e.Err
			if e.Message != "" {
				p.Message = e.Message
			}
			return
		}
	}
	v.progress = append(v.progress, Progress{Step: e.Step, Message: e.Message, Done: e.Done || e.Skipped, Skipped: e.Skipped, Err: e.Err})
}
