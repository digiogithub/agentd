package daemon_test

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/digiogithub/agentd/internal/client"
	"github.com/digiogithub/agentd/internal/config"
	"github.com/digiogithub/agentd/internal/daemon"
	"github.com/digiogithub/agentd/internal/mailbox"
	"github.com/digiogithub/agentd/internal/proto"
)

func TestLabelsRunMergeAndEvent(t *testing.T) {
	e := startDaemon(t)
	c := e.dial(t)
	sub := subscribe(t, e.sock, proto.SubscribeArgs{Kinds: []string{"session"}})
	expectHello(t, sub, e)

	if _, err := c.Run(proto.RunArgs{Name: "bad", Cmd: []string{"sleep", "60"}, Labels: map[string]string{"a b": "x"}}); client.ErrorCode(err) != proto.CodeInvalid {
		t.Fatalf("invalid label key: %v", err)
	}
	if _, err := c.Run(proto.RunArgs{Name: "lab", Cmd: []string{"sleep", "60"}, Labels: map[string]string{"herdr.space": "p1"}}); err != nil {
		t.Fatal(err)
	}
	var started proto.SessionInfo
	_ = json.Unmarshal(nextKind(t, sub, proto.EvSessionStarted).Data, &started)
	if started.Labels["herdr.space"] != "p1" {
		t.Fatalf("session.started labels = %v", started.Labels)
	}
	got, err := c.Label("lab", map[string]string{"herdr.seen": "3", "herdr.space": ""})
	if err != nil {
		t.Fatal(err)
	}
	if got["herdr.seen"] != "3" || got["herdr.space"] != "" || len(got) != 1 {
		t.Fatalf("labels = %v", got)
	}
	var ld proto.LabelsData
	ev := nextKind(t, sub, proto.EvSessionLabels)
	if json.Unmarshal(ev.Data, &ld) != nil || ev.Session != "lab" || ld.Labels["herdr.seen"] != "3" {
		t.Fatalf("labels event = %+v", ev)
	}
	if s := findSession(t, c, "lab"); s.Labels["herdr.seen"] != "3" {
		t.Fatalf("ls labels = %v", s.Labels)
	}
	if _, err := c.Label("nope", map[string]string{"k": "v"}); client.ErrorCode(err) != proto.CodeNotFound {
		t.Fatalf("label unknown: %v", err)
	}
}

func TestLabelFromSessionOnlyItself(t *testing.T) {
	e := startDaemon(t)
	c := e.dial(t)
	mustRun(t, c, "self")
	mustRun(t, c, "other")
	as := dialAs(t, e, "self")
	if _, err := as.Label("self", map[string]string{"k": "v"}); err != nil {
		t.Fatalf("label self: %v", err)
	}
	if _, err := as.Label("other", map[string]string{"k": "v"}); client.ErrorCode(err) != proto.CodeForbidden {
		t.Fatalf("label other: %v", err)
	}
	// Sessions read documents but never write them.
	if _, err := as.DocGet("x"); err != nil {
		t.Fatalf("doc_get from a session: %v", err)
	}
	if _, err := as.DocSet("x", []byte(`1`), 0); client.ErrorCode(err) != proto.CodeForbidden {
		t.Fatalf("doc_set from a session: %v", err)
	}
}

func TestDocsVersionedPersistentAndEvented(t *testing.T) {
	e := startDaemon(t)
	c := e.dial(t)
	sub := subscribe(t, e.sock, proto.SubscribeArgs{Kinds: []string{"doc"}})
	expectHello(t, sub, e)

	r, err := c.DocGet("herdr-app")
	if err != nil || string(r.Value) != "null" || r.Version != 0 {
		t.Fatalf("empty doc = %s v%d, %v", r.Value, r.Version, err)
	}
	v, err := c.DocSet("herdr-app", []byte(`{"spaces": [ {"id":"s1"} ]}`), 0)
	if err != nil || v != 1 {
		t.Fatalf("set = %d, %v", v, err)
	}
	var dc proto.DocChangedData
	if ev := nextKind(t, sub, proto.EvDocChanged); json.Unmarshal(ev.Data, &dc) != nil || dc.Name != "herdr-app" || dc.Version != 1 {
		t.Fatalf("doc.changed = %+v", ev)
	}
	if _, err := c.DocSet("herdr-app", []byte(`{}`), 0); client.ErrorCode(err) != proto.CodeConflict {
		t.Fatalf("stale set: %v", err)
	}
	for _, bad := range []string{"", "a/b", strings.Repeat("x", 65)} {
		if _, err := c.DocSet(bad, []byte(`1`), 0); client.ErrorCode(err) != proto.CodeInvalid {
			t.Fatalf("name %q: %v", bad, err)
		}
	}
	if _, err := c.DocSet("big", []byte(`"`+strings.Repeat("x", daemon.MaxDocBytes)+`"`), 0); client.ErrorCode(err) != proto.CodeInvalid {
		t.Fatalf("oversized: %v", err)
	}
	// Null deletes the value but keeps the version.
	if v, err := c.DocSet("herdr-app", []byte(`null`), 1); err != nil || v != 2 {
		t.Fatalf("delete = %d, %v", v, err)
	}
	if v, err := c.DocSet("herdr-app", []byte(`{"spaces":[]}`), 2); err != nil || v != 3 {
		t.Fatalf("rewrite = %d, %v", v, err)
	}

	// The store survives a daemon restart (same state dir).
	path, _ := config.DocsPath()
	if fi, err := os.Stat(path); err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("docs file: %v %v", fi, err)
	}
	e2 := restartDaemon(t, e)
	r, err = e2.dial(t).DocGet("herdr-app")
	if err != nil || r.Version != 3 || string(r.Value) != `{"spaces":[]}` {
		t.Fatalf("after restart = %s v%d, %v", r.Value, r.Version, err)
	}
}

func TestDocsBrokenFileStartsEmpty(t *testing.T) {
	e := startDaemon(t)
	path, _ := config.DocsPath()
	if err := os.WriteFile(path, []byte("{broken"), 0o600); err != nil {
		t.Fatal(err)
	}
	r, err := e.dial(t).DocGet("herdr-app")
	if err != nil || r.Version != 0 {
		t.Fatalf("broken store = v%d, %v", r.Version, err)
	}
	if _, err := os.Stat(path + ".bad"); err != nil {
		t.Fatalf("broken file not kept as .bad: %v", err)
	}
}

// restartDaemon stops e's daemon and starts a new one on the same socket and
// state dir (the environment startDaemon set).
func restartDaemon(t *testing.T, e *env) *env {
	t.Helper()
	e.srv.Shutdown()
	dbPath, _ := config.MailboxPath()
	store, err := mailbox.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	srv := daemon.New(config.Default(), nil, store, nil)
	if err := srv.Listen(); err != nil {
		t.Fatal(err)
	}
	e2 := &env{srv: srv, sock: e.sock, errc: make(chan error, 1)}
	go func() { e2.errc <- srv.Serve(context.Background()) }()
	t.Cleanup(srv.Shutdown)
	return e2
}
