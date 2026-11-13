package httpapi

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/digiogithub/agentd/internal/client"
)

const (
	maxIdleConns = 4
	// callTimeout bounds a read call to the daemon.
	callTimeout = 10 * time.Second
	// writeTimeout bounds a side-effecting call (run, kill, rm, send, reply).
	writeTimeout = 15 * time.Second
	// ActTimeout bounds POST .../act. The daemon's own act budget is at most
	// actLockWait (10 s) plus a few 3 s key steps and a 250 ms settle; the
	// gateway deadline must stay above it, or the gateway would give up on an
	// act the daemon is still performing.
	ActTimeout = 40 * time.Second
	// maxInflight caps concurrent daemon calls; beyond it callers wait
	// busyWait and then get 503 busy.
	maxInflight = 32
	busyWait    = 250 * time.Millisecond
	healthTTL   = time.Second
)

// pool keeps a few idle daemon connections. client.Client serializes calls
// on one connection, so a connection is borrowed for exactly one call.
type pool struct {
	dial   func() (*client.Client, error)
	mu     sync.Mutex
	idle   []*client.Client
	closed bool
	sem    chan struct{}
}

func (p *pool) acquire(ctx context.Context) error {
	select {
	case p.sem <- struct{}{}:
		return nil
	default:
	}
	t := time.NewTimer(busyWait)
	defer t.Stop()
	select {
	case p.sem <- struct{}{}:
		return nil
	case <-t.C:
		return errBusy()
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (p *pool) release() { <-p.sem }

// dispatchTracker records that a request reached a daemon connection (the
// idempotency layer uses it to tell "never sent" from "outcome unknown").
type dispatchTracker struct{ dispatched atomic.Bool }

type trackerKey struct{}

func withTracker(ctx context.Context) (context.Context, *dispatchTracker) {
	t := &dispatchTracker{}
	return context.WithValue(ctx, trackerKey{}, t), t
}

func markDispatched(ctx context.Context) {
	if t, _ := ctx.Value(trackerKey{}).(*dispatchTracker); t != nil {
		t.dispatched.Store(true)
	}
}

func (p *pool) get(fresh bool) (c *client.Client, pooled bool, err error) {
	if !fresh {
		p.mu.Lock()
		if n := len(p.idle); n > 0 {
			c = p.idle[n-1]
			p.idle = p.idle[:n-1]
			p.mu.Unlock()
			return c, true, nil
		}
		p.mu.Unlock()
	}
	if p.dial == nil {
		return nil, false, errors.New("no daemon dialer configured")
	}
	c, err = p.dial()
	return c, false, err
}

func (p *pool) put(c *client.Client) {
	p.mu.Lock()
	if !p.closed && len(p.idle) < maxIdleConns {
		p.idle = append(p.idle, c)
		p.mu.Unlock()
		return
	}
	p.mu.Unlock()
	_ = c.Close()
}

func (p *pool) close() {
	p.mu.Lock()
	p.closed = true
	idle := p.idle
	p.idle = nil
	p.mu.Unlock()
	for _, c := range idle {
		_ = c.Close()
	}
}

// WithDaemon runs a READ call fn on a daemon connection under ctx (and a 10 s
// cap). A daemon-level error (*proto.RemoteError) keeps the connection; any
// I/O error discards it, and when the connection came from the idle pool the
// call is retried once on a fresh dial (reads are repeatable). The returned
// error is the raw one; use FromDaemonError to map it.
func (s *Server) WithDaemon(ctx context.Context, fn func(*client.Client) error) error {
	return s.call(ctx, callTimeout, false, fn)
}

// WithDaemonWrite runs a side-effecting call. It always uses a freshly dialled
// connection and never retries: once the request may have reached the daemon, a
// failure to see the answer (I/O error, timeout, client gone) is reported as
// 502/504 outcome_unknown (not retryable) instead of a retryable error, so a
// caller cannot execute the same action twice.
func (s *Server) WithDaemonWrite(ctx context.Context, timeout time.Duration, fn func(*client.Client) error) error {
	if timeout <= 0 {
		timeout = writeTimeout
	}
	return s.call(ctx, timeout, true, fn)
}

func (s *Server) call(ctx context.Context, timeout time.Duration, write bool, fn func(*client.Client) error) error {
	s.init()
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	if err := s.pool.acquire(ctx); err != nil {
		return err
	}
	defer s.pool.release()
	fresh := write
	for attempt := 0; ; attempt++ {
		c, pooled, err := s.pool.get(fresh)
		if err != nil {
			ue := errUnavailable("daemon unreachable")
			ue.cause = err
			return ue // nothing was sent
		}
		markDispatched(ctx)
		err = runCall(ctx, c, fn)
		if err == nil || client.IsRemote(err) {
			s.pool.put(c)
			return err
		}
		_ = c.Close()
		if write {
			status := http.StatusBadGateway
			if errors.Is(err, context.DeadlineExceeded) || ctx.Err() == context.DeadlineExceeded {
				status = http.StatusGatewayTimeout
			}
			return errOutcomeUnknown(status, err)
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if pooled && attempt == 0 {
			fresh = true // stale idle connection: retry once, freshly dialled
			continue
		}
		ue := errUnavailable("daemon unreachable")
		ue.cause = err
		return ue
	}
}

// runCall runs fn and abandons it (closing the connection) when ctx ends.
func runCall(ctx context.Context, c *client.Client, fn func(*client.Client) error) error {
	done := make(chan error, 1)
	go func() { done <- fn(c) }()
	select {
	case err := <-done:
		return err
	case <-ctx.Done():
		_ = c.Close()
		<-done
		return ctx.Err()
	}
}

// ping answers /v1/health: the result is cached for healthTTL, so health
// probes cost the daemon at most one call per second however often they come.
func (s *Server) ping() error {
	s.init()
	s.health.mu.Lock()
	defer s.health.mu.Unlock()
	if !s.health.at.IsZero() && time.Since(s.health.at) < healthTTL {
		return s.health.err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	err := s.WithDaemon(ctx, func(c *client.Client) error { _, e := c.Ping(); return e })
	s.health.at, s.health.err = time.Now(), err
	return err
}

type healthCache struct {
	mu  sync.Mutex
	at  time.Time
	err error
}
