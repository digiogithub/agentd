package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/digiogithub/agentd/internal/apitoken"
	"github.com/digiogithub/agentd/internal/config"
	"github.com/digiogithub/agentd/internal/httpapi"
)

func TestPairURLFormat(t *testing.T) {
	got := pairURL("https://box.example.ts.net:7477", "7K2M-9QXD-4T3A", "sha256:ab12", "box")
	want := "agentd://pair?v=1&u=https%3A%2F%2Fbox.example.ts.net%3A7477&c=7K2M-9QXD-4T3A&fp=sha256%3Aab12&h=box"
	if got != want {
		t.Fatalf("got  %s\nwant %s", got, want)
	}
	u, err := url.Parse(got)
	if err != nil || u.Scheme != "agentd" || u.Host != "pair" {
		t.Fatalf("parse: %v %v", u, err)
	}
	q := u.Query()
	if q.Get("v") != "1" || q.Get("u") != "https://box.example.ts.net:7477" || q.Get("c") != "7K2M-9QXD-4T3A" || q.Get("fp") != "sha256:ab12" || q.Get("h") != "box" {
		t.Fatalf("query %v", q)
	}
	// fp omitted without a pin.
	if s := pairURL("http://127.0.0.1:7477", "AAAA-BBBB-CCCC", "", "box"); strings.Contains(s, "fp=") {
		t.Errorf("fp present: %s", s)
	}
}

func TestGatewayURL(t *testing.T) {
	cases := []struct {
		api      config.APIConfig
		override string
		want     string
		err      bool
	}{
		{config.APIConfig{Listen: "127.0.0.1:7477", TLS: "off"}, "", "http://127.0.0.1:7477", false},
		{config.APIConfig{Listen: "100.64.0.2:8443", TLS: "files"}, "", "https://100.64.0.2:8443", false},
		{config.APIConfig{Listen: "127.0.0.1:7477", TLS: "off"}, "https://box.ts.net/", "https://box.ts.net", false},
		{config.APIConfig{Listen: "127.0.0.1:7477"}, "ftp://x", "", true},
		{config.APIConfig{Listen: "127.0.0.1:7477"}, "box", "", true},
	}
	for _, c := range cases {
		got, err := gatewayURL(c.api, c.override)
		if (err != nil) != c.err || got != c.want {
			t.Errorf("%+v %q: got %q err %v", c.api, c.override, got, err)
		}
	}
	// Unspecified host becomes a concrete name.
	if got, err := gatewayURL(config.APIConfig{Listen: "0.0.0.0:7477", TLS: "files"}, ""); err != nil || strings.Contains(got, "0.0.0.0") {
		t.Errorf("unspecified: %q %v", got, err)
	}
}

type lockedBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *lockedBuf) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}
func (l *lockedBuf) String() string { l.mu.Lock(); defer l.mu.Unlock(); return l.b.String() }

// pairHarness runs `serve pair` (runPair) in one goroutine against a gateway
// handler in the same state dir, like the two real processes.
type pairHarness struct {
	url    string
	out    *lockedBuf
	errOut *lockedBuf
	done   chan error
	cancel context.CancelFunc
	devs   *apitoken.Store
	srv    *httptest.Server
}

func startPair(t *testing.T, o pairOptions, stdin string) *pairHarness {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "api")
	devs, err := apitoken.Open(dir, apitoken.Options{})
	if err != nil {
		t.Fatal(err)
	}
	pr, err := apitoken.OpenPairing(dir, apitoken.PairOptions{})
	if err != nil {
		t.Fatal(err)
	}
	a := apitoken.NewAuth(devs)
	srv := &httpapi.Server{Auth: a}
	if err := srv.AddRoute(apitoken.PairRoute(pr, devs, apitoken.PairHandlerOptions{PollEach: 10 * time.Millisecond})); err != nil {
		t.Fatal(err)
	}
	srv.WrapHandler(a.Guard)
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	h := &pairHarness{url: ts.URL, out: &lockedBuf{}, errOut: &lockedBuf{}, done: make(chan error, 1), devs: devs, srv: ts}
	o.Dir, o.Poll, o.Hostname = dir, 10*time.Millisecond, "testbox"
	o.API = config.APIConfig{Listen: "127.0.0.1:7477", TLS: "off"}
	ctx, cancel := context.WithCancel(context.Background())
	h.cancel = cancel
	t.Cleanup(cancel)
	go func() { h.done <- runPair(ctx, o, strings.NewReader(stdin), h.out, h.errOut) }()
	return h
}

var codeRE = regexp.MustCompile(`(?m)^Code:\s+([0-9A-Z]{4}-[0-9A-Z]{4}-[0-9A-Z]{4})$`)

func (h *pairHarness) code(t *testing.T) string {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if m := codeRE.FindStringSubmatch(h.out.String()); m != nil {
			return m[1]
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("no code printed: %q", h.out.String())
	return ""
}

func (h *pairHarness) claim(t *testing.T, code, name string) (int, map[string]any) {
	t.Helper()
	b, _ := json.Marshal(map[string]string{"code": code, "name": name, "platform": "linux"})
	resp, err := http.Post(h.url+"/v1/pair", "application/json", bytes.NewReader(b))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	var m map[string]any
	_ = json.Unmarshal(raw, &m)
	return resp.StatusCode, m
}

func (h *pairHarness) wait(t *testing.T) error {
	t.Helper()
	select {
	case err := <-h.done:
		return err
	case <-time.After(5 * time.Second):
		t.Fatal("runPair did not return")
	}
	return nil
}

func TestPairCLIApprove(t *testing.T) {
	h := startPair(t, pairOptions{Name: "hint"}, "y\n")
	code := h.code(t)
	out := h.out.String()
	for _, want := range []string{"agentd://pair?v=1&u=http%3A%2F%2F127.0.0.1%3A7477&c=" + code + "&h=testbox", "URL:   http://127.0.0.1:7477", "write"} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
	if !strings.ContainsAny(out, "\u2580\u2584\u2588") {
		t.Error("no half-block QR in output")
	}
	status, body := h.claim(t, code, "My Phone")
	if status != 201 || body["scope"] != "write" || !strings.HasPrefix(body["token"].(string), "agd_") {
		t.Fatalf("claim: %d %v", status, body)
	}
	if err := h.wait(t); err != nil {
		t.Fatalf("runPair: %v", err)
	}
	if o := h.out.String(); !strings.Contains(o, `"My Phone" (linux, 127.0.0.1)`) || !strings.Contains(o, "Approve? [y/N]") || !strings.Contains(o, "Paired:") {
		t.Errorf("prompt/outcome missing:\n%s", o)
	}
	if strings.Contains(h.out.String()+h.errOut.String(), body["token"].(string)) {
		t.Error("token printed by the CLI")
	}
	// A second claim (replay) fails.
	if s, _ := h.claim(t, code, "Replay"); s != 403 {
		t.Errorf("replay: %d", s)
	}
}

func TestPairCLIDefaultIsNo(t *testing.T) {
	for _, stdin := range []string{"\n", "", "n\n", "maybe\n"} {
		h := startPair(t, pairOptions{}, stdin)
		code := h.code(t)
		status, body := h.claim(t, code, "Intruder")
		if status != 403 {
			t.Fatalf("stdin %q: claim got %d %v", stdin, status, body)
		}
		if err := h.wait(t); err == nil || !strings.Contains(err.Error(), "denied") {
			t.Errorf("stdin %q: err %v", stdin, err)
		}
		if l, _ := h.devs.List(); len(l) != 0 {
			t.Errorf("device created on deny")
		}
	}
}

func TestPairCLINoApprove(t *testing.T) {
	h := startPair(t, pairOptions{NoApprove: true, Scope: "read"}, "")
	code := h.code(t)
	status, body := h.claim(t, code, "Script")
	if status != 201 || body["scope"] != "read" {
		t.Fatalf("%d %v", status, body)
	}
	if err := h.wait(t); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(h.out.String(), "Approve?") || !strings.Contains(h.errOut.String(), "--no-approve") {
		t.Errorf("out %q err %q", h.out.String(), h.errOut.String())
	}
}

func TestPairCLICancelBurnsCode(t *testing.T) {
	h := startPair(t, pairOptions{}, "")
	code := h.code(t)
	h.cancel()
	if err := h.wait(t); err == nil || !strings.Contains(err.Error(), "cancelled") {
		t.Fatalf("err %v", err)
	}
	if s, _ := h.claim(t, code, "Late"); s != 403 {
		t.Errorf("cancelled code accepted: %d", s)
	}
}

func TestPairCLIRejectsBadScopeAndTTL(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "api")
	api := config.APIConfig{Listen: "127.0.0.1:7477", TLS: "off"}
	var out, errw bytes.Buffer
	if err := runPair(context.Background(), pairOptions{API: api, Dir: dir, Scope: "root"}, strings.NewReader(""), &out, &errw); err == nil {
		t.Error("bad scope accepted")
	}
	if err := runPair(context.Background(), pairOptions{API: api, Dir: dir, TTL: time.Hour}, strings.NewReader(""), &out, &errw); err == nil {
		t.Error("ttl over max accepted")
	}
	if strings.Contains(out.String(), "Code:") {
		t.Error("code printed despite error")
	}
}
