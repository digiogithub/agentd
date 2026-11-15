package core_test

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/digiogithub/agentd/internal/client"
	"github.com/digiogithub/agentd/internal/desktopui/backend"
	"github.com/digiogithub/agentd/internal/desktopui/core"
	"github.com/digiogithub/agentd/internal/desktopui/remote"
	"github.com/digiogithub/agentd/internal/proto"
)

func TestLocalSessionsViewAct(t *testing.T) {
	dir := isolate(t)
	sock := startDaemon(t)
	b := newBackend(t, sshOpts(dir, sock))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sessions := b.WatchSessions(ctx)
	runCat(t, sock, "cat@proj", nil)
	runCat(t, sock, "other", map[string]string{"agentd.harness": "codex", "herdr.space": "webapp"})
	ss := waitSessions(t, sessions, "two agentd sessions", func(ss []backend.Session) bool {
		_, a := find(ss, "local/agentd/cat@proj")
		_, b := find(ss, "local/agentd/other")
		return a && b
	})
	s, _ := find(ss, "local/agentd/cat@proj")
	if s.Harness != "cat" || s.Project != "proj" || s.Info == nil || s.Info.Name != "cat@proj" {
		t.Fatalf("mapped session: %+v", s)
	}
	o, _ := find(ss, "local/agentd/other")
	if o.Harness != "codex" || o.Project != "webapp" {
		t.Fatalf("labels: %+v", o)
	}

	views, err := b.WatchView(ctx, s)
	if err != nil {
		t.Fatal(err)
	}
	first := <-views
	if first.Name != "cat@proj" || first.Epoch == "" {
		t.Fatalf("first view: %+v", first)
	}
	res, err := b.Act(ctx, s, proto.ActArgs{Action: proto.ActKeys, Keys: []string{"h", "i", "enter"}})
	if err != nil || res.Result != "keys_sent" {
		t.Fatalf("act: %+v %v", res, err)
	}
	// The view stream reports the new screen.
	deadline := time.After(timeout)
	for found := false; !found; {
		select {
		case v := <-views:
			for _, l := range v.Lines {
				if strings.Contains(l.Text, "hi") {
					found = true
				}
			}
		case <-deadline:
			t.Fatal("view never showed the keys")
		}
	}
	// Errors keep the daemon code.
	_, err = b.Act(ctx, s, proto.ActArgs{Action: proto.ActChoose, OptionID: "x"})
	if client.ErrorCode(err) != proto.CodeInvalid {
		t.Fatalf("choose without revision: %v (code %q)", err, client.ErrorCode(err))
	}
	if err := b.ViewAck(ctx, s); err != nil {
		t.Fatal(err)
	}

	// Mailbox passthrough.
	msg, err := b.Send(ctx, backend.LocalHostID, proto.SendArgs{To: "cat@proj", Subject: "s", Body: "hello"})
	if err != nil || msg.ID == 0 {
		t.Fatalf("send: %+v %v", msg, err)
	}
	if _, err := b.Inbox(ctx, backend.LocalHostID); err != nil {
		t.Fatal(err)
	}
	if _, err := b.Send(ctx, backend.LocalHostID, proto.SendArgs{To: "queue:jobs", Body: "task"}); err != nil {
		t.Fatal(err)
	}
	qs, err := b.Queues(ctx, backend.LocalHostID)
	if err != nil || len(qs) != 1 || qs[0].Name != "jobs" || qs[0].Unclaimed != 1 {
		t.Fatalf("queues: %+v %v", qs, err)
	}
	if ps, err := b.Projects(ctx, backend.LocalHostID); err != nil || ps == nil {
		t.Fatalf("projects: %v %v", ps, err)
	}

	// Kill: the exit shows up in the list.
	if err := b.Kill(ctx, s); err != nil {
		t.Fatal(err)
	}
	waitSessions(t, sessions, "exited", func(ss []backend.Session) bool {
		x, ok := find(ss, "local/agentd/cat@proj")
		return ok && x.Status == "exited"
	})
	// A non-agentd session cannot be viewed.
	if _, err := b.WatchView(ctx, backend.Session{HostID: "local", Source: backend.SourceTmux, Name: "x"}); !errors.Is(err, core.ErrUnsupported) {
		t.Fatalf("WatchView tmux: %v", err)
	}
}

func TestRemoteHostOverSSH(t *testing.T) {
	dir := isolate(t)
	sock := startDaemon(t) // the "remote" daemon: reached through the SSH server
	opts := sshOpts(dir, sock+".nolocal")
	opts.SocketProbe = "" // the real probe: $AGENTD_SOCKET (the test daemon) comes first
	b := newBackend(t, opts)
	srv := sshHost(t, b)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	hosts := b.WatchHosts(ctx)
	if err := b.Connect(ctx, "box"); err != nil {
		t.Fatal(err)
	}
	hs := waitHosts(t, hosts, "box online with agentd", func(hs []backend.HostStatus) bool {
		h, ok := hostStatus(hs, "box")
		return ok && h.State == backend.HostOnline && h.AgentdVer != ""
	})
	if hs[0].Host.ID != backend.LocalHostID {
		t.Fatalf("local not first: %+v", hs)
	}
	if l, _ := hostStatus(hs, "local"); l.AgentdVer != "" {
		t.Fatalf("local agentd should be missing: %+v", l)
	}
	// TOFU stored the fingerprint.
	h, err := remote.NewHostStore(dir).Get("box")
	if err != nil || !strings.HasPrefix(h.HostKey, "SHA256:") {
		t.Fatalf("host key not stored: %+v %v", h, err)
	}

	sessions := b.WatchSessions(ctx)
	runCat(t, sock, "cat@remote", nil)
	ss := waitSessions(t, sessions, "remote session", func(ss []backend.Session) bool {
		_, ok := find(ss, "box/agentd/cat@remote")
		return ok
	})
	s, _ := find(ss, "box/agentd/cat@remote")

	// Terminal: attach, type, resize, then kill gives the exit code.
	term, err := b.Terminal(ctx, s, 100, 30, false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := term.Write([]byte("ping-remote\r")); err != nil {
		t.Fatal(err)
	}
	readUntil(t, term, "ping-remote")
	if err := term.Resize(90, 20); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "resize", func() bool {
		c := dialDaemon(t, sock)
		ls, _ := c.Ls()
		for _, si := range ls {
			if si.Name == "cat@remote" {
				return si.Cols == 90 && si.Rows == 20
			}
		}
		return false
	})
	if err := b.Kill(ctx, s); err != nil {
		t.Fatal(err)
	}
	waitDone := make(chan int, 1)
	go func() {
		buf := make([]byte, 1024)
		for {
			if _, err := term.Read(buf); err != nil {
				break
			}
		}
		code, _ := term.Wait()
		waitDone <- code
	}()
	select {
	case code := <-waitDone:
		if code < 0 {
			t.Fatalf("exit code %d", code)
		}
	case <-time.After(timeout):
		t.Fatal("terminal did not end after kill")
	}
	_ = term.Close()

	// Mailbox over SSH: the daemon sees the human user.
	if _, err := b.Send(ctx, "box", proto.SendArgs{To: "cat@remote", Body: "x"}); err != nil {
		t.Fatal(err)
	}
	if _, err := b.Inbox(ctx, "box"); err != nil {
		t.Fatal(err)
	}

	// The SSH connection drops: the host reconnects and the list comes back.
	srv.DropConns()
	waitHosts(t, hosts, "box back online", func(hs []backend.HostStatus) bool {
		h, ok := hostStatus(hs, "box")
		return ok && h.State == backend.HostOnline && h.Sessions >= 1
	})
	if err := b.RemoveHost(ctx, "box"); err != nil {
		t.Fatal(err)
	}
	waitSessions(t, sessions, "box sessions gone", func(ss []backend.Session) bool {
		_, ok := find(ss, "box/agentd/cat@remote")
		return !ok
	})
}

func TestNoAgentdHost(t *testing.T) {
	dir := isolate(t)
	b := newBackend(t, sshOpts(dir, "/nonexistent/agentd.sock"))
	sshHost(t, b)
	ctx := context.Background()
	if err := b.Connect(ctx, "box"); err != nil {
		t.Fatal(err)
	}
	ps, err := b.Projects(ctx, "box")
	if err != nil || len(ps) != 0 {
		t.Fatalf("projects without agentd: %v %v", ps, err)
	}
	if _, err := b.Inbox(ctx, "box"); !errors.Is(err, core.ErrNoAgentd) {
		t.Fatalf("inbox without agentd: %v", err)
	}
}

func TestHostKeyMismatchSurfaces(t *testing.T) {
	dir := isolate(t)
	b := newBackend(t, sshOpts(dir, "/nonexistent"))
	srv := sshHost(t, b)
	h := backend.Host{ID: "box", Name: "box", Addr: srv.Host, Port: srv.Port, Auth: backend.AuthPassword, HostKey: "SHA256:old"}
	if err := b.SaveHost(context.Background(), h); err != nil {
		t.Fatal(err)
	}
	err := b.Connect(context.Background(), "box")
	var mm *remote.HostKeyMismatch
	if !errors.As(err, &mm) {
		t.Fatalf("want HostKeyMismatch, got %v", err)
	}
	hosts := b.WatchHosts(context.Background())
	waitHosts(t, hosts, "box in error", func(hs []backend.HostStatus) bool {
		h, ok := hostStatus(hs, "box")
		return ok && h.State == backend.HostError && strings.Contains(h.Err, "changed")
	})
}

// ---- mux aggregation ----

type fakeMux struct {
	src   backend.Source
	mu    sync.Mutex
	list  []backend.Session
	lists atomic.Int32
	kills []string
}

func (f *fakeMux) Source() backend.Source { return f.src }
func (f *fakeMux) List(ctx context.Context) ([]backend.Session, error) {
	f.lists.Add(1)
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]backend.Session(nil), f.list...), nil
}
func (f *fakeMux) Attach(ctx context.Context, name string, cols, rows int) (backend.PTY, error) {
	return nil, errors.New("fake attach")
}
func (f *fakeMux) Launch(ctx context.Context, spec backend.LaunchSpec) (backend.Session, error) {
	s := backend.Session{Name: "new-" + spec.Harness, Status: "unknown", Updated: time.Now()}
	f.mu.Lock()
	f.list = append(f.list, s)
	f.mu.Unlock()
	return s, nil
}
func (f *fakeMux) Kill(ctx context.Context, name string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.kills = append(f.kills, name)
	return nil
}

func TestSessionAggregationWithMux(t *testing.T) {
	dir := isolate(t)
	sock := startDaemon(t)
	now := time.Now()
	z := &fakeMux{src: backend.SourceZellij, list: []backend.Session{
		{Name: "old", Status: "idle", Updated: now.Add(-time.Hour)},
		{Name: "busy", Status: "working", Updated: now.Add(-2 * time.Hour)},
	}}
	tm := &fakeMux{src: backend.SourceTmux, list: []backend.Session{
		{Name: "asks", Status: "waiting", Attention: true, Updated: now.Add(-3 * time.Hour)},
		{Name: "fresh", Status: "idle", Updated: now.Add(time.Hour)},
	}}
	var factoryHosts []string
	var fmu sync.Mutex
	opts := sshOpts(dir, sock)
	opts.Mux = func(sh backend.Shell, hostID string) []core.MuxAdapter {
		fmu.Lock()
		factoryHosts = append(factoryHosts, hostID)
		fmu.Unlock()
		return []core.MuxAdapter{z, tm}
	}
	b := newBackend(t, opts)
	ctx, cancel := context.WithCancel(context.Background())
	sessions := b.WatchSessions(ctx)
	ss := waitSessions(t, sessions, "mux sessions", func(ss []backend.Session) bool { return len(ss) == 4 })
	var order []string
	for _, s := range ss {
		order = append(order, string(s.Source)+"/"+s.Name)
		if s.HostID != "local" {
			t.Fatalf("host id not set: %+v", s)
		}
	}
	if got := strings.Join(order, ","); got != "tmux/asks,zellij/busy,tmux/fresh,zellij/old" {
		t.Fatalf("order: %s", got)
	}
	// Launch and Kill go to the adapter.
	ns, err := b.Launch(ctx, backend.LaunchSpec{HostID: "local", Where: backend.SourceTmux, Harness: "claude"})
	if err != nil || ns.Source != backend.SourceTmux || ns.HostID != "local" {
		t.Fatalf("mux launch: %+v %v", ns, err)
	}
	waitSessions(t, sessions, "launched tmux", func(ss []backend.Session) bool {
		_, ok := find(ss, "local/tmux/new-claude")
		return ok
	})
	if err := b.Kill(ctx, ns); err != nil || len(tm.kills) != 1 {
		t.Fatalf("mux kill: %v %v", err, tm.kills)
	}
	// An agentd session joins the same list.
	runCat(t, sock, "cat@agg", nil)
	waitSessions(t, sessions, "agentd + mux", func(ss []backend.Session) bool {
		_, ok := find(ss, "local/agentd/cat@agg")
		return ok && len(ss) == 6
	})
	// Polling stops when nobody watches.
	cancel()
	time.Sleep(300 * time.Millisecond)
	n := z.lists.Load()
	time.Sleep(500 * time.Millisecond)
	if z.lists.Load() != n {
		t.Fatalf("mux still polled without watchers: %d -> %d", n, z.lists.Load())
	}
	fmu.Lock()
	defer fmu.Unlock()
	if len(factoryHosts) != 1 {
		t.Fatalf("adapters rebuilt: %v", factoryHosts)
	}
}

func TestShellSessions(t *testing.T) {
	dir := isolate(t)
	b := newBackend(t, sshOpts(dir, "/nonexistent"))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sessions := b.WatchSessions(ctx)
	s, err := b.Launch(ctx, backend.LaunchSpec{HostID: "local", Where: backend.SourceShell, Project: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	waitSessions(t, sessions, "shell listed", func(ss []backend.Session) bool {
		x, ok := find(ss, s.Key())
		return ok && x.Status == "unknown" && x.Source == backend.SourceShell
	})
	term, err := b.Terminal(ctx, s, 80, 24, false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := term.Write([]byte("echo out-$((40+2))\r")); err != nil {
		t.Fatal(err)
	}
	readUntil(t, term, "out-42")
	if err := term.Resize(100, 30); err != nil {
		t.Fatal(err)
	}
	// Detach and re-attach: the backlog comes back.
	_ = term.Close()
	if code, _ := term.Wait(); code != -1 {
		t.Fatalf("detached wait: %d", code)
	}
	term2, err := b.Terminal(ctx, s, 80, 24, false)
	if err != nil {
		t.Fatal(err)
	}
	readUntil(t, term2, "out-42")
	if _, err := term2.Write([]byte("exit 3\r")); err != nil {
		t.Fatal(err)
	}
	if code, _ := term2.Wait(); code != 3 {
		t.Fatalf("shell exit code %d", code)
	}
	waitSessions(t, sessions, "shell gone", func(ss []backend.Session) bool {
		_, ok := find(ss, s.Key())
		return !ok
	})
}

func TestShellSessionOverSSHAndKill(t *testing.T) {
	dir := isolate(t)
	b := newBackend(t, sshOpts(dir, "/nonexistent"))
	sshHost(t, b)
	ctx := context.Background()
	s, err := b.Launch(ctx, backend.LaunchSpec{HostID: "box", Where: backend.SourceShell})
	if err != nil {
		t.Fatal(err)
	}
	term, err := b.Terminal(ctx, s, 80, 24, false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := term.Write([]byte("echo ssh-$((1+1))\r")); err != nil {
		t.Fatal(err)
	}
	readUntil(t, term, "ssh-2")
	if err := b.Kill(ctx, s); err != nil {
		t.Fatal(err)
	}
	if _, err := term.Wait(); err != nil {
		t.Fatal(err)
	}
}

// ---- provisioning passthrough ----

type fakeProv struct{ installs atomic.Int32 }

func (p *fakeProv) Detect(ctx context.Context, sh backend.Shell) (*backend.Capabilities, error) {
	res, err := sh.Exec(ctx, "printf %s \"$HOME\"", nil)
	if err != nil {
		return nil, err
	}
	return &backend.Capabilities{OS: "linux", Home: res.Stdout, Tools: map[string]backend.Tool{"agentd": {Name: "agentd"}}}, nil
}
func (p *fakeProv) Plan(caps *backend.Capabilities, opts backend.ProvisionOptions) []backend.PlannedStep {
	return []backend.PlannedStep{{Step: "install", Title: "Install agentd"}}
}
func (p *fakeProv) Install(ctx context.Context, sh backend.Shell, caps *backend.Capabilities, opts backend.ProvisionOptions) <-chan backend.ProvisionEvent {
	p.installs.Add(1)
	ch := make(chan backend.ProvisionEvent, 2)
	ch <- backend.ProvisionEvent{Step: "install", Message: "ok"}
	ch <- backend.ProvisionEvent{Step: "install", Done: true}
	close(ch)
	return ch
}

func TestProvisioning(t *testing.T) {
	dir := isolate(t)
	opts := sshOpts(dir, "/nonexistent")
	opts.Provisioner = &fakeProv{}
	b := newBackend(t, opts)
	sshHost(t, b)
	ctx := context.Background()
	caps, err := b.Detect(ctx, "box")
	if err != nil || caps.OS != "linux" || caps.Home == "" {
		t.Fatalf("detect: %+v %v", caps, err)
	}
	hosts := b.WatchHosts(ctx)
	waitHosts(t, hosts, "caps cached", func(hs []backend.HostStatus) bool {
		h, ok := hostStatus(hs, "box")
		return ok && h.Capabilities != nil && h.Capabilities.OS == "linux"
	})
	if steps := b.Plan(caps, backend.ProvisionOptions{Agentd: true}); len(steps) != 1 {
		t.Fatalf("plan: %+v", steps)
	}
	evs, err := b.Install(ctx, "box", backend.ProvisionOptions{Agentd: true})
	if err != nil {
		t.Fatal(err)
	}
	var got []backend.ProvisionEvent
	for ev := range evs {
		got = append(got, ev)
	}
	if len(got) != 2 || !got[1].Done {
		t.Fatalf("events: %+v", got)
	}
	// Without a provisioner: a clear error.
	b2 := newBackend(t, sshOpts(dir, "/nonexistent"))
	if _, err := b2.Detect(ctx, "local"); !errors.Is(err, core.ErrUnsupported) {
		t.Fatalf("no provisioner: %v", err)
	}
}

func TestPublicKeyAndHosts(t *testing.T) {
	dir := isolate(t)
	b := newBackend(t, sshOpts(dir, "/nonexistent"))
	ctx := context.Background()
	line, err := b.PublicKey(ctx)
	if err != nil || !strings.HasSuffix(line, " agentd-desktop") {
		t.Fatalf("public key: %q %v", line, err)
	}
	if err := b.SaveHost(ctx, backend.Host{ID: "local", Addr: "x"}); !errors.Is(err, remote.ErrLocalHost) {
		t.Fatalf("save local: %v", err)
	}
	if err := b.SaveHost(ctx, backend.Host{Name: "A", Addr: "a.example"}); err != nil {
		t.Fatal(err)
	}
	hosts := b.WatchHosts(ctx)
	hs := waitHosts(t, hosts, "two hosts", func(hs []backend.HostStatus) bool { return len(hs) == 2 })
	if hs[1].Host.ID != "a" || hs[1].State != backend.HostOffline {
		t.Fatalf("saved host: %+v", hs[1])
	}
	if err := b.RemoveHost(ctx, "a"); err != nil {
		t.Fatal(err)
	}
	waitHosts(t, hosts, "one host", func(hs []backend.HostStatus) bool { return len(hs) == 1 })
}
