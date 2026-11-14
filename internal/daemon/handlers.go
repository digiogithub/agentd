package daemon

import (
	"context"
	"errors"
	"io"
	"maps"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"golang.org/x/sys/unix"

	"github.com/digiogithub/agentd/internal/config"
	"github.com/digiogithub/agentd/internal/delivery"
	"github.com/digiogithub/agentd/internal/inject"
	"github.com/digiogithub/agentd/internal/mailbox"
	"github.com/digiogithub/agentd/internal/proto"
	"github.com/digiogithub/agentd/internal/session"
)

// Child environment variables (AGENTS.md, "Child env").
const (
	EnvID     = "AGENTD_ID"
	EnvSocket = config.EnvSocket
)

// HumanPrefix marks human mailbox addresses ("human:<user>").
const HumanPrefix = "human:"

// requestTimeout bounds mailbox operations of one request.
const requestTimeout = 30 * time.Second

func (s *Server) handleConn(conn net.Conn) {
	defer s.untrack(conn)
	defer func() { _ = conn.Close() }()
	codec := proto.NewCodec(conn)
	peer, err := peerOf(conn) // for identity resolution (identity.go)
	if err != nil {
		peer = peerInfo{uid: os.Getuid()}
	}
	for {
		req, err := codec.ReadRequest()
		if err != nil {
			if !errors.Is(err, io.EOF) && !errors.Is(err, net.ErrClosed) {
				s.Log.Debug("read request", "err", err)
			}
			return
		}
		if req.Op == proto.OpAttach {
			if s.handleAttach(conn, codec, req) {
				return // the connection was upgraded to frames and is now done
			}
			continue
		}
		if req.Op == proto.OpSubscribe {
			if s.handleSubscribe(conn, codec, req, peer) {
				return
			}
			continue
		}
		ctx, cancel := context.WithTimeout(context.Background(), requestTimeout)
		stop := func() {}
		if req.Op == proto.OpAct {
			// An act holds the input lock and types into a terminal: it must
			// not outlive a client that went away.
			ctx, stop = s.cancelOnDisconnect(ctx, conn, codec)
		}
		data, err := s.dispatch(ctx, req, peer)
		stop()
		cancel()
		err = classifyError(err)
		var resp proto.Response
		if err != nil {
			s.Log.Debug("op failed", "op", req.Op, "err", err)
			resp = proto.ErrorResponse(err)
		} else if resp, err = proto.OKResponse(data); err != nil {
			resp = proto.ErrorResponse(err)
		}
		if err := codec.WriteResponse(resp); err != nil {
			return
		}
		if req.Op == proto.OpShutdown && resp.OK {
			go s.Shutdown()
			return
		}
	}
}

// cancelOnDisconnect returns ctx cancelled as soon as the client's connection
// is closed or fails. It watches with a Peek on the codec's reader, so a
// pipelined next request is not consumed; stop ends the watch (it returns
// once the watcher goroutine is gone and the read deadline is cleared).
func (s *Server) cancelOnDisconnect(parent context.Context, conn net.Conn, codec *proto.Codec) (context.Context, func()) {
	ctx, cancel := context.WithCancel(parent)
	res := make(chan struct{})
	go func() {
		defer close(res)
		if _, err := codec.Reader().Peek(1); err != nil && !errors.Is(err, os.ErrDeadlineExceeded) {
			cancel()
		}
	}()
	return ctx, func() {
		_ = conn.SetReadDeadline(time.Unix(1, 0)) // unblock the Peek
		<-res
		_ = conn.SetReadDeadline(time.Time{})
		cancel()
	}
}

func (s *Server) dispatch(ctx context.Context, req proto.Request, peer peerInfo) (any, error) {
	switch req.Op {
	case proto.OpPing:
		return s.ping(), nil
	case proto.OpInfo:
		var a proto.InfoArgs
		if err := req.DecodeArgs(&a); err != nil {
			return nil, err
		}
		return s.infoOp(a), nil
	case proto.OpRun:
		var a proto.RunArgs
		if err := req.DecodeArgs(&a); err != nil {
			return nil, err
		}
		return s.run(ctx, a)
	case proto.OpLs:
		// A session caller without agent_view must not read other sessions'
		// transcript preview through ls. An unresolvable identity is treated
		// the same way (no error: ls stays usable).
		who, err := s.resolveCaller(peer, req.Auth)
		strip := err != nil || (who.session && !s.Cfg.Security.AgentView)
		return s.ls(ctx, who.id, strip)
	case proto.OpKill:
		var a proto.KillArgs
		if err := req.DecodeArgs(&a); err != nil {
			return nil, err
		}
		return nil, s.killSession(a)
	case proto.OpRm:
		var a proto.NameArgs
		if err := req.DecodeArgs(&a); err != nil {
			return nil, err
		}
		return nil, s.rm(a.Name)
	case proto.OpDump:
		var a proto.DumpArgs
		if err := req.DecodeArgs(&a); err != nil {
			return nil, err
		}
		return s.dump(a)
	case proto.OpType:
		var a proto.TypeArgs
		if err := req.DecodeArgs(&a); err != nil {
			return nil, err
		}
		who, err := s.resolveCaller(peer, req.Auth)
		if err != nil {
			return nil, err
		}
		return nil, s.typeText(who, a)
	case proto.OpSend:
		var a proto.SendArgs
		if err := req.DecodeArgs(&a); err != nil {
			return nil, err
		}
		who, err := s.resolveCaller(peer, req.Auth)
		if err != nil {
			return nil, err
		}
		return s.send(ctx, who, a)
	case proto.OpInbox:
		var a proto.InboxArgs
		if err := req.DecodeArgs(&a); err != nil {
			return nil, err
		}
		who, err := s.resolveCaller(peer, req.Auth)
		if err != nil {
			return nil, err
		}
		return s.inbox(ctx, who, a)
	case proto.OpRead:
		var a proto.ReadArgs
		if err := req.DecodeArgs(&a); err != nil {
			return nil, err
		}
		who, err := s.resolveCaller(peer, req.Auth)
		if err != nil {
			return nil, err
		}
		return s.read(ctx, who, a)
	case proto.OpReply:
		var a proto.ReplyArgs
		if err := req.DecodeArgs(&a); err != nil {
			return nil, err
		}
		who, err := s.resolveCaller(peer, req.Auth)
		if err != nil {
			return nil, err
		}
		return s.reply(ctx, who, a)
	case proto.OpPurge:
		var a proto.PurgeArgs
		if err := req.DecodeArgs(&a); err != nil {
			return nil, err
		}
		who, err := s.resolveCaller(peer, req.Auth)
		if err != nil {
			return nil, err
		}
		return s.purge(ctx, who, a)
	case proto.OpView:
		var a proto.ViewArgs
		if err := req.DecodeArgs(&a); err != nil {
			return nil, err
		}
		who, err := s.resolveCaller(peer, req.Auth)
		if err != nil {
			return nil, err
		}
		return s.viewOp(who, a)
	case proto.OpViewAck:
		var a proto.NameArgs
		if err := req.DecodeArgs(&a); err != nil {
			return nil, err
		}
		who, err := s.resolveCaller(peer, req.Auth)
		if err != nil {
			return nil, err
		}
		return s.viewAck(who, a.Name)
	case proto.OpAct:
		var a proto.ActArgs
		if err := req.DecodeArgs(&a); err != nil {
			return nil, err
		}
		who, err := s.resolveCaller(peer, req.Auth)
		if err != nil {
			return nil, err
		}
		return s.actOp(ctx, who, a)
	case proto.OpLabel:
		var a proto.LabelArgs
		if err := req.DecodeArgs(&a); err != nil {
			return nil, err
		}
		who, err := s.resolveCaller(peer, req.Auth)
		if err != nil {
			return nil, err
		}
		return s.label(who, a)
	case proto.OpDocGet:
		var a proto.DocGetArgs
		if err := req.DecodeArgs(&a); err != nil {
			return nil, err
		}
		return s.docGet(a)
	case proto.OpDocSet:
		var a proto.DocSetArgs
		if err := req.DecodeArgs(&a); err != nil {
			return nil, err
		}
		who, err := s.resolveCaller(peer, req.Auth)
		if err != nil {
			return nil, err
		}
		return s.docSet(who, a)
	case proto.OpTurns:
		var a proto.NameArgs
		if err := req.DecodeArgs(&a); err != nil {
			return nil, err
		}
		who, err := s.resolveCaller(peer, req.Auth)
		if err != nil {
			return nil, err
		}
		return s.turnsOp(who, a)
	case proto.OpPeers:
		return s.peers(), nil
	case proto.OpWhoami:
		who, err := s.resolveCaller(peer, req.Auth)
		if err != nil {
			return nil, err
		}
		return s.whoami(who), nil
	case proto.OpShutdown:
		return nil, nil
	case "":
		return nil, proto.Errorf(proto.CodeInvalid, "missing op")
	default:
		return nil, proto.Errorf(proto.CodeInvalid, "unknown op %q", req.Op)
	}
}

// ---- Sessions ----

func (s *Server) run(ctx context.Context, a proto.RunArgs) (proto.SessionInfo, error) {
	a, err := s.resolveRun(a)
	if err != nil {
		return proto.SessionInfo{}, err
	}
	if err := config.ValidName(a.Name); err != nil {
		return proto.SessionInfo{}, proto.WithCode(proto.CodeInvalid, err)
	}
	if len(a.Cmd) == 0 || a.Cmd[0] == "" {
		return proto.SessionInfo{}, proto.Errorf(proto.CodeInvalid, "run: empty command")
	}
	labels, err := mergeLabels(nil, a.Labels)
	if err != nil {
		return proto.SessionInfo{}, err
	}
	if len(labels) == 0 {
		labels = nil
	}
	if a.Cwd != "" {
		if !filepath.IsAbs(a.Cwd) {
			return proto.SessionInfo{}, proto.Errorf(proto.CodeInvalid, "run: cwd %q is not absolute", a.Cwd)
		}
		if fi, err := os.Stat(a.Cwd); err != nil || !fi.IsDir() {
			return proto.SessionInfo{}, proto.Errorf(proto.CodeInvalid, "run: cwd %q is not a directory", a.Cwd)
		}
	}
	// Fixed size policy (ADR-A02): without an explicit size the session gets
	// its profile's cols/rows, never the caller's terminal size.
	profile := s.Cfg.Profile(a.Profile)
	if a.Cols == 0 {
		a.Cols = uint16(profile.Cols)
	}
	if a.Rows == 0 {
		a.Rows = uint16(profile.Rows)
	}
	logPath, err := config.SessionLogPath(a.Name)
	if err != nil {
		return proto.SessionInfo{}, err
	}
	// Child env: base (CLI env, stripped) < --env < daemon-set AGENTD_* vars.
	env := make(map[string]string, len(a.Env)+2)
	for k, v := range a.Env {
		env[k] = v
	}
	sock, err := filepath.Abs(s.socketPath())
	if err != nil {
		return proto.SessionInfo{}, err
	}
	env[EnvID] = a.Name
	env[EnvSocket] = sock
	env[EnvToken] = newToken() // identity.go; kept in memory only (in the session's Spec)

	// Reserve the name, then start without holding the registry lock (a slow
	// fork/exec must not stall every other op).
	s.mu.Lock()
	if s.closing {
		s.mu.Unlock()
		return proto.SessionInfo{}, proto.Errorf(proto.CodeNotReady, "daemon is shutting down")
	}
	_, exists := s.sessions[a.Name]
	_, starting := s.starting[a.Name]
	if exists || starting {
		s.mu.Unlock()
		return proto.SessionInfo{}, proto.Errorf(proto.CodeExists, "session %q already exists", a.Name)
	}
	s.starting[a.Name] = proto.SessionInfo{
		Name: a.Name, State: StateStarting, Cwd: a.Cwd, Cmd: a.Cmd, Profile: profile.Name,
		Cols: a.Cols, Rows: a.Rows, StartedAt: time.Now(), Labels: labels,
	}
	created := s.starting[a.Name]
	s.mu.Unlock()

	sess, err := session.Start(session.Spec{
		Name: a.Name, Cwd: a.Cwd, Cmd: a.Cmd, BaseEnv: s.childBaseEnv(a.EnvBase), Env: env,
		Cols: a.Cols, Rows: a.Rows, LogPath: logPath,
		LogMaxBytes: s.Cfg.Run.LogMaxBytes(), WriteTimeout: s.WriteTimeout,
	})
	s.mu.Lock()
	delete(s.starting, a.Name)
	if err != nil {
		s.mu.Unlock()
		return proto.SessionInfo{}, err
	}
	if s.closing { // Shutdown already collected the sessions to close
		s.mu.Unlock()
		_ = sess.Close()
		return proto.SessionInfo{}, proto.Errorf(proto.CodeNotReady, "daemon is shutting down")
	}
	e := &entry{sess: sess, profile: profile, baseCols: a.Cols, baseRows: a.Rows, restore: s.Cfg.Run.RestoreSize(),
		exitPub: make(chan struct{}), labels: labels}
	e.view = newViewState(s, e, a.Name)
	s.sessions[a.Name] = e
	// session.created only for a session that really started (a failed start
	// or a shutdown race publishes nothing, so no session.removed is owed);
	// both before the watcher starts, so they precede any view event.
	s.publish(proto.EvSessionCreated, a.Name, created)
	s.publish(proto.EvSessionStarted, a.Name, proto.SessionInfo{
		Name: a.Name, State: sess.State(), Pid: sess.Pid(), Cwd: a.Cwd, Cmd: a.Cmd, Profile: profile.Name,
		StartedAt: sess.StartedAt(), Cols: a.Cols, Rows: a.Rows, Labels: maps.Clone(labels),
	})
	e.view.start() // under the lock: Shutdown must find a started watcher
	s.mu.Unlock()

	s.Log.Info("session started", "name", a.Name, "pid", sess.Pid(), "cmd", a.Cmd, "cwd", a.Cwd, "profile", profile.Name)
	go func() {
		<-sess.Done()
		code, _ := sess.ExitCode()
		s.Log.Info("session exited", "name", a.Name, "code", code)
		s.publish(proto.EvSessionExited, a.Name, proto.ExitedData{ExitCode: code})
		close(e.exitPub) // rm waits for this before it publishes session.removed
	}()
	s.kick(a.Name) // messages may have been queued before it started
	return s.info(ctx, e), nil
}

func (s *Server) info(ctx context.Context, e *entry) proto.SessionInfo {
	sess := e.sess
	spec := sess.Spec()
	info := proto.SessionInfo{
		Name:       sess.Name(),
		State:      sess.State(),
		Pid:        sess.Pid(),
		Cwd:        spec.Cwd,
		Cmd:        spec.Cmd,
		Profile:    e.profile.Name,
		StartedAt:  sess.StartedAt(),
		LastOutput: sess.LastOutput(),
		Attached:   e.attachedCount(),
		Title:      sess.Title(),
		Labels:     e.labelsCopy(),
	}
	if ps := sess.ProgramStatus(); ps.Reported {
		info.Reported = true
		info.ProgramStatus, info.ProgramRecords = programStatus(ps)
	}
	info.Cols, info.Rows = sess.Size()
	if code, ok := sess.ExitCode(); ok {
		info.ExitCode = &code
	} else {
		info.InputState = delivery.InputState(sess, e.profile, time.Now())
	}
	last := info.LastOutput
	if last.IsZero() {
		last = info.StartedAt
	}
	info.IdleMS = time.Since(last).Milliseconds()
	if e.view != nil {
		info.Status, info.Attention, info.Revision, info.Preview, info.Turn = e.view.summary()
	}
	if n, err := s.Store.UnreadCount(ctx, info.Name); err == nil {
		info.Unread = n
	}
	return info
}

// ls lists the sessions. With stripPreview the transcript preview of every
// session other than self is blanked (status and attention stay: peers need
// them).
func (s *Server) ls(ctx context.Context, self string, stripPreview bool) (proto.LsResult, error) {
	res := proto.LsResult{Sessions: []proto.SessionInfo{}}
	for _, e := range s.sortedEntries() {
		info := s.info(ctx, e)
		if stripPreview && info.Name != self {
			info.Preview = ""
		}
		res.Sessions = append(res.Sessions, info)
	}
	if starting := s.startingInfos(); len(starting) > 0 {
		res.Sessions = append(res.Sessions, starting...)
		sort.Slice(res.Sessions, func(i, j int) bool { return res.Sessions[i].Name < res.Sessions[j].Name })
	}
	return res, nil
}

func (s *Server) peers() proto.PeersResult {
	res := proto.PeersResult{Peers: []proto.Peer{}}
	for _, e := range s.sortedEntries() {
		res.Peers = append(res.Peers, proto.Peer{
			Name: e.sess.Name(), State: e.sess.State(), Profile: e.profile.Name, Cwd: e.sess.Spec().Cwd,
		})
	}
	return res
}

// ParseSignal parses a signal name ("TERM", "SIGTERM", "kill") or number.
// Empty means SIGTERM.
func ParseSignal(v string) (syscall.Signal, error) {
	v = strings.TrimSpace(v)
	if v == "" {
		return syscall.SIGTERM, nil
	}
	if n, err := strconv.Atoi(v); err == nil {
		if n <= 0 || n > 64 {
			return 0, proto.Errorf(proto.CodeInvalid, "invalid signal %q", v)
		}
		return syscall.Signal(n), nil
	}
	name := strings.ToUpper(v)
	if !strings.HasPrefix(name, "SIG") {
		name = "SIG" + name
	}
	sig := unix.SignalNum(name)
	if sig == 0 {
		return 0, proto.Errorf(proto.CodeInvalid, "unknown signal %q", v)
	}
	return sig, nil
}

func (s *Server) killSession(a proto.KillArgs) error {
	e, err := s.lookup(a.Name)
	if err != nil {
		return err
	}
	sig, err := ParseSignal(a.Signal)
	if err != nil {
		return err
	}
	if err := e.sess.Signal(sig); err != nil {
		if errors.Is(err, session.ErrExited) {
			return proto.Errorf(proto.CodeExited, "session %q has already exited", a.Name)
		}
		return err
	}
	s.Log.Info("session signalled", "name", a.Name, "signal", sig.String())
	return nil
}

func (s *Server) rm(name string) error {
	s.mu.Lock()
	e, ok := s.sessions[name]
	if !ok {
		s.mu.Unlock()
		return proto.Errorf(proto.CodeNotFound, "no such session %q", name)
	}
	if e.sess.State() != session.StateExited {
		s.mu.Unlock()
		return proto.Errorf(proto.CodeConflict, "session %q is still running (kill it first)", name)
	}
	s.mu.Unlock()
	// session.exited is published by a goroutine that may lag the state
	// change: wait for it, so session.removed never precedes it.
	if e.exitPub != nil {
		select {
		case <-e.exitPub:
		case <-time.After(5 * time.Second):
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.sessions[name] != e { // removed (and maybe re-created) meanwhile
		return proto.Errorf(proto.CodeNotFound, "no such session %q", name)
	}
	delete(s.sessions, name)
	if e.view != nil {
		e.view.stopOnce.Do(func() { close(e.view.stop) })
	}
	s.Log.Info("session removed", "name", name)
	s.publish(proto.EvSessionRemoved, name, nil)
	return nil
}

func (s *Server) dump(a proto.DumpArgs) (proto.DumpResult, error) {
	e, err := s.lookup(a.Name)
	if err != nil {
		return proto.DumpResult{}, err
	}
	opts := delivery.GhostOpts(e.profile)
	opts.HideGhost = !a.Ghost && !a.ANSI
	var res proto.DumpResult
	switch {
	case a.Scrollback && a.ANSI:
		return proto.DumpResult{}, proto.Errorf(proto.CodeInvalid, "dump: ansi output is not supported with scrollback")
	case a.Scrollback:
		res.Text, res.Ghost = e.sess.ScrollbackFiltered(opts)
	case a.ANSI:
		res.Text, res.Ghost = e.sess.ScreenANSI(), e.sess.GhostText(opts.Ghost)
	default:
		res.Text, res.Ghost = e.sess.ScreenFiltered(opts)
	}
	res.GhostHidden = opts.HideGhost && res.Ghost != ""
	if a.Cells {
		row := -1
		if a.Row != nil {
			row = *a.Row
		}
		cur, y, cells := e.sess.RowCells(row)
		cd := &proto.CellsDump{CursorX: cur.X, CursorY: cur.Y, Row: y, Cells: make([]proto.CellInfo, len(cells))}
		for i, c := range cells {
			cd.Cells[i] = proto.CellInfo(c)
		}
		res.Cells = cd
	}
	return res, nil
}

func mailboxErr(err error, id int64, who string) error {
	switch {
	case errors.Is(err, mailbox.ErrNotFound):
		return proto.Errorf(proto.CodeNotFound, "message %d not found", id)
	case errors.Is(err, mailbox.ErrForbidden):
		return proto.Errorf(proto.CodeForbidden, "message %d is not addressed to %q", id, who)
	}
	return err
}

func toWire(m mailbox.Message) proto.Message {
	return proto.Message{
		ID: m.ID, Sender: m.Sender, Recipient: m.Recipient, Subject: m.Subject, Body: m.Body,
		ReplyTo: m.ReplyTo, State: string(m.State), Attempts: m.Attempts, CreatedAt: m.CreatedAt,
		DeliveredAt: m.DeliveredAt, ReadAt: m.ReadAt, RepliedAt: m.RepliedAt,
	}
}

// classifyError gives an uncoded error from a lower layer its stable code:
// a PTY write timeout is `timeout`, a paste that never showed up is
// `verify_failed`, a pending human draft is `busy`. Errors that already carry
// a code are untouched; the message wording never changes.
func classifyError(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, session.ErrWriteTimeout):
		return proto.WithCode(proto.CodeTimeout, err)
	case errors.Is(err, inject.ErrPasteNotVisible):
		return proto.WithCode(proto.CodeVerifyFailed, err)
	case errors.Is(err, inject.ErrDraft):
		return proto.WithCode(proto.CodeBusy, err)
	}
	return err
}
