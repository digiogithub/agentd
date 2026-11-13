// Package httpapi is the HTTP/JSON gateway in front of the agentd daemon
// (`agentd serve`, docs/design/api/http-api.md). It is a thin translator, like
// internal/mcpserver: it talks to the daemon only through internal/client and
// internal/proto and never imports the daemon, session or config packages
// (config is parsed by cmd/agentd and passed in as Config).
package httpapi

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/digiogithub/agentd/internal/client"
)

// APIVersion is the HTTP API major version, reported by GET /v1/info
// (ADR-A16).
const APIVersion = "1"

// ShutdownTimeout bounds http.Server.Shutdown (http-api 1).
const ShutdownTimeout = 5 * time.Second

// PromptMaxChars is the longest act prompt (in characters) the gateway
// accepts and advertises. The daemon limit proto.MaxActText is in bytes
// (64 KiB); 16000 runes are at most 64000 bytes, so the two stay consistent.
const PromptMaxChars = 16000

// Config is the gateway's own view of the [api] settings; cmd/agentd maps
// config.APIConfig onto it.
type Config struct {
	// Version is the gateway (agentd) version.
	Version string
	// CORSOrigins are exact origins allowed for browsers; empty = none.
	CORSOrigins []string
	// AllowedHosts are extra accepted Host header values. The Host allow-list
	// always holds localhost, 127.0.0.1, [::1], the bind address (every local
	// interface address for a wildcard bind), the machine hostname and
	// TLSHosts; any other Host is answered 421 before authentication.
	AllowedHosts []string
	// BindAddr is the "host:port" the gateway listens on (Host allow-list).
	BindAddr string
	// TLSHosts are the extra names of the TLS certificate (api.tls_hosts).
	TLSHosts []string
	// Hostname overrides os.Hostname (tests).
	Hostname string
	// RateLimit / RateBurst: per-device token bucket, requests per second
	// (default 20, burst 40). ActRateLimit: POST .../act (default 5, burst 10).
	RateLimit    int
	RateBurst    int
	ActRateLimit int
	// MaxStreamsPerDevice / MaxStreams cap concurrent SSE streams (8 / 64).
	MaxStreamsPerDevice int
	MaxStreams          int
	// MaxBodyKB caps request bodies (default 256).
	MaxBodyKB int
	// SSEHeartbeat, EventBuffer, IdempotencyTTL are consumed by the stories
	// that add streams and idempotent writes.
	SSEHeartbeat   time.Duration
	EventBuffer    int
	IdempotencyTTL time.Duration
	// RunHarnesses are the harnesses POST /v1/sessions accepts with write.
	RunHarnesses []string
	// Notify configures ntfy / webhook notifications ([api.notify]).
	Notify NotifyConfig
}

func (c Config) maxBody() int64 {
	if c.MaxBodyKB <= 0 {
		return 256 << 10
	}
	return int64(c.MaxBodyKB) << 10
}

// Server is the HTTP gateway.
type Server struct {
	Config Config
	// Dial connects to the daemon for explicit requests (the CLI passes an
	// autostarting dialer unless api.autostart_daemon = false).
	Dial func() (*client.Client, error)
	// HubDial is the dialer of the background event subscription. It must
	// not start the daemon: only an explicit request may. nil = Dial.
	HubDial func() (*client.Client, error)
	// Guard, when set, wraps the handler inside the request-id / access-log /
	// recover chain (cmd/agentd installs the device-token guard here, so its
	// rejections are logged with a request id).
	Guard func(http.Handler) http.Handler
	// Audit receives the audit trail (nil = none).
	Audit Auditor
	// Auth authenticates every route above ScopeNone. nil = DenyAll: only
	// /v1/health is reachable.
	Auth Authenticator
	// Log defaults to a discard logger.
	Log *slog.Logger

	once       sync.Once
	pool       *pool
	handler    http.Handler
	routes     []Route
	extra      []Route // appended by tests / later stories
	draining   chan struct{}
	drainMu    sync.Once
	hub        *hub
	notify     *notifier
	notifyOnce sync.Once
	sseQueue   int // per-stream event queue size; 0 = default (tests shrink it)

	idem       *idemStore
	actTimeout time.Duration // 0 = ActTimeout (tests shrink it)

	health  healthCache
	limits  *limiter
	streams streamCounter
	hosts   map[string]bool
}

func (s *Server) init() {
	s.once.Do(func() {
		if s.Log == nil {
			s.Log = slog.New(slog.DiscardHandler)
		}
		if s.Auth == nil {
			s.Auth = DenyAll{}
		}
		s.pool = &pool{dial: s.Dial, sem: make(chan struct{}, maxInflight)}
		s.limits = newLimiter(s.Config)
		s.hosts = s.buildHostSet()
		s.draining = make(chan struct{})
		s.hub = newHub(s)
		s.routes = append(s.baseRoutes(), s.extra...)
		s.handler = s.buildHandler()
	})
}

// Handler returns the full handler (middleware chain + routes).
func (s *Server) Handler() http.Handler {
	s.init()
	return s.handler
}

// Routes returns the routing table, for contract tests against the OpenAPI
// document.
func (s *Server) Routes() []Route {
	s.init()
	return append([]Route(nil), s.routes...)
}

// Draining is closed when shutdown starts: streaming handlers must send an
// `event: bye` and return.
func (s *Server) Draining() <-chan struct{} {
	s.init()
	return s.draining
}

// Serve serves on ln until ctx is done, then drains (Draining closes so SSE
// streams say bye) and shuts down within ShutdownTimeout. It closes the
// daemon connections but never touches the daemon itself. A nil return means
// a clean shutdown.
func (s *Server) Serve(ctx context.Context, ln net.Listener) error {
	s.init()
	s.startNotifier()
	hs := s.httpServer(ctx)
	errc := make(chan error, 1)
	go func() { errc <- hs.Serve(ln) }()
	var err error
	select {
	case err = <-errc: // listener failed
		s.drainMu.Do(func() { close(s.draining) })
		s.hub.wait()
		s.waitNotifier()
		s.pool.close()
		return err
	case <-ctx.Done():
	}
	s.drainMu.Do(func() { close(s.draining) })
	sctx, cancel := context.WithTimeout(context.Background(), ShutdownTimeout)
	defer cancel()
	if err := hs.Shutdown(sctx); err != nil {
		s.Log.Warn("shutdown timed out, closing connections", "err", err)
		_ = hs.Close()
	}
	s.pool.close()
	s.hub.wait()
	s.waitNotifier()
	if err := <-errc; err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

// httpServer is the net/http server: slow-header and idle-connection limits
// and a small header cap (no read/write timeout: SSE streams and the pairing
// long poll are long-lived by design).
func (s *Server) httpServer(ctx context.Context) *http.Server {
	return &http.Server{
		Handler:           s.handler,
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       120 * time.Second,
		MaxHeaderBytes:    16 << 10,
		ErrorLog:          slog.NewLogLogger(s.Log.Handler(), slog.LevelWarn),
		BaseContext:       func(net.Listener) context.Context { return context.WithoutCancel(ctx) },
	}
}

func (s *Server) actDeadline() time.Duration {
	if s.actTimeout > 0 {
		return s.actTimeout
	}
	return ActTimeout
}

// Close starts the drain (open streams say bye), stops the daemon event
// subscription and closes the idle daemon connections. Serve does this on its
// own; Close is for callers that use Handler directly (tests).
func (s *Server) Close() {
	s.init()
	s.drainMu.Do(func() { close(s.draining) })
	s.hub.wait()
	s.waitNotifier()
	s.pool.close()
}
