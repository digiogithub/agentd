package httpapi

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/digiogithub/agentd/internal/proto"
)

type sinkReq struct {
	hdr  http.Header
	body string
	path string
}

// sink is an httptest receiver that records requests; fail makes the first n
// answer 500.
type sink struct {
	*httptest.Server
	mu   sync.Mutex
	reqs []sinkReq
	fail atomic.Int32
	hits atomic.Int32
}

func newSink(t *testing.T) *sink {
	t.Helper()
	k := &sink{}
	k.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		k.hits.Add(1)
		if k.fail.Add(-1) >= 0 {
			w.WriteHeader(500)
			return
		}
		k.mu.Lock()
		k.reqs = append(k.reqs, sinkReq{r.Header.Clone(), string(b), r.URL.Path})
		k.mu.Unlock()
	}))
	t.Cleanup(k.Close)
	return k
}

func (k *sink) wait(t *testing.T, n int) []sinkReq {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		k.mu.Lock()
		if len(k.reqs) >= n {
			r := append([]sinkReq(nil), k.reqs...)
			k.mu.Unlock()
			return r
		}
		k.mu.Unlock()
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("sink got %d requests, want %d", len(k.reqs), n)
	return nil
}

func (k *sink) count() int { k.mu.Lock(); defer k.mu.Unlock(); return len(k.reqs) }

func newNotifier(t *testing.T, cfg NotifyConfig) (*Server, *notifier) {
	t.Helper()
	cfg.Enabled = true
	if cfg.Backoff == 0 {
		cfg.Backoff = time.Millisecond
	}
	s := &Server{Config: Config{Notify: cfg}}
	s.startNotifier()
	t.Cleanup(s.Close)
	if s.notify == nil {
		t.Fatal("notifier not started")
	}
	return s, s.notify
}

func attEv(session string, set bool, reason string) *hubEvent {
	d, _ := json.Marshal(proto.AttentionChangedData{Attention: set, Reason: reason})
	return &hubEvent{kind: proto.EvAttentionChanged, session: session, data: d}
}

func exitEv(session string, code int) *hubEvent {
	d, _ := json.Marshal(proto.ExitedData{ExitCode: code})
	return &hubEvent{kind: proto.EvSessionExited, session: session, data: d}
}

func TestNotifyTriggerMapping(t *testing.T) {
	for _, tc := range []struct {
		ev             *hubEvent
		reason, status string
		ok             bool
	}{
		{attEv("a", true, "waiting"), "waiting", "waiting", true},
		{attEv("a", true, "bell"), "waiting", "waiting", true},
		{attEv("a", true, "notify"), "waiting", "waiting", true},
		{attEv("a", true, "turn_done"), "turn_done", "idle", true},
		{attEv("a", false, "waiting"), "", "", false},
		{exitEv("a", 2), "exited", "exited", true},
		{&hubEvent{kind: proto.EvStatusChanged, session: "a"}, "", "", false},
	} {
		r, st, _, ok := mapEvent(tc.ev)
		if r != tc.reason || st != tc.status || ok != tc.ok {
			t.Errorf("%s %s: got %q %q %v", tc.ev.kind, tc.ev.data, r, st, ok)
		}
	}
}

func TestNotifyOnFilterAndWebhookPayload(t *testing.T) {
	k := newSink(t)
	_, n := newNotifier(t, NotifyConfig{WebhookURL: k.URL + "/hook", On: []string{"waiting"}, HostLabel: "box", WebhookSecret: "s3cret"})
	n.offer(attEv("claude@webapp", true, "turn_done")) // filtered out
	n.offer(attEv("claude@webapp", true, "waiting"))
	reqs := k.wait(t, 1)
	time.Sleep(50 * time.Millisecond)
	if k.count() != 1 {
		t.Fatalf("requests = %d, want 1", k.count())
	}
	var p map[string]any
	if err := json.Unmarshal([]byte(reqs[0].body), &p); err != nil {
		t.Fatal(err)
	}
	if p["host"] != "box" || p["session"] != "claude@webapp" || p["reason"] != "waiting" || p["status"] != "waiting" ||
		p["link"] != "agentd://open?h=box&s=claude%40webapp" || p["at"] == "" {
		t.Fatalf("payload = %v", p)
	}
	if _, has := p["preview"]; has {
		t.Fatalf("preview present while include_preview is off: %v", p)
	}
	if got, want := reqs[0].hdr.Get(SignatureHeader), Sign("s3cret", []byte(reqs[0].body)); got != want || !strings.HasPrefix(got, "sha256=") {
		t.Fatalf("signature %q want %q", got, want)
	}
	if reqs[0].hdr.Get("Content-Type") != "application/json" {
		t.Fatal("content type")
	}
}

func TestNotifyNoSignatureWithoutSecret(t *testing.T) {
	k := newSink(t)
	_, n := newNotifier(t, NotifyConfig{WebhookURL: k.URL})
	n.offer(exitEv("a", 0))
	if h := k.wait(t, 1)[0].hdr; h.Get(SignatureHeader) != "" {
		t.Fatal("unexpected signature")
	}
}

func TestNotifyNtfyHeaders(t *testing.T) {
	k := newSink(t)
	_, n := newNotifier(t, NotifyConfig{NtfyURL: k.URL + "/topic", NtfyToken: "tk_abc", HostLabel: "my host"})
	n.offer(attEv("codex@api", true, "waiting"))
	n.offer(exitEv("codex@api", 3))
	n.offer(attEv("codex@api", true, "turn_done"))
	reqs := k.wait(t, 3)
	by := map[string]sinkReq{}
	for _, r := range reqs {
		by[r.hdr.Get("Tags")] = r
	}
	w := by["hourglass_flowing_sand"]
	if w.path != "/topic" || w.body != "codex@api is waiting for input" || w.hdr.Get("Priority") != "4" ||
		w.hdr.Get("Authorization") != "Bearer tk_abc" || w.hdr.Get("Title") != "agentd: codex@api" ||
		w.hdr.Get("Click") != "agentd://open?h=my+host&s=codex%40api" {
		t.Fatalf("waiting = %+v", w)
	}
	if e := by["x"]; e.body != "codex@api exited (code 3)" || e.hdr.Get("Priority") != "4" {
		t.Fatalf("exit = %+v", e)
	}
	if d := by["white_check_mark"]; d.body != "codex@api finished its turn" || d.hdr.Get("Priority") != "3" {
		t.Fatalf("done = %+v", d)
	}
}

func TestNotifyDebounce(t *testing.T) {
	k := newSink(t)
	now := time.Unix(1000, 0)
	var mu sync.Mutex
	_, n := newNotifier(t, NotifyConfig{WebhookURL: k.URL, Debounce: 30 * time.Second,
		Now: func() time.Time { mu.Lock(); defer mu.Unlock(); return now }})
	adv := func(d time.Duration) { mu.Lock(); now = now.Add(d); mu.Unlock() }
	n.offer(attEv("a", true, "waiting"))
	adv(10 * time.Second)
	n.offer(attEv("a", true, "waiting"))   // debounced
	n.offer(attEv("b", true, "waiting"))   // other session
	n.offer(attEv("a", true, "turn_done")) // other reason
	k.wait(t, 3)
	adv(25 * time.Second)
	n.offer(attEv("a", true, "waiting")) // 35 s after the first: passes
	k.wait(t, 4)
	time.Sleep(50 * time.Millisecond)
	if k.count() != 4 {
		t.Fatalf("requests = %d, want 4", k.count())
	}
}

func TestNotifyRetryBackoff(t *testing.T) {
	k := newSink(t)
	k.fail.Store(3) // 3 failures then success = the 3 allowed retries
	_, n := newNotifier(t, NotifyConfig{WebhookURL: k.URL})
	n.offer(exitEv("a", 1))
	k.wait(t, 1)
	if k.hits.Load() != 4 {
		t.Fatalf("hits = %d, want 4", k.hits.Load())
	}

	k2 := newSink(t)
	k2.fail.Store(100)
	var logw syncBuf
	s2, n2 := newNotifier(t, NotifyConfig{WebhookURL: k2.URL + "/?token=SECRETTOPIC", WebhookSecret: "hmacsecret"})
	s2.Log = newTestLogger(&logw)
	n2.offer(exitEv("a", 1))
	deadline := time.Now().Add(5 * time.Second)
	for !strings.Contains(logw.String(), "delivery failed") && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if k2.hits.Load() != 4 {
		t.Fatalf("gave up after %d attempts, want 4", k2.hits.Load())
	}
	if l := logw.String(); !strings.Contains(l, "delivery failed") || strings.Contains(l, "SECRETTOPIC") || strings.Contains(l, "hmacsecret") {
		t.Fatalf("log = %q", l)
	}
}

func TestNotifyNoRetryOnClientError(t *testing.T) {
	var hits atomic.Int32
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hits.Add(1); w.WriteHeader(403) }))
	defer ts.Close()
	_, n := newNotifier(t, NotifyConfig{WebhookURL: ts.URL})
	n.offer(exitEv("a", 1))
	time.Sleep(200 * time.Millisecond)
	if hits.Load() != 1 {
		t.Fatalf("hits = %d, want 1", hits.Load())
	}
}

func TestNotifyQueueOverflowDoesNotBlock(t *testing.T) {
	block := make(chan struct{})
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { <-block }))
	defer ts.Close()
	defer close(block)
	_, n := newNotifier(t, NotifyConfig{WebhookURL: ts.URL, QueueSize: 4, Timeout: time.Minute})
	done := make(chan struct{})
	go func() {
		for i := 0; i < 500; i++ {
			n.offer(exitEv("s"+string(rune('a'+i%26))+strings.Repeat("x", i/26), 0))
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("offer blocked on a stuck sink")
	}
	n.mu.Lock()
	l := len(n.queue)
	n.mu.Unlock()
	if l > 4 {
		t.Fatalf("queue = %d, want <= 4", l)
	}
}

func TestNotifyDisabledNoGoroutines(t *testing.T) {
	before := runtime.NumGoroutine()
	s := &Server{Config: Config{Notify: NotifyConfig{WebhookURL: "http://127.0.0.1:1"}}} // Enabled=false
	s.startNotifier()
	if s.notify != nil || s.hub.tap != nil {
		t.Fatal("notifier created while disabled")
	}
	time.Sleep(50 * time.Millisecond)
	if after := runtime.NumGoroutine(); after > before {
		t.Fatalf("goroutines %d -> %d", before, after)
	}
	s.Close()
}

func TestNotifyCleanPreview(t *testing.T) {
	got := cleanPreview("a\x1b[31m\tb\n" + strings.Repeat("é", 300))
	if strings.ContainsAny(got, "\x1b\n\t") || len([]rune(got)) != 160 {
		t.Fatalf("%q", got)
	}
}

// End to end against a real daemon: session.exited reaches the webhook through
// the hub, and the screen text stays out of the payload unless asked.
func TestNotifyEndToEndPreview(t *testing.T) {
	for _, withPreview := range []bool{false, true} {
		k := newSink(t)
		e := newSSEEnv(t, func(s *Server) {
			s.Config.Notify = NotifyConfig{Enabled: true, WebhookURL: k.URL, IncludePreview: withPreview, HostLabel: "h", Backoff: time.Millisecond}
			s.startNotifier()
		})
		e.run(t, "sh@x", "sh", "-c", "echo SECRET-SCREEN-LINE; sleep 1; exit 3")
		r := k.wait(t, 1)[0]
		var p webhookPayload
		if err := json.Unmarshal([]byte(r.body), &p); err != nil {
			t.Fatal(err)
		}
		if p.Session != "sh@x" || p.Reason != "exited" || p.ExitCode == nil || *p.ExitCode != 3 {
			t.Fatalf("payload = %+v", p)
		}
		has := strings.Contains(r.body, "SECRET-SCREEN-LINE")
		if !withPreview && has {
			t.Fatalf("screen text leaked with include_preview off: %s", r.body)
		}
		if withPreview && !has {
			t.Fatalf("preview missing with include_preview on: %s", r.body)
		}
	}
}
