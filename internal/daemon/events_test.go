package daemon_test

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/digiogithub/agentd/internal/client"
	"github.com/digiogithub/agentd/internal/config"
	"github.com/digiogithub/agentd/internal/daemon"
	"github.com/digiogithub/agentd/internal/proto"
	"github.com/digiogithub/agentd/internal/testutil"
)

func subscribe(t *testing.T, sock string, a proto.SubscribeArgs) *client.Subscription {
	t.Helper()
	c, err := client.Dial(sock)
	if err != nil {
		t.Fatal(err)
	}
	sub, err := c.Subscribe(a, 4096)
	if err != nil {
		_ = c.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sub.Close() })
	return sub
}

func next(t *testing.T, sub *client.Subscription) proto.Event {
	t.Helper()
	select {
	case ev, ok := <-sub.Events:
		if !ok {
			t.Fatalf("stream closed: %v", sub.Err())
		}
		return ev
	case <-time.After(10 * time.Second):
		t.Fatal("timed out waiting for an event")
	}
	return proto.Event{}
}

// nextKind skips events until one of kind arrives.
func nextKind(t *testing.T, sub *client.Subscription, kind string) proto.Event {
	t.Helper()
	for {
		if ev := next(t, sub); ev.Kind == kind {
			return ev
		}
	}
}

func expectHello(t *testing.T, sub *client.Subscription, e *env) {
	t.Helper()
	ev := next(t, sub)
	var h proto.HelloData
	if ev.Kind != proto.EvHello || json.Unmarshal(ev.Data, &h) != nil || h.Epoch != e.srv.Epoch() || ev.Epoch != h.Epoch {
		t.Fatalf("first event = %+v, want hello", ev)
	}
}

func TestSubscribeOrderingAndFilters(t *testing.T) {
	e := startServer(t, nil, nil)
	all := subscribe(t, e.sock, proto.SubscribeArgs{})
	only := subscribe(t, e.sock, proto.SubscribeArgs{Kinds: []string{"mail"}, Sessions: []string{"a"}})
	if all.Info.Epoch != e.srv.Epoch() {
		t.Fatalf("info epoch = %q", all.Info.Epoch)
	}
	expectHello(t, all, e)
	expectHello(t, only, e)

	for i := 0; i < 50; i++ {
		e.srv.PublishForTest(proto.EvViewChanged, "a", proto.ViewChangedData{Revision: uint64(i)})
	}
	e.srv.PublishForTest(proto.EvViewChanged, "b", nil)
	e.srv.PublishForTest(proto.EvMailQueued, "b", proto.MailEventData{ID: 1})
	e.srv.PublishForTest(proto.EvMailQueued, "a", proto.MailEventData{ID: 2})
	var prev uint64
	for i := 0; i < 53; i++ {
		ev := next(t, all)
		if ev.Seq != prev+1 && prev != 0 || ev.Seq == 0 {
			t.Fatalf("event %d seq %d after %d", i, ev.Seq, prev)
		}
		prev = ev.Seq
	}
	if prev != e.srv.BusSeqForTest() {
		t.Fatalf("last seq %d != bus seq %d", prev, e.srv.BusSeqForTest())
	}
	ev := next(t, only)
	if ev.Kind != proto.EvMailQueued || ev.Session != "a" {
		t.Fatalf("filtered stream got %+v", ev)
	}
}

func TestSubscribeRejectsUnknownKind(t *testing.T) {
	e := startServer(t, nil, nil)
	c := e.dial(t)
	_, err := c.Subscribe(proto.SubscribeArgs{Kinds: []string{"bogus"}}, 0)
	if !client.IsCode(err, proto.CodeInvalid) {
		t.Fatalf("err = %v, want invalid", err)
	}
	// The connection stays usable after a failed subscribe.
	if _, err := c.Ping(); err != nil {
		t.Fatal(err)
	}
}

func TestSubscribeReplayAndResync(t *testing.T) {
	e := startServer(t, nil, nil)
	for i := 1; i <= 10; i++ {
		e.srv.PublishForTest(proto.EvViewChanged, "a", proto.ViewChangedData{Revision: uint64(i)})
	}
	sub := subscribe(t, e.sock, proto.SubscribeArgs{SinceSeq: 5, Epoch: e.srv.Epoch()})
	expectHello(t, sub, e)
	for want := uint64(6); want <= 10; want++ {
		if ev := next(t, sub); ev.Seq != want {
			t.Fatalf("replay seq %d, want %d", ev.Seq, want)
		}
	}
	e.srv.PublishForTest(proto.EvViewChanged, "a", nil)
	if ev := next(t, sub); ev.Seq != 11 || ev.Kind == proto.EvResync {
		t.Fatalf("live after replay = %+v", ev)
	}

	resyncReason := func(a proto.SubscribeArgs) string {
		s := subscribe(t, e.sock, a)
		expectHello(t, s, e)
		ev := next(t, s)
		if ev.Kind != proto.EvResync {
			t.Fatalf("got %+v, want resync", ev)
		}
		var r proto.ResyncData
		_ = json.Unmarshal(ev.Data, &r)
		return r.Reason
	}
	if r := resyncReason(proto.SubscribeArgs{SinceSeq: 5, Epoch: "other-epoch"}); r != proto.ResyncEpochChanged {
		t.Errorf("epoch mismatch reason = %q", r)
	}
	if r := resyncReason(proto.SubscribeArgs{SinceSeq: 9999, Epoch: e.srv.Epoch()}); r != proto.ResyncSinceTooOld {
		t.Errorf("future since_seq reason = %q", r)
	}
	for i := 0; i < daemon.ReplayRing+5; i++ {
		e.srv.PublishForTest(proto.EvViewChanged, "a", nil)
	}
	if r := resyncReason(proto.SubscribeArgs{SinceSeq: 3, Epoch: e.srv.Epoch()}); r != proto.ResyncSinceTooOld {
		t.Errorf("ring miss reason = %q", r)
	}
	// Still inside the ring after wrap: replay, no resync.
	last := e.srv.BusSeqForTest()
	s := subscribe(t, e.sock, proto.SubscribeArgs{SinceSeq: last - 3, Epoch: e.srv.Epoch()})
	expectHello(t, s, e)
	if ev := next(t, s); ev.Seq != last-2 {
		t.Fatalf("wrapped replay first = %+v", ev)
	}
}

// A subscriber that stops reading must never slow publishers: they return
// at once and the client is told to resync.
func TestSubscribeSlowReaderDoesNotBlockPublishers(t *testing.T) {
	e := startServer(t, nil, nil)
	c, err := client.Dial(e.sock)
	if err != nil {
		t.Fatal(err)
	}
	sub, err := c.Subscribe(proto.SubscribeArgs{}, 1) // tiny buffer, not drained
	if err != nil {
		t.Fatal(err)
	}
	defer sub.Close()

	payload := map[string]string{"pad": strings.Repeat("x", 8<<10)}
	start := time.Now()
	for i := 0; i < 3000; i++ { // ~24 MiB: far beyond socket buffers
		e.srv.PublishForTest(proto.EvViewChanged, "a", payload)
	}
	if d := time.Since(start); d > 3*time.Second {
		t.Fatalf("publishers were slowed down by the stuck subscriber: %v", d)
	}
	// Now drain: a resync must be in the stream, then live events.
	resyncs := 0
	timeout := time.After(15 * time.Second)
	for resyncs == 0 {
		select {
		case ev := <-sub.Events:
			if ev.Kind == proto.EvResync {
				resyncs++
			}
		case <-timeout:
			t.Fatal("no resync after overflow")
		}
	}
	e.srv.PublishForTest(proto.EvInputChanged, "a", proto.InputChangedData{State: "draft"})
	for ev := range sub.Events {
		if ev.Kind == proto.EvInputChanged {
			return
		}
	}
	t.Fatal("stream ended before the live event")
}

func TestSubscribeHeartbeat(t *testing.T) {
	e := startServer(t, nil, func(s *daemon.Server) { s.HeartbeatInterval = 40 * time.Millisecond })
	sub := subscribe(t, e.sock, proto.SubscribeArgs{})
	expectHello(t, sub, e)
	for i := 0; i < 2; i++ {
		if ev := next(t, sub); ev.Kind != proto.EvHeartbeat || ev.Seq != 0 {
			t.Fatalf("got %+v, want heartbeat", ev)
		}
	}
}

func TestSubscribeShutdownSaysBye(t *testing.T) {
	e := startServer(t, nil, nil)
	sub := subscribe(t, e.sock, proto.SubscribeArgs{})
	expectHello(t, sub, e)
	e.srv.PublishForTest(proto.EvViewChanged, "a", nil)
	shutdownWithin(t, e.srv, 10*time.Second)
	var kinds []string
	for ev := range sub.Events {
		kinds = append(kinds, ev.Kind)
	}
	if len(kinds) != 2 || kinds[0] != proto.EvViewChanged || kinds[1] != proto.EvBye {
		t.Fatalf("events after shutdown = %v, want [view.changed bye]", kinds)
	}
	if sub.Err() != nil {
		t.Fatal(sub.Err())
	}
	if n := e.srv.BusSubscribersForTest(); n != 0 {
		t.Fatalf("%d subscribers left after shutdown", n)
	}
}

func TestSubscribeCloseUnregisters(t *testing.T) {
	e := startServer(t, nil, nil)
	c, _ := client.Dial(e.sock)
	sub, err := c.Subscribe(proto.SubscribeArgs{}, 0)
	if err != nil {
		t.Fatal(err)
	}
	testutil.WaitFor(t, 5*time.Second, func() bool { return e.srv.BusSubscribersForTest() == 1 }, "registered", nil)
	_ = sub.Close()
	testutil.WaitFor(t, 5*time.Second, func() bool { return e.srv.BusSubscribersForTest() == 0 }, "unregistered after close", nil)
}

func TestSubscribeIdentityFiltering(t *testing.T) {
	cfg := config.Default()
	e := startServer(t, cfg, nil)
	h := e.dial(t)
	for _, n := range []string{"a", "b"} {
		runView(t, h, n, "default", "sh", "-c", "sleep 60")
	}
	ca := dialAs(t, e, "a")
	asub, err := ca.Subscribe(proto.SubscribeArgs{}, 4096)
	if err != nil {
		t.Fatal(err)
	}
	defer asub.Close()
	if len(asub.Info.Sessions) != 1 || asub.Info.Sessions[0].Name != "a" {
		t.Fatalf("a's snapshot = %+v", asub.Info.Sessions)
	}
	hsub := subscribe(t, e.sock, proto.SubscribeArgs{Kinds: []string{"mail"}})
	if len(hsub.Info.Sessions) != 2 {
		t.Fatalf("human snapshot = %d sessions", len(hsub.Info.Sessions))
	}

	e.srv.PublishForTest(proto.EvViewChanged, "b", nil) // not about a
	e.srv.PublishForTest(proto.EvViewChanged, "a", nil)
	if _, err := h.Send(proto.SendArgs{To: "b", Body: "SECRET-BODY-1"}); err != nil {
		t.Fatal(err)
	}
	if _, err := h.Send(proto.SendArgs{To: "a", Body: "SECRET-BODY-2"}); err != nil {
		t.Fatal(err)
	}
	cb := dialAs(t, e, "b")
	if _, err := cb.Send(proto.SendArgs{To: "a", Body: "SECRET-BODY-3"}); err != nil {
		t.Fatal(err)
	}
	// a: its own view event, then mail 2 (to a), mail 3 (to a); never b's mail 1.
	var got []string
	for len(got) < 3 {
		ev := next(t, asub)
		if ev.Kind == proto.EvHello {
			continue
		}
		if strings.Contains(string(ev.Data), "SECRET") {
			t.Fatalf("event leaks the body: %s", ev.Data)
		}
		if ev.Kind == proto.EvSessionCreated || ev.Kind == proto.EvSessionStarted || strings.HasPrefix(ev.Kind, "status") || ev.Kind == proto.EvInputChanged || ev.Kind == proto.EvAttentionChanged {
			if ev.Session != "a" {
				t.Fatalf("a saw an event about %q: %+v", ev.Session, ev)
			}
			continue
		}
		got = append(got, ev.Kind+"/"+ev.Session)
		if ev.Session != "a" {
			t.Fatalf("a saw %+v", ev)
		}
	}
	want := []string{"view.changed/a", "mail.queued/a", "mail.queued/a"}
	for i := range want {
		if got[i] != want[i] {
			// real view.changed events from the watcher may interleave; only mail matters.
			if !strings.HasPrefix(got[i], "view.changed") {
				t.Fatalf("a got %v, want %v", got, want)
			}
		}
	}
	// Human sees all three mails, with ids.
	for i := 0; i < 3; i++ {
		ev := nextKind(t, hsub, proto.EvMailQueued)
		var d proto.MailEventData
		_ = json.Unmarshal(ev.Data, &d)
		if d.ID == 0 || d.To == "" || d.From == "" {
			t.Fatalf("mail event data = %s", ev.Data)
		}
	}
}

func TestSubscribeLifecycleAndStatusEvents(t *testing.T) {
	e := startViewServer(t, "")
	c := e.dial(t)
	sub := subscribe(t, e.sock, proto.SubscribeArgs{Sessions: []string{"sh1"}})
	expectHello(t, sub, e)
	runView(t, c, "sh1", "fake", "sh", "-c",
		"echo ready; sleep 3.6; for i in 1 2 3; do echo step $i; sleep 0.15; done; sleep 1; exit 3")

	var kinds, statuses []string
	exitCode := -1
	var lastRev uint64
	sawExitedStatus := false
	for len(kinds) == 0 || kinds[len(kinds)-1] != proto.EvSessionExited {
		ev := next(t, sub)
		kinds = append(kinds, ev.Kind)
		if ev.Kind == proto.EvStatusChanged && strings.Contains(string(ev.Data), `"exited"`) {
			sawExitedStatus = true
		}
		switch ev.Kind {
		case proto.EvStatusChanged:
			var d proto.StatusChangedData
			_ = json.Unmarshal(ev.Data, &d)
			if d.Status == d.From {
				t.Errorf("status.changed without change: %+v", d)
			}
			statuses = append(statuses, d.Status)
		case proto.EvViewChanged:
			var d proto.ViewChangedData
			_ = json.Unmarshal(ev.Data, &d)
			if d.Revision <= lastRev {
				t.Errorf("view.changed revision %d after %d", d.Revision, lastRev)
			}
			lastRev = d.Revision
		case proto.EvSessionExited:
			var d proto.ExitedData
			_ = json.Unmarshal(ev.Data, &d)
			exitCode = d.ExitCode
		}
	}
	if len(kinds) < 3 || kinds[0] != proto.EvSessionCreated || kinds[1] != proto.EvSessionStarted {
		t.Fatalf("kinds = %v, want created, started first", kinds)
	}
	if exitCode != 3 {
		t.Errorf("exit_code = %d", exitCode)
	}
	if !seq(statuses, "working", "idle") && !seq(statuses, "starting", "working") {
		t.Errorf("statuses = %v, want a starting/working/idle progression", statuses)
	}
	// The watcher's final status.changed(exited) races with session.exited.
	for !sawExitedStatus {
		ev := next(t, sub)
		sawExitedStatus = ev.Kind == proto.EvStatusChanged && strings.Contains(string(ev.Data), `"exited"`)
	}
	if err := c.Rm("sh1"); err != nil {
		t.Fatal(err)
	}
	if ev := nextKind(t, sub, proto.EvSessionRemoved); ev.Session != "sh1" {
		t.Fatalf("removed = %+v", ev)
	}
}

func seq(all []string, want ...string) bool {
	i := 0
	for _, s := range all {
		if i < len(want) && s == want[i] {
			i++
		}
	}
	return i == len(want)
}

func TestSubscribeMailQueuedAndDelivered(t *testing.T) {
	sock, srv := startDeliveryServer(t)
	bin := testutil.BuildFakeAgent(t)
	c, err := client.Dial(sock)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if _, err := c.Run(proto.RunArgs{Name: "fake@a", Cwd: t.TempDir(), Cmd: []string{bin}, Profile: "fake"}); err != nil {
		t.Fatal(err)
	}
	waitDump(t, c, "fake@a", "fakeagent ready")
	sub := subscribe(t, sock, proto.SubscribeArgs{Kinds: []string{"mail"}})
	_ = srv
	m, err := c.Send(proto.SendArgs{To: "fake@a", Body: "ping"})
	if err != nil {
		t.Fatal(err)
	}
	q := nextKind(t, sub, proto.EvMailQueued)
	d := nextKind(t, sub, proto.EvMailDelivered)
	var qd, dd proto.MailEventData
	_ = json.Unmarshal(q.Data, &qd)
	_ = json.Unmarshal(d.Data, &dd)
	if qd.ID != m.ID || dd.ID != m.ID || qd.To != "fake@a" || q.Session != "fake@a" || q.Seq >= d.Seq {
		t.Fatalf("queued %+v delivered %+v for msg %d", q, d, m.ID)
	}
}
