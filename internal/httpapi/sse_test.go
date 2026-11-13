package httpapi

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"math/rand"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/digiogithub/agentd/internal/client"
	"github.com/digiogithub/agentd/internal/proto"
)

// ---- helpers ----

type sseFrame struct {
	id, event, data string
	comment         string
	at              time.Time
}

type sseConn struct {
	resp   *http.Response
	frames chan sseFrame
	cancel context.CancelFunc
}

func openSSEStatus(t *testing.T, url, token, lastID string) (*sseConn, int) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	req, _ := http.NewRequestWithContext(ctx, "GET", url, nil)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if lastID != "" {
		req.Header.Set("Last-Event-ID", lastID)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	c := &sseConn{resp: resp, frames: make(chan sseFrame, 4096), cancel: cancel}
	t.Cleanup(c.close)
	if resp.StatusCode != 200 {
		return c, resp.StatusCode
	}
	go func() {
		defer close(c.frames)
		sc := bufio.NewScanner(resp.Body)
		sc.Buffer(make([]byte, 1<<20), 8<<20)
		var f sseFrame
		for sc.Scan() {
			line := sc.Text()
			switch {
			case line == "":
				if f.event != "" || f.comment != "" {
					f.at = time.Now()
					c.frames <- f
				}
				f = sseFrame{}
			case strings.HasPrefix(line, ": "):
				f.comment = line[2:]
			case strings.HasPrefix(line, "id: "):
				f.id = line[4:]
			case strings.HasPrefix(line, "event: "):
				f.event = line[7:]
			case strings.HasPrefix(line, "data: "):
				f.data = line[6:]
			}
		}
	}()
	return c, 200
}

func openSSE(t *testing.T, url, token, lastID string) *sseConn {
	t.Helper()
	c, code := openSSEStatus(t, url, token, lastID)
	if code != 200 {
		t.Fatalf("GET %s = %d", url, code)
	}
	return c
}

func (c *sseConn) close() {
	c.cancel()
	_ = c.resp.Body.Close()
}

// next returns the next non-comment frame.
func (c *sseConn) next(t *testing.T, d time.Duration) sseFrame {
	t.Helper()
	for {
		f, ok := c.nextAny(t, d)
		if !ok {
			t.Fatal("stream ended")
		}
		if f.comment == "" {
			return f
		}
	}
}

func (c *sseConn) nextAny(t *testing.T, d time.Duration) (sseFrame, bool) {
	t.Helper()
	select {
	case f, ok := <-c.frames:
		return f, ok
	case <-time.After(d):
		t.Fatal("timeout waiting for an SSE frame")
	}
	return sseFrame{}, false
}

// waitFor skips frames until pred matches.
func (c *sseConn) waitFor(t *testing.T, d time.Duration, what string, pred func(sseFrame) bool) sseFrame {
	t.Helper()
	deadline := time.After(d)
	for {
		select {
		case f, ok := <-c.frames:
			if !ok {
				t.Fatalf("stream ended while waiting for %s", what)
			}
			if f.comment == "" && pred(f) {
				return f
			}
		case <-deadline:
			t.Fatalf("timeout waiting for %s", what)
		}
	}
}

func kindIs(k string) func(sseFrame) bool { return func(f sseFrame) bool { return f.event == k } }

// ended reports whether the stream closes within d (draining leftovers).
func (c *sseConn) ended(d time.Duration) bool {
	deadline := time.After(d)
	for {
		select {
		case _, ok := <-c.frames:
			if !ok {
				return true
			}
		case <-deadline:
			return false
		}
	}
}

type sseEnv struct {
	d  *daemonEnv
	s  *Server
	ts *httptest.Server
}

func newSSEEnv(t *testing.T, mod func(*Server)) *sseEnv {
	t.Helper()
	d := startDaemon(t)
	s := newServer(t, d, testAuth(), nil)
	s.Config.SSEHeartbeat = 30 * time.Second
	if mod != nil {
		mod(s)
	}
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(ts.Close)
	t.Cleanup(s.Close) // runs before ts.Close: streams say bye and return
	return &sseEnv{d: d, s: s, ts: ts}
}

func (e *sseEnv) events(t *testing.T, query string) *sseConn {
	return openSSE(t, e.ts.URL+"/v1/events"+query, "read-token", "")
}

func (e *sseEnv) daemon(t *testing.T) *client.Client {
	t.Helper()
	c, err := client.Dial(e.d.sock)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return c
}

func (e *sseEnv) run(t *testing.T, name string, cmd ...string) {
	t.Helper()
	if _, err := e.daemon(t).Run(proto.RunArgs{Name: name, Cwd: t.TempDir(), Cmd: cmd, Profile: "default"}); err != nil {
		t.Fatal(err)
	}
}

type evData struct {
	Type    string          `json:"type"`
	Session string          `json:"session"`
	TS      time.Time       `json:"ts"`
	Data    json.RawMessage `json:"data"`
}

func decodeEv(t *testing.T, f sseFrame) evData {
	t.Helper()
	var d evData
	if err := json.Unmarshal([]byte(f.data), &d); err != nil {
		t.Fatalf("frame %q: %v", f.data, err)
	}
	return d
}

// ---- /v1/events ----

func TestEventsHelloAndLive(t *testing.T) {
	e := newSSEEnv(t, nil)
	if rec := do(t, e.s.Handler(), "GET", "/v1/events", "", nil); rec.Code != 401 {
		t.Fatalf("no token = %d", rec.Code)
	}
	c := e.events(t, "")
	if ct := c.resp.Header.Get("Content-Type"); ct != "text/event-stream" {
		t.Fatalf("content-type %q", ct)
	}
	if c.resp.Header.Get("Cache-Control") != "no-cache" || c.resp.Header.Get("X-Accel-Buffering") != "no" {
		t.Fatalf("headers %v", c.resp.Header)
	}
	hello := c.next(t, 5*time.Second)
	if hello.event != "hello" || hello.id != "" {
		t.Fatalf("first frame %+v", hello)
	}
	var hd struct {
		Data struct {
			Epoch      string `json:"epoch"`
			Seq        uint64 `json:"seq"`
			APIVersion string `json:"api_version"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(hello.data), &hd); err != nil || hd.Data.Epoch == "" || hd.Data.APIVersion != APIVersion {
		t.Fatalf("hello %s %v", hello.data, err)
	}

	e.run(t, "live@a", "sh", "-c", "echo hi; sleep 60")
	f := c.waitFor(t, 5*time.Second, "session.created", kindIs("session.created"))
	d := decodeEv(t, f)
	if d.Session != "live@a" || d.Type != "session.created" || d.TS.IsZero() || !strings.HasPrefix(f.id, hd.Data.Epoch+":") {
		t.Fatalf("frame %+v data %+v", f, d)
	}
	c.waitFor(t, 5*time.Second, "session.started", kindIs("session.started"))
}

func TestEventsFilters(t *testing.T) {
	e := newSSEEnv(t, nil)
	onlyA := e.events(t, "?session=a")
	onlyMail := e.events(t, "?kind=mail.*")
	lifecycle := e.events(t, "?kind=session&session=b")
	for _, c := range []*sseConn{onlyA, onlyMail, lifecycle} {
		c.next(t, 5*time.Second) // hello
	}
	e.run(t, "a", "sh", "-c", "sleep 60")
	e.run(t, "b", "sh", "-c", "sleep 60")
	if _, err := e.daemon(t).Send(proto.SendArgs{To: "a", Body: "x"}); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		f := onlyA.next(t, 5*time.Second)
		if decodeEv(t, f).Session != "a" {
			t.Fatalf("session filter leaked %+v", f)
		}
	}
	m := onlyMail.next(t, 5*time.Second)
	if !strings.HasPrefix(m.event, "mail.") {
		t.Fatalf("kind filter got %q", m.event)
	}
	f := lifecycle.next(t, 5*time.Second)
	if !strings.HasPrefix(f.event, "session.") || decodeEv(t, f).Session != "b" {
		t.Fatalf("combined filter got %+v", f)
	}
}

func TestEventsResume(t *testing.T) {
	e := newSSEEnv(t, nil)
	c := e.events(t, "")
	hello := c.next(t, 5*time.Second)
	e.run(t, "r1", "sh", "-c", "sleep 60")
	last := c.waitFor(t, 5*time.Second, "session.started", kindIs("session.started"))
	c.close()

	// events missed while disconnected
	e.run(t, "r2", "sh", "-c", "sleep 60")
	e.run(t, "r3", "sh", "-c", "sleep 60")

	c2 := openSSE(t, e.ts.URL+"/v1/events", "read-token", last.id)
	if c2.next(t, 5*time.Second).event != "hello" {
		t.Fatal("no hello on resume")
	}
	seen := map[string]bool{}
	var prevSeq uint64
	deadline := time.After(5 * time.Second)
	for len(seen) < 4 { // r2/r3 created+started
		select {
		case f := <-c2.frames:
			if f.comment != "" {
				continue
			}
			if f.event == "resync" {
				t.Fatalf("unexpected resync on resume: %+v", f)
			}
			d := decodeEv(t, f)
			_, seq, ok := parseEventID(f.id)
			if !ok || seq <= prevSeq {
				t.Fatalf("ids not increasing: %q after %d", f.id, prevSeq)
			}
			prevSeq = seq
			if _, ls, _ := parseEventID(last.id); seq <= ls {
				t.Fatalf("replayed an event already seen: %+v", f)
			}
			if d.Session != "r1" && (f.event == "session.created" || f.event == "session.started") {
				seen[d.Session+"/"+f.event] = true
			}
		case <-deadline:
			t.Fatalf("missing replayed events, got %v", seen)
		}
	}
	_ = hello

	// Unknown / foreign / future ids => resync.
	for _, id := range []string{"garbage", "other-epoch:3", fmt.Sprintf("%s:%d", strings.Split(last.id, ":")[0], 999999)} {
		cx := openSSE(t, e.ts.URL+"/v1/events", "read-token", id)
		cx.next(t, 5*time.Second) // hello
		rs := cx.next(t, 5*time.Second)
		if rs.event != "resync" {
			t.Fatalf("id %q: got %+v, want resync", id, rs)
		}
		cx.close()
	}
}

func TestEventsResumeOutOfRing(t *testing.T) {
	e := newSSEEnv(t, func(s *Server) { s.Config.EventBuffer = 4 })
	c := e.events(t, "")
	c.next(t, 5*time.Second)
	e.run(t, "o1", "sh", "-c", "sleep 60")
	first := c.waitFor(t, 5*time.Second, "created", kindIs("session.created"))
	for i := 2; i <= 6; i++ {
		e.run(t, fmt.Sprintf("o%d", i), "sh", "-c", "sleep 60")
	}
	c.waitFor(t, 5*time.Second, "last started", func(f sseFrame) bool {
		return f.event == "session.started" && decodeEv(t, f).Session == "o6"
	})
	cx := openSSE(t, e.ts.URL+"/v1/events", "read-token", first.id)
	cx.next(t, 5*time.Second)
	rs := cx.next(t, 5*time.Second)
	if rs.event != "resync" || !strings.Contains(rs.data, proto.ResyncSinceTooOld) {
		t.Fatalf("got %+v, want resync since_seq_too_old", rs)
	}
}

func TestSlowClientOverflowDoesNotBlockOthers(t *testing.T) {
	e := newSSEEnv(t, func(s *Server) { s.sseQueue = 4 })
	h := e.s.hubFor()
	fast := e.events(t, "")
	fast.next(t, 5*time.Second)
	// A subscriber that never drains its queue.
	slow, _, err := h.attach(context.Background(), eventFilter{}, "")
	if err != nil {
		t.Fatal(err)
	}
	defer h.detach(slow)

	const n = 6 // 6 sessions => 12+ events, far over the queue of 4
	for i := 0; i < n; i++ {
		e.run(t, fmt.Sprintf("s%d", i), "sh", "-c", "sleep 60")
	}
	got := 0
	for got < 2*n {
		f := fast.next(t, 5*time.Second)
		if f.event == "session.created" || f.event == "session.started" {
			got++
		}
	}
	evs, reason, id := slow.take()
	if reason != "overflow" || id == "" || len(evs) != 0 {
		t.Fatalf("slow subscriber: reason=%q id=%q queued=%d", reason, id, len(evs))
	}
	// After the resync it continues with later events.
	e.run(t, "after", "sh", "-c", "sleep 60")
	fast.waitFor(t, 5*time.Second, "after", func(f sseFrame) bool { return decodeEv(t, f).Session == "after" })
	if evs, reason, _ := slow.take(); reason != "" || len(evs) == 0 {
		t.Fatalf("slow subscriber did not continue: reason=%q queued=%d", reason, len(evs))
	}
}

func TestOneDaemonSubscriptionForManyClients(t *testing.T) {
	e := newSSEEnv(t, nil)
	const n = 12
	var conns []*sseConn
	for i := 0; i < n; i++ {
		c := e.events(t, "")
		c.next(t, 5*time.Second)
		conns = append(conns, c)
	}
	if got := e.s.hub.daemonSubs.Load(); got != 1 {
		t.Fatalf("daemon subscriptions = %d, want 1", got)
	}
	if got := e.s.hub.numSubscribers(); got != n {
		t.Fatalf("http subscribers = %d, want %d", got, n)
	}
	e.run(t, "fan", "sh", "-c", "sleep 60")
	var wg sync.WaitGroup
	for _, c := range conns {
		wg.Add(1)
		go func() {
			defer wg.Done()
			c.waitFor(t, 5*time.Second, "fan created", kindIs("session.created"))
		}()
	}
	wg.Wait()
	if got := e.s.hub.daemonSubs.Load(); got != 1 {
		t.Fatalf("daemon subscriptions after traffic = %d", got)
	}
}

func TestHeartbeatAndBye(t *testing.T) {
	e := newSSEEnv(t, func(s *Server) { s.Config.SSEHeartbeat = 40 * time.Millisecond })
	ev := e.events(t, "")
	ev.next(t, 5*time.Second)
	e.run(t, "hb", "sh", "-c", "sleep 60")
	vs := openSSE(t, e.ts.URL+"/v1/sessions/hb/view/stream", "read-token", "")
	vs.next(t, 5*time.Second) // view
	pings := 0
	deadline := time.After(3 * time.Second)
	for pings < 3 {
		f, ok := ev.nextAny(t, 3*time.Second)
		if !ok {
			t.Fatal("ended")
		}
		if f.comment == "ping" {
			pings++
		}
		select {
		case <-deadline:
			t.Fatal("no heartbeat")
		default:
		}
	}
	e.s.Close()
	bye := ev.waitFor(t, 5*time.Second, "bye", kindIs("bye"))
	if !strings.Contains(bye.data, "shutdown") {
		t.Fatalf("bye %+v", bye)
	}
	vs.waitFor(t, 5*time.Second, "view bye", kindIs("bye"))
	if !ev.ended(5*time.Second) || !vs.ended(5*time.Second) {
		t.Fatal("streams did not end after bye")
	}
	// New streams are refused while draining.
	if rec := do(t, e.s.Handler(), "GET", "/v1/events", "read-token", nil); rec.Code != 503 {
		t.Fatalf("events while draining = %d", rec.Code)
	}
}

func TestStreamsGoroutinesStable(t *testing.T) {
	e := newSSEEnv(t, nil)
	e.run(t, "g", "sh", "-c", "sleep 60")
	cycle := func() {
		a := openSSE(t, e.ts.URL+"/v1/events", "read-token", "")
		a.next(t, 5*time.Second)
		b := openSSE(t, e.ts.URL+"/v1/sessions/g/view/stream", "read-token", "")
		b.next(t, 5*time.Second)
		a.close()
		b.close()
	}
	cycle() // warm up: hub, pool, transport
	settle := func() int {
		var n int
		for i := 0; i < 20; i++ {
			time.Sleep(50 * time.Millisecond)
			n = runtime.NumGoroutine()
		}
		return n
	}
	base := settle()
	for i := 0; i < 30; i++ {
		cycle()
	}
	if e.s.hub.numSubscribers() != 0 {
		waitTrue(t, func() bool { return e.s.hub.numSubscribers() == 0 }, "subscribers detached")
	}
	after := settle()
	if after > base+4 {
		buf := make([]byte, 1<<16)
		t.Fatalf("goroutines grew %d -> %d\n%s", base, after, buf[:runtime.Stack(buf, true)])
	}
}

func waitTrue(t *testing.T, f func() bool, what string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if f() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timeout: %s", what)
}

func TestFilterMatch(t *testing.T) {
	ev := &hubEvent{kind: "mail.queued", session: "x"}
	for _, tc := range []struct {
		kinds []string
		want  bool
	}{
		{nil, true}, {[]string{"mail.queued"}, true}, {[]string{"mail.*"}, true}, {[]string{"mail"}, true},
		{[]string{"session", "status"}, false}, {[]string{"mailx"}, false},
	} {
		if got := (eventFilter{kinds: tc.kinds}).match(ev); got != tc.want {
			t.Errorf("kinds %v: got %v", tc.kinds, got)
		}
	}
}

// ---- view stream ----

// localView is a client mirroring a stream.
type localView struct {
	v       proto.View
	have    bool
	patches int
	fulls   int
	times   []time.Time
}

func (l *localView) apply(t *testing.T, f sseFrame) {
	t.Helper()
	switch f.event {
	case "view":
		var v proto.View
		if err := json.Unmarshal([]byte(f.data), &v); err != nil {
			t.Fatal(err)
		}
		l.v, l.have = v, true
		l.fulls++
	case "view.patch":
		var p viewPatch
		if err := json.Unmarshal([]byte(f.data), &p); err != nil {
			t.Fatal(err)
		}
		nv, err := applyViewPatch(l.v, p)
		if err != nil {
			t.Fatalf("apply patch %s: %v", f.data, err)
		}
		l.v = nv
		l.patches++
	default:
		return
	}
	l.times = append(l.times, f.at)
}

func sameView(a, b proto.View) bool {
	a.IdleMS, b.IdleMS = 0, 0
	if len(a.Lines) == 0 {
		a.Lines = nil
	}
	if len(b.Lines) == 0 {
		b.Lines = nil
	}
	x, _ := json.Marshal(a)
	y, _ := json.Marshal(b)
	return string(x) == string(y)
}

func TestViewStreamPatchesMatchFreshView(t *testing.T) {
	e := newSSEEnv(t, nil)
	e.run(t, "vcat", "cat")
	d := e.daemon(t)
	c := openSSE(t, e.ts.URL+"/v1/sessions/vcat/view/stream?tail=40", "read-token", "")
	first := c.next(t, 5*time.Second)
	if first.event != "view" || !strings.HasPrefix(first.id, "e") || !strings.Contains(first.id, "-r") {
		t.Fatalf("first frame %+v", first)
	}
	var lv localView
	lv.apply(t, first)

	rng := rand.New(rand.NewSource(7))
	for round := 0; round < 5; round++ {
		for i, n := 0, 1+rng.Intn(30); i < n; i++ {
			text := fmt.Sprintf("round %d line %d %s", round, i, strings.Repeat("x", rng.Intn(20)))
			if err := d.Type(proto.TypeArgs{Name: "vcat", Text: text, Submit: true}); err != nil {
				t.Fatal(err)
			}
		}
		// Converge: apply frames until the local view equals a fresh fetch.
		deadline := time.Now().Add(8 * time.Second)
		for {
			fresh, err := d.View(proto.ViewArgs{Name: "vcat", LinesLimit: 40})
			if err != nil {
				t.Fatal(err)
			}
			if lv.v.Revision >= fresh.Revision {
				if !sameView(lv.v, fresh) {
					a, _ := json.Marshal(lv.v)
					b, _ := json.Marshal(fresh)
					t.Fatalf("round %d: patched view differs from fresh view\nlocal: %s\nfresh: %s", round, a, b)
				}
				break
			}
			if time.Now().After(deadline) {
				t.Fatalf("round %d: stream stuck at r%d, fresh r%d", round, lv.v.Revision, fresh.Revision)
			}
			select {
			case f, ok := <-c.frames:
				if !ok {
					t.Fatal("stream ended")
				}
				lv.apply(t, f)
			case <-time.After(100 * time.Millisecond):
			}
		}
	}
	if lv.patches == 0 {
		t.Fatalf("no view.patch observed (fulls=%d)", lv.fulls)
	}
	for i := 1; i < len(lv.times); i++ {
		if gap := lv.times[i].Sub(lv.times[i-1]); gap < 80*time.Millisecond {
			t.Fatalf("view events %d and %d only %v apart (want >= ~100ms)", i-1, i, gap)
		}
	}
}

func TestViewStreamResumeAndEnd(t *testing.T) {
	e := newSSEEnv(t, nil)
	e.run(t, "vend", "sh", "-c", "echo one; sleep 1; echo two; sleep 1; exit 3")
	d := e.daemon(t)
	c := openSSE(t, e.ts.URL+"/v1/sessions/vend/view/stream", "read-token", "")
	var lv localView
	lv.apply(t, c.next(t, 5*time.Second))
	lastID := ""
	for lv.v.Status != "" && lastID == "" {
		f := c.next(t, 5*time.Second)
		lv.apply(t, f)
		if f.event == "view" || f.event == "view.patch" {
			lastID = f.id
		}
	}
	c.close()
	// Resume with the id of the last view seen: no full view unless needed,
	// and the stream ends with session.exited + bye.
	c2 := openSSE(t, e.ts.URL+"/v1/sessions/vend/view/stream", "read-token", lastID)
	var lv2 localView
	lv2.v, lv2.have = lv.v, true
	sawExit := false
	deadline := time.After(10 * time.Second)
loop:
	for {
		select {
		case f, ok := <-c2.frames:
			if !ok {
				break loop
			}
			if f.comment != "" {
				continue
			}
			if f.event == "view" && lv2.fulls == 0 && lv2.patches == 0 && lastID == "" {
				t.Fatal("unexpected full view on resume")
			}
			lv2.apply(t, f)
			if f.event == "session.exited" {
				sawExit = true
			}
			if f.event == "bye" {
				if !sawExit {
					t.Fatal("bye before session.exited")
				}
				break loop
			}
		case <-deadline:
			t.Fatal("stream did not end after exit")
		}
	}
	fresh, err := d.View(proto.ViewArgs{Name: "vend"})
	if err != nil {
		t.Fatal(err)
	}
	if !sameView(lv2.v, fresh) || lv2.v.ExitCode == nil || *lv2.v.ExitCode != 3 {
		t.Fatalf("final view mismatch: local %+v fresh %+v", lv2.v, fresh)
	}
}

func TestViewStreamErrors(t *testing.T) {
	e := newSSEEnv(t, nil)
	if _, code := openSSEStatus(t, e.ts.URL+"/v1/sessions/nope/view/stream", "read-token", ""); code != 404 {
		t.Fatalf("unknown session = %d", code)
	}
	e.run(t, "ve", "sh", "-c", "sleep 60")
	if _, code := openSSEStatus(t, e.ts.URL+"/v1/sessions/ve/view/stream?tail=0", "read-token", ""); code != 400 {
		t.Fatalf("bad tail = %d", code)
	}
	if _, code := openSSEStatus(t, e.ts.URL+"/v1/sessions/ve/view/stream", "", ""); code != 401 {
		t.Fatalf("no token = %d", code)
	}
}

func TestViewStreamRemoved(t *testing.T) {
	e := newSSEEnv(t, nil)
	e.run(t, "vrm", "sh", "-c", "sleep 60")
	c := openSSE(t, e.ts.URL+"/v1/sessions/vrm/view/stream", "read-token", "")
	c.next(t, 5*time.Second)
	d := e.daemon(t)
	if err := d.Kill("vrm", "KILL"); err != nil {
		t.Fatal(err)
	}
	waitTrue(t, func() bool {
		l, _ := d.Ls()
		return len(l) == 1 && l[0].State == "exited"
	}, "exited")
	c.waitFor(t, 5*time.Second, "session.exited", kindIs("session.exited"))
	c.waitFor(t, 5*time.Second, "bye", kindIs("bye"))
}

// ---- patch algebra (property-style) ----

func randView(rng *rand.Rand, first, n int) proto.View {
	v := proto.View{Name: "x", Epoch: "e", Revision: 1, Status: "idle", FirstLine: first}
	for i := 0; i < n; i++ {
		v.Lines = append(v.Lines, proto.ViewLine{Text: fmt.Sprintf("l%d", rng.Intn(6)), Role: []string{"transcript", "status"}[rng.Intn(2)]})
	}
	return v
}

func TestDiffViewsProperty(t *testing.T) {
	rng := rand.New(rand.NewSource(1))
	for i := 0; i < 500; i++ {
		old := randView(rng, rng.Intn(5), rng.Intn(12))
		cur := randView(rng, old.FirstLine+rng.Intn(15), rng.Intn(12))
		cur.Revision = old.Revision + 1 + uint64(rng.Intn(3))
		cur.Status = []string{"idle", "working", "waiting"}[rng.Intn(3)]
		if rng.Intn(2) == 0 {
			cur.Dialog = &proto.ViewDialog{Question: "q"}
		}
		if rng.Intn(3) == 0 {
			code := rng.Intn(3)
			cur.ExitCode = &code
		}
		if rng.Intn(2) == 0 { // tail drop of the head, overlapping lines
			if k := rng.Intn(len(old.Lines) + 1); k <= len(old.Lines) && rng.Intn(2) == 0 {
				cur.Lines = append(append([]proto.ViewLine{}, old.Lines[k:]...), cur.Lines...)
				cur.FirstLine = old.FirstLine + k
			}
		}
		p, ok := diffViews(&old, &cur)
		if !ok {
			t.Fatalf("diff refused %d", i)
		}
		raw, _ := json.Marshal(p)
		var p2 viewPatch
		_ = json.Unmarshal(raw, &p2)
		got, err := applyViewPatch(old, p2)
		if err != nil {
			t.Fatalf("%d: %v", i, err)
		}
		if !sameView(got, cur) {
			a, _ := json.Marshal(got)
			b, _ := json.Marshal(cur)
			t.Fatalf("%d: patch result differs\ngot:  %s\nwant: %s\npatch: %s", i, a, b, raw)
		}
	}
	// Not expressible as a patch.
	a, b := randView(rng, 5, 3), randView(rng, 2, 3)
	b.Revision = 9
	if _, ok := diffViews(&a, &b); ok {
		t.Fatal("first_line moving backwards must fall back to a full view")
	}
	b.FirstLine, b.Epoch = 5, "other"
	if _, ok := diffViews(&a, &b); ok {
		t.Fatal("epoch change must fall back to a full view")
	}
}

func TestSingleflightCollapsesFetches(t *testing.T) {
	var g flightGroup
	var mu sync.Mutex
	calls := 0
	release := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			v, err := g.do(context.Background(), "k", func() (*fetchedView, error) {
				mu.Lock()
				calls++
				mu.Unlock()
				<-release
				return &fetchedView{v: &proto.View{Revision: 5}}, nil
			})
			if err != nil || v.v.Revision != 5 {
				t.Errorf("got %v %v", v, err)
			}
		}()
	}
	time.Sleep(100 * time.Millisecond)
	close(release)
	wg.Wait()
	if calls != 1 {
		t.Fatalf("fn ran %d times, want 1", calls)
	}
}
