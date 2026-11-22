package daemon

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/digiogithub/agentd/internal/config"
	"github.com/digiogithub/agentd/internal/mailbox"
	"github.com/digiogithub/agentd/internal/proto"
)

type notifySink struct {
	*httptest.Server
	mu     sync.Mutex
	bodies []string
}

func newNotifySink(t *testing.T) *notifySink {
	t.Helper()
	k := &notifySink{}
	k.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		k.mu.Lock()
		k.bodies = append(k.bodies, string(b))
		k.mu.Unlock()
	}))
	t.Cleanup(k.Close)
	return k
}

func (k *notifySink) wait(t *testing.T, n int) []string {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		k.mu.Lock()
		if len(k.bodies) >= n {
			b := append([]string(nil), k.bodies...)
			k.mu.Unlock()
			return b
		}
		k.mu.Unlock()
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("sink got %d bodies, want %d", len(k.bodies), n)
	return nil
}

func (k *notifySink) count() int { k.mu.Lock(); defer k.mu.Unlock(); return len(k.bodies) }

func notifyServer(t *testing.T, nc config.NotifyConfig) *Server {
	t.Helper()
	s := newTestServer(t)
	cfg := config.Default()
	cfg.Notify = nc
	cfg.Notify.Enabled = true
	if cfg.Notify.On == nil {
		cfg.Notify.On = append([]string(nil), config.DefaultDaemonNotifyOn...)
	}
	s.Cfg = cfg
	s.startNotify()
	return s
}

func TestNotifyDefaultsAndDisabled(t *testing.T) {
	if got := config.Default().Notify; got.Enabled || strings.Join(got.On, ",") != "question,waiting,blocked,exited" {
		t.Fatalf("defaults = %+v", got)
	}
	s := newTestServer(t)
	before := runtime.NumGoroutine()
	s.Cfg = config.Default()
	s.Cfg.Notify.WebhookURL = "http://127.0.0.1:1" // not enabled
	s.startNotify()
	s.bus.mu.Lock()
	subs := len(s.bus.subs)
	s.bus.mu.Unlock()
	if subs != 0 || s.ntf.cancel != nil {
		t.Fatalf("tap started while disabled (subs=%d)", subs)
	}
	time.Sleep(30 * time.Millisecond)
	if after := runtime.NumGoroutine(); after > before {
		t.Fatalf("goroutines %d -> %d", before, after)
	}
}

func TestNotifyBusMapping(t *testing.T) {
	k := newNotifySink(t)
	s := notifyServer(t, config.NotifyConfig{WebhookURL: k.URL, HostLabel: "h", On: []string{"question", "waiting", "blocked", "turn_done", "exited"}})

	s.bus.publish(proto.EvAttentionChanged, "a@x", "", "", proto.AttentionChangedData{Attention: true, Reason: "bell"})
	s.bus.publish(proto.EvAttentionChanged, "b@x", "", "", proto.AttentionChangedData{Attention: true, Reason: "turn_done"})
	s.bus.publish(proto.EvAttentionChanged, "c@x", "", "", proto.AttentionChangedData{Attention: false, Reason: "waiting"}) // cleared: no
	s.bus.publish(proto.EvSessionExited, "d@x", "", "", proto.ExitedData{ExitCode: 2})
	// A question to a human; one to another session and a plain message must not notify.
	s.bus.publish(proto.EvMailQueued, "", "e@x", "human:me", map[string]any{"id": 1, "from": "e@x", "to": "human:me", "kind": "question"})
	s.bus.publish(proto.EvMailQueued, "f@x", "e@x", "f@x", map[string]any{"id": 2, "from": "e@x", "to": "f@x", "kind": "question"})
	s.bus.publish(proto.EvMailQueued, "", "e@x", "human:me", map[string]any{"id": 3, "from": "e@x", "to": "human:me"})
	// Tasks: newly blocked notifies; already blocked or other statuses do not.
	s.bus.publish("task.changed", "g@x", "", "", map[string]any{"revision": 2, "changed": []map[string]any{{"id": "t1", "title": "T", "status": "blocked", "from": "blocked"}, {"id": "t2", "title": "U", "status": "done", "from": "blocked"}}})
	s.bus.publish("task.changed", "g@x", "", "", map[string]any{"revision": 3, "changed": []map[string]any{{"id": "t3", "title": "Fix it", "status": "blocked", "from": "in_progress"}}})

	bodies := k.wait(t, 5)
	time.Sleep(100 * time.Millisecond)
	if k.count() != 5 {
		t.Fatalf("notifications = %d, want 5: %v", k.count(), k.bodies)
	}
	all := strings.Join(bodies, "\n")
	for _, want := range []string{
		`"session":"a@x","reason":"waiting"`,
		`"session":"b@x","reason":"turn_done"`,
		`"session":"d@x","reason":"exited"`, `"exit_code":2`,
		`"session":"e@x","reason":"question"`,
		`"session":"g@x","reason":"blocked"`,
	} {
		if !strings.Contains(all, want) {
			t.Errorf("missing %s in %s", want, all)
		}
	}
	if strings.Contains(all, "Fix it") {
		t.Fatalf("agent text leaked with include_preview off: %s", all)
	}
}

func TestNotifyOnDefaultsSkipTurnDone(t *testing.T) {
	k := newNotifySink(t)
	s := notifyServer(t, config.NotifyConfig{WebhookURL: k.URL})
	s.bus.publish(proto.EvAttentionChanged, "a@x", "", "", proto.AttentionChangedData{Attention: true, Reason: "turn_done"})
	s.bus.publish(proto.EvSessionExited, "b@x", "", "", proto.ExitedData{})
	k.wait(t, 1)
	time.Sleep(100 * time.Millisecond)
	if k.count() != 1 || !strings.Contains(k.bodies[0], `"reason":"exited"`) {
		t.Fatalf("bodies = %v", k.bodies)
	}
}

func TestNotifyBlockedTextOnlyWithPreview(t *testing.T) {
	k := newNotifySink(t)
	s := notifyServer(t, config.NotifyConfig{WebhookURL: k.URL, IncludePreview: true})
	s.bus.publish("task.changed", "g@x", "", "", map[string]any{"changed": []map[string]any{{"title": "Fix\x1b[31m it " + strings.Repeat("y", 300), "status": "blocked"}}})
	b := k.wait(t, 1)[0]
	if !strings.Contains(b, "Fix [31m it") || strings.Contains(b, "\\u001b") || strings.Contains(b, strings.Repeat("y", 200)) {
		t.Fatalf("body = %s", b)
	}
}

func TestNotifyStopBeforeBusClose(t *testing.T) {
	k := newNotifySink(t)
	s := notifyServer(t, config.NotifyConfig{WebhookURL: k.URL})
	s.stopNotify()
	s.bus.mu.Lock()
	subs := len(s.bus.subs)
	s.bus.mu.Unlock()
	if subs != 0 {
		t.Fatalf("subscribers left: %d", subs)
	}
	s.stopNotify() // idempotent
	s.bus.publish(proto.EvSessionExited, "a@x", "", "", proto.ExitedData{})
	time.Sleep(50 * time.Millisecond)
	if k.count() != 0 {
		t.Fatal("notified after stop")
	}
}

func writeFile0644(p string) error { return os.WriteFile(p, []byte("[notify]\n"), 0o644) }

func TestNotifyRefusesPlainSecretInOpenConfig(t *testing.T) {
	s := newTestServer(t)
	cfg := config.Default()
	cfg.Notify = config.NotifyConfig{Enabled: true, WebhookURL: "http://127.0.0.1:1", WebhookSecret: "s3cret"}
	cfg.Path = t.TempDir() + "/config.toml"
	if err := writeFile0644(cfg.Path); err != nil {
		t.Fatal(err)
	}
	s.Cfg = cfg
	s.startNotify()
	if s.ntf.cancel != nil {
		t.Fatal("started with a plain secret in a group/other-readable config")
	}
}

// A question's text comes from the store (mail events carry no body) and only
// with include_preview.
func TestNotifyQuestionTextFromStore(t *testing.T) {
	for _, preview := range []bool{true, false} {
		k := newNotifySink(t)
		s := notifyServer(t, config.NotifyConfig{WebhookURL: k.URL, IncludePreview: preview})
		m, err := s.Store.Send(t.Context(), mailbox.Message{Sender: "a", Recipient: "human:me", Body: "Ship v4 now?", Kind: mailbox.KindQuestion})
		if err != nil {
			t.Fatal(err)
		}
		s.publishMailKind(proto.EvMailQueued, m.ID, m.Sender, m.Recipient, m.Kind)
		b := k.wait(t, 1)[0]
		if got := strings.Contains(b, "Ship v4 now?"); got != preview || !strings.Contains(b, `"reason":"question"`) {
			t.Fatalf("preview=%v body = %s", preview, b)
		}
	}
}
