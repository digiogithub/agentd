// Package app is the core of the agentd-desktop UI: the app-wide Context
// handed to every view (backend, router, toasts, local preferences, the live
// hosts and sessions), the registry of areas (agents, hosts, mailbox,
// settings, ...), the router and the window layout (sidebar + content).
// Each area lives in its own package under app/<area> and registers itself
// from init(). The UI talks to hosts only through backend.Backend.
package app

import (
	"context"
	"fmt"
	"log/slog"
	"runtime/debug"
	"sync"

	"github.com/digiogithub/agentd/internal/desktopui/backend"
	"github.com/digiogithub/agentd/internal/desktopui/kit"
	"github.com/egoist/mygo/ui"
)

// Dispatcher posts work to the UI thread. *mygo.Window implements it.
type Dispatcher interface {
	Update(fn func())
	Invalidate()
}

// Shell is what the app needs from the window shell (shell.Controller):
// desktop notifications and the focus state.
type Shell interface {
	Notify(id, title, body string, onClick func())
	Focused() bool
}

// Context is the app-wide context every view receives. Mutate view state
// only on the UI thread (in Build, or through Update from goroutines).
type Context struct {
	// Ctx is canceled when the app shuts down.
	Ctx context.Context
	// Backend reaches the hosts.
	Backend backend.Backend
	// Router navigates between areas.
	Router *ui.Router
	// Toasts queues toasts from any goroutine.
	Toasts *kit.Toaster
	// Prefs are the local preferences (notifications, mutes, theme).
	Prefs *Prefs
	// Live holds the hosts and sessions the backend reports (UI thread).
	Live *Live
	// Views caches the semantic views of agentd sessions (UI thread).
	Views *Views
	// Log is the UI logger.
	Log *slog.Logger

	dispMu sync.RWMutex
	disp   Dispatcher
	shell  Shell
}

// NewContext returns a context on backend b. prefs may be nil (in memory).
func NewContext(ctx context.Context, b backend.Backend, prefs *Prefs) *Context {
	if ctx == nil {
		ctx = context.Background()
	}
	if prefs == nil {
		prefs = NewPrefs("")
	}
	r := ui.NewRouter(DefaultPath)
	r.Transition = ui.TransitionNone
	a := &Context{
		Ctx:     ctx,
		Backend: b,
		Router:  r,
		Toasts:  &kit.Toaster{},
		Prefs:   prefs,
		Log:     slog.Default().With("component", "desktopui"),
	}
	a.Live = &Live{a: a}
	a.Views = newViews(a)
	a.Toasts.Invalidate = a.Invalidate
	return a
}

// SetDispatcher connects the context to its window (or a test queue).
func (a *Context) SetDispatcher(d Dispatcher) {
	a.dispMu.Lock()
	a.disp = d
	a.dispMu.Unlock()
}

// SetShell connects the window shell (notifications, focus).
func (a *Context) SetShell(s Shell) {
	a.dispMu.Lock()
	a.shell = s
	a.dispMu.Unlock()
}

// ShellHost returns the window shell, nil before the window exists.
func (a *Context) ShellHost() Shell {
	a.dispMu.RLock()
	defer a.dispMu.RUnlock()
	return a.shell
}

func (a *Context) dispatcher() Dispatcher {
	a.dispMu.RLock()
	defer a.dispMu.RUnlock()
	return a.disp
}

// Update runs fn on the UI thread and redraws.
func (a *Context) Update(fn func()) {
	if d := a.dispatcher(); d != nil {
		d.Update(fn)
	}
}

// Invalidate asks for a redraw.
func (a *Context) Invalidate() {
	if d := a.dispatcher(); d != nil {
		d.Invalidate()
	}
}

// Go runs fn on a goroutine with the app context, turning a panic into an
// error toast instead of a crash.
func (a *Context) Go(fn func(ctx context.Context)) {
	go func() {
		defer func() {
			if r := recover(); r != nil {
				a.Log.Error("background task panicked", "panic", r, "stack", string(debug.Stack()))
				a.Toasts.Error("Error interno", fmt.Sprint(r))
			}
		}()
		fn(a.Ctx)
	}()
}

// Navigate pushes a path on the router (UI thread).
func (a *Context) Navigate(path string) { a.Router.Push(path) }

// Batch coalesces items pushed from goroutines into one UI update per
// frame.
type Batch[T any] struct {
	mu        sync.Mutex
	items     []T
	scheduled bool
	update    func(func())
	apply     func([]T)
}

// NewBatch returns a batch applying items on the UI thread through update
// (usually Context.Update).
func NewBatch[T any](update func(func()), apply func([]T)) *Batch[T] {
	return &Batch[T]{update: update, apply: apply}
}

// Push queues an item. Safe from any goroutine.
func (b *Batch[T]) Push(v T) {
	b.mu.Lock()
	b.items = append(b.items, v)
	if b.scheduled {
		b.mu.Unlock()
		return
	}
	b.scheduled = true
	b.mu.Unlock()
	b.update(b.flush)
}

func (b *Batch[T]) flush() {
	b.mu.Lock()
	items := b.items
	b.items = nil
	b.scheduled = false
	b.mu.Unlock()
	if len(items) > 0 {
		b.apply(items)
	}
}

// QueueDispatcher is a Dispatcher for tests: Update queues fn until Drain
// runs the queue on the caller's goroutine.
type QueueDispatcher struct {
	mu    sync.Mutex
	queue []func()
	inval int
}

// Update queues fn.
func (q *QueueDispatcher) Update(fn func()) {
	q.mu.Lock()
	q.queue = append(q.queue, fn)
	q.mu.Unlock()
}

// Invalidate counts a redraw request.
func (q *QueueDispatcher) Invalidate() {
	q.mu.Lock()
	q.inval++
	q.mu.Unlock()
}

// Drain runs the queued functions (and those they queue).
func (q *QueueDispatcher) Drain() int {
	n := 0
	for {
		q.mu.Lock()
		fns := q.queue
		q.queue = nil
		q.mu.Unlock()
		if len(fns) == 0 {
			return n
		}
		for _, fn := range fns {
			fn()
			n++
		}
	}
}
