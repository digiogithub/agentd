package core_test

import (
	"context"
	"testing"
	"time"

	"github.com/digiogithub/agentd/internal/desktopui/backend"
	"github.com/digiogithub/agentd/internal/proto"
)

func waitBoards(t *testing.T, ch <-chan []backend.HostBoard, what string, cond func([]backend.HostBoard) bool) []backend.HostBoard {
	t.Helper()
	deadline := time.After(10 * time.Second)
	var last []backend.HostBoard
	for {
		select {
		case bs, ok := <-ch:
			if !ok {
				t.Fatalf("%s: board channel closed", what)
			}
			last = bs
			if cond(bs) {
				return bs
			}
		case <-deadline:
			t.Fatalf("%s: timed out; last %+v", what, last)
		}
	}
}

func localBoard(bs []backend.HostBoard) (backend.HostBoard, bool) {
	for _, b := range bs {
		if b.HostID == backend.LocalHostID {
			return b, true
		}
	}
	return backend.HostBoard{}, false
}

func TestWatchBoardRefreshesOnEvents(t *testing.T) {
	dir := isolate(t)
	fd := newFakeDaemon(t)
	fd.setSessions(proto.SessionInfo{Name: "a@p", State: "running", Status: "working", StartedAt: time.Now()})
	fd.setBoard(proto.BoardResult{Digest: "d1", Sessions: []proto.BoardSession{{Name: "a@p", Status: "working",
		Tasks: []proto.Task{{ID: "t1", Title: "Uno", Status: proto.TaskInProgress}}}}})
	opts := sshOpts(dir, fd.sock)
	opts.BoardDebounce = 20 * time.Millisecond
	b := newBackend(t, opts)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	boards := b.WatchBoard(ctx)
	waitBoards(t, boards, "first board", func(bs []backend.HostBoard) bool {
		hb, ok := localBoard(bs)
		return ok && len(hb.Board.Sessions) == 1 && hb.Board.Sessions[0].Tasks[0].Title == "Uno"
	})
	waitFor(t, "subscription", func() bool {
		fd.mu.Lock()
		defer fd.mu.Unlock()
		return len(fd.subs) == 1
	})
	calls := len(fd.boardCalls())

	// An event the board does not depend on: no refresh.
	fd.emit(proto.Event{Seq: 11, Epoch: "E1", Kind: proto.EvViewChanged, Session: "a@p", Data: rawJSON(proto.ViewChangedData{Revision: 2})})
	time.Sleep(150 * time.Millisecond)
	if n := len(fd.boardCalls()); n != calls {
		t.Fatalf("view.changed refreshed the board: %d calls, want %d", n, calls)
	}

	// A burst of task events: one debounced refresh carrying the digest.
	fd.setBoard(proto.BoardResult{Digest: "d2", Sessions: []proto.BoardSession{{Name: "a@p", Status: "working",
		Tasks: []proto.Task{{ID: "t1", Title: "Uno", Status: proto.TaskBlocked}}}}})
	for i := range 3 {
		fd.emit(proto.Event{Seq: uint64(12 + i), Epoch: "E1", Kind: "task.changed", Session: "a@p"})
	}
	waitBoards(t, boards, "board after task.changed", func(bs []backend.HostBoard) bool {
		hb, ok := localBoard(bs)
		return ok && len(hb.Board.Sessions) == 1 && hb.Board.Sessions[0].Tasks[0].Status == proto.TaskBlocked
	})
	got := fd.boardCalls()
	if n := len(got) - calls; n != 1 {
		t.Errorf("burst of 3 events made %d board calls, want 1", n)
	}
	if last := got[len(got)-1]; last.SinceDigest != "d1" {
		t.Errorf("refresh since_digest %q, want d1", last.SinceDigest)
	}

	// not_modified keeps the last board.
	fd.emit(proto.Event{Seq: 20, Epoch: "E1", Kind: proto.EvMailQueued, Data: rawJSON(proto.MailEventData{ID: 7, From: "a@p", To: "human:u", Kind: "question"})})
	waitFor(t, "refresh after mail.queued", func() bool { return len(fd.boardCalls()) > len(got) })
	time.Sleep(50 * time.Millisecond)
	bs := waitBoards(t, b.WatchBoard(ctx), "kept", func(bs []backend.HostBoard) bool { _, ok := localBoard(bs); return ok })
	if hb, _ := localBoard(bs); hb.Board.Digest != "d2" || len(hb.Board.Sessions) != 1 || hb.Err != "" {
		t.Errorf("board after not_modified: %+v", hb)
	}

	// Reply goes to op reply and refreshes the board.
	before := len(fd.boardCalls())
	m, err := b.Reply(ctx, backend.LocalHostID, 7, "Sí", false)
	if err != nil {
		t.Fatal(err)
	}
	if m.ReplyTo == nil || *m.ReplyTo != 7 {
		t.Errorf("reply %+v", m)
	}
	fd.mu.Lock()
	reps := append([]proto.ReplyArgs(nil), fd.replies...)
	fd.mu.Unlock()
	if len(reps) != 1 || reps[0].ID != 7 || reps[0].Body != "Sí" || reps[0].Notify == nil || *reps[0].Notify {
		t.Errorf("replies %+v", reps)
	}
	waitFor(t, "refresh after reply", func() bool { return len(fd.boardCalls()) > before })
}

func TestWatchBoardOldDaemon(t *testing.T) {
	dir := isolate(t)
	fd := newFakeDaemon(t)
	fd.mu.Lock()
	fd.noBoard = true
	fd.mu.Unlock()
	opts := sshOpts(dir, fd.sock)
	opts.BoardDebounce = 10 * time.Millisecond
	b := newBackend(t, opts)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	waitBoards(t, b.WatchBoard(ctx), "error board", func(bs []backend.HostBoard) bool {
		hb, ok := localBoard(bs)
		return ok && hb.Err != "" && len(hb.Board.Sessions) == 0
	})
	waitFor(t, "subscription", func() bool {
		fd.mu.Lock()
		defer fd.mu.Unlock()
		return len(fd.subs) == 1
	})
	n := len(fd.boardCalls())
	fd.emit(proto.Event{Seq: 11, Epoch: "E1", Kind: "task.changed", Session: "a@p"})
	time.Sleep(100 * time.Millisecond)
	if got := len(fd.boardCalls()); got != n {
		t.Errorf("unsupported board asked again on an event: %d calls, want %d", got, n)
	}
}

func TestWatchBoardNotes(t *testing.T) {
	dir := isolate(t)
	fd := newFakeDaemon(t)
	fd.setSessions(proto.SessionInfo{Name: "a@p", State: "running", Status: "idle", StartedAt: time.Now()})
	fd.setBoard(proto.BoardResult{Digest: "d1", Sessions: []proto.BoardSession{{Name: "a@p", Status: "idle"}}})
	fd.mu.Lock()
	fd.notes = []proto.Note{{ID: 1, Title: "Revisar licencias", State: proto.NoteOpen}}
	fd.mu.Unlock()
	opts := sshOpts(dir, fd.sock)
	opts.BoardDebounce = 20 * time.Millisecond
	b := newBackend(t, opts)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	boards := b.WatchBoard(ctx)
	waitBoards(t, boards, "board with notes", func(bs []backend.HostBoard) bool {
		hb, ok := localBoard(bs)
		return ok && len(hb.Notes) == 1 && hb.NotesErr == "" && hb.Err == ""
	})
	// NoteAdd goes to the daemon and refreshes the backlog.
	if _, err := b.NoteAdd(ctx, backend.LocalHostID, "Migrar CI", "con cache"); err != nil {
		t.Fatal(err)
	}
	waitBoards(t, boards, "board after note_add", func(bs []backend.HostBoard) bool {
		hb, ok := localBoard(bs)
		return ok && len(hb.Notes) == 2
	})
	// A note.changed event refreshes it too.
	fd.mu.Lock()
	fd.notes = append(fd.notes, proto.Note{ID: 3, Title: "Otra", State: proto.NoteOpen})
	fd.mu.Unlock()
	fd.emit(proto.Event{Seq: 11, Epoch: "E1", Kind: "note.changed"})
	waitBoards(t, boards, "board after note.changed", func(bs []backend.HostBoard) bool {
		hb, ok := localBoard(bs)
		return ok && len(hb.Notes) == 3
	})
}

func TestWatchBoardNotesOldDaemon(t *testing.T) {
	dir := isolate(t)
	fd := newFakeDaemon(t)
	fd.setSessions(proto.SessionInfo{Name: "a@p", State: "running", Status: "idle", StartedAt: time.Now()})
	fd.setBoard(proto.BoardResult{Digest: "d1", Sessions: []proto.BoardSession{{Name: "a@p", Status: "idle"}}})
	fd.mu.Lock()
	fd.noNotes = true
	fd.mu.Unlock()
	opts := sshOpts(dir, fd.sock)
	opts.BoardDebounce = 20 * time.Millisecond
	b := newBackend(t, opts)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	waitBoards(t, b.WatchBoard(ctx), "board without notes op", func(bs []backend.HostBoard) bool {
		hb, ok := localBoard(bs)
		// The board itself stays healthy.
		return ok && hb.Err == "" && len(hb.Board.Sessions) == 1 && hb.NotesErr != "" && len(hb.Notes) == 0
	})
}
