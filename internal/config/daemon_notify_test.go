package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestDaemonNotifyDefaultsAndLoad(t *testing.T) {
	n := Default().Notify
	if n.Enabled || strings.Join(n.On, ",") != "question,waiting,blocked,exited" || time.Duration(n.Debounce) != 30*time.Second || n.DeepLinkBase != "agentd://open" {
		t.Fatalf("defaults = %+v", n)
	}
	c, err := loadAPI(t, "[notify]\nenabled = true\nntfy_url = \"https://ntfy.sh/abc\"\non = [\"question\", \"blocked\", \"turn_done\"]\ninclude_preview = true\n")
	if err != nil {
		t.Fatal(err)
	}
	if n := c.Notify; !n.Enabled || !n.IncludePreview || len(n.On) != 3 {
		t.Fatalf("loaded = %+v", n)
	}
	// [api.notify] keeps its own defaults.
	if got := strings.Join(c.API.Notify.On, ","); got != "waiting,turn_done,exited" {
		t.Fatalf("api.notify.on = %s", got)
	}
}

func TestDaemonNotifyValidation(t *testing.T) {
	for body, want := range map[string]string{
		"enabled = true": "notify.enabled needs ntfy_url or webhook_url",
		"enabled = true\nntfy_url = \"https://ntfy.sh/a\"\non = [\"nope\"]":          "notify.on: unknown reason",
		"enabled = true\nwebhook_url = \"http://example.com/h\"":                     "notify.webhook_url must use https",
		"enabled = true\nntfy_url = \"https://ntfy.sh/a\"\nntfy_token_env = \"x y\"": "notify.ntfy_token_env",
	} {
		_, err := loadAPI(t, "[notify]\n"+body+"\n")
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%q: err = %v, want %q", body, err, want)
		}
	}
	// [api.notify] still refuses the new reasons.
	if _, err := loadAPI(t, "[api.notify]\nenabled = true\nntfy_url = \"https://ntfy.sh/a\"\non = [\"question\"]\n"); err == nil {
		t.Fatal("api.notify accepted question")
	}
}

func TestDaemonNotifyResolveSecrets(t *testing.T) {
	p := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	env := func(k string) string {
		if k == "S" {
			return "fromenv"
		}
		return ""
	}
	n := NotifyConfig{Enabled: true, WebhookSecret: "plain"}
	if err := n.ResolveSecretsFor("notify", env, p); err == nil || !strings.HasPrefix(err.Error(), "notify:") || strings.Contains(err.Error(), "plain\"") {
		t.Fatalf("open file: err = %v", err)
	}
	n = NotifyConfig{Enabled: true, WebhookSecret: "plain", WebhookSecretEnv: "S"}
	if err := os.Chmod(p, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := n.ResolveSecretsFor("notify", env, p); err != nil || n.WebhookSecret != "fromenv" {
		t.Fatalf("env must win: %v %q", err, n.WebhookSecret)
	}
	n = NotifyConfig{Enabled: true, NtfyTokenEnv: "MISSING"}
	if err := n.ResolveSecretsFor("notify", env, ""); err == nil || !strings.Contains(err.Error(), "notify.ntfy_token_env") {
		t.Fatalf("missing env: %v", err)
	}
}
