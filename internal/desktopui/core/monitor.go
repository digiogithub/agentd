package core

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"sync"
	"time"

	"github.com/digiogithub/agentd/internal/desktopui/backend"
	"github.com/digiogithub/agentd/internal/proto"
)

// hostMon watches one host: the agentd session list (subscribe stream,
// reconnected with since_seq/epoch) and, while sessions are watched, the
// zellij/tmux lists. WatchView consumers register here for the session
// events that should refresh their view.
type hostMon struct {
	b      *Backend
	id     string
	ctx    context.Context
	cancel context.CancelFunc
	wake   chan struct{}
	muxK   chan struct{}

	mu       sync.Mutex
	sessions map[string]proto.SessionInfo
	up       bool
	version  string
	epoch    string
	seq      uint64
	mux      map[backend.Source][]backend.Session
	views    map[string]map[chan struct{}]struct{}
}

var errBye = errors.New("core: daemon closed the event stream")

// activate starts watching host id (once).
func (b *Backend) activate(id string) *hostMon {
	b.mu.Lock()
	defer b.mu.Unlock()
	if m := b.mons[id]; m != nil {
		return m
	}
	ctx, cancel := context.WithCancel(b.ctx)
	m := &hostMon{
		b: b, id: id, ctx: ctx, cancel: cancel,
		wake:     make(chan struct{}, 1),
		muxK:     make(chan struct{}, 1),
		sessions: map[string]proto.SessionInfo{},
		mux:      map[backend.Source][]backend.Session{},
		views:    map[string]map[chan struct{}]struct{}{},
	}
	if b.ctx.Err() != nil {
		return m // closed: never started
	}
	b.mons[id] = m
	b.wg.Add(1)
	go func() {
		defer b.wg.Done()
		m.runAgentd()
	}()
	if b.opts.Mux != nil {
		b.wg.Add(1)
		go func() {
			defer b.wg.Done()
			m.runMux()
		}()
	}
	return m
}

func (b *Backend) wakeMonitor(id string) {
	b.mu.Lock()
	m := b.mons[id]
	b.mu.Unlock()
	if m != nil {
		kick(m.wake)
		kick(m.muxK)
	}
}

func kick(ch chan struct{}) {
	select {
	case ch <- struct{}{}:
	default:
	}
}

func (m *hostMon) stop() { m.cancel() }

func (m *hostMon) kickMux() { kick(m.muxK) }

func (m *hostMon) counts() (int, string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	n := len(m.sessions)
	for _, l := range m.mux {
		n += len(l)
	}
	return n, m.version
}

func (m *hostMon) list() []backend.Session {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]backend.Session, 0, len(m.sessions))
	for _, si := range m.sessions {
		out = append(out, agentdSession(m.id, si))
	}
	for _, l := range m.mux {
		out = append(out, l...)
	}
	return out
}

// addView registers a refresh trigger for session name.
func (m *hostMon) addView(name string, ch chan struct{}) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.views[name] == nil {
		m.views[name] = map[chan struct{}]struct{}{}
	}
	m.views[name][ch] = struct{}{}
}

func (m *hostMon) removeView(name string, ch chan struct{}) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.views[name], ch)
	if len(m.views[name]) == 0 {
		delete(m.views, name)
	}
}

// kickViews triggers the views of name ("" = all). Caller holds m.mu.
func (m *hostMon) kickViewsLocked(name string) {
	for n, set := range m.views {
		if name != "" && n != name {
			continue
		}
		for ch := range set {
			kick(ch)
		}
	}
}

// upsert records a session (Launch answers before the event arrives).
func (m *hostMon) upsert(si proto.SessionInfo) {
	m.mu.Lock()
	m.sessions[si.Name] = si
	m.mu.Unlock()
	m.b.publishSessions()
}

func (m *hostMon) sleep(d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return true
	case <-m.wake:
		return true
	case <-m.ctx.Done():
		return false
	}
}

func (m *hostMon) setDown() {
	m.mu.Lock()
	changed := m.up || len(m.sessions) > 0
	m.up = false
	m.version = ""
	m.sessions = map[string]proto.SessionInfo{}
	m.kickViewsLocked("")
	m.mu.Unlock()
	if changed {
		m.b.publishSessions()
	}
}

func (m *hostMon) runAgentd() {
	const minBackoff, maxBackoff = 500 * time.Millisecond, 30 * time.Second
	backoff := minBackoff
	for m.ctx.Err() == nil {
		connected, err := m.streamOnce()
		if m.ctx.Err() != nil {
			return
		}
		m.setDown()
		if connected {
			backoff = minBackoff
		}
		if err != nil && !errors.Is(err, errBye) {
			m.b.log.Debug("agentd stream", "host", m.id, "err", err)
		}
		if !m.sleep(backoff) {
			return
		}
		backoff = min(backoff*2, maxBackoff)
	}
}

// streamOnce runs one subscribe connection until it ends. connected says
// whether the daemon answered (resets the backoff).
func (m *hostMon) streamOnce() (connected bool, err error) {
	c, err := m.b.dialAgentd(m.ctx, m.id)
	if err != nil {
		return false, err
	}
	stop := context.AfterFunc(m.ctx, func() { _ = c.Close() })
	defer stop()
	ping, err := c.Ping()
	if err != nil {
		_ = c.Close()
		return false, err
	}
	args := proto.SubscribeArgs{}
	m.mu.Lock()
	if m.seq > 0 && m.epoch != "" {
		args.SinceSeq, args.Epoch = m.seq, m.epoch
	}
	m.mu.Unlock()
	sub, err := c.Subscribe(args, 256)
	if err != nil {
		_ = c.Close()
		return true, err
	}
	defer sub.Close()

	snapSeq := sub.Info.Seq
	m.mu.Lock()
	m.up = true
	m.version = ping.Version
	m.epoch, m.seq = sub.Info.Epoch, sub.Info.Seq
	m.sessions = map[string]proto.SessionInfo{}
	for _, si := range sub.Info.Sessions {
		m.sessions[si.Name] = si
	}
	m.kickViewsLocked("")
	m.mu.Unlock()
	m.b.publishSessions()
	kick(m.muxK)

	var refresh <-chan time.Time
	var timer *time.Timer
	defer func() {
		if timer != nil {
			timer.Stop()
		}
	}()
	for {
		select {
		case ev, ok := <-sub.Events:
			if !ok {
				if err := sub.Err(); err != nil {
					return true, err
				}
				return true, io.EOF
			}
			if ev.Seq > 0 && ev.Seq <= snapSeq && ev.Epoch == sub.Info.Epoch {
				continue // already in the snapshot
			}
			changed, reload, prev, bye := m.apply(ev)
			if bye {
				return true, errBye
			}
			if reload {
				if err := m.reload(); err != nil {
					return true, err
				}
			} else if changed {
				m.b.publishSessions()
			}
			if prev && timer == nil {
				timer = time.NewTimer(m.b.opts.PreviewRefresh)
				refresh = timer.C
			}
		case <-refresh:
			timer, refresh = nil, nil
			if err := m.reload(); err != nil {
				return true, err
			}
		case <-m.ctx.Done():
			return true, m.ctx.Err()
		}
	}
}

// apply folds one event into the session map. changed says the list may
// differ; reload asks for a fresh ls; preview for a debounced one (previews
// come only with ls).
func (m *hostMon) apply(ev proto.Event) (changed, reload, preview, bye bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if ev.Seq > m.seq {
		m.seq = ev.Seq
	}
	if ev.Epoch != "" && ev.Seq > 0 {
		m.epoch = ev.Epoch
	}
	si, known := m.sessions[ev.Session]
	switch ev.Kind {
	case proto.EvBye:
		return false, false, false, true
	case proto.EvResync:
		m.kickViewsLocked("")
		return true, true, false, false
	case proto.EvSessionCreated, proto.EvSessionStarted:
		var info proto.SessionInfo
		if json.Unmarshal(ev.Data, &info) == nil && info.Name != "" {
			m.sessions[info.Name] = info
		}
	case proto.EvSessionExited:
		if known {
			var d proto.ExitedData
			_ = json.Unmarshal(ev.Data, &d)
			si.State, si.Status = "exited", "exited"
			code := d.ExitCode
			si.ExitCode = &code
			si.Attention = false
			m.sessions[ev.Session] = si
		}
	case proto.EvSessionRemoved:
		delete(m.sessions, ev.Session)
	case proto.EvStatusChanged:
		if known {
			var d proto.StatusChangedData
			if json.Unmarshal(ev.Data, &d) == nil {
				si.Status, si.Attention, si.Revision = d.Status, d.Attention, d.Revision
				if d.Turn > 0 {
					si.Turn = d.Turn
				}
				m.sessions[ev.Session] = si
			}
		}
		preview = true
	case proto.EvAttentionChanged:
		if known {
			var d proto.AttentionChangedData
			if json.Unmarshal(ev.Data, &d) == nil {
				si.Attention = d.Attention
				m.sessions[ev.Session] = si
			}
		}
	case proto.EvInputChanged:
		if known {
			var d proto.InputChangedData
			if json.Unmarshal(ev.Data, &d) == nil {
				si.InputState = d.State
				m.sessions[ev.Session] = si
			}
		}
	case proto.EvViewChanged:
		if known {
			var d proto.ViewChangedData
			if json.Unmarshal(ev.Data, &d) == nil {
				si.Revision = d.Revision
				m.sessions[ev.Session] = si
			}
		}
		preview = true
	case proto.EvSessionLabels:
		if known {
			var d proto.LabelsData
			if json.Unmarshal(ev.Data, &d) == nil {
				si.Labels = d.Labels
				m.sessions[ev.Session] = si
			}
		}
	default:
		return false, false, false, false
	}
	if ev.Session != "" {
		m.kickViewsLocked(ev.Session)
	}
	return true, false, preview, false
}

// reload replaces the session map with a fresh ls.
func (m *hostMon) reload() error {
	var list []proto.SessionInfo
	err := m.b.withClient(m.ctx, m.id, func(c *clientConn) error {
		var err error
		list, err = c.Ls()
		return err
	})
	if err != nil {
		return err
	}
	m.mu.Lock()
	m.sessions = make(map[string]proto.SessionInfo, len(list))
	for _, si := range list {
		m.sessions[si.Name] = si
	}
	m.kickViewsLocked("")
	m.mu.Unlock()
	m.b.publishSessions()
	return nil
}

// runMux polls the zellij/tmux adapters while sessions are watched.
func (m *hostMon) runMux() {
	t := time.NewTicker(m.b.opts.MuxPoll)
	defer t.Stop()
	for {
		if m.b.watchingSessions() {
			m.pollMux()
		}
		select {
		case <-t.C:
		case <-m.muxK:
		case <-m.ctx.Done():
			return
		}
	}
}

func (m *hostMon) pollMux() {
	ctx, cancel := context.WithTimeout(m.ctx, 15*time.Second)
	defer cancel()
	adapters, err := m.b.muxAdapters(ctx, m.id)
	if err != nil {
		return
	}
	next := map[backend.Source][]backend.Session{}
	for _, a := range adapters {
		list, err := a.List(ctx)
		if err != nil {
			m.b.log.Debug("mux list", "host", m.id, "source", a.Source(), "err", err)
			continue
		}
		for i := range list {
			list[i].HostID = m.id
			list[i].Source = a.Source()
		}
		next[a.Source()] = list
	}
	if m.ctx.Err() != nil {
		return
	}
	m.mu.Lock()
	m.mux = next
	m.mu.Unlock()
	m.b.publishSessions()
}

// muxAdapters returns the adapters of a host, rebuilt when its shell changes.
func (b *Backend) muxAdapters(ctx context.Context, id string) ([]MuxAdapter, error) {
	if b.opts.Mux == nil {
		return nil, nil
	}
	if _, relay := b.relayHost(id); relay {
		return nil, nil // no shell: no zellij/tmux
	}
	sh, err := b.shell(ctx, id)
	if err != nil {
		return nil, err
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if set := b.adapters[id]; set != nil && set.shell == sh {
		return set.list, nil
	}
	set := &adapterSet{shell: sh, list: b.opts.Mux(sh, id)}
	b.adapters[id] = set
	return set.list, nil
}

func (b *Backend) muxAdapter(ctx context.Context, id string, src backend.Source) (MuxAdapter, error) {
	list, err := b.muxAdapters(ctx, id)
	if err != nil {
		return nil, err
	}
	for _, a := range list {
		if a.Source() == src {
			return a, nil
		}
	}
	return nil, ErrUnsupported
}
