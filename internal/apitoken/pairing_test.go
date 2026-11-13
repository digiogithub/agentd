package apitoken

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/digiogithub/agentd/internal/httpapi"
)

// pairEnv simulates the CLI process (cli) and the gateway process (gw): two
// Pairing and two Store instances over the same directory.
type pairEnv struct {
	cli, gw *Pairing
	devs    *Store // gateway's device store
	cliDevs *Store
	ts      *httptest.Server
	dir     string
	logs    *bytes.Buffer
	h       *pairHandler
}

type syncBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuf) Write(p []byte) (int, error) { s.mu.Lock(); defer s.mu.Unlock(); return s.b.Write(p) }
func (s *syncBuf) String() string              { s.mu.Lock(); defer s.mu.Unlock(); return s.b.String() }

func newPairEnv(t *testing.T) (*pairEnv, *syncBuf) {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "api")
	logs := &syncBuf{}
	log := slog.New(slog.NewTextHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	cli, err := OpenPairing(dir, PairOptions{})
	if err != nil {
		t.Fatal(err)
	}
	gw, err := OpenPairing(dir, PairOptions{Log: log})
	if err != nil {
		t.Fatal(err)
	}
	devs, err := Open(dir, Options{Log: log})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(devs.FlushTouches)
	cliDevs, _ := Open(dir, Options{})
	h := newPairHandler(gw, devs, PairHandlerOptions{Log: log, PollEach: 10 * time.Millisecond})
	srv := &httpapi.Server{Auth: NewAuth(devs), Log: slog.New(slog.DiscardHandler)}
	if err := srv.AddRoute(httpapi.Route{Method: "POST", Pattern: "/v1/pair", Handler: h.serve}); err != nil {
		t.Fatal(err)
	}
	srv.WrapHandler(NewAuth(devs).Guard)
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	return &pairEnv{cli: cli, gw: gw, devs: devs, cliDevs: cliDevs, ts: ts, dir: dir, h: h}, logs
}

type pairResult struct {
	status int
	body   string
	resp   pairResp
	code   string
}

func (e *pairEnv) post(t *testing.T, code, name, platform string) pairResult {
	t.Helper()
	b, _ := json.Marshal(pairReq{Code: code, Name: name, Platform: platform})
	return e.postRaw(t, string(b))
}

func (e *pairEnv) postRaw(t *testing.T, body string) pairResult {
	t.Helper()
	resp, err := http.Post(e.ts.URL+"/v1/pair", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	r := pairResult{status: resp.StatusCode, body: string(raw)}
	_ = json.Unmarshal(raw, &r.resp)
	var env struct{ Error struct{ Code string } }
	_ = json.Unmarshal(raw, &env)
	r.code = env.Error.Code
	return r
}

// asyncPost runs post in the background (the claim long-polls).
func (e *pairEnv) asyncPost(t *testing.T, code, name, platform string) <-chan pairResult {
	ch := make(chan pairResult, 1)
	go func() { ch <- e.post(t, code, name, platform) }()
	return ch
}

func (e *pairEnv) waitState(t *testing.T, id, state string) Pending {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if p, err := e.cli.Get(id); err == nil && p.State == state {
			return p
		}
		time.Sleep(5 * time.Millisecond)
	}
	p, err := e.cli.Get(id)
	t.Fatalf("pairing %s never reached %q (now %q, err %v)", id, state, p.State, err)
	return Pending{}
}

func recv(t *testing.T, ch <-chan pairResult) pairResult {
	t.Helper()
	select {
	case r := <-ch:
		return r
	case <-time.After(5 * time.Second):
		t.Fatal("request did not finish")
	}
	return pairResult{}
}

func TestCodeFormat(t *testing.T) {
	for i := 0; i < 50; i++ {
		c, err := newCode()
		if err != nil || len(c) != CodeLen || strings.Trim(c, crockford) != "" {
			t.Fatalf("code %q err %v", c, err)
		}
		f := FormatCode(c)
		if len(f) != 14 || f[4] != '-' || f[9] != '-' {
			t.Fatalf("format %q", f)
		}
		if n, ok := NormalizeCode(strings.ToLower(f)); !ok || n != c {
			t.Fatalf("normalize %q -> %q %v", f, n, ok)
		}
	}
	if n, ok := NormalizeCode("oi1l-0000-0000"); !ok || n != "011100000000" {
		t.Errorf("aliases: %q %v", n, ok)
	}
	for _, bad := range []string{"", "ABCD", "ABCD-EFGH-JKMN-P", "UUUU-UUUU-UUUU", "ABCD-EFGH-JK!N", "ÅBCD-EFGH-JKMN"} {
		if _, ok := NormalizeCode(bad); ok {
			t.Errorf("accepted %q", bad)
		}
	}
}

func TestPairFullFlowApproved(t *testing.T) {
	e, logs := newPairEnv(t)
	code, id, _, err := e.cli.Issue(IssueArgs{NameHint: "hint"})
	if err != nil {
		t.Fatal(err)
	}
	// The file holds a hash, never the code (and is 0600).
	raw, _ := os.ReadFile(filepath.Join(e.dir, pairFile))
	if bytes.Contains(raw, []byte(strings.ReplaceAll(code, "-", ""))) {
		t.Fatal("clear code stored")
	}
	if fi, _ := os.Stat(filepath.Join(e.dir, pairFile)); fi.Mode().Perm() != 0o600 {
		t.Errorf("mode %v", fi.Mode())
	}
	ch := e.asyncPost(t, strings.ToLower(code), "Pixel", "android")
	p := e.waitState(t, id, PairClaimed)
	if p.Device == nil || p.Device.Name != "Pixel" || p.Device.Platform != "android" || p.Device.Addr == "" {
		t.Fatalf("claim info %+v", p.Device)
	}
	select {
	case r := <-ch:
		t.Fatalf("answered before approval: %d %s", r.status, r.body)
	case <-time.After(150 * time.Millisecond):
	}
	if err := e.cli.Decide(id, true); err != nil {
		t.Fatal(err)
	}
	r := recv(t, ch)
	if r.status != 201 || r.resp.Scope != ScopeWrite || !strings.HasPrefix(r.resp.Token, Prefix) || r.resp.DeviceID == "" {
		t.Fatalf("pair: %d %s", r.status, r.body)
	}
	// The token works through the authenticator on a protected route.
	req, _ := http.NewRequest("GET", e.ts.URL+"/v1/info", nil)
	req.Header.Set("Authorization", "Bearer "+r.resp.Token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode == 401 || resp.StatusCode == 403 {
		t.Fatalf("paired token rejected: %d", resp.StatusCode)
	}
	// The CLI process sees the device in its own store view, paired via qr.
	devs, _ := e.cliDevs.List()
	if len(devs) != 1 || devs[0].Name != "Pixel" || devs[0].PairedVia != "qr" || devs[0].Scope != ScopeWrite {
		t.Fatalf("devices %+v", devs)
	}
	if done := e.waitState(t, id, PairDone); done.DeviceID != r.resp.DeviceID {
		t.Errorf("device id %q", done.DeviceID)
	}
	// Replay of the used code.
	if r2 := e.post(t, code, "Evil", ""); r2.status != 403 || r2.code != "pairing_invalid" {
		t.Errorf("replay: %d %s", r2.status, r2.body)
	}
	// Neither codes nor tokens in logs.
	for _, secret := range []string{strings.ReplaceAll(code, "-", ""), code, r.resp.Token} {
		if strings.Contains(logs.String(), secret) {
			t.Fatalf("secret in logs: %s", logs.String())
		}
	}
}

func TestPairDenied(t *testing.T) {
	e, _ := newPairEnv(t)
	code, id, _, _ := e.cli.Issue(IssueArgs{})
	ch := e.asyncPost(t, code, "Phone", "")
	e.waitState(t, id, PairClaimed)
	if err := e.cli.Decide(id, false); err != nil {
		t.Fatal(err)
	}
	if r := recv(t, ch); r.status != 403 || r.code != "pairing_denied" || strings.Contains(r.body, "agd_") {
		t.Fatalf("deny: %d %s", r.status, r.body)
	}
	if devs, _ := e.cliDevs.List(); len(devs) != 0 {
		t.Fatalf("device created: %+v", devs)
	}
	// The code cannot be reused after a denial.
	if r := e.post(t, code, "Phone", ""); r.status != 403 || r.code != "pairing_invalid" {
		t.Errorf("reuse after deny: %d", r.status)
	}
}

func TestPairTimeoutWhileWaitingForApproval(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "api")
	clk := &fakeClock{t: time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)}
	cli, _ := OpenPairing(dir, PairOptions{Clock: clk.Now})
	devs, _ := Open(dir, Options{Clock: clk.Now})
	h := newPairHandler(cli, devs, PairHandlerOptions{Clock: clk.Now, PollEach: 10 * time.Millisecond})
	srv := &httpapi.Server{}
	_ = srv.AddRoute(httpapi.Route{Method: "POST", Pattern: "/v1/pair", Handler: h.serve})
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()
	e := &pairEnv{cli: cli, ts: ts}
	code, id, _, _ := cli.Issue(IssueArgs{TTL: time.Minute})
	ch := e.asyncPost(t, code, "Slow", "")
	e.waitState(t, id, PairClaimed)
	clk.Add(2 * time.Minute)
	if r := recv(t, ch); r.status != 410 || r.code != "pairing_expired" {
		t.Fatalf("timeout: %d %s", r.status, r.body)
	}
	if l, _ := devs.List(); len(l) != 0 {
		t.Fatalf("device created: %+v", l)
	}
	// Expired code is simply invalid.
	code2, _, _, _ := cli.Issue(IssueArgs{TTL: time.Minute})
	clk.Add(2 * time.Minute)
	if r := e.post(t, code2, "Late", ""); r.status != 403 {
		t.Fatalf("expired code: %d", r.status)
	}
}

func TestWrongAttemptsBurnTheCode(t *testing.T) {
	e, _ := newPairEnv(t)
	code, id, _, _ := e.cli.Issue(IssueArgs{})
	for i := 0; i < MaxAttempts; i++ {
		if r := e.post(t, "0000-0000-000"+string(rune('0'+i)), "x", ""); r.status != 403 || r.code != "pairing_invalid" {
			t.Fatalf("attempt %d: %d %s", i, r.status, r.body)
		}
	}
	p, _ := e.cli.Get(id)
	if p.State != PairBurned {
		t.Fatalf("state %q after %d wrong attempts", p.State, MaxAttempts)
	}
	// Even the right code is refused now, with the same answer as a wrong one.
	if r := e.post(t, code, "Right", ""); r.status != 403 || r.code != "pairing_invalid" {
		t.Fatalf("burned code accepted: %d %s", r.status, r.body)
	}
	if l, _ := e.cliDevs.List(); len(l) != 0 {
		t.Fatalf("device created: %+v", l)
	}
}

func TestFourWrongThenRightStillWorks(t *testing.T) {
	e, _ := newPairEnv(t)
	code, id, _, _ := e.cli.Issue(IssueArgs{NoApprove: true})
	for i := 0; i < MaxAttempts-1; i++ {
		e.post(t, "ZZZZ-ZZZZ-ZZZZ", "x", "")
	}
	r := e.post(t, code, "Ok", "linux") // no-approve: answered at once
	if r.status != 201 {
		t.Fatalf("%d %s", r.status, r.body)
	}
	e.waitState(t, id, PairDone)
}

func TestMalformedRequests(t *testing.T) {
	e, _ := newPairEnv(t)
	code, _, _, _ := e.cli.Issue(IssueArgs{NoApprove: true})
	for name, body := range map[string]string{
		"not json":    "nope",
		"unknown key": `{"code":"` + code + `","name":"a","admin":true}`,
		"no name":     `{"code":"` + code + `"}`,
		"control":     `{"code":"` + code + `","name":"a\u001b[2Jb"}`,
		"bidi":        `{"code":"` + code + `","name":"a‮b"}`,
		"long name":   `{"code":"` + code + `","name":"` + strings.Repeat("a", 65) + `"}`,
		"long code":   `{"code":"` + strings.Repeat("A", 100) + `","name":"a"}`,
	} {
		if r := e.postRaw(t, body); r.status != 400 {
			t.Errorf("%s: %d %s", name, r.status, r.body)
		}
	}
	// None of that consumed the code.
	if r := e.post(t, code, "Fine", ""); r.status != 201 {
		t.Fatalf("code consumed by malformed requests: %d %s", r.status, r.body)
	}
}

func TestRateLimitPerAddress(t *testing.T) {
	e, _ := newPairEnv(t)
	for i := 0; i < AddrLimit; i++ {
		if r := e.post(t, "ZZZZ-ZZZZ-ZZZZ", "x", ""); r.status != 403 {
			t.Fatalf("request %d: %d", i, r.status)
		}
	}
	resp, err := http.Post(e.ts.URL+"/v1/pair", "application/json", strings.NewReader(`{"code":"ZZZZ-ZZZZ-ZZZZ","name":"x"}`))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 429 || resp.Header.Get("Retry-After") == "" {
		t.Fatalf("limit: %d retry-after %q", resp.StatusCode, resp.Header.Get("Retry-After"))
	}
}

func TestRateLimitWindowAndLockout(t *testing.T) {
	clk := &fakeClock{t: time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)}
	dir := filepath.Join(t.TempDir(), "api")
	p, _ := OpenPairing(dir, PairOptions{Clock: clk.Now})
	devs, _ := Open(dir, Options{Clock: clk.Now})
	h := newPairHandler(p, devs, PairHandlerOptions{Clock: clk.Now})
	// Window slides.
	for i := 0; i < AddrLimit; i++ {
		if _, ok := h.admit("1.1.1.1"); !ok {
			t.Fatal("early refusal")
		}
	}
	if _, ok := h.admit("1.1.1.1"); ok {
		t.Fatal("11th accepted")
	}
	if _, ok := h.admit("2.2.2.2"); !ok {
		t.Fatal("other address limited")
	}
	clk.Add(AddrWindow + time.Second)
	if _, ok := h.admit("1.1.1.1"); !ok {
		t.Fatal("window did not slide")
	}
	// Global lockout after LockoutFails failures from many addresses.
	for i := 0; i < LockoutFails; i++ {
		h.failed("x")
	}
	if retry, ok := h.admit("3.3.3.3"); ok || retry <= 0 {
		t.Fatalf("not locked out: %d %v", retry, ok)
	}
	clk.Add(LockoutFor + time.Second)
	if _, ok := h.admit("3.3.3.3"); !ok {
		t.Fatal("lockout did not end")
	}
}

func TestSingleUseConcurrentClaims(t *testing.T) {
	e, _ := newPairEnv(t)
	code, _, _, _ := e.cli.Issue(IssueArgs{NoApprove: true})
	var wg sync.WaitGroup
	var mu sync.Mutex
	ok := 0
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if r := e.post(t, code, "racer", ""); r.status == 201 {
				mu.Lock()
				ok++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if ok != 1 {
		t.Fatalf("%d tokens issued for one code", ok)
	}
	if l, _ := e.cliDevs.List(); len(l) != 1 {
		t.Fatalf("devices: %d", len(l))
	}
}

func TestAbandonedClaimBurnsCode(t *testing.T) {
	e, _ := newPairEnv(t)
	code, id, _, _ := e.cli.Issue(IssueArgs{})
	req, _ := http.NewRequest("POST", e.ts.URL+"/v1/pair", strings.NewReader(`{"code":"`+code+`","name":"gone"}`))
	ctx, cancel := context.WithCancel(context.Background())
	req.Header.Set("Content-Type", "application/json")
	req = req.WithContext(ctx)
	done := make(chan struct{})
	go func() { _, _ = http.DefaultClient.Do(req); close(done) }()
	e.waitState(t, id, PairClaimed)
	cancel()
	<-done
	e.waitState(t, id, PairBurned)
	if err := e.cli.Decide(id, true); err == nil {
		t.Fatal("decide on a burned record succeeded")
	}
}

func TestIssueEvictsOldestBeyondMax(t *testing.T) {
	e, _ := newPairEnv(t)
	var ids []string
	var codes []string
	for i := 0; i < MaxPending+1; i++ {
		c, id, _, err := e.cli.Issue(IssueArgs{NoApprove: true})
		if err != nil {
			t.Fatal(err)
		}
		ids, codes = append(ids, id), append(codes, c)
	}
	if p, _ := e.cli.Get(ids[0]); p.State != PairBurned {
		t.Errorf("oldest state %q", p.State)
	}
	if r := e.post(t, codes[0], "old", ""); r.status != 403 {
		t.Errorf("evicted code accepted: %d", r.status)
	}
	if r := e.post(t, codes[MaxPending], "new", ""); r.status != 201 {
		t.Errorf("newest code: %d", r.status)
	}
}

func TestIssueValidation(t *testing.T) {
	e, _ := newPairEnv(t)
	if _, _, _, err := e.cli.Issue(IssueArgs{Scope: "root"}); err == nil {
		t.Error("bad scope accepted")
	}
	if _, _, _, err := e.cli.Issue(IssueArgs{TTL: time.Hour}); err == nil {
		t.Error("ttl over max accepted")
	}
	_, id, exp, err := e.cli.Issue(IssueArgs{})
	if err != nil {
		t.Fatal(err)
	}
	if p, _ := e.cli.Get(id); p.Scope != ScopeWrite || p.ExpiresAt.Sub(p.CreatedAt) != DefaultPairTTL || !exp.Equal(p.ExpiresAt) {
		t.Errorf("defaults %+v", p)
	}
}

func TestUnsafePairingFileRefused(t *testing.T) {
	e, _ := newPairEnv(t)
	code, _, _, _ := e.cli.Issue(IssueArgs{NoApprove: true})
	if err := os.Chmod(filepath.Join(e.dir, pairFile), 0o644); err != nil {
		t.Fatal(err)
	}
	if r := e.post(t, code, "x", ""); r.status != 503 {
		t.Fatalf("unsafe file: %d", r.status)
	}
	if _, _, _, err := e.cli.Issue(IssueArgs{}); err == nil {
		t.Error("issue overwrote an unsafe file")
	}
}

type auditSink struct {
	mu  sync.Mutex
	evs []httpapi.AuditEvent
}

func (a *auditSink) Audit(ev httpapi.AuditEvent) {
	a.mu.Lock()
	a.evs = append(a.evs, ev)
	a.mu.Unlock()
}

func TestPairingIsAudited(t *testing.T) {
	e, _ := newPairEnv(t)
	sink := &auditSink{}
	e.h.aud = sink
	code, id, _, _ := e.cli.Issue(IssueArgs{})
	ch := e.asyncPost(t, code, "Pixel", "android")
	e.waitState(t, id, PairClaimed)
	if err := e.cli.Decide(id, true); err != nil {
		t.Fatal(err)
	}
	r := recv(t, ch)
	if r.status != 201 {
		t.Fatalf("pair: %d %s", r.status, r.body)
	}
	sink.mu.Lock()
	defer sink.mu.Unlock()
	if len(sink.evs) != 1 || sink.evs[0].Action != "device_pair" || sink.evs[0].Device != r.resp.DeviceID || sink.evs[0].Result != "ok" {
		t.Fatalf("audit = %+v", sink.evs)
	}
	b, _ := json.Marshal(sink.evs[0])
	if strings.Contains(string(b), r.resp.Token) || strings.Contains(string(b), strings.ReplaceAll(code, "-", "")) {
		t.Errorf("audit line leaks a secret: %s", b)
	}
}
