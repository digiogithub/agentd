package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/digiogithub/agentd/internal/client"
	"github.com/digiogithub/agentd/internal/config"
)

// A missing dialer is a plain error, never a nil-function panic (it used to
// crash at the daemon ping when a test or caller forgot Dial).
func TestRunServeNilDialIsAnError(t *testing.T) {
	err := runServe(context.Background(), serveOptions{API: config.Default().API, Listen: "127.0.0.1:0", Log: discardLog()})
	if err == nil || !strings.Contains(err.Error(), "dialer") {
		t.Fatalf("err = %v", err)
	}
	// A nil logger is tolerated too.
	err = runServe(context.Background(), serveOptions{API: config.Default().API, Listen: "127.0.0.1:0",
		Dial: func() (*client.Client, error) { return nil, errors.New("down") }})
	if err == nil || !strings.Contains(err.Error(), "daemon") {
		t.Fatalf("err = %v", err)
	}
}

func httpDo(t *testing.T, method, url, host, auth, body string) (int, string) {
	t.Helper()
	req, _ := http.NewRequest(method, url, strings.NewReader(body))
	if host != "" {
		req.Host = host
	}
	if auth != "" {
		req.Header.Set("Authorization", auth)
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var b strings.Builder
	buf := make([]byte, 4096)
	for {
		n, err := resp.Body.Read(buf)
		b.Write(buf[:n])
		if err != nil {
			break
		}
	}
	return resp.StatusCode, b.String()
}

func TestServePairingToggleHostAndAudit(t *testing.T) {
	sock := serveTestDaemon(t)
	state := filepath.Join(t.TempDir(), "state")
	devDir := filepath.Join(state, "api")
	t.Setenv(config.EnvStateDir, state)
	out, _, err := runCLI(t, "serve", "devices", "add", "--name", "ci", "--scope", "write")
	if err != nil {
		t.Fatal(err)
	}
	tok := strings.TrimSpace(out)

	// pairing = true (default): /v1/pair is mounted (a bad code is 403, not 404).
	addr, cancel, done := startServe(t, sock, serveOptions{API: config.Default().API, Listen: "127.0.0.1:0", DevicesDir: devDir})
	base := "http://" + addr.String()
	if c, b := httpDo(t, "POST", base+"/v1/pair", "", "", `{"code":"AAAA-AAAA","name":"n"}`); c != 403 {
		t.Errorf("pairing enabled: %d %s", c, b)
	}
	// Unknown Host: 421 before auth, on every route including health and pair.
	for _, p := range []string{"/v1/health", "/v1/info", "/v1/pair"} {
		if c, _ := httpDo(t, "GET", base+p, "evil.example", "Bearer "+tok, ""); c != 421 {
			t.Errorf("Host evil.example on %s = %d, want 421", p, c)
		}
	}
	if c, _ := httpDo(t, "GET", base+"/v1/health", "localhost:7477", "", ""); c != 200 {
		t.Errorf("localhost Host = %d", c)
	}
	// A refused kill is audited (device, action, result), tokens never.
	if c, _ := httpDo(t, "POST", base+"/v1/sessions/ghost/kill", "", "Bearer "+tok, ""); c != 404 {
		t.Errorf("kill ghost = %d", c)
	}
	cancel()
	<-done

	// pairing = false: the route does not exist.
	api := config.Default().API
	f := false
	api.Pairing = &f
	addr, cancel, done = startServe(t, sock, serveOptions{API: api, Listen: "127.0.0.1:0", DevicesDir: devDir})
	if c, b := httpDo(t, "POST", "http://"+addr.String()+"/v1/pair", "", "", `{"code":"AAAA-AAAA","name":"n"}`); c != 404 {
		t.Errorf("pairing disabled: %d %s", c, b)
	}
	cancel()
	<-done

	// Audit log: gateway (kill) and CLI (device add / revoke) lines, 0600.
	if _, _, err := runCLI(t, "serve", "devices", "revoke", "ci"); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(devDir, "audit.log")
	st, err := os.Stat(path)
	if err != nil || st.Mode().Perm() != 0o600 {
		t.Fatalf("audit.log: %v %v", st, err)
	}
	raw, _ := os.ReadFile(path)
	if strings.Contains(string(raw), tok) {
		t.Fatal("token in the audit log")
	}
	seen := map[string]string{}
	for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		var ev struct {
			Action, Result, Device string
		}
		if err := json.Unmarshal([]byte(line), &ev); err != nil {
			t.Fatalf("bad line %q: %v", line, err)
		}
		seen[ev.Action] = ev.Result + "/" + ev.Device
	}
	for _, a := range []string{"device_add", "device_revoke", "kill"} {
		if seen[a] == "" {
			t.Errorf("no %s in the audit log: %v", a, seen)
		}
	}
	if !strings.HasPrefix(seen["kill"], "not_found/") || seen["device_add"] != "ok/cli" {
		t.Errorf("audit = %v", seen)
	}
}

func TestAPIConfigHardeningDefaults(t *testing.T) {
	p := filepath.Join(t.TempDir(), "c.toml")
	if err := os.WriteFile(p, []byte("[api]\npairing = false\nautostart_daemon = false\nrate_limit = 7\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(p)
	if err != nil {
		t.Fatal(err)
	}
	a := cfg.API
	if a.PairingEnabled() || a.AutostartEnabled() || a.RateLimit != 7 || a.RateBurst != 40 || a.ActRateLimit != 5 ||
		a.MaxStreamsPerDevice != 8 || a.MaxStreams != 64 || a.AuditMaxMB != 10 {
		t.Fatalf("api = %+v", a)
	}
	d := config.Default().API
	if !d.PairingEnabled() || !d.AutostartEnabled() {
		t.Fatal("defaults must be pairing=true, autostart_daemon=true")
	}
}
