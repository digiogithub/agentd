package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/digiogithub/agentd/internal/client"
	"github.com/digiogithub/agentd/internal/proto"
)

// hubEvent is one daemon bus event as the gateway keeps and forwards it. id
// is the gateway event id "<epoch>:<seq>".
type hubEvent struct {
	seq     uint64
	epoch   string
	id      string
	kind    string
	session string
	data    json.RawMessage
	payload []byte // envelopeData JSON, encoded once
}

// eventFilter is the query-string filter of a stream.
type eventFilter struct {
	sessions map[string]bool
	kinds    []string
}

func parseFilter(r *http.Request) eventFilter {
	q := r.URL.Query()
	var f eventFilter
	for _, k := range []string{"session", "sessions"} {
		for _, v := range q[k] {
			for _, n := range strings.Split(v, ",") {
				if n = strings.TrimSpace(n); n != "" {
					if f.sessions == nil {
						f.sessions = map[string]bool{}
					}
					f.sessions[n] = true
				}
			}
		}
	}
	for _, k := range []string{"kind", "kinds", "types"} {
		for _, v := range q[k] {
			for _, n := range strings.Split(v, ",") {
				if n = strings.TrimSpace(n); n != "" {
					f.kinds = append(f.kinds, n)
				}
			}
		}
	}
	return f
}

func (f eventFilter) match(ev *hubEvent) bool {
	if f.sessions != nil && !f.sessions[ev.session] {
		return false
	}
	if len(f.kinds) == 0 {
		return true
	}
	for _, p := range f.kinds {
		switch {
		case p == ev.kind:
			return true
		case strings.HasSuffix(p, ".*") && strings.HasPrefix(ev.kind, p[:len(p)-1]):
			return true
		case !strings.Contains(p, ".") && strings.HasPrefix(ev.kind, p+"."):
			return true
		}
	}
	return false
}

// subscriber is one HTTP stream attached to the hub. push never blocks: a
// full queue is replaced by a pending resync.
type subscriber struct {
	filter eventFilter
	max    int
	notify chan struct{}

	mu       sync.Mutex
	q        []*hubEvent
	resync   string // pending resync reason; events are dropped while set
	resyncID string
}

func newSubscriber(f eventFilter, max int) *subscriber {
	return &subscriber{filter: f, max: max, notify: make(chan struct{}, 1)}
}

func (sb *subscriber) wake() {
	select {
	case sb.notify <- struct{}{}:
	default:
	}
}

func (sb *subscriber) push(ev *hubEvent) {
	sb.mu.Lock()
	if sb.resync == "" {
		if len(sb.q) >= sb.max {
			sb.q = nil
			sb.resync, sb.resyncID = "overflow", ev.id
		} else {
			sb.q = append(sb.q, ev)
		}
	}
	sb.mu.Unlock()
	sb.wake()
}

// forceResync supersedes everything queued: the client must refetch.
func (sb *subscriber) forceResync(reason, id string) {
	sb.mu.Lock()
	sb.q = nil
	if sb.resync == "" {
		sb.resync, sb.resyncID = reason, id
	}
	sb.mu.Unlock()
	sb.wake()
}

func (sb *subscriber) take() (evs []*hubEvent, reason, id string) {
	sb.mu.Lock()
	defer sb.mu.Unlock()
	evs, sb.q = sb.q, nil
	reason, id = sb.resync, sb.resyncID
	sb.resync, sb.resyncID = "", ""
	return evs, reason, id
}

// hub owns the gateway's single daemon subscription and fans it out.
type hub struct {
	s *Server

	startOnce sync.Once
	ready     chan struct{} // closed after the first successful subscribe
	readyOnce sync.Once
	done      chan struct{} // closed when the run loop has exited
	stopped   atomic.Bool

	// daemonSubs counts daemon subscribe calls (tests: N clients => 1).
	daemonSubs atomic.Int64

	mu       sync.Mutex
	subs     map[*subscriber]struct{}
	ring     []*hubEvent
	max      int
	epoch    string
	head     uint64
	floor    uint64 // seq of the last daemon-side resync: older ids cannot resume
	skipSync bool   // the daemon's epoch_mismatch resync was already announced

	// tap, when set, sees every sequenced event (notifier); it must not block.
	tap func(*hubEvent)

	views   viewCache
	flights flightGroup
}

func newHub(s *Server) *hub {
	return &hub{
		s:     s,
		ready: make(chan struct{}),
		done:  make(chan struct{}),
		subs:  map[*subscriber]struct{}{},
		max:   s.eventBuffer(),
	}
}

func (h *hub) start() {
	h.startOnce.Do(func() {
		ctx, cancel := context.WithCancel(context.Background())
		go func() {
			select {
			case <-h.s.draining:
				h.stopped.Store(true)
				cancel()
			case <-h.done:
			}
		}()
		go func() {
			defer close(h.done)
			defer cancel()
			h.run(ctx)
		}()
	})
}

// wait blocks until the run loop has ended (after draining).
func (h *hub) wait() {
	select {
	case <-h.done:
	default:
		h.startOnce.Do(func() { close(h.done) }) // never started
		<-h.done
	}
}

func (h *hub) run(ctx context.Context) {
	backoff := 250 * time.Millisecond
	for ctx.Err() == nil {
		connected := h.session(ctx)
		if connected {
			backoff = 250 * time.Millisecond
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
		if !connected && backoff < 5*time.Second {
			backoff *= 2
		}
	}
}

func (h *hub) headID() string {
	if h.head == 0 {
		return ""
	}
	return h.epoch + ":" + strconv.FormatUint(h.head, 10)
}

// session runs one daemon subscription until it ends; it reports whether the
// subscribe succeeded.
func (h *hub) session(ctx context.Context) bool {
	dial := h.s.hubDial()
	if dial == nil {
		return false
	}
	c, err := dial()
	if err != nil {
		h.s.Log.Warn("events: daemon unreachable", "err", err)
		return false
	}
	stop := context.AfterFunc(ctx, func() { _ = c.Close() })
	defer stop()
	h.mu.Lock()
	args := proto.SubscribeArgs{}
	if h.head > 0 {
		args.SinceSeq, args.Epoch = h.head, h.epoch
	}
	h.mu.Unlock()
	h.daemonSubs.Add(1)
	sub, err := c.Subscribe(args, 256)
	if err != nil {
		_ = c.Close()
		h.s.Log.Warn("events: subscribe failed", "err", err)
		return false
	}
	defer sub.Close()

	h.mu.Lock()
	if h.epoch != "" && sub.Info.Epoch != h.epoch {
		h.ring, h.head, h.floor = nil, 0, 0
		h.views.clear()
		h.epoch = sub.Info.Epoch
		h.skipSync = true
		h.resyncAllLocked(proto.ResyncEpochChanged)
	} else if h.epoch == "" {
		h.epoch = sub.Info.Epoch
	}
	h.mu.Unlock()
	h.readyOnce.Do(func() { close(h.ready) })

	for ev := range sub.Events {
		h.handle(ev)
	}
	if err := sub.Err(); err != nil && ctx.Err() == nil {
		h.s.Log.Warn("events: daemon stream ended", "err", err)
	}
	return true
}

func (h *hub) handle(ev proto.Event) {
	switch ev.Kind {
	case proto.EvHello, proto.EvHeartbeat, proto.EvBye:
		return
	case proto.EvResync:
		var d proto.ResyncData
		_ = json.Unmarshal(ev.Data, &d)
		h.mu.Lock()
		defer h.mu.Unlock()
		if h.skipSync && d.Reason == proto.ResyncEpochChanged {
			h.skipSync = false
			return
		}
		h.skipSync = false
		h.ring = nil // the sequence has a gap: no resume across it
		h.floor = h.head
		h.views.clear()
		h.resyncAllLocked(d.Reason)
		return
	}
	if ev.Seq == 0 {
		return
	}
	body := envelopeData{Type: ev.Kind, Session: ev.Session, TS: ev.Time.UTC(), Data: ev.Data}
	payload, _ := json.Marshal(body)
	he := &hubEvent{
		seq: ev.Seq, epoch: ev.Epoch, kind: ev.Kind, session: ev.Session, data: ev.Data, payload: payload,
		id: ev.Epoch + ":" + strconv.FormatUint(ev.Seq, 10),
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if he.epoch == h.epoch && he.seq <= h.head {
		return // replay overlap
	}
	h.ring = append(h.ring, he)
	if over := len(h.ring) - h.max; over > 0 {
		h.ring = append([]*hubEvent(nil), h.ring[over:]...)
	}
	h.head = he.seq
	for sb := range h.subs {
		if sb.filter.match(he) {
			sb.push(he)
		}
	}
	h.views.invalidateOn(he)
	if h.tap != nil {
		h.tap(he)
	}
}

func (h *hub) resyncAllLocked(reason string) {
	id := h.headID()
	for sb := range h.subs {
		sb.forceResync(reason, id)
	}
}

// attachInfo is what a new stream learns when it attaches.
type attachInfo struct {
	epoch  string
	seq    uint64
	lastID string
}

// attach registers a subscriber atomically with the resume decision, so no
// event is lost or duplicated between replay and live. lastID is the
// client's Last-Event-ID ("" = fresh).
func (h *hub) attach(ctx context.Context, f eventFilter, lastID string) (*subscriber, attachInfo, error) {
	if h.s.hubDial() == nil || h.stopped.Load() {
		return nil, attachInfo{}, errUnavailable("gateway is shutting down")
	}
	h.start()
	t := time.NewTimer(callTimeout)
	defer t.Stop()
	select {
	case <-h.ready:
	case <-ctx.Done():
		return nil, attachInfo{}, ctx.Err()
	case <-h.s.draining:
		return nil, attachInfo{}, errUnavailable("gateway is shutting down")
	case <-t.C:
		return nil, attachInfo{}, errUnavailable("daemon unreachable")
	}
	sb := newSubscriber(f, h.s.queueSize())
	h.mu.Lock()
	defer h.mu.Unlock()
	info := attachInfo{epoch: h.epoch, seq: h.head, lastID: h.headID()}
	if lastID != "" {
		epoch, seq, ok := parseEventID(lastID)
		switch {
		case !ok:
			sb.resync, sb.resyncID = proto.ResyncSinceTooOld, h.headID()
		case epoch != h.epoch:
			sb.resync, sb.resyncID = proto.ResyncEpochChanged, h.headID()
		case seq > h.head || seq < h.floor || (h.floor > 0 && seq == h.floor) || (len(h.ring) > 0 && seq+1 < h.ring[0].seq) || (len(h.ring) == 0 && seq != h.head):
			sb.resync, sb.resyncID = proto.ResyncSinceTooOld, h.headID()
		default:
			for _, ev := range h.ring {
				if ev.seq > seq && f.match(ev) {
					if len(sb.q) >= sb.max {
						sb.q = nil
						sb.resync, sb.resyncID = "overflow", h.headID()
						break
					}
					sb.q = append(sb.q, ev)
				}
			}
		}
	}
	h.subs[sb] = struct{}{}
	sb.wake() // deliver a pending resync / replay right away
	return sb, info, nil
}

func (h *hub) detach(sb *subscriber) {
	h.mu.Lock()
	delete(h.subs, sb)
	h.mu.Unlock()
}

func (h *hub) numSubscribers() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.subs)
}

func parseEventID(id string) (epoch string, seq uint64, ok bool) {
	i := strings.LastIndex(id, ":")
	if i <= 0 {
		return "", 0, false
	}
	n, err := strconv.ParseUint(id[i+1:], 10, 64)
	if err != nil {
		return "", 0, false
	}
	return id[:i], n, true
}

// hubDial is the dialer of the background subscription: it falls back to Dial
// only when no HubDial was given. The CLI always gives a non-autostarting one,
// so an idle gateway never resurrects a daemon that was stopped on purpose.
func (s *Server) hubDial() func() (*client.Client, error) {
	if s.HubDial != nil {
		return s.HubDial
	}
	return s.Dial
}

func (s *Server) queueSize() int {
	if s.sseQueue > 0 {
		return s.sseQueue
	}
	return sseQueueSize
}

// hubFor returns the gateway's hub (created on first use).
func (s *Server) hubFor() *hub {
	s.init()
	return s.hub
}

func lastEventID(r *http.Request) string {
	if v := r.Header.Get("Last-Event-ID"); v != "" {
		return v
	}
	return r.URL.Query().Get("last_event_id")
}

// handleEvents serves GET /v1/events.
func (s *Server) handleEvents(w http.ResponseWriter, r *http.Request) error {
	dev := ""
	if p := PrincipalFrom(r.Context()); p != nil {
		dev = p.Device
	}
	release, err := s.acquireStream(dev)
	if err != nil {
		return err
	}
	defer release()
	h := s.hubFor()
	sb, info, err := h.attach(r.Context(), parseFilter(r), lastEventID(r))
	if err != nil {
		return err
	}
	defer h.detach(sb)
	sw, err := startSSE(w)
	if err != nil {
		return nil
	}
	hello := controlPayload("hello", map[string]any{
		"epoch": info.epoch, "seq": info.seq, "last_id": info.lastID, "api_version": APIVersion,
	})
	if sw.event("", "hello", hello) != nil {
		return nil
	}
	hb := time.NewTicker(s.sseHeartbeat())
	defer hb.Stop()
	for {
		select {
		case <-r.Context().Done():
			return nil
		case <-s.draining:
			_ = sw.event("", "bye", controlPayload("bye", map[string]any{"reason": "shutdown"}))
			return nil
		case <-hb.C:
			if sw.comment("ping") != nil {
				return nil
			}
		case <-sb.notify:
			evs, reason, id := sb.take()
			if reason != "" {
				if sw.event(id, "resync", controlPayload("resync", map[string]any{"reason": reason})) != nil {
					return nil
				}
			}
			for _, ev := range evs {
				if sw.event(ev.id, ev.kind, ev.payload) != nil {
					return nil
				}
			}
		}
	}
}
