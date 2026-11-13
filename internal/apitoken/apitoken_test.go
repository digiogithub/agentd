package apitoken

import (
	"bytes"
	"context"
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/digiogithub/agentd/internal/httpapi"
)

type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *fakeClock) Now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *fakeClock) Add(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

func newStore(t *testing.T, o Options) (*Store, *fakeClock, string) {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "api")
	clk := &fakeClock{t: time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)}
	if o.Clock == nil {
		o.Clock = clk.Now
	}
	s, err := Open(dir, o)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.FlushTouches) // background last_seen writes must end before TempDir is removed
	return s, clk, dir
}

func mustCreate(t *testing.T, s *Store, name, scope string, ttl time.Duration) (Device, string) {
	t.Helper()
	d, tok, err := s.Create(CreateArgs{Name: name, Scope: scope, TTL: ttl})
	if err != nil {
		t.Fatal(err)
	}
	return d, tok
}

func TestTokenFormat(t *testing.T) {
	s, _, _ := newStore(t, Options{})
	d, tok := mustCreate(t, s, "ci", ScopeRead, 0)
	if !strings.HasPrefix(tok, "agd_"+d.ID+"_") || len(tok) != 58 {
		t.Fatalf("token shape: %q", tok)
	}
	id, secret, err := ParseToken(tok)
	if err != nil || id != d.ID || len(secret) != 43 {
		t.Fatalf("parse: %v %q", err, id)
	}
	if d.Hash != hashSecret(secret) || strings.Contains(d.Hash, secret) {
		t.Fatal("hash mismatch")
	}
	for _, bad := range []string{"", "agd_", tok[:57], tok + "x", "xxx_" + tok[4:], strings.Replace(tok, "_", "-", 1), "agd_" + strings.ToLower(d.ID) + tok[14:] + "", tok[:20] + "!" + tok[21:]} {
		if _, _, err := ParseToken(bad); err == nil && bad != strings.ToLower(bad) {
			t.Errorf("accepted %q", bad)
		}
	}
	if _, _, err := ParseToken(tok[:14] + "-" + tok[15:]); err == nil {
		t.Error("accepted bad separator")
	}
	// The clear token never lands in the file.
	b, _ := os.ReadFile(s.Path())
	if bytes.Contains(b, []byte(secret)) {
		t.Fatal("secret stored in clear")
	}
}

func TestFilePermsAndFormat(t *testing.T) {
	s, _, dir := newStore(t, Options{})
	mustCreate(t, s, "a", ScopeWrite, time.Hour)
	if fi, _ := os.Stat(dir); fi.Mode().Perm() != 0o700 {
		t.Fatalf("dir mode %v", fi.Mode().Perm())
	}
	if fi, _ := os.Stat(s.Path()); fi.Mode().Perm() != 0o600 {
		t.Fatalf("file mode %v", fi.Mode().Perm())
	}
	var fd fileData
	b, _ := os.ReadFile(s.Path())
	if err := json.Unmarshal(b, &fd); err != nil || fd.Version != 1 || len(fd.Devices) != 1 || fd.Devices[0].PairedVia != "cli" || fd.Devices[0].ExpiresAt.IsZero() {
		t.Fatalf("file: %s (%v)", b, err)
	}
	// Group-readable file: refused on load, deny all.
	_, tok := mustCreate(t, s, "b", ScopeRead, 0)
	if _, err := s.Verify(tok, "1.2.3.4:5"); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(s.Path(), 0o640); err != nil {
		t.Fatal(err)
	}
	s2, err := Open(dir, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if s2.Err() == nil || !strings.Contains(s2.Err().Error(), "0640") {
		t.Fatalf("err = %v", s2.Err())
	}
	if _, err := s2.Verify(tok, ""); err == nil {
		t.Fatal("verified from an unsafe file")
	}
	if _, err := s2.List(); err == nil {
		t.Fatal("listed an unsafe file")
	}
	if _, _, err := s2.Create(CreateArgs{Name: "x", Scope: ScopeRead}); err == nil {
		t.Fatal("wrote over an unsafe file")
	}
	// World-open directory: Open refuses.
	if err := os.Chmod(s.Path(), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(dir, Options{}); err == nil {
		t.Fatal("opened a world-readable dir")
	}
}

func TestMalformedFileDeniesAll(t *testing.T) {
	var logs bytes.Buffer
	s, _, dir := newStore(t, Options{Log: slog.New(slog.NewTextHandler(&logs, nil))})
	_, tok := mustCreate(t, s, "a", ScopeAdmin, 0)
	if _, err := s.Verify(tok, ""); err != nil {
		t.Fatal(err)
	}
	s.FlushTouches() // the queued last_seen write must land before the file is corrupted
	if err := os.WriteFile(s.Path(), []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	s.refresh(true)
	if s.Err() == nil {
		t.Fatal("no error state")
	}
	if _, err := s.Verify(tok, ""); err == nil {
		t.Fatal("verified with a malformed file")
	}
	if !strings.Contains(logs.String(), "unusable") {
		t.Fatalf("no error log: %s", logs.String())
	}
	if strings.Contains(logs.String(), tok) {
		t.Fatal("token logged")
	}
	for _, bad := range []string{
		`{"version":2,"devices":[]}`,
		`{"version":1,"devices":[{"id":"short","name":"x","scope":"read","hash":"` + strings.Repeat("a", 64) + `"}]}`,
		`{"version":1,"devices":[{"id":"ABCDEFGHJK","name":"x","scope":"root","hash":"` + strings.Repeat("a", 64) + `"}]}`,
		`{"version":1,"devices":[{"id":"ABCDEFGHJK","name":"x","scope":"read","hash":"zz"}]}`,
	} {
		if err := os.WriteFile(s.Path(), []byte(bad), 0o600); err != nil {
			t.Fatal(err)
		}
		if s3, _ := Open(dir, Options{}); s3.Err() == nil {
			t.Errorf("accepted %s", bad)
		}
	}
	// Fixing the file recovers (a fresh read sees valid content).
	if err := os.WriteFile(s.Path(), []byte(`{"version":1,"devices":[]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	s.refresh(true)
	if s.Err() != nil {
		t.Fatalf("still broken: %v", s.Err())
	}
}

func TestVerifyNegative(t *testing.T) {
	s, _, _ := newStore(t, Options{})
	d, tok := mustCreate(t, s, "a", ScopeRead, 0)
	if got, err := s.Verify(tok, "10.0.0.1:99"); err != nil || got.ID != d.ID || got.Scope != ScopeRead {
		t.Fatalf("verify: %v %v", got, err)
	}
	wrong := tamper(tok)
	other := "agd_" + "ABCDEFGHJK" + tok[14:]
	for name, bad := range map[string]string{"wrong secret": wrong, "unknown id": other, "garbage": "hello", "empty": ""} {
		if _, err := s.Verify(bad, ""); err != ErrInvalid {
			t.Errorf("%s: err = %v", name, err)
		}
	}
}

func TestConstantTimeCompareUsed(t *testing.T) {
	s, _, _ := newStore(t, Options{})
	_, tok := mustCreate(t, s, "a", ScopeRead, 0)
	var n atomic.Int32
	old := ctCompare
	ctCompare = func(a, b []byte) int { n.Add(1); return subtle.ConstantTimeCompare(a, b) }
	defer func() { ctCompare = old }()
	_, _ = s.Verify(tok, "")
	_, _ = s.Verify("agd_ABCDEFGHJK"+tok[14:], "") // unknown id still compares
	if n.Load() != 2 {
		t.Fatalf("ctCompare calls = %d, want 2", n.Load())
	}
}

func TestExpiry(t *testing.T) {
	s, clk, _ := newStore(t, Options{})
	_, ttlTok := mustCreate(t, s, "ttl", ScopeRead, time.Hour)
	_, idleTok := mustCreate(t, s, "idle", ScopeRead, 0)
	clk.Add(59 * time.Minute)
	if _, err := s.Verify(ttlTok, ""); err != nil {
		t.Fatal(err)
	}
	clk.Add(2 * time.Minute)
	if _, err := s.Verify(ttlTok, ""); err != ErrInvalid {
		t.Fatalf("expired ttl token: %v", err)
	}
	// Idle: used regularly it survives past 90 days, unused it dies.
	for i := 0; i < 5; i++ {
		clk.Add(30 * 24 * time.Hour)
		if _, err := s.Verify(idleTok, ""); err != nil {
			t.Fatalf("active device rejected at step %d: %v", i, err)
		}
	}
	clk.Add(91 * 24 * time.Hour)
	if _, err := s.Verify(idleTok, ""); err != ErrInvalid {
		t.Fatalf("idle-expired: %v", err)
	}
	devs, _ := s.List()
	for _, d := range devs {
		if s.State(d) != "expired" {
			t.Errorf("%s state %s", d.Name, s.State(d))
		}
	}
	// Idle expiry can be disabled.
	s2, clk2, _ := newStore(t, Options{IdleExpiry: -1})
	_, tk := mustCreate(t, s2, "forever", ScopeRead, 0)
	clk2.Add(1000 * 24 * time.Hour)
	if _, err := s2.Verify(tk, ""); err != nil {
		t.Fatal(err)
	}
}

func TestLastSeenThrottled(t *testing.T) {
	s, clk, _ := newStore(t, Options{})
	d, tok := mustCreate(t, s, "a", ScopeRead, 0)
	stat := func() os.FileInfo { s.FlushTouches(); fi, _ := os.Stat(s.Path()); return fi }
	read := func() Device { s.FlushTouches(); l, _ := s.List(); return l[0] }
	if read().LastSeenAt.IsZero() == false {
		t.Fatal("last seen set at creation")
	}
	if _, err := s.Verify(tok, "192.0.2.7:4000"); err != nil {
		t.Fatal(err)
	}
	first := read()
	if first.LastSeenAt.IsZero() || first.LastSeenAddr != "192.0.2.7" || first.ID != d.ID {
		t.Fatalf("first touch: %+v", first)
	}
	m1 := stat().ModTime()
	for i := 0; i < 50; i++ { // 50 requests within the minute: no write
		clk.Add(time.Second)
		if _, err := s.Verify(tok, "192.0.2.8:4000"); err != nil {
			t.Fatal(err)
		}
	}
	if !stat().ModTime().Equal(m1) || !read().LastSeenAt.Equal(first.LastSeenAt) {
		t.Fatal("last_seen written more than once a minute")
	}
	clk.Add(time.Minute)
	if _, err := s.Verify(tok, "192.0.2.9:4000"); err != nil {
		t.Fatal(err)
	}
	if got := read(); !got.LastSeenAt.After(first.LastSeenAt) || got.LastSeenAddr != "192.0.2.9" {
		t.Fatalf("second touch: %+v", got)
	}
}

func TestRevokeAndReloadFromOtherProcess(t *testing.T) {
	s, clk, dir := newStore(t, Options{})
	d, tok := mustCreate(t, s, "phone", ScopeWrite, 0)
	var revoked []string
	var mu sync.Mutex
	s.Subscribe(func(ids []string) { mu.Lock(); revoked = append(revoked, ids...); mu.Unlock() })
	if _, err := s.Verify(tok, ""); err != nil {
		t.Fatal(err)
	}
	// A second Store on the same dir plays the CLI process.
	cli, err := Open(dir, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := cli.Revoke("nobody"); err == nil {
		t.Fatal("revoked a missing device")
	}
	if _, err := cli.Revoke("phone"); err != nil { // by name
		t.Fatal(err)
	}
	clk.Add(1100 * time.Millisecond) // past the reload throttle
	if _, err := s.Verify(tok, ""); err != ErrInvalid {
		t.Fatalf("revoked token still valid: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(revoked) != 1 || revoked[0] != d.ID {
		t.Fatalf("hook ids = %v", revoked)
	}
}

func TestRevokeAmbiguousName(t *testing.T) {
	s, _, _ := newStore(t, Options{})
	a, _ := mustCreate(t, s, "same", ScopeRead, 0)
	mustCreate(t, s, "same", ScopeRead, 0)
	if _, err := s.Revoke("same"); err == nil || !strings.Contains(err.Error(), "ambiguous") {
		t.Fatalf("err = %v", err)
	}
	if _, err := s.Revoke(a.ID); err != nil {
		t.Fatal(err)
	}
}

func TestCreateValidation(t *testing.T) {
	s, _, _ := newStore(t, Options{})
	for _, a := range []CreateArgs{{Name: "", Scope: ScopeRead}, {Name: "x", Scope: "root"}, {Name: "a\nb", Scope: ScopeRead}, {Name: strings.Repeat("n", 65), Scope: ScopeRead}} {
		if _, _, err := s.Create(a); err == nil {
			t.Errorf("accepted %+v", a)
		}
	}
}

// Concurrent creators/revokers (as CLI + gateway would be) never lose or
// corrupt records.
func TestConcurrentAccessWithFlock(t *testing.T) {
	_, _, dir := newStore(t, Options{})
	const workers, per = 6, 8
	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			st, err := Open(dir, Options{}) // separate Store = separate "process"
			if err != nil {
				t.Error(err)
				return
			}
			defer st.FlushTouches()
			for i := 0; i < per; i++ {
				d, tok, err := st.Create(CreateArgs{Name: fmt.Sprintf("w%d-%d", w, i), Scope: ScopeRead})
				if err != nil {
					t.Error(err)
					return
				}
				if _, err := st.Verify(tok, "127.0.0.1:1"); err != nil {
					t.Error(err)
				}
				if i%2 == 0 {
					if _, err := st.Revoke(d.ID); err != nil {
						t.Error(err)
					}
				}
			}
		}()
	}
	wg.Wait()
	st, _ := Open(dir, Options{})
	if st.Err() != nil {
		t.Fatal(st.Err())
	}
	l, err := st.List()
	if err != nil || len(l) != workers*per/2 {
		t.Fatalf("devices = %d (%v), want %d", len(l), err, workers*per/2)
	}
	if ents, _ := filepath.Glob(filepath.Join(dir, ".devices-*.tmp")); len(ents) != 0 {
		t.Fatalf("leftover temp files %v", ents)
	}
}

// ---- HTTP ----

func newServer(t *testing.T, s *Store) (*httptest.Server, *Auth) {
	t.Helper()
	a := NewAuth(s)
	srv := &httpapi.Server{Auth: a, Log: slog.New(slog.DiscardHandler)}
	srv.WrapHandler(a.Guard)
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	return ts, a
}

func get(t *testing.T, url, auth string) *http.Response {
	t.Helper()
	req, _ := http.NewRequest("GET", url, nil)
	if auth != "" {
		req.Header.Set("Authorization", auth)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { resp.Body.Close() })
	return resp
}

// /v1/info needs read; with no daemon behind it a valid token gets past auth
// and fails later with 503, which is all these tests need to tell apart from
// 401/403.
func TestScopeEnforcementOnInfo(t *testing.T) {
	s, _, _ := newStore(t, Options{})
	_, rtok := mustCreate(t, s, "r", ScopeRead, 0)
	ts, _ := newServer(t, s)
	if c := get(t, ts.URL+"/v1/info", "").StatusCode; c != 401 {
		t.Errorf("no token: %d", c)
	}
	if c := get(t, ts.URL+"/v1/info", "Bearer "+tamper(rtok)).StatusCode; c != 401 {
		t.Errorf("tampered: %d", c)
	}
	if c := get(t, ts.URL+"/v1/info", "Basic "+rtok).StatusCode; c != 401 {
		t.Errorf("basic scheme: %d", c)
	}
	if c := get(t, ts.URL+"/v1/info", rtok).StatusCode; c != 401 {
		t.Errorf("no scheme: %d", c)
	}
	if c := get(t, ts.URL+"/v1/info", "Bearer "+rtok).StatusCode; c == 401 || c == 403 {
		t.Errorf("valid read token rejected: %d", c)
	}
}

func TestAuthenticateScopes(t *testing.T) {
	s, _, _ := newStore(t, Options{})
	a := NewAuth(s)
	_, rtok := mustCreate(t, s, "r", ScopeRead, 0)
	_, wtok := mustCreate(t, s, "w", ScopeWrite, 0)
	_, atok := mustCreate(t, s, "a", ScopeAdmin, 0)
	cases := []struct {
		tok  string
		need httpapi.Scope
		err  error
	}{
		{rtok, httpapi.ScopeRead, nil}, {rtok, httpapi.ScopeWrite, httpapi.ErrForbidden}, {rtok, httpapi.ScopeAdmin, httpapi.ErrForbidden},
		{wtok, httpapi.ScopeRead, nil}, {wtok, httpapi.ScopeWrite, nil}, {wtok, httpapi.ScopeAdmin, httpapi.ErrForbidden},
		{atok, httpapi.ScopeRead, nil}, {atok, httpapi.ScopeWrite, nil}, {atok, httpapi.ScopeAdmin, nil},
	}
	for _, c := range cases {
		r := httptest.NewRequest("GET", "/x", nil)
		r.Header.Set("Authorization", "Bearer "+c.tok)
		p, err := a.Authenticate(r, c.need)
		if err != c.err || (err == nil && (p == nil || p.Device == "")) {
			t.Errorf("%s need %v: p=%v err=%v", c.tok[:14], c.need, p, err)
		}
	}
	// Two Authorization headers, or a cookie, are not tokens.
	r := httptest.NewRequest("GET", "/x", nil)
	r.Header.Add("Authorization", "Bearer "+atok)
	r.Header.Add("Authorization", "Bearer "+atok)
	if _, err := a.Authenticate(r, httpapi.ScopeRead); err != httpapi.ErrUnauthenticated {
		t.Errorf("two headers: %v", err)
	}
	r = httptest.NewRequest("GET", "/x", nil)
	r.AddCookie(&http.Cookie{Name: "token", Value: atok})
	if _, err := a.Authenticate(r, httpapi.ScopeRead); err != httpapi.ErrUnauthenticated {
		t.Errorf("cookie: %v", err)
	}
}

func TestQueryTokenRejected(t *testing.T) {
	s, _, _ := newStore(t, Options{})
	_, tok := mustCreate(t, s, "r", ScopeRead, 0)
	ts, _ := newServer(t, s)
	for _, q := range []string{"?token=" + tok, "?access_token=x", "?foo=" + tok, "?x=1&Token=abc", "?" + tok} {
		for _, hdr := range []string{"", "Bearer " + tok} {
			resp := get(t, ts.URL+"/v1/info"+q, hdr)
			if resp.StatusCode != 400 {
				t.Errorf("%s (hdr %v): %d", q, hdr != "", resp.StatusCode)
			}
			b, _ := io.ReadAll(resp.Body)
			if bytes.Contains(b, []byte(tok)) {
				t.Error("token echoed")
			}
		}
	}
	// Health too: the rule is global.
	if c := get(t, ts.URL+"/v1/health?token="+tok, "").StatusCode; c != 400 {
		t.Errorf("health: %d", c)
	}
}

// A revoke (here by another Store on the same dir, i.e. the CLI) ends an
// in-flight streaming request within ~1 s.
func TestRevokeClosesOpenStream(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "api")
	s, err := Open(dir, Options{})
	if err != nil {
		t.Fatal(err)
	}
	d, tok := mustCreate(t, s, "phone", ScopeRead, 0)
	_, otherTok := mustCreate(t, s, "tablet", ScopeRead, 0)
	a := NewAuth(s)
	ctx, stop := context.WithCancel(context.Background())
	defer stop()
	go s.Run(ctx)

	started := make(chan string, 4)
	ended := make(chan string, 4)
	mux := http.NewServeMux()
	mux.HandleFunc("GET /stream", func(w http.ResponseWriter, r *http.Request) {
		if _, err := a.Authenticate(r, httpapi.ScopeRead); err != nil {
			http.Error(w, "no", 401)
			return
		}
		id, _, _ := ParseToken(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
		w.WriteHeader(200)
		w.(http.Flusher).Flush()
		started <- id
		<-r.Context().Done()
		ended <- id
	})
	ts := httptest.NewServer(a.Guard(mux))
	defer ts.Close()

	open := func(tk string) *http.Response {
		req, _ := http.NewRequest("GET", ts.URL+"/stream", nil)
		req.Header.Set("Authorization", "Bearer "+tk)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		return resp
	}
	r1, r2 := open(tok), open(otherTok)
	defer r1.Body.Close()
	defer r2.Body.Close()
	for i := 0; i < 2; i++ {
		select {
		case <-started:
		case <-time.After(2 * time.Second):
			t.Fatal("streams did not start")
		}
	}
	cli, err := Open(dir, Options{})
	if err != nil {
		t.Fatal(err)
	}
	t0 := time.Now()
	if _, err := cli.Revoke(d.ID); err != nil {
		t.Fatal(err)
	}
	select {
	case id := <-ended:
		if id != d.ID {
			t.Fatalf("wrong stream ended: %s", id)
		}
		if el := time.Since(t0); el > 1500*time.Millisecond {
			t.Fatalf("took %v", el)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("stream not closed after revoke")
	}
	// The other device's stream is untouched; the revoked token is refused.
	select {
	case id := <-ended:
		t.Fatalf("stream of %s ended too", id)
	case <-time.After(1200 * time.Millisecond):
	}
	if resp := open(tok); resp.StatusCode != 401 {
		t.Fatalf("revoked token: %d", resp.StatusCode)
	}
}

// tamper changes the last character of tok.
func tamper(tok string) string {
	c := "A"
	if strings.HasSuffix(tok, "A") {
		c = "B"
	}
	return tok[:len(tok)-1] + c
}

// ---- AGED-US-0024 / review fixes ----

// Only a parameter that has the SHAPE of a device token is rejected: a session
// or project that merely mentions "agd_" must stay usable.
func TestQueryTokenShapeNotPrefix(t *testing.T) {
	s, _, _ := newStore(t, Options{})
	_, tok := mustCreate(t, s, "r", ScopeRead, 0)
	ts, _ := newServer(t, s)
	for _, q := range []string{"?session=agd_build", "?q=agd_", "?name=magd_x_y", "?kind=agd_ABCDEFGHJK_short"} {
		resp := get(t, ts.URL+"/v1/info"+q, "Bearer "+tok)
		if resp.StatusCode == 400 {
			t.Errorf("%s rejected as a token", q)
		}
		resp.Body.Close()
	}
	for _, q := range []string{"?a=" + tok, "?a=x%20" + tok, "?" + tok + "=1"} {
		if c := get(t, ts.URL+"/v1/info"+q, "Bearer "+tok).StatusCode; c != 400 {
			t.Errorf("%s = %d, want 400", q, c)
		}
	}
}

// The guard runs inside the request-id / access-log chain: its 400 is logged
// with a request id (and without the token).
func TestGuardRejectionIsLoggedWithRequestID(t *testing.T) {
	s, _, _ := newStore(t, Options{})
	_, tok := mustCreate(t, s, "r", ScopeRead, 0)
	a := NewAuth(s)
	logs := &syncBuf{}
	srv := &httpapi.Server{Auth: a, Guard: a.Guard, Log: slog.New(slog.NewTextHandler(logs, nil)), Config: httpapi.Config{Hostname: "example.com"}}
	h := srv.Handler()
	r := httptest.NewRequest("GET", "/v1/info?x="+tok, nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, r)
	id := rec.Header().Get("X-Request-Id")
	if rec.Code != 400 || id == "" {
		t.Fatalf("code %d id %q", rec.Code, id)
	}
	if !strings.Contains(logs.String(), id) || !strings.Contains(logs.String(), "status=400") || strings.Contains(logs.String(), tok) {
		t.Fatalf("log: %s", logs.String())
	}
}

// Verify never waits for the devices.json lock: last_seen is written by a
// background worker (coalesced), even while another process holds the flock.
func TestLastSeenWriteIsAsyncAndCoalesced(t *testing.T) {
	s, clk, dir := newStore(t, Options{})
	d1, tok1 := mustCreate(t, s, "a", ScopeRead, 0)
	d2, tok2 := mustCreate(t, s, "b", ScopeRead, 0)
	lf, err := os.OpenFile(filepath.Join(dir, lockName), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer lf.Close()
	if err := syscall.Flock(int(lf.Fd()), syscall.LOCK_EX); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	for i := 0; i < 5; i++ {
		clk.Add(2 * time.Minute) // every call is a new touch
		if _, err := s.Verify(tok1, "192.0.2.1:1"); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.Verify(tok2, "192.0.2.2:1"); err != nil {
		t.Fatal(err)
	}
	if el := time.Since(start); el > 500*time.Millisecond {
		t.Fatalf("Verify blocked on the file lock for %s", el)
	}
	_ = syscall.Flock(int(lf.Fd()), syscall.LOCK_UN)
	s.FlushTouches()
	l, _ := s.List()
	for _, d := range l {
		if d.LastSeenAt.IsZero() {
			t.Errorf("%s: last_seen never persisted", d.Name)
		}
	}
	_ = d1
	_ = d2
}
