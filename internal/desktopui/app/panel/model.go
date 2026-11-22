package panel

import (
	"sort"
	"time"

	"github.com/digiogithub/agentd/internal/desktopui/app"
	"github.com/digiogithub/agentd/internal/desktopui/backend"
	"github.com/digiogithub/agentd/internal/proto"
)

// Agent is one agentd session of the Panel: its live session and, when its
// host's daemon has op board, its board entry (tasks, open questions).
type Agent struct {
	Session backend.Session
	// Board is nil when the host has no board (an older daemon, or the
	// first board has not arrived yet): the card falls back to status and
	// preview.
	Board *proto.BoardSession
}

// Status is the live status (the session list), else the board's.
func (ag Agent) Status() string {
	if ag.Session.Status != "" {
		return ag.Session.Status
	}
	if ag.Board != nil {
		return ag.Board.Status
	}
	return "unknown"
}

// Tasks returns the agent's task list (nil without a board).
func (ag Agent) Tasks() []proto.Task {
	if ag.Board == nil {
		return nil
	}
	return ag.Board.Tasks
}

// Questions returns the open questions the agent asked.
func (ag Agent) Questions() []proto.BoardQuestion {
	if ag.Board == nil {
		return nil
	}
	return ag.Board.Questions
}

// Kinds of attention items, in the order the Panel lists them.
const (
	ItemQuestion = iota // an open question (mailbox)
	ItemMenu            // a waiting session (terminal menu or prompt)
	ItemBlocked         // a blocked task
)

// Item is one entry of "Necesita tu atención".
type Item struct {
	Kind     int
	Agent    Agent
	Question proto.BoardQuestion // ItemQuestion
	Task     proto.Task          // ItemBlocked
	Since    time.Time
}

// Key identifies an item across frames.
func (it Item) Key() string {
	switch it.Kind {
	case ItemQuestion:
		return "q:" + it.Agent.Session.Key() + ":" + itoa64(it.Question.ID)
	case ItemBlocked:
		return "b:" + it.Agent.Session.Key() + ":" + it.Task.ID
	}
	return "m:" + it.Agent.Session.Key()
}

// Model is what the Panel shows for a host filter.
type Model struct {
	Agents []Agent
	Items  []Item
	// Hosts counts the hosts with agents listed.
	Hosts int
	// Updated is the newest board refresh.
	Updated time.Time
}

// Agents collects the agentd sessions of the live list (hostID "" = every
// host) joined with their board entries, sorted by host then name. Exited
// sessions are left out unless they still report tasks or questions.
func Agents(l *app.Live, hostID string) []Agent {
	type key struct{ host, name string }
	boards := map[key]*proto.BoardSession{}
	for i := range l.Boards {
		hb := &l.Boards[i]
		if hostID != "" && hb.HostID != hostID {
			continue
		}
		for j := range hb.Board.Sessions {
			bs := &hb.Board.Sessions[j]
			boards[key{hb.HostID, bs.Name}] = bs
		}
	}
	seen := map[key]bool{}
	var out []Agent
	for _, s := range l.Sessions {
		if s.Source != backend.SourceAgentd || hostID != "" && s.HostID != hostID {
			continue
		}
		k := key{s.HostID, s.Name}
		seen[k] = true
		ag := Agent{Session: s, Board: boards[k]}
		if s.Status == "exited" && len(ag.Tasks()) == 0 && len(ag.Questions()) == 0 {
			continue
		}
		out = append(out, ag)
	}
	// Board entries the session list does not carry yet (a race between the
	// two streams).
	for k, bs := range boards {
		if seen[k] || bs.Status == "exited" && len(bs.Tasks) == 0 && len(bs.Questions) == 0 {
			continue
		}
		out = append(out, Agent{Session: backend.Session{HostID: k.host, Source: backend.SourceAgentd, Name: bs.Name,
			Status: bs.Status, Attention: bs.Attention, Preview: bs.Preview}, Board: bs})
	}
	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i].Session, out[j].Session
		if a.HostID != b.HostID {
			return a.HostID < b.HostID
		}
		return a.Name < b.Name
	})
	return out
}

// Collect builds the Panel model for a host filter.
func Collect(l *app.Live, hostID string) Model {
	m := Model{Agents: Agents(l, hostID)}
	hosts := map[string]bool{}
	for _, ag := range m.Agents {
		hosts[ag.Session.HostID] = true
		for _, q := range ag.Questions() {
			m.Items = append(m.Items, Item{Kind: ItemQuestion, Agent: ag, Question: q, Since: q.CreatedAt})
		}
		if ag.Status() == "waiting" {
			m.Items = append(m.Items, Item{Kind: ItemMenu, Agent: ag, Since: ag.Session.Updated})
		}
		for _, t := range ag.Tasks() {
			if t.Status == proto.TaskBlocked {
				m.Items = append(m.Items, Item{Kind: ItemBlocked, Agent: ag, Task: t, Since: t.UpdatedAt})
			}
		}
	}
	sort.SliceStable(m.Items, func(i, j int) bool { return m.Items[i].Kind < m.Items[j].Kind })
	m.Hosts = len(hosts)
	for _, hb := range l.Boards {
		if (hostID == "" || hb.HostID == hostID) && hb.Updated.After(m.Updated) {
			m.Updated = hb.Updated
		}
	}
	return m
}

// AttentionCount is the sidebar badge: open questions, waiting sessions and
// blocked tasks of every host.
func AttentionCount(a *app.Context) int { return len(Collect(a.Live, "").Items) }

// Counts are the summary tiles.
type Counts struct {
	InProgress, Pending, DoneToday int
}

// CountTasks counts the tasks of agents; "today" is now's local day.
func CountTasks(agents []Agent, now time.Time) Counts {
	var c Counts
	for _, ag := range agents {
		for _, t := range ag.Tasks() {
			switch t.Status {
			case proto.TaskInProgress:
				c.InProgress++
			case proto.TaskPending:
				c.Pending++
			case proto.TaskDone:
				if SameDay(t.UpdatedAt, now) {
					c.DoneToday++
				}
			}
		}
	}
	return c
}

// SameDay reports whether t falls on now's local calendar day.
func SameDay(t, now time.Time) bool {
	if t.IsZero() {
		return false
	}
	y1, m1, d1 := t.In(now.Location()).Date()
	y2, m2, d2 := now.Date()
	return y1 == y2 && m1 == m2 && d1 == d2
}

// Progress is done / (total - cancelled) of a task list.
func Progress(ts []proto.Task) (done, total int) {
	for _, t := range ts {
		switch t.Status {
		case proto.TaskCancelled:
			continue
		case proto.TaskDone:
			done++
		}
		total++
	}
	return done, total
}

// Column is one Tablero column.
type Column struct {
	Status string
	Title  string
	Cards  []Card
}

// Card is one task of the Tablero.
type Card struct {
	Agent Agent
	Task  proto.Task
	// Next marks the first pending task of its agent ("siguiente").
	Next bool
}

// Board builds the four Tablero columns: Pendiente, En curso, Bloqueada and
// Hecha hoy (done tasks updated on now's local day, newest first).
func Board(agents []Agent, now time.Time) []Column {
	cols := []Column{
		{Status: proto.TaskPending, Title: "Pendiente"},
		{Status: proto.TaskInProgress, Title: "En curso"},
		{Status: proto.TaskBlocked, Title: "Bloqueada"},
		{Status: proto.TaskDone, Title: "Hecha hoy"},
	}
	for _, ag := range agents {
		first := true
		for _, t := range ag.Tasks() {
			switch t.Status {
			case proto.TaskPending:
				cols[0].Cards = append(cols[0].Cards, Card{Agent: ag, Task: t, Next: first})
				first = false
			case proto.TaskInProgress:
				cols[1].Cards = append(cols[1].Cards, Card{Agent: ag, Task: t})
			case proto.TaskBlocked:
				cols[2].Cards = append(cols[2].Cards, Card{Agent: ag, Task: t})
			case proto.TaskDone:
				if SameDay(t.UpdatedAt, now) {
					cols[3].Cards = append(cols[3].Cards, Card{Agent: ag, Task: t})
				}
			}
		}
	}
	for i := 1; i < len(cols); i++ {
		cs := cols[i].Cards
		sort.SliceStable(cs, func(a, b int) bool { return cs[a].Task.UpdatedAt.After(cs[b].Task.UpdatedAt) })
	}
	return cols
}
