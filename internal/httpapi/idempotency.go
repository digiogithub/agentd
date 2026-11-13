package httpapi

import (
	"bytes"
	"container/list"
	"crypto/sha256"
	"errors"
	"io"
	"net/http"
	"regexp"
	"sync"
	"time"

	"github.com/digiogithub/agentd/internal/client"
	"github.com/digiogithub/agentd/internal/proto"
)

// Idempotency-Key support (docs/design/api/http-api.md 2.5): an in-memory
// store keyed by (device, key) that replays the first successful response.

const (
	idemMaxEntries   = 4096
	idemMaxPerDevice = 256
	idemDefaultTTL   = 10 * time.Minute
)

var idemKeyRE = regexp.MustCompile(`^[A-Za-z0-9._:-]{1,128}$`)

type idemPhase int

const (
	phaseInFlight idemPhase = iota
	phaseDone
	// phaseUnknown: the request reached the daemon but its result was never
	// seen. A retry gets 409 outcome_unknown instead of running it again.
	phaseUnknown
)

type idemEntry struct {
	key     string
	device  string
	hash    [sha256.Size]byte
	phase   idemPhase
	status  int
	header  http.Header
	body    []byte
	created time.Time
	gEl     *list.Element // global recency list (front = newest)
	dEl     *list.Element // per-device recency list
}

// idemStore keeps entries in two intrusive LRU lists (global and per device),
// so inserts and evictions are O(1) amortized instead of a map scan.
type idemStore struct {
	ttl time.Duration
	now func() time.Time
	mu  sync.Mutex
	m   map[string]*idemEntry
	all *list.List
	dev map[string]*list.List
}

func newIdemStore(ttl time.Duration) *idemStore {
	if ttl <= 0 {
		ttl = idemDefaultTTL
	}
	return &idemStore{ttl: ttl, now: time.Now, m: map[string]*idemEntry{}, all: list.New(), dev: map[string]*list.List{}}
}

type idemState int

const (
	idemNew idemState = iota
	idemReplay
	idemMismatch
	idemInFlight
	idemUnknown
	idemFull
)

func (s *idemStore) removeLocked(e *idemEntry) {
	delete(s.m, e.key)
	s.all.Remove(e.gEl)
	if dl := s.dev[e.device]; dl != nil {
		dl.Remove(e.dEl)
		if dl.Len() == 0 {
			delete(s.dev, e.device)
		}
	}
}

// evictBackLocked drops the oldest finished (or expired) entry of l; in-flight
// entries are skipped. It reports whether it removed one.
func (s *idemStore) evictBackLocked(l *list.List, now time.Time) bool {
	for el := l.Back(); el != nil; el = el.Prev() {
		e := el.Value.(*idemEntry)
		if e.phase != phaseInFlight || now.Sub(e.created) >= s.ttl {
			s.removeLocked(e)
			return true
		}
	}
	return false
}

// begin registers the request or reports what is stored for the key.
func (s *idemStore) begin(device, key string, hash [sha256.Size]byte) (idemState, *idemEntry) {
	k := device + "\x00" + key
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	for el := s.all.Back(); el != nil; { // expired entries sit at the back
		e := el.Value.(*idemEntry)
		if now.Sub(e.created) < s.ttl {
			break
		}
		el = el.Prev()
		s.removeLocked(e)
	}
	if e, ok := s.m[k]; ok {
		switch {
		case e.hash != hash:
			return idemMismatch, nil
		case e.phase == phaseInFlight:
			return idemInFlight, nil
		case e.phase == phaseUnknown:
			return idemUnknown, nil
		}
		return idemReplay, e
	}
	if dl := s.dev[device]; dl != nil && dl.Len() >= idemMaxPerDevice && !s.evictBackLocked(dl, now) {
		return idemFull, nil
	}
	if len(s.m) >= idemMaxEntries && !s.evictBackLocked(s.all, now) {
		return idemFull, nil
	}
	e := &idemEntry{key: k, device: device, hash: hash, created: now}
	e.gEl = s.all.PushFront(e)
	if s.dev[device] == nil {
		s.dev[device] = list.New()
	}
	e.dEl = s.dev[device].PushFront(e)
	s.m[k] = e
	return idemNew, nil
}

func (s *idemStore) finish(device, key string, status int, h http.Header, body []byte) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if e, ok := s.m[device+"\x00"+key]; ok {
		e.phase, e.status, e.header, e.body = phaseDone, status, h, body
	}
}

func (s *idemStore) markUnknown(device, key string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if e, ok := s.m[device+"\x00"+key]; ok {
		e.phase = phaseUnknown
	}
}

func (s *idemStore) forget(device, key string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if e, ok := s.m[device+"\x00"+key]; ok {
		s.removeLocked(e)
	}
}

func (s *idemStore) size() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.m)
}

// captureWriter records a handler's response so it can be stored.
type captureWriter struct {
	w      http.ResponseWriter
	status int
	buf    bytes.Buffer
}

func (c *captureWriter) Header() http.Header { return c.w.Header() }
func (c *captureWriter) WriteHeader(s int) {
	if c.status == 0 {
		c.status = s
	}
	c.w.WriteHeader(s)
}
func (c *captureWriter) Write(p []byte) (int, error) {
	if c.status == 0 {
		c.status = http.StatusOK
	}
	c.buf.Write(p)
	return c.w.Write(p)
}

// replayedHeaders are the response headers a replay restores.
var replayedHeaders = []string{"Content-Type", "Cache-Control", "Location", "ETag"}

// definiteFailure reports whether a failed handler is known not to have run:
// nothing was sent to the daemon, or the daemon (or the gateway) answered with
// a definite refusal. Anything else (I/O error, timeout, client gone, revoke,
// 5xx after dispatch) leaves the outcome unknown.
func definiteFailure(err error, dispatched bool) bool {
	if !dispatched {
		return true
	}
	var ae *Error
	if errors.As(err, &ae) {
		return ae.Code != "outcome_unknown" && ae.Status >= 400 && ae.Status < 500 && ae.Status != 499
	}
	if client.IsRemote(err) {
		// The daemon answered: a refusal means nothing ran. Only a daemon
		// timeout or internal failure leaves the outcome open.
		switch proto.CodeOf(err) {
		case proto.CodeTimeout, proto.CodeInternal:
			return false
		}
		return true
	}
	return false
}

// idempotent wraps a write handler with Idempotency-Key handling. Only 2xx
// responses are stored for replay. A failure known to have happened before the
// daemon received the request (validation, auth, dial) forgets the key so the
// caller can retry; a failure after dispatch (timeout, I/O error, client gone,
// revoke, panic) records "outcome unknown": a retry with the same key gets
// 409 outcome_unknown and never executes the request twice.
func idempotent(st *idemStore, h Handler) Handler {
	return func(w http.ResponseWriter, r *http.Request) (err error) {
		key := r.Header.Get("Idempotency-Key")
		if key == "" {
			return h(w, r)
		}
		if !idemKeyRE.MatchString(key) {
			return errInvalid("Idempotency-Key must be 1-128 characters of [A-Za-z0-9._:-]")
		}
		body, err := io.ReadAll(r.Body)
		if err != nil {
			return bodyError(err)
		}
		r.Body = io.NopCloser(bytes.NewReader(body))
		device := ""
		if p := PrincipalFrom(r.Context()); p != nil {
			device = p.Device
		}
		sum := sha256.New()
		sum.Write([]byte(r.Method + " " + r.URL.EscapedPath() + "\n" + r.Header.Get("If-Match") + "\n"))
		sum.Write(body)
		var hash [sha256.Size]byte
		copy(hash[:], sum.Sum(nil))
		state, e := st.begin(device, key, hash)
		switch state {
		case idemMismatch:
			return &Error{Status: http.StatusUnprocessableEntity, Code: "idempotency_mismatch",
				Message: "Idempotency-Key was already used with a different request"}
		case idemInFlight:
			return &Error{Status: http.StatusConflict, Code: "in_progress", Retryable: true, RetryAfter: 1,
				Message: "a request with this Idempotency-Key is still in progress"}
		case idemUnknown:
			ue := errOutcomeUnknown(http.StatusConflict, nil)
			ue.Message = "an earlier request with this Idempotency-Key reached the daemon but its outcome is unknown; fetch the current state before sending a new request with a new key"
			return ue
		case idemFull:
			return errBusy()
		case idemReplay:
			for _, n := range replayedHeaders {
				if v := e.header.Get(n); v != "" {
					w.Header().Set(n, v)
				}
			}
			w.Header().Set("Idempotent-Replayed", "true")
			w.WriteHeader(e.status)
			_, _ = w.Write(e.body)
			return nil
		}
		ctx, tr := withTracker(r.Context())
		r = r.WithContext(ctx)
		finished := false
		defer func() {
			if finished {
				return
			}
			// Panic or an unclassified exit: never leave the entry in flight.
			if tr.dispatched.Load() {
				st.markUnknown(device, key)
			} else {
				st.forget(device, key)
			}
		}()
		cw := &captureWriter{w: w}
		err = h(cw, r)
		switch {
		case err == nil && cw.status >= 200 && cw.status < 300:
			hdr := http.Header{}
			for _, n := range replayedHeaders {
				if v := w.Header().Get(n); v != "" {
					hdr.Set(n, v)
				}
			}
			st.finish(device, key, cw.status, hdr, append([]byte(nil), cw.buf.Bytes()...))
		case err == nil || definiteFailure(err, tr.dispatched.Load()):
			st.forget(device, key)
		default:
			st.markUnknown(device, key)
		}
		finished = true
		return err
	}
}
