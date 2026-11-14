package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/digiogithub/agentd/internal/config"
	"github.com/digiogithub/agentd/internal/daemon"
	"github.com/digiogithub/agentd/internal/mailbox"
)

// hcTestDaemon runs a daemon whose config file is configTOML, with
// herdr-core's places (XDG_RUNTIME_DIR, XDG_STATE_HOME) under a temp root.
func hcTestDaemon(t *testing.T, configTOML string) (root string) {
	t.Helper()
	root, err := os.MkdirTemp("", "agentd-h")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	t.Setenv(config.EnvSocket, filepath.Join(root, "agentd.sock"))
	t.Setenv(config.EnvStateDir, filepath.Join(root, "agentd-state"))
	cfgPath := filepath.Join(root, "config.toml")
	t.Setenv(config.EnvConfig, cfgPath)
	t.Setenv("XDG_RUNTIME_DIR", filepath.Join(root, "run"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(root, "state"))
	if err := os.WriteFile(cfgPath, []byte(configTOML), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := config.EnsureDirs(); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(cfgPath)
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
	return root
}

func runHC(t *testing.T, args ...string) (int, map[string]any, string) {
	t.Helper()
	var out, errb bytes.Buffer
	code := hcMain(args, strings.NewReader(""), &out, &errb)
	var m map[string]any
	if s := strings.TrimSpace(out.String()); s != "" && strings.HasPrefix(s, "{") {
		if err := json.Unmarshal([]byte(s), &m); err != nil {
			t.Fatalf("hc %v: stdout %q: %v", args, s, err)
		}
	}
	return code, m, errb.String()
}

// The shapes Whip and herdr-core's own CLI read: `server start --json` and
// `server status --json` print server.status (exit 0), `server stop --json`
// prints {"stopped": bool} and never stops the daemon.
func TestHCServerCommands(t *testing.T) {
	root := hcTestDaemon(t, "[herdr]\nlisten = true\n")
	sock := filepath.Join(root, "run", "herdr-core", "default.sock")

	code, st, stderr := runHC(t, "server", "start", "--json")
	if code != 0 {
		t.Fatalf("start: %d %s", code, stderr)
	}
	core, _ := st["core"].(map[string]any)
	if core["id"] == "" || st["installed_by"] != "agentd" || st["pid"].(float64) != float64(os.Getpid()) {
		t.Fatalf("start = %v", st)
	}
	for _, k := range []string{"version", "uptime_ms", "holders", "live", "finished"} {
		if _, ok := st[k]; !ok {
			t.Fatalf("start lacks %s: %v", k, st)
		}
	}
	l := st["agentd"].(map[string]any)["listener"].(map[string]any)
	if l["listening"] != true || l["socket"] != sock {
		t.Fatalf("listener = %v", l)
	}
	if _, err := os.Stat(sock); err != nil {
		t.Fatal(err)
	}
	// The same core id as `hc connect` uses.
	if id, _ := hcCoreID(); id != core["id"] {
		t.Fatalf("core id %v, connect uses %s", core["id"], id)
	}

	if code, st, stderr = runHC(t, "server", "status", "--json"); code != 0 || st["core"] == nil {
		t.Fatalf("status: %d %v %s", code, st, stderr)
	}
	if code, st, _ = runHC(t, "server", "stop", "--json"); code != 0 || st["stopped"] != true {
		t.Fatalf("stop: %d %v", code, st)
	}
	if _, err := os.Lstat(sock); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("socket after stop: %v", err)
	}
	if err := daemon.Ping(config.SocketPath(), time.Second); err != nil {
		t.Fatalf("daemon stopped with the listener: %v", err)
	}
	if code, st, _ = runHC(t, "server", "stop", "--json"); code != 0 || st["stopped"] != false {
		t.Fatalf("second stop: %d %v", code, st)
	}
	// status still answers (the core is the daemon); the listener is off.
	if code, st, _ = runHC(t, "server", "status", "--json"); code != 0 ||
		st["agentd"].(map[string]any)["listener"].(map[string]any)["listening"] != false {
		t.Fatalf("status after stop: %d %v", code, st)
	}
	// Human output without --json.
	var out bytes.Buffer
	if hcMain([]string{"server", "start"}, nil, &out, &out) != 0 || !strings.Contains(out.String(), "hc socket "+sock) {
		t.Fatalf("human start: %q", out.String())
	}
	if code, _, stderr = runHC(t, "server", "frobnicate"); code != 2 || !strings.Contains(stderr, `"error"`) {
		t.Fatalf("unknown server action: %d %s", code, stderr)
	}
}

// With the listener off (the default), start still exits 0 so Whip falls
// back to `hc connect --session S`.
func TestHCServerStartWithListenerOff(t *testing.T) {
	hcTestDaemon(t, "")
	code, st, stderr := runHC(t, "server", "start", "--json")
	if code != 0 {
		t.Fatalf("start: %d %s", code, stderr)
	}
	l := st["agentd"].(map[string]any)["listener"].(map[string]any)
	if l["listening"] != false || !strings.Contains(l["reason"].(string), "listen = false") {
		t.Fatalf("listener = %v", l)
	}
}

// No daemon: status fails like herdr-core's (no autostart), stop reports
// {"stopped": false}.
func TestHCServerWithoutDaemon(t *testing.T) {
	dir, err := os.MkdirTemp("", "agentd-h")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	t.Setenv(config.EnvSocket, filepath.Join(dir, "none.sock"))
	t.Setenv(config.EnvStateDir, filepath.Join(dir, "state"))
	if code, _, stderr := runHC(t, "server", "status", "--json"); code != 2 || !strings.Contains(stderr, "no core running") {
		t.Fatalf("status: %d %s", code, stderr)
	}
	if code, st, _ := runHC(t, "server", "stop", "--json"); code != 0 || st["stopped"] != false {
		t.Fatalf("stop: %d %v", code, st)
	}
	if _, err := os.Lstat(filepath.Join(dir, "none.sock")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("status or stop started a daemon")
	}
}

// Whip's exec fallback: `hc connect --session S` (S is accepted, ignored).
func TestHCConnectAcceptsSession(t *testing.T) {
	hcTestDaemon(t, "")
	var out, errb bytes.Buffer
	in := strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"hello","params":{"generation":1}}` + "\n")
	if code := hcMain([]string{"connect", "--session", "work"}, in, &out, &errb); code != 0 {
		t.Fatalf("connect: %d %s", code, errb.String())
	}
	if !strings.Contains(out.String(), `"generation":1`) {
		t.Fatalf("connect reply %q", out.String())
	}
}

func TestShimConflicts(t *testing.T) {
	dir := t.TempDir()
	exe := filepath.Join(dir, "agentd")
	if err := os.WriteFile(exe, []byte("x"), 0o755); err != nil {
		t.Fatal(err)
	}
	ours, foreign, rt := filepath.Join(dir, "ours"), filepath.Join(dir, "foreign"), filepath.Join(dir, "rt")
	for _, d := range []string{ours, foreign, rt} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := linkShim(ours, exe); err != nil {
		t.Fatal(err)
	}
	if err := linkShim(ours, exe); err != nil {
		t.Fatalf("relinking our own shim: %v", err)
	}
	if err := os.WriteFile(filepath.Join(foreign, "herdr-core"), []byte("real"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := linkShim(foreign, exe); err == nil {
		t.Fatal("linkShim replaced a foreign herdr-core")
	}
	if got := shimConflicts(exe, []string{ours}, []string{rt}); len(got) != 0 {
		t.Fatalf("our own shim reported: %v", got)
	}
	got := shimConflicts(exe, []string{ours, foreign}, []string{rt})
	if len(got) != 1 || !strings.Contains(got[0], filepath.Join(foreign, "herdr-core")) {
		t.Fatalf("foreign binary: %v", got)
	}

	// A live herdr-core socket is reported; a stale one and holder sockets are not.
	l, err := net.Listen("unix", filepath.Join(rt, "default.sock"))
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	if err := os.WriteFile(filepath.Join(rt, "stale.sock"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	h, err := net.Listen("unix", filepath.Join(rt, "default.holder.abc.sock"))
	if err != nil {
		t.Fatal(err)
	}
	defer h.Close()
	got = shimConflicts(exe, []string{ours}, []string{rt})
	if len(got) != 1 || !strings.Contains(got[0], "default.sock") {
		t.Fatalf("running core: %v", got)
	}

	// agentd's own hc listener answers server.status with installed_by agentd.
	ag, err := net.Listen("unix", filepath.Join(rt, "agentd.sock"))
	if err != nil {
		t.Fatal(err)
	}
	defer ag.Close()
	go func() {
		for {
			c, err := ag.Accept()
			if err != nil {
				return
			}
			_, _ = c.Read(make([]byte, 512))
			_, _ = c.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{"installed_by":"agentd"}}` + "\n"))
			_ = c.Close()
		}
	}()
	got = shimConflicts(exe, []string{ours}, []string{rt})
	if len(got) != 1 || strings.Contains(got[0], "agentd.sock") {
		t.Fatalf("agentd's own listener reported: %v", got)
	}
}
