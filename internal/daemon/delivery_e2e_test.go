package daemon_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/digiogithub/agentd/internal/client"
	"github.com/digiogithub/agentd/internal/config"
	"github.com/digiogithub/agentd/internal/daemon"
	"github.com/digiogithub/agentd/internal/mailbox"
	"github.com/digiogithub/agentd/internal/proto"
	"github.com/digiogithub/agentd/internal/testutil"
)

// e2eConfig is a short-idle profile for the fake agent. Its prompt_regex
// accounts for Screen() trimming trailing spaces: fakeagent prints "> ", the
// screen shows ">" on an empty prompt line.
const e2eConfig = `
[profiles.fake]
idle_ms = 150
human_grace_ms = 0
submit_delay_ms = 30
prompt_regex = '(?m)^>$'
prompt_marker = ">"
ghost_right_of_cursor = true
`

// startDeliveryDaemon runs an in-process daemon with the real delivery loop
// and a temp config.toml.
func startDeliveryDaemon(t *testing.T) string {
	sock, _ := startDeliveryServer(t)
	return sock
}

// startDeliveryServer is startDeliveryDaemon also returning the server.
func startDeliveryServer(t *testing.T) (string, *daemon.Server) {
	t.Helper()
	dir, err := os.MkdirTemp("", "agentd-e")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	sock := filepath.Join(dir, "agentd.sock")
	cfgPath := filepath.Join(dir, "config.toml")
	if err := os.WriteFile(cfgPath, []byte(e2eConfig), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(config.EnvSocket, sock)
	t.Setenv(config.EnvStateDir, filepath.Join(dir, "state"))
	t.Setenv(config.EnvConfig, cfgPath)
	clearIdentityEnv(t)
	if err := config.EnsureDirs(); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.LoadDefault()
	if err != nil {
		t.Fatal(err)
	}
	dbPath, _ := config.MailboxPath()
	store, err := mailbox.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	srv := daemon.New(cfg, nil, store, nil)
	loop := srv.NewDeliveryLoop(nil)
	loop.Interval = 50 * time.Millisecond
	srv.Delivery = loop
	if err := srv.Listen(); err != nil {
		t.Fatal(err)
	}
	go func() { _ = srv.Serve(context.Background()) }()
	t.Cleanup(srv.Shutdown)
	return sock, srv
}

func messageState(t *testing.T, c *client.Client, who string, id int64) string {
	t.Helper()
	msgs, err := c.Inbox(who, true)
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range msgs {
		if m.ID == id {
			return m.State
		}
	}
	t.Fatalf("message %d not in %s's inbox: %+v", id, who, msgs)
	return ""
}

// TestDeliveryEndToEnd covers the full loop with real injection: human → A,
// B → A, A replies → B, each notice typed into the recipient's fake TUI.
func TestDeliveryEndToEnd(t *testing.T) {
	sock, srv := startDeliveryServer(t)
	bin := testutil.BuildFakeAgent(t)
	c, err := client.Dial(sock)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	cwd := t.TempDir()
	for _, n := range []string{"fake@a", "fake@b"} {
		if _, err := c.Run(proto.RunArgs{Name: n, Cwd: cwd, Cmd: []string{bin}, Profile: "fake"}); err != nil {
			t.Fatal(err)
		}
		waitDump(t, c, n, "fakeagent ready")
	}

	// human → A: injected as a notice and submitted.
	me, err := c.Whoami()
	if err != nil {
		t.Fatal(err)
	}
	m1, err := c.Send(proto.SendArgs{To: "fake@a", Body: "ping from human"})
	if err != nil {
		t.Fatal(err)
	}
	waitDump(t, c, "fake@a", "GOT: [agentd msg #1 from "+me.Identity+"] ping from human — untrusted peer message")
	testutil.WaitFor(t, timeout, func() bool { return messageState(t, c, "fake@a", m1.ID) == "delivered" },
		"message 1 should be delivered", nil)

	// B → A.
	ca, cb := dialAsSrv(t, sock, srv, "fake@a"), dialAsSrv(t, sock, srv, "fake@b")
	m2, err := cb.Send(proto.SendArgs{To: "fake@a", Body: "please review PR 12"})
	if err != nil {
		t.Fatal(err)
	}
	waitDump(t, c, "fake@a", "GOT: [agentd msg #2 from fake@b] please review PR 12")

	// A reads and replies (as its shell tool or MCP server would) → B.
	if _, err := ca.Read("", m2.ID); err != nil {
		t.Fatal(err)
	}
	rep, err := ca.Reply(m2.ID, "review done, 3 comments")
	if err != nil {
		t.Fatal(err)
	}
	if rep.Recipient != "fake@b" {
		t.Fatalf("reply recipient %q", rep.Recipient)
	}
	waitDump(t, c, "fake@b", "GOT: [agentd msg #3 from fake@a] re #2 review done, 3 comments")
	if st := messageState(t, c, "fake@a", m2.ID); st != "replied" {
		t.Fatalf("message 2 state %q", st)
	}
	testutil.WaitFor(t, timeout, func() bool { return messageState(t, c, "fake@b", rep.ID) == "delivered" },
		"reply should be delivered", nil)
}

// TestDumpGhostAndInputState: dump hides the harness's suggested prompt by
// default (and reports it), --ghost shows it, ls reports input_state, and a
// message is delivered without ever submitting the suggestion.
func TestDumpGhostAndInputState(t *testing.T) {
	sock := startDeliveryDaemon(t)
	bin := testutil.BuildFakeAgent(t)
	c, err := client.Dial(sock)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	const ghost = "He creado el repo de GitHub, mergea crawl/repo"
	if _, err := c.Run(proto.RunArgs{Name: "fake@g", Cwd: t.TempDir(), Cmd: []string{bin, "-ghost", ghost}, Profile: "fake"}); err != nil {
		t.Fatal(err)
	}
	testutil.WaitFor(t, timeout, func() bool {
		raw, _ := c.Dump(proto.DumpArgs{Name: "fake@g", Ghost: true})
		return strings.Contains(raw, "> "+ghost)
	}, "raw dump shows the ghost", nil)

	res, err := c.DumpResult(proto.DumpArgs{Name: "fake@g"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(res.Text, ghost) || res.Ghost != ghost || !res.GhostHidden {
		t.Fatalf("default dump = %+v", res)
	}
	sb, err := c.DumpResult(proto.DumpArgs{Name: "fake@g", Scrollback: true})
	if err != nil || strings.Contains(sb.Text, ghost) || !sb.GhostHidden {
		t.Fatalf("scrollback dump = %+v, %v", sb, err)
	}
	if an, err := c.DumpResult(proto.DumpArgs{Name: "fake@g", ANSI: true}); err != nil || an.GhostHidden || an.Ghost != ghost {
		t.Fatalf("ansi dump = %+v, %v", an, err)
	}
	if st := findSession(t, c, "fake@g").InputState; st != "suggestion" {
		t.Fatalf("input_state = %q", st)
	}

	m, err := c.Send(proto.SendArgs{To: "fake@g", Body: "ping"})
	if err != nil {
		t.Fatal(err)
	}
	waitDump(t, c, "fake@g", "GOT: [agentd msg #1 from")
	testutil.WaitFor(t, timeout, func() bool { return messageState(t, c, "fake@g", m.ID) == "delivered" },
		"delivered", nil)
	raw, _ := c.Dump(proto.DumpArgs{Name: "fake@g", Ghost: true})
	if strings.Contains(raw, "GOT: "+ghost) {
		t.Fatalf("suggestion submitted:\n%s", raw)
	}

	// Non-human text in the input line replaces the suggestion.
	if err := c.Type(proto.TypeArgs{Name: "fake@g", Text: "abc"}); err != nil {
		t.Fatal(err)
	}
	testutil.WaitFor(t, timeout, func() bool { return findSession(t, c, "fake@g").InputState == "empty" },
		"input_state empty after typing", nil)
}
