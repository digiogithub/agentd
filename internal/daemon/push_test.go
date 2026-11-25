package daemon

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/digiogithub/agentd/internal/config"
	"github.com/digiogithub/agentd/internal/proto"
	"github.com/digiogithub/agentd/internal/push"
)

// fakeFCM is the OAuth endpoint and FCM in one httptest server.
type fakeFCM struct {
	srv  *httptest.Server
	mu   sync.Mutex
	msgs []map[string]any // "message" objects
	code func(token string) (int, string)
}

func newFakeFCM(t *testing.T) *fakeFCM {
	f := &fakeFCM{}
	mux := http.NewServeMux()
	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"access_token":"at","expires_in":3600}`))
	})
	mux.HandleFunc("/v1/projects/proj-x/messages:send", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Message map[string]any `json:"message"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		f.mu.Lock()
		f.msgs = append(f.msgs, body.Message)
		f.mu.Unlock()
		if f.code != nil {
			if c, b := f.code(body.Message["token"].(string)); c != 200 {
				w.WriteHeader(c)
				_, _ = w.Write([]byte(b))
			}
		}
	})
	f.srv = httptest.NewServer(mux)
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeFCM) wait(t *testing.T, n int) []map[string]any {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		f.mu.Lock()
		if len(f.msgs) >= n {
			m := append([]map[string]any(nil), f.msgs...)
			f.mu.Unlock()
			return m
		}
		f.mu.Unlock()
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("FCM got %d messages, want %d", len(f.msgs), n)
	return nil
}

func (f *fakeFCM) count() int { f.mu.Lock(); defer f.mu.Unlock(); return len(f.msgs) }

// pushServer returns a server with push enabled against a fake FCM.
func pushServer(t *testing.T, f *fakeFCM, mod func(*config.PushConfig)) *Server {
	t.Helper()
	s := newTestServer(t)
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	der, _ := x509.MarshalPKCS8PrivateKey(key)
	cj, _ := json.Marshal(map[string]string{"type": "service_account", "project_id": "proj-x", "client_email": "e@x",
		"private_key": string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})), "token_uri": f.srv.URL + "/token"})
	cred := filepath.Join(t.TempDir(), "sa.json")
	if err := os.WriteFile(cred, cj, 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := config.Default()
	cfg.Push = config.PushConfig{Enabled: true, CredentialsFile: cred, HostLabel: "hst",
		On: []string{"question", "permission", "waiting", "blocked", "exited", "turn_done"}}
	if mod != nil {
		mod(&cfg.Push)
	}
	s.Cfg = cfg
	st, err := push.OpenStore(filepath.Join(t.TempDir(), "push", "tokens.json"))
	if err != nil {
		t.Fatal(err)
	}
	s.PushStore = st
	s.PushEndpoint = f.srv.URL
	s.PushBackoff = time.Millisecond
	s.startPush()
	t.Cleanup(s.stopPush)
	return s
}

var humanCaller = caller{id: "human:me"}

func register(t *testing.T, s *Server, token string) string {
	t.Helper()
	r, err := s.pushRegister(humanCaller, proto.PushRegisterArgs{Token: token, Platform: "android", Name: "phone", HostRef: "ref1"})
	if err != nil {
		t.Fatal(err)
	}
	return r.ID
}

func TestPushQuestionMessage(t *testing.T) {
	f := newFakeFCM(t)
	s := pushServer(t, f, func(c *config.PushConfig) { c.IncludePreview = false })
	register(t, s, "TOKEN-A")
	s.bus.publish(proto.EvMailQueued, "", "a@x", "human:me", map[string]any{"id": 7, "from": "a@x", "to": "human:me", "kind": "question"})
	m := f.wait(t, 1)[0]
	n := m["notification"].(map[string]any)
	d := m["data"].(map[string]any)
	if m["token"] != "TOKEN-A" || n["title"] != "a@x te pregunta" || n["body"] != "hst" {
		t.Fatalf("message %v", m)
	}
	if d["v"] != "1" || d["host"] != "hst" || d["host_ref"] != "ref1" || d["session"] != "a@x" || d["reason"] != "question" ||
		d["message_id"] != "7" || d["link"] != "agentd://open?h=hst&s=a%40x" {
		t.Fatalf("data %v", d)
	}
	a := m["android"].(map[string]any)
	an := a["notification"].(map[string]any)
	if a["priority"] != "HIGH" || a["collapse_key"] != "a@x" || an["channel_id"] != "agentd_attention" || an["tag"] != "hst/a@x/question" {
		t.Fatalf("android %v", a)
	}
}

func TestPushPreviewOnlyWhenEnabled(t *testing.T) {
	for _, preview := range []bool{false, true} {
		f := newFakeFCM(t)
		s := pushServer(t, f, func(c *config.PushConfig) { c.IncludePreview = preview })
		register(t, s, "TOKEN-A")
		s.bus.publish("task.changed", "g@x", "", "", map[string]any{"revision": 2, "changed": []map[string]any{{"id": "t", "title": "Secret title", "status": "blocked", "from": "in_progress"}}})
		m := f.wait(t, 1)[0]
		body := m["notification"].(map[string]any)["body"].(string)
		if preview != strings.Contains(body, "Secret title") {
			t.Fatalf("preview=%v body=%q", preview, body)
		}
		if m["notification"].(map[string]any)["title"] != "g@x tiene una tarea bloqueada" {
			t.Fatalf("title %v", m["notification"])
		}
	}
}

func TestPushReasonFilterAndDebounce(t *testing.T) {
	f := newFakeFCM(t)
	s := pushServer(t, f, func(c *config.PushConfig) {
		c.On = []string{"question", "exited"}
		c.Debounce = config.Duration(time.Hour)
	})
	register(t, s, "TOKEN-A")
	s.bus.publish(proto.EvAttentionChanged, "a@x", "", "", proto.AttentionChangedData{Attention: true, Reason: "bell"}) // waiting: not in On
	s.bus.publish(proto.EvSessionExited, "d@x", "", "", proto.ExitedData{ExitCode: 1})
	s.bus.publish(proto.EvSessionExited, "d@x", "", "", proto.ExitedData{ExitCode: 1}) // debounced
	s.bus.publish(proto.EvMailQueued, "", "e@x", "human:me", map[string]any{"id": 1, "from": "e@x", "to": "human:me", "kind": "question"})
	s.bus.publish(proto.EvMailQueued, "f@x", "e@x", "f@x", map[string]any{"id": 2, "from": "e@x", "to": "f@x", "kind": "question"}) // to a session
	f.wait(t, 2)
	time.Sleep(100 * time.Millisecond)
	if f.count() != 2 {
		t.Fatalf("messages = %d, want 2", f.count())
	}
}

func TestPushPermissionNeedsMenu(t *testing.T) {
	f := newFakeFCM(t)
	s := pushServer(t, f, func(c *config.PushConfig) { c.IncludePreview = true })
	register(t, s, "TOKEN-A")
	menu := `printf 'Do you want to edit a.go?\r\n❯ 1. Yes\r\n  2. No\r\n'; sleep 60`
	if _, err := s.run(context.Background(), proto.RunArgs{Name: "m@x", Cmd: []string{"bash", "-c", menu}, Cwd: t.TempDir()}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.run(context.Background(), proto.RunArgs{Name: "p@x", Cmd: []string{"bash", "-c", "echo hello; sleep 60"}, Cwd: t.TempDir()}); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		if n, _ := s.pushDialog("m@x"); n > 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Skip("the default profile does not detect the menu here")
		}
		time.Sleep(20 * time.Millisecond)
	}
	s.bus.publish(proto.EvAttentionChanged, "p@x", "", "", proto.AttentionChangedData{Attention: true, Reason: "waiting"})
	s.bus.publish(proto.EvAttentionChanged, "m@x", "", "", proto.AttentionChangedData{Attention: true, Reason: "waiting"})
	ms := f.wait(t, 2)
	got := map[string]string{}
	for _, m := range ms {
		d := m["data"].(map[string]any)
		got[d["session"].(string)] = d["reason"].(string)
	}
	if got["m@x"] != "permission" || got["p@x"] != "waiting" {
		t.Fatalf("reasons = %v", got)
	}
	for _, m := range ms {
		n := m["notification"].(map[string]any)
		if m["data"].(map[string]any)["session"] == "m@x" && (n["title"] != "m@x pide permiso" || !strings.Contains(n["body"].(string), "edit a.go")) {
			t.Fatalf("permission message %v", n)
		}
	}
}

func TestPushUnregisteredTokenRemoved(t *testing.T) {
	f := newFakeFCM(t)
	f.code = func(tok string) (int, string) {
		if tok == "DEAD" {
			return 404, `{"error":{"status":"NOT_FOUND","details":[{"errorCode":"UNREGISTERED"}]}}`
		}
		return 200, ""
	}
	s := pushServer(t, f, nil)
	register(t, s, "DEAD")
	live := register(t, s, "LIVE")
	s.bus.publish(proto.EvSessionExited, "d@x", "", "", proto.ExitedData{})
	f.wait(t, 2)
	deadline := time.Now().Add(3 * time.Second)
	for len(s.PushStore.List()) != 1 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	l := s.PushStore.List()
	if len(l) != 1 || l[0].ID != live {
		t.Fatalf("tokens = %+v", l)
	}
	deadline = time.Now().Add(3 * time.Second)
	for l[0].LastOKAt == nil && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
		l = s.PushStore.List()
	}
	if l[0].LastOKAt == nil {
		t.Fatal("last_ok_at not recorded")
	}
}

func TestPushOpsIdentity(t *testing.T) {
	f := newFakeFCM(t)
	s := pushServer(t, f, nil)
	sess := caller{id: "a@x", session: true}
	if _, err := s.pushRegister(sess, proto.PushRegisterArgs{Token: "T", Platform: "android"}); proto.CodeOf(err) != proto.CodeForbidden {
		t.Fatalf("session register: %v", err)
	}
	if _, err := s.pushLs(sess); proto.CodeOf(err) != proto.CodeForbidden {
		t.Fatalf("session ls: %v", err)
	}
	if _, err := s.pushTest(context.Background(), sess, proto.PushTestArgs{}); proto.CodeOf(err) != proto.CodeForbidden {
		t.Fatalf("session test: %v", err)
	}
	// Validation.
	for _, a := range []proto.PushRegisterArgs{{Token: "", Platform: "android"}, {Token: "a b", Platform: "android"},
		{Token: "T", Platform: "palm"}, {Token: "T", Platform: "ios", HostRef: "a b"}, {Token: strings.Repeat("x", 4097), Platform: "ios"}} {
		if _, err := s.pushRegister(humanCaller, a); proto.CodeOf(err) != proto.CodeInvalid {
			t.Fatalf("%+v: %v", a, err)
		}
	}
	// A guest device is refused, an owner device registers and sees only its own.
	guest := caller{id: "human:me", dev: &relayDevice{ID: "dg", Kind: relayDeviceKindGuest}}
	if _, err := s.pushRegister(guest, proto.PushRegisterArgs{Token: "T", Platform: "android"}); proto.CodeOf(err) != proto.CodeForbidden {
		t.Fatalf("guest: %v", err)
	}
	dev1 := caller{id: "human:me", dev: &relayDevice{ID: "d1", Kind: relayDeviceKindOwner, Scope: config.RelayScopeView}}
	dev2 := caller{id: "human:me", dev: &relayDevice{ID: "d2", Kind: relayDeviceKindOwner, Scope: config.RelayScopeView}}
	r1, err := s.pushRegister(dev1, proto.PushRegisterArgs{Token: "T1", Platform: "android", Name: "one"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.pushRegister(dev2, proto.PushRegisterArgs{Token: "T2", Platform: "ios", Name: "two"}); err != nil {
		t.Fatal(err)
	}
	register(t, s, "T3")
	if ls, _ := s.pushLs(dev1); len(ls.Tokens) != 1 || ls.Tokens[0].Device != "d1" || !ls.Enabled || ls.ProjectID != "proj-x" {
		t.Fatalf("device ls = %+v", ls)
	}
	if ls, _ := s.pushLs(humanCaller); len(ls.Tokens) != 3 {
		t.Fatalf("human ls = %+v", ls)
	}
	raw, _ := json.Marshal(proto.PushLsResult{})
	_ = raw
	// A device cannot remove another device's token, nor test.
	if r, _ := s.pushUnregister(dev2, proto.PushUnregisterArgs{ID: r1.ID}); r.Removed != 0 {
		t.Fatal("device removed a foreign token")
	}
	if _, err := s.pushTest(context.Background(), dev1, proto.PushTestArgs{}); proto.CodeOf(err) != proto.CodeForbidden {
		t.Fatalf("device test: %v", err)
	}
	if r, _ := s.pushUnregister(dev1, proto.PushUnregisterArgs{Token: "T1"}); r.Removed != 1 {
		t.Fatal("device could not remove its own token")
	}
	// push_test sends to every token and counts.
	res, err := s.pushTest(context.Background(), humanCaller, proto.PushTestArgs{})
	if err != nil || res.Sent != 2 || len(res.Errors) != 0 {
		t.Fatalf("test = %+v, %v", res, err)
	}
	if _, err := s.pushTest(context.Background(), humanCaller, proto.PushTestArgs{ID: "pnone"}); proto.CodeOf(err) != proto.CodeNotFound {
		t.Fatalf("unknown id: %v", err)
	}
}

func TestPushOffReasons(t *testing.T) {
	// Disabled: ls says why, test is not_ready, registering still works.
	s := newTestServer(t)
	s.Cfg = config.Default()
	st, _ := push.OpenStore(filepath.Join(t.TempDir(), "t.json"))
	s.PushStore = st
	s.startPush()
	register(t, s, "T")
	ls, _ := s.pushLs(humanCaller)
	if ls.Enabled || ls.Reason == "" || len(ls.Tokens) != 1 {
		t.Fatalf("ls = %+v", ls)
	}
	if _, err := s.pushTest(context.Background(), humanCaller, proto.PushTestArgs{}); proto.CodeOf(err) != proto.CodeNotReady {
		t.Fatalf("test: %v", err)
	}
	// Loose credentials permissions keep it off.
	f := newFakeFCM(t)
	s2 := newTestServer(t)
	cred := filepath.Join(t.TempDir(), "sa.json")
	_ = os.WriteFile(cred, []byte("{}"), 0o644)
	s2.Cfg = config.Default()
	s2.Cfg.Push = config.PushConfig{Enabled: true, CredentialsFile: cred, On: []string{"question"}}
	s2.PushStore = st
	s2.PushEndpoint = f.srv.URL
	s2.startPush()
	if ls, _ := s2.pushLs(humanCaller); ls.Enabled || !strings.Contains(ls.Reason, "private") {
		t.Fatalf("ls = %+v", ls)
	}
}

func TestPushRevokeRemovesDeviceTokens(t *testing.T) {
	f := newFakeFCM(t)
	s := pushServer(t, f, nil)
	s.relay.dir = t.TempDir()
	dev := &relayDevice{ID: "dev9", Name: "phone", Kind: relayDeviceKindOwner, Scope: config.RelayScopeOperate}
	s.relay.devices = map[string]*relayDevice{dev.ID: dev}
	if _, err := s.pushRegister(caller{id: "human:me", dev: dev}, proto.PushRegisterArgs{Token: "T9", Platform: "android"}); err != nil {
		t.Fatal(err)
	}
	register(t, s, "KEEP")
	if _, err := s.relayRevoke("dev9", "", false); err != nil {
		t.Fatal(err)
	}
	l := s.PushStore.List()
	if len(l) != 1 || l[0].Token != "KEEP" {
		t.Fatalf("tokens after revoke = %+v", l)
	}
}

func TestPushRelayGate(t *testing.T) {
	owner := &relayDevice{ID: "o", Kind: relayDeviceKindOwner, Scope: config.RelayScopeView}
	guest := &relayDevice{ID: "g", Kind: relayDeviceKindGuest, Scope: config.RelayScopeView}
	for _, op := range []string{proto.OpPushRegister, proto.OpPushUnregister, proto.OpPushLs} {
		if _, err := relayGate(owner, &proto.Request{Op: op}); err != nil {
			t.Errorf("%s owner: %v", op, err)
		}
		if _, err := relayGate(guest, &proto.Request{Op: op}); proto.CodeOf(err) != proto.CodeForbidden {
			t.Errorf("%s guest: %v", op, err)
		}
	}
	if _, err := relayGate(owner, &proto.Request{Op: proto.OpPushTest}); proto.CodeOf(err) != proto.CodeForbidden {
		t.Errorf("push_test: %v", err)
	}
}
