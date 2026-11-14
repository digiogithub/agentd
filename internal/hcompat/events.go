package hcompat

import (
	"encoding/json"
	"maps"
	"strings"
	"sync"
	"time"

	"github.com/digiogithub/agentd/internal/client"
	"github.com/digiogithub/agentd/internal/proto"
)

// subscription is one events.subscribe: a daemon subscribe stream translated
// into hc notifications (event, heartbeat, missed).
type subscription struct {
	c    *conn
	st   *stream // framed mode; nil in plain mode (notifications on the connection)
	hold *stream // plain mode: keeps the connection open while it streams

	terminal string
	labels   map[string]string
	docs     []string

	mu      sync.Mutex
	seq     uint64
	known   map[string]proto.SessionInfo // every session seen (for filters and status app)
	visible map[string]bool              // session names in this subscriber's view
	out     chan []byte
	done    chan struct{}
	quit    sync.Once
}

// subscribeKinds are the daemon event kinds hc events are built from.
var subscribeKinds = []string{"session", "status", "doc"}

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
	ds, err := cl.Subscribe(proto.SubscribeArgs{Kinds: subscribeKinds}, 0)
	if err != nil {
		_ = cl.Close()
		return nil, nil, fromDaemon(err)
	}
	sub := &subscription{c: c, labels: p.Labels, docs: p.Docs,
		known: map[string]proto.SessionInfo{}, visible: map[string]bool{},
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
		sub.hold = c.newStream()
	}
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
	sub.known = map[string]proto.SessionInfo{}
	for _, si := range all {
		sub.known[si.Name] = si
		if t := sub.c.s.record(si); sub.matches(si.Name, t) {
			sub.visible[si.Name] = true
			terms = append(terms, t)
		}
	}
	return terms
}

// matches applies the terminal and label filters.
func (sub *subscription) matches(name string, t Terminal) bool {
	if sub.docsOnly() || sub.terminal != "" && name != sub.terminal {
		return false
	}
	return matchLabels(t, sub.labels)
}

// docsOnly: naming documents and no terminal or label asks for documents
// alone (herdr-core: naming narrows).
func (sub *subscription) docsOnly() bool {
	return len(sub.docs) > 0 && sub.terminal == "" && len(sub.labels) == 0
}

// wantsDoc: unfiltered subscribers hear every document; terminal and label
// filters exclude documents unless documents are named.
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
			if !ok || !sub.translate(ev) {
				return
			}
		}
	}
}

func (sub *subscription) translate(ev proto.Event) bool {
	switch ev.Kind {
	case proto.EvHeartbeat:
		sub.mu.Lock()
		sub.emitLocked("heartbeat", map[string]any{})
		sub.mu.Unlock()
	case proto.EvBye:
		return false
	case proto.EvResync:
		all, err := sub.c.s.sessions()
		if err != nil {
			return false
		}
		terms := sub.snapshot(all)
		sub.mu.Lock()
		sub.seq++ // clients drop events whose seq is not above the snapshot's
		sub.emitLocked("missed", map[string]any{"terminals": terms})
		sub.mu.Unlock()
	case proto.EvDocChanged:
		var d proto.DocChangedData
		if json.Unmarshal(ev.Data, &d) != nil {
			return true
		}
		sub.mu.Lock()
		if sub.wantsDoc(d.Name) {
			sub.event("doc", "", map[string]any{"name": d.Name, "version": d.Version})
		}
		sub.mu.Unlock()
	case proto.EvSessionStarted:
		var si proto.SessionInfo
		if json.Unmarshal(ev.Data, &si) != nil {
			return true
		}
		t := sub.c.s.record(si)
		sub.mu.Lock()
		sub.known[si.Name] = si
		if sub.matches(si.Name, t) {
			sub.visible[si.Name] = true
			sub.event("created", si.Name, t)
		}
		sub.mu.Unlock()
	case proto.EvSessionExited:
		var d proto.ExitedData
		_ = json.Unmarshal(ev.Data, &d)
		sub.mu.Lock()
		if si, ok := sub.known[ev.Session]; ok {
			code := d.ExitCode
			si.State, si.ExitCode = "exited", &code
			sub.known[ev.Session] = si
		}
		if sub.visible[ev.Session] {
			sub.event("status", ev.Session, map[string]any{"status": nil, "records": map[string]any{}, "reported": true})
			sub.event("finished", ev.Session, map[string]any{
				"reason": "exited", "exit_code": d.ExitCode, "signal": nil, "finished_ms": time.Now().UnixMilli(),
			})
		}
		sub.mu.Unlock()
	case proto.EvSessionRemoved:
		sub.mu.Lock()
		delete(sub.known, ev.Session)
		if sub.visible[ev.Session] {
			delete(sub.visible, ev.Session)
			sub.event("removed", ev.Session, map[string]any{})
		}
		sub.mu.Unlock()
	case proto.EvSessionLabels:
		var d proto.LabelsData
		if json.Unmarshal(ev.Data, &d) != nil {
			return true
		}
		sub.mu.Lock()
		sub.labelsChanged(ev.Session, d.Labels)
		sub.mu.Unlock()
	case proto.EvStatusChanged:
		var d proto.StatusChangedData
		if json.Unmarshal(ev.Data, &d) != nil {
			return true
		}
		sub.mu.Lock()
		si := sub.known[ev.Session]
		si.Status, si.Turn = d.Status, d.Turn
		if si.Name != "" {
			sub.known[ev.Session] = si
		}
		if sub.visible[ev.Session] && d.Status != "exited" {
			st := mapStatus(proto.SessionInfo{Status: d.Status, Profile: si.Profile})
			sub.event("status", ev.Session, map[string]any{"status": st, "records": map[string]any{}, "reported": st != nil})
			if d.From == "working" && d.Status != "working" {
				word := ""
				if st != nil {
					word = st.State
				}
				sub.event("turn", ev.Session, map[string]any{"turn": d.Turn, "state": word})
			}
		}
		sub.mu.Unlock()
	}
	return true
}

// labelsChanged turns a daemon session.labels into hc labels / keep events,
// following herdr-core's filter rules: a terminal that newly matches a label
// filter comes with its whole record; one that stops matching still gets the
// event, then leaves the view.
func (sub *subscription) labelsChanged(name string, labels map[string]string) {
	si, ok := sub.known[name]
	if !ok {
		return
	}
	oldKeep := si.Labels[labelKeep] != ""
	oldVisible := visibleLabels(si)
	si.Labels = labels
	sub.known[name] = si
	t := sub.c.s.record(si)
	was, now := sub.visible[name], sub.matches(name, t)
	if !was && !now {
		return
	}
	if !maps.Equal(oldVisible, t.Labels) || !was {
		data := map[string]any{"labels": t.Labels}
		if !was {
			data["record"] = t
		}
		sub.event("labels", name, data)
	}
	if was && t.Keep != oldKeep {
		sub.event("keep", name, map[string]any{"keep": t.Keep})
	}
	sub.visible[name] = now
	if !now {
		delete(sub.visible, name)
	}
}
