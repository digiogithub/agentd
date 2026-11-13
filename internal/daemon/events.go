package daemon

// Daemon event bus and op subscribe (docs/design/api/daemon-ops.md, §6).
//
// Publishers (handlers, the view watchers, delivery through a callback) call
// bus.publish, which never blocks: it assigns a seq, appends to a replay ring
// and does a non-blocking send to every matching subscriber. A subscriber
// whose bounded queue is full loses its queue and gets a single `resync`
// event instead, then continues live, so a slow client loses detail, never
// consistency, and never slows the daemon down.

import (
	"context"
	"encoding/json"
	"net"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/digiogithub/agentd/internal/proto"
)

// Bus sizes and timings.
const (
	// ReplayRing is how many events the bus keeps for since_seq replay.
	ReplayRing = 1024
	// SubscriberQueue is the capacity of each subscriber's queue.
	SubscriberQueue = 256
	// DefaultHeartbeat is the default Server.HeartbeatInterval.
	DefaultHeartbeat = 15 * time.Second
	// MinHeartbeat is the smallest heartbeat_ms a client may ask for.
	MinHeartbeat = time.Second
	// byeTimeout bounds writing the final bye line at shutdown.
	byeTimeout = 2 * time.Second
	// byeWait bounds how long Shutdown waits for subscribers to say bye.
	byeWait = 3 * time.Second
)

// busEvent is a ring entry: the wire event plus the mail addresses used for
// identity filtering (never sent).
type busEvent struct {
	ev       proto.Event
	from, to string // mail events only
}

type bus struct {
	epoch string

	mu     sync.Mutex
	seq    uint64
	ring   [ReplayRing]busEvent
	subs   map[*subscriber]struct{}
	closed bool
	wg     sync.WaitGroup // one per subscriber loop
}

type subscriber struct {
	f    eventFilter
	ch   chan proto.Event
	quit chan struct{} // closed by bus.close: say bye and stop
	// end is closed (once) when the session this identity-bound subscriber
	// belongs to is removed: the subscription ends with a bye.
	end     chan struct{}
	endDone bool
	replay  []proto.Event // delivered before ch, set at registration
	// firstResync, if set, is sent before anything else (epoch mismatch or
	// since_seq out of the ring).
	firstResync string
}

// eventFilter selects events: session names, kinds and the caller's identity.
type eventFilter struct {
	sessions []string // exact names; empty = all
	kinds    []string // exact kinds or prefixes; empty = all
	// self != "" restricts to events about the session self and its own
	// mail; allowSessions lifts the session-event part ([security]
	// agent_view). Mail is never lifted.
	self          string
	allowSessions bool
}

func newBus(epoch string) *bus {
	return &bus{epoch: epoch, subs: make(map[*subscriber]struct{})}
}

// kindMatches reports whether kind is selected by the kinds list.
func kindMatches(kinds []string, kind string) bool {
	if len(kinds) == 0 {
		return true
	}
	for _, k := range kinds {
		if kind == k || strings.HasPrefix(kind, k+".") {
			return true
		}
	}
	return false
}

func (f eventFilter) match(be busEvent) bool {
	ev := be.ev
	if !kindMatches(f.kinds, ev.Kind) {
		return false
	}
	if len(f.sessions) > 0 && !slices.Contains(f.sessions, ev.Session) {
		return false
	}
	if f.self != "" {
		if strings.HasPrefix(ev.Kind, "mail.") {
			return be.from == f.self || be.to == f.self
		}
		return f.allowSessions || ev.Session == f.self
	}
	return true
}

// publish appends an event and fans it out. It never blocks: a subscriber
// with a full queue is flushed and receives one resync.
func (b *bus) publish(kind, session, from, to string, data any) {
	raw := rawOf(data)
	now := time.Now().UTC()
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return
	}
	b.seq++
	ev := proto.Event{Seq: b.seq, Epoch: b.epoch, Time: now, Kind: kind, Session: session, Data: raw}
	be := busEvent{ev: ev, from: from, to: to}
	b.ring[b.seq%ReplayRing] = be
	for s := range b.subs {
		if kind == proto.EvSessionRemoved && s.f.self != "" && session == s.f.self && !s.endDone {
			s.endDone = true
			defer close(s.end) // after the event below is queued
		}
		if !s.f.match(be) {
			continue
		}
		select {
		case s.ch <- ev:
		default:
			// Overflow: drop the queue, leave one resync (always fits).
			for drained := false; !drained; {
				select {
				case <-s.ch:
				default:
					drained = true
				}
			}
			s.ch <- b.controlLocked(proto.EvResync, proto.ResyncData{Reason: proto.ResyncOverflow})
		}
	}
}

func rawOf(data any) json.RawMessage {
	if data == nil {
		return nil
	}
	raw, _ := json.Marshal(data)
	return raw
}

func (b *bus) controlLocked(kind string, data any) proto.Event {
	return proto.Event{Epoch: b.epoch, Time: time.Now().UTC(), Kind: kind, Data: rawOf(data)}
}

// subscribe registers a subscriber, atomically with the replay selection so
// no event is missed or duplicated. It returns nil when the bus is closed.
func (b *bus) subscribe(f eventFilter, sinceSeq uint64, epoch string) (*subscriber, uint64) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return nil, 0
	}
	s := &subscriber{f: f, ch: make(chan proto.Event, SubscriberQueue), quit: make(chan struct{}), end: make(chan struct{})}
	switch {
	case epoch != "" && epoch != b.epoch:
		s.firstResync = proto.ResyncEpochChanged
	case sinceSeq > 0:
		oldest := uint64(1)
		if b.seq > ReplayRing {
			oldest = b.seq - ReplayRing + 1
		}
		if sinceSeq > b.seq || sinceSeq+1 < oldest {
			s.firstResync = proto.ResyncSinceTooOld
			break
		}
		for seq := sinceSeq + 1; seq <= b.seq; seq++ {
			if be := b.ring[seq%ReplayRing]; f.match(be) {
				s.replay = append(s.replay, be.ev)
			}
		}
	}
	b.subs[s] = struct{}{}
	b.wg.Add(1)
	return s, b.seq
}

func (b *bus) unsubscribe(s *subscriber) {
	b.mu.Lock()
	delete(b.subs, s)
	b.mu.Unlock()
	b.wg.Done()
}

func (b *bus) lastSeq() uint64 {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.seq
}

// close stops publishing and tells every subscriber loop to say bye. It
// returns once they have finished or after byeWait.
func (b *bus) close() {
	b.mu.Lock()
	if !b.closed {
		b.closed = true
		for s := range b.subs {
			close(s.quit)
		}
	}
	b.mu.Unlock()
	done := make(chan struct{})
	go func() { b.wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(byeWait):
	}
}

// publish emits an event about session (may be empty).
func (s *Server) publish(kind, session string, data any) {
	s.bus.publish(kind, session, "", "", data)
}

// publishMail emits a mail.* event; the session is the recipient when it is
// a registered session. The body is never included.
func (s *Server) publishMail(kind string, id int64, from, to string) {
	sess := ""
	if s.isSession(to) {
		sess = to
	}
	s.bus.publish(kind, sess, from, to, proto.MailEventData{ID: id, From: from, To: to})
}

func (s *Server) heartbeat() time.Duration {
	if s.HeartbeatInterval > 0 {
		return s.HeartbeatInterval
	}
	return DefaultHeartbeat
}

var knownKinds = []string{
	proto.EvSessionCreated, proto.EvSessionStarted, proto.EvSessionExited, proto.EvSessionRemoved,
	proto.EvStatusChanged, proto.EvViewChanged, proto.EvInputChanged, proto.EvAttentionChanged,
	proto.EvMailQueued, proto.EvMailDelivered, proto.EvMailRead,
}

func validKind(k string) bool {
	for _, kk := range knownKinds {
		if k == kk || strings.HasPrefix(kk, k+".") {
			return true
		}
	}
	return false
}

// handleSubscribe serves op subscribe. It returns false if the request
// failed before the upgrade (an error response was sent and the connection
// stays in JSON mode), true once the stream has ended.
func (s *Server) handleSubscribe(conn net.Conn, codec *proto.Codec, req proto.Request, peer peerInfo) bool {
	fail := func(err error) bool {
		_ = codec.WriteResponse(proto.ErrorResponse(classifyError(err)))
		return false
	}
	var a proto.SubscribeArgs
	if err := req.DecodeArgs(&a); err != nil {
		return fail(err)
	}
	who, err := s.resolveCaller(peer, req.Auth)
	if err != nil {
		return fail(err)
	}
	for _, k := range a.Kinds {
		if !validKind(k) {
			return fail(proto.Errorf(proto.CodeInvalid, "subscribe: unknown kind %q", k))
		}
	}
	if a.SinceSeq > 0 && a.Epoch == "" {
		return fail(proto.Errorf(proto.CodeInvalid, "subscribe: since_seq needs epoch (sequence numbers only mean something within one daemon epoch)"))
	}
	if a.HeartbeatMS < 0 {
		return fail(proto.Errorf(proto.CodeInvalid, "subscribe: negative heartbeat_ms"))
	}
	hb := s.heartbeat()
	if a.HeartbeatMS > 0 {
		hb = max(time.Duration(a.HeartbeatMS)*time.Millisecond, MinHeartbeat)
	}
	f := eventFilter{sessions: a.Sessions, kinds: a.Kinds}
	if who.session {
		f.self, f.allowSessions = who.id, s.Cfg.Security.AgentView
	}
	sub, _ := s.bus.subscribe(f, a.SinceSeq, a.Epoch)
	if sub == nil {
		return fail(proto.Errorf(proto.CodeNotReady, "daemon is shutting down"))
	}
	defer s.bus.unsubscribe(sub)
	// The subscriber is registered, so nothing is missed from here on. seq is
	// read BEFORE the snapshot is built: the snapshot reflects every event up
	// to seq, and live events with seq <= minSeq (queued while it was being
	// built) are skipped, so no event ever predates the snapshot. A client
	// asking for a replay (since_seq) gets it unfiltered.
	seq := s.bus.lastSeq()
	minSeq := seq
	if a.SinceSeq > 0 {
		minSeq = 0
	}

	res := proto.SubscribeResult{Epoch: s.epoch, Seq: seq, Sessions: []proto.SessionInfo{}}
	for _, e := range s.sortedEntries() {
		if n := e.sess.Name(); (len(f.sessions) == 0 || slices.Contains(f.sessions, n)) &&
			(f.self == "" || f.allowSessions || n == f.self) {
			res.Sessions = append(res.Sessions, s.info(context.Background(), e))
		}
	}
	ok, err := proto.OKResponse(res)
	if err != nil {
		return fail(err)
	}
	if err := codec.WriteResponse(ok); err != nil {
		return true
	}
	s.Log.Info("client subscribed", "who", who.id, "since_seq", a.SinceSeq)
	defer s.Log.Info("client unsubscribed", "who", who.id)

	// Anything the client writes is discarded; a read error means it left.
	gone := make(chan struct{})
	readerDone := make(chan struct{})
	go func() {
		defer close(readerDone)
		defer close(gone)
		buf := make([]byte, 512)
		for {
			if _, err := codec.Reader().Read(buf); err != nil {
				return
			}
		}
	}()
	defer func() { _ = conn.Close(); <-readerDone }()

	timeout := s.AttachWriteTimeout
	if timeout <= 0 {
		timeout = DefaultAttachWriteTimeout
	}
	write := func(ev proto.Event, to time.Duration) bool {
		_ = conn.SetWriteDeadline(time.Now().Add(to))
		return codec.WriteJSON(ev) == nil
	}
	control := func(kind string, data any) proto.Event {
		return proto.Event{Epoch: s.epoch, Time: time.Now().UTC(), Kind: kind, Data: rawOf(data)}
	}
	if !write(control(proto.EvHello, proto.HelloData{Epoch: s.epoch, Seq: seq}), timeout) {
		return true
	}
	if sub.firstResync != "" && !write(control(proto.EvResync, proto.ResyncData{Reason: sub.firstResync}), timeout) {
		return true
	}
	for _, ev := range sub.replay {
		if !write(ev, timeout) {
			return true
		}
	}
	// bye ends the stream after flushing what is already queued.
	bye := func() bool {
		for drained := false; !drained; {
			select {
			case ev := <-sub.ch:
				if ev.Seq != 0 && ev.Seq <= minSeq {
					continue
				}
				if !write(ev, byeTimeout) {
					return true
				}
			default:
				drained = true
			}
		}
		write(control(proto.EvBye, nil), byeTimeout)
		return true
	}
	tick := time.NewTicker(hb)
	defer tick.Stop()
	for {
		select {
		case ev := <-sub.ch:
			if ev.Seq != 0 && ev.Seq <= minSeq {
				continue // already reflected in the snapshot
			}
			if !write(ev, timeout) {
				return true
			}
		case <-tick.C:
			if !write(control(proto.EvHeartbeat, nil), timeout) {
				return true
			}
		case <-sub.quit:
			return bye()
		case <-sub.end:
			return bye()
		case <-gone:
			return true
		}
	}
}
