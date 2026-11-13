package config

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

func loadAPI(t *testing.T, body string) (*Config, error) {
	t.Helper()
	p := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return Load(p)
}

func TestAPIDefaults(t *testing.T) {
	c := Default()
	a := c.API
	if a.Listen != "127.0.0.1:7477" || a.TLS != "off" || a.MaxBodyKB != 256 ||
		time.Duration(a.SSEHeartbeat) != 15*time.Second || a.EventBuffer != 1024 ||
		time.Duration(a.IdempotencyTTL) != 10*time.Minute || a.RunEnv != "daemon" {
		t.Fatalf("defaults = %+v", a)
	}
	if slices.Contains(a.RunHarnesses, "default") || !slices.Contains(a.RunHarnesses, "claude") {
		t.Fatalf("run_harnesses = %v", a.RunHarnesses)
	}
}

func TestAPILoad(t *testing.T) {
	c, err := loadAPI(t, `[api]
listen = "0.0.0.0:9000"
tls = "files"
tls_cert = "c.pem"
tls_key = "k.pem"
cors_origins = ["https://x.example"]
allowed_hosts = ["h.example"]
max_body_kb = 8
sse_heartbeat = "5s"
run_harnesses = ["codex"]
`)
	if err != nil {
		t.Fatal(err)
	}
	if c.API.Listen != "0.0.0.0:9000" || c.API.MaxBodyKB != 8 || time.Duration(c.API.SSEHeartbeat) != 5*time.Second ||
		!slices.Equal(c.API.RunHarnesses, []string{"codex"}) || c.API.EventBuffer != 1024 {
		t.Fatalf("api = %+v", c.API)
	}
}

func TestAPIInvalid(t *testing.T) {
	for name, tc := range map[string]struct{ body, want string }{
		"non-loopback no tls": {"[api]\nlisten = \"0.0.0.0:7477\"\n", "not loopback"},
		"empty host no tls":   {"[api]\nlisten = \":7477\"\n", "not loopback"},
		"lan ip no tls":       {"[api]\nlisten = \"192.168.1.5:7477\"\ntls = \"off\"\n", "not loopback"},
		"bad tls":             {"[api]\ntls = \"maybe\"\n", "api.tls"},
		"files w/o cert":      {"[api]\ntls = \"files\"\n", "tls_cert"},
		"bad listen":          {"[api]\nlisten = \"nope\"\n", "api.listen"},
		"bad run_env":         {"[api]\nrun_env = \"gateway\"\n", "run_env"},
		"unknown harness":     {"[api]\nrun_harnesses = [\"nope\"]\n", "unknown harness"},
		"tiny heartbeat":      {"[api]\nsse_heartbeat = \"10ms\"\n", "sse_heartbeat"},
		"unknown key":         {"[api]\nfoo = 1\n", "unknown keys"},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := loadAPI(t, tc.body)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want %q", err, tc.want)
			}
		})
	}
	// Non-loopback with TLS is fine.
	if _, err := loadAPI(t, "[api]\nlisten = \"0.0.0.0:7477\"\ntls = \"self-signed\"\n"); err != nil {
		t.Fatal(err)
	}
}

func TestIsLoopbackAddr(t *testing.T) {
	for addr, want := range map[string]bool{
		"127.0.0.1:1": true, "[::1]:1": true, "localhost:1": true, "127.5.5.5:80": true,
		"0.0.0.0:1": false, ":1": false, "[::]:1": false, "10.0.0.1:1": false, "example.com:1": false, "bad": false,
	} {
		if got := IsLoopbackAddr(addr); got != want {
			t.Errorf("IsLoopbackAddr(%q) = %v", addr, got)
		}
	}
}
