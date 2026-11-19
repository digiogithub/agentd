package app

import (
	"context"
	"errors"
	"time"

	"github.com/digiogithub/agentd/internal/desktopui/backend"
	"github.com/digiogithub/agentd/internal/proto"
)

// Live is the app-wide state the backend streams: hosts and sessions. Its
// fields change only on the UI thread.
type Live struct {
	a       *Context
	started bool

	Hosts          []backend.HostStatus
	Sessions       []backend.Session
	HostsLoaded    bool
	SessionsLoaded bool

	// OnSessions runs on the UI thread with the previous and new sessions
	// (the notification service uses it).
	OnSessions []func(prev, next []backend.Session)
}

// Start subscribes to the backend (once). Call it on the UI thread.
func (l *Live) Start() {
	if l.started || l.a.Backend == nil {
		return
	}
	l.started = true
	a := l.a
	a.Go(func(ctx context.Context) {
		for hs := range a.Backend.WatchHosts(ctx) {
			a.Update(func() { l.Hosts, l.HostsLoaded = hs, true })
		}
	})
	a.Go(func(ctx context.Context) {
		for ss := range a.Backend.WatchSessions(ctx) {
			a.Update(func() {
				prev := l.Sessions
				l.Sessions, l.SessionsLoaded = ss, true
				for _, fn := range l.OnSessions {
					fn(prev, ss)
				}
			})
		}
	})
}

// Host returns the status of a host.
func (l *Live) Host(id string) (backend.HostStatus, bool) {
	for _, h := range l.Hosts {
		if h.Host.ID == id {
			return h, true
		}
	}
	return backend.HostStatus{}, false
}

// Allows reports whether the app may offer c on host id (backend.Host.Allows;
// an unknown host allows everything: the backend still refuses).
func (l *Live) Allows(id string, c backend.Capability) bool {
	if h, ok := l.Host(id); ok {
		return h.Host.Allows(c)
	}
	return true
}

// HostName returns the display name of a host ("" when unknown).
func (l *Live) HostName(id string) string {
	if h, ok := l.Host(id); ok {
		return h.Host.Name
	}
	return ""
}

// Session returns the live session with key.
func (l *Live) Session(key string) (backend.Session, bool) {
	for _, s := range l.Sessions {
		if s.Key() == key {
			return s, true
		}
	}
	return backend.Session{}, false
}

// Online returns the hosts in line.
func (l *Live) Online() []backend.HostStatus {
	var out []backend.HostStatus
	for _, h := range l.Hosts {
		if h.State == backend.HostOnline {
			out = append(out, h)
		}
	}
	return out
}

// Views keeps one WatchView stream per agentd session someone asked for in
// the last seconds, and the latest view of each (UI thread only).
type Views struct {
	a *Context
	m map[string]*viewEntry
	// Linger is how long an entry nobody asks for keeps streaming.
	Linger time.Duration
}

type viewEntry struct {
	view   *proto.View
	err    error
	cancel context.CancelFunc
	used   time.Time
}

func newViews(a *Context) *Views {
	return &Views{a: a, m: map[string]*viewEntry{}, Linger: 10 * time.Second}
}

// Get returns the latest view of s (nil until the first one arrives),
// starting its stream when needed. Only agentd sessions have views.
func (v *Views) Get(s backend.Session) (*proto.View, error) {
	if s.Source != backend.SourceAgentd || v.a.Backend == nil {
		return nil, nil
	}
	key := s.Key()
	e, ok := v.m[key]
	if !ok {
		e = &viewEntry{}
		v.m[key] = e
		v.start(s, e)
	}
	e.used = time.Now()
	return e.view, e.err
}

// Set replaces the cached view of s (an act conflict carries the new one).
func (v *Views) Set(s backend.Session, view *proto.View) {
	if e, ok := v.m[s.Key()]; ok && view != nil {
		if e.view == nil || view.Revision >= e.view.Revision || view.Epoch != e.view.Epoch {
			e.view = view
		}
	}
}

// Restart drops a failed stream so the next Get opens it again.
func (v *Views) Restart(s backend.Session) {
	if e, ok := v.m[s.Key()]; ok {
		e.cancel()
		delete(v.m, s.Key())
	}
}

func (v *Views) start(s backend.Session, e *viewEntry) {
	ctx, cancel := context.WithCancel(v.a.Ctx)
	e.cancel = cancel
	a := v.a
	a.Go(func(context.Context) {
		ch, err := a.Backend.WatchView(ctx, s)
		if err != nil {
			a.Update(func() {
				if !errors.Is(ctx.Err(), context.Canceled) {
					e.err = err
				}
			})
			return
		}
		for view := range ch {
			a.Update(func() { e.view, e.err = view, nil })
		}
	})
}

// Sweep stops the streams nobody asked for lately. The root view calls it
// every frame.
func (v *Views) Sweep() {
	now := time.Now()
	for k, e := range v.m {
		if now.Sub(e.used) > v.Linger {
			e.cancel()
			delete(v.m, k)
		}
	}
}

// Close stops every stream.
func (v *Views) Close() {
	for k, e := range v.m {
		e.cancel()
		delete(v.m, k)
	}
}
