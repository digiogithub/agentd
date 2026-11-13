package config

import (
	"fmt"
	"net"
	"net/url"
	"os"
	"regexp"
	"strings"
	"time"
)

// Defaults of the [api] table.
const (
	DefaultAPIListen       = "127.0.0.1:7477"
	DefaultAPIMaxBodyKB    = 256
	DefaultAPIHeartbeat    = 15 * time.Second
	DefaultAPIEventBuffer  = 1024
	DefaultAPIIdempotency  = 10 * time.Minute
	DefaultAPIRate         = 20
	DefaultAPIRateBurst    = 40
	DefaultAPIActRate      = 5
	DefaultAPIStreamsDev   = 8
	DefaultAPIStreamsTotal = 64
	DefaultAPIAuditMaxMB   = 10
	APITLSOff              = "off"
	APITLSSelfSigned       = "self-signed"
	APITLSFiles            = "files"
	APIRunEnvDaemon        = "daemon"
	defaultProfileName     = "default"
	minAPISSEHeartbeat     = time.Second
	maxAPIEventBufferLimit = 1 << 20
)

// APIConfig is the [api] table of config.toml: the HTTP gateway (`agentd
// serve`). Zero values are replaced by the defaults when the config is
// loaded (Config.init), so a loaded Config always has a complete API.
type APIConfig struct {
	// Listen is the TCP address ("host:port"). A non-loopback host requires
	// TLS (ADR-A08); there is no override.
	Listen string `toml:"listen"`
	// TLS is "off" (default), "self-signed" or "files" (TLSCert + TLSKey).
	TLS     string `toml:"tls"`
	TLSCert string `toml:"tls_cert"`
	TLSKey  string `toml:"tls_key"`
	// TLSHosts are extra SANs (DNS names or IPs) of the self-signed
	// certificate, e.g. a MagicDNS name or a public hostname. Changing them
	// takes effect on `agentd serve tls rotate`.
	TLSHosts []string `toml:"tls_hosts"`
	// CORSOrigins are exact origins allowed for browser clients; empty =
	// same-origin only.
	CORSOrigins []string `toml:"cors_origins"`
	// AllowedHosts restricts the Host header (DNS-rebinding defence); empty
	// = any host.
	AllowedHosts []string `toml:"allowed_hosts"`
	// MaxBodyKB caps request bodies (413 above it).
	MaxBodyKB int `toml:"max_body_kb"`
	// SSEHeartbeat is the idle interval of SSE comment lines.
	SSEHeartbeat Duration `toml:"sse_heartbeat"`
	// EventBuffer is the number of events kept for Last-Event-ID replay.
	EventBuffer int `toml:"event_buffer"`
	// IdempotencyTTL is how long Idempotency-Key results are kept.
	IdempotencyTTL Duration `toml:"idempotency_ttl"`
	// RunHarnesses are the harness names POST /v1/sessions accepts with the
	// write scope; default: every built-in profile except "default".
	RunHarnesses []string `toml:"run_harnesses"`
	// RunEnv is the env base of API-started sessions; only "daemon".
	RunEnv string `toml:"run_env"`
	// Pairing mounts POST /v1/pair (QR pairing) when true (default). Set
	// false to only allow `agentd serve devices add`.
	Pairing *bool `toml:"pairing"`
	// AutostartDaemon lets an explicit API request start a stopped daemon
	// (default true). The background event hub never starts it.
	AutostartDaemon *bool `toml:"autostart_daemon"`
	// RateLimit / RateBurst: per-device token bucket (requests per second,
	// burst). ActRateLimit is the stricter bucket of POST .../act.
	RateLimit    int `toml:"rate_limit"`
	RateBurst    int `toml:"rate_burst"`
	ActRateLimit int `toml:"act_rate_limit"`
	// MaxStreamsPerDevice / MaxStreams cap concurrent SSE streams.
	MaxStreamsPerDevice int `toml:"max_streams_per_device"`
	MaxStreams          int `toml:"max_streams"`
	// AuditMaxMB caps <state>/api/audit.log before one rotation (.1).
	AuditMaxMB int `toml:"audit_max_mb"`
	// Notify is the [api.notify] table (ntfy / webhook notifications).
	Notify NotifyConfig `toml:"notify"`
}

// Defaults of [api.notify].
const (
	DefaultNotifyDebounce     = 30 * time.Second
	DefaultNotifyDeepLinkBase = "agentd://open"
)

// NotifyConfig is [api.notify]: push-style notifications from the gateway.
// Secrets are never logged. Prefer ntfy_token_env / webhook_secret_env (the
// name of an environment variable read at serve start; it wins over the plain
// value). A plain secret in config.toml requires the file to be private
// (no group/other permission bits, owned by the user): see ResolveSecrets.
type NotifyConfig struct {
	Enabled bool `toml:"enabled"`
	// NtfyURL is the topic URL (https://ntfy.sh/<topic> or self-hosted);
	// NtfyToken is an optional Bearer token.
	NtfyURL   string `toml:"ntfy_url"`
	NtfyToken string `toml:"ntfy_token"`
	// NtfyTokenEnv names the environment variable holding the token.
	NtfyTokenEnv string `toml:"ntfy_token_env"`
	// WebhookURL receives a JSON POST; WebhookSecret signs it
	// (X-Agentd-Signature: sha256=<hex HMAC of the body>).
	WebhookURL    string `toml:"webhook_url"`
	WebhookSecret string `toml:"webhook_secret"`
	// WebhookSecretEnv names the environment variable holding the secret.
	WebhookSecretEnv string `toml:"webhook_secret_env"`
	// On lists the reasons that notify: waiting, turn_done, exited.
	On []string `toml:"on"`
	// Debounce is the minimum gap per session and reason.
	Debounce Duration `toml:"debounce"`
	// IncludePreview adds the session's preview line (default false: no
	// terminal text leaves the host).
	IncludePreview bool `toml:"include_preview"`
	// HostLabel names the host in payloads (default: hostname).
	HostLabel string `toml:"host_label"`
	// DeepLinkBase is the app deep link base (default agentd://open).
	DeepLinkBase string `toml:"deep_link_base"`
}

var notifyReasons = map[string]bool{"waiting": true, "turn_done": true, "exited": true}

func (n *NotifyConfig) applyDefaults() {
	if n.On == nil {
		n.On = []string{"waiting", "turn_done", "exited"}
	}
	if n.Debounce == 0 {
		n.Debounce = Duration(DefaultNotifyDebounce)
	}
	if n.DeepLinkBase == "" {
		n.DeepLinkBase = DefaultNotifyDeepLinkBase
	}
}

// ValidateNotifyURL requires https, or http only for a loopback host.
func ValidateNotifyURL(name, raw string) error {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return fmt.Errorf("api.notify.%s is not a valid URL", name)
	}
	switch u.Scheme {
	case "https":
	case "http":
		if !IsLoopbackAddr(net.JoinHostPort(u.Hostname(), "0")) {
			return fmt.Errorf("api.notify.%s must use https (http is only allowed for loopback hosts)", name)
		}
	default:
		return fmt.Errorf("api.notify.%s must be an https URL", name)
	}
	return nil
}

func (n NotifyConfig) validate() error {
	if !n.Enabled {
		return nil
	}
	if n.NtfyURL == "" && n.WebhookURL == "" {
		return fmt.Errorf("api.notify.enabled needs ntfy_url or webhook_url")
	}
	for name, v := range map[string]string{"ntfy_url": n.NtfyURL, "webhook_url": n.WebhookURL} {
		if v != "" {
			if err := ValidateNotifyURL(name, v); err != nil {
				return err
			}
		}
	}
	for name, v := range map[string]string{"ntfy_token_env": n.NtfyTokenEnv, "webhook_secret_env": n.WebhookSecretEnv} {
		if v != "" && !envNameRe.MatchString(v) {
			return fmt.Errorf("api.notify.%s %q is not a valid environment variable name", name, v)
		}
	}
	for _, r := range n.On {
		if !notifyReasons[r] {
			return fmt.Errorf("api.notify.on: unknown reason %q (waiting, turn_done, exited)", r)
		}
	}
	if time.Duration(n.Debounce) < 0 {
		return fmt.Errorf("api.notify.debounce must be >= 0")
	}
	return nil
}

// PairingEnabled reports [api] pairing (default true).
func (a APIConfig) PairingEnabled() bool { return a.Pairing == nil || *a.Pairing }

// AutostartEnabled reports [api] autostart_daemon (default true).
func (a APIConfig) AutostartEnabled() bool { return a.AutostartDaemon == nil || *a.AutostartDaemon }

// applyDefaults fills every unset field.
func (a *APIConfig) applyDefaults() {
	if a.Listen == "" {
		a.Listen = DefaultAPIListen
	}
	if a.TLS == "" {
		a.TLS = APITLSOff
	}
	if a.MaxBodyKB == 0 {
		a.MaxBodyKB = DefaultAPIMaxBodyKB
	}
	if a.SSEHeartbeat == 0 {
		a.SSEHeartbeat = Duration(DefaultAPIHeartbeat)
	}
	if a.EventBuffer == 0 {
		a.EventBuffer = DefaultAPIEventBuffer
	}
	if a.IdempotencyTTL == 0 {
		a.IdempotencyTTL = Duration(DefaultAPIIdempotency)
	}
	if a.RunEnv == "" {
		a.RunEnv = APIRunEnvDaemon
	}
	t := true
	if a.Pairing == nil {
		a.Pairing = &t
	}
	if a.AutostartDaemon == nil {
		a.AutostartDaemon = &t
	}
	for p, d := range map[*int]int{&a.RateLimit: DefaultAPIRate, &a.RateBurst: DefaultAPIRateBurst, &a.ActRateLimit: DefaultAPIActRate,
		&a.MaxStreamsPerDevice: DefaultAPIStreamsDev, &a.MaxStreams: DefaultAPIStreamsTotal, &a.AuditMaxMB: DefaultAPIAuditMaxMB} {
		if *p == 0 {
			*p = d
		}
	}
	a.Notify.applyDefaults()
	if a.RunHarnesses == nil {
		for _, n := range BuiltinProfileNames() {
			if n != defaultProfileName {
				a.RunHarnesses = append(a.RunHarnesses, n)
			}
		}
	}
}

// IsLoopbackAddr reports whether a "host:port" listen address binds only
// loopback interfaces. An empty host (":7477") binds every interface.
func IsLoopbackAddr(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return false
	}
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(strings.Trim(host, "[]"))
	return ip != nil && ip.IsLoopback()
}

// Validate checks the (defaulted) settings. It is exported because `agentd
// serve --listen` overrides Listen after loading and must re-validate.
func (a APIConfig) Validate() error {
	host, port, err := net.SplitHostPort(a.Listen)
	if err != nil {
		return fmt.Errorf("api.listen %q: %w", a.Listen, err)
	}
	if port == "" {
		return fmt.Errorf("api.listen %q: missing port", a.Listen)
	}
	_ = host
	switch a.TLS {
	case APITLSOff, APITLSSelfSigned:
	case APITLSFiles:
		if a.TLSCert == "" || a.TLSKey == "" {
			return fmt.Errorf("api.tls = %q needs tls_cert and tls_key", APITLSFiles)
		}
	default:
		return fmt.Errorf("api.tls must be off, self-signed or files, got %q", a.TLS)
	}
	if a.TLS == APITLSOff && !IsLoopbackAddr(a.Listen) {
		return fmt.Errorf("api.listen %q is not loopback: refusing to serve without TLS (set api.tls = \"self-signed\" or \"files\")", a.Listen)
	}
	if a.MaxBodyKB < 0 {
		return fmt.Errorf("api.max_body_kb must be > 0, got %d", a.MaxBodyKB)
	}
	if time.Duration(a.SSEHeartbeat) < minAPISSEHeartbeat {
		return fmt.Errorf("api.sse_heartbeat must be >= %s, got %s", minAPISSEHeartbeat, time.Duration(a.SSEHeartbeat))
	}
	if a.EventBuffer < 0 || a.EventBuffer > maxAPIEventBufferLimit {
		return fmt.Errorf("api.event_buffer must be in 1..%d, got %d", maxAPIEventBufferLimit, a.EventBuffer)
	}
	if time.Duration(a.IdempotencyTTL) < 0 {
		return fmt.Errorf("api.idempotency_ttl must be positive")
	}
	for name, v := range map[string]int{"rate_limit": a.RateLimit, "rate_burst": a.RateBurst, "act_rate_limit": a.ActRateLimit,
		"max_streams_per_device": a.MaxStreamsPerDevice, "max_streams": a.MaxStreams, "audit_max_mb": a.AuditMaxMB} {
		if v < 0 {
			return fmt.Errorf("api.%s must be >= 0, got %d", name, v)
		}
	}
	if err := a.Notify.validate(); err != nil {
		return err
	}
	if a.RunEnv != APIRunEnvDaemon {
		return fmt.Errorf("api.run_env must be %q, got %q", APIRunEnvDaemon, a.RunEnv)
	}
	return nil
}

// validateAPIHarnesses checks run_harnesses against the known profiles.
func (c *Config) validateAPIHarnesses() error {
	for _, h := range c.API.RunHarnesses {
		if h == defaultProfileName || !c.HasProfile(h) {
			return fmt.Errorf("api.run_harnesses: unknown harness %q", h)
		}
	}
	return nil
}

var envNameRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// ResolveSecrets fills NtfyToken / WebhookSecret from the environment variables
// named by ntfy_token_env / webhook_secret_env (getenv, normally os.Getenv; the
// environment wins when both are set). A named variable that is unset or empty
// is an error unless a plain value is configured. When a plain secret is
// present in config.toml (cfgPath, "" = none, e.g. no file) the file must not
// be readable by group/other and must be owned by the current user; otherwise
// an error is returned and notifications must not start. A no-op when
// notifications are disabled. Secret values are never part of an error.
func (n *NotifyConfig) ResolveSecrets(getenv func(string) string, cfgPath string) error {
	if !n.Enabled {
		return nil
	}
	if cfgPath != "" && (n.NtfyToken != "" || n.WebhookSecret != "") {
		if err := checkPrivateFile(cfgPath); err != nil {
			return fmt.Errorf("api.notify: plain secret in config.toml: %w (chmod 600 it, or use ntfy_token_env / webhook_secret_env)", err)
		}
	}
	if err := resolveEnvSecret("ntfy_token", n.NtfyTokenEnv, &n.NtfyToken, getenv); err != nil {
		return err
	}
	return resolveEnvSecret("webhook_secret", n.WebhookSecretEnv, &n.WebhookSecret, getenv)
}

func resolveEnvSecret(key, env string, dst *string, getenv func(string) string) error {
	if env == "" {
		return nil
	}
	if v := getenv(env); v != "" {
		*dst = v
		return nil
	}
	if *dst == "" {
		return fmt.Errorf("api.notify.%s_env: environment variable %s is not set", key, env)
	}
	return nil
}

// checkPrivateFile verifies that path is a regular file, owned by the current
// user and without group/other permission bits.
func checkPrivateFile(path string) error {
	fi, err := os.Stat(path)
	if err != nil {
		return err
	}
	if !fi.Mode().IsRegular() {
		return fmt.Errorf("%s is not a regular file", path)
	}
	if fi.Mode().Perm()&0o077 != 0 {
		return fmt.Errorf("%s has insecure permissions %v (want 0600)", path, fi.Mode().Perm())
	}
	if uid, ok := ownerUID(fi); ok && uid != os.Getuid() {
		return fmt.Errorf("%s is owned by uid %d, not %d", path, uid, os.Getuid())
	}
	return nil
}
