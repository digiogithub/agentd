package panel

import (
	"context"
	"strconv"
	"strings"
	"time"

	"github.com/digiogithub/agentd/internal/desktopui/app"
	"github.com/digiogithub/agentd/internal/desktopui/backend"
	"github.com/digiogithub/agentd/internal/desktopui/kit"
	"github.com/digiogithub/agentd/internal/proto"
	"github.com/egoist/mygo/ui"
)

// noteForm is the add / edit dialog.
type noteForm struct {
	open  bool
	busy  bool
	host  string
	id    int64 // 0 = a new note
	title string
	body  string
}

// noteDelete is the confirmation of a delete.
type noteDelete struct {
	open bool
	busy bool
	host string
	note proto.Note
}

// noteAssign is the "Asignar" dialog.
type noteAssign struct {
	open    bool
	busy    bool
	host    string
	note    proto.Note
	session string
	started int // 0 Pendiente, 1 En curso
}

// backlogUI is the state of the Backlog section (UI thread only).
type backlogUI struct {
	form noteForm
	del  noteDelete
	asg  noteAssign
	// busy marks notes with a request in flight (Devolver al backlog).
	busy map[string]bool
	// hostIdx is the host chosen in the add dialog.
	hostIdx int
}

func noteKey(host string, id int64) string { return host + ":" + strconv.FormatInt(id, 10) }

// backlogSection draws "Backlog": the notes of the hosts in the filter,
// nothing when no host serves notes.
func (v *View) backlogSection(c *ui.Context, t *kit.Tokens, now time.Time) {
	hosts := Backlog(v.a.Live, v.host)
	if len(hosts) == 0 {
		return
	}
	writable := false
	open, total := 0, 0
	for _, h := range hosts {
		writable = writable || h.Writable
		open += len(h.Open)
		total += h.Total()
	}
	multi := len(hosts) > 1
	ui.Column(c.Key("backlog")).Gap(12).Children(func() {
		c := c.Key("backlog")
		ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
			ui.Column(c).Grow(1).Shrink(1).Children(func() {
				kit.SectionLabel(c, "Backlog · "+app.Plural(open, "abierta", "abiertas"), t.Muted)
			})
			if writable && kit.Button(c.Key("add"), kit.ButtonOpts{Variant: kit.Outline, Label: "Añadir nota", Icon: kit.IconPlus, Height: 34, FontSize: 13,
				Aria: "Añadir nota al backlog"}).Clicked() {
				v.openAdd(hosts)
			}
		})
		if total == 0 {
			ui.Text(c, "Sin notas. Apunta aquí ideas y tareas futuras y asígnalas a un agente cuando toque.").FontSize(13).TextColor(t.Muted)
		}
		for _, h := range hosts {
			hc := c.Key("h:" + h.HostID)
			for _, n := range h.Open {
				v.noteCard(hc.Key(noteKey(h.HostID, n.ID)), t, h, n, multi, now)
			}
			for _, n := range h.Assigned {
				v.noteCard(hc.Key(noteKey(h.HostID, n.ID)), t, h, n, multi, now)
			}
		}
		if done := doneCount(hosts); done > 0 {
			label := "Ver hechas · " + strconv.Itoa(done)
			if v.expanded["notes-done"] {
				label = "Ocultar hechas"
			}
			b := ui.ButtonBase(c.Key("done-toggle")).Label(label).Padding(8).Radius(10).Border(1, t.Border).AlignItems(ui.Center).
				Justify(ui.Center).Cursor(ui.CursorPointer).TextColor(t.Muted)
			b.Children(func() { ui.Text(c, label).FontSize(12) })
			if b.Clicked() {
				v.expanded["notes-done"] = !v.expanded["notes-done"]
			}
			if v.expanded["notes-done"] {
				for _, h := range hosts {
					hc := c.Key("h:" + h.HostID)
					for _, n := range h.Done {
						v.noteCard(hc.Key(noteKey(h.HostID, n.ID)), t, h, n, multi, now)
					}
				}
			}
		}
	})
}

func doneCount(hs []HostNotes) int {
	n := 0
	for _, h := range hs {
		n += len(h.Done)
	}
	return n
}

func (v *View) noteCard(c *ui.Context, t *kit.Tokens, h HostNotes, n proto.Note, multi bool, now time.Time) {
	key := noteKey(h.HostID, n.ID)
	dim := n.State == proto.NoteDone
	title := t.Text
	if dim {
		title = t.Muted
	}
	ui.Column(c).Gap(6).Padding(12, 14).Radius(12).Background(t.Surface).Border(1, t.Border).Label("Nota: " + n.Title).Children(func() {
		ui.Row(c).Gap(8).AlignItems(ui.Start).Children(func() {
			ui.Text(c, n.Title).FontSize(14).FontWeight(500).LineHeight(1.4).TextColor(title).Grow(1).Shrink(1).MinWidth(0)
			if multi {
				ui.Text(c, v.hostLabel(h.HostID)).FontSize(12).TextColor(t.Muted).SingleLine()
			}
		})
		if n.Body != "" {
			ui.Text(c, n.Body).FontSize(12.5).LineHeight(1.45).TextColor(t.Secondary).MaxLines(3).Ellipsis("…")
		}
		switch n.State {
		case proto.NoteAssigned, proto.NoteDone:
			line := "→ " + n.AssignedTo
			if st := NoteTaskStatus(v.a.Live, h.HostID, n); st != "" && n.State == proto.NoteAssigned {
				line += " · " + strings.ToLower(TaskStateLabel(st))
			} else if n.State == proto.NoteDone {
				line += " · hecha"
			}
			if n.AssignedAt != nil {
				if rt := app.RelativeTime(*n.AssignedAt, now); rt != "" {
					line += " · " + rt
				}
			}
			ui.Text(c, line).Font(kit.MonoFamily()).FontSize(12).TextColor(t.Working)
		default:
			if rt := app.RelativeTime(n.CreatedAt, now); rt != "" {
				ui.Text(c, rt).FontSize(11.5).TextColor(t.Muted)
			}
		}
		if !h.Writable {
			return
		}
		ui.Row(c).Gap(6).Wrap().GapY(6).Children(func() {
			small := func(id, label, aria string) bool {
				return kit.Button(c.Key(id), kit.ButtonOpts{Variant: kit.Outline, Label: label, Height: 30, FontSize: 12.5,
					Aria: aria + " «" + n.Title + "»"}).Clicked()
			}
			if n.State == proto.NoteOpen && small("assign", "Asignar", "Asignar") {
				v.openAssign(h.HostID, n)
			}
			if n.State != proto.NoteOpen && !v.bl.busy[key] && small("reopen", "Devolver al backlog", "Devolver al backlog") {
				v.reopen(h.HostID, n)
			}
			if n.State != proto.NoteDone && small("edit", "Editar", "Editar") {
				v.openEdit(h.HostID, n)
			}
			if small("delete", "Eliminar", "Eliminar") {
				v.bl.del = noteDelete{open: true, host: h.HostID, note: n}
			}
		})
	})
}

// writableHosts are the hosts of the filter where notes may be written.
func writableHosts(hosts []HostNotes) []string {
	var out []string
	for _, h := range hosts {
		if h.Writable {
			out = append(out, h.HostID)
		}
	}
	return out
}

func (v *View) openAdd(hosts []HostNotes) {
	ids := writableHosts(hosts)
	if len(ids) == 0 {
		return
	}
	host := ids[0]
	for _, id := range ids { // the local host by default
		if id == backend.LocalHostID {
			host = id
		}
	}
	if v.host != "" {
		host = v.host
	}
	v.bl.form = noteForm{open: true, host: host}
	v.bl.hostIdx = 0
	for i, id := range ids {
		if id == host {
			v.bl.hostIdx = i
		}
	}
}

func (v *View) openEdit(host string, n proto.Note) {
	v.bl.form = noteForm{open: true, host: host, id: n.ID, title: n.Title, body: n.Body}
}

func (v *View) openAssign(host string, n proto.Note) {
	v.bl.asg = noteAssign{open: true, host: host, note: n}
	if ts := AssignTargets(v.a.Live, host); len(ts) == 1 {
		v.bl.asg.session = ts[0].Name
	}
}

// backlogDialogs draws the dialogs of the section (every frame).
func (v *View) backlogDialogs(c *ui.Context, t *kit.Tokens) {
	v.noteFormDialog(c, t)
	v.noteDeleteDialog(c)
	v.noteAssignDialog(c, t)
}

func (v *View) noteFormDialog(c *ui.Context, t *kit.Tokens) {
	f := &v.bl.form
	if !f.open {
		return
	}
	hosts := Backlog(v.a.Live, v.host)
	ids := writableHosts(hosts)
	heading := "Nueva nota"
	if f.id != 0 {
		heading = "Editar nota"
	}
	open := f.open
	kit.Dialog(c.Key("note-form"), &open, kit.DialogOpts{
		Title: heading, Width: 520,
		Body: func() {
			if f.id == 0 && len(ids) > 1 {
				names := make([]string, len(ids))
				for i, id := range ids {
					names[i] = v.hostLabel(id)
				}
				kit.Field(c, "Host", func() {
					if kit.Segmented(c.Key("note-host"), &v.bl.hostIdx, names, nil) {
						f.host = ids[v.bl.hostIdx]
					}
				})
			}
			kit.Field(c, "Título", func() {
				kit.Input(c.Key("note-title"), &f.title, "Qué hay que hacer", false).Label("Título de la nota")
			})
			kit.Field(c, "Detalle (opcional)", func() {
				ui.TextArea(c.Key("note-body"), &f.body).Lines(3, 8).Placeholder("Contexto, enlaces, criterios…").Label("Detalle de la nota").
					Padding(10, 12).Background(t.Bg).Border(1, t.Border).Radius(12).FontSize(14)
			})
		},
		Footer: func() {
			if kit.Button(c.Key("note-cancel"), kit.ButtonOpts{Variant: kit.Outline, Label: "Cancelar", Height: 36}).Clicked() {
				open = false
			}
			label := "Añadir"
			if f.id != 0 {
				label = "Guardar"
			}
			if kit.Button(c.Key("note-save"), kit.ButtonOpts{Variant: kit.Primary, Label: label, Height: 36,
				Disabled: f.busy || strings.TrimSpace(f.title) == "", Aria: "Guardar nota"}).Clicked() && !f.busy && strings.TrimSpace(f.title) != "" {
				v.saveNote()
			}
		},
	})
	if !open && !f.busy {
		f.open = false
	}
}

func (v *View) saveNote() {
	a := v.a
	f := &v.bl.form
	f.busy = true
	host, id, title, body := f.host, f.id, strings.TrimSpace(f.title), f.body
	a.Go(func(ctx context.Context) {
		var err error
		if id == 0 {
			_, err = a.Backend.NoteAdd(ctx, host, title, body)
		} else {
			_, err = a.Backend.NoteUpdate(ctx, host, proto.NoteUpdateArgs{ID: id, Title: &title, Body: &body})
		}
		a.Update(func() {
			f.busy = false
			if err != nil {
				a.Toasts.Error("No se pudo guardar la nota", app.ErrorText(err))
				return
			}
			*f = noteForm{}
			a.Toasts.Success("Nota guardada")
		})
	})
}

func (v *View) noteDeleteDialog(c *ui.Context) {
	d := &v.bl.del
	if !d.open {
		return
	}
	open := d.open
	kit.Dialog(c.Key("note-delete"), &open, kit.DialogOpts{
		Title:       "Eliminar la nota?",
		Description: "«" + oneLine(d.note.Title, 80) + "» se borra del backlog. Si ya es una tarea de un agente, la tarea se queda.",
		Width:       460,
		Footer: func() {
			if kit.Button(c.Key("del-cancel"), kit.ButtonOpts{Variant: kit.Outline, Label: "Cancelar", Height: 36}).Clicked() {
				open = false
			}
			if kit.Button(c.Key("del-confirm"), kit.ButtonOpts{Variant: kit.Danger, Label: "Eliminar", Height: 36, Disabled: d.busy,
				Aria: "Confirmar eliminar nota"}).Clicked() && !d.busy {
				v.removeNote()
			}
		},
	})
	if !open && !d.busy {
		d.open = false
	}
}

func (v *View) removeNote() {
	a := v.a
	d := &v.bl.del
	d.busy = true
	host, id := d.host, d.note.ID
	a.Go(func(ctx context.Context) {
		err := a.Backend.NoteRemove(ctx, host, id)
		a.Update(func() {
			d.busy = false
			if err != nil {
				a.Toasts.Error("No se pudo eliminar la nota", app.ErrorText(err))
				return
			}
			*d = noteDelete{}
			a.Toasts.Info("Nota eliminada")
		})
	})
}

func (v *View) reopen(host string, n proto.Note) {
	a := v.a
	key := noteKey(host, n.ID)
	if v.bl.busy == nil {
		v.bl.busy = map[string]bool{}
	}
	v.bl.busy[key] = true
	open := proto.NoteOpen
	a.Go(func(ctx context.Context) {
		_, err := a.Backend.NoteUpdate(ctx, host, proto.NoteUpdateArgs{ID: n.ID, State: &open})
		a.Update(func() {
			delete(v.bl.busy, key)
			if err != nil {
				a.Toasts.Error("No se pudo devolver la nota", app.ErrorText(err))
				return
			}
			a.Toasts.Info("Nota devuelta al backlog")
		})
	})
}

func (v *View) noteAssignDialog(c *ui.Context, t *kit.Tokens) {
	d := &v.bl.asg
	if !d.open {
		return
	}
	targets := AssignTargets(v.a.Live, d.host)
	open := d.open
	kit.Dialog(c.Key("note-assign"), &open, kit.DialogOpts{
		Title:       "Asignar a un agente",
		Description: "«" + oneLine(d.note.Title, 80) + "» pasa a la lista de tareas del agente y se le avisa por su buzón.",
		Width:       500,
		Body: func() {
			if len(targets) == 0 {
				ui.Text(c, "No hay agentes en marcha en este host.").FontSize(13).TextColor(t.Muted)
				return
			}
			kit.Field(c, "Agente", func() {
				label := d.session
				if label == "" {
					label = "Elige un agente"
				}
				b := ui.ButtonBase(c.Key("asg-session")).Label("Agente").Height(40).Padding(0, 12).Gap(6).Radius(12).Background(t.Bg).
					Border(1, t.Border).AlignItems(ui.Center).Cursor(ui.CursorPointer).TextColor(t.Text)
				b.Children(func() {
					ui.Text(c, label).Font(kit.MonoFamily()).FontSize(13).SingleLine().Grow(1)
					kit.Icon(c, kit.IconChevronDown).FontSize(14).TextColor(t.Muted)
				})
				b.Menu(func(mn *ui.Menu) {
					for _, s := range targets {
						if mn.Item(s.Name).Checked(d.session == s.Name).Chosen() {
							d.session = s.Name
						}
					}
				})
			})
			kit.Field(c, "Estado de la tarea", func() {
				ui.Column(c).Width(240).AlignItems(ui.Stretch).Children(func() {
					kit.Segmented(c.Key("asg-status"), &d.started, []string{"Pendiente", "En curso"}, nil)
				})
			})
		},
		Footer: func() {
			if kit.Button(c.Key("asg-cancel"), kit.ButtonOpts{Variant: kit.Outline, Label: "Cancelar", Height: 36}).Clicked() {
				open = false
			}
			ok := d.session != "" && !d.busy
			if kit.Button(c.Key("asg-confirm"), kit.ButtonOpts{Variant: kit.Primary, Label: "Asignar", Height: 36, Disabled: !ok,
				Aria: "Confirmar asignar nota"}).Clicked() && ok {
				v.assignNote()
			}
		},
	})
	if !open && !d.busy {
		d.open = false
	}
}

func (v *View) assignNote() {
	a := v.a
	d := &v.bl.asg
	d.busy = true
	args := proto.NoteAssignArgs{ID: d.note.ID, Session: d.session, Status: proto.TaskPending}
	if d.started == 1 {
		args.Status = proto.TaskInProgress
	}
	host := d.host
	a.Go(func(ctx context.Context) {
		res, err := a.Backend.NoteAssign(ctx, host, args)
		a.Update(func() {
			d.busy = false
			if err != nil {
				a.Toasts.Error("No se pudo asignar la nota", app.ErrorText(err))
				return
			}
			*d = noteAssign{}
			if res != nil && res.Warning != "" {
				a.Toasts.Info("Asignada a " + args.Session + " (" + res.Warning + ")")
				return
			}
			a.Toasts.Success("Asignada a " + args.Session)
		})
	})
}
