package httpapi

import (
	"encoding/json"
	"errors"
	"net/http/httptest"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/digiogithub/agentd/internal/client"
	"github.com/digiogithub/agentd/internal/proto"
)

// Contract tests: the real handler (temp daemon, `sh` and fakeagent sessions,
// test authenticator) is driven through every operation of
// docs/api/openapi.yaml and each request, response status, body and content
// type is validated against the spec with libopenapi-validator (OpenAPI 3.1).
// SSE payloads are validated per event name against the schemas listed in the
// spec's x-sse-events. Spec lint lives in openapi_lint_test.go.

func startContract(t *testing.T) *contractEnv {
	t.Helper()
	return &contractEnv{restEnv: startREST(t), v: newSpecValidator(t), covered: map[string]bool{}}
}

// specOperations lists "operationId" of every operation in the spec.
func specOperations(t testing.TB) map[string]string {
	t.Helper()
	_, m := loadSpec(t)
	out := map[string]string{}
	for p := m.Paths.PathItems.First(); p != nil; p = p.Next() {
		for o := p.Value().GetOperations().First(); o != nil; o = o.Next() {
			out[o.Value().OperationId] = strings.ToUpper(o.Key()) + " " + p.Key()
		}
	}
	return out
}

func TestContractREST(t *testing.T) {
	c := startContract(t)
	dir := t.TempDir()

	// ---- host ----
	c.call(t, "getHealth", 200, "GET", "/v1/health", "", "")
	info := c.call(t, "getInfo", 200, "GET", "/v1/info", "read-token", "")
	if v := jsonOf[map[string]any](t, info); v["identity"] == nil {
		t.Errorf("info = %s", info.Body)
	}
	c.call(t, "getInfo", 401, "GET", "/v1/info", "", "")

	// ---- sessions ----
	c.call(t, "listSessions", 200, "GET", "/v1/sessions", "read-token", "")
	runBody := `{"target":"` + dir + `","harness":"sh","name":"sh@contract","cmd":["sh","-c","sleep 60"]}`
	rec := c.call(t, "runSession", 201, "POST", "/v1/sessions", "admin-token", runBody, "Idempotency-Key", "c-run")
	if loc := rec.Header().Get("Location"); loc != "/v1/sessions/sh%40contract" {
		t.Errorf("Location = %q", loc)
	}
	rep := c.call(t, "runSession", 201, "POST", "/v1/sessions", "admin-token", runBody, "Idempotency-Key", "c-run")
	if rep.Header().Get("Idempotent-Replayed") != "true" {
		t.Errorf("replay header = %v", rep.Header())
	}
	c.call(t, "runSession", 409, "POST", "/v1/sessions", "admin-token", runBody)
	c.call(t, "runSession", 422, "POST", "/v1/sessions", "admin-token", `{"target":"`+dir+`","name":"other"}`, "Idempotency-Key", "c-run")
	c.callBad(t, "runSession", 400, "POST", "/v1/sessions", "admin-token", `{"harness":"sh"}`)
	c.call(t, "runSession", 401, "POST", "/v1/sessions", "", `{"target":"`+dir+`"}`)
	c.call(t, "runSession", 403, "POST", "/v1/sessions", "write-token", `{"target":"`+dir+`","harness":"bash","name":"nope"}`)
	c.callBad(t, "runSession", 413, "POST", "/v1/sessions", "admin-token", `{"target":"`+strings.Repeat("x", 70<<10)+`"}`)
	c.callBad(t, "runSession", 415, "POST", "/v1/sessions", "admin-token", runBody, "Content-Type", "text/plain")

	list := c.call(t, "listSessions", 200, "GET", "/v1/sessions", "read-token", "")
	etag := list.Header().Get("ETag")
	if etag == "" {
		t.Fatalf("list without ETag: %v", list.Header())
	}
	c.call(t, "listSessions", 304, "GET", "/v1/sessions", "read-token", "", "If-None-Match", etag)
	c.call(t, "listSessions", 401, "GET", "/v1/sessions", "", "")

	c.call(t, "getSession", 200, "GET", "/v1/sessions/sh%40contract", "read-token", "")
	c.call(t, "getSession", 404, "GET", "/v1/sessions/nope", "read-token", "")
	c.call(t, "getSession", 401, "GET", "/v1/sessions/nope", "", "")

	// ---- kill / remove ----
	c.call(t, "removeSession", 409, "DELETE", "/v1/sessions/sh%40contract", "write-token", "")
	c.call(t, "removeSession", 403, "DELETE", "/v1/sessions/sh%40contract", "read-token", "")
	c.callBad(t, "removeSession", 400, "DELETE", "/v1/sessions/sh%40contract?force=maybe", "write-token", "")
	c.call(t, "killSession", 404, "POST", "/v1/sessions/nope/kill", "write-token", "")
	c.call(t, "killSession", 403, "POST", "/v1/sessions/sh%40contract/kill", "read-token", "")
	c.callBad(t, "killSession", 400, "POST", "/v1/sessions/sh%40contract/kill", "write-token", `{"sig":"x"}`)
	c.call(t, "killSession", 202, "POST", "/v1/sessions/sh%40contract/kill", "write-token", `{"signal":"KILL"}`)
	c.waitExited(t, "sh@contract")
	c.call(t, "killSession", 409, "POST", "/v1/sessions/sh%40contract/kill", "write-token", "")
	c.call(t, "removeSession", 204, "DELETE", "/v1/sessions/sh%40contract", "write-token", "")
	c.call(t, "removeSession", 404, "DELETE", "/v1/sessions/sh%40contract", "write-token", "")
	c.call(t, "removeSession", 401, "DELETE", "/v1/sessions/sh%40contract", "", "")
	c.runSleeper(t, "sh@force")
	c.call(t, "removeSession", 204, "DELETE", "/v1/sessions/sh%40force?force=true", "write-token", "")

	// ---- view / act / terminal on the fake agent ----
	v := c.runFake(t, "v1", "idle")
	vr := c.call(t, "getView", 200, "GET", "/v1/sessions/v1/view", "read-token", "")
	vetag := vr.Header().Get("ETag")
	if vetag == "" || strings.HasPrefix(vetag, "W/") {
		t.Errorf("view ETag = %q", vetag)
	}
	c.call(t, "getView", 304, "GET", "/v1/sessions/v1/view", "read-token", "", "If-None-Match", vetag)
	c.call(t, "getView", 200, "GET", "/v1/sessions/v1/view?epoch="+v.Epoch+"&since="+strconv.FormatUint(v.Revision, 10), "read-token", "")
	c.call(t, "getView", 200, "GET", "/v1/sessions/v1/view?lines=1&from=0&scrollback=false", "read-token", "")
	c.call(t, "getView", 200, "GET", "/v1/sessions/v1/view?scrollback=true", "read-token", "")
	c.callBad(t, "getView", 400, "GET", "/v1/sessions/v1/view?lines=0", "read-token", "")
	c.call(t, "getView", 404, "GET", "/v1/sessions/nope/view", "read-token", "")
	c.call(t, "getView", 401, "GET", "/v1/sessions/v1/view", "", "")

	c.call(t, "ackView", 200, "POST", "/v1/sessions/v1/view/ack", "write-token", "")
	c.call(t, "ackView", 404, "POST", "/v1/sessions/nope/view/ack", "write-token", "")
	c.call(t, "ackView", 403, "POST", "/v1/sessions/v1/view/ack", "read-token", "")

	ar := c.call(t, "act", 200, "POST", "/v1/sessions/v1/act", "write-token", `{"action":"prompt","text":"hello contract"}`, "Idempotency-Key", "c-act")
	if r := jsonOf[map[string]any](t, ar); r["result"] != "submitted" {
		t.Errorf("act = %s", ar.Body)
	}
	waitTerminal(t, c.restEnv, "v1", "GOT: hello contract")
	c.call(t, "act", 200, "POST", "/v1/sessions/v1/act", "write-token", `{"action":"keys","keys":["esc"]}`)
	c.call(t, "act", 422, "POST", "/v1/sessions/v1/act", "write-token", `{"action":"prompt","text":"other"}`, "Idempotency-Key", "c-act")
	c.callBad(t, "act", 400, "POST", "/v1/sessions/v1/act", "write-token", `{"action":"prompt"}`)
	c.callBad(t, "act", 400, "POST", "/v1/sessions/v1/act", "write-token", `{"action":"prompt","text":"x","bogus":1}`)
	c.call(t, "act", 404, "POST", "/v1/sessions/nope/act", "write-token", `{"action":"interrupt"}`)
	c.call(t, "act", 403, "POST", "/v1/sessions/v1/act", "read-token", `{"action":"interrupt"}`)
	c.call(t, "act", 401, "POST", "/v1/sessions/v1/act", "", `{"action":"interrupt"}`)
	c.callBad(t, "act", 415, "POST", "/v1/sessions/v1/act", "write-token", `{"action":"interrupt"}`, "Content-Type", "text/plain")
	c.callBad(t, "act", 413, "POST", "/v1/sessions/v1/act", "write-token", `{"action":"prompt","text":"`+strings.Repeat("x", 70<<10)+`"}`)
	c.callBad(t, "act", 412, "POST", "/v1/sessions/v1/act", "write-token", `{"action":"interrupt"}`, "If-Match", `"e0-i0-r0"`)

	m := c.runFake(t, "m1", "waiting", "-menu", "Alpha|Beta|Gamma")
	var gamma string
	for _, o := range m.Options {
		if o.Label == "Gamma" {
			gamma = o.ID
		}
	}
	if gamma == "" {
		t.Fatalf("no Gamma option: %+v", m.Options)
	}
	cr := c.call(t, "act", 409, "POST", "/v1/sessions/m1/act", "write-token",
		`{"action":"choose","option_id":"`+gamma+`","label":"Gamma","revision":`+strconv.FormatUint(m.Revision, 10)+`,"epoch":"old-epoch"}`)
	if cr.Header().Get("ETag") == "" {
		t.Errorf("409 conflict without ETag: %v", cr.Header())
	}
	c.call(t, "act", 200, "POST", "/v1/sessions/m1/act", "write-token", `{"action":"choose","option_id":"`+gamma+`","label":"Gamma","revision":`+strconv.FormatUint(m.Revision, 10)+`,"epoch":"`+m.Epoch+`"}`)

	tr := c.call(t, "getTerminal", 200, "GET", "/v1/sessions/v1/terminal", "read-token", "")
	if !strings.Contains(tr.Body.String(), "GOT: hello contract") {
		t.Errorf("terminal = %s", tr.Body)
	}
	c.call(t, "getTerminal", 200, "GET", "/v1/sessions/v1/terminal?format=ansi&ghost=true", "read-token", "")
	pt := c.call(t, "getTerminal", 200, "GET", "/v1/sessions/v1/terminal", "read-token", "", "Accept", "text/plain")
	if !strings.HasPrefix(pt.Header().Get("Content-Type"), "text/plain") {
		t.Errorf("text/plain terminal Content-Type = %q", pt.Header().Get("Content-Type"))
	}
	c.callBad(t, "getTerminal", 400, "GET", "/v1/sessions/v1/terminal?format=ansi&scrollback=true", "read-token", "")
	c.call(t, "getTerminal", 404, "GET", "/v1/sessions/nope/terminal", "read-token", "")
	c.call(t, "getTerminal", 401, "GET", "/v1/sessions/v1/terminal", "", "")

	// ---- mailbox ----
	c.runSleeper(t, "box")
	self := jsonOf[struct{ Identity string }](t, info).Identity
	sent := c.call(t, "sendMessage", 201, "POST", "/v1/mailbox/messages", "write-token", `{"to":"box","subject":"s","body":"hi box"}`, "Idempotency-Key", "c-send")
	if m := jsonOf[proto.Message](t, sent); m.Recipient != "box" || m.State != "queued" {
		t.Errorf("sent = %s", sent.Body)
	}
	c.call(t, "sendMessage", 201, "POST", "/v1/mailbox/messages", "write-token", `{"to":"box","subject":"s","body":"hi box"}`, "Idempotency-Key", "c-send")
	c.call(t, "sendMessage", 422, "POST", "/v1/mailbox/messages", "write-token", `{"to":"box","body":"different"}`, "Idempotency-Key", "c-send")
	c.callBad(t, "sendMessage", 400, "POST", "/v1/mailbox/messages", "write-token", `{"to":"box","body":""}`)
	c.callBad(t, "sendMessage", 400, "POST", "/v1/mailbox/messages", "write-token", `{"to":"box","body":"x","from":"me"}`)
	c.call(t, "sendMessage", 403, "POST", "/v1/mailbox/messages", "read-token", `{"to":"box","body":"x"}`)
	c.call(t, "sendMessage", 401, "POST", "/v1/mailbox/messages", "", `{"to":"box","body":"x"}`)
	c.callBad(t, "sendMessage", 413, "POST", "/v1/mailbox/messages", "write-token", `{"to":"box","body":"`+strings.Repeat("x", 70<<10)+`"}`)
	own := jsonOf[proto.Message](t, c.call(t, "sendMessage", 201, "POST", "/v1/mailbox/messages", "write-token",
		`{"to":"`+self+`","subject":"n","body":"note"}`))
	id := strconv.FormatInt(own.ID, 10)

	c.call(t, "listMailbox", 200, "GET", "/v1/mailbox?limit=10&all=true", "read-token", "")
	c.call(t, "listMailbox", 200, "GET", "/v1/mailbox?mailbox=box&all=true", "admin-token", "")
	c.callBad(t, "listMailbox", 400, "GET", "/v1/mailbox?limit=0", "read-token", "")
	c.call(t, "listMailbox", 401, "GET", "/v1/mailbox", "", "")
	c.call(t, "listMailbox", 403, "GET", "/v1/mailbox?mailbox=box", "read-token", "")

	c.call(t, "getMessage", 200, "GET", "/v1/mailbox/messages/"+id, "read-token", "")
	c.call(t, "getMessage", 404, "GET", "/v1/mailbox/messages/99999", "read-token", "")
	c.callBad(t, "getMessage", 400, "GET", "/v1/mailbox/messages/abc", "read-token", "")
	c.call(t, "getMessage", 401, "GET", "/v1/mailbox/messages/"+id, "", "")
	c.call(t, "getMessage", 403, "GET", "/v1/mailbox/messages/"+id+"?mailbox=box", "read-token", "")

	rd := c.call(t, "markMessageRead", 200, "POST", "/v1/mailbox/messages/"+id+"/read", "write-token", "")
	if m := jsonOf[proto.Message](t, rd); m.State != "read" {
		t.Errorf("read = %s", rd.Body)
	}
	c.call(t, "markMessageRead", 404, "POST", "/v1/mailbox/messages/99999/read", "write-token", "")
	c.callBad(t, "markMessageRead", 400, "POST", "/v1/mailbox/messages/abc/read", "write-token", "")
	c.call(t, "markMessageRead", 403, "POST", "/v1/mailbox/messages/"+id+"/read", "read-token", "")
	c.call(t, "markMessageRead", 401, "POST", "/v1/mailbox/messages/"+id+"/read", "", "")

	c.call(t, "replyMessage", 201, "POST", "/v1/mailbox/messages/"+id+"/reply", "write-token", `{"body":"re: note"}`, "Idempotency-Key", "c-reply")
	c.call(t, "replyMessage", 422, "POST", "/v1/mailbox/messages/"+id+"/reply", "write-token", `{"body":"another"}`, "Idempotency-Key", "c-reply")
	c.callBad(t, "replyMessage", 400, "POST", "/v1/mailbox/messages/"+id+"/reply", "write-token", `{"body":""}`)
	c.call(t, "replyMessage", 404, "POST", "/v1/mailbox/messages/99999/reply", "write-token", `{"body":"x"}`)
	c.call(t, "replyMessage", 403, "POST", "/v1/mailbox/messages/"+id+"/reply", "read-token", `{"body":"x"}`)
	c.call(t, "replyMessage", 401, "POST", "/v1/mailbox/messages/"+id+"/reply", "", `{"body":"x"}`)

	// ---- daemon down: 503 on every daemon-backed operation ----
	down := newServer(t, &daemonEnv{}, restAuth(), nil)
	down.Dial = func() (*client.Client, error) { return nil, errors.New("daemon down") }
	d := &contractEnv{restEnv: &restEnv{h: down.Handler(), s: down}, v: c.v, covered: map[string]bool{}}
	d.call(t, "getHealth", 503, "GET", "/v1/health", "", "")
	d.call(t, "getInfo", 503, "GET", "/v1/info", "read-token", "")
	d.call(t, "listSessions", 503, "GET", "/v1/sessions", "read-token", "")
	d.call(t, "getSession", 503, "GET", "/v1/sessions/x", "read-token", "")
	d.call(t, "getView", 503, "GET", "/v1/sessions/x/view", "read-token", "")
	d.call(t, "getTerminal", 503, "GET", "/v1/sessions/x/terminal", "read-token", "")
	d.call(t, "listMailbox", 503, "GET", "/v1/mailbox", "read-token", "")
	d.call(t, "getMessage", 503, "GET", "/v1/mailbox/messages/1", "read-token", "")

	// Merge both runs: every operation (but the streams and pairing, covered
	// below) needs a happy-path status and at least one documented error.
	for k := range d.covered {
		c.covered[k] = true
	}
	c.assertCoverage(t)
}

// assertCoverage fails when an operation of the spec has no exercised 2xx
// (3xx) or no exercised 4xx/5xx status (streams are covered by the SSE tests,
// pairing lives in internal/apitoken and cmd/agentd).
func (c *contractEnv) assertCoverage(t *testing.T) {
	t.Helper()
	skip := map[string]bool{"streamView": true, "streamEvents": true, "pairDevice": true}
	ops := specOperations(t)
	var names []string
	for op := range ops {
		names = append(names, op)
	}
	sort.Strings(names)
	for _, op := range names {
		if skip[op] {
			continue
		}
		var ok, bad bool
		for k := range c.covered {
			name, status, _ := strings.Cut(k, " ")
			if name != op {
				continue
			}
			n, _ := strconv.Atoi(status)
			if n < 400 {
				ok = true
			} else {
				bad = true
			}
		}
		if !ok || !bad {
			t.Errorf("operation %s (%s) not exercised: happy=%v error=%v", op, ops[op], ok, bad)
		}
	}
}

func (c *contractEnv) waitExited(t *testing.T, name string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		rec := c.req(t, "GET", "/v1/sessions/"+enc(name), "read-token", "")
		if rec.Code == 200 && jsonOf[proto.SessionInfo](t, rec).State == "exited" {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("%s did not exit", name)
}

// ---- SSE ----

// validateFrame checks a frame's data payload against the schema the spec's
// x-sse-events assigns to its event name ("*" is the fallback).
func validateFrame(t *testing.T, path string, schemas map[string]string, f sseFrame) {
	t.Helper()
	name, ok := schemas[f.event]
	if !ok {
		name, ok = schemas["*"]
	}
	if !ok {
		t.Errorf("%s: event %q (%s) has no schema in x-sse-events", path, f.event, f.data)
		return
	}
	if !json.Valid([]byte(f.data)) {
		t.Errorf("%s: event %q data is not JSON: %q", path, f.event, f.data)
		return
	}
	if errs := validateAgainst(t, name, f.data); len(errs) > 0 {
		t.Errorf("%s: event %q violates %s: %s\n%s", path, f.event, name, strings.Join(errs, "; "), f.data)
	}
}

func TestContractSSEEvents(t *testing.T) {
	const path = "/v1/events"
	e := newSSEEnv(t, nil)
	schemas := sseSchemas(t, path)
	seen := map[string]bool{}
	check := func(c *sseConn, until func(sseFrame) bool, d time.Duration) {
		t.Helper()
		deadline := time.After(d)
		for {
			select {
			case f, ok := <-c.frames:
				if !ok {
					return
				}
				if f.comment != "" {
					continue
				}
				seen[f.event] = true
				validateFrame(t, path, schemas, f)
				if until(f) {
					return
				}
			case <-deadline:
				t.Fatalf("timeout; seen %v", seen)
			}
		}
	}

	c := e.events(t, "")
	if h := c.resp.Header.Get("Content-Type"); !strings.HasPrefix(h, "text/event-stream") {
		t.Errorf("Content-Type = %q", h)
	}
	if h := c.resp.Header.Get("Cache-Control"); !strings.Contains(h, "no-cache") {
		t.Errorf("Cache-Control = %q", h)
	}
	e.run(t, "sh@sse", "sh", "-c", "sleep 0.3; echo done")
	if _, err := e.daemon(t).Send(proto.SendArgs{To: "sh@sse", Subject: "s", Body: "b"}); err != nil {
		t.Fatal(err)
	}
	check(c, func(f sseFrame) bool { return f.event == "session.exited" }, 15*time.Second)

	// Resume with an unknown id: hello then resync.
	r := openSSE(t, e.ts.URL+path, "read-token", "bogus:1")
	n := 0
	check(r, func(f sseFrame) bool { n++; return f.event == "resync" || n > 3 }, 5*time.Second)

	// Filtered stream and bye on shutdown.
	b := e.events(t, "?kind=status.changed")
	e.s.Close()
	check(b, func(f sseFrame) bool { return f.event == "bye" }, 10*time.Second)

	for _, want := range []string{"hello", "resync", "bye", "session.exited"} {
		if !seen[want] {
			t.Errorf("%s: never saw event %q (seen %v)", path, want, seen)
		}
	}
}

func TestContractSSEViewStream(t *testing.T) {
	c := startContract(t)
	ts := httptest.NewServer(c.s.Handler())
	t.Cleanup(ts.Close)
	t.Cleanup(c.s.Close)
	schemas := sseSchemas(t, "/v1/sessions/{name}/view/stream")
	c.runFake(t, "vs1", "idle")
	conn := openSSE(t, ts.URL+"/v1/sessions/vs1/view/stream", "read-token", "")
	seen := map[string]bool{}
	pump := func(until string, d time.Duration) {
		t.Helper()
		deadline := time.After(d)
		for {
			select {
			case f, ok := <-conn.frames:
				if !ok {
					t.Fatalf("stream ended before %q (seen %v)", until, seen)
				}
				if f.comment != "" {
					continue
				}
				seen[f.event] = true
				validateFrame(t, "view/stream", schemas, f)
				if f.event == until {
					return
				}
			case <-deadline:
				t.Fatalf("timeout waiting for %q (seen %v)", until, seen)
			}
		}
	}
	pump("view", 10*time.Second)
	c.must(t, 200, "POST", "/v1/sessions/vs1/act", "write-token", `{"action":"prompt","text":"stream me"}`)
	pump("view.patch", 10*time.Second)
	c.waitStatus(t, "vs1", "idle")
	c.must(t, 200, "POST", "/v1/sessions/vs1/act", "write-token", `{"action":"prompt","text":"/exit 0"}`)
	pump("session.exited", 10*time.Second)
	pump("bye", 10*time.Second)
	c.must(t, 204, "DELETE", "/v1/sessions/vs1", "write-token", "")

	for _, want := range []string{"view", "view.patch", "status.changed", "session.exited", "bye"} {
		if !seen[want] {
			t.Errorf("view/stream: never saw %q (seen %v)", want, seen)
		}
	}
}
