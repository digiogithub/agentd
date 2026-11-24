package fake

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/digiogithub/agentd/internal/desktopui/backend"
	"github.com/digiogithub/agentd/internal/proto"
)

// ReplyCall is one recorded Reply.
type ReplyCall struct {
	HostID string
	ID     int64
	Body   string
	Notify bool
}

// QuestionID is the id of the seeded open question (pando@agentd asks
// whether to migrate the mailbox schema in the same change).
const QuestionID int64 = 160

// seedBoard gives the agentd sessions the task lists and the open question
// of the Panel mockups (f.mu held or not yet shared).
func (f *Fake) seedBoard(now time.Time) {
	ago := func(d time.Duration) time.Time { return now.Add(-d) }
	task := func(id, title, status, note string, at time.Time) proto.Task {
		return proto.Task{ID: id, Title: title, Status: status, Note: note, UpdatedAt: at}
	}
	tasks := map[string][]proto.Task{
		"pando@agentd": {
			task("t1", "Modelo de tareas y validación", proto.TaskDone, "", ago(35*time.Minute)),
			task("t2", "Ops tasks_set y task_update", proto.TaskDone, "", ago(50*time.Minute)),
			task("t3", "Contrato del panel en AGENTS.md", proto.TaskDone, "", ago(70*time.Minute)),
			task("t4", "Op board: resumen agregado por sesión", proto.TaskInProgress, "", ago(time.Minute)),
			task("t5", "Persistir tareas en SQLite", proto.TaskInProgress, "Pregunta abierta: ¿esquema v4 en este cambio?", ago(4*time.Minute)),
			task("t6", "Eventos task.changed en el bus", proto.TaskPending, "", ago(70*time.Minute)),
			task("t7", "Herramientas MCP para informar tareas", proto.TaskPending, "", ago(70*time.Minute)),
		},
		"claude@webapp": {
			task("t1", "Reproducir el fallo de carrera", proto.TaskDone, "", ago(25*time.Minute)),
			task("t2", "Copia de seguridad del estado", proto.TaskDone, "", ago(40*time.Minute)),
			task("t3", "Pasar los tests del daemon con -race", proto.TaskInProgress, "Espera permiso para go test", ago(time.Minute)),
			task("t4", "Arreglar las carreras que aparezcan", proto.TaskPending, "", ago(40*time.Minute)),
		},
		"codex@api": {
			task("t1", "Añadir expires_at al modelo", proto.TaskDone, "", ago(10*time.Minute)),
			task("t2", "Tests de expiración", proto.TaskDone, "", ago(6*time.Minute)),
			task("t3", "Migración de la tabla sessions", proto.TaskInProgress, "Espera tu decisión en el terminal", ago(3*time.Minute)),
			task("t4", "Actualizar los datos de prueba", proto.TaskPending, "", ago(10*time.Minute)),
		},
		"claude@docs": {
			task("t1", "Sección de colas en el README", proto.TaskDone, "", ago(20*time.Minute)),
			task("t2", "Ejemplo de agentd claim", proto.TaskDone, "", ago(22*time.Minute)),
			task("t3", "Revisar enlaces rotos", proto.TaskDone, "", ago(30*time.Minute)),
			task("t4", "Índice de la documentación", proto.TaskDone, "", ago(26*time.Hour)),
		},
		"codex@webapp": {
			task("t1", "Relay arrancado con TLS", proto.TaskDone, "", ago(time.Hour)),
			task("t2", "Alta del dispositivo de pruebas", proto.TaskDone, "", ago(50*time.Minute)),
			task("t3", "Prueba de campo del relay en el VPS", proto.TaskBlocked, "Falta el token de alta del VPS", ago(22*time.Minute)),
			task("t4", "Documentar el despliegue", proto.TaskPending, "", ago(time.Hour)),
		},
	}
	for _, r := range f.sessions {
		if ts, ok := tasks[r.s.Name]; ok {
			r.tasks = ts
		}
		if r.s.Name == "pando@agentd" {
			r.questions = []proto.BoardQuestion{{ID: QuestionID,
				Body:    "¿Migro el esquema del buzón a v4 en este mismo cambio o lo separo en otro?",
				Options: []string{"En este cambio", "Cambio aparte"}, Task: "t5", CreatedAt: ago(4 * time.Minute)}}
		}
	}
}

// boardSnapshot builds the boards of the online hosts with agentd (f.mu
// held).
func (f *Fake) boardSnapshot() []backend.HostBoard {
	out := []backend.HostBoard{}
	now := time.Now()
	for _, h := range f.hosts {
		if h.st.State != backend.HostOnline || h.st.AgentdVer == "" && (h.st.Capabilities == nil || !h.st.Capabilities.Has("agentd")) {
			continue
		}
		owner := h.st.Host.Relay == nil || h.st.Host.Relay.Kind != "guest"
		b := proto.BoardResult{Sessions: []proto.BoardSession{}}
		var sig strings.Builder
		for _, r := range f.sessions {
			if r.s.HostID != h.st.Host.ID || r.s.Source != backend.SourceAgentd {
				continue
			}
			bs := proto.BoardSession{Name: r.s.Name, Status: r.s.Status, Attention: r.s.Attention, Preview: r.s.Preview,
				Tasks: slices.Clone(r.tasks), Questions: []proto.BoardQuestion{}}
			if bs.Tasks == nil {
				bs.Tasks = []proto.Task{}
			}
			if r.s.Info != nil {
				bs.Turn = r.s.Info.Turn
			}
			if owner {
				for _, q := range r.questions {
					q.Options = slices.Clone(q.Options)
					bs.Questions = append(bs.Questions, q)
				}
			}
			b.Sessions = append(b.Sessions, bs)
			fmt.Fprintf(&sig, "%s:%s:%d:%d;", bs.Name, bs.Status, len(bs.Tasks), len(bs.Questions))
		}
		b.Digest = fmt.Sprintf("d%x", len(sig.String()))
		hb := backend.HostBoard{HostID: h.st.Host.ID, Board: b, Updated: now}
		hb.Notes, hb.NotesErr = f.notesBoard(h.st.Host)
		out = append(out, hb)
	}
	slices.SortFunc(out, func(a, b backend.HostBoard) int { return strings.Compare(a.HostID, b.HostID) })
	return out
}

// WatchBoard implements backend.Backend.
func (f *Fake) WatchBoard(ctx context.Context) <-chan []backend.HostBoard {
	f.mu.Lock()
	defer f.mu.Unlock()
	return watch(ctx, f, &f.boardSubs, f.boardSnapshot())
}

// Reply implements backend.Backend: it answers an open question (or any
// message) and closes the question.
func (f *Fake) Reply(ctx context.Context, hostID string, id int64, body string, notify bool) (*proto.Message, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.replies = append(f.replies, ReplyCall{HostID: hostID, ID: id, Body: body, Notify: notify})
	h := f.hostLocked(hostID)
	if h == nil {
		return nil, proto.Errorf(proto.CodeNotFound, "no such host %q", hostID)
	}
	if !h.st.Host.Allows(backend.CapMailbox) {
		return nil, proto.Errorf(proto.CodeForbidden, "no mailbox for this device")
	}
	if strings.TrimSpace(body) == "" {
		return nil, proto.Errorf(proto.CodeInvalid, "empty reply")
	}
	for _, r := range f.sessions {
		if r.s.HostID != hostID {
			continue
		}
		for i, q := range r.questions {
			if q.ID != id {
				continue
			}
			r.questions = slices.Delete(r.questions, i, i+1)
			to := r.s.Name
			m := proto.Message{ID: f.nextMsg, Sender: "human:jose", Recipient: to, Body: body, ReplyTo: &id, State: replyState(notify), CreatedAt: time.Now()}
			f.nextMsg++
			f.messages = append(f.messages, m)
			f.publishLocked()
			return &m, nil
		}
	}
	for _, m := range f.messages {
		if m.ID == id {
			out := proto.Message{ID: f.nextMsg, Sender: "human:jose", Recipient: m.Sender, Body: body, ReplyTo: &id, State: "queued", CreatedAt: time.Now()}
			f.nextMsg++
			f.messages = append(f.messages, out)
			f.publishLocked()
			return &out, nil
		}
	}
	return nil, proto.Errorf(proto.CodeNotFound, "no such message %d", id)
}

// Replies returns the recorded Reply calls.
func (f *Fake) Replies() []ReplyCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.replies)
}

// SetTasks replaces the task list of a session and publishes (tests: a
// task newly blocked).
func (f *Fake) SetTasks(name string, tasks []proto.Task) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, r := range f.sessions {
		if r.s.Name == name {
			r.tasks = slices.Clone(tasks)
		}
	}
	f.publishLocked()
}

// Tasks returns a copy of a session's task list (tests).
func (f *Fake) Tasks(name string) []proto.Task {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, r := range f.sessions {
		if r.s.Name == name {
			return slices.Clone(r.tasks)
		}
	}
	return nil
}

// Ask adds an open question from session name and publishes (tests); it
// returns the question id.
func (f *Fake) Ask(name, body string, options ...string) int64 {
	f.mu.Lock()
	defer f.mu.Unlock()
	id := f.nextMsg
	f.nextMsg++
	for _, r := range f.sessions {
		if r.s.Name == name {
			r.questions = append(r.questions, proto.BoardQuestion{ID: id, Body: body, Options: options, CreatedAt: time.Now()})
		}
	}
	f.publishLocked()
	return id
}

func replyState(notify bool) string {
	if notify {
		return "queued"
	}
	return "delivered"
}
