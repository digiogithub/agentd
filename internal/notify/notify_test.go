package notify

import (
	"context"
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

func newN(t *testing.T, cfg Config) *Notifier {
	t.Helper()
	if cfg.Backoff == 0 {
		cfg.Backoff = time.Millisecond
	}
	n := New(cfg)
	t.Cleanup(n.Close)
	return n
}

func ev(session, reason string) Event {
	return Event{Session: session, Reason: reason, Status: "waiting"}
}

func TestOnFilterAndWebhookPayload(t *testing.T) {
	k := newSink(t)
	n := newN(t, Config{WebhookURL: k.URL + "/hook", On: []string{Waiting}, HostLabel: "box", WebhookSecret: "s3cret"})
	n.Offer(ev("claude@webapp", TurnDone)) // filtered out
	n.Offer(ev("claude@webapp", Waiting))
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
}

func TestNoSignatureWithoutSecret(t *testing.T) {
	k := newSink(t)
	n := newN(t, Config{WebhookURL: k.URL})
	n.Offer(ev("a", Exited))
	if h := k.wait(t, 1)[0].hdr; h.Get(SignatureHeader) != "" {
		t.Fatal("unexpected signature")
	}
}

func TestNtfyHeaders(t *testing.T) {
	k := newSink(t)
	n := newN(t, Config{NtfyURL: k.URL + "/topic", NtfyToken: "tk_abc", HostLabel: "my host"})
	n.Offer(ev("codex@api", Waiting))
	code := 3
	n.Offer(Event{Session: "codex@api", Reason: Exited, Exit: &code})
	n.Offer(ev("codex@api", TurnDone))
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

func TestQuestionAndBlockedText(t *testing.T) {
	for _, withPreview := range []bool{false, true} {
		k := newSink(t)
		n := newN(t, Config{NtfyURL: k.URL, WebhookURL: k.URL, IncludePreview: withPreview, On: []string{Question, Blocked}})
		n.Offer(Event{Session: "claude@api", Reason: Question, Status: "waiting", Text: "Drop the \x1b[31mtable?\n" + strings.Repeat("x", 300)})
		n.Offer(Event{Session: "claude@api", Reason: Blocked, Status: "idle", Text: "Fix migration"})
		reqs := k.wait(t, 4)
		for _, r := range reqs {
			has := strings.Contains(r.body, "Drop the") || strings.Contains(r.body, "Fix migration")
			if has != withPreview {
				t.Fatalf("withPreview=%v: agent text presence %v in %q", withPreview, has, r.body)
			}
			if strings.ContainsAny(r.body, "\x1b") {
				t.Fatalf("control char in %q", r.body)
			}
		}
		var sawQ, sawB bool
		for _, r := range reqs {
			sawQ = sawQ || strings.Contains(r.body, "asked you a question") || strings.Contains(r.body, `"reason":"question"`)
			sawB = sawB || strings.Contains(r.body, "has a blocked task") || strings.Contains(r.body, `"reason":"blocked"`)
		}
		if !sawQ || !sawB {
			t.Fatalf("missing question/blocked: %+v", reqs)
		}
		if withPreview {
			for _, r := range reqs {
				if strings.Contains(r.body, "Drop the") {
					var p Payload
					if json.Unmarshal([]byte(r.body), &p) == nil && len([]rune(p.Preview)) > PreviewChars {
						t.Fatalf("preview not capped: %d", len([]rune(p.Preview)))
					}
				}
			}
		}
	}
}

func TestPreviewHookOnlyWhenEnabled(t *testing.T) {
	var calls atomic.Int32
	hook := func(context.Context, string) string { calls.Add(1); return "last line" }
	k := newSink(t)
	n := newN(t, Config{WebhookURL: k.URL, Preview: hook})
	n.Offer(ev("a", Waiting))
	k.wait(t, 1)
	if calls.Load() != 0 {
		t.Fatal("preview fetched with include_preview off")
	}
	k2 := newSink(t)
	n2 := newN(t, Config{WebhookURL: k2.URL, Preview: hook, IncludePreview: true})
	n2.Offer(ev("a", Waiting))
	if r := k2.wait(t, 1)[0]; !strings.Contains(r.body, "last line") {
		t.Fatalf("body = %s", r.body)
	}
}

func TestDebounce(t *testing.T) {
	k := newSink(t)
	now := time.Unix(1000, 0)
	var mu sync.Mutex
	n := newN(t, Config{WebhookURL: k.URL, Debounce: 30 * time.Second,
		Now: func() time.Time { mu.Lock(); defer mu.Unlock(); return now }})
	adv := func(d time.Duration) { mu.Lock(); now = now.Add(d); mu.Unlock() }
	n.Offer(ev("a", Waiting))
	adv(10 * time.Second)
	n.Offer(ev("a", Waiting))  // debounced
	n.Offer(ev("b", Waiting))  // other session
	n.Offer(ev("a", TurnDone)) // other reason
	k.wait(t, 3)
	adv(25 * time.Second)
	n.Offer(ev("a", Waiting)) // 35 s after the first: passes
	k.wait(t, 4)
	time.Sleep(50 * time.Millisecond)
	if k.count() != 4 {
		t.Fatalf("requests = %d, want 4", k.count())
	}
}

func TestRetryBackoffAndNoSecretsInLog(t *testing.T) {
	k := newSink(t)
	k.fail.Store(3) // 3 failures then success = the 3 allowed retries
	n := newN(t, Config{WebhookURL: k.URL})
	n.Offer(ev("a", Exited))
	k.wait(t, 1)
	if k.hits.Load() != 4 {
		t.Fatalf("hits = %d, want 4", k.hits.Load())
	}

	k2 := newSink(t)
	k2.fail.Store(100)
	var mu sync.Mutex
	var logged strings.Builder
	n2 := newN(t, Config{WebhookURL: k2.URL + "/?token=SECRETTOPIC", WebhookSecret: "hmacsecret",
		Warn: func(msg string, args ...any) {
			mu.Lock()
			defer mu.Unlock()
			logged.WriteString(msg)
			for _, a := range args {
				logged.WriteString(" " + strings.TrimSpace(strings.ReplaceAll(toS(a), "\n", " ")))
			}
		}})
	n2.Offer(ev("a", Exited))
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		done := strings.Contains(logged.String(), "delivery failed")
		mu.Unlock()
		if done {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if k2.hits.Load() != 4 {
		t.Fatalf("gave up after %d attempts, want 4", k2.hits.Load())
	}
	mu.Lock()
	l := logged.String()
	mu.Unlock()
	if !strings.Contains(l, "delivery failed") || strings.Contains(l, "SECRETTOPIC") || strings.Contains(l, "hmacsecret") {
		t.Fatalf("log = %q", l)
	}
}

func toS(a any) string {
	if s, ok := a.(string); ok {
		return s
	}
	b, _ := json.Marshal(a)
	return string(b)
}

func TestNoRetryOnClientError(t *testing.T) {
	var hits atomic.Int32
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hits.Add(1); w.WriteHeader(403) }))
	defer ts.Close()
	n := newN(t, Config{WebhookURL: ts.URL})
	n.Offer(ev("a", Exited))
	time.Sleep(200 * time.Millisecond)
	if hits.Load() != 1 {
		t.Fatalf("hits = %d, want 1", hits.Load())
	}
}

func TestNoRedirects(t *testing.T) {
	var target atomic.Int32
	dst := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { target.Add(1) }))
	defer dst.Close()
	src := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, dst.URL, http.StatusTemporaryRedirect)
	}))
	defer src.Close()
	n := newN(t, Config{WebhookURL: src.URL, WebhookSecret: "x"})
	n.Offer(ev("a", Exited))
	time.Sleep(200 * time.Millisecond)
	if target.Load() != 0 {
		t.Fatal("redirect followed")
	}
}

func TestQueueOverflowDoesNotBlock(t *testing.T) {
	block := make(chan struct{})
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { <-block }))
	defer ts.Close()
	defer close(block)
	n := newN(t, Config{WebhookURL: ts.URL, QueueSize: 4, Timeout: time.Minute})
	done := make(chan struct{})
	go func() {
		for i := 0; i < 500; i++ {
			n.Offer(ev("s"+string(rune('a'+i%26))+strings.Repeat("x", i/26), Exited))
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("Offer blocked on a stuck sink")
	}
	n.mu.Lock()
	l := len(n.queue)
	n.mu.Unlock()
	if l > 4 {
		t.Fatalf("queue = %d, want <= 4", l)
	}
}

func TestCloseStopsWorker(t *testing.T) {
	before := runtime.NumGoroutine()
	n := New(Config{WebhookURL: "http://127.0.0.1:1"})
	n.Close()
	select {
	case <-n.Done():
	default:
		t.Fatal("not done after Close")
	}
	time.Sleep(50 * time.Millisecond)
	if after := runtime.NumGoroutine(); after > before {
		t.Fatalf("goroutines %d -> %d", before, after)
	}
}

func TestCleanText(t *testing.T) {
	got := CleanText("a\x1b[31m\tb\n" + strings.Repeat("é", 300))
	if strings.ContainsAny(got, "\x1b\n\t") || len([]rune(got)) != PreviewChars {
		t.Fatalf("%q", got)
	}
}
