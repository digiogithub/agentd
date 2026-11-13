package httpapi

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/digiogithub/agentd/internal/client"
	"github.com/digiogithub/agentd/internal/proto"
)

// ---- fake daemon ----

// fakeDaemon speaks the daemon wire protocol on a unix socket. Ops without a
// custom handler get a canned answer; a handler may drop or hang the
// connection to simulate a daemon that received a request but never answered.
type fakeDaemon struct {
	sock   string
	mu     sync.Mutex
	counts map[string]int
	custom map[string]func(c net.Conn, cd *proto.Codec)
}

func newFakeDaemon(t *testing.T) *fakeDaemon {
	t.Helper()
	dir, err := os.MkdirTemp("", "agentd-f")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	f := &fakeDaemon{sock: filepath.Join(dir, "d.sock"), counts: map[string]int{}, custom: map[string]func(net.Conn, *proto.Codec){}}
	ln, err := net.Listen("unix", f.sock)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go f.serve(c)
		}
	}()
	return f
}

func (f *fakeDaemon) serve(c net.Conn) {
	defer c.Close()
	cd := proto.NewCodec(c)
	for {
		req, err := cd.ReadRequest()
		if err != nil {
			return
		}
		f.mu.Lock()
		f.counts[req.Op]++
		h := f.custom[req.Op]
		f.mu.Unlock()
		if h != nil {
			h(c, cd)
			// A custom handler that returns without answering leaves the
			// connection to the next loop turn; dropping is done by closing c.
			continue
		}
		var data any
		switch req.Op {
		case proto.OpPing:
			data = proto.PingResult{Version: "t", Epoch: "e1"}
		case proto.OpLs:
			data = proto.LsResult{Sessions: []proto.SessionInfo{{Name: "x", State: "running", StartedAt: time.Unix(1700000000, 5)}}}
		case proto.OpView:
			data = proto.View{Name: "x", Epoch: "e1", Revision: 3, Status: "idle"}
		case proto.OpWhoami:
			data = proto.WhoamiResult{Identity: "human:t"}
		default:
			data = map[string]any{}
		}
		resp, _ := proto.OKResponse(data)
		_ = cd.WriteResponse(resp)
	}
}

func (f *fakeDaemon) on(op string, fn func(c net.Conn, cd *proto.Codec)) {
	f.mu.Lock()
	f.custom[op] = fn
	f.mu.Unlock()
}

func (f *fakeDaemon) count(op string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.counts[op]
}

func (f *fakeDaemon) server(t *testing.T) *Server {
	t.Helper()
	s := &Server{
		Config: Config{Version: "t", Hostname: "example.com", RunHarnesses: []string{"claude"}, RateLimit: 100000, RateBurst: 100000, ActRateLimit: 100000},
		Dial:   func() (*client.Client, error) { return client.Dial(f.sock) },
		Auth:   testAuth(),
	}
	t.Cleanup(s.Close)
	return s
}

func dropConn(c net.Conn, _ *proto.Codec) { _ = c.Close() }

func post(t *testing.T, h http.Handler, path, token, body string, hdr ...string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest("POST", path, strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Authorization", "Bearer "+token)
	for i := 0; i+1 < len(hdr); i += 2 {
		r.Header.Set(hdr[i], hdr[i+1])
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, r)
	return rec
}

type errBody struct {
	Error struct {
		Code      string         `json:"code"`
		Retryable bool           `json:"retryable"`
		Details   map[string]any `json:"details"`
	} `json:"error"`
}

// ---- item 1, 3: act deadline, no retry, outcome_unknown ----

func TestActNoRetryAndOutcomeUnknown(t *testing.T) {
	f := newFakeDaemon(t)
	f.on(proto.OpAct, dropConn) // received, never answered
	s := f.server(t)
	rec := post(t, s.Handler(), "/v1/sessions/x/act", testToken, `{"action":"prompt","text":"hi"}`)
	var e errBody
	decodeBody(t, rec, &e)
	if rec.Code != 502 || e.Error.Code != "outcome_unknown" || e.Error.Retryable {
		t.Fatalf("dropped act = %d %s", rec.Code, rec.Body)
	}
	if n := f.count(proto.OpAct); n != 1 {
		t.Fatalf("daemon saw act %d times, want exactly 1 (no retry)", n)
	}
	// The response carries a fresh view when one can be fetched.
	if e.Error.Details["view"] == nil || rec.Header().Get("ETag") == "" {
		t.Errorf("no fresh view: %s %v", rec.Body, rec.Header())
	}
}

func TestActDeadlineAboveDaemonAndHangIs504Unknown(t *testing.T) {
	if ActTimeout < 40*time.Second {
		t.Fatalf("ActTimeout = %s, want >= 40s (daemon act budget is lock 10s + key steps)", ActTimeout)
	}
	f := newFakeDaemon(t)
	f.on(proto.OpAct, func(net.Conn, *proto.Codec) { time.Sleep(3 * time.Second) })
	s := f.server(t)
	s.actTimeout = 200 * time.Millisecond
	rec := post(t, s.Handler(), "/v1/sessions/x/act", testToken, `{"action":"prompt","text":"hi"}`)
	var e errBody
	decodeBody(t, rec, &e)
	if rec.Code != 504 || e.Error.Code != "outcome_unknown" || e.Error.Retryable {
		t.Fatalf("hung act = %d %s", rec.Code, rec.Body)
	}
	if rec.Header().Get("Retry-After") != "" {
		t.Error("outcome_unknown must not carry Retry-After")
	}
}

func TestWritesNeverRetryAndUseFreshConnections(t *testing.T) {
	f := newFakeDaemon(t)
	s := f.server(t)
	// Warm the pool, then kill the idle connection: a write must dial afresh
	// and run exactly once instead of failing on (or retrying over) it.
	if err := s.WithDaemon(context.Background(), func(c *client.Client) error { _, e := c.Ping(); return e }); err != nil {
		t.Fatal(err)
	}
	s.pool.mu.Lock()
	for _, c := range s.pool.idle {
		_ = c.Close()
	}
	s.pool.mu.Unlock()
	rec := post(t, s.Handler(), "/v1/sessions/x/kill", testToken, "")
	if rec.Code != 202 || f.count(proto.OpKill) != 1 {
		t.Fatalf("kill = %d %s (daemon kills: %d)", rec.Code, rec.Body, f.count(proto.OpKill))
	}
	// A write that dies after dispatch is never repeated.
	f.on(proto.OpKill, dropConn)
	rec = post(t, s.Handler(), "/v1/sessions/x/kill", testToken, "")
	var e errBody
	decodeBody(t, rec, &e)
	if rec.Code != 502 || e.Error.Code != "outcome_unknown" || f.count(proto.OpKill) != 2 {
		t.Fatalf("dropped kill = %d %s (daemon kills: %d)", rec.Code, rec.Body, f.count(proto.OpKill))
	}
	// Same for send, reply, run, rm.
	for _, op := range []string{proto.OpSend, proto.OpReply, proto.OpRun, proto.OpRm} {
		f.on(op, dropConn)
	}
	for path, body := range map[string]string{
		"/v1/mailbox/messages":         `{"to":"x","body":"b"}`,
		"/v1/mailbox/messages/4/reply": `{"body":"b"}`,
		"/v1/sessions":                 `{"target":"/tmp","harness":"claude","name":"n1"}`,
	} {
		if rec := post(t, s.Handler(), path, testToken, body); rec.Code != 502 || !strings.Contains(rec.Body.String(), "outcome_unknown") {
			t.Errorf("%s = %d %s", path, rec.Code, rec.Body)
		}
	}
	if f.count(proto.OpSend) != 1 || f.count(proto.OpReply) != 1 || f.count(proto.OpRun) != 1 {
		t.Errorf("counts send=%d reply=%d run=%d, want 1 each", f.count(proto.OpSend), f.count(proto.OpReply), f.count(proto.OpRun))
	}
}

// ---- item 2: idempotency ----

func TestIdempotencyOutcomeUnknownBlocksReexecution(t *testing.T) {
	f := newFakeDaemon(t)
	f.on(proto.OpAct, dropConn)
	s := f.server(t)
	h := s.Handler()
	body := `{"action":"prompt","text":"once"}`
	if rec := post(t, h, "/v1/sessions/x/act", testToken, body, "Idempotency-Key", "k1"); rec.Code != 502 {
		t.Fatalf("first = %d %s", rec.Code, rec.Body)
	}
	rec := post(t, h, "/v1/sessions/x/act", testToken, body, "Idempotency-Key", "k1")
	var e errBody
	decodeBody(t, rec, &e)
	if rec.Code != 409 || e.Error.Code != "outcome_unknown" || e.Error.Retryable {
		t.Fatalf("retry = %d %s", rec.Code, rec.Body)
	}
	if f.count(proto.OpAct) != 1 {
		t.Fatalf("act executed %d times", f.count(proto.OpAct))
	}
}

func TestIdempotencyForgetsOnlyPreDispatchErrors(t *testing.T) {
	f := newFakeDaemon(t)
	s := f.server(t)
	var down atomic.Bool
	down.Store(true)
	s.Dial = func() (*client.Client, error) {
		if down.Load() {
			return nil, errors.New("down")
		}
		return client.Dial(f.sock)
	}
	h := s.Handler()
	body := `{"action":"prompt","text":"hello"}`
	if rec := post(t, h, "/v1/sessions/x/act", testToken, body, "Idempotency-Key", "k2"); rec.Code != 503 {
		t.Fatalf("dial failure = %d", rec.Code)
	}
	down.Store(false)
	if rec := post(t, h, "/v1/sessions/x/act", testToken, body, "Idempotency-Key", "k2"); rec.Code != 200 {
		t.Fatalf("retry after pre-dispatch failure = %d %s (must execute)", rec.Code, rec.Body)
	}
	// Validation errors are forgotten too.
	if rec := post(t, h, "/v1/sessions/x/act", testToken, `{"text":"x"}`, "Idempotency-Key", "k3"); rec.Code != 400 {
		t.Fatalf("validation = %d", rec.Code)
	}
	if n := s.idem.size(); n != 1 { // only k2's stored success
		t.Errorf("store holds %d entries, want 1", n)
	}
}

func TestIdempotentPanicFinalizesEntry(t *testing.T) {
	st := newIdemStore(time.Minute)
	boom := func(dispatch bool) Handler {
		return func(w http.ResponseWriter, r *http.Request) error {
			if dispatch {
				markDispatched(r.Context())
			}
			panic("boom")
		}
	}
	for dispatch, want := range map[bool]idemState{true: idemUnknown, false: idemNew} {
		h := idempotent(st, boom(dispatch))
		r := httptest.NewRequest("POST", "/x", strings.NewReader("{}"))
		r.Header.Set("Idempotency-Key", "p")
		func() {
			defer func() { _ = recover() }()
			_ = h(httptest.NewRecorder(), r)
		}()
		var hash [32]byte
		sum := hashOf("POST /x\n\n{}")
		copy(hash[:], sum)
		if got, _ := st.begin("", "p", hash); got != want {
			t.Errorf("after panic (dispatched=%v) begin = %v, want %v", dispatch, got, want)
		}
		st.forget("", "p")
	}
}

func TestIdempotencyIncludesIfMatch(t *testing.T) {
	f := newFakeDaemon(t)
	s := f.server(t)
	h := s.Handler()
	body := `{"action":"prompt","text":"a"}`
	if rec := post(t, h, "/v1/sessions/x/act", testToken, body, "Idempotency-Key", "im", "If-Match", "*"); rec.Code != 200 {
		t.Fatalf("first = %d %s", rec.Code, rec.Body)
	}
	if rec := post(t, h, "/v1/sessions/x/act", testToken, body, "Idempotency-Key", "im", "If-Match", `"e1-i1-r3"`); rec.Code != 422 {
		t.Fatalf("same key, other If-Match = %d, want 422", rec.Code)
	}
}

func TestIdempotencyPerDeviceQuotaAndLRU(t *testing.T) {
	st := newIdemStore(time.Hour)
	var h [32]byte
	for i := 0; i < idemMaxPerDevice+50; i++ {
		k := "k" + itoa(i)
		if s, _ := st.begin("dev1", k, h); s != idemNew {
			t.Fatalf("begin %d = %v", i, s)
		}
		st.finish("dev1", k, 200, http.Header{}, nil)
	}
	st.mu.Lock()
	n1 := st.dev["dev1"].Len()
	st.mu.Unlock()
	if n1 != idemMaxPerDevice {
		t.Fatalf("device holds %d entries, want %d", n1, idemMaxPerDevice)
	}
	// The oldest were evicted (new again), the newest still replay.
	if s, _ := st.begin("dev1", "k0", h); s != idemNew {
		t.Errorf("oldest should be evicted, got %v", s)
	}
	if s, _ := st.begin("dev1", "k"+itoa(idemMaxPerDevice+49), h); s != idemReplay {
		t.Errorf("newest should replay, got %v", s)
	}
	// Another device is unaffected by dev1's churn.
	if s, _ := st.begin("dev2", "k0", h); s != idemNew {
		t.Error("dev2 blocked")
	}
	// In-flight entries are never evicted: a device full of them is refused.
	st2 := newIdemStore(time.Hour)
	for i := 0; i < idemMaxPerDevice; i++ {
		st2.begin("d", "f"+itoa(i), h)
	}
	if s, _ := st2.begin("d", "extra", h); s != idemFull {
		t.Errorf("all in flight: begin = %v, want idemFull", s)
	}
}

// ---- item 4: health cache and in-flight cap ----

func TestHealthCachesPing(t *testing.T) {
	f := newFakeDaemon(t)
	s := f.server(t)
	h := s.Handler()
	for i := 0; i < 30; i++ {
		if rec := do(t, h, "GET", "/v1/health", "", nil); rec.Code != 200 {
			t.Fatalf("health = %d", rec.Code)
		}
	}
	if n := f.count(proto.OpPing); n != 1 {
		t.Fatalf("30 health probes caused %d daemon pings, want 1", n)
	}
	time.Sleep(healthTTL + 100*time.Millisecond)
	do(t, h, "GET", "/v1/health", "", nil)
	if n := f.count(proto.OpPing); n != 2 {
		t.Fatalf("after TTL pings = %d, want 2", n)
	}
}

func TestInflightCapAnswersBusy(t *testing.T) {
	f := newFakeDaemon(t)
	release := make(chan struct{})
	f.on(proto.OpLs, func(net.Conn, *proto.Codec) { <-release })
	s := f.server(t)
	h := s.Handler()
	var wg sync.WaitGroup
	codes := make(chan int, maxInflight+8)
	for i := 0; i < maxInflight+8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			rec := do(t, h, "GET", "/v1/sessions", testToken, nil)
			var e errBody
			_ = json.Unmarshal(rec.Body.Bytes(), &e)
			if rec.Code == 503 && e.Error.Code == "busy" && e.Error.Retryable && rec.Header().Get("Retry-After") == "1" {
				codes <- 503
				return
			}
			codes <- rec.Code
		}()
	}
	deadline := time.After(5 * time.Second)
	busy := 0
	for busy < 8 {
		select {
		case c := <-codes:
			if c == 503 {
				busy++
			}
		case <-deadline:
			t.Fatalf("only %d busy answers", busy)
		}
	}
	close(release)
	wg.Wait()
}

// ---- item 5, 6: Host, Origin, headers, limits ----

func TestHostAllowListDefaultsAnd421BeforeAuth(t *testing.T) {
	f := newFakeDaemon(t)
	s := f.server(t)
	s.Config.BindAddr = "192.0.2.7:7477"
	s.Config.TLSHosts = []string{"gw.tail.net"}
	s.Config.AllowedHosts = []string{"extra.example"}
	s.Config.Hostname = "myhost"
	h := s.Handler()
	for _, host := range []string{"localhost:7477", "127.0.0.1", "[::1]:7477", "192.0.2.7:7477", "myhost", "MYHOST.", "gw.tail.net", "extra.example"} {
		r := httptest.NewRequest("GET", "/v1/health", nil)
		r.Host = host
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, r)
		if rec.Code != 200 {
			t.Errorf("Host %q = %d", host, rec.Code)
		}
	}
	for _, path := range []string{"/v1/health", "/v1/info", "/v1/pair", "/nope"} {
		for _, host := range []string{"evil.example", "evil.example:7477", "192.0.2.8", ""} {
			r := httptest.NewRequest("GET", path, nil)
			r.Host = host
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, r)
			if rec.Code != 421 || errCode(t, rec) != "misdirected_request" {
				t.Errorf("GET %s Host %q = %d %s (want 421 before auth/routing)", path, host, rec.Code, rec.Body)
			}
		}
	}
}

func TestOriginPolicy(t *testing.T) {
	f := newFakeDaemon(t)
	s := f.server(t)
	h := s.Handler()
	get := func(origin string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("GET", "/v1/health", nil)
		r.Header.Set("Origin", origin)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, r)
		return rec
	}
	if rec := get("https://app.example"); rec.Code != 403 {
		t.Errorf("origin with empty cors_origins = %d, want 403", rec.Code)
	}
	if rec := get("null"); rec.Code != 403 {
		t.Errorf("Origin: null = %d, want 403", rec.Code)
	}
	s2 := f.server(t)
	s2.Config.CORSOrigins = []string{"https://app.example", "null"}
	h = s2.Handler()
	if rec := get("null"); rec.Code != 403 {
		t.Errorf("Origin: null must be rejected even if listed = %d", rec.Code)
	}
	if rec := get("https://app.example"); rec.Code != 200 || rec.Header().Get("Access-Control-Allow-Origin") != "https://app.example" {
		t.Errorf("allowed origin = %d %v", rec.Code, rec.Header())
	}
	if rec := get("https://other.example"); rec.Code != 403 {
		t.Errorf("foreign origin = %d", rec.Code)
	}
}

func TestSecurityHeadersAndContentType(t *testing.T) {
	f := newFakeDaemon(t)
	s := f.server(t)
	h := s.Handler()
	rec := do(t, h, "GET", "/v1/sessions", testToken, nil)
	if rec.Header().Get("X-Content-Type-Options") != "nosniff" || rec.Header().Get("Referrer-Policy") != "no-referrer" ||
		rec.Header().Get("Cache-Control") != "no-store" {
		t.Errorf("headers = %v", rec.Header())
	}
	rec = do(t, h, "GET", "/nope", testToken, nil)
	if rec.Header().Get("X-Content-Type-Options") != "nosniff" || rec.Header().Get("Cache-Control") != "no-store" {
		t.Errorf("error headers = %v", rec.Header())
	}
	// A POST with a body must be application/json.
	for _, ct := range []string{"text/plain", "application/x-www-form-urlencoded", ""} {
		r := httptest.NewRequest("POST", "/v1/mailbox/messages", strings.NewReader(`{"to":"x","body":"b"}`))
		r.Header.Set("Authorization", "Bearer "+testToken)
		if ct != "" {
			r.Header.Set("Content-Type", ct)
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, r)
		if rec.Code != 415 || errCode(t, rec) != "unsupported_media_type" {
			t.Errorf("Content-Type %q = %d %s", ct, rec.Code, rec.Body)
		}
	}
	r := httptest.NewRequest("POST", "/v1/mailbox/messages", strings.NewReader(`{"to":"x","body":"b"}`))
	r.Header.Set("Authorization", "Bearer "+testToken)
	r.Header.Set("Content-Type", "application/json; charset=utf-8")
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, r)
	if rec.Code != 201 {
		t.Errorf("json with charset = %d %s", rec.Code, rec.Body)
	}
	// A body-less POST needs no content type.
	if rec := do(t, h, "POST", "/v1/sessions/x/kill", testToken, nil); rec.Code != 202 {
		t.Errorf("kill without body = %d %s", rec.Code, rec.Body)
	}
}

func TestHeadRejectedOnStreamAndMailboxRoutes(t *testing.T) {
	f := newFakeDaemon(t)
	h := f.server(t).Handler()
	for _, p := range []string{"/v1/events", "/v1/sessions/x/view/stream", "/v1/mailbox", "/v1/mailbox/messages/1"} {
		rec := do(t, h, "HEAD", p, testToken, nil)
		if rec.Code != 405 || rec.Header().Get("Allow") != "GET" {
			t.Errorf("HEAD %s = %d %v", p, rec.Code, rec.Header())
		}
	}
}

func TestHTTPServerLimits(t *testing.T) {
	s := &Server{}
	s.init()
	hs := s.httpServer(context.Background())
	if hs.ReadHeaderTimeout != 10*time.Second || hs.IdleTimeout != 120*time.Second || hs.MaxHeaderBytes != 16<<10 {
		t.Fatalf("server limits = %v %v %v", hs.ReadHeaderTimeout, hs.IdleTimeout, hs.MaxHeaderBytes)
	}
}

func TestRateLimitPerDeviceAndAct(t *testing.T) {
	f := newFakeDaemon(t)
	s := f.server(t)
	s.Config.RateLimit, s.Config.RateBurst, s.Config.ActRateLimit = 2, 4, 1
	h := s.Handler()
	now := time.Now()
	s.limits.now = func() time.Time { return now }
	ok, limited := 0, 0
	for i := 0; i < 8; i++ {
		rec := do(t, h, "GET", "/v1/sessions", testToken, nil)
		switch rec.Code {
		case 200:
			ok++
		case 429:
			limited++
			if rec.Header().Get("Retry-After") == "" || errCode(t, rec) != "rate_limited" {
				t.Errorf("429 = %v %s", rec.Header(), rec.Body)
			}
		}
	}
	if ok != 4 || limited != 4 {
		t.Fatalf("burst: ok=%d limited=%d, want 4/4", ok, limited)
	}
	// Another device has its own bucket.
	if rec := do(t, h, "GET", "/v1/sessions", "read-token", nil); rec.Code != 200 {
		t.Errorf("other device = %d", rec.Code)
	}
	now = now.Add(10 * time.Second) // refilled
	acts := 0
	for i := 0; i < 5; i++ {
		if rec := post(t, h, "/v1/sessions/x/act", testToken, `{"action":"prompt","text":"a"}`); rec.Code == 200 {
			acts++
		}
	}
	if acts != 2 { // act burst = 2x its rate
		t.Errorf("acts admitted = %d, want 2 (act bucket is stricter)", acts)
	}
}

func TestFailedAuthRateLimitPerAddress(t *testing.T) {
	f := newFakeDaemon(t)
	h := f.server(t).Handler()
	try := func(addr, token string) int {
		r := httptest.NewRequest("GET", "/v1/sessions", nil)
		r.RemoteAddr = addr
		r.Header.Set("Authorization", "Bearer "+token)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, r)
		return rec.Code
	}
	for i := 0; i < failLimit; i++ {
		if c := try("198.51.100.1:1000", "wrong"); c != 401 {
			t.Fatalf("attempt %d = %d", i, c)
		}
	}
	if c := try("198.51.100.1:1001", "wrong"); c != 429 {
		t.Fatalf("after %d failures = %d, want 429", failLimit, c)
	}
	if c := try("198.51.100.1:1002", testToken); c != 429 {
		t.Fatalf("even a valid token from a blocked address = %d, want 429", c)
	}
	if c := try("198.51.100.2:1000", testToken); c != 200 {
		t.Fatalf("other address = %d", c)
	}
}

func TestStreamCaps(t *testing.T) {
	e := newSSEEnv(t, func(s *Server) {
		s.Config.MaxStreamsPerDevice = 2
		s.Config.MaxStreams = 3
	})
	url := e.ts.URL + "/v1/events"
	openSSE(t, url, testToken, "")
	openSSE(t, url, testToken, "")
	if _, code := openSSEStatus(t, url, testToken, ""); code != 429 {
		t.Fatalf("third stream of one device = %d, want 429", code)
	}
	openSSE(t, url, "read-token", "") // total 3
	if _, code := openSSEStatus(t, url, "read-token", ""); code != 429 {
		t.Fatalf("stream over the global cap = %d, want 429", code)
	}
}

func TestStreamCapsUnit(t *testing.T) {
	s := &Server{Config: Config{MaxStreamsPerDevice: 2, MaxStreams: 3}}
	r1, err1 := s.acquireStream("a")
	r2, err2 := s.acquireStream("a")
	_, err3 := s.acquireStream("a")
	if err1 != nil || err2 != nil || err3 == nil {
		t.Fatalf("per-device: %v %v %v", err1, err2, err3)
	}
	var ae *Error
	if !errors.As(err3, &ae) || ae.Status != 429 || ae.Code != "too_many_streams" {
		t.Fatalf("err = %#v", err3)
	}
	rb, errb := s.acquireStream("b")
	_, errc := s.acquireStream("c")
	if errb != nil || errc == nil {
		t.Fatalf("global cap: %v %v", errb, errc)
	}
	r1()
	r1() // double release is harmless
	if _, err := s.acquireStream("c"); err != nil {
		t.Fatalf("slot freed: %v", err)
	}
	r2()
	rb()
	d := &Server{}
	for i := 0; i < 8; i++ { // defaults 8 per device, 64 total
		if _, err := d.acquireStream("x"); err != nil {
			t.Fatalf("default cap hit at %d: %v", i, err)
		}
	}
	if _, err := d.acquireStream("x"); err == nil {
		t.Fatal("ninth stream of one device allowed")
	}
	for i := 0; i < 56; i++ {
		if _, err := d.acquireStream("dev" + itoa(i)); err != nil {
			t.Fatalf("total cap hit early at %d: %v", i, err)
		}
	}
	if _, err := d.acquireStream("last"); err == nil {
		t.Fatal("65th stream allowed")
	}
}

// ---- item 18: no leaking of daemon internals ----

func TestInternalDaemonErrorsAreMasked(t *testing.T) {
	for _, code := range []string{proto.CodeInternal, "weird", ""} {
		e := FromDaemonError(&proto.RemoteError{Code: code, Msg: "open /home/secret/path: boom"})
		if e.Status != 500 || e.Code != "internal" || strings.Contains(e.Message, "secret") || e.cause == nil {
			t.Errorf("code %q -> %+v", code, e)
		}
	}
	if e := FromDaemonError(&proto.RemoteError{Code: proto.CodeNotFound, Msg: `no such session "a"`}); e.Message == "internal error" {
		t.Error("known codes keep their message")
	}
	// The detail reaches the server log.
	f := newFakeDaemon(t)
	f.on(proto.OpKill, func(c net.Conn, cd *proto.Codec) {
		_ = cd.WriteResponse(proto.Response{Error: "stat /home/secret/path: boom", Code: "internal"})
	})
	var buf syncBuf
	s := f.server(t)
	s.Log = newTestLogger(&buf)
	rec := post(t, s.Handler(), "/v1/sessions/x/kill", testToken, "")
	if rec.Code != 500 || strings.Contains(rec.Body.String(), "secret") || !strings.Contains(buf.String(), "secret/path") {
		t.Fatalf("body=%s log=%s", rec.Body, buf.String())
	}
}

// ---- item 7, 8, 11: scopes and prompt size ----

func TestMailboxPeekRequiresAdmin(t *testing.T) {
	e := startREST(t)
	e.runSleeper(t, "pk")
	for _, tok := range []string{"read-token", "write-token"} {
		for _, p := range []string{"/v1/mailbox?mailbox=pk", "/v1/mailbox/messages/1?mailbox=pk"} {
			if rec := e.req(t, "GET", p, tok, ""); rec.Code != 403 || errCode(t, rec) != "forbidden" {
				t.Errorf("%s %s = %d %s", tok, p, rec.Code, rec.Body)
			}
		}
	}
	e.must(t, 200, "GET", "/v1/mailbox?mailbox=pk", "admin-token", "")
	e.must(t, 200, "GET", "/v1/mailbox", "read-token", "") // own mailbox: fine
}

func TestRunArgsRequireAdmin(t *testing.T) {
	e := startREST(t)
	rec := e.req(t, "POST", "/v1/sessions", "write-token", `{"target":"`+t.TempDir()+`","harness":"sh","name":"a1","args":["-c","true"]}`)
	if rec.Code != 403 || !strings.Contains(rec.Body.String(), "args") {
		t.Fatalf("write + args = %d %s", rec.Code, rec.Body)
	}
}

func TestActPromptMaxChars(t *testing.T) {
	f := newFakeDaemon(t)
	s := f.server(t)
	h := s.Handler()
	over := strings.Repeat("é", PromptMaxChars+1) // 2 bytes each: still far below the daemon's byte cap
	rec := post(t, h, "/v1/sessions/x/act", testToken, `{"action":"prompt","text":"`+over+`"}`)
	if rec.Code != 413 || errCode(t, rec) != "payload_too_large" || f.count(proto.OpAct) != 0 {
		t.Fatalf("over limit = %d %s (act calls %d)", rec.Code, rec.Body, f.count(proto.OpAct))
	}
	// Exactly the limit (in runes) goes through.
	ok := strings.Repeat("é", PromptMaxChars)
	s.Config.MaxBodyKB = 1024
	if rec := post(t, h, "/v1/sessions/x/act", testToken, `{"action":"prompt","text":"`+ok+`"}`); rec.Code != 200 {
		t.Fatalf("at limit = %d %s", rec.Code, rec.Body)
	}
	if PromptMaxChars*4 > proto.MaxActText {
		t.Errorf("PromptMaxChars (%d runes, up to %d bytes) exceeds proto.MaxActText (%d bytes)", PromptMaxChars, PromptMaxChars*4, proto.MaxActText)
	}
	info := jsonOf2[map[string]any](t, do(t, h, "GET", "/v1/info", testToken, nil))
	if int(info["limits"].(map[string]any)["prompt_max_chars"].(float64)) != PromptMaxChars {
		t.Errorf("info limits = %v", info["limits"])
	}
}

func jsonOf2[T any](t *testing.T, rec *httptest.ResponseRecorder) T {
	t.Helper()
	var v T
	if err := json.Unmarshal(rec.Body.Bytes(), &v); err != nil {
		t.Fatalf("body %q: %v", rec.Body.String(), err)
	}
	return v
}

// ---- item 12, 13, 14, 15, 16 ----

func TestQuantTailAndTrimView(t *testing.T) {
	for in, want := range map[int]int{1: 50, 50: 50, 51: 100, 100: 100, 150: 200, 201: 500, 501: maxTail, maxTail: maxTail} {
		if got := quantTail(in); got != want {
			t.Errorf("quantTail(%d) = %d, want %d", in, got, want)
		}
	}
	v := &proto.View{FirstLine: 10, Lines: make([]proto.ViewLine, 8)}
	tv := trimView(v, 3)
	if len(tv.Lines) != 3 || tv.FirstLine != 15 || len(v.Lines) != 8 {
		t.Errorf("trim = %d lines first %d (orig %d)", len(tv.Lines), tv.FirstLine, len(v.Lines))
	}
	if trimView(v, 100) != v {
		t.Error("no-op trim must return the same view")
	}
}

func TestViewCacheLRUAndByteBudget(t *testing.T) {
	var vc viewCache
	mk := func(name string, rev uint64, size int) *fetchedView {
		return &fetchedView{v: &proto.View{Name: name, Epoch: "e", Revision: rev}, inst: "i", size: size}
	}
	// Byte budget: three entries of 40% each cannot all stay.
	big := viewCacheBytes * 4 / 10
	vc.put("a", mk("a", 1, big))
	vc.put("b", mk("b", 1, big))
	vc.get("a") // a is now the most recent
	vc.put("c", mk("c", 1, big))
	if vc.get("b") != nil || vc.get("a") == nil || vc.get("c") == nil {
		t.Fatalf("LRU eviction wrong: a=%v b=%v c=%v", vc.get("a") != nil, vc.get("b") != nil, vc.get("c") != nil)
	}
	if vc.bytes > viewCacheBytes {
		t.Fatalf("bytes %d over budget", vc.bytes)
	}
	// Entry cap.
	var v2 viewCache
	for i := 0; i < viewCacheMax+20; i++ {
		v2.put("k"+itoa(i), mk("k", 1, 10))
	}
	if v2.lru.Len() != viewCacheMax || v2.get("k0") != nil || v2.get("k"+itoa(viewCacheMax+19)) == nil {
		t.Fatalf("entry cap: len %d", v2.lru.Len())
	}
	// An older revision of the same incarnation does not replace a newer one.
	v2.put("z", mk("z", 9, 10))
	v2.put("z", mk("z", 4, 10))
	if v2.get("z").v.Revision != 9 {
		t.Error("older revision replaced a newer one")
	}
	v2.clear()
	if v2.get("z") != nil || v2.bytes != 0 {
		t.Error("clear")
	}
}

func TestViewStreamResumeAcrossSessionRecreation(t *testing.T) {
	e := newSSEEnv(t, nil)
	e.run(t, "re", "sh", "-c", "sleep 60")
	c1 := openSSE(t, e.ts.URL+"/v1/sessions/re/view/stream", "read-token", "")
	f1 := c1.waitFor(t, 5*time.Second, "view", kindIs("view"))
	ref1, ok := parseETagRef(`"` + f1.id + `"`)
	if !ok || ref1.inst == "" {
		t.Fatalf("event id %q has no session instance", f1.id)
	}
	c1.close()
	dc := e.daemon(t)
	if err := dc.Kill("re", "KILL"); err != nil {
		t.Fatal(err)
	}
	time.Sleep(300 * time.Millisecond)
	if err := dc.Rm("re"); err != nil {
		t.Fatal(err)
	}
	e.run(t, "re", "sh", "-c", "sleep 60")
	// Resuming with the old id must give a FULL view of the new session,
	// never a patch against the old one.
	c2 := openSSE(t, e.ts.URL+"/v1/sessions/re/view/stream", "read-token", f1.id)
	f2 := c2.waitFor(t, 5*time.Second, "first view", func(f sseFrame) bool { return f.event == "view" || f.event == "view.patch" })
	if f2.event != "view" {
		t.Fatalf("resumed across a re-created session with %s", f2.event)
	}
	ref2, _ := parseETagRef(`"` + f2.id + `"`)
	if ref2.inst == ref1.inst {
		t.Errorf("instance did not change: %q", ref2.inst)
	}
}

func TestHubResyncFloorAndNonAutostartingDialer(t *testing.T) {
	var reqDials, hubDials atomic.Int32
	f := newFakeDaemon(t)
	s := f.server(t)
	s.Dial = func() (*client.Client, error) { reqDials.Add(1); return client.Dial(f.sock) }
	s.HubDial = func() (*client.Client, error) { hubDials.Add(1); return nil, errors.New("daemon down") }
	h := s.hubFor()
	h.readyOnce.Do(func() { close(h.ready) })
	h.mu.Lock()
	h.epoch, h.head = "E", 5
	h.mu.Unlock()
	attachID := func(id string) string {
		sb, _, err := h.attach(context.Background(), eventFilter{}, id)
		if err != nil {
			t.Fatal(err)
		}
		defer h.detach(sb)
		_, reason, _ := sb.take()
		return reason
	}
	if r := attachID("E:5"); r != "" {
		t.Fatalf("up-to-date resume got resync %q", r)
	}
	// A daemon-side resync leaves a gap: an id from before it cannot resume,
	// even though it equals the head and the ring is empty.
	d, _ := json.Marshal(proto.ResyncData{Reason: "since_seq_too_old"})
	h.handle(proto.Event{Kind: proto.EvResync, Data: d})
	if r := attachID("E:5"); r != proto.ResyncSinceTooOld {
		t.Fatalf("resume across a resync gap got %q", r)
	}
	if r := attachID("E:3"); r != proto.ResyncSinceTooOld {
		t.Fatalf("older id got %q", r)
	}
	// The hub dials with HubDial, never with the request dialer.
	deadline := time.Now().Add(3 * time.Second)
	for hubDials.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if hubDials.Load() == 0 || reqDials.Load() != 0 {
		t.Fatalf("hub dials=%d request dials=%d", hubDials.Load(), reqDials.Load())
	}
}

func TestETagListsAndWildcards(t *testing.T) {
	if !etagMatches(`"a", W/"b", "c"`, `"b"`) || !etagMatches(`*`, `"zzz"`) || etagMatches(`"a","c"`, `"b"`) {
		t.Error("etagMatches (weak comparison, list, star)")
	}
	if !etagMatches(`W/"b"`, `"b"`) || !etagMatches(`"b"`, `W/"b"`) {
		t.Error("weak comparison ignores W/")
	}
	tags, star := splitETagList(`"a,b", "c"`)
	if star || len(tags) != 2 || tags[0] != `"a,b"` {
		t.Errorf("split = %v %v", tags, star)
	}
	if _, star := splitETagList(" * "); !star {
		t.Error("star")
	}
}

func TestIfMatchStarAndListOnAct(t *testing.T) {
	e := startREST(t)
	e.runFake(t, "ifm", "idle")
	rec := e.must(t, 200, "GET", "/v1/sessions/ifm/view", "read-token", "")
	etag := rec.Header().Get("ETag")
	act := `{"action":"prompt","text":"hello"}`
	e.must(t, 200, "POST", "/v1/sessions/ifm/act", "write-token", act, "If-Match", "*")
	e.waitStatus(t, "ifm", "idle")
	cur := e.must(t, 200, "GET", "/v1/sessions/ifm/view", "read-token", "").Header().Get("ETag")
	// A list holding a stale tag and the current one passes.
	e.must(t, 200, "POST", "/v1/sessions/ifm/act", "write-token", act, "If-Match", `"e0-i0-r1", `+cur)
	e.waitStatus(t, "ifm", "idle")
	cur = e.must(t, 200, "GET", "/v1/sessions/ifm/view", "read-token", "").Header().Get("ETag")
	// Weak validators never match If-Match (strong comparison).
	r := e.must(t, 412, "POST", "/v1/sessions/ifm/act", "write-token", act, "If-Match", "W/"+cur)
	if errCode(t, r) != "precondition_failed" || r.Header().Get("ETag") == "" {
		t.Errorf("weak If-Match = %s", r.Body)
	}
	// A stale session incarnation in the ETag is refused with the fresh view.
	ref, _ := parseETagRef(cur)
	stale := formatETag(ref.epoch, "zzz", ref.rev)
	r = e.must(t, 412, "POST", "/v1/sessions/ifm/act", "write-token", act, "If-Match", stale)
	if !strings.Contains(r.Body.String(), `"view"`) {
		t.Errorf("412 without fresh view: %s", r.Body)
	}
	_ = etag
	// Garbage is a 400.
	e.must(t, 400, "POST", "/v1/sessions/ifm/act", "write-token", act, "If-Match", "banana")
	// If-None-Match on view: list and weak forms; another incarnation is a miss.
	e.must(t, 304, "GET", "/v1/sessions/ifm/view", "read-token", "", "If-None-Match", `"zzz", W/`+cur)
	e.must(t, 200, "GET", "/v1/sessions/ifm/view", "read-token", "", "If-None-Match", stale)
}

// ---- item 22 helper: logger ----

func newTestLogger(w *syncBuf) *slog.Logger { return slog.New(slog.NewTextHandler(w, nil)) }

func hashOf(s string) []byte {
	h := sha256.Sum256([]byte(s))
	return h[:]
}
