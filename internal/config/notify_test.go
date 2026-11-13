package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestNotifyDefaultsAndLoad(t *testing.T) {
	n := Default().API.Notify
	if n.Enabled || time.Duration(n.Debounce) != 30*time.Second || n.DeepLinkBase != "agentd://open" || len(n.On) != 3 || n.IncludePreview {
		t.Fatalf("defaults = %+v", n)
	}
	c, err := loadAPI(t, `[api.notify]
enabled = true
ntfy_url = "https://ntfy.sh/abc"
webhook_url = "http://127.0.0.1:9/hook"
on = ["waiting"]
debounce = "5s"
`)
	if err != nil {
		t.Fatal(err)
	}
	if n := c.API.Notify; !n.Enabled || n.NtfyURL != "https://ntfy.sh/abc" || time.Duration(n.Debounce) != 5*time.Second || len(n.On) != 1 {
		t.Fatalf("loaded = %+v", n)
	}
}

func TestNotifyValidation(t *testing.T) {
	for body, want := range map[string]string{
		"enabled = true": "needs ntfy_url or webhook_url",
		"enabled = true\nntfy_url = \"http://ntfy.example.com/x\"":                "https",
		"enabled = true\nwebhook_url = \"ftp://x.example/h\"":                     "https",
		"enabled = true\nwebhook_url = \"https://x.example/h\"\non = [\"bogus\"]": "unknown reason",
		"enabled = true\nwebhook_url = \"/relative\"":                             "valid URL",
	} {
		_, err := loadAPI(t, "[api.notify]\n"+body+"\n")
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%q: err = %v, want %q", body, err, want)
		}
	}
	for _, u := range []string{"http://localhost:8080/x", "http://127.0.0.1/x", "http://[::1]:9/x", "https://ntfy.sh/t"} {
		if err := ValidateNotifyURL("u", u); err != nil {
			t.Errorf("%s: %v", u, err)
		}
	}
	if _, err := loadAPI(t, "[api.notify]\nenabled = false\nntfy_url = \"http://bad.example\"\n"); err != nil {
		t.Fatalf("disabled config must not be validated: %v", err)
	}
}

func TestNotifyResolveSecrets(t *testing.T) {
	env := map[string]string{"NT": "from-env"}
	getenv := func(k string) string { return env[k] }
	cfgFile := func(mode os.FileMode) string {
		p := filepath.Join(t.TempDir(), "config.toml")
		if err := os.WriteFile(p, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(p, mode); err != nil {
			t.Fatal(err)
		}
		return p
	}
	// Env wins over a plain value; private file accepted.
	n := NotifyConfig{Enabled: true, NtfyToken: "plain", NtfyTokenEnv: "NT"}
	if err := n.ResolveSecrets(getenv, cfgFile(0o600)); err != nil || n.NtfyToken != "from-env" {
		t.Fatalf("env should win: %v %q", err, n.NtfyToken)
	}
	// Env only: the file permissions are irrelevant.
	n = NotifyConfig{Enabled: true, WebhookSecretEnv: "NT"}
	if err := n.ResolveSecrets(getenv, cfgFile(0o644)); err != nil || n.WebhookSecret != "from-env" {
		t.Fatalf("env only: %v %q", err, n.WebhookSecret)
	}
	// Named variable unset and no plain value: error.
	n = NotifyConfig{Enabled: true, NtfyTokenEnv: "MISSING"}
	if err := n.ResolveSecrets(getenv, ""); err == nil || !strings.Contains(err.Error(), "MISSING") {
		t.Fatalf("unset env: %v", err)
	}
	// Unset variable falls back to the plain value.
	n = NotifyConfig{Enabled: true, NtfyToken: "plain", NtfyTokenEnv: "MISSING"}
	if err := n.ResolveSecrets(getenv, cfgFile(0o600)); err != nil || n.NtfyToken != "plain" {
		t.Fatalf("fallback: %v %q", err, n.NtfyToken)
	}
	// Plain secret in a group/world-readable file: refused, secret not in the error.
	for _, mode := range []os.FileMode{0o640, 0o604, 0o644} {
		n = NotifyConfig{Enabled: true, WebhookSecret: "s3cr3t-value"}
		err := n.ResolveSecrets(getenv, cfgFile(mode))
		if err == nil || !strings.Contains(err.Error(), "insecure permissions") || strings.Contains(err.Error(), "s3cr3t-value") {
			t.Fatalf("mode %v: err = %v", mode, err)
		}
	}
	// Disabled: no-op.
	n = NotifyConfig{WebhookSecret: "x"}
	if err := n.ResolveSecrets(getenv, cfgFile(0o644)); err != nil {
		t.Fatalf("disabled: %v", err)
	}
	// Name validation.
	if _, err := loadAPI(t, "[api.notify]\nenabled = true\nntfy_url = \"https://ntfy.sh/a\"\nntfy_token_env = \"bad name\"\n"); err == nil {
		t.Fatal("invalid env name accepted")
	}
}
