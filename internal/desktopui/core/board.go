package core

import (
	"context"
	"sort"
	"strings"
	"time"

	"github.com/digiogithub/agentd/internal/client"
	"github.com/digiogithub/agentd/internal/desktopui/backend"
	"github.com/digiogithub/agentd/internal/proto"
)

// boardState is the board of one host monitor (guarded by hostMon.mu).
type boardState struct {
	res     *proto.BoardResult // nil until the first answer
	updated time.Time
	err     string
	// unsupported: the daemon has no op board; asked again after the event
	// stream reconnects (a daemon upgrade restarts it).
	unsupported bool
}

// boardEvent reports whether an event kind may change the board.
func boardEvent(kind string) bool {
	for _, p := range []string{"task.", "mail.", "status.", "attention.", "session."} {
		if strings.HasPrefix(kind, p) {
			return true
		}
	}
	return kind == proto.EvResync
}

// WatchBoard implements backend.Backend: one board per host whose daemon is
// up, refreshed by its event stream (see backend.Backend.WatchBoard).
func (b *Backend) WatchBoard(ctx context.Context) <-chan []backend.HostBoard {
	b.startWatching()
	b.mu.Lock()
	b.boardWatchers++
	mons := b.monList()
	b.mu.Unlock()
	ch := b.boardB.subscribe(ctx)
	context.AfterFunc(ctx, func() {
		b.mu.Lock()
		b.boardWatchers--
		b.mu.Unlock()
	})
	for _, m := range mons {
		kick(m.boardK)
	}
	b.publishBoard()
	return ch
}

func (b *Backend) watchingBoard() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.boardWatchers > 0
}

func (b *Backend) publishBoard() {
	if b.ctx.Err() != nil {
		return
	}
	b.mu.Lock()
	mons := b.monList()
	b.mu.Unlock()
	out := []backend.HostBoard{}
	for _, m := range mons {
		if hb, ok := m.hostBoard(); ok {
			out = append(out, hb)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].HostID < out[j].HostID })
	b.boardB.publish(out)
}

// hostBoard returns the host's board while its daemon is up.
func (m *hostMon) hostBoard() (backend.HostBoard, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.up || m.board.res == nil && m.board.err == "" {
		return backend.HostBoard{}, false
	}
	hb := backend.HostBoard{HostID: m.id, Updated: m.board.updated, Err: m.board.err}
	if m.board.res != nil {
		hb.Board = *m.board.res
	}
	return hb, true
}

// runBoard refreshes the board after kicks (events, reconnects, a new
// watcher), at most once per Options.BoardDebounce, while someone watches.
func (m *hostMon) runBoard() {
	for {
		select {
		case <-m.boardK:
		case <-m.ctx.Done():
			return
		}
		// Debounce: let a burst of events (a tasks_set, a turn end) settle.
		t := time.NewTimer(m.b.opts.BoardDebounce)
		select {
		case <-t.C:
		case <-m.ctx.Done():
			t.Stop()
			return
		}
		select { // kicks during the wait are covered by this refresh
		case <-m.boardK:
		default:
		}
		if !m.b.watchingBoard() {
			continue
		}
		m.refreshBoard()
	}
}

func (m *hostMon) refreshBoard() {
	m.mu.Lock()
	up, unsupported := m.up, m.board.unsupported
	since := ""
	if m.board.res != nil && m.board.err == "" {
		since = m.board.res.Digest
	}
	m.mu.Unlock()
	if !up || unsupported {
		return
	}
	ctx, cancel := context.WithTimeout(m.ctx, 20*time.Second)
	defer cancel()
	var res proto.BoardResult
	err := m.b.withClient(ctx, m.id, func(c *client.Client) error {
		var err error
		res, err = c.Board(since)
		return err
	})
	if m.ctx.Err() != nil {
		return
	}
	m.mu.Lock()
	switch {
	case err != nil:
		m.board.err = err.Error()
		// An older daemon: unknown op (invalid) or, over the relay, an op
		// its device gate does not know (forbidden).
		if code := client.ErrorCode(err); code == proto.CodeInvalid || code == proto.CodeForbidden {
			m.board.unsupported = true
		}
	case res.NotModified && m.board.res != nil:
		m.board.err = ""
		m.board.updated = time.Now()
	default:
		if res.Sessions == nil {
			res.Sessions = []proto.BoardSession{}
		}
		m.board.res, m.board.err, m.board.updated = &res, "", time.Now()
	}
	m.mu.Unlock()
	if err != nil {
		m.b.log.Debug("board", "host", m.id, "err", err)
	}
	m.b.publishBoard()
}

// resetBoard forgets the board when the event stream (re)connects or the
// daemon goes away. Caller holds m.mu.
func (m *hostMon) resetBoardLocked() {
	m.board = boardState{}
}

// Reply answers message id as the host's human user (op reply).
func (b *Backend) Reply(ctx context.Context, hostID string, id int64, body string, notify bool) (*proto.Message, error) {
	if h, relay := b.relayHost(hostID); relay && !h.Allows(backend.CapMailbox) {
		return nil, proto.Errorf(proto.CodeForbidden, "this device has no mailbox on %s", h.Name)
	}
	var m proto.Message
	err := b.withClient(ctx, hostID, func(c *client.Client) error {
		var err error
		if notify {
			m, err = c.Reply(id, body)
		} else {
			m, err = c.ReplyQuiet(id, body)
		}
		return err
	})
	if err != nil {
		return nil, err
	}
	b.mu.Lock()
	mon := b.mons[hostID]
	b.mu.Unlock()
	if mon != nil {
		kick(mon.boardK) // the question is closed
	}
	return &m, nil
}
