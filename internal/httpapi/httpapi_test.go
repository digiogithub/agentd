package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/digiogithub/agentd/internal/client"
	"github.com/digiogithub/agentd/internal/config"
	"github.com/digiogithub/agentd/internal/daemon"
	"github.com/digiogithub/agentd/internal/mailbox"
	"github.com/digiogithub/agentd/internal/proto"
)

func TestMain(m *testing.M) {
	for _, k := range []string{"AGENTD_TOKEN", "AGENTD_ID", "AGENTD_SOCKET"} {
		os.Unsetenv(k)
	}
	os.Exit(m.Run())
}

const testToken = "test-token-secret"

// testAuth is the test-only token checker: "Bearer test-token-secret" gets
// write scope; "Bearer read-token" gets read.
func testAuth() Authenticator {
	return AuthenticatorFunc(func(r *http.Request, need Scope) (*Principal, error) {
		switch r.Header.Get("Authorization") {
		case "Bearer " + testToken:
			return &Principal{Device: "test-dev", Scope: ScopeWrite}, nil
		case "Bearer read-token":
			return &Principal{Device: "ro-dev", Scope: ScopeRead}, nil
		}
		return nil, ErrUnauthenticated
	})
}

type daemonEnv struct {
	sock string
	srv  *daemon.Server
}

func startDaemon(t *testing.T) *daemonEnv {
	t.Helper()
	dir, err := os.MkdirTemp("", "agentd-h")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	sock := filepath.Join(dir, "agentd.sock")
	t.Setenv(config.EnvSocket, sock)
	t.Setenv(config.EnvStateDir, filepath.Join(dir, "state"))
	t.Setenv(config.EnvConfig, filepath.Join(dir, "config.toml"))
	if err := config.EnsureDirs(); err != nil {
		t.Fatal(err)
	}
	dbPath, _ := config.MailboxPath()
	store, err := mailbox.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	srv := daemon.New(config.Default(), nil, store, nil)
	if err := srv.Listen(); err != nil {
		t.Fatal(err)
	}
	go func() { _ = srv.Serve(context.Background()) }()
	t.Cleanup(srv.Shutdown)
	return &daemonEnv{sock: sock, srv: srv}
}

func newServer(t *testing.T, d *daemonEnv, auth Authenticator, logw io.Writer) *Server {
	t.Helper()
	if logw == nil {
		logw = io.Discard
	}
	return &Server{
		Config: Config{Version: "9.9.9-test", Hostname: "example.com", MaxBodyKB: 1, RateLimit: 100000, RateBurst: 100000, ActRateLimit: 100000, MaxStreams: 1000, MaxStreamsPerDevice: 1000, RunHarnesses: []string{"claude", "codex"}},
		Dial:   func() (*client.Client, error) { return client.Dial(d.sock) },
		Auth:   auth,
		Log:    slog.New(slog.NewTextHandler(logw, nil)),
	}
}

func do(t *testing.T, h http.Handler, method, path, token string, body io.Reader) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, body)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func decodeBody(t *testing.T, rec *httptest.ResponseRecorder, v any) {
	t.Helper()
	if err := json.Unmarshal(rec.Body.Bytes(), v); err != nil {
		t.Fatalf("body %q: %v", rec.Body.String(), err)
	}
}

func errCode(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	var e envelope
	decodeBody(t, rec, &e)
	return e.Error.Code
}

func TestHealthNoAuth(t *testing.T) {
	d := startDaemon(t)
	s := newServer(t, d, nil, nil) // nil Auth = DenyAll stub
	rec := do(t, s.Handler(), "GET", "/v1/health", "", nil)
	if rec.Code != 200 {
		t.Fatalf("health = %d %s", rec.Code, rec.Body)
	}
	var got struct {
		OK      bool   `json:"ok"`
		Version string `json:"version"`
	}
	decodeBody(t, rec, &got)
	if !got.OK || got.Version != "9.9.9-test" {
		t.Fatalf("health body = %+v", got)
	}
	if rec.Header().Get("X-Request-Id") == "" {
		t.Fatal("no X-Request-Id")
	}
}

func TestHealthDaemonDown(t *testing.T) {
	s := &Server{Dial: func() (*client.Client, error) { return nil, errors.New("down") }, Config: Config{Hostname: "example.com"}}
	rec := do(t, s.Handler(), "GET", "/v1/health", "", nil)
	if rec.Code != 503 || errCode(t, rec) != "daemon_unavailable" || rec.Header().Get("Retry-After") != "1" {
		t.Fatalf("down = %d %s %v", rec.Code, rec.Body, rec.Header())
	}
}

func TestInfoRequiresAuth(t *testing.T) {
	d := startDaemon(t)
	// Default stub rejects everything but health, even with a token.
	s := newServer(t, d, nil, nil)
	if rec := do(t, s.Handler(), "GET", "/v1/info", testToken, nil); rec.Code != 401 || errCode(t, rec) != "unauthenticated" {
		t.Fatalf("stub = %d %s", rec.Code, rec.Body)
	}
	s = newServer(t, d, testAuth(), nil)
	h := s.Handler()
	rec := do(t, h, "GET", "/v1/info", "", nil)
	if rec.Code != 401 || rec.Header().Get("WWW-Authenticate") != "Bearer" {
		t.Fatalf("no token = %d %v", rec.Code, rec.Header())
	}
	if rec := do(t, h, "GET", "/v1/info", "bogus", nil); rec.Code != 401 {
		t.Fatalf("bad token = %d", rec.Code)
	}
	rec = do(t, h, "GET", "/v1/info", "read-token", nil)
	if rec.Code != 200 {
		t.Fatalf("info = %d %s", rec.Code, rec.Body)
	}
	var info struct {
		APIVersion string            `json:"api_version"`
		Features   []string          `json:"features"`
		Host       string            `json:"host"`
		Gateway    map[string]string `json:"gateway"`
		Daemon     struct {
			Version string `json:"version"`
			Pid     int    `json:"pid"`
			Epoch   string `json:"epoch"`
		} `json:"daemon"`
		Identity  string           `json:"identity"`
		Scopes    []string         `json:"scopes"`
		Harnesses []string         `json:"harnesses"`
		Profiles  []map[string]any `json:"profiles"`
		Aliases   []any            `json:"aliases"`
		Limits    map[string]any   `json:"limits"`
	}
	decodeBody(t, rec, &info)
	if info.APIVersion != "1" || !slices.Contains(info.Features, "info") ||
		info.Gateway["version"] != "9.9.9-test" || info.Daemon.Pid == 0 ||
		!strings.HasPrefix(info.Identity, "human:") || len(info.Scopes) != 1 || info.Scopes[0] != "read" ||
		len(info.Harnesses) != 2 || len(info.Profiles) == 0 || info.Aliases == nil || info.Limits["max_body_kb"] != float64(1) {
		t.Fatalf("info = %+v", info)
	}
	rec = do(t, h, "GET", "/v1/info", testToken, nil)
	if !strings.Contains(rec.Body.String(), `"write"`) {
		t.Fatalf("write token scopes: %s", rec.Body)
	}
}

func TestScopeTooSmall(t *testing.T) {
	d := startDaemon(t)
	s := newServer(t, d, AuthenticatorFunc(func(*http.Request, Scope) (*Principal, error) {
		return &Principal{Device: "x", Scope: ScopeNone}, nil
	}), nil)
	if rec := do(t, s.Handler(), "GET", "/v1/info", "x", nil); rec.Code != 403 || errCode(t, rec) != "forbidden" {
		t.Fatalf("= %d %s", rec.Code, rec.Body)
	}
}

func TestNotFoundAndMethodNotAllowed(t *testing.T) {
	s := newServer(t, startDaemon(t), testAuth(), nil)
	h := s.Handler()
	if rec := do(t, h, "GET", "/v1/nope", "", nil); rec.Code != 404 || errCode(t, rec) != "not_found" {
		t.Fatalf("404 = %d %s", rec.Code, rec.Body)
	}
	rec := do(t, h, "POST", "/v1/health", "", nil)
	if rec.Code != 405 || errCode(t, rec) != "method_not_allowed" || rec.Header().Get("Allow") != "GET" {
		t.Fatalf("405 = %d %s %v", rec.Code, rec.Body, rec.Header())
	}
}

func TestErrorEnvelopeMapping(t *testing.T) {
	remote := func(code, msg string) error { return &proto.RemoteError{Code: code, Msg: msg} }
	for _, tc := range []struct {
		name       string
		err        error
		status     int
		code       string
		retryable  bool
		retryAfter int
	}{
		{"invalid", remote("invalid", "bad"), 400, "invalid_argument", false, 0},
		{"not_found", remote("not_found", "x"), 404, "not_found", false, 0},
		{"starting", remote("starting", "x"), 409, "starting", true, 1},
		{"exited", remote("exited", "x"), 409, "exited", false, 0},
		{"exists", remote("exists", "x"), 409, "already_exists", false, 0},
		{"forbidden", remote("forbidden", "x"), 403, "forbidden", false, 0},
		{"conflict", remote("conflict", "x"), 409, "conflict", false, 0},
		{"busy", remote("busy", "x"), 409, "busy", true, 0},
		{"not_ready", remote("not_ready", "x"), 409, "not_ready", true, 0},
		{"verify_failed", remote("verify_failed", "x"), 503, "paste_unverified", true, 1},
		{"timeout", remote("timeout", "x"), 504, "timeout", true, 0},
		{"internal", remote("internal", "x"), 500, "internal", false, 0},
		{"unknown code", remote("weird", "x"), 500, "internal", false, 0},
		{"legacy not found", remote("", `no such session "a"`), 404, "not_found", false, 0},
		{"legacy exists", remote("", `session "a" already exists`), 409, "already_exists", false, 0},
		{"legacy starting", remote("", `session "a" is starting`), 409, "starting", true, 1},
		{"uncoded", remote("", "boom"), 500, "internal", false, 0},
		{"io error", errors.New("read: EOF"), 503, "daemon_unavailable", true, 1},
		{"deadline", context.DeadlineExceeded, 504, "timeout", true, 0},
		{"api error passthrough", errInvalid("x"), 400, "invalid_argument", false, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			WriteError(rec, FromDaemonError(tc.err))
			var e envelope
			decodeBody(t, rec, &e)
			if rec.Code != tc.status || e.Error.Code != tc.code || e.Error.Retryable != tc.retryable ||
				rec.Header().Get("Retry-After") != map[bool]string{true: "1", false: ""}[tc.retryAfter == 1] {
				t.Fatalf("got %d %+v ra=%q", rec.Code, e.Error, rec.Header().Get("Retry-After"))
			}
			if e.Error.Message == "" || strings.Contains(e.Error.Message, "boom") {
				t.Fatal("empty message")
			}
		})
	}
	rec := httptest.NewRecorder()
	WriteError(rec, errors.New("secret internals"))
	if rec.Code != 500 || strings.Contains(rec.Body.String(), "secret") {
		t.Fatalf("plain error leaked: %d %s", rec.Code, rec.Body)
	}
}

func TestBodyCapAndDecode(t *testing.T) {
	s := newServer(t, startDaemon(t), testAuth(), nil) // 1 KiB cap
	s.extra = []Route{{Method: "POST", Pattern: "/v1/echo", Scope: ScopeWrite, Handler: func(w http.ResponseWriter, r *http.Request) error {
		var v struct {
			A string `json:"a"`
		}
		if err := DecodeJSON(r, &v); err != nil {
			return err
		}
		WriteJSON(w, 200, v)
		return nil
	}}}
	h := s.Handler()
	if rec := do(t, h, "POST", "/v1/echo", testToken, strings.NewReader(`{"a":"x"}`)); rec.Code != 200 {
		t.Fatalf("ok = %d %s", rec.Code, rec.Body)
	}
	big := `{"a":"` + strings.Repeat("x", 2048) + `"}`
	if rec := do(t, h, "POST", "/v1/echo", testToken, strings.NewReader(big)); rec.Code != 413 || errCode(t, rec) != "payload_too_large" {
		t.Fatalf("declared length = %d %s", rec.Code, rec.Body)
	}
	// Undeclared length (chunked): enforced while reading.
	req := httptest.NewRequest("POST", "/v1/echo", io.NopCloser(strings.NewReader(big)))
	req.ContentLength = -1
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+testToken)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != 413 {
		t.Fatalf("chunked = %d %s", rec.Code, rec.Body)
	}
	for body, want := range map[string]string{`{"b":1}`: "invalid_argument", `{"a":"x"} {}`: "invalid_argument", ``: "invalid_argument", `nope`: "invalid_argument"} {
		if rec := do(t, h, "POST", "/v1/echo", testToken, strings.NewReader(body)); rec.Code != 400 || errCode(t, rec) != want {
			t.Fatalf("body %q = %d %s", body, rec.Code, rec.Body)
		}
	}
	// Unauthenticated callers do not get to the handler.
	if rec := do(t, h, "POST", "/v1/echo", "", strings.NewReader(`{}`)); rec.Code != 401 {
		t.Fatalf("unauth = %d", rec.Code)
	}
}

func TestRecoverAndAccessLogHasNoSecrets(t *testing.T) {
	var buf syncBuf
	s := newServer(t, startDaemon(t), testAuth(), &buf)
	s.extra = []Route{{Method: "GET", Pattern: "/v1/boom/{name}", Scope: ScopeRead, Handler: func(http.ResponseWriter, *http.Request) error {
		panic("kaboom")
	}}}
	h := s.Handler()
	rec := do(t, h, "GET", "/v1/boom/a@b?token=querysecret", testToken, nil)
	if rec.Code != 500 || errCode(t, rec) != "internal" || strings.Contains(rec.Body.String(), "kaboom") {
		t.Fatalf("panic = %d %s", rec.Code, rec.Body)
	}
	do(t, h, "GET", "/v1/info", testToken, nil)
	log := buf.String()
	for _, secret := range []string{testToken, "querysecret", "a@b", "Authorization"} {
		if strings.Contains(log, secret) {
			t.Fatalf("log leaks %q:\n%s", secret, log)
		}
	}
	for _, want := range []string{"/v1/boom/{name}", "route=\"GET /v1/info\"", "device=test-dev", "status=500"} {
		if !strings.Contains(log, want) && !strings.Contains(log, strings.ReplaceAll(want, `"`, "")) {
			t.Fatalf("log lacks %q:\n%s", want, log)
		}
	}
}

type syncBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuf) Write(p []byte) (int, error) { s.mu.Lock(); defer s.mu.Unlock(); return s.b.Write(p) }
func (s *syncBuf) String() string              { s.mu.Lock(); defer s.mu.Unlock(); return s.b.String() }

func TestAllowedHostsAndCORS(t *testing.T) {
	s := newServer(t, startDaemon(t), testAuth(), nil)
	s.Config.AllowedHosts = []string{"gw.example"}
	s.Config.CORSOrigins = []string{"https://app.example"}
	h := s.Handler()
	req := httptest.NewRequest("GET", "http://evil.example/v1/health", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != 421 {
		t.Fatalf("bad host = %d", rec.Code)
	}
	req = httptest.NewRequest("GET", "http://gw.example:7477/v1/health", nil)
	req.Header.Set("Origin", "https://app.example")
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != 200 || rec.Header().Get("Access-Control-Allow-Origin") != "https://app.example" {
		t.Fatalf("cors = %d %v", rec.Code, rec.Header())
	}
	req = httptest.NewRequest("OPTIONS", "http://gw.example/v1/info", nil)
	req.Header.Set("Origin", "https://app.example")
	req.Header.Set("Access-Control-Request-Method", "GET")
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != 204 || !strings.Contains(rec.Header().Get("Access-Control-Allow-Headers"), "Authorization") {
		t.Fatalf("preflight = %d %v", rec.Code, rec.Header())
	}
	req = httptest.NewRequest("GET", "http://gw.example/v1/health", nil)
	req.Header.Set("Origin", "https://other.example")
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != 403 || rec.Header().Get("Access-Control-Allow-Origin") != "" {
		t.Fatalf("foreign origin = %d %v", rec.Code, rec.Header())
	}
}

func TestRoutesTable(t *testing.T) {
	s := newServer(t, startDaemon(t), nil, nil)
	got := map[string]Scope{}
	for _, rt := range s.Routes() {
		got[rt.Method+" "+rt.Pattern] = rt.Scope
		if rt.Handler == nil {
			t.Fatalf("route %s has no handler", rt.Pattern)
		}
	}
	if got["GET /v1/health"] != ScopeNone || got["GET /v1/info"] != ScopeRead {
		t.Fatalf("routes = %v", got)
	}
}

func TestPoolReuseAndStaleRetry(t *testing.T) {
	d := startDaemon(t)
	var mu sync.Mutex
	dials := 0
	s := newServer(t, d, testAuth(), nil)
	s.Dial = func() (*client.Client, error) {
		mu.Lock()
		dials++
		mu.Unlock()
		return client.Dial(d.sock)
	}
	h := s.Handler()
	for i := 0; i < 3; i++ {
		if rec := do(t, h, "GET", "/v1/info", testToken, nil); rec.Code != 200 {
			t.Fatalf("info %d %s", rec.Code, rec.Body)
		}
	}
	if dials != 1 {
		t.Fatalf("dials = %d, want 1 (pooled)", dials)
	}
	// Kill the idle connection: the next call retries once on a fresh dial.
	s.pool.mu.Lock()
	for _, c := range s.pool.idle {
		_ = c.Close()
	}
	s.pool.mu.Unlock()
	if rec := do(t, h, "GET", "/v1/info", testToken, nil); rec.Code != 200 {
		t.Fatalf("after stale = %d %s", rec.Code, rec.Body)
	}
	if dials != 2 {
		t.Fatalf("dials = %d, want 2", dials)
	}
	// More concurrent calls than max idle never keep more than 4.
	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); do(t, h, "GET", "/v1/info", testToken, nil) }()
	}
	wg.Wait()
	s.pool.mu.Lock()
	n := len(s.pool.idle)
	s.pool.mu.Unlock()
	if n > maxIdleConns {
		t.Fatalf("idle = %d", n)
	}
}

func TestCallTimeoutAbandonsConnection(t *testing.T) {
	// A fake daemon that accepts and never answers.
	sock := filepath.Join(t.TempDir(), "s.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			defer c.Close()
		}
	}()
	s := &Server{Dial: func() (*client.Client, error) { return client.Dial(sock) }}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	err = s.WithDaemon(ctx, func(c *client.Client) error { _, err := c.Ping(); return err })
	if e := FromDaemonError(err); e.Status != 504 {
		t.Fatalf("err = %v -> %+v", err, e)
	}
}

func TestGracefulShutdown(t *testing.T) {
	d := startDaemon(t)
	s := newServer(t, d, testAuth(), nil)
	s.extra = []Route{{Method: "GET", Pattern: "/v1/stream", Scope: ScopeRead, Handler: func(w http.ResponseWriter, r *http.Request) error {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(200)
		w.(http.Flusher).Flush()
		select {
		case <-s.Draining():
			io.WriteString(w, "event: bye\ndata: {}\n\n")
		case <-r.Context().Done():
		}
		return nil
	}}}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- s.Serve(ctx, ln) }()
	base := "http://" + ln.Addr().String()
	resp, err := http.Get(base + "/v1/health")
	if err != nil || resp.StatusCode != 200 {
		t.Fatalf("health: %v %v", resp, err)
	}
	resp.Body.Close()
	req, _ := http.NewRequest("GET", base+"/v1/stream", nil)
	req.Header.Set("Authorization", "Bearer "+testToken)
	sresp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	cancel()
	b, _ := io.ReadAll(sresp.Body)
	sresp.Body.Close()
	if !strings.Contains(string(b), "event: bye") {
		t.Fatalf("stream body = %q", b)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Serve = %v", err)
		}
	case <-time.After(8 * time.Second):
		t.Fatal("Serve did not return")
	}
	if _, err := http.Get(base + "/v1/health"); err == nil {
		t.Fatal("still serving after shutdown")
	}
	// The daemon is untouched.
	c, err := client.Dial(d.sock)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if _, err := c.Ping(); err != nil {
		t.Fatalf("daemon ping after gateway stop: %v", err)
	}
}

func TestDependencies(t *testing.T) {
	out, err := exec.Command("go", "list", "-deps", "github.com/digiogithub/agentd/internal/httpapi").Output()
	if err != nil {
		t.Skipf("go list: %v", err)
	}
	allowed := map[string]bool{"client": true, "proto": true}
	for _, p := range strings.Fields(string(out)) {
		_, rest, ok := strings.Cut(p, "github.com/digiogithub/agentd/internal/")
		if !ok {
			continue
		}
		pkg, _, _ := strings.Cut(rest, "/")
		if pkg == "httpapi" || allowed[pkg] {
			continue
		}
		t.Errorf("internal/httpapi depends on internal/%s (only client and proto are allowed)", pkg)
	}
}
