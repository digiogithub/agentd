package daemon_test

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/digiogithub/agentd/internal/config"
	"github.com/digiogithub/agentd/internal/daemon"
	"github.com/digiogithub/agentd/internal/mailbox"
	"github.com/digiogithub/agentd/internal/proto"
)

// fakeClock is a manual daemon.Clock: Advance moves time, Tick fires the
// retention ticker.
type fakeClock struct {
	mu       sync.Mutex
	now      time.Time
	tick     chan time.Time
	interval time.Duration
	stopped  bool
}

func newFakeClock(now time.Time) *fakeClock {
	return &fakeClock{now: now, tick: make(chan time.Time)}
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) NewTicker(d time.Duration) (<-chan time.Time, func()) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.interval = d
	return c.tick, func() {
		c.mu.Lock()
		c.stopped = true
		c.mu.Unlock()
	}
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(d)
	c.mu.Unlock()
}

func (c *fakeClock) Tick(t *testing.T) {
	t.Helper()
	select {
	case c.tick <- c.Now():
	case <-time.After(timeout):
		t.Fatal("retention loop is not waiting on its ticker")
	}
}

// syncBuffer is a goroutine-safe log sink.
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

type purgeRun struct {
	n   int
	err error
}

func waitPurge(t *testing.T, ch <-chan purgeRun) purgeRun {
	t.Helper()
	select {
	case r := <-ch:
		if r.err != nil {
			t.Fatalf("retention purge: %v", r.err)
		}
		return r
	case <-time.After(timeout):
		t.Fatal("no retention purge")
	}
	return purgeRun{}
}

func seed(t *testing.T, st *mailbox.Store, at time.Time, body string) mailbox.Message {
	t.Helper()
	st.SetClock(func() time.Time { return at })
	defer st.SetClock(nil)
	m, err := st.Send(context.Background(), mailbox.Message{Sender: "human:x", Recipient: "human:y", Body: body})
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func exists(t *testing.T, st *mailbox.Store, id int64) bool {
	t.Helper()
	_, err := st.Get(context.Background(), id)
	if err != nil && !errors.Is(err, mailbox.ErrNotFound) {
		t.Fatal(err)
	}
	return err == nil
}

func TestRetentionLoop(t *testing.T) {
	clearIdentityEnv(t)
	t0 := time.Date(2026, 11, 7, 10, 0, 0, 0, time.UTC)
	fc := newFakeClock(t0)
	runs := make(chan purgeRun, 10)
	logs := &syncBuffer{}
	var old, mid, fresh mailbox.Message
	e := startServer(t, nil, func(srv *daemon.Server) { // defaults: 5d, every 1h
		srv.Clock = fc
		srv.Log = slog.New(slog.NewTextHandler(logs, nil))
		srv.SetRetentionHookForTest(func(n int, err error) { runs <- purgeRun{n, err} })
		old = seed(t, srv.Store, t0.Add(-6*24*time.Hour), "old")
		mid = seed(t, srv.Store, t0.Add(-4*24*time.Hour), "mid")
		fresh = seed(t, srv.Store, t0, "fresh")
	})
	st := e.srv.Store

	// At startup: everything older than 5 days goes, whatever its state.
	if r := waitPurge(t, runs); r.n != 1 {
		t.Fatalf("startup purge = %d, want 1", r.n)
	}
	if exists(t, st, old.ID) || !exists(t, st, mid.ID) || !exists(t, st, fresh.ID) {
		t.Fatal("wrong messages purged at startup")
	}
	fc.mu.Lock()
	interval := fc.interval
	fc.mu.Unlock()
	if interval != config.DefaultPurgeInterval {
		t.Fatalf("ticker interval = %v", interval)
	}

	// A tick before anything expires purges nothing.
	fc.Tick(t)
	if r := waitPurge(t, runs); r.n != 0 {
		t.Fatalf("early tick purged %d", r.n)
	}
	// One day later mid is past retention.
	fc.Advance(24*time.Hour + time.Minute)
	fc.Tick(t)
	if r := waitPurge(t, runs); r.n != 1 {
		t.Fatalf("tick purge = %d, want 1", r.n)
	}
	if exists(t, st, mid.ID) || !exists(t, st, fresh.ID) {
		t.Fatal("wrong messages purged on tick")
	}
	if l := logs.String(); !strings.Contains(l, `msg="mailbox retention purge" purged=1`) {
		t.Fatalf("retention not logged:\n%s", l)
	}

	shutdownWithin(t, e.srv, timeout)
	fc.mu.Lock()
	stopped := fc.stopped
	fc.mu.Unlock()
	if !stopped {
		t.Fatal("retention ticker not stopped on shutdown")
	}
}

func TestRetentionDisabled(t *testing.T) {
	clearIdentityEnv(t)
	cfg := config.Default()
	zero := config.Duration(0)
	cfg.Mailbox.Retention = &zero
	called := make(chan struct{}, 1)
	var old mailbox.Message
	e := startServer(t, cfg, func(srv *daemon.Server) {
		srv.SetRetentionHookForTest(func(int, error) { called <- struct{}{} })
		old = seed(t, srv.Store, time.Now().Add(-365*24*time.Hour), "ancient")
	})
	if _, err := e.dial(t).Ping(); err != nil { // Serve has started
		t.Fatal(err)
	}
	select {
	case <-called:
		t.Fatal("retention ran although disabled")
	case <-time.After(100 * time.Millisecond):
	}
	if !exists(t, e.srv.Store, old.ID) {
		t.Fatal("message purged with retention disabled")
	}
}

func TestPurgeOpPermissions(t *testing.T) {
	e := startDaemon(t)
	c := e.dial(t)
	mustRun(t, c, "a")
	mustRun(t, c, "b")
	ca := dialAs(t, e, "a")
	st := e.srv.Store
	send := func(to, body string) int64 {
		t.Helper()
		m, err := c.Send(proto.SendArgs{To: to, Body: body})
		if err != nil {
			t.Fatal(err)
		}
		return m.ID
	}
	m1, m2, m3 := send("a", "one"), send("a", "two"), send("b", "three")
	if _, err := ca.Read("", m1); err != nil { // m1 read, m2 queued
		t.Fatal(err)
	}

	wantErr := func(name string, cl interface {
		Purge(proto.PurgeArgs) (proto.PurgeResult, error)
	}, a proto.PurgeArgs, frag string) {
		t.Helper()
		if _, err := cl.Purge(a); err == nil || !strings.Contains(err.Error(), frag) {
			t.Errorf("%s: err = %v, want %q", name, err, frag)
		}
	}
	// No accidental full wipe.
	wantErr("human, no filter", c, proto.PurgeArgs{}, "all=true")
	wantErr("human, dry run only", c, proto.PurgeArgs{DryRun: true}, "all=true")
	wantErr("agent, no filter", ca, proto.PurgeArgs{}, "all=true")
	// Agents: own mailbox, read/replied only, no vacuum.
	wantErr("agent, other mailbox", ca, proto.PurgeArgs{Recipient: "b"}, "own mailbox")
	wantErr("agent, queued", ca, proto.PurgeArgs{States: []string{"queued"}}, "read or replied")
	wantErr("agent, vacuum", ca, proto.PurgeArgs{All: true, Vacuum: true}, "humans only")
	// Argument validation.
	wantErr("bad state", c, proto.PurgeArgs{States: []string{"gone"}}, "unknown state")
	wantErr("bad before", c, proto.PurgeArgs{Before: "yesterday"}, "RFC 3339")
	wantErr("bad older_than", c, proto.PurgeArgs{OlderThan: "soon"}, "older_than")
	wantErr("both", c, proto.PurgeArgs{Before: time.Now().Format(time.RFC3339), OlderThan: "1h"}, "mutually exclusive")

	// Agent dry run, then the real thing: only its read message.
	if r, err := ca.Purge(proto.PurgeArgs{All: true, DryRun: true}); err != nil || r.Count != 1 || !r.DryRun {
		t.Fatalf("agent dry run = %+v, %v", r, err)
	}
	if !exists(t, st, m1) {
		t.Fatal("dry run deleted")
	}
	if r, err := ca.Purge(proto.PurgeArgs{Recipient: "a", All: true}); err != nil || r.Count != 1 {
		t.Fatalf("agent purge = %+v, %v", r, err)
	}
	if exists(t, st, m1) || !exists(t, st, m2) || !exists(t, st, m3) {
		t.Fatal("agent purge touched the wrong messages")
	}

	// Humans: any mailbox and state.
	if r, err := c.Purge(proto.PurgeArgs{OlderThan: "1h"}); err != nil || r.Count != 0 {
		t.Fatalf("older_than 1h = %+v, %v", r, err)
	}
	if r, err := c.Purge(proto.PurgeArgs{Recipient: "b", DryRun: true}); err != nil || r.Count != 1 {
		t.Fatalf("human dry run = %+v, %v", r, err)
	}
	future := time.Now().Add(time.Hour).Format(time.RFC3339)
	if r, err := c.Purge(proto.PurgeArgs{Before: future, Recipient: "a", States: []string{"queued", "delivered"}}); err != nil || r.Count != 1 {
		t.Fatalf("human purge of a's queued = %+v, %v", r, err)
	}
	if r, err := c.Purge(proto.PurgeArgs{All: true, Vacuum: true}); err != nil || r.Count != 1 {
		t.Fatalf("human purge all = %+v, %v", r, err)
	}
	if exists(t, st, m2) || exists(t, st, m3) {
		t.Fatal("messages left after purge all")
	}
	// Ids keep growing after a full purge.
	if id := send("a", "after"); id <= m3 {
		t.Fatalf("id after purge = %d, want > %d", id, m3)
	}
}

// older_than is measured on the daemon's clock.
func TestPurgeOlderThanUsesDaemonClock(t *testing.T) {
	clearIdentityEnv(t)
	cfg := config.Default()
	zero := config.Duration(0)
	cfg.Mailbox.Retention = &zero
	fc := newFakeClock(time.Now())
	e := startServer(t, cfg, func(srv *daemon.Server) { srv.Clock = fc })
	c := e.dial(t)
	if _, err := c.Send(proto.SendArgs{To: "human:y", Body: "x"}); err != nil {
		t.Fatal(err)
	}
	if r, err := c.Purge(proto.PurgeArgs{OlderThan: "5d"}); err != nil || r.Count != 0 {
		t.Fatalf("before advancing = %+v, %v", r, err)
	}
	fc.Advance(5*24*time.Hour + time.Second)
	if r, err := c.Purge(proto.PurgeArgs{OlderThan: "5d"}); err != nil || r.Count != 1 {
		t.Fatalf("after advancing = %+v, %v", r, err)
	}
}
