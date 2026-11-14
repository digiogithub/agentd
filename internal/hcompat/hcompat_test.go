package hcompat_test

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/digiogithub/agentd/internal/client"
	"github.com/digiogithub/agentd/internal/config"
	"github.com/digiogithub/agentd/internal/daemon"
	"github.com/digiogithub/agentd/internal/hcompat"
	"github.com/digiogithub/agentd/internal/mailbox"
	"github.com/digiogithub/agentd/internal/proto"
	"github.com/digiogithub/agentd/internal/testutil"
)

// startDaemon runs a daemon on a temp socket and state dir.
func startDaemon(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "hcompat")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	sock := filepath.Join(dir, "agentd.sock")
	// A test run from inside an agentd session must not carry its identity.
	t.Setenv("AGENTD_ID", "")
	t.Setenv("AGENTD_TOKEN", "")
	t.Setenv(config.EnvSocket, sock)
	t.Setenv(config.EnvStateDir, filepath.Join(dir, "state"))
	t.Setenv(config.EnvConfig, filepath.Join(dir, "config.toml"))
	if err := config.EnsureDirs(); err != nil {
		t.Fatal(err)
	}
	dbPath, _ := config.MailboxPath()
	store, err := mailbox.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	srv := daemon.New(config.Default(), nil, store, nil)
	if err := srv.Listen(); err != nil {
		t.Fatal(err)
	}
	go func() { _ = srv.Serve(context.Background()) }()
	t.Cleanup(srv.Shutdown)
	return sock
}

func runSession(t *testing.T, sock, name string, cmd ...string) {
	t.Helper()
	c, err := client.Dial(sock)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	cwd := t.TempDir()
	if _, err := c.Run(proto.RunArgs{Name: name, Cwd: cwd, Cmd: cmd, Cols: 80, Rows: 24}); err != nil {
		t.Fatal(err)
	}
}

// hcClient drives hcompat.Server like Herdr Desktop: one hello line, then frames.
type hcClient struct {
	t      *testing.T
	w      io.Writer
	r      *bufio.Reader
	mu     sync.Mutex
	nextID int
	frames chan hcompat.Frame
	ctrl   chan map[string]any
}

func newHCClient(t *testing.T, sock string, opts ...func(*hcompat.Server)) *hcClient {
	t.Helper()
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	srv := &hcompat.Server{
		Dial: func() (*client.Client, error) { return client.Dial(sock) },
		Core: hcompat.CoreInfo{ID: "test-core", Name: "testhost"},
	}
	for _, o := range opts {
		o(srv)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = srv.Serve(ctx, inR, outW)
		_ = outW.Close()
	}()
	t.Cleanup(func() {
		_ = inW.Close()
		cancel()
		_ = outR.Close()
		<-done
	})
	return &hcClient{t: t, w: inW, r: bufio.NewReader(outR), nextID: 1}
}

// helloFramed does Desktop's handshake and starts the frame reader.
func (c *hcClient) helloFramed() map[string]any {
	c.t.Helper()
	if _, err := io.WriteString(c.w, `{"jsonrpc":"2.0","id":0,"method":"hello","params":{"framed":true}}`+"\n"); err != nil {
		c.t.Fatal(err)
	}
	line, err := c.r.ReadBytes('\n')
	if err != nil {
		c.t.Fatal(err)
	}
	var resp map[string]any
	if err := json.Unmarshal(line, &resp); err != nil {
		c.t.Fatalf("hello reply %q: %v", line, err)
	}
	if resp["error"] != nil {
		c.t.Fatalf("hello error: %v", resp["error"])
	}
	c.frames = make(chan hcompat.Frame, 1024)
	c.ctrl = make(chan map[string]any, 64)
	go func() {
		defer close(c.frames)
		for {
			f, err := hcompat.ReadFrame(c.r)
			if err != nil {
				return
			}
			if f.Channel == 0 {
				var m map[string]any
				_ = json.Unmarshal(f.Payload, &m)
				c.ctrl <- m
				continue
			}
			c.frames <- f
		}
	}()
	return resp["result"].(map[string]any)
}

func (c *hcClient) send(ch uint32, kind byte, payload []byte) {
	c.t.Helper()
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, err := c.w.Write(hcompat.AppendFrame(nil, ch, kind, payload)); err != nil {
		c.t.Fatal(err)
	}
}

// call sends one framed request and waits for its reply.
func (c *hcClient) call(method string, params any) map[string]any {
	c.t.Helper()
	c.mu.Lock()
	id := c.nextID
	c.nextID++
	c.mu.Unlock()
	b, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": params})
	c.send(0, hcompat.KindData, b)
	timeout := time.After(10 * time.Second)
	for {
		select {
		case m := <-c.ctrl:
			if got, ok := m["id"].(float64); ok && int(got) == id {
				return m
			}
		case <-timeout:
			c.t.Fatalf("%s: no reply", method)
		}
	}
}

func result(t *testing.T, m map[string]any) map[string]any {
	t.Helper()
	if m["error"] != nil {
		t.Fatalf("error: %v", m["error"])
	}
	return m["result"].(map[string]any)
}

func errKind(m map[string]any) string {
	e, _ := m["error"].(map[string]any)
	d, _ := e["data"].(map[string]any)
	k, _ := d["kind"].(string)
	return k
}

// readStream collects DATA on ch until cond holds over the collected bytes.
func (c *hcClient) readStream(ch uint32, cond func([]byte) bool) []byte {
	c.t.Helper()
	var buf []byte
	timeout := time.After(10 * time.Second)
	for {
		select {
		case f, ok := <-c.frames:
			if !ok {
				c.t.Fatalf("stream ended; got %q", buf)
			}
			if f.Channel != ch {
				continue
			}
			if f.Kind == hcompat.KindData {
				buf = append(buf, f.Payload...)
				c.send(ch, hcompat.KindCredit, hcompat.CreditPayload(uint32(len(f.Payload))))
				if cond(buf) {
					return buf
				}
			}
		case <-timeout:
			c.t.Fatalf("timeout; got %q", buf)
		}
	}
}

// waitFrame waits for a frame of kind on ch, crediting DATA as it goes.
func (c *hcClient) waitFrame(ch uint32, kind byte) hcompat.Frame {
	c.t.Helper()
	timeout := time.After(10 * time.Second)
	for {
		select {
		case f, ok := <-c.frames:
			if !ok {
				c.t.Fatal("stream ended")
			}
			if f.Channel != ch {
				continue
			}
			if f.Kind == kind {
				return f
			}
			if f.Kind == hcompat.KindData {
				c.send(ch, hcompat.KindCredit, hcompat.CreditPayload(uint32(len(f.Payload))))
			}
		case <-timeout:
			c.t.Fatalf("timeout waiting for kind %d on %d", kind, ch)
		}
	}
}

func TestHelloSatisfiesHerdrClients(t *testing.T) {
	sock := startDaemon(t)
	c := newHCClient(t, sock)
	res := c.helloFramed()
	if res["generation"].(float64) != 1 || res["framed"] != true {
		t.Fatalf("hello = %v", res)
	}
	if res["version"] != hcompat.CompatVersion {
		t.Fatalf("version = %v", res["version"])
	}
	core := res["core"].(map[string]any)
	if core["id"] != "test-core" || core["name"] != "testhost" {
		t.Fatalf("core = %v", core)
	}
	have := map[string]bool{}
	for _, m := range res["methods"].([]any) {
		have[m.(string)] = true
	}
	// Herdr Desktop's USES (herdr-app src/hc.rs).
	for _, m := range []string{"terminal.run", "terminal.list", "terminal.kill", "terminal.label",
		"terminal.attach", "terminal.keep", "doc.get", "doc.set", "events.subscribe"} {
		if !have[m] {
			t.Errorf("hello.methods lacks %s", m)
		}
	}
	st := result(t, c.call("server.status", map[string]any{}))
	checkResult(t, loadSchema(t), "server.status", st)
	if st["installed_by"] != "agentd" {
		t.Fatalf("server.status = %v", st)
	}
}

func TestHelloRejectsOtherGeneration(t *testing.T) {
	sock := startDaemon(t)
	c := newHCClient(t, sock)
	if _, err := io.WriteString(c.w, `{"jsonrpc":"2.0","id":1,"method":"hello","params":{"generation":2}}`+"\n"); err != nil {
		t.Fatal(err)
	}
	line, err := c.r.ReadBytes('\n')
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	_ = json.Unmarshal(line, &m)
	if errKind(m) != "unsupported_generation" {
		t.Fatalf("reply = %s", line)
	}
}

func TestPlainModeList(t *testing.T) {
	sock := startDaemon(t)
	runSession(t, sock, "sh@proj", "sh", "-c", "sleep 30")
	c := newHCClient(t, sock)
	if _, err := io.WriteString(c.w, `{"jsonrpc":"2.0","id":1,"method":"terminal.list","params":{}}`+"\n"); err != nil {
		t.Fatal(err)
	}
	line, err := c.r.ReadBytes('\n')
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(line, &m); err != nil {
		t.Fatal(err)
	}
	checkResult(t, loadSchema(t), "terminal.list", result(t, m))
	terms := result(t, m)["terminals"].([]any)
	if len(terms) != 1 {
		t.Fatalf("terminals = %v", terms)
	}
	term := terms[0].(map[string]any)
	labels := term["labels"].(map[string]any)
	if term["address"] != "sh@proj" || term["state"] != "live" || labels["herdr.space"] != "proj" || labels["herdr.tab"] != "sh@proj" {
		t.Fatalf("terminal = %v", term)
	}
	if strings.Contains(term["address"].(string), "/") {
		t.Fatal("local addresses must not contain '/'")
	}
}

func TestSubscribeAttachInputAndClose(t *testing.T) {
	sock := startDaemon(t)
	runSession(t, sock, "cat@proj", "sh", "-c", "echo ready-hc; exec cat")
	c := newHCClient(t, sock)
	c.helloFramed()

	sub := result(t, c.call("events.subscribe", map[string]any{}))
	checkResult(t, loadSchema(t), "events.subscribe", sub)
	terms := sub["terminals"].([]any)
	if len(terms) != 1 || terms[0].(map[string]any)["address"] != "cat@proj" {
		t.Fatalf("snapshot = %v", terms)
	}
	evCh := uint32(sub["channel"].(float64))

	// Wait until the program printed, then attach like Desktop.
	testutil.WaitFor(t, 10*time.Second, func() bool {
		cl, err := client.Dial(sock)
		if err != nil {
			return false
		}
		defer cl.Close()
		s, err := cl.Dump(proto.DumpArgs{Name: "cat@proj"})
		return err == nil && strings.Contains(s, "ready-hc")
	}, "program output", nil)
	att := result(t, c.call("terminal.attach", map[string]any{
		"terminal": "cat@proj", "cols": 80, "rows": 24, "history": 2000,
		"display": map[string]any{"fg": "#ffffff", "bg": "#000000"},
	}))
	checkResult(t, loadSchema(t), "terminal.attach", att)
	ch := uint32(att["channel"].(float64))
	if ch == 0 || ch == evCh {
		t.Fatalf("attach channel = %d (events %d)", ch, evCh)
	}
	paint := c.readStream(ch, func(b []byte) bool { return bytes.Contains(b, []byte("ready-hc")) })
	if paint[0] != 0x18 {
		t.Fatalf("first paint does not start with CAN: %q", paint[:min(len(paint), 16)])
	}

	c.send(ch, hcompat.KindResize, hcompat.ResizePayload(100, 30))
	c.send(ch, hcompat.KindData, []byte("hello-from-herdr\r"))
	c.readStream(ch, func(b []byte) bool { return bytes.Count(b, []byte("hello-from-herdr")) >= 2 })

	// Detach: the core answers an empty CLOSE.
	c.send(ch, hcompat.KindClose, nil)
	if f := c.waitFrame(ch, hcompat.KindClose); len(f.Payload) != 0 {
		t.Fatalf("detach answer = %q", f.Payload)
	}

	// Kill: the subscription reports finished.
	k := result(t, c.call("terminal.kill", map[string]any{"terminal": "cat@proj"}))
	checkResult(t, loadSchema(t), "terminal.kill", k)
	if k["state"] != "finished" {
		t.Fatalf("kill = %v", k)
	}
	got := c.readStream(evCh, func(b []byte) bool { return bytes.Contains(b, []byte(`"type":"finished"`)) })
	for _, line := range bytes.Split(bytes.TrimSpace(got), []byte("\n")) {
		var m map[string]any
		if err := json.Unmarshal(line, &m); err != nil {
			t.Fatalf("event line %q: %v", line, err)
		}
		if m["jsonrpc"] != "2.0" || (m["method"] != "event" && m["method"] != "heartbeat") {
			t.Fatalf("event = %s", line)
		}
	}
}

func TestAttachToFinishedAndUnknown(t *testing.T) {
	sock := startDaemon(t)
	runSession(t, sock, "done@proj", "sh", "-c", "exit 3")
	c := newHCClient(t, sock)
	c.helloFramed()
	testutil.WaitFor(t, 10*time.Second, func() bool {
		m := c.call("terminal.get", map[string]any{"terminal": "done@proj"})
		r, _ := m["result"].(map[string]any)
		return r != nil && r["state"] == "finished"
	}, "session exit", nil)
	if k := errKind(c.call("terminal.attach", map[string]any{"terminal": "done@proj", "cols": 80, "rows": 24})); k != "finished" {
		t.Fatalf("attach finished: kind %q", k)
	}
	if k := errKind(c.call("terminal.get", map[string]any{"terminal": "nope"})); k != "not_found" {
		t.Fatalf("get unknown: kind %q", k)
	}
	if k := errKind(c.call("file.read", map[string]any{"path": "/etc/hostname"})); k != "not_supported" {
		t.Fatalf("unimplemented method: kind %q", k)
	}
}

func TestDocsVersionedWithEvent(t *testing.T) {
	sock := startDaemon(t)
	c := newHCClient(t, sock)
	c.helloFramed()
	sub := result(t, c.call("events.subscribe", map[string]any{}))
	evCh := uint32(sub["channel"].(float64))

	g := result(t, c.call("doc.get", map[string]any{"name": "herdr-app", "on": nil}))
	checkResult(t, loadSchema(t), "doc.get", g)
	if g["value"] != nil || g["version"].(float64) != 0 {
		t.Fatalf("empty doc = %v", g)
	}
	value := map[string]any{"spaces": []any{map[string]any{"id": "s1", "name": "proj", "tabs": []any{}}}}
	s := result(t, c.call("doc.set", map[string]any{"name": "herdr-app", "value": value, "version": 0}))
	checkResult(t, loadSchema(t), "doc.set", s)
	if s["version"].(float64) != 1 {
		t.Fatalf("set = %v", s)
	}
	if k := errKind(c.call("doc.set", map[string]any{"name": "herdr-app", "value": value, "version": 0})); k != "conflict" {
		t.Fatalf("stale set: kind %q", k)
	}
	g = result(t, c.call("doc.get", map[string]any{"name": "herdr-app"}))
	if g["version"].(float64) != 1 || g["value"].(map[string]any)["spaces"] == nil {
		t.Fatalf("doc = %v", g)
	}
	c.readStream(evCh, func(b []byte) bool { return bytes.Contains(b, []byte(`"type":"doc"`)) })
}

func TestLabelMergeAndRun(t *testing.T) {
	sock := startDaemon(t)
	c := newHCClient(t, sock)
	c.helloFramed()
	cwd := t.TempDir()
	r := result(t, c.call("terminal.run", map[string]any{
		"argv": []string{"sh", "-c", "sleep 30"}, "cwd": cwd, "keep": true,
		"labels": map[string]string{"herdr.space": "sp1", "herdr.tab": "tb1", "herdr.seen": "0"},
	}))
	checkResult(t, loadSchema(t), "terminal.run", r)
	addr := r["address"].(string)
	if !strings.HasPrefix(addr, "sh@") || r["keep"] != true || r["labels"].(map[string]any)["herdr.space"] != "sp1" {
		t.Fatalf("run = %v", r)
	}
	// A second run in the same folder gets a free name.
	r2 := result(t, c.call("terminal.run", map[string]any{"argv": []string{"sh", "-c", "sleep 30"}, "cwd": cwd}))
	if r2["address"] == addr {
		t.Fatalf("second run reused %s", addr)
	}
	l := result(t, c.call("terminal.label", map[string]any{"terminal": addr, "labels": map[string]string{"herdr.seen": "2", "herdr.tab": ""}}))
	checkResult(t, loadSchema(t), "terminal.label", l)
	labels := l["labels"].(map[string]any)
	if labels["herdr.seen"] != "2" || labels["herdr.space"] != "sp1" {
		t.Fatalf("labels = %v", labels)
	}
	if _, ok := labels["herdr.tab"]; ok {
		t.Fatalf("empty value must delete the label: %v", labels)
	}
}
