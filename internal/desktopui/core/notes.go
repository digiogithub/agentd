package core

import (
	"context"

	"github.com/digiogithub/agentd/internal/client"
	"github.com/digiogithub/agentd/internal/desktopui/backend"
	"github.com/digiogithub/agentd/internal/proto"
)

// notesState is the backlog of one host monitor (guarded by hostMon.mu,
// part of boardState).
type notesState struct {
	notes  []proto.Note
	digest string
	// err is the last error ("" = fresh); unsupported (an older daemon, or a
	// device the gate refuses) stops asking until the event stream
	// reconnects.
	err         string
	unsupported bool
}

// refreshNotes fetches the backlog of the host together with a board
// refresh. It never fails the board: any error ends up in NotesErr.
func (m *hostMon) refreshNotes(ctx context.Context) {
	if h, relay := m.b.relayHost(m.id); relay && !h.Allows(backend.CapBacklog) {
		m.mu.Lock()
		m.board.notes = notesState{err: "este dispositivo no puede ver el backlog", unsupported: true}
		m.mu.Unlock()
		return
	}
	m.mu.Lock()
	if m.board.notes.unsupported {
		m.mu.Unlock()
		return
	}
	since := ""
	if m.board.notes.err == "" {
		since = m.board.notes.digest
	}
	m.mu.Unlock()
	var res proto.NotesResult
	err := m.b.withClient(ctx, m.id, func(c *client.Client) error {
		return c.Call(proto.OpNotes, proto.NotesArgs{SinceDigest: since}, &res)
	})
	if m.ctx.Err() != nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	st := &m.board.notes
	switch {
	case err != nil:
		st.err = err.Error()
		// An older daemon (unknown op: invalid) or a relay gate that does not
		// know the op (forbidden).
		if code := client.ErrorCode(err); code == proto.CodeInvalid || code == proto.CodeForbidden {
			st.unsupported = true
			st.notes, st.digest = nil, ""
		}
		m.b.log.Debug("notes", "host", m.id, "err", err)
	case res.NotModified && st.digest != "":
		st.err = ""
	default:
		st.notes, st.digest, st.err = res.Notes, res.Digest, ""
	}
}

// notesLocked returns the board fields of the backlog. Caller holds m.mu.
func (m *hostMon) notesLocked() ([]proto.Note, string) {
	if m.board.unsupported && m.board.notes.err == "" {
		return nil, "el daemon no soporta el backlog"
	}
	return m.board.notes.notes, m.board.notes.err
}

func (b *Backend) noteGate(hostID string) error {
	if h, relay := b.relayHost(hostID); relay && !h.Allows(backend.CapBacklog) {
		return proto.Errorf(proto.CodeForbidden, "this device has no backlog on %s", h.Name)
	}
	return nil
}

// notesChanged asks the host's board (and so its backlog) to refresh.
func (b *Backend) notesChanged(hostID string) {
	b.mu.Lock()
	mon := b.mons[hostID]
	b.mu.Unlock()
	if mon != nil {
		kick(mon.boardK)
	}
}

// NoteAdd implements backend.Backend (op note_add).
func (b *Backend) NoteAdd(ctx context.Context, hostID, title, body string) (*proto.Note, error) {
	if err := b.noteGate(hostID); err != nil {
		return nil, err
	}
	var n proto.Note
	err := b.withClient(ctx, hostID, func(c *client.Client) error {
		return c.Call(proto.OpNoteAdd, proto.NoteAddArgs{Title: title, Body: body}, &n)
	})
	if err != nil {
		return nil, err
	}
	b.notesChanged(hostID)
	return &n, nil
}

// NoteUpdate implements backend.Backend (op note_update).
func (b *Backend) NoteUpdate(ctx context.Context, hostID string, args proto.NoteUpdateArgs) (*proto.Note, error) {
	if err := b.noteGate(hostID); err != nil {
		return nil, err
	}
	var n proto.Note
	err := b.withClient(ctx, hostID, func(c *client.Client) error {
		return c.Call(proto.OpNoteUpdate, args, &n)
	})
	if err != nil {
		return nil, err
	}
	b.notesChanged(hostID)
	return &n, nil
}

// NoteRemove implements backend.Backend (op note_rm).
func (b *Backend) NoteRemove(ctx context.Context, hostID string, id int64) error {
	if err := b.noteGate(hostID); err != nil {
		return err
	}
	var n proto.Note
	err := b.withClient(ctx, hostID, func(c *client.Client) error {
		return c.Call(proto.OpNoteRm, proto.NoteIDArgs{ID: id}, &n)
	})
	if err != nil {
		return err
	}
	b.notesChanged(hostID)
	return nil
}

// NoteAssign implements backend.Backend (op note_assign).
func (b *Backend) NoteAssign(ctx context.Context, hostID string, args proto.NoteAssignArgs) (*proto.NoteAssignResult, error) {
	if err := b.noteGate(hostID); err != nil {
		return nil, err
	}
	var r proto.NoteAssignResult
	err := b.withClient(ctx, hostID, func(c *client.Client) error {
		return c.Call(proto.OpNoteAssign, args, &r)
	})
	if err != nil {
		return nil, err
	}
	b.notesChanged(hostID)
	return &r, nil
}
