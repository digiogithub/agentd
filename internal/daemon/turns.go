package daemon

import (
	"time"

	"github.com/digiogithub/agentd/internal/proto"
)

// Turn records: one per time a session's published status leaves working,
// with how the turn ended and who started it. Kept per session in memory
// (MaxTurnRecords), served by op turns and published as turn.completed.

const (
	// MaxTurnRecords is how many turns a session keeps.
	MaxTurnRecords = 100
	// originWindow: the input that started a turn is the latest one at most
	// this long before the status went working.
	originWindow = 60 * time.Second
	// abortWindow: a turn that ends within this long after a stop key
	// (interrupt/cancel act, Ctrl-C or Esc typed) was aborted.
	abortWindow = 15 * time.Second
)

// Input origins of a turn.
const (
	OriginHuman   = "human"   // typed by a person on an attached terminal
	OriginAct     = "act"     // op act (CLI, gateway, hc clients)
	OriginMail    = "mail"    // a delivered mailbox message
	OriginProgram = "program" // nothing recent: the program started on its own
)

// turnTrack is the turn bookkeeping of one session, guarded by entry.mu.
type turnTrack struct {
	lastInput     time.Time
	lastInputKind string
	lastStop      time.Time

	startedAt time.Time // current turn (status went working)
	origin    string
	records   []proto.TurnRecord
}

// noteInput records who wrote to the session last (for a turn's origin).
func (e *entry) noteInput(kind string, stop bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	now := time.Now()
	e.turns.lastInput, e.turns.lastInputKind = now, kind
	if stop {
		e.turns.lastStop = now
	}
}

// turnStarted is called when the published status becomes working.
func (e *entry) turnStarted(now time.Time) {
	e.mu.Lock()
	defer e.mu.Unlock()
	t := &e.turns
	t.startedAt = now
	t.origin = OriginProgram
	if !t.lastInput.IsZero() && now.Sub(t.lastInput) <= originWindow {
		t.origin = t.lastInputKind
	}
}

// turnEnded is called when the published status leaves working; it returns
// the new record.
func (e *entry) turnEnded(turn uint64, status, message string, now time.Time) proto.TurnRecord {
	e.mu.Lock()
	defer e.mu.Unlock()
	t := &e.turns
	r := proto.TurnRecord{Turn: turn, Outcome: "completed", Origin: t.origin, Status: status,
		Message: message, StartedAt: t.startedAt, EndedAt: now}
	// Input noted just after the status went working (a delivery marks the
	// message after injecting it) still started this turn.
	if (r.Origin == "" || r.Origin == OriginProgram) && !t.lastInput.IsZero() && !t.startedAt.IsZero() &&
		t.lastInput.After(t.startedAt.Add(-originWindow)) && t.lastInput.Sub(t.startedAt) < 5*time.Second {
		r.Origin = t.lastInputKind
	}
	if r.Origin == "" {
		r.Origin = OriginProgram
	}
	if status == "exited" {
		r.Outcome = "exited"
	} else if !t.lastStop.IsZero() && !t.lastStop.Before(t.startedAt) && now.Sub(t.lastStop) <= abortWindow {
		r.Outcome = "aborted"
	}
	t.records = append(t.records, r)
	if len(t.records) > MaxTurnRecords {
		t.records = t.records[len(t.records)-MaxTurnRecords:]
	}
	t.startedAt, t.origin = time.Time{}, ""
	return r
}

func (e *entry) turnRecords() []proto.TurnRecord {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]proto.TurnRecord{}, e.turns.records...)
}

// turnsOp serves op turns: same visibility as view (a session sees its own
// turns unless [security] agent_view).
func (s *Server) turnsOp(who caller, a proto.NameArgs) (proto.TurnsResult, error) {
	if who.session && who.id != a.Name && !s.Cfg.Security.AgentView {
		return proto.TurnsResult{}, proto.Errorf(proto.CodeForbidden,
			"turns: session %q may not read session %q (set [security] agent_view = true to allow)", who.id, a.Name)
	}
	e, err := s.lookup(a.Name)
	if err != nil {
		return proto.TurnsResult{}, err
	}
	return proto.TurnsResult{Turns: e.turnRecords()}, nil
}

// isStopInput reports whether human bytes stop the program (Ctrl-C, or a lone
// Esc).
func isStopInput(p []byte) bool {
	for _, b := range p {
		if b == 0x03 {
			return true
		}
	}
	return len(p) == 1 && p[0] == 0x1b
}
