package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/digiogithub/agentd/internal/client"
	"github.com/digiogithub/agentd/internal/config"
	"github.com/digiogithub/agentd/internal/daemon"
	"github.com/digiogithub/agentd/internal/mailbox"
	"github.com/digiogithub/agentd/internal/proto"
	"github.com/digiogithub/agentd/internal/testutil"
)

const restConfig = `
[profiles.fake]
idle_ms = 300
human_grace_ms = 0
prompt_marker = "❯"
ghost_right_of_cursor = true
`

// restAuth: "read-token" = read, "write-token" = write, "admin-token" = admin.
func restAuth() Authenticator {
	return AuthenticatorFunc(func(r *http.Request, need Scope) (*Principal, error) {
		switch r.Header.Get("Authorization") {
		case "Bearer read-token":
			return &Principal{Device: "ro", Scope: ScopeRead}, nil
		case "Bearer write-token":
			return &Principal{Device: "rw", Scope: ScopeWrite}, nil
		case "Bearer admin-token":
			return &Principal{Device: "adm", Scope: ScopeAdmin}, nil
		}
		return nil, ErrUnauthenticated
	})
}

type restEnv struct {
	sock string
	h    http.Handler
	s    *Server
}

func startREST(t *testing.T) *restEnv {
	t.Helper()
	dir, err := os.MkdirTemp("", "agentd-r")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	sock := filepath.Join(dir, "agentd.sock")
	cfgPath := filepath.Join(dir, "config.toml")
	if err := os.WriteFile(cfgPath, []byte(restConfig), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(config.EnvSocket, sock)
	t.Setenv(config.EnvStateDir, filepath.Join(dir, "state"))
	t.Setenv(config.EnvConfig, cfgPath)
	if err := config.EnsureDirs(); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.LoadDefault()
	if err != nil {
		t.Fatal(err)
	}
	dbPath, _ := config.MailboxPath()
	store, err := mailbox.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	srv := daemon.New(cfg, nil, store, nil)
	if err := srv.Listen(); err != nil {
		t.Fatal(err)
	}
	go func() { _ = srv.Serve(context.Background()) }()
	t.Cleanup(srv.Shutdown)
	d := &daemonEnv{sock: sock, srv: srv}
	s := newServer(t, d, restAuth(), nil)
	s.Config.MaxBodyKB = 64
	s.Config.RunHarnesses = []string{"sh"}
	return &restEnv{sock: sock, h: s.Handler(), s: s}
}

func (e *restEnv) dial(t *testing.T) *client.Client {
	t.Helper()
	c, err := client.Dial(e.sock)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return c
}

func (e *restEnv) req(t *testing.T, method, path, token, body string, hdr ...string) *httptest.ResponseRecorder {
	t.Helper()
	var rd *strings.Reader
	if body != "" {
		rd = strings.NewReader(body)
	} else {
		rd = strings.NewReader("")
	}
	r := httptest.NewRequest(method, path, rd)
	if body != "" {
		r.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	for i := 0; i+1 < len(hdr); i += 2 {
		r.Header.Set(hdr[i], hdr[i+1])
	}
	rec := httptest.NewRecorder()
	e.h.ServeHTTP(rec, r)
	return rec
}

func (e *restEnv) must(t *testing.T, status int, method, path, token, body string, hdr ...string) *httptest.ResponseRecorder {
	t.Helper()
	rec := e.req(t, method, path, token, body, hdr...)
	if rec.Code != status {
		t.Fatalf("%s %s = %d, want %d: %s", method, path, rec.Code, status, rec.Body)
	}
	return rec
}

func jsonOf[T any](t *testing.T, rec *httptest.ResponseRecorder) T {
	t.Helper()
	var v T
	if err := json.Unmarshal(rec.Body.Bytes(), &v); err != nil {
		t.Fatalf("body %q: %v", rec.Body.String(), err)
	}
	return v
}

func enc(name string) string { return strings.ReplaceAll(url.PathEscape(name), "@", "%40") }

// runSleeper starts a long-lived `sh -c "sleep 60"` session as admin.
func (e *restEnv) runSleeper(t *testing.T, name string) {
	t.Helper()
	body := `{"target":"` + t.TempDir() + `","harness":"sh","name":"` + name + `","cmd":["sh","-c","sleep 60"]}`
	e.must(t, 201, "POST", "/v1/sessions", "admin-token", body)
}

// runMenu starts the fakeagent with args as the "fake" profile and waits for
// the given semantic status.
func (e *restEnv) runFake(t *testing.T, name, status string, args ...string) proto.View {
	t.Helper()
	cmd, _ := json.Marshal(append([]string{testutil.BuildFakeAgent(t)}, args...))
	body := `{"target":"` + t.TempDir() + `","harness":"fake","profile":"fake","name":"` + name + `","cmd":` + string(cmd) + `}`
	e.must(t, 201, "POST", "/v1/sessions", "admin-token", body)
	return e.waitStatus(t, name, status)
}

func (e *restEnv) waitStatus(t *testing.T, name, status string) proto.View {
	t.Helper()
	var v proto.View
	testutil.WaitFor(t, 10*time.Second, func() bool {
		rec := e.req(t, "GET", "/v1/sessions/"+enc(name)+"/view", "read-token", "")
		if rec.Code != 200 {
			return false
		}
		v = jsonOf[proto.View](t, rec)
		return v.Status == status
	}, name+" should be "+status, func() string { return "" })
	return v
}

func TestRESTScopeDenial(t *testing.T) {
	e := startREST(t)
	for _, tc := range []struct{ method, path, body string }{
		{"POST", "/v1/sessions", `{"target":"/tmp"}`},
		{"DELETE", "/v1/sessions/x", ""},
		{"POST", "/v1/sessions/x/kill", ""},
		{"POST", "/v1/sessions/x/view/ack", ""},
		{"POST", "/v1/sessions/x/act", `{"action":"interrupt"}`},
		{"POST", "/v1/mailbox/messages", `{"to":"x","body":"b"}`},
		{"POST", "/v1/mailbox/messages/1/read", ""},
		{"POST", "/v1/mailbox/messages/1/reply", `{"body":"b"}`},
	} {
		if rec := e.req(t, tc.method, tc.path, "read-token", tc.body); rec.Code != 403 || errCode(t, rec) != "forbidden" {
			t.Errorf("read token %s %s = %d %s", tc.method, tc.path, rec.Code, rec.Body)
		}
	}
	for _, tc := range []struct{ method, path string }{
		{"GET", "/v1/sessions"}, {"GET", "/v1/sessions/x"}, {"GET", "/v1/sessions/x/view"},
		{"GET", "/v1/sessions/x/terminal"}, {"GET", "/v1/mailbox"}, {"GET", "/v1/mailbox/messages/1"},
	} {
		if rec := e.req(t, tc.method, tc.path, "", ""); rec.Code != 401 {
			t.Errorf("no token %s %s = %d", tc.method, tc.path, rec.Code)
		}
	}
	// read is enough for the GETs (404 = past auth).
	if rec := e.req(t, "GET", "/v1/sessions/nope", "read-token", ""); rec.Code != 404 || errCode(t, rec) != "not_found" {
		t.Errorf("get unknown = %d %s", rec.Code, rec.Body)
	}
}

func TestRESTSessionLifecycle(t *testing.T) {
	e := startREST(t)
	name := "sh@life.v1"
	body := `{"target":"` + t.TempDir() + `","harness":"sh","name":"` + name + `","cmd":["sh","-c","sleep 60"]}`
	rec := e.must(t, 201, "POST", "/v1/sessions", "admin-token", body)
	if loc := rec.Header().Get("Location"); loc != "/v1/sessions/sh%40life.v1" {
		t.Errorf("Location = %q", loc)
	}
	if si := jsonOf[proto.SessionInfo](t, rec); si.Name != name {
		t.Errorf("created = %+v", si)
	}
	e.must(t, 409, "POST", "/v1/sessions", "admin-token", body)

	rec = e.must(t, 200, "GET", "/v1/sessions", "read-token", "")
	list := jsonOf[struct{ Sessions []proto.SessionInfo }](t, rec)
	if len(list.Sessions) != 1 || list.Sessions[0].Name != name {
		t.Fatalf("list = %+v", list)
	}
	etag := rec.Header().Get("ETag")
	if !strings.HasPrefix(etag, `W/"`) {
		t.Errorf("list ETag = %q", etag)
	}
	e.must(t, 304, "GET", "/v1/sessions", "read-token", "", "If-None-Match", etag)

	rec = e.must(t, 200, "GET", "/v1/sessions/"+enc(name), "read-token", "")
	if si := jsonOf[proto.SessionInfo](t, rec); si.Name != name || si.State != "running" {
		t.Errorf("get = %+v", si)
	}
	// Still running: no force = 409 still_running.
	if rec := e.req(t, "DELETE", "/v1/sessions/"+enc(name), "write-token", ""); rec.Code != 409 || errCode(t, rec) != "still_running" {
		t.Errorf("delete running = %d %s", rec.Code, rec.Body)
	}
	e.must(t, 400, "DELETE", "/v1/sessions/"+enc(name)+"?force=maybe", "write-token", "")

	e.must(t, 202, "POST", "/v1/sessions/"+enc(name)+"/kill", "write-token", `{"signal":"KILL"}`)
	testutil.WaitFor(t, 10*time.Second, func() bool {
		si := jsonOf[proto.SessionInfo](t, e.must(t, 200, "GET", "/v1/sessions/"+enc(name), "read-token", ""))
		return si.State == "exited"
	}, "session exits", func() string { return "" })
	e.must(t, 204, "DELETE", "/v1/sessions/"+enc(name), "write-token", "")
	e.must(t, 404, "GET", "/v1/sessions/"+enc(name), "read-token", "")

	// kill with a bad body / unknown field
	e.must(t, 400, "POST", "/v1/sessions/x/kill", "write-token", `{"sig":"x"}`)

	// force delete of a running session
	e.runSleeper(t, "sh@force")
	e.must(t, 204, "DELETE", "/v1/sessions/sh%40force?force=true", "write-token", "")
	e.must(t, 404, "GET", "/v1/sessions/sh%40force", "read-token", "")
	rec = e.must(t, 200, "GET", "/v1/sessions", "read-token", "")
	if l := jsonOf[struct{ Sessions []proto.SessionInfo }](t, rec); len(l.Sessions) != 0 {
		t.Errorf("sessions left: %+v", l)
	}
}

func TestRESTRunHarnessPolicy(t *testing.T) {
	e := startREST(t)
	dir := t.TempDir()
	run := func(token, extra string) *httptest.ResponseRecorder {
		return e.req(t, "POST", "/v1/sessions", token, `{"target":"`+dir+`",`+extra+`}`)
	}
	// Harness outside run_harnesses: write denied, admin allowed.
	if rec := run("write-token", `"harness":"bash","name":"b1"`); rec.Code != 403 || errCode(t, rec) != "forbidden" {
		t.Errorf("write + bash = %d %s", rec.Code, rec.Body)
	}
	// cmd / env need admin even for an allowed harness.
	if rec := run("write-token", `"harness":"sh","name":"b2","cmd":["sh","-c","true"]`); rec.Code != 403 {
		t.Errorf("write + cmd = %d %s", rec.Code, rec.Body)
	}
	if rec := run("write-token", `"harness":"sh","name":"b3","env":{"A":"1"}`); rec.Code != 403 {
		t.Errorf("write + env = %d %s", rec.Code, rec.Body)
	}
	// args choose what is executed: admin only (even for an allowed harness).
	if rec := run("write-token", `"harness":"sh","name":"ok1","args":["-c","sleep 60"]`); rec.Code != 403 {
		t.Fatalf("write + args = %d %s", rec.Code, rec.Body)
	}
	if rec := run("admin-token", `"harness":"sh","name":"ok1","args":["-c","sleep 60"]`); rec.Code != 201 {
		t.Fatalf("admin + args = %d %s", rec.Code, rec.Body)
	}
	// Allowed harness without args/cmd/env: write is enough.
	if rec := run("write-token", `"harness":"sh","name":"ok0"`); rec.Code != 201 {
		t.Fatalf("write + allowed harness = %d %s", rec.Code, rec.Body)
	}
	// Admin override: a harness outside the list, with cmd and env.
	rec := run("admin-token", `"harness":"bash","name":"ok2","cmd":["sh","-c","sleep 60"],"env":{"FOO":"bar"}`)
	if rec.Code != 201 {
		t.Fatalf("admin override = %d %s", rec.Code, rec.Body)
	}
	// Validation.
	for _, tc := range []struct{ name, body string }{
		{"no target", `{"harness":"sh"}`},
		{"relative path", `{"target":"sub/dir","harness":"sh"}`},
		{"dotdot", `{"target":"..","harness":"sh"}`},
		{"bad name", `{"target":"` + dir + `","harness":"sh","name":"a b"}`},
		{"args and cmd", `{"target":"` + dir + `","harness":"sh","args":["x"],"cmd":["sh"]}`},
		{"unknown field", `{"target":"` + dir + `","cols":10}`},
	} {
		if rec := e.req(t, "POST", "/v1/sessions", "admin-token", tc.body); rec.Code != 400 {
			t.Errorf("%s = %d %s", tc.name, rec.Code, rec.Body)
		}
	}
	// Unknown alias is a client error, not a 5xx.
	if rec := e.req(t, "POST", "/v1/sessions", "admin-token", `{"target":"no-such-alias","harness":"sh"}`); rec.Code < 400 || rec.Code >= 500 {
		t.Errorf("unknown alias = %d %s", rec.Code, rec.Body)
	}
}

func TestRESTViewETag(t *testing.T) {
	e := startREST(t)
	e.runFake(t, "v1", "idle")
	rec := e.must(t, 200, "GET", "/v1/sessions/v1/view", "read-token", "")
	v := jsonOf[proto.View](t, rec)
	etag := rec.Header().Get("ETag")
	if ref, ok := parseETagRef(etag); !ok || ref.epoch != v.Epoch || ref.rev != v.Revision || ref.inst == "" || ref.weak {
		t.Fatalf("ETag = %q (%+v)", etag, ref)
	}
	if v.Name != "v1" || len(v.Lines) == 0 {
		t.Errorf("view = %+v", v)
	}
	rec = e.must(t, 304, "GET", "/v1/sessions/v1/view", "read-token", "", "If-None-Match", etag)
	if rec.Body.Len() != 0 || rec.Header().Get("ETag") != etag {
		t.Errorf("304 = %q %v", rec.Body, rec.Header())
	}
	// since + epoch: unchanged marker (without epoch the daemon never says
	// not_modified); stale epoch: full view.
	rec = e.must(t, 200, "GET", "/v1/sessions/v1/view?epoch="+v.Epoch+"&since="+strconv.FormatUint(v.Revision, 10), "read-token", "")
	if u := jsonOf[map[string]any](t, rec); u["unchanged"] != true || u["revision"] != float64(v.Revision) {
		t.Errorf("unchanged = %v", u)
	}
	rec = e.must(t, 200, "GET", "/v1/sessions/v1/view", "read-token", "", "If-None-Match", `"eother-r`+strconv.FormatUint(v.Revision, 10)+`"`)
	if jsonOf[proto.View](t, rec).Name != "v1" {
		t.Errorf("stale epoch should return the full view: %s", rec.Body)
	}
	if v2 := jsonOf[map[string]any](t, e.must(t, 200, "GET", "/v1/sessions/v1/view?since="+strconv.FormatUint(v.Revision, 10), "read-token", "")); v2["unchanged"] != nil {
		t.Errorf("since without epoch = %v", v2)
	}
	// An older revision gets the full view.
	e.must(t, 200, "GET", "/v1/sessions/v1/view?since=0", "read-token", "")
	// lines / from / bad params.
	rec = e.must(t, 200, "GET", "/v1/sessions/v1/view?lines=1&from=0", "read-token", "")
	if n := len(jsonOf[proto.View](t, rec).Lines); n != 1 {
		t.Errorf("lines=1 returned %d lines", n)
	}
	for _, q := range []string{"lines=0", "lines=99999", "since=x", "from=-1", "scrollback=zz"} {
		e.must(t, 400, "GET", "/v1/sessions/v1/view?"+q, "read-token", "")
	}
	e.must(t, 404, "GET", "/v1/sessions/nope/view", "read-token", "")
	// ack: humans clear attention; the revision is returned.
	rec = e.must(t, 200, "POST", "/v1/sessions/v1/view/ack", "write-token", "")
	if _, ok := jsonOf[map[string]any](t, rec)["revision"]; !ok {
		t.Errorf("ack = %s", rec.Body)
	}
}

func TestRESTActPromptAndKeys(t *testing.T) {
	e := startREST(t)
	e.runFake(t, "p1", "idle")
	rec := e.must(t, 200, "POST", "/v1/sessions/p1/act", "write-token", `{"action":"prompt","text":"hello rest"}`)
	res := jsonOf[map[string]any](t, rec)
	if res["action"] != "prompt" || res["result"] != "submitted" || res["submitted"] != true {
		t.Errorf("act = %v", res)
	}
	waitTerminal(t, e, "p1", "GOT: hello rest")
	e.must(t, 200, "POST", "/v1/sessions/p1/act", "write-token", `{"action":"keys","keys":["esc"]}`)
	// Validation.
	e.must(t, 400, "POST", "/v1/sessions/p1/act", "write-token", `{}`)
	e.must(t, 400, "POST", "/v1/sessions/p1/act", "write-token", `{"action":"prompt"}`)
	e.must(t, 400, "POST", "/v1/sessions/p1/act", "write-token", `{"action":"prompt","text":"x","bogus":1}`)
	e.must(t, 400, "POST", "/v1/sessions/p1/act", "write-token", `{"action":"keys","keys":["esc"]}`, "If-Match", "garbage")
	e.must(t, 404, "POST", "/v1/sessions/nope/act", "write-token", `{"action":"interrupt"}`)
}

func waitTerminal(t *testing.T, e *restEnv, name, want string) {
	t.Helper()
	testutil.WaitFor(t, 10*time.Second, func() bool {
		rec := e.req(t, "GET", "/v1/sessions/"+enc(name)+"/terminal", "read-token", "")
		return rec.Code == 200 && strings.Contains(rec.Body.String(), want)
	}, "terminal shows "+want, func() string { return "" })
}

func TestRESTActChooseAndConflict(t *testing.T) {
	e := startREST(t)
	v := e.runFake(t, "m1", "waiting", "-menu", "Alpha|Beta|Gamma")
	var gamma string
	for _, o := range v.Options {
		if o.Label == "Gamma" {
			gamma = o.ID
		}
	}
	if gamma == "" {
		t.Fatalf("no Gamma option: %+v", v.Options)
	}
	rev := strconv.FormatUint(v.Revision, 10)

	// Stale epoch: 409 conflict with the fresh view in details.
	body := `{"action":"choose","option_id":"` + gamma + `","label":"Gamma","revision":` + rev + `,"epoch":"old-epoch"}`
	rec := e.must(t, 409, "POST", "/v1/sessions/m1/act", "write-token", body)
	var env struct {
		Error struct {
			Code    string
			Details struct {
				Revision uint64
				View     proto.View
			}
		}
	}
	decodeBody(t, rec, &env)
	if env.Error.Code != "conflict" || env.Error.Details.View.Name != "m1" || env.Error.Details.Revision != v.Revision ||
		len(env.Error.Details.View.Options) != 3 {
		t.Fatalf("conflict = %s", rec.Body)
	}
	if et := rec.Header().Get("ETag"); !strings.HasPrefix(et, `"e`+v.Epoch+`-i`) || !strings.HasSuffix(et, `-r`+rev+`"`) {
		t.Errorf("conflict ETag = %q", rec.Header().Get("ETag"))
	}
	// If-Match with a stale epoch: 412 precondition_failed.
	rec = e.must(t, 412, "POST", "/v1/sessions/m1/act", "write-token",
		`{"action":"choose","option_id":"`+gamma+`","label":"Gamma"}`, "If-Match", `"eold-epoch-r`+rev+`"`)
	if errCode(t, rec) != "precondition_failed" {
		t.Errorf("412 = %s", rec.Body)
	}
	// Missing revision is a client error.
	e.must(t, 400, "POST", "/v1/sessions/m1/act", "write-token", `{"action":"choose","option_id":"`+gamma+`"}`)

	// Good choose, revision + epoch supplied through If-Match.
	rec = e.must(t, 200, "POST", "/v1/sessions/m1/act", "write-token",
		`{"action":"choose","option_id":"`+gamma+`","label":"Gamma"}`, "If-Match", `"e`+v.Epoch+`-r`+rev+`"`)
	if r := jsonOf[map[string]any](t, rec); r["action"] != "choose" || r["result"] != "selected" {
		t.Errorf("choose = %v", r)
	}
	waitTerminal(t, e, "m1", "chose: Gamma")
}

func TestRESTChooseBodyEpoch(t *testing.T) {
	e := startREST(t)
	v := e.runFake(t, "m2", "waiting", "-menu", "Alpha|Beta")
	id := v.Options[1].ID
	body := `{"action":"choose","option_id":"` + id + `","revision":` + strconv.FormatUint(v.Revision, 10) + `,"epoch":"` + v.Epoch + `"}`
	e.must(t, 200, "POST", "/v1/sessions/m2/act", "write-token", body)
	waitTerminal(t, e, "m2", "chose: "+v.Options[1].Label)
}

func TestRESTIdempotency(t *testing.T) {
	e := startREST(t)
	e.runFake(t, "i1", "idle")
	body := `{"action":"prompt","text":"once only"}`
	rec1 := e.must(t, 200, "POST", "/v1/sessions/i1/act", "write-token", body, "Idempotency-Key", "k-1")
	if rec1.Header().Get("Idempotent-Replayed") != "" {
		t.Error("first response marked as replay")
	}
	waitTerminal(t, e, "i1", "GOT: once only")
	rec2 := e.must(t, 200, "POST", "/v1/sessions/i1/act", "write-token", body, "Idempotency-Key", "k-1")
	if rec2.Header().Get("Idempotent-Replayed") != "true" || rec2.Body.String() != rec1.Body.String() {
		t.Errorf("replay = %v %q vs %q", rec2.Header(), rec2.Body, rec1.Body)
	}
	time.Sleep(500 * time.Millisecond)
	term := e.must(t, 200, "GET", "/v1/sessions/i1/terminal", "read-token", "")
	if n := strings.Count(jsonOf[map[string]any](t, term)["text"].(string), "GOT: once only"); n != 1 {
		t.Errorf("prompt typed %d times", n)
	}
	// Same key, different body: 422. Different device: independent.
	rec := e.must(t, 422, "POST", "/v1/sessions/i1/act", "write-token", `{"action":"prompt","text":"other"}`, "Idempotency-Key", "k-1")
	if errCode(t, rec) != "idempotency_mismatch" {
		t.Errorf("422 = %s", rec.Body)
	}
	e.must(t, 200, "POST", "/v1/sessions/i1/act", "admin-token", `{"action":"prompt","text":"other"}`, "Idempotency-Key", "k-1")
	// Bad key format.
	e.must(t, 400, "POST", "/v1/sessions/i1/act", "write-token", body, "Idempotency-Key", "bad key!")

	// Run: replay returns the original 201 instead of 409 already_exists.
	run := `{"target":"` + t.TempDir() + `","harness":"sh","name":"idem-run","cmd":["sh","-c","sleep 60"]}`
	r1 := e.must(t, 201, "POST", "/v1/sessions", "admin-token", run, "Idempotency-Key", "run-1")
	r2 := e.must(t, 201, "POST", "/v1/sessions", "admin-token", run, "Idempotency-Key", "run-1")
	if r2.Header().Get("Idempotent-Replayed") != "true" || r2.Body.String() != r1.Body.String() ||
		r2.Header().Get("Location") != r1.Header().Get("Location") {
		t.Errorf("run replay = %v", r2.Header())
	}
	e.must(t, 409, "POST", "/v1/sessions", "admin-token", run, "Idempotency-Key", "run-2")
}

func TestIdemStoreTTLAndFailures(t *testing.T) {
	st := newIdemStore(time.Minute)
	now := time.Now()
	st.now = func() time.Time { return now }
	var h [32]byte
	if s, _ := st.begin("d", "k", h); s != idemNew {
		t.Fatal("first begin")
	}
	if s, _ := st.begin("d", "k", h); s != idemInFlight {
		t.Fatal("in flight")
	}
	st.finish("d", "k", 201, http.Header{}, []byte("x"))
	if s, _ := st.begin("d", "k", h); s != idemReplay {
		t.Fatal("replay")
	}
	now = now.Add(2 * time.Minute)
	if s, _ := st.begin("d", "k", h); s != idemNew {
		t.Fatal("expired entry should be new")
	}
	// A failed attempt (no 2xx) is forgotten so the key can be retried.
	e := startREST(t)
	body := `{"to":"nobody-here","body":"x"}`
	if rec := e.req(t, "POST", "/v1/mailbox/messages", "write-token", body, "Idempotency-Key", "f1"); rec.Code < 400 {
		t.Fatalf("send to unknown = %d", rec.Code)
	}
	if rec := e.req(t, "POST", "/v1/mailbox/messages", "write-token", body, "Idempotency-Key", "f1"); rec.Header().Get("Idempotent-Replayed") != "" {
		t.Error("error responses must not be replayed")
	}
}

func TestRESTMailbox(t *testing.T) {
	e := startREST(t)
	e.runSleeper(t, "box")
	self := jsonOf[struct{ Identity string }](t, e.must(t, 200, "GET", "/v1/info", "read-token", "")).Identity
	if !strings.HasPrefix(self, "human:") {
		t.Fatalf("identity = %q", self)
	}
	// Send to a session (queued) and to the gateway's own mailbox.
	rec := e.must(t, 201, "POST", "/v1/mailbox/messages", "write-token", `{"to":"box","subject":"s","body":"hi box"}`, "Idempotency-Key", "m-1")
	if m := jsonOf[proto.Message](t, rec); m.Sender != self || m.Recipient != "box" || m.State != "queued" {
		t.Errorf("sent = %+v", m)
	}
	rep := e.must(t, 201, "POST", "/v1/mailbox/messages", "write-token", `{"to":"box","subject":"s","body":"hi box"}`, "Idempotency-Key", "m-1")
	if rep.Header().Get("Idempotent-Replayed") != "true" {
		t.Error("send not replayed")
	}
	if rec := e.req(t, "POST", "/v1/mailbox/messages", "write-token", `{"to":"ghost-session","body":"x"}`); rec.Code != 400 && rec.Code != 404 {
		t.Errorf("unknown recipient = %d %s", rec.Code, rec.Body)
	}
	e.must(t, 400, "POST", "/v1/mailbox/messages", "write-token", `{"to":"bad recipient","body":"x"}`)
	e.must(t, 400, "POST", "/v1/mailbox/messages", "write-token", `{"to":"box","body":""}`)
	e.must(t, 400, "POST", "/v1/mailbox/messages", "write-token", `{"to":"box","body":"x","from":"me"}`)

	var ids []int64
	for i := 0; i < 3; i++ {
		m := jsonOf[proto.Message](t, e.must(t, 201, "POST", "/v1/mailbox/messages", "write-token",
			`{"to":"`+self+`","subject":"n`+strconv.Itoa(i)+`","body":"note `+strconv.Itoa(i)+`"}`))
		ids = append(ids, m.ID)
	}
	// Peek at the session's mailbox: no state change.
	rec = e.must(t, 200, "GET", "/v1/mailbox?mailbox=box&all=true", "admin-token", "")
	if l := jsonOf[struct{ Messages []proto.Message }](t, rec); len(l.Messages) != 1 || l.Messages[0].State != "queued" {
		t.Errorf("peek = %s", rec.Body)
	}
	// Own mailbox: newest first, paginated.
	rec = e.must(t, 200, "GET", "/v1/mailbox?limit=2", "read-token", "")
	page := jsonOf[struct {
		Messages   []proto.Message
		NextBefore int64 `json:"next_before"`
	}](t, rec)
	if len(page.Messages) != 2 || page.Messages[0].ID != ids[2] || page.Messages[1].ID != ids[1] || page.NextBefore != ids[1] {
		t.Fatalf("page 1 = %s", rec.Body)
	}
	rec = e.must(t, 200, "GET", "/v1/mailbox?limit=2&before="+strconv.FormatInt(page.NextBefore, 10), "read-token", "")
	page2 := jsonOf[map[string]any](t, rec)
	if msgs := page2["messages"].([]any); len(msgs) != 1 || page2["next_before"] != nil {
		t.Fatalf("page 2 = %s", rec.Body)
	}
	for _, q := range []string{"limit=0", "limit=500", "before=x", "all=zz", "mailbox=bad%20box"} {
		e.must(t, 400, "GET", "/v1/mailbox?"+q, "read-token", "")
	}

	// GET never marks read; POST read does.
	id := strconv.FormatInt(ids[0], 10)
	if m := jsonOf[proto.Message](t, e.must(t, 200, "GET", "/v1/mailbox/messages/"+id, "read-token", "")); m.State == "read" {
		t.Errorf("GET marked the message read: %+v", m)
	}
	if m := jsonOf[proto.Message](t, e.must(t, 200, "GET", "/v1/mailbox/messages/"+id, "read-token", "")); m.State == "read" {
		t.Errorf("second GET: %+v", m)
	}
	if m := jsonOf[proto.Message](t, e.must(t, 200, "POST", "/v1/mailbox/messages/"+id+"/read", "write-token", "")); m.State != "read" {
		t.Errorf("read = %+v", m)
	}
	// Peek at another mailbox through GET ?mailbox=.
	boxMsg := jsonOf[struct{ Messages []proto.Message }](t, e.must(t, 200, "GET", "/v1/mailbox?mailbox=box&all=true", "admin-token", "")).Messages[0]
	if m := jsonOf[proto.Message](t, e.must(t, 200, "GET", "/v1/mailbox/messages/"+strconv.FormatInt(boxMsg.ID, 10)+"?mailbox=box", "admin-token", "")); m.ID != boxMsg.ID || m.State != "queued" {
		t.Errorf("peek message = %+v", m)
	}
	e.must(t, 404, "GET", "/v1/mailbox/messages/99999", "read-token", "")
	e.must(t, 400, "GET", "/v1/mailbox/messages/abc", "read-token", "")
	e.must(t, 404, "POST", "/v1/mailbox/messages/99999/read", "write-token", "")

	// Reply (to self: the reply lands in the sender's, i.e. our own, mailbox).
	rec = e.must(t, 201, "POST", "/v1/mailbox/messages/"+id+"/reply", "write-token", `{"body":"re: note"}`, "Idempotency-Key", "r-1")
	if m := jsonOf[proto.Message](t, rec); m.ReplyTo == nil || *m.ReplyTo != ids[0] || m.Body != "re: note" {
		t.Errorf("reply = %+v", m)
	}
	if r2 := e.must(t, 201, "POST", "/v1/mailbox/messages/"+id+"/reply", "write-token", `{"body":"re: note"}`, "Idempotency-Key", "r-1"); r2.Header().Get("Idempotent-Replayed") != "true" {
		t.Error("reply not replayed")
	}
	e.must(t, 400, "POST", "/v1/mailbox/messages/"+id+"/reply", "write-token", `{"body":""}`)
	e.must(t, 404, "POST", "/v1/mailbox/messages/99999/reply", "write-token", `{"body":"x"}`)
}

func TestRESTTerminalSnapshot(t *testing.T) {
	e := startREST(t)
	e.runFake(t, "t1", "idle", "-ghost", "Try something")
	c := e.dial(t)

	rec := e.must(t, 200, "GET", "/v1/sessions/t1/terminal", "read-token", "")
	snap := jsonOf[struct {
		Format      string
		Text        string
		Ghost       string
		GhostHidden bool `json:"ghost_hidden"`
		Cols, Rows  int
	}](t, rec)
	want, err := c.DumpResult(proto.DumpArgs{Name: "t1"})
	if err != nil {
		t.Fatal(err)
	}
	if snap.Format != "text" || snap.Text != want.Text || snap.Ghost != want.Ghost || snap.GhostHidden != want.GhostHidden {
		t.Errorf("snapshot %+v != dump %+v", snap, want)
	}
	infos, _ := c.Ls()
	if snap.Cols != int(infos[0].Cols) || snap.Rows != int(infos[0].Rows) || snap.Cols == 0 {
		t.Errorf("size = %dx%d, session %dx%d", snap.Cols, snap.Rows, infos[0].Cols, infos[0].Rows)
	}
	if !strings.Contains(snap.Text, "fakeagent ready") {
		t.Errorf("text = %q", snap.Text)
	}
	// Never resizes.
	e.must(t, 200, "GET", "/v1/sessions/t1/terminal?scrollback=true", "read-token", "")
	after, _ := c.Ls()
	if after[0].Cols != infos[0].Cols || after[0].Rows != infos[0].Rows {
		t.Errorf("terminal changed the size: %v -> %v", infos[0], after[0])
	}

	// ghost=true = raw; scrollback; ansi; text/plain.
	raw := jsonOf[map[string]any](t, e.must(t, 200, "GET", "/v1/sessions/t1/terminal?ghost=true", "read-token", ""))
	wantRaw, _ := c.Dump(proto.DumpArgs{Name: "t1", Ghost: true})
	if raw["text"] != wantRaw {
		t.Errorf("ghost=true text mismatch")
	}
	sb := jsonOf[map[string]any](t, e.must(t, 200, "GET", "/v1/sessions/t1/terminal?scrollback=true", "read-token", ""))
	wantSB, _ := c.Dump(proto.DumpArgs{Name: "t1", Scrollback: true})
	if sb["text"] != wantSB {
		t.Errorf("scrollback mismatch")
	}
	an := jsonOf[map[string]any](t, e.must(t, 200, "GET", "/v1/sessions/t1/terminal?format=ansi", "read-token", ""))
	wantAnsi, _ := c.Dump(proto.DumpArgs{Name: "t1", ANSI: true})
	if an["format"] != "ansi" || an["text"] != wantAnsi || !strings.Contains(wantAnsi, "\x1b[") {
		t.Errorf("ansi mismatch")
	}
	rec = e.must(t, 200, "GET", "/v1/sessions/t1/terminal", "read-token", "", "Accept", "text/plain")
	if !strings.HasPrefix(rec.Header().Get("Content-Type"), "text/plain") || rec.Body.String() != want.Text {
		t.Errorf("text/plain = %q %q", rec.Header().Get("Content-Type"), rec.Body)
	}
	for _, q := range []string{"format=pdf", "format=ansi&scrollback=true", "scrollback=zz", "ghost=zz"} {
		e.must(t, 400, "GET", "/v1/sessions/t1/terminal?"+q, "read-token", "")
	}
	e.must(t, 404, "GET", "/v1/sessions/nope/terminal", "read-token", "")
}

func TestRESTPercentEncodedNames(t *testing.T) {
	e := startREST(t)
	e.runSleeper(t, "codex@web.app-1")
	for _, p := range []string{"", "/view", "/terminal"} {
		e.must(t, 200, "GET", "/v1/sessions/codex%40web.app-1"+p, "read-token", "")
	}
	e.must(t, 200, "GET", "/v1/sessions/codex@web.app-1", "read-token", "")
	e.must(t, 400, "GET", "/v1/sessions/bad%20name", "read-token", "")
}

// TestRoutesMatchOpenAPI scans docs/api/openapi.yaml for paths, methods and
// x-agentd-scope (stdlib only) and compares them with the route table.
func TestRoutesMatchOpenAPI(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", "docs", "api", "openapi.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	pathRE := regexp.MustCompile(`^  (/\S+):\s*$`)
	methodRE := regexp.MustCompile(`^    (get|post|put|delete|patch):\s*$`)
	scopeRE := regexp.MustCompile(`^      x-agentd-scope:\s*(\w+)`)
	spec := map[string]string{}
	var path, method string
	inPaths := false
	for _, line := range strings.Split(string(data), "\n") {
		switch {
		case line == "paths:":
			inPaths = true
		case inPaths && line != "" && line[0] != ' ' && line[0] != '#':
			inPaths = false
		}
		if !inPaths {
			continue
		}
		if m := pathRE.FindStringSubmatch(line); m != nil {
			path, method = m[1], ""
		} else if m := methodRE.FindStringSubmatch(line); m != nil {
			method = strings.ToUpper(m[1])
		} else if m := scopeRE.FindStringSubmatch(line); m != nil && method != "" {
			spec[method+" "+path] = m[1]
		}
	}
	s := newServer(t, startDaemon(t), nil, nil)
	got := map[string]string{}
	for _, rt := range s.Routes() {
		if rt.Pattern == "/v1/health" {
			continue // documented separately as unauthenticated
		}
		got[rt.Method+" "+rt.Pattern] = rt.Scope.String()
	}
	for k, sc := range got {
		if spec[k] != sc {
			t.Errorf("route %s (scope %s): openapi scope %q", k, sc, spec[k])
		}
	}
	// Routes owned by other stories (SSE) may be absent from the table in this
	// build; every documented path implemented elsewhere is checked there.
	streaming := map[string]bool{"GET /v1/sessions/{name}/view/stream": true, "GET /v1/events": true}
	for k := range spec {
		if _, ok := got[k]; !ok && !streaming[k] && k != "GET /v1/health" {
			t.Errorf("openapi operation %s has no route", k)
		}
	}
}
