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

	mu        sync.Mutex
	sessions  []proto.SessionInfo
	subs      []chan proto.Event
	subArgs   []proto.SubscribeArgs
	lsCalls   int
	viewCalls int
	conns     []net.Conn
	// board is what op board answers; boardArgs records each call. A call
	// whose since_digest equals board.Digest gets not_modified.
	board     proto.BoardResult
	boardArgs []proto.BoardArgs
	noBoard   bool // answer board like a daemon without the op
	replies   []proto.ReplyArgs
	// notes is what op notes answers (digest "n<len>"); noNotes answers like
	// a daemon without the op; noteArgs records the note_* calls.
	notes     []proto.Note
	noNotes   bool
	notesCall int
	noteOps   []string
	// relayArgs records op relay calls; relayErr makes it fail.
	relayArgs []proto.RelayArgs
	relayErr  bool
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
		case proto.OpView:
			// A daemon before 0.6.3: its ViewArgs has no "markup".
			var a struct {
				Name          string `json:"name"`
				SinceRevision uint64 `json:"since_revision,omitempty"`
				Epoch         string `json:"epoch,omitempty"`
			}
			f.mu.Lock()
			f.viewCalls++
			f.mu.Unlock()
			if err := req.DecodeArgs(&a); err != nil {
				resp = proto.ErrorResponse(err)
				break
			}
			resp, _ = proto.OKResponse(proto.View{Name: a.Name, Epoch: "E1", Revision: 1, Status: "idle",
				Lines: []proto.ViewLine{{Text: "hola *mundo*", Role: "transcript"}}})
		case proto.OpBoard:
			var a proto.BoardArgs
			_ = req.DecodeArgs(&a)
			f.mu.Lock()
			f.boardArgs = append(f.boardArgs, a)
			b, no := f.board, f.noBoard
			f.mu.Unlock()
			switch {
			case no:
				resp = proto.ErrorResponse(proto.Errorf(proto.CodeInvalid, "unknown op %q", req.Op))
			case a.SinceDigest != "" && a.SinceDigest == b.Digest:
				resp, _ = proto.OKResponse(proto.BoardResult{Digest: b.Digest, NotModified: true})
			default:
				resp, _ = proto.OKResponse(b)
			}
		case proto.OpNotes:
			var a proto.NotesArgs
			_ = req.DecodeArgs(&a)
			f.mu.Lock()
			f.notesCall++
			ns, no := append([]proto.Note(nil), f.notes...), f.noNotes
			f.mu.Unlock()
			digest := "n" + string(rune('0'+len(ns)))
			switch {
			case no:
				resp = proto.ErrorResponse(proto.Errorf(proto.CodeInvalid, "unknown op %q", req.Op))
			case a.SinceDigest == digest:
				resp, _ = proto.OKResponse(proto.NotesResult{Digest: digest, NotModified: true})
			default:
				resp, _ = proto.OKResponse(proto.NotesResult{Notes: ns, Digest: digest})
			}
		case proto.OpNoteAdd:
			var a proto.NoteAddArgs
			_ = req.DecodeArgs(&a)
			f.mu.Lock()
			f.noteOps = append(f.noteOps, req.Op+":"+a.Title)
			n := proto.Note{ID: int64(len(f.notes) + 1), Title: a.Title, Body: a.Body, State: proto.NoteOpen}
			f.notes = append(f.notes, n)
			f.mu.Unlock()
			resp, _ = proto.OKResponse(n)
		case proto.OpReply:
			var a proto.ReplyArgs
			_ = req.DecodeArgs(&a)
			f.mu.Lock()
			f.replies = append(f.replies, a)
			f.mu.Unlock()
			id := a.ID
			resp, _ = proto.OKResponse(proto.Message{ID: 900, Sender: "human:u", Recipient: "a@p", Body: a.Body, ReplyTo: &id, State: "queued"})
		case proto.OpRelay:
			var a proto.RelayArgs
			_ = req.DecodeArgs(&a)
			f.mu.Lock()
			f.relayArgs = append(f.relayArgs, a)
			bad := f.relayErr
			f.mu.Unlock()
			if bad {
				resp = proto.ErrorResponse(proto.Errorf(proto.CodeInvalid, "relay is not enabled"))
				break
			}
			resp, _ = proto.OKResponse(proto.RelayResult{Devices: []proto.RelayDeviceInfo{{ID: "d1", Name: "Móvil", Scope: "operate", Kind: "owner"}}})
		default:
			resp = proto.ErrorResponse(proto.Errorf(proto.CodeInvalid, "fake: %s", req.Op))
		}
		if codec.WriteResponse(resp) != nil {
			return
		}
	}
}

func (f *fakeDaemon) setBoard(b proto.BoardResult) {
	f.mu.Lock()
	f.board = b
	f.mu.Unlock()
}

func (f *fakeDaemon) boardCalls() []proto.BoardArgs {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]proto.BoardArgs(nil), f.boardArgs...)
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

// A daemon that predates view markup still serves the chat view: the
// rejected "markup" argument is dropped for that host.
func TestViewFallsBackWithoutMarkup(t *testing.T) {
	dir := isolate(t)
	fd := newFakeDaemon(t)
	fd.setSessions(proto.SessionInfo{Name: "a@p", State: "running", Status: "idle", StartedAt: time.Now()})
	b := newBackend(t, sshOpts(dir, fd.sock))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	views, err := b.WatchView(ctx, backend.Session{HostID: "local", Source: backend.SourceAgentd, Name: "a@p"})
	if err != nil {
		t.Fatal(err)
	}
	v := <-views
	if len(v.Lines) != 1 || v.Lines[0].Text != "hola *mundo*" || v.Lines[0].Md != `hola \*mundo\*` {
		t.Fatalf("view %+v", v)
	}
	fd.mu.Lock()
	n := fd.viewCalls
	fd.mu.Unlock()
	if n != 2 {
		t.Fatalf("view calls %d, want 2 (refused with markup, then without)", n)
	}
}
