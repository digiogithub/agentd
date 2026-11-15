package core_test

import (
	"context"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/digiogithub/agentd/internal/desktopui/backend"
	"github.com/digiogithub/agentd/internal/proto"
)

// fakeDaemon speaks just enough of the socket protocol (ping, ls, subscribe)
// to drive the event stream by hand.
type fakeDaemon struct {
	sock string

	mu       sync.Mutex
	sessions []proto.SessionInfo
	subs     []chan proto.Event
	subArgs  []proto.SubscribeArgs
	lsCalls  int
	conns    []net.Conn
}

func newFakeDaemon(t *testing.T) *fakeDaemon {
	t.Helper()
	d, err := os.MkdirTemp("", "fd")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(d) })
	f := &fakeDaemon{sock: filepath.Join(d, "s.sock")}
	ln, err := net.Listen("unix", f.sock)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = ln.Close()
		f.dropAll()
	})
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			f.mu.Lock()
			f.conns = append(f.conns, c)
			f.mu.Unlock()
			go f.serve(c)
		}
	}()
	return f
}

func (f *fakeDaemon) dropAll() {
	f.mu.Lock()
	conns := f.conns
	f.conns = nil
	f.mu.Unlock()
	for _, c := range conns {
		_ = c.Close()
	}
}

func (f *fakeDaemon) setSessions(ss ...proto.SessionInfo) {
	f.mu.Lock()
	f.sessions = ss
	f.mu.Unlock()
}

// emit sends ev to every open subscription.
func (f *fakeDaemon) emit(ev proto.Event) {
	f.mu.Lock()
	subs := f.subs
	f.mu.Unlock()
	for _, ch := range subs {
		ch <- ev
	}
}

func (f *fakeDaemon) serve(c net.Conn) {
	defer c.Close()
	codec := proto.NewCodec(c)
	for {
		req, err := codec.ReadRequest()
		if err != nil {
			return
		}
		var resp proto.Response
		switch req.Op {
		case proto.OpPing:
			resp, _ = proto.OKResponse(proto.PingResult{Version: "fake-1", Epoch: "E1"})
		case proto.OpLs:
			f.mu.Lock()
			f.lsCalls++
			ss := append([]proto.SessionInfo(nil), f.sessions...)
			f.mu.Unlock()
			resp, _ = proto.OKResponse(proto.LsResult{Sessions: ss})
		case proto.OpSubscribe:
			var a proto.SubscribeArgs
			_ = req.DecodeArgs(&a)
			ch := make(chan proto.Event, 16)
			f.mu.Lock()
			f.subArgs = append(f.subArgs, a)
			f.subs = append(f.subs, ch)
			ss := append([]proto.SessionInfo(nil), f.sessions...)
			f.mu.Unlock()
			resp, _ = proto.OKResponse(proto.SubscribeResult{Epoch: "E1", Seq: 10, Sessions: ss})
			if codec.WriteResponse(resp) != nil {
				return
			}
			for ev := range ch {
				if codec.WriteJSON(ev) != nil {
					return
				}
			}
			return
		default:
			resp = proto.ErrorResponse(proto.Errorf(proto.CodeInvalid, "fake: %s", req.Op))
		}
		if codec.WriteResponse(resp) != nil {
			return
		}
	}
}

func rawJSON(v any) json.RawMessage {
	b, _ := json.Marshal(v)
	return b
}

func TestSubscribeEventsResyncAndReconnect(t *testing.T) {
	dir := isolate(t)
	fd := newFakeDaemon(t)
	fd.setSessions(proto.SessionInfo{Name: "a@p", State: "running", Status: "idle", StartedAt: time.Now()})
	b := newBackend(t, sshOpts(dir, fd.sock))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sessions := b.WatchSessions(ctx)
	waitSessions(t, sessions, "snapshot", func(ss []backend.Session) bool {
		s, ok := find(ss, "local/agentd/a@p")
		return ok && s.Status == "idle"
	})
	waitFor(t, "subscription", func() bool {
		fd.mu.Lock()
		defer fd.mu.Unlock()
		return len(fd.subs) == 1
	})

	// Events update the list without an ls.
	fd.emit(proto.Event{Seq: 11, Epoch: "E1", Kind: proto.EvStatusChanged, Session: "a@p",
		Data: rawJSON(proto.StatusChangedData{Status: "waiting", Attention: true, Revision: 5})})
	waitSessions(t, sessions, "status event", func(ss []backend.Session) bool {
		s, ok := find(ss, "local/agentd/a@p")
		return ok && s.Status == "waiting" && s.Attention
	})
	// A replayed event older than the snapshot is ignored.
	fd.emit(proto.Event{Seq: 9, Epoch: "E1", Kind: proto.EvSessionRemoved, Session: "a@p"})
	fd.emit(proto.Event{Seq: 12, Epoch: "E1", Kind: proto.EvSessionCreated, Session: "b@p",
		Data: rawJSON(proto.SessionInfo{Name: "b@p", State: "starting"})})
	waitSessions(t, sessions, "created", func(ss []backend.Session) bool {
		_, a := find(ss, "local/agentd/a@p")
		s, b := find(ss, "local/agentd/b@p")
		return a && b && s.Status == "starting"
	})

	// resync => ls reload.
	fd.setSessions(proto.SessionInfo{Name: "c@p", State: "running", Status: "working"})
	fd.emit(proto.Event{Kind: proto.EvResync, Data: rawJSON(proto.ResyncData{Reason: proto.ResyncOverflow})})
	waitSessions(t, sessions, "reloaded after resync", func(ss []backend.Session) bool {
		_, c := find(ss, "local/agentd/c@p")
		return c && len(ss) == 1
	})

	// Connection lost: reconnect with since_seq + epoch.
	fd.mu.Lock()
	for _, ch := range fd.subs {
		close(ch)
	}
	fd.subs = nil
	fd.mu.Unlock()
	waitFor(t, "resubscribe", func() bool {
		fd.mu.Lock()
		defer fd.mu.Unlock()
		return len(fd.subArgs) >= 2
	})
	fd.mu.Lock()
	a := fd.subArgs[1]
	fd.mu.Unlock()
	if a.Epoch != "E1" || a.SinceSeq < 12 {
		t.Fatalf("resubscribe args: %+v", a)
	}
	// bye also reconnects.
	fd.emit(proto.Event{Kind: proto.EvBye})
	waitFor(t, "resubscribe after bye", func() bool {
		fd.mu.Lock()
		defer fd.mu.Unlock()
		return len(fd.subArgs) >= 3
	})
	hosts := b.WatchHosts(ctx)
	waitHosts(t, hosts, "agentd version", func(hs []backend.HostStatus) bool {
		h, _ := hostStatus(hs, "local")
		return h.AgentdVer == "fake-1" && h.Sessions == 1
	})
}

func TestLaunchFirstPrompt(t *testing.T) {
	dir := isolate(t)
	sock := startDaemon(t)
	b := newBackend(t, sshOpts(dir, sock))
	sshHost(t, b)
	ctx := context.Background()
	proj := t.TempDir()
	// Remote launch through SSH: cwd = the SSH user's home, target = dir.
	s, err := b.Launch(ctx, backend.LaunchSpec{HostID: "box", Where: backend.SourceAgentd, Project: proj, Harness: "bash",
		FirstPrompt: "echo first-$((2+3))"})
	if err != nil {
		t.Fatal(err)
	}
	if s.HostID != "box" || s.Source != backend.SourceAgentd || s.Harness != "bash" || s.Info == nil || s.Info.Cwd != proj {
		t.Fatalf("launched: %+v", s)
	}
	c := dialDaemon(t, sock)
	deadline := time.Now().Add(30 * time.Second)
	var screen string
	for time.Now().Before(deadline) {
		screen, _ = c.Dump(proto.DumpArgs{Name: s.Name})
		if strings.Contains(screen, "first-5") {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("first prompt never ran; screen:\n%s", screen)
}
