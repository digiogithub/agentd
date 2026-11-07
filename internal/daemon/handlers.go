package daemon

import (
	"context"
	"errors"
	"fmt"
	"io"
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
		data, err := s.dispatch(req, peer)
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

func (s *Server) dispatch(req proto.Request, peer peerInfo) (any, error) {
	ctx, cancel := context.WithTimeout(context.Background(), requestTimeout)
	defer cancel()
	switch req.Op {
	case proto.OpPing:
		return proto.PingResult{Version: Version, Pid: os.Getpid()}, nil
	case proto.OpRun:
		var a proto.RunArgs
		if err := req.DecodeArgs(&a); err != nil {
			return nil, err
		}
		return s.run(ctx, a)
	case proto.OpLs:
		return s.ls(ctx)
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
		return nil, errors.New("missing op")
	default:
		return nil, fmt.Errorf("unknown op %q", req.Op)
	}
}

// ---- Sessions ----

func (s *Server) run(ctx context.Context, a proto.RunArgs) (proto.SessionInfo, error) {
	if err := config.ValidName(a.Name); err != nil {
		return proto.SessionInfo{}, err
	}
	if len(a.Cmd) == 0 || a.Cmd[0] == "" {
		return proto.SessionInfo{}, errors.New("run: empty command")
	}
	if a.Cwd != "" {
		if !filepath.IsAbs(a.Cwd) {
			return proto.SessionInfo{}, fmt.Errorf("run: cwd %q is not absolute", a.Cwd)
		}
		if fi, err := os.Stat(a.Cwd); err != nil || !fi.IsDir() {
			return proto.SessionInfo{}, fmt.Errorf("run: cwd %q is not a directory", a.Cwd)
		}
	}
	if a.Cols == 0 {
		a.Cols = session.DefaultCols
	}
	if a.Rows == 0 {
		a.Rows = session.DefaultRows
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
	profile := s.Cfg.Profile(a.Profile)

	// Reserve the name, then start without holding the registry lock (a slow
	// fork/exec must not stall every other op).
	s.mu.Lock()
	if s.closing {
		s.mu.Unlock()
		return proto.SessionInfo{}, errors.New("daemon is shutting down")
	}
	_, exists := s.sessions[a.Name]
	_, starting := s.starting[a.Name]
	if exists || starting {
		s.mu.Unlock()
		return proto.SessionInfo{}, fmt.Errorf("session %q already exists", a.Name)
	}
	s.starting[a.Name] = proto.SessionInfo{
		Name: a.Name, State: StateStarting, Cwd: a.Cwd, Cmd: a.Cmd, Profile: profile.Name,
		Cols: a.Cols, Rows: a.Rows, StartedAt: time.Now(),
	}
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
		return proto.SessionInfo{}, errors.New("daemon is shutting down")
	}
	e := &entry{sess: sess, profile: profile}
	s.sessions[a.Name] = e
	s.mu.Unlock()

	s.Log.Info("session started", "name", a.Name, "pid", sess.Pid(), "cmd", a.Cmd, "cwd", a.Cwd, "profile", profile.Name)
	go func() {
		<-sess.Done()
		code, _ := sess.ExitCode()
		s.Log.Info("session exited", "name", a.Name, "code", code)
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
	if n, err := s.Store.UnreadCount(ctx, info.Name); err == nil {
		info.Unread = n
	}
	return info
}

func (s *Server) ls(ctx context.Context) (proto.LsResult, error) {
	res := proto.LsResult{Sessions: []proto.SessionInfo{}}
	for _, e := range s.sortedEntries() {
		res.Sessions = append(res.Sessions, s.info(ctx, e))
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
			return 0, fmt.Errorf("invalid signal %q", v)
		}
		return syscall.Signal(n), nil
	}
	name := strings.ToUpper(v)
	if !strings.HasPrefix(name, "SIG") {
		name = "SIG" + name
	}
	sig := unix.SignalNum(name)
	if sig == 0 {
		return 0, fmt.Errorf("unknown signal %q", v)
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
			return fmt.Errorf("session %q has already exited", a.Name)
		}
		return err
	}
	s.Log.Info("session signalled", "name", a.Name, "signal", sig.String())
	return nil
}

func (s *Server) rm(name string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok := s.sessions[name]
	if !ok {
		return fmt.Errorf("no such session %q", name)
	}
	if e.sess.State() != session.StateExited {
		return fmt.Errorf("session %q is still running (kill it first)", name)
	}
	delete(s.sessions, name)
	s.Log.Info("session removed", "name", name)
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
		return proto.DumpResult{}, errors.New("dump: ansi output is not supported with scrollback")
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
		return fmt.Errorf("message %d not found", id)
	case errors.Is(err, mailbox.ErrForbidden):
		return fmt.Errorf("message %d is not addressed to %q", id, who)
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
