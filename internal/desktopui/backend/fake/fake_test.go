package fake

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/digiogithub/agentd/internal/desktopui/backend"
	"github.com/digiogithub/agentd/internal/proto"
)

func TestMockupData(t *testing.T) {
	f := New()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	hs := <-f.WatchHosts(ctx)
	ss := <-f.WatchSessions(ctx)
	if len(hs) != 5 || len(ss) != 8 {
		t.Fatalf("hosts %d sessions %d", len(hs), len(ss))
	}
	v := f.View("codex@api")
	if v == nil || len(v.Options) != 3 || v.Dialog == nil {
		t.Fatalf("codex@api view %+v", v)
	}
}

func TestActRevisionsAndConflicts(t *testing.T) {
	f := New()
	f.Delay = 0
	s, _ := f.SessionByName("claude@webapp")
	if _, err := f.Act(context.Background(), s, proto.ActArgs{Action: "choose", OptionID: "w1"}); proto.CodeOf(err) != proto.CodeInvalid {
		t.Errorf("choose without revision: %v", err)
	}
	if _, err := f.Act(context.Background(), s, proto.ActArgs{Action: "choose", OptionID: "w1", Revision: 30, Epoch: Epoch}); proto.CodeOf(err) != proto.CodeConflict {
		t.Errorf("stale revision: %v", err)
	}
	res, err := f.Act(context.Background(), s, proto.ActArgs{Action: "choose", OptionID: "w1", Revision: 31, Epoch: Epoch})
	if err != nil || res.Result != "chosen" {
		t.Fatalf("choose: %+v %v", res, err)
	}
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) && f.View("claude@webapp").Status != "idle" {
		time.Sleep(5 * time.Millisecond)
	}
	if v := f.View("claude@webapp"); v.Status != "idle" || v.Revision <= 31 || v.Dialog != nil {
		t.Errorf("after choose %+v", v)
	}
}

func TestWatchViewStreamsChanges(t *testing.T) {
	f := New()
	f.Delay = 0
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s, _ := f.SessionByName("claude@docs")
	ch, err := f.WatchView(ctx, s)
	if err != nil {
		t.Fatal(err)
	}
	first := <-ch
	if _, err := f.Act(ctx, s, proto.ActArgs{Action: "prompt", Text: "hola"}); err != nil {
		t.Fatal(err)
	}
	for v := range ch {
		if v.Revision > first.Revision && strings.Contains(v.Lines[len(v.Lines)-1].Text, "hola") {
			return
		}
	}
	t.Error("no change streamed")
}

func TestTerminalEchoAndObserve(t *testing.T) {
	f := New()
	s, _ := f.SessionByName("claude@webapp")
	p, err := f.Terminal(context.Background(), s, 160, 50, false)
	if err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 4096)
	n, _ := io.ReadAtLeast(p, buf, 10)
	if !strings.Contains(string(buf[:n]), "Bash(go test") {
		t.Errorf("screen %q", buf[:n])
	}
	_, _ = p.Write([]byte("hi\r"))
	got := ""
	for !strings.Contains(got, "hi\r\n") {
		n, err := p.Read(buf)
		if err != nil {
			t.Fatal(err)
		}
		got += string(buf[:n])
	}
	_ = p.Close()
	o, _ := f.Terminal(context.Background(), s, 160, 50, true)
	_, _ = o.Write([]byte("nope"))
	_ = o.Close()
	if f.Typed() != "hi\r" {
		t.Errorf("typed %q", f.Typed())
	}
}

func TestInstallEventsAndHostKey(t *testing.T) {
	f := New()
	f.Delay = 0
	ch, err := f.Install(context.Background(), "portatil", backend.ProvisionOptions{Agentd: true, CLIs: []string{"codex"}})
	if err != nil {
		t.Fatal(err)
	}
	var done int
	for e := range ch {
		if e.Done {
			done++
		}
	}
	if done != 5 {
		t.Errorf("done events %d", done)
	}
	if c, _ := f.Detect(context.Background(), "portatil"); !c.Has("agentd") || !c.Has("codex") {
		t.Errorf("caps after install %+v", c.Tools)
	}
	h := backend.Host{ID: "n", Name: "n", Addr: "n.local", User: "u"}
	_ = f.SaveHost(context.Background(), h)
	err = f.Connect(context.Background(), "n")
	hk, ok := err.(*backend.HostKeyError)
	if !ok || !strings.HasPrefix(hk.HostKeyFingerprint(), "SHA256:") || hk.HostID != "n" {
		t.Fatalf("connect: %v", err)
	}
	if err := f.TrustHostKey(context.Background(), "n", hk.Fingerprint); err != nil {
		t.Fatal(err)
	}
	if err := f.TrustHostKey(context.Background(), "n", "SHA256:other"); !errors.Is(err, backend.ErrHostKeyChanged) {
		t.Errorf("replace key: %v", err)
	}
	if err := f.Connect(context.Background(), "n"); err != nil {
		t.Error(err)
	}
}

func TestPasswordConnect(t *testing.T) {
	f := New()
	f.Delay = 0
	ctx := context.Background()
	h := backend.Host{ID: "p", Name: "p", Addr: "p.local", User: "u", Auth: backend.AuthPassword}
	_ = f.SaveHost(ctx, h)
	var hk *backend.HostKeyError
	if err := f.Connect(ctx, "p"); !errors.As(err, &hk) {
		t.Fatalf("connect: %v", err)
	}
	_ = f.TrustHostKey(ctx, "p", hk.Fingerprint)
	if err := f.Connect(ctx, "p"); !errors.Is(err, backend.ErrPasswordRequired) {
		t.Fatalf("connect without password: %v", err)
	}
	if err := f.ConnectWithPassword(ctx, "p", "nope", true); err == nil {
		t.Fatal("wrong password accepted")
	}
	if err := f.ConnectWithPassword(ctx, "p", FakePassword, true); err != nil {
		t.Fatal(err)
	}
	if k := f.KeysInstalled(); len(k) != 1 || k[0] != "p" {
		t.Errorf("keys installed %v", k)
	}
	if err := f.Connect(ctx, "p"); err != nil {
		t.Errorf("connect on the app key: %v", err)
	}
}

func TestListDirTreeLocksAndHidden(t *testing.T) {
	f := New()
	ctx := context.Background()
	l, err := f.ListDir(ctx, "estudio", "", false)
	if err != nil || l.Path != "/home/jose" || l.Home != "/home/jose" {
		t.Fatalf("home: %+v %v", l, err)
	}
	var names []string
	locked := ""
	for _, e := range l.Entries {
		names = append(names, e.Name)
		if !e.Enterable {
			locked = e.Name
		}
	}
	if strings.Join(names, ",") != "Documentos,private,src" || locked != "private" {
		t.Errorf("entries %v locked %q", names, locked)
	}
	if l, _ = f.ListDir(ctx, "estudio", "~", true); len(l.Entries) != 4 || l.Entries[0].Name != ".config" {
		t.Errorf("hidden: %+v", l.Entries)
	}
	if l, err = f.ListDir(ctx, "estudio", "/", false); err != nil || len(l.Entries) == 0 || l.Path != "/" {
		t.Errorf("root: %+v %v", l, err)
	}
	if _, err = f.ListDir(ctx, "estudio", "/root", false); !errors.Is(err, backend.ErrDirDenied) {
		t.Errorf("/root: %v", err)
	}
	if _, err = f.ListDir(ctx, "estudio", "/nope", false); !errors.Is(err, backend.ErrDirNotFound) {
		t.Errorf("/nope: %v", err)
	}
	if _, err = f.ListDir(ctx, "raspberry", "", false); err == nil {
		t.Errorf("offline host listed")
	}
}

func TestTerminateRemovesTheSession(t *testing.T) {
	f := New()
	ctx := context.Background()
	s := backend.Session{HostID: "estudio", Source: backend.SourceAgentd, Name: "claude@docs"}
	if err := f.Terminate(ctx, s); err != nil {
		t.Fatal(err)
	}
	if got := f.Terminated(); len(got) != 1 || got[0] != s.Key() {
		t.Errorf("terminated %v", got)
	}
	if ss := <-f.WatchSessions(ctx); len(ss) != 7 {
		t.Errorf("%d sessions after terminate", len(ss))
	}
	if err := f.Terminate(ctx, s); proto.CodeOf(err) != proto.CodeNotFound {
		t.Errorf("second terminate: %v", err)
	}
}
