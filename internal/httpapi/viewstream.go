package httpapi

import (
	"bytes"
	"container/list"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/digiogithub/agentd/internal/client"
	"github.com/digiogithub/agentd/internal/proto"
)

const (
	// viewMinInterval is the coalescing window: at most one view event per
	// stream in this time (the latest revision wins).
	viewMinInterval = 100 * time.Millisecond
	defaultTail     = 500
	maxTail         = 5000
	viewCacheMax    = 128
	// viewCacheBytes is the memory budget of the last-view cache.
	viewCacheBytes = 8 << 20
)

// tailBuckets are the line counts the gateway really fetches (cache and
// singleflight share them); a stream asking for fewer lines gets the newest
// ones trimmed from the bucket, so N different tails do not mean N fetches.
var tailBuckets = []int{50, 100, 200, 500, maxTail}

func quantTail(n int) int {
	for _, b := range tailBuckets {
		if n <= b {
			return b
		}
	}
	return maxTail
}

// fetchedView is a view plus the session incarnation it belongs to.
type fetchedView struct {
	v    *proto.View
	inst string
	size int
}

func viewSize(v *proto.View) int {
	n := 1024
	for _, l := range v.Lines {
		n += len(l.Text) + len(l.Role) + 32
	}
	return n
}

// trimView returns v limited to its newest tail lines (v itself when it
// already fits). first_line moves with the dropped lines.
func trimView(v *proto.View, tail int) *proto.View {
	if len(v.Lines) <= tail {
		return v
	}
	drop := len(v.Lines) - tail
	c := *v
	c.Lines = v.Lines[drop:]
	c.FirstLine = v.FirstLine + drop
	return &c
}

// ---- view fetching: singleflight + last-view cache ----

type flightCall struct {
	done chan struct{}
	v    *fetchedView
	err  error
}

// flightGroup collapses concurrent fetches of the same key into one daemon
// call. The call runs detached from any single caller's context.
type flightGroup struct {
	mu sync.Mutex
	m  map[string]*flightCall
}

func (g *flightGroup) do(ctx context.Context, key string, fn func() (*fetchedView, error)) (*fetchedView, error) {
	g.mu.Lock()
	if g.m == nil {
		g.m = map[string]*flightCall{}
	}
	c, ok := g.m[key]
	if !ok {
		c = &flightCall{done: make(chan struct{})}
		g.m[key] = c
		go func() {
			c.v, c.err = fn()
			g.mu.Lock()
			delete(g.m, key)
			g.mu.Unlock()
			close(c.done)
		}()
	}
	g.mu.Unlock()
	select {
	case <-c.done:
		return c.v, c.err
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// viewCache remembers the newest view served per session+tail bucket, so a
// reconnect with a known revision can be answered with a patch. It is an LRU
// bounded by entries and by a byte budget.
type viewCache struct {
	mu    sync.Mutex
	m     map[string]*list.Element
	lru   *list.List // front = most recently used
	bytes int
}

type viewCacheEntry struct {
	key string
	fv  *fetchedView
}

func viewKey(name string, tail int) string { return name + "\x00" + strconv.Itoa(tail) }

func (vc *viewCache) put(key string, fv *fetchedView) {
	vc.mu.Lock()
	defer vc.mu.Unlock()
	if vc.m == nil {
		vc.m, vc.lru = map[string]*list.Element{}, list.New()
	}
	if el, ok := vc.m[key]; ok {
		old := el.Value.(*viewCacheEntry)
		if old.fv.inst == fv.inst && old.fv.v.Epoch == fv.v.Epoch && old.fv.v.Revision > fv.v.Revision {
			vc.lru.MoveToFront(el)
			return
		}
		vc.bytes -= old.fv.size
		old.fv = fv
		vc.bytes += fv.size
		vc.lru.MoveToFront(el)
	} else {
		vc.m[key] = vc.lru.PushFront(&viewCacheEntry{key: key, fv: fv})
		vc.bytes += fv.size
	}
	for vc.lru.Len() > 1 && (vc.lru.Len() > viewCacheMax || vc.bytes > viewCacheBytes) {
		vc.removeLocked(vc.lru.Back())
	}
}

func (vc *viewCache) removeLocked(el *list.Element) {
	e := el.Value.(*viewCacheEntry)
	vc.bytes -= e.fv.size
	delete(vc.m, e.key)
	vc.lru.Remove(el)
}

func (vc *viewCache) get(key string) *fetchedView {
	vc.mu.Lock()
	defer vc.mu.Unlock()
	if el, ok := vc.m[key]; ok {
		vc.lru.MoveToFront(el)
		return el.Value.(*viewCacheEntry).fv
	}
	return nil
}

func (vc *viewCache) clear() {
	vc.mu.Lock()
	defer vc.mu.Unlock()
	vc.m, vc.lru, vc.bytes = nil, nil, 0
}

func (vc *viewCache) invalidateOn(ev *hubEvent) {
	if ev.kind != proto.EvSessionRemoved && ev.kind != proto.EvSessionExited {
		return
	}
	vc.mu.Lock()
	defer vc.mu.Unlock()
	for k, el := range vc.m {
		if strings.HasPrefix(k, ev.session+"\x00") {
			vc.removeLocked(el)
		}
	}
}

// fetchView returns the session's view (at the bucket tail qtail), at least
// at revision minRev when the daemon already has it (a flight that started
// before the change may be stale, so it retries briefly).
func (h *hub) fetchView(ctx context.Context, name string, qtail int, minRev uint64) (*fetchedView, error) {
	key := viewKey(name, qtail)
	for attempt := 0; ; attempt++ {
		fv, err := h.flights.do(ctx, key, func() (*fetchedView, error) {
			var out proto.View
			var inst string
			err := h.s.WithDaemon(context.Background(), func(c *client.Client) error {
				si, e := findSession(c, name)
				if e != nil {
					return e
				}
				inst = sessionInstance(si)
				out, e = c.View(proto.ViewArgs{Name: name, LinesLimit: qtail})
				return e
			})
			if err != nil {
				return nil, err
			}
			return &fetchedView{v: &out, inst: inst, size: viewSize(&out)}, nil
		})
		if err != nil {
			return nil, err
		}
		if fv.v.Revision >= minRev || attempt >= 3 {
			h.views.put(key, fv)
			return fv, nil
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(20 * time.Millisecond):
		}
	}
}

// ---- patches ----

// viewSplice edits the line list: after dropping the old lines whose
// absolute index is below the patch's first_line, delete `delete` lines at
// relative index `at` and put `insert` there.
type viewSplice struct {
	At     int              `json:"at"`
	Delete int              `json:"delete"`
	Insert []proto.ViewLine `json:"insert"`
}

// viewPatch is the data of `view.patch`.
type viewPatch struct {
	FromRevision uint64         `json:"from_revision"`
	Revision     uint64         `json:"revision"`
	Epoch        string         `json:"epoch"`
	FirstLine    int            `json:"first_line"`
	Splice       *viewSplice    `json:"splice,omitempty"`
	Set          map[string]any `json:"set,omitempty"`
}

func marshalEq(a, b any) bool {
	x, _ := json.Marshal(a)
	y, _ := json.Marshal(b)
	return bytes.Equal(x, y)
}

// diffViews computes the patch turning old into cur. ok is false when a
// patch cannot express the change (other epoch/session, history moved
// backwards); the caller then sends the full view.
func diffViews(old, cur *proto.View) (p viewPatch, ok bool) {
	if old == nil || old.Epoch != cur.Epoch || old.Name != cur.Name ||
		cur.Revision <= old.Revision || cur.FirstLine < old.FirstLine {
		return p, false
	}
	p = viewPatch{FromRevision: old.Revision, Revision: cur.Revision, Epoch: cur.Epoch, FirstLine: cur.FirstLine}
	drop := cur.FirstLine - old.FirstLine
	if drop > len(old.Lines) {
		drop = len(old.Lines)
	}
	ol, nl := old.Lines[drop:], cur.Lines
	pre := 0
	for pre < len(ol) && pre < len(nl) && ol[pre] == nl[pre] {
		pre++
	}
	suf := 0
	for suf < len(ol)-pre && suf < len(nl)-pre && ol[len(ol)-1-suf] == nl[len(nl)-1-suf] {
		suf++
	}
	if del, ins := len(ol)-pre-suf, nl[pre:len(nl)-suf]; del > 0 || len(ins) > 0 {
		p.Splice = &viewSplice{At: pre, Delete: del, Insert: append([]proto.ViewLine{}, ins...)}
	}
	set := map[string]any{}
	add := func(k string, a, b any) {
		if !marshalEq(a, b) {
			set[k] = b
		}
	}
	add("status", old.Status, cur.Status)
	add("status_reason", old.StatusReason, cur.StatusReason)
	add("attention", old.Attention, cur.Attention)
	add("title", old.Title, cur.Title)
	add("cwd", old.Cwd, cur.Cwd)
	add("input", old.Input, cur.Input)
	add("options", old.Options, cur.Options)
	add("hints", old.Hints, cur.Hints)
	add("dialog", old.Dialog, cur.Dialog)
	add("idle_ms", old.IdleMS, cur.IdleMS)
	add("exit_code", old.ExitCode, cur.ExitCode)
	if len(set) > 0 {
		p.Set = set
	}
	return p, true
}

// applyViewPatch is the reference client-side application of a patch (the
// tests use it to check patches against fresh views).
func applyViewPatch(v proto.View, p viewPatch) (proto.View, error) {
	if v.Revision != p.FromRevision || v.Epoch != p.Epoch {
		return v, errors.New("patch does not apply to this view")
	}
	drop := p.FirstLine - v.FirstLine
	if drop < 0 {
		return v, errors.New("patch moves first_line backwards")
	}
	lines := v.Lines
	if drop > len(lines) {
		drop = len(lines)
	}
	lines = lines[drop:]
	if p.Splice != nil {
		s := p.Splice
		if s.At < 0 || s.At+s.Delete > len(lines) {
			return v, errors.New("splice out of range")
		}
		nl := append([]proto.ViewLine{}, lines[:s.At]...)
		nl = append(nl, s.Insert...)
		lines = append(nl, lines[s.At+s.Delete:]...)
	}
	v.Lines = lines
	v.FirstLine = p.FirstLine
	v.Revision = p.Revision
	if len(p.Set) > 0 {
		// Round-trip through JSON: the same path a real client takes.
		raw, _ := json.Marshal(v)
		var m map[string]json.RawMessage
		_ = json.Unmarshal(raw, &m)
		for k, val := range p.Set {
			b, _ := json.Marshal(val)
			m[k] = b
		}
		raw, _ = json.Marshal(m)
		lines, first := v.Lines, v.FirstLine
		v = proto.View{}
		if err := json.Unmarshal(raw, &v); err != nil {
			return v, err
		}
		v.Lines, v.FirstLine = lines, first
	}
	return v, nil
}

// viewID is the SSE event id of a view: "e<epoch>-i<instance>-r<revision>".
// The instance (session start time) keeps a re-created session of the same
// name from being resumed as if it were the old one.
func viewID(v *proto.View, inst string) string {
	return "e" + v.Epoch + "-i" + inst + "-r" + strconv.FormatUint(v.Revision, 10)
}

func parseViewID(id string) (epoch, inst string, rev uint64, ok bool) {
	ref, ok := parseETagRef(`"` + id + `"`)
	if !ok || ref.inst == "" {
		return "", "", 0, false
	}
	return ref.epoch, ref.inst, ref.rev, true
}

// ---- handler ----

// handleViewStream serves GET /v1/sessions/{name}/view/stream.
func (s *Server) handleViewStream(w http.ResponseWriter, r *http.Request) error {
	name := r.PathValue("name")
	tail := defaultTail
	if t := r.URL.Query().Get("tail"); t != "" {
		n, err := strconv.Atoi(t)
		if err != nil || n < 1 || n > maxTail {
			return errInvalid("tail must be an integer between 1 and %d", maxTail)
		}
		tail = n
	}
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
	// Attach before the first fetch so no change between the two is lost.
	sb, _, err := h.attach(r.Context(), eventFilter{
		sessions: map[string]bool{name: true},
		kinds:    []string{"view", "status", "input", "attention", "session"},
	}, "")
	if err != nil {
		return err
	}
	defer h.detach(sb)

	qt := quantTail(tail)
	key := viewKey(name, qt)
	var base *proto.View // the client's known view on resume
	baseInst := ""
	if ep, inst, rev, ok := parseViewID(lastEventID(r)); ok {
		if c := h.views.get(key); c != nil && c.inst == inst && c.v.Epoch == ep && c.v.Revision == rev {
			base, baseInst = trimView(c.v, tail), c.inst
		}
	}
	fv, err := h.fetchView(r.Context(), name, qt, 0)
	if err != nil {
		return err
	}
	cur := trimView(fv.v, tail)
	sw, err := startSSE(w)
	if err != nil {
		return nil
	}
	vs := &viewStream{s: s, h: h, sw: sw, name: name, tail: tail}
	switch {
	case base != nil && baseInst != fv.inst:
		if vs.full(cur, fv.inst) != nil { // another incarnation: no patch base
			return nil
		}
	case base != nil && base.Revision == cur.Revision:
		vs.cur, vs.curInst = cur, fv.inst // the client is current: nothing to send
	case base != nil:
		vs.cur, vs.curInst = base, baseInst
		if vs.emit(cur, fv.inst) != nil {
			return nil
		}
	default:
		if vs.full(cur, fv.inst) != nil {
			return nil
		}
	}

	var (
		dirty     bool
		wantRev   uint64
		lastEmit  = time.Now()
		flushTime = time.NewTimer(time.Hour)
	)
	defer flushTime.Stop()
	hb := time.NewTicker(s.sseHeartbeat())
	defer hb.Stop()

	bye := func(reason string) {
		_ = sw.event("", "bye", controlPayload("bye", map[string]any{"reason": reason}))
	}
	// flush fetches and emits the latest view when the 100 ms window allows.
	// It returns false when the stream must end.
	flush := func(force bool) bool {
		if !dirty {
			return true
		}
		if wait := viewMinInterval - time.Since(lastEmit); wait > 0 && !force {
			flushTime.Reset(wait)
			return true
		}
		fv, err := h.fetchView(r.Context(), name, qt, wantRev)
		switch {
		case r.Context().Err() != nil:
			return false
		case err != nil:
			var ae *Error
			if errors.As(FromDaemonError(err), &ae) && ae.Status == http.StatusNotFound {
				bye("session_removed")
				return false
			}
			vs.cur = nil
			if sw.event("", "resync", controlPayload("resync", map[string]any{"reason": "fetch_failed"})) != nil {
				return false
			}
			flushTime.Reset(time.Second)
			return true
		}
		dirty = false
		lastEmit = time.Now()
		return vs.emit(trimView(fv.v, tail), fv.inst) == nil
	}

	for {
		select {
		case <-r.Context().Done():
			return nil
		case <-s.draining:
			bye("shutdown")
			return nil
		case <-hb.C:
			if sw.comment("ping") != nil {
				return nil
			}
		case <-flushTime.C:
			if !flush(false) {
				return nil
			}
		case <-sb.notify:
			evs, reason, id := sb.take()
			if reason != "" {
				if sw.event(id, "resync", controlPayload("resync", map[string]any{"reason": reason})) != nil {
					return nil
				}
				vs.cur = nil // next view event is a full one
				dirty = true
			}
			for _, ev := range evs {
				switch ev.kind {
				case proto.EvViewChanged:
					var d proto.ViewChangedData
					_ = json.Unmarshal(ev.data, &d)
					wantRev = max(wantRev, d.Revision)
					dirty = true
				case proto.EvAttentionChanged:
					dirty = true
				case proto.EvStatusChanged, proto.EvInputChanged:
					dirty = true
					if sw.event("", ev.kind, ev.payload) != nil {
						return nil
					}
				case proto.EvSessionExited:
					dirty = true
					flush(true) // final state (exit code) before the end
					_ = sw.event("", ev.kind, ev.payload)
					bye("session_exited")
					return nil
				case proto.EvSessionRemoved:
					_ = sw.event("", ev.kind, ev.payload)
					bye("session_removed")
					return nil
				}
			}
			if !flush(false) {
				return nil
			}
		}
	}
}

// viewStream tracks the last view sent on one stream.
type viewStream struct {
	s    *Server
	h    *hub
	sw   *sseWriter
	name string
	tail int
	cur  *proto.View
	// curInst is the session incarnation of cur.
	curInst string
}

func (vs *viewStream) full(v *proto.View, inst string) error {
	b, _ := json.Marshal(v)
	vs.cur, vs.curInst = v, inst
	return vs.sw.event(viewID(v, inst), "view", b)
}

// emit sends v as a patch against the last sent view (or a full view when a
// patch is impossible or not smaller). An unchanged revision sends nothing.
func (vs *viewStream) emit(v *proto.View, inst string) error {
	if vs.cur != nil && inst == vs.curInst && v.Revision == vs.cur.Revision && v.Epoch == vs.cur.Epoch {
		return nil
	}
	if inst != vs.curInst {
		return vs.full(v, inst) // another session incarnation: never a patch
	}
	p, ok := diffViews(vs.cur, v)
	if !ok {
		return vs.full(v, inst)
	}
	pb, _ := json.Marshal(p)
	if fb, _ := json.Marshal(v); len(pb) >= len(fb) {
		return vs.full(v, inst)
	}
	vs.cur = v
	return vs.sw.event(viewID(v, inst), "view.patch", pb)
}
