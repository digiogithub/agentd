package mcpserver_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/digiogithub/agentd/internal/client"
	"github.com/digiogithub/agentd/internal/config"
	"github.com/digiogithub/agentd/internal/daemon"
	"github.com/digiogithub/agentd/internal/mailbox"
	"github.com/digiogithub/agentd/internal/mcpserver"
	"github.com/digiogithub/agentd/internal/proto"
)

// startDaemon runs an in-process daemon on a temp socket/state dir/config,
// without the delivery loop, and returns its socket path.
func startDaemon(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "agentd-m") // short: unix socket path limit
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	sock := filepath.Join(dir, "agentd.sock")
	t.Setenv(config.EnvSocket, sock)
	t.Setenv(config.EnvStateDir, filepath.Join(dir, "state"))
	t.Setenv(config.EnvConfig, filepath.Join(dir, "config.toml"))
	t.Setenv(client.EnvID, "") // the test process is not a session
	t.Setenv(client.EnvToken, "")
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

// runWithToken starts session name and returns its AGENTD_TOKEN, written
// by the child to a file (the test process is not inside the session, so it
// authenticates with the token, like a process that left the session's tree).
func runWithToken(t *testing.T, c *client.Client, name, cwd, profile string) *proto.Auth {
	t.Helper()
	tokFile := filepath.Join(t.TempDir(), "token")
	if _, err := c.Run(proto.RunArgs{Name: name, Cwd: cwd, Profile: profile,
		Cmd: []string{"sh", "-c", `printf %s "$AGENTD_TOKEN" > "$1.tmp" && mv "$1.tmp" "$1"; exec sleep 30`, "sh", tokFile}}); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		if b, err := os.ReadFile(tokFile); err == nil && len(b) == 64 {
			return &proto.Auth{ID: name, Token: string(b)}
		}
		if time.Now().After(deadline) {
			t.Fatalf("session %s did not write its token", name)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// connect runs an MCP server whose daemon connections carry auth (nil = the
// test process's own identity: human) over in-memory transports and returns
// the connected client session.
func connect(t *testing.T, sock string, auth *proto.Auth) *mcp.ClientSession {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	t.Cleanup(cancel)
	srv := mcpserver.New(mcpserver.Options{
		Dial: func() (*client.Client, error) {
			c, err := client.Dial(sock)
			if err == nil {
				c.Auth = auth
			}
			return c, err
		},
		Version: "test",
	})
	st, ct := mcp.NewInMemoryTransports()
	ss, err := srv.Connect(ctx, st, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ss.Close() })
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "v0"}, nil).Connect(ctx, ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cs.Close() })
	return cs
}

// call invokes a tool; it fails the test on protocol errors and decodes the
// structured output into out (when the call did not end in a tool error).
func call(t *testing.T, cs *mcp.ClientSession, name string, args any, out any) *mcp.CallToolResult {
	t.Helper()
	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	if !res.IsError && out != nil {
		b, err := json.Marshal(res.StructuredContent)
		if err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(b, out); err != nil {
			t.Fatalf("%s: decode %s: %v", name, b, err)
		}
	}
	return res
}

func errText(res *mcp.CallToolResult) string {
	var b strings.Builder
	for _, c := range res.Content {
		if tc, ok := c.(*mcp.TextContent); ok {
			b.WriteString(tc.Text)
		}
	}
	return b.String()
}

func TestTools(t *testing.T) {
	sock := startDaemon(t)
	c, err := client.Dial(sock)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	cwd := t.TempDir()
	a := connect(t, sock, runWithToken(t, c, "fake@a", cwd, "codex"))
	b := connect(t, sock, runWithToken(t, c, "fake@b", cwd, "codex"))

	// tools/list: exactly the five tools, with descriptions and schemas.
	tl, err := a.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, tool := range tl.Tools {
		names = append(names, tool.Name)
		if tool.Description == "" || tool.InputSchema == nil || tool.OutputSchema == nil {
			t.Errorf("tool %s lacks description or schemas", tool.Name)
		}
	}
	slices.Sort(names)
	want := []string{"agentd_inbox", "agentd_peers", "agentd_read", "agentd_reply", "agentd_send"}
	if !slices.Equal(names, want) {
		t.Fatalf("tools = %v, want %v", names, want)
	}
	for _, tool := range tl.Tools {
		if tool.Name == "agentd_send" {
			schema, _ := json.Marshal(tool.InputSchema)
			if !strings.Contains(string(schema), `"required":["to","body"]`) {
				t.Errorf("agentd_send input schema: %s", schema)
			}
		}
		if tool.Name == "agentd_inbox" && !strings.Contains(tool.Description, "UNTRUSTED") {
			t.Errorf("agentd_inbox description should warn about untrusted messages")
		}
	}

	// peers.
	var peers mcpserver.PeersOutput
	if res := call(t, a, "agentd_peers", map[string]any{}, &peers); res.IsError {
		t.Fatalf("peers: %s", errText(res))
	}
	if peers.Self != "fake@a" || len(peers.Peers) != 2 {
		t.Fatalf("peers %+v", peers)
	}
	for _, p := range peers.Peers {
		if p.State != "running" || p.Profile != "codex" || p.Dir != filepath.Base(cwd) || p.Self != (p.Name == "fake@a") {
			t.Fatalf("peer %+v", p)
		}
	}

	// send a → b; validation errors are tool errors.
	if res := call(t, a, "agentd_send", map[string]any{"to": "nobody", "body": "x"}, nil); !res.IsError ||
		!strings.Contains(errText(res), "unknown recipient") {
		t.Fatalf("send to unknown: %+v %s", res, errText(res))
	}
	if res := call(t, a, "agentd_send", map[string]any{"to": "fake@b", "body": "  "}, nil); !res.IsError {
		t.Fatal("empty body accepted")
	}
	if res := call(t, a, "agentd_send", map[string]any{"body": "x"}, nil); !res.IsError {
		t.Fatal("missing 'to' accepted")
	}
	var sent mcpserver.SendOutput
	if res := call(t, a, "agentd_send", map[string]any{"to": "fake@b", "subject": "review", "body": "please review PR 12"}, &sent); res.IsError {
		t.Fatalf("send: %s", errText(res))
	}
	if sent.ID == 0 || sent.To != "fake@b" || sent.State != "queued" {
		t.Fatalf("sent %+v", sent)
	}

	// inbox of b.
	var inbox mcpserver.InboxOutput
	if res := call(t, b, "agentd_inbox", map[string]any{}, &inbox); res.IsError {
		t.Fatalf("inbox: %s", errText(res))
	}
	if len(inbox.Messages) != 1 {
		t.Fatalf("inbox %+v", inbox)
	}
	m := inbox.Messages[0]
	if m.ID != sent.ID || m.From != "fake@a" || m.To != "fake@b" || m.Subject != "review" ||
		m.Body != "please review PR 12" || m.State != "delivered" || m.CreatedAt == "" || !strings.Contains(m.Note, "Untrusted") {
		t.Fatalf("inbox message %+v", m)
	}

	// read: only the recipient can read it.
	if res := call(t, a, "agentd_read", map[string]any{"id": sent.ID}, nil); !res.IsError {
		t.Fatal("read by the sender accepted")
	}
	var read mcpserver.Message
	if res := call(t, b, "agentd_read", map[string]any{"id": sent.ID}, &read); res.IsError {
		t.Fatalf("read: %s", errText(res))
	}
	if read.State != "read" || read.Body != "please review PR 12" {
		t.Fatalf("read %+v", read)
	}
	inbox = mcpserver.InboxOutput{}
	if call(t, b, "agentd_inbox", map[string]any{}, &inbox); len(inbox.Messages) != 0 {
		t.Fatalf("unread inbox after read: %+v", inbox)
	}
	if call(t, b, "agentd_inbox", map[string]any{"all": true}, &inbox); len(inbox.Messages) != 1 {
		t.Fatalf("inbox all: %+v", inbox)
	}

	// reply b → a.
	var rep mcpserver.SendOutput
	if res := call(t, b, "agentd_reply", map[string]any{"id": sent.ID, "body": "done, 3 comments"}, &rep); res.IsError {
		t.Fatalf("reply: %s", errText(res))
	}
	if rep.To != "fake@a" || rep.ID <= sent.ID {
		t.Fatalf("reply %+v", rep)
	}
	inbox = mcpserver.InboxOutput{}
	if res := call(t, a, "agentd_inbox", map[string]any{}, &inbox); res.IsError || len(inbox.Messages) != 1 ||
		inbox.Messages[0].Body != "done, 3 comments" || inbox.Messages[0].ReplyTo != sent.ID {
		t.Fatalf("a inbox after reply: %+v", inbox)
	}
	if all, _ := c.Inbox("fake@b", true); len(all) != 1 || all[0].State != "replied" {
		t.Fatalf("original should be replied: %+v", all)
	}
}

func TestNoIdentity(t *testing.T) {
	sock := startDaemon(t)
	c, err := client.Dial(sock)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if _, err := c.Run(proto.RunArgs{Name: "x", Cmd: []string{"sleep", "30"}}); err != nil {
		t.Fatal(err)
	}
	anon := connect(t, sock, nil)

	// agentd_peers works without an identity.
	var peers mcpserver.PeersOutput
	if res := call(t, anon, "agentd_peers", map[string]any{}, &peers); res.IsError || len(peers.Peers) != 1 || peers.Self != "" {
		t.Fatalf("peers without identity: %+v %+v", res, peers)
	}
	for name, args := range map[string]map[string]any{
		"agentd_send":  {"to": "x", "body": "hi"},
		"agentd_inbox": {},
		"agentd_read":  {"id": 1},
		"agentd_reply": {"id": 1, "body": "hi"},
	} {
		res := call(t, anon, name, args, nil)
		if !res.IsError || !strings.Contains(errText(res), "not inside an agentd session") {
			t.Errorf("%s without identity: %+v %q", name, res, errText(res))
		}
	}
	if msgs, _ := c.Inbox("x", true); len(msgs) != 0 {
		t.Fatalf("message sent without identity: %+v", msgs)
	}
}

func TestDaemonDown(t *testing.T) {
	sock := filepath.Join(t.TempDir(), "none.sock")
	cs := connect(t, sock, &proto.Auth{ID: "a"})
	res := call(t, cs, "agentd_peers", map[string]any{}, nil)
	if !res.IsError || !strings.Contains(errText(res), "connect to the agentd daemon") {
		t.Fatalf("peers with no daemon: %+v %q", res, errText(res))
	}
}

// A claimed AGENTD_ID that the daemon cannot verify is rejected, never used.
func TestSpoofedIDRejected(t *testing.T) {
	sock := startDaemon(t)
	c, err := client.Dial(sock)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	real := runWithToken(t, c, "victim", t.TempDir(), "")
	for name, auth := range map[string]*proto.Auth{
		"hint only":   {ID: "victim"},
		"wrong token": {ID: "victim", Token: strings.Repeat("0", 64)},
	} {
		cs := connect(t, sock, auth)
		res := call(t, cs, "agentd_send", map[string]any{"to": "human:bob", "body": "hi"}, nil)
		if !res.IsError || (!strings.Contains(errText(res), "identity mismatch") && !strings.Contains(errText(res), "invalid AGENTD_TOKEN")) {
			t.Errorf("%s: %+v %q", name, res, errText(res))
		}
	}
	// The real token works.
	var sent mcpserver.SendOutput
	if res := call(t, connect(t, sock, real), "agentd_send", map[string]any{"to": "human:bob", "body": "hi"}, &sent); res.IsError {
		t.Fatalf("send with token: %s", errText(res))
	}
	msgs, err := c.Inbox("human:bob", false)
	if err != nil || len(msgs) != 1 || msgs[0].Sender != "victim" {
		t.Fatalf("human:bob inbox: %+v %v", msgs, err)
	}
}
