package httpapi

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/digiogithub/agentd/internal/proto"
)

type auditRecorder struct {
	mu  sync.Mutex
	evs []AuditEvent
}

func (a *auditRecorder) Audit(ev AuditEvent) { a.mu.Lock(); a.evs = append(a.evs, ev); a.mu.Unlock() }

func (a *auditRecorder) find(action string) *AuditEvent {
	a.mu.Lock()
	defer a.mu.Unlock()
	for i := range a.evs {
		if a.evs[i].Action == action {
			return &a.evs[i]
		}
	}
	return nil
}

func TestAuditTrailCoversWritesWithoutSecrets(t *testing.T) {
	e := startREST(t)
	rec := &auditRecorder{}
	e.s.Audit = rec
	const secret = "TOP-SECRET-PROMPT-TEXT"
	const bodySecret = "TOP-SECRET-MESSAGE-BODY"
	e.runFake(t, "au", "idle")
	e.must(t, 200, "POST", "/v1/sessions/au/act", "write-token", `{"action":"prompt","text":"`+secret+`"}`)
	e.must(t, 200, "POST", "/v1/sessions/au/act", "write-token", `{"action":"keys","keys":["down","enter"]}`)
	e.must(t, 200, "POST", "/v1/sessions/au/view/ack", "write-token", "")
	self := jsonOf[struct{ Identity string }](t, e.must(t, 200, "GET", "/v1/info", "read-token", "")).Identity
	m := jsonOf[proto.Message](t, e.must(t, 201, "POST", "/v1/mailbox/messages", "write-token", `{"to":"`+self+`","subject":"subj-secret","body":"`+bodySecret+`"}`))
	e.must(t, 201, "POST", "/v1/mailbox/messages/"+itoa(int(m.ID))+"/reply", "write-token", `{"body":"`+bodySecret+`"}`)
	e.must(t, 202, "POST", "/v1/sessions/au/kill", "write-token", `{"signal":"KILL"}`)
	time.Sleep(500 * time.Millisecond)
	e.must(t, 204, "DELETE", "/v1/sessions/au", "write-token", "")
	// A refused request is audited with its error code.
	e.req(t, "POST", "/v1/sessions/ghost/kill", "write-token", "")

	for _, a := range []string{"run", "act", "view_ack", "send", "reply", "kill", "rm"} {
		ev := rec.find(a)
		if ev == nil {
			t.Errorf("no audit event for %s", a)
			continue
		}
		if ev.Device != "rw" && ev.Device != "adm" || ev.Time.IsZero() || ev.RequestID == "" || ev.Result == "" {
			t.Errorf("%s event incomplete: %+v", a, ev)
		}
	}
	rec.mu.Lock()
	defer rec.mu.Unlock()
	var acts []AuditEvent
	for _, ev := range rec.evs {
		b, _ := json.Marshal(ev)
		if s := string(b); strings.Contains(s, secret) || strings.Contains(s, bodySecret) || strings.Contains(s, "subj-secret") {
			t.Errorf("audit line leaks content: %s", s)
		}
		if ev.Action == "act" {
			acts = append(acts, ev)
		}
	}
	if len(acts) != 2 || acts[0].Detail["act"] != "prompt" || acts[0].Detail["text_len"] != len(secret) ||
		strings.Join(toStrings(acts[1].Detail["keys"]), ",") != "down,enter" {
		t.Errorf("act details = %+v", acts)
	}
	for _, ev := range rec.evs {
		if ev.Action == "kill" && ev.Session == "ghost" && ev.Result != "not_found" {
			t.Errorf("refused kill result = %q", ev.Result)
		}
		if ev.Action == "send" && (ev.Detail["id"] == nil || ev.Detail["body_len"] != len(bodySecret)) {
			t.Errorf("send detail = %+v", ev.Detail)
		}
	}
}

func toStrings(v any) []string {
	switch x := v.(type) {
	case []string:
		return x
	case []any:
		var out []string
		for _, i := range x {
			out = append(out, i.(string))
		}
		return out
	}
	return nil
}

func TestAuditFileJSONLPermsAndRotation(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "api")
	path := filepath.Join(dir, "audit.log")
	a, err := OpenAuditFile(path, 600)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	for i := 0; i < 20; i++ {
		a.Audit(AuditEvent{Action: "act", Device: "d1", Session: "s", Result: "ok", Detail: map[string]any{"text_len": i}})
	}
	for _, p := range []string{path, path + ".1"} {
		st, err := os.Stat(p)
		if err != nil {
			t.Fatalf("%s: %v", p, err)
		}
		if st.Mode().Perm() != 0o600 {
			t.Errorf("%s mode %v, want 0600", p, st.Mode().Perm())
		}
		if st.Size() > 600 {
			t.Errorf("%s is %d bytes, over the 600 cap", p, st.Size())
		}
	}
	if st, _ := os.Stat(dir); st.Mode().Perm() != 0o700 {
		t.Errorf("dir mode %v", st.Mode().Perm())
	}
	if _, err := os.Stat(path + ".2"); err == nil {
		t.Error("only one rotation is kept")
	}
	f, _ := os.Open(path)
	defer f.Close()
	sc := bufio.NewScanner(f)
	n := 0
	for sc.Scan() {
		var ev AuditEvent
		if err := json.Unmarshal(sc.Bytes(), &ev); err != nil || ev.Action != "act" || ev.Time.IsZero() {
			t.Fatalf("bad JSONL line %q: %v", sc.Text(), err)
		}
		n++
	}
	if n == 0 {
		t.Error("current file is empty")
	}
	// A second process appending (the CLI) after a rotation reopens the file.
	b, err := OpenAuditFile(path, 600)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	b.Audit(AuditEvent{Action: "device_revoke", Result: "ok"})
	a.Audit(AuditEvent{Action: "act", Result: "ok"})
	data, _ := os.ReadFile(path)
	old, _ := os.ReadFile(path + ".1")
	if !strings.Contains(string(data)+string(old), "device_revoke") {
		t.Error("event of the second writer missing")
	}
}
