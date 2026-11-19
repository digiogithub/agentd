// Package relayserver is agentd-relay: a blind rendezvous between agentd daemons
// (host links) and clients (tunnels). It authenticates hosts by their ed25519
// route key (and a join token), pairs each client tunnel with a fresh host data
// connection and forwards binary messages unchanged. Tunnel content is end-to-end
// encrypted between the client and the daemon (relaywire); the relay never sees
// keys, session names or screens. Plan: docs/plans/agentd/relay_plan.md.
package relayserver

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"
	"github.com/digiogithub/agentd/internal/relaywire"
)

// Config tunes a Server. Zero values take the defaults.
type Config struct {
	// JoinTokens admit hosts; empty with Open false = no host can register.
	JoinTokens []string
	// Open lets any host register (no join token).
	Open bool
	// MaxTunnels per route (default 64).
	MaxTunnels int
	// MaxPending connects per route waiting for the host (default 16).
	MaxPending int
	// AcceptTimeout: how long a client waits for the host's data connection (default 10 s).
	AcceptTimeout time.Duration
	// ConnectPerMinute: client connects per remote IP (default 30).
	ConnectPerMinute int
	// TrustForwarded takes the client IP from X-Forwarded-For (behind a reverse proxy).
	TrustForwarded bool
	// PingInterval of host links and tunnels (default 30 s).
	PingInterval time.Duration
	Log          *slog.Logger
}

// Server is the relay.
type Server struct {
	cfg Config
	log *slog.Logger

	mu      sync.Mutex
	hosts   map[string]*hostLink
	pending map[string]*pendingTunnel
	limiter *ipLimiter
}

type hostLink struct {
	route   string
	c       *websocket.Conn
	wmu     sync.Mutex
	tunnels int
	waiting int
}

type pendingTunnel struct {
	route string
	ch    chan *websocket.Conn
	ended chan struct{} // closed when the tunnel is over (keeps the accept handler alive)
}

// New builds a Server.
func New(cfg Config) *Server {
	if cfg.MaxTunnels <= 0 {
		cfg.MaxTunnels = 64
	}
	if cfg.MaxPending <= 0 {
		cfg.MaxPending = 16
	}
	if cfg.AcceptTimeout <= 0 {
		cfg.AcceptTimeout = 10 * time.Second
	}
	if cfg.ConnectPerMinute <= 0 {
		cfg.ConnectPerMinute = 30
	}
	if cfg.PingInterval <= 0 {
		cfg.PingInterval = 30 * time.Second
	}
	if cfg.Log == nil {
		cfg.Log = slog.New(slog.DiscardHandler)
	}
	return &Server{
		cfg:     cfg,
		log:     cfg.Log,
		hosts:   map[string]*hostLink{},
		pending: map[string]*pendingTunnel{},
		limiter: newIPLimiter(cfg.ConnectPerMinute),
	}
}

// Handler returns the HTTP handler of every endpoint.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("ok\n")) })
	mux.HandleFunc("GET /v1/host", s.handleHost)
	mux.HandleFunc("GET /v1/accept", s.handleAccept)
	mux.HandleFunc("GET /v1/connect", s.handleConnect)
	return mux
}

// Routes reports the number of registered hosts (tests, status).
func (s *Server) Routes() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.hosts)
}

func accept(w http.ResponseWriter, r *http.Request) (*websocket.Conn, error) {
	c, err := websocket.Accept(w, r, &websocket.AcceptOptions{Subprotocols: []string{relaywire.Subprotocol}})
	if err != nil {
		return nil, err
	}
	if c.Subprotocol() != relaywire.Subprotocol {
		_ = c.Close(websocket.StatusPolicyViolation, "subprotocol "+relaywire.Subprotocol+" required")
		return nil, errors.New("missing subprotocol")
	}
	c.SetReadLimit(relaywire.ReadLimit)
	return c, nil
}

type ctrlMsg struct {
	T         string `json:"t"`
	Nonce     string `json:"nonce,omitempty"`
	Pub       string `json:"pub,omitempty"`
	Sig       string `json:"sig,omitempty"`
	JoinToken string `json:"join_token,omitempty"`
	Route     string `json:"route,omitempty"`
	Tunnel    string `json:"tunnel,omitempty"`
	Error     string `json:"error,omitempty"`
}

func writeCtrl(ctx context.Context, c *websocket.Conn, m ctrlMsg) error {
	b, _ := json.Marshal(m)
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	return c.Write(ctx, websocket.MessageText, b)
}

func (s *Server) joinAllowed(tok string) bool {
	if s.cfg.Open {
		return true
	}
	ok := false
	for _, t := range s.cfg.JoinTokens {
		if t != "" && subtle.ConstantTimeCompare([]byte(t), []byte(tok)) == 1 {
			ok = true
		}
	}
	return ok
}

func (s *Server) handleHost(w http.ResponseWriter, r *http.Request) {
	ip := s.clientIP(r)
	if !s.limiter.allow("host:" + ip) {
		http.Error(w, "rate limited", http.StatusTooManyRequests)
		return
	}
	c, err := accept(w, r)
	if err != nil {
		return
	}
	defer func() { _ = c.CloseNow() }()
	ctx := r.Context()

	nonce := make([]byte, 32)
	_, _ = rand.Read(nonce)
	if err := writeCtrl(ctx, c, ctrlMsg{T: "challenge", Nonce: relaywire.B64.EncodeToString(nonce)}); err != nil {
		return
	}
	rctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	typ, b, err := c.Read(rctx)
	cancel()
	if err != nil || typ != websocket.MessageText {
		return
	}
	var reg ctrlMsg
	if json.Unmarshal(b, &reg) != nil || reg.T != "register" {
		_ = c.Close(websocket.StatusPolicyViolation, "register expected")
		return
	}
	fail := func(msg string) {
		_ = writeCtrl(ctx, c, ctrlMsg{T: "error", Error: msg})
		_ = c.Close(websocket.StatusPolicyViolation, msg)
		s.log.Info("host refused", "ip", ip, "reason", msg)
	}
	if !s.joinAllowed(reg.JoinToken) {
		fail("join token refused")
		return
	}
	pub, err1 := relaywire.B64.DecodeString(reg.Pub)
	sig, err2 := relaywire.B64.DecodeString(reg.Sig)
	if err1 != nil || err2 != nil || len(pub) != ed25519.PublicKeySize ||
		!ed25519.Verify(pub, relaywire.RegisterMessage(nonce), sig) {
		fail("bad signature")
		return
	}
	route := relaywire.RouteID(pub)
	link := &hostLink{route: route, c: c}
	s.mu.Lock()
	old := s.hosts[route]
	s.hosts[route] = link
	s.mu.Unlock()
	if old != nil {
		_ = old.c.Close(websocket.StatusGoingAway, "replaced by a new link")
	}
	if err := writeCtrl(ctx, c, ctrlMsg{T: "ok", Route: route}); err != nil {
		s.dropHost(link)
		return
	}
	s.log.Info("host linked", "route", route, "ip", ip)

	kctx, kcancel := context.WithCancel(ctx)
	go relaywire.KeepAlive(kctx, c, s.cfg.PingInterval)
	// The host sends nothing after registering; reading processes pings/close.
	for {
		if _, _, err := c.Read(ctx); err != nil {
			break
		}
	}
	kcancel()
	s.dropHost(link)
	s.log.Info("host unlinked", "route", route)
}

func (s *Server) dropHost(link *hostLink) {
	s.mu.Lock()
	if s.hosts[link.route] == link {
		delete(s.hosts, link.route)
	}
	s.mu.Unlock()
}

func (s *Server) handleAccept(w http.ResponseWriter, r *http.Request) {
	id := r.URL.Query().Get("tunnel")
	s.mu.Lock()
	p := s.pending[id]
	delete(s.pending, id)
	s.mu.Unlock()
	if p == nil || id == "" {
		http.Error(w, "unknown tunnel", http.StatusNotFound)
		return
	}
	c, err := accept(w, r)
	if err != nil {
		close(p.ch)
		return
	}
	p.ch <- c // buffered; the connect handler owns c from now on
	<-p.ended
	_ = c.CloseNow() // also covers a connection handed over after the client gave up
}

func (s *Server) handleConnect(w http.ResponseWriter, r *http.Request) {
	ip := s.clientIP(r)
	route := r.URL.Query().Get("route")
	if !relaywire.ValidRoute(route) {
		http.Error(w, "bad route", http.StatusBadRequest)
		return
	}
	limited := !s.limiter.allow("connect:" + ip)
	c, err := accept(w, r)
	if err != nil {
		return
	}
	defer func() { _ = c.CloseNow() }()
	if limited {
		_ = c.Close(relaywire.StatusRateLimited, "rate limited")
		return
	}

	s.mu.Lock()
	link := s.hosts[route]
	switch {
	case link == nil:
		s.mu.Unlock()
		_ = c.Close(relaywire.StatusHostOffline, "host offline")
		return
	case link.tunnels >= s.cfg.MaxTunnels || link.waiting >= s.cfg.MaxPending:
		s.mu.Unlock()
		_ = c.Close(relaywire.StatusTooManyTunnel, "too many tunnels")
		return
	}
	idb := make([]byte, 16)
	_, _ = rand.Read(idb)
	id := relaywire.B64.EncodeToString(idb)
	p := &pendingTunnel{route: route, ch: make(chan *websocket.Conn, 1), ended: make(chan struct{})}
	defer close(p.ended)
	s.pending[id] = p
	link.waiting++
	s.mu.Unlock()

	link.wmu.Lock()
	err = writeCtrl(r.Context(), link.c, ctrlMsg{T: "incoming", Tunnel: id})
	link.wmu.Unlock()

	var hc *websocket.Conn
	if err == nil {
		t := time.NewTimer(s.cfg.AcceptTimeout)
		select {
		case hc = <-p.ch:
		case <-t.C:
		case <-r.Context().Done():
		}
		t.Stop()
	}
	s.mu.Lock()
	delete(s.pending, id)
	link.waiting--
	if hc != nil {
		link.tunnels++
	}
	s.mu.Unlock()
	if hc == nil {
		_ = c.Close(relaywire.StatusHostNoAnswer, "host did not answer")
		return
	}
	defer func() {
		s.mu.Lock()
		link.tunnels--
		s.mu.Unlock()
	}()
	defer func() { _ = hc.CloseNow() }()
	s.splice(r.Context(), c, hc)
}

// splice forwards messages both ways until either side ends.
func (s *Server) splice(ctx context.Context, a, b *websocket.Conn) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	go relaywire.KeepAlive(ctx, a, s.cfg.PingInterval)
	go relaywire.KeepAlive(ctx, b, s.cfg.PingInterval)
	done := make(chan struct{}, 2)
	pump := func(dst, src *websocket.Conn) {
		defer func() { done <- struct{}{} }()
		for {
			typ, msg, err := src.Read(ctx)
			if err != nil {
				return
			}
			wctx, wcancel := context.WithTimeout(ctx, 30*time.Second)
			err = dst.Write(wctx, typ, msg)
			wcancel()
			if err != nil {
				return
			}
		}
	}
	go pump(a, b)
	go pump(b, a)
	<-done
	cancel()
	_ = a.Close(websocket.StatusNormalClosure, "")
	_ = b.Close(websocket.StatusNormalClosure, "")
	<-done
}

func (s *Server) clientIP(r *http.Request) string {
	if s.cfg.TrustForwarded {
		if f := r.Header.Get("X-Forwarded-For"); f != "" {
			if i := strings.IndexByte(f, ','); i >= 0 {
				f = f[:i]
			}
			return strings.TrimSpace(f)
		}
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// ipLimiter is a fixed-window per-key counter.
type ipLimiter struct {
	mu     sync.Mutex
	limit  int
	window time.Time
	counts map[string]int
}

func newIPLimiter(perMinute int) *ipLimiter {
	return &ipLimiter{limit: perMinute, counts: map[string]int{}}
}

func (l *ipLimiter) allow(key string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := time.Now()
	if now.Sub(l.window) >= time.Minute {
		l.window = now
		clear(l.counts)
	}
	l.counts[key]++
	return l.counts[key] <= l.limit
}
