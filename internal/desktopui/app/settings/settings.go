// Package settings is the Ajustes area (mockup 9): which events notify
// (stored locally), muted sessions, security (the app's SSH key, devices and
// tokens) and the theme. The phone's "Pedir huella para aprobar" has no
// desktop counterpart and is left out.
package settings

import (
	"context"
	"slices"
	"strconv"
	"time"

	"github.com/digiogithub/agentd/internal/desktopui/app"
	"github.com/digiogithub/agentd/internal/desktopui/kit"
	"github.com/egoist/mygo/ui"
)

func init() {
	app.Register(app.Area{
		ID: "settings", Path: "/settings", Title: "Ajustes", Icon: kit.IconSettings, Section: app.SectionBottom, Shortcut: 4,
		New: func(a *app.Context) app.View { return &View{a: a, Now: time.Now} },
	})
}

// Themes of the segmented control.
var Themes = []kit.Mode{kit.ModeDark, kit.ModeLight, kit.ModeSystem}

// View is the area state (UI thread only).
type View struct {
	a       *app.Context
	keyOpen bool
	pubKey  string
	// Now is the clock of mute expiry (tests).
	Now func() time.Time
}

func (v *View) save(fn func(s *app.Settings)) {
	if err := v.a.Prefs.Update(fn); err != nil {
		v.a.Toasts.Error("No se pudieron guardar los ajustes", err.Error())
	}
}

// Build shows the settings.
func (v *View) Build(c *ui.Context, r *ui.Route) {
	t := kit.Current()
	s := v.a.Prefs.Get()
	ui.Column(c).Grow(1).MinHeight(0).AlignItems(ui.Stretch).Children(func() {
		kit.ScreenHeader(c, "", "Ajustes", nil, nil)
		ui.Scroll(c.Key("body")).Grow(1).MinHeight(0).Padding(4, 24, 24, 24).Children(func() {
			ui.Column(c).Gap(10).MaxWidth(720).FillWidth().Children(func() {
				kit.SectionLabel(c, "Avisarme cuando", t.Muted)
				group(c, t, func() {
					v.toggle(c, t, "wait", "Un agente espera respuesta", "Permisos, preguntas y planes", s.NotifyWaiting,
						func(s *app.Settings, on bool) { s.NotifyWaiting = on })
					v.toggle(c, t, "done", "Termina un turno", "Agrupado cada 30 s por sesión", s.NotifyTurnDone,
						func(s *app.Settings, on bool) { s.NotifyTurnDone = on })
					v.toggle(c, t, "question", "Preguntas", "Un agente te pregunta por el buzón", s.NotifyQuestion,
						func(s *app.Settings, on bool) { s.NotifyQuestion = on })
					v.toggle(c, t, "blocked", "Tareas bloqueadas", "Un agente marca una tarea como bloqueada", s.NotifyBlocked,
						func(s *app.Settings, on bool) { s.NotifyBlocked = on })
					v.toggle(c, t, "exit", "Una sesión se cierra", "Incluye el código de salida", s.NotifyExit,
						func(s *app.Settings, on bool) { s.NotifyExit = on })
					v.toggle(c, t, "reply", "Responder desde el aviso", "Al hacer clic en el aviso se abre la pregunta para permitir, denegar o escribir",
						s.ReplyFromAlert, func(s *app.Settings, on bool) { s.ReplyFromAlert = on })
				})
				kit.SectionLabel(c, "Silenciadas", t.Muted).Margin(12, 0, 0, 0)
				now := v.Now()
				var muted []app.Mute
				for _, m := range s.Muted {
					if now.Before(m.Until) {
						muted = append(muted, m)
					}
				}
				group(c, t, func() {
					if len(muted) == 0 {
						ui.Text(c, "Ninguna sesión silenciada").FontSize(12).TextColor(t.Muted).Padding(14)
					}
					for _, m := range muted {
						cc := c.Key("mute:" + m.Session)
						ui.Row(cc).MinHeight(56).Padding(8, 14).Gap(12).AlignItems(ui.Center).Children(func() {
							kit.Icon(cc, kit.IconBellOff).FontSize(16).TextColor(t.Muted)
							ui.Column(cc).Grow(1).Gap(2).Children(func() {
								ui.Text(cc, m.Label).Font(kit.MonoFamily()).FontSize(13.5)
								ui.Text(cc, until(m.Until, now)).FontSize(12).TextColor(t.Muted)
							})
							if kit.Button(cc.Key("unmute"), kit.ButtonOpts{Variant: kit.Outline, Label: "Quitar", Height: 34,
								Tooltip: "Quitar silencio de " + m.Label, Aria: "Quitar silencio de " + m.Label}).Clicked() {
								key := m.Session
								v.save(func(s *app.Settings) {
									s.Muted = slices.DeleteFunc(s.Muted, func(x app.Mute) bool { return x.Session == key })
								})
							}
						})
					}
				})
				ui.Text(c, "El silencio se guarda en este equipo.").FontSize(12).TextColor(t.Muted)
				kit.SectionLabel(c, "Seguridad", t.Muted).Margin(12, 0, 0, 0)
				group(c, t, func() {
					if link(c.Key("keys"), t, "Claves SSH", "1 ed25519") {
						v.showKey()
					}
					if link(c.Key("devices"), t, "Dispositivos y tokens", "revocar") {
						v.a.Toasts.Info("Gestión de dispositivos: pendiente")
					}
				})
				kit.SectionLabel(c, "Vista chat", t.Muted).Margin(12, 0, 0, 0)
				v.chatFont(c, t, s)
				kit.SectionLabel(c, "Apariencia", t.Muted).Margin(12, 0, 0, 0)
				sel := slices.Index(Themes, s.Theme)
				if sel < 0 {
					sel = 0
				}
				if kit.Segmented(c.Key("theme"), &sel, []string{"Oscuro", "Claro", "Sistema"}, nil) {
					mode := Themes[sel]
					v.save(func(s *app.Settings) { s.Theme = mode })
				}
			})
		})
	})
	v.keyDialog(c, t)
}

// chatFont sets the chat view's default text size, font and line spacing
// (the chat's "Texto del chat" menu changes the same settings).
func (v *View) chatFont(c *ui.Context, t *kit.Tokens, s app.Settings) {
	group(c, t, func() {
		ui.Row(c).MinHeight(60).Padding(8, 14).Gap(12).AlignItems(ui.Center).Children(func() {
			ui.Column(c).Grow(1).Shrink(1).Gap(2).Children(func() {
				ui.Text(c, "Tamaño del texto").FontSize(14.5)
				ui.Text(c, "Mensajes del agente y tuyos").FontSize(12).TextColor(t.Muted)
			})
			size := s.ChatSize()
			if kit.Button(c.Key("smaller"), kit.ButtonOpts{Variant: kit.Outline, Icon: kit.IconMinus, Height: 34, Round: true,
				Tooltip: "Más pequeño", Aria: "Texto más pequeño", Disabled: size <= app.MinChatFontSize}).Clicked() {
				v.save(func(s *app.Settings) { s.ChatFontSize = max(s.ChatSize()-1, app.MinChatFontSize) })
			}
			ui.Text(c, strconv.FormatFloat(float64(size), 'f', -1, 32)+" pt").FontSize(13.5).Width(52).TextAlign(ui.Center)
			if kit.Button(c.Key("bigger"), kit.ButtonOpts{Variant: kit.Outline, Icon: kit.IconPlus, Height: 34, Round: true,
				Tooltip: "Más grande", Aria: "Texto más grande", Disabled: size >= app.MaxChatFontSize}).Clicked() {
				v.save(func(s *app.Settings) { s.ChatFontSize = min(s.ChatSize()+1, app.MaxChatFontSize) })
			}
		})
		ui.Column(c).Padding(10, 14).Gap(8).Children(func() {
			ui.Text(c, "Tipo de letra").FontSize(14.5)
			// One radio tile per font, its name drawn in that font.
			for i, font := range app.ChatFonts {
				label := app.ChatFontLabels[i]
				if kit.SelectTile(c.Key("chatfont:"+font), label, s.ChatFontOrDefault() == font, true, func() {
					ui.Text(c, label).FontSize(14.5).Font(app.ChatFamily(font))
				}).MinHeight(44).Clicked() {
					v.save(func(s *app.Settings) { s.ChatFont = font })
				}
			}
		})
		ui.Column(c).Padding(10, 14).Gap(8).Children(func() {
			ui.Text(c, "Interlineado").FontSize(14.5)
			sel := slices.Index(app.ChatLineHeights, s.ChatLineHeightOrDefault())
			if kit.Segmented(c.Key("chatline"), &sel, app.ChatLineLabels, nil) {
				lh := app.ChatLineHeights[sel]
				v.save(func(s *app.Settings) { s.ChatLineHeight = lh })
			}
			ui.Text(c, "Así se verá el texto del agente.\nCon párrafos de varias líneas y el interlineado elegido.").
				FontSize(s.ChatSize()).Font(app.ChatFamily(s.ChatFontOrDefault())).
				LineHeight(app.ChatAgentLineHeight * s.ChatLineScale()).TextColor(t.Secondary)
		})
	})
}

func until(u, now time.Time) string {
	y, m, d := now.Date()
	tomorrow := time.Date(y, m, d+1, 0, 0, 0, 0, now.Location())
	switch {
	case u.Before(tomorrow):
		return "hasta las " + u.Format("15:04")
	case u.Before(tomorrow.AddDate(0, 0, 1)):
		return "hasta mañana"
	}
	return "hasta el " + u.Format("02/01")
}

func group(c *ui.Context, t *kit.Tokens, fn func()) {
	ui.Column(c).Radius(14).Background(t.Surface).Border(1, t.Border).Dividers(1, t.Divider).Children(fn)
}

func (v *View) toggle(c *ui.Context, t *kit.Tokens, key, label, sub string, on bool, set func(s *app.Settings, on bool)) {
	cc := c.Key("t:" + key)
	ui.Row(cc).MinHeight(60).Padding(8, 14).Gap(12).AlignItems(ui.Center).Children(func() {
		val := on
		clicked := false
		ui.Column(cc).Grow(1).Shrink(1).Gap(2).Children(func() {
			clicked = ui.Text(cc, label).FontSize(14.5).Cursor(ui.CursorPointer).Clicked()
			ui.Text(cc, sub).FontSize(12).TextColor(t.Muted)
		})
		if clicked {
			val = !on
			v.save(func(s *app.Settings) { set(s, val) })
		}
		if kit.Toggle(cc.Key("sw"), &val, label) {
			v.save(func(s *app.Settings) { set(s, val) })
		}
	})
}

func link(c *ui.Context, t *kit.Tokens, title, value string) bool {
	b := ui.ButtonBase(c).Label(title+", "+value).MinHeight(52).Padding(8, 14).Gap(8).AlignItems(ui.Center).Cursor(ui.CursorPointer)
	if b.Hovered() {
		b.Background(t.Raised)
	}
	b.Children(func() {
		ui.Text(c, title).FontSize(14.5).Grow(1)
		ui.Text(c, value).FontSize(12.5).TextColor(t.Muted)
		kit.Icon(c, kit.IconChevronR).FontSize(16).TextColor(t.Muted)
	})
	return b.Clicked()
}

func (v *View) showKey() {
	v.keyOpen = true
	a := v.a
	a.Go(func(ctx context.Context) {
		k, err := a.Backend.PublicKey(ctx)
		a.Update(func() {
			if err != nil {
				v.pubKey = "No se pudo leer la clave: " + err.Error()
				return
			}
			v.pubKey = k
		})
	})
}

func (v *View) keyDialog(c *ui.Context, t *kit.Tokens) {
	if !v.keyOpen {
		return
	}
	open := true
	kit.Dialog(c.Key("key"), &open, kit.DialogOpts{Title: "Clave SSH de este equipo", Width: 520,
		Description: "Añádela a ~/.ssh/authorized_keys de los hosts que usan la clave generada.",
		Body: func() {
			k := v.pubKey
			if k == "" {
				k = "Cargando…"
			}
			kit.CodeBox(c, k)
		},
		Footer: func() {
			if kit.Button(c.Key("copy"), kit.ButtonOpts{Variant: kit.Primary, Icon: kit.IconCopy, Label: "Copiar clave pública", Height: 40,
				Disabled: v.pubKey == ""}).Clicked() && v.pubKey != "" {
				c.WriteClipboard(v.pubKey)
				v.a.Toasts.Success("Clave pública copiada")
				open = false
			}
		}})
	if !open {
		v.keyOpen = false
	}
}
