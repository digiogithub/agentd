package hcompat

import (
	"encoding/json"
	"strings"
	"sync"
	"time"

	"github.com/digiogithub/agentd/internal/client"
	"github.com/digiogithub/agentd/internal/proto"
)

// subscription is one events.subscribe: a daemon subscribe stream translated
// into hc event notifications, plus local events (labels, keep, doc).
type subscription struct {
	c    *conn
	st   *stream // framed mode; nil in plain mode (notifications on the connection)
	hold *stream // plain mode: keeps the connection open while it streams

	terminal string
	labels   map[string]string
	docs     []string

	mu      sync.Mutex
	seq     uint64
	visible map[string]bool   // session names in this subscriber's view
	profile map[string]string // session name -> profile (status app)
	out     chan []byte
	done    chan struct{}
	quit    sync.Once
}

func eventsSubscribe(c *conn, params json.RawMessage) (any, func(), *rpcError) {
	var p struct {
		Terminal *string           `json:"terminal"`
		Labels   map[string]string `json:"labels"`
		Docs     []string          `json:"docs"`
	}
	if err := decode(params, &p); err != nil {
		return nil, nil, err
	}
	cl, err := c.s.Dial()
	if err != nil {
		return nil, nil, fromDaemon(err)
	}
	ds, err := cl.Subscribe(proto.SubscribeArgs{Kinds: []string{"session", "status"}}, 0)
	if err != nil {
		_ = cl.Close()
		return nil, nil, fromDaemon(err)
	}
	sub := &subscription{c: c, labels: p.Labels, docs: p.Docs,
		visible: map[string]bool{}, profile: map[string]string{},
		out: make(chan []byte, 1024), done: make(chan struct{})}
	if p.Terminal != nil {
		sub.terminal = *p.Terminal
	}
	terms := sub.snapshot(ds.Info.Sessions)
	result := map[string]any{"seq": 0, "terminals": terms}
	if c.framed {
		sub.st = c.newStream()
		result["channel"] = sub.st.ch
	} else {
		// Plain mode: the connection now carries only notifications; a
		// placeholder stream keeps it open until the subscription ends.
		sub.hold = c.newStream()
	}
	c.s.mu.Lock()
	c.s.subs[sub] = struct{}{}
	c.s.mu.Unlock()
	after := func() {
		go sub.writer()
		go sub.pump(ds)
		go func() {
			select {
			case <-c.ctx.Done():
			case <-sub.done:
			}
			sub.stop()
			_ = ds.Close()
		}()
	}
	return result, after, nil
}

// snapshot filters sessions into hc records and resets the view.
func (sub *subscription) snapshot(all []proto.SessionInfo) []Terminal {
	terms := []Terminal{}
	sub.mu.Lock()
	defer sub.mu.Unlock()
	sub.visible = map[string]bool{}
	if sub.docsOnly() {
		return terms
	}
	for _, si := range all {
		sub.profile[si.Name] = si.Profile
		t := sub.c.s.record(si)
		if sub.terminal != "" && si.Name != sub.terminal || !matchLabels(t, sub.labels) {
			continue
		}
		sub.visible[si.Name] = true
		terms = append(terms, t)
	}
	return terms
}

// docsOnly: naming documents and no terminal or label asks for documents
// alone (herdr-core: naming narrows).
func (sub *subscription) docsOnly() bool {
	return len(sub.docs) > 0 && sub.terminal == "" && len(sub.labels) == 0
}

func (sub *subscription) wantsDoc(name string) bool {
	if len(sub.docs) == 0 {
		return sub.terminal == "" && len(sub.labels) == 0
	}
	for _, d := range sub.docs {
		if d == name || strings.HasSuffix(d, "*") && strings.HasPrefix(name, strings.TrimSuffix(d, "*")) {
			return true
		}
	}
	return false
}

func (sub *subscription) stop() {
	sub.quit.Do(func() {
		close(sub.done)
		if sub.hold != nil {
			sub.hold.close(nil)
		}
		sub.c.s.mu.Lock()
		delete(sub.c.s.subs, sub)
		sub.c.s.mu.Unlock()
	})
}

// emitLocked queues one notification; seq rises by one per event.
func (sub *subscription) emitLocked(method string, params map[string]any) {
	if method == "event" {
		sub.seq++
	}
	params["seq"] = sub.seq
	b, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "method": method, "params": params})
	if err != nil {
		return
	}
	select {
	case sub.out <- append(b, '\n'):
	case <-sub.done:
	}
}

func (sub *subscription) event(typ, terminal string, data any) {
	params := map[string]any{"type": typ, "data": data}
	if terminal != "" {
		params["terminal"] = terminal
	}
	sub.emitLocked("event", params)
}

// local delivers a Server-generated event (labels, keep, doc).
func (sub *subscription) local(typ, terminal string, data any) {
	sub.mu.Lock()
	defer sub.mu.Unlock()
	if typ == "doc" {
		if m, ok := data.(map[string]any); ok {
			if name, _ := m["name"].(string); sub.wantsDoc(name) {
				sub.event("doc", "", data)
			}
		}
		return
	}
	if sub.visible[terminal] {
		sub.event(typ, terminal, data)
	}
}

func (sub *subscription) writer() {
	for {
		select {
		case <-sub.done:
			return
		case line := <-sub.out:
			if sub.st != nil {
				if !sub.st.send(line) {
					sub.stop()
					return
				}
			} else {
				sub.c.wmu.Lock()
				_, err := sub.c.w.Write(line)
				sub.c.wmu.Unlock()
				if err != nil {
					sub.stop()
					return
				}
			}
		}
	}
}

// pump translates daemon events until the stream ends.
func (sub *subscription) pump(ds *client.Subscription) {
	defer func() {
		if sub.st != nil {
			sub.st.close(map[string]any{"reason": "server_stop"})
		}
		sub.stop()
	}()
	for {
		select {
		case <-sub.done:
			return
		case ev, ok := <-ds.Events:
			if !ok {
				return
			}
			if !sub.translate(ev) {
				return
			}
		}
	}
}

func (sub *subscription) translate(ev proto.Event) bool {
	s := sub.c.s
	switch ev.Kind {
	case proto.EvHeartbeat:
		sub.mu.Lock()
		sub.emitLocked("heartbeat", map[string]any{})
		sub.mu.Unlock()
	case proto.EvBye:
		return false
	case proto.EvResync:
		all, err := s.sessions()
		if err != nil {
			return false
		}
		terms := sub.snapshot(all)
		sub.mu.Lock()
		sub.seq++ // drop events whose seq is not above the snapshot's
		sub.emitLocked("missed", map[string]any{"terminals": terms})
		sub.mu.Unlock()
	case proto.EvSessionStarted:
		var si proto.SessionInfo
		if json.Unmarshal(ev.Data, &si) != nil {
			return true
		}
		t := s.record(si)
		sub.mu.Lock()
		sub.profile[si.Name] = si.Profile
		if !sub.docsOnly() && (sub.terminal == "" || sub.terminal == si.Name) && matchLabels(t, sub.labels) {
			sub.visible[si.Name] = true
			sub.event("created", si.Name, t)
		}
		sub.mu.Unlock()
	case proto.EvSessionExited:
		var d proto.ExitedData
		_ = json.Unmarshal(ev.Data, &d)
		sub.mu.Lock()
		if sub.visible[ev.Session] {
			sub.event("status", ev.Session, map[string]any{"status": nil, "records": map[string]any{}, "reported": true})
			sub.event("finished", ev.Session, map[string]any{
				"reason": "exited", "exit_code": d.ExitCode, "signal": nil, "finished_ms": time.Now().UnixMilli(),
			})
		}
		sub.mu.Unlock()
	case proto.EvSessionRemoved:
		sub.mu.Lock()
		if sub.visible[ev.Session] {
			delete(sub.visible, ev.Session)
			sub.event("removed", ev.Session, map[string]any{})
		}
		sub.mu.Unlock()
	case proto.EvStatusChanged:
		var d proto.StatusChangedData
		if json.Unmarshal(ev.Data, &d) != nil {
			return true
		}
		turn, ended := s.noteStatus(ev, d)
		sub.mu.Lock()
		if sub.visible[ev.Session] && d.Status != "exited" {
			st := mapStatus(proto.SessionInfo{Status: d.Status, Profile: sub.profile[ev.Session]})
			sub.event("status", ev.Session, map[string]any{"status": st, "records": map[string]any{}, "reported": st != nil})
			if ended {
				word := ""
				if st != nil {
					word = st.State
				}
				sub.event("turn", ev.Session, map[string]any{"turn": turn, "state": word})
			}
		}
		sub.mu.Unlock()
	}
	return true
}

// noteStatus advances a session's turn counter when its status leaves
// working. Several subscriptions see the same daemon event: the counter
// moves once per daemon event (by seq).
func (s *Server) noteStatus(ev proto.Event, d proto.StatusChangedData) (turn uint64, ended bool) {
	ended = d.From == "working" && d.Status != "working"
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.turnSeq == nil {
		s.turnSeq = map[string]uint64{}
	}
	if ended && ev.Seq > s.turnSeq[ev.Session] {
		s.turnSeq[ev.Session] = ev.Seq
		s.turns[ev.Session]++
	}
	return s.turns[ev.Session], ended
}
