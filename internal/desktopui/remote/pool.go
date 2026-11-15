package remote

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/digiogithub/agentd/internal/desktopui/backend"
)

// DialFunc opens a Shell to a host.
type DialFunc func(ctx context.Context, h backend.Host) (backend.Shell, error)

// Backoff bounds between failed connection attempts to one host.
const (
	MinBackoff = time.Second
	MaxBackoff = time.Minute
)

// ErrBackoff wraps the last connection error while the pool waits before
// trying a host again.
var ErrBackoff = errors.New("remote: waiting before reconnecting")

// Pool keeps one Shell per host: connected on demand, dropped when the
// connection ends or a caller reports an error, reconnected with exponential
// backoff (Reset skips the wait, for an explicit "Connect").
type Pool struct {
	dial   DialFunc
	lookup func(id string) (backend.Host, error)
	// OnChange is called (without locks held) when a host's shell connects
	// or is dropped.
	OnChange func(hostID string, connected bool, err error)
	now      func() time.Time

	mu      sync.Mutex
	entries map[string]*poolEntry
	closed  bool
}

type poolEntry struct {
	shell    backend.Shell
	dialing  chan struct{} // closed when the current dial ends
	failures int
	next     time.Time
	lastErr  error
}

// donner is implemented by shells that know when their connection ends.
type donner interface{ Done() <-chan struct{} }

// NewPool returns a pool that resolves host ids with lookup and connects
// with dial.
func NewPool(lookup func(id string) (backend.Host, error), dial DialFunc) *Pool {
	return &Pool{dial: dial, lookup: lookup, now: time.Now, entries: map[string]*poolEntry{}}
}

func (p *Pool) entry(id string) *poolEntry {
	e := p.entries[id]
	if e == nil {
		e = &poolEntry{}
		p.entries[id] = e
	}
	return e
}

// Connected reports whether a live shell is held for id.
func (p *Pool) Connected(id string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	e := p.entries[id]
	return e != nil && e.shell != nil
}

// Dialing reports whether a connection attempt to id is in progress.
func (p *Pool) Dialing(id string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	e := p.entries[id]
	return e != nil && e.dialing != nil
}

// LastErr is the last connection error of id (nil after a success).
func (p *Pool) LastErr(id string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if e := p.entries[id]; e != nil {
		return e.lastErr
	}
	return nil
}

// Get returns the shell of host id, connecting when needed. While the host
// is in backoff after a failure it returns ErrBackoff wrapping the last
// error without dialing.
func (p *Pool) Get(ctx context.Context, id string) (backend.Shell, error) {
	for {
		p.mu.Lock()
		if p.closed {
			p.mu.Unlock()
			return nil, errors.New("remote: pool closed")
		}
		e := p.entry(id)
		if e.shell != nil {
			s := e.shell
			p.mu.Unlock()
			return s, nil
		}
		if e.dialing != nil {
			ch := e.dialing
			p.mu.Unlock()
			select {
			case <-ch:
				continue
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}
		if e.lastErr != nil && p.now().Before(e.next) {
			err := e.lastErr
			p.mu.Unlock()
			return nil, fmt.Errorf("%w: %w", ErrBackoff, err)
		}
		e.dialing = make(chan struct{})
		p.mu.Unlock()
		return p.connect(ctx, id, e)
	}
}

func (p *Pool) connect(ctx context.Context, id string, e *poolEntry) (backend.Shell, error) {
	var s backend.Shell
	h, err := p.lookup(id)
	if err == nil {
		s, err = p.dial(ctx, h)
	}
	p.mu.Lock()
	ch := e.dialing
	e.dialing = nil
	if err != nil {
		// A cancelled caller is not a host failure.
		if ctx.Err() == nil {
			e.failures++
			e.lastErr = err
			e.next = p.now().Add(backoff(e.failures))
		}
	} else if p.closed {
		err = errors.New("remote: pool closed")
		_ = s.Close()
		s = nil
	} else {
		e.shell, e.failures, e.lastErr = s, 0, nil
	}
	p.mu.Unlock()
	close(ch)
	if err != nil {
		if ctx.Err() == nil && p.OnChange != nil {
			p.OnChange(id, false, err)
		}
		return nil, err
	}
	if d, ok := s.(donner); ok {
		go func() {
			<-d.Done()
			p.drop(id, s, errors.New("remote: connection lost"))
		}()
	}
	if p.OnChange != nil {
		p.OnChange(id, true, nil)
	}
	return s, nil
}

func backoff(failures int) time.Duration {
	d := MinBackoff
	for i := 1; i < failures && d < MaxBackoff; i++ {
		d *= 2
	}
	return min(d, MaxBackoff)
}

// Drop closes and forgets the shell of id if it is still s (nil = whatever
// is held). Callers report a broken shell this way; the next Get reconnects.
func (p *Pool) Drop(id string, s backend.Shell) { p.drop(id, s, nil) }

func (p *Pool) drop(id string, s backend.Shell, cause error) {
	p.mu.Lock()
	e := p.entries[id]
	if e == nil || e.shell == nil || (s != nil && e.shell != s) {
		p.mu.Unlock()
		return
	}
	held := e.shell
	e.shell = nil
	if cause != nil {
		e.lastErr = cause
		e.next = p.now() // reconnect at once on the next Get
	}
	p.mu.Unlock()
	_ = held.Close()
	if p.OnChange != nil {
		p.OnChange(id, false, cause)
	}
}

// Reset clears the backoff of id so the next Get dials at once.
func (p *Pool) Reset(id string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if e := p.entries[id]; e != nil {
		e.next = time.Time{}
		e.failures = 0
	}
}

// Forget drops the shell and the backoff state of id (host edited or
// removed).
func (p *Pool) Forget(id string) {
	p.Drop(id, nil)
	p.mu.Lock()
	if e := p.entries[id]; e != nil && e.dialing == nil {
		delete(p.entries, id)
	}
	p.mu.Unlock()
}

// Close closes every shell; Get fails afterwards.
func (p *Pool) Close() {
	p.mu.Lock()
	p.closed = true
	var shells []backend.Shell
	for _, e := range p.entries {
		if e.shell != nil {
			shells = append(shells, e.shell)
			e.shell = nil
		}
	}
	p.mu.Unlock()
	for _, s := range shells {
		_ = s.Close()
	}
}
