package app

import (
	"fmt"
	"time"

	"github.com/digiogithub/agentd/internal/desktopui/backend"
	"github.com/digiogithub/agentd/internal/proto"
)

// TurnDoneWindow groups "turn done" notifications per session.
const TurnDoneWindow = 30 * time.Second

// Notifier turns session transitions into desktop notifications, as
// Ajustes > Avisarme cuando says: an agent starts waiting, a turn ends
// (at most one per session every 30 s), a session exits, and from the
// boards an agent asks a question or marks a task blocked (a click opens
// the Panel). Muted sessions and a focused window get none. A click opens the session; MyGo notifications
// have no action buttons, so "responder desde el aviso" opens the session
// on its question instead of answering inside the notification.
type Notifier struct {
	a        *Context
	lastDone map[string]time.Time
	// Now is the clock (tests).
	Now func() time.Time
	// Sent records what was notified (tests): "title|body".
	Sent []string
}

// StartNotifier wires the notifier to the live sessions.
func StartNotifier(a *Context) *Notifier {
	n := &Notifier{a: a, lastDone: map[string]time.Time{}, Now: time.Now}
	a.Live.OnSessions = append(a.Live.OnSessions, n.onSessions)
	a.Live.OnBoards = append(a.Live.OnBoards, n.onBoards)
	return n
}

// PanelPath is the route of the Panel area.
const PanelPath = "/panel"

// BoardSessionKey is the Session.Key() of an agentd session of a board.
func BoardSessionKey(hostID, name string) string {
	return hostID + "/" + string(backend.SourceAgentd) + "/" + name
}

// onBoards notifies new open questions and tasks newly blocked. A host's
// first board (and a host that comes back) only sets the baseline.
func (n *Notifier) onBoards(prev, next []backend.HostBoard) {
	if prev == nil {
		return
	}
	old := map[string]backend.HostBoard{}
	for _, b := range prev {
		old[b.HostID] = b
	}
	set := n.a.Prefs.Get()
	now := n.Now()
	for _, b := range next {
		p, ok := old[b.HostID]
		if !ok || p.Board.Sessions == nil && p.Err != "" {
			continue
		}
		before := map[string]proto.BoardSession{}
		for _, s := range p.Board.Sessions {
			before[s.Name] = s
		}
		host := n.a.Live.HostName(b.HostID)
		for _, s := range b.Board.Sessions {
			key := BoardSessionKey(b.HostID, s.Name)
			if set.IsMuted(key, now) {
				continue
			}
			ps, known := before[s.Name]
			if set.NotifyQuestion {
				asked := map[int64]bool{}
				for _, q := range ps.Questions {
					asked[q.ID] = true
				}
				for _, q := range s.Questions {
					if !asked[q.ID] {
						n.sendPanel(key, withHost(s.Name+" pregunta", host), q.Body)
					}
				}
			}
			if set.NotifyBlocked && known {
				was := map[string]string{}
				for _, t := range ps.Tasks {
					was[t.ID] = t.Status
				}
				for _, t := range s.Tasks {
					if t.Status == proto.TaskBlocked && was[t.ID] != proto.TaskBlocked {
						body := t.Title
						if t.Note != "" {
							body += " · " + t.Note
						}
						n.sendPanel(key, withHost(s.Name+" · tarea bloqueada", host), body)
					}
				}
			}
		}
	}
}

func withHost(title, host string) string {
	if host != "" {
		return title + " · " + host
	}
	return title
}

// sendPanel sends a notification whose click opens the Panel.
func (n *Notifier) sendPanel(key, title, body string) {
	sh := n.a.ShellHost()
	if sh != nil && sh.Focused() {
		return
	}
	n.Sent = append(n.Sent, title+"|"+body)
	if sh == nil {
		return
	}
	sh.Notify("agentd-panel:"+key, title, body, func() {
		n.a.Update(func() { n.a.Navigate(PanelPath) })
	})
}

func (n *Notifier) onSessions(prev, next []backend.Session) {
	if prev == nil {
		return // first snapshot: nothing changed yet
	}
	old := map[string]backend.Session{}
	for _, s := range prev {
		old[s.Key()] = s
	}
	set := n.a.Prefs.Get()
	now := n.Now()
	for _, s := range next {
		p, ok := old[s.Key()]
		if !ok || p.Status == s.Status || set.IsMuted(s.Key(), now) {
			continue
		}
		title := SessionTitle(s)
		if host := n.a.Live.HostName(s.HostID); host != "" {
			title += " · " + host
		}
		switch {
		case s.Status == "waiting" && set.NotifyWaiting:
			body := "Espera tu respuesta"
			if s.Preview != "" {
				body = s.Preview
			}
			n.send(s, title, body)
		case p.Status == "working" && s.Status == "idle" && set.NotifyTurnDone:
			if t, ok := n.lastDone[s.Key()]; ok && now.Sub(t) < TurnDoneWindow {
				continue
			}
			n.lastDone[s.Key()] = now
			body := "Turno terminado"
			if s.Preview != "" {
				body += " · «" + s.Preview + "»"
			}
			n.send(s, title, body)
		case s.Status == "exited" && set.NotifyExit:
			body := "Sesión terminada"
			if s.Info != nil && s.Info.ExitCode != nil {
				body = fmt.Sprintf("Sesión terminada · código %d", *s.Info.ExitCode)
			}
			n.send(s, title, body)
		}
	}
}

func (n *Notifier) send(s backend.Session, title, body string) {
	sh := n.a.ShellHost()
	if sh != nil && sh.Focused() {
		return
	}
	n.Sent = append(n.Sent, title+"|"+body)
	if sh == nil {
		return
	}
	path := SessionPath(s)
	sh.Notify("agentd:"+s.Key(), title, body, func() {
		n.a.Update(func() { n.a.Navigate(path) })
	})
}
