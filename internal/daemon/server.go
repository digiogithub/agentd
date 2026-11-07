// Package daemon implements the agentd daemon: the unix socket server, the
// session registry, the op handlers, attach streaming, delivery wiring and
// lifecycle (see AGENTS.md, "Architecture" and "Key behaviours").
package daemon

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"syscall"
	"time"

	"github.com/digiogithub/agentd/internal/config"
	"github.com/digiogithub/agentd/internal/mailbox"
	"github.com/digiogithub/agentd/internal/proto"
	"github.com/digiogithub/agentd/internal/session"
)

// Version is reported by ping. cmd/agentd sets it at startup.
var Version = "dev"

// Deliverer is the delivery loop as seen by the server (implemented by
// *delivery.Loop). It is optional: a nil Deliverer disables injection.
type Deliverer interface {
	Run(ctx context.Context) error
	Kick(recipient string)
}

// Server is the agentd daemon.
type Server struct {
	Cfg      *config.Config
	Aliases  *config.Aliases
	Store    *mailbox.Store
	Log      *slog.Logger
	Delivery Deliverer

	// SocketPath is where Listen binds; "" means config.SocketPath().
	SocketPath string
	// MaxConns caps concurrently tracked connections (0 = DefaultMaxConns).
	// Connections beyond it are closed right after accept.
	MaxConns int
	// WriteTimeout bounds each PTY input chunk write of the sessions it
	// starts (0 = session.DefaultWriteTimeout).
	WriteTimeout time.Duration
	// AttachWriteTimeout bounds each frame written to an attached client; a
	// client that does not drain it in time is dropped (0 =
	// DefaultAttachWriteTimeout).
	AttachWriteTimeout time.Duration
	// Clock drives mailbox retention and purge's older_than (nil = real time).
	Clock Clock

	mu       sync.Mutex
	sessions map[string]*entry
	// starting reserves names of sessions whose session.Start is in progress
	// (see run); they are listed with state "starting" but not looked up.
	starting map[string]proto.SessionInfo
	conns    map[net.Conn]struct{}
	ln       net.Listener
	closing  bool

	lockFile *os.File    // exclusive flock on <socketdir>/agentd.lock, held for the daemon's lifetime
	sockInfo os.FileInfo // the socket file as bound, to unlink only our own on shutdown

	shutdownOnce sync.Once
	done         chan struct{}
	stopDelivery context.CancelFunc
	deliveryDone chan struct{}

	stopRetention context.CancelFunc
	retentionDone chan struct{}
	// retentionHook, if set (tests), is called after each retention purge.
	retentionHook func(purged int, err error)
}

// New returns a server. Store is required; a nil Cfg means defaults.
func New(cfg *config.Config, aliases *config.Aliases, store *mailbox.Store, log *slog.Logger) *Server {
	if cfg == nil {
		cfg = config.Default()
	}
	if log == nil {
		log = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	return &Server{
		Cfg:      cfg,
		Aliases:  aliases,
		Store:    store,
		Log:      log,
		sessions: make(map[string]*entry),
		starting: make(map[string]proto.SessionInfo),
		conns:    make(map[net.Conn]struct{}),
		done:     make(chan struct{}),
	}
}

func (s *Server) socketPath() string {
	if s.SocketPath != "" {
		return s.SocketPath
	}
	return config.SocketPath()
}

// ErrAlreadyRunning is returned by Listen when another daemon holds the lock
// file or answers on the socket.
var ErrAlreadyRunning = errors.New("agentd daemon already running")

// DefaultMaxConns is the default Server.MaxConns.
const DefaultMaxConns = 1024

// LockName is the daemon lock file, next to the socket.
const LockName = "agentd.lock"

// Listen creates the directories, takes the daemon lock and binds the socket
// (mode 0600).
//
// Only the holder of the exclusive flock on <socketdir>/agentd.lock may remove
// a stale socket file and bind, so two daemons racing on start cannot both
// listen: the loser gets ErrAlreadyRunning. The lock is held until Shutdown.
func (s *Server) Listen() (err error) {
	if err := config.EnsureDirs(); err != nil {
		return err
	}
	path := s.socketPath()
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create socket dir: %w", err)
	}
	lock, err := acquireLock(filepath.Join(dir, LockName))
	if err != nil {
		return err
	}
	defer func() {
		if err != nil {
			_ = lock.Close() // releases the flock
		}
	}()
	if _, err := os.Lstat(path); err == nil {
		// A daemon answering here without holding the lock predates it.
		if Ping(path, time.Second) == nil {
			return fmt.Errorf("%w on %s", ErrAlreadyRunning, path)
		}
		if err := os.Remove(path); err != nil {
			return fmt.Errorf("remove stale socket: %w", err)
		}
	}
	ln, err := net.Listen("unix", path)
	if err != nil {
		return fmt.Errorf("listen %s: %w", path, err)
	}
	// The socket dir is 0700, so the window before chmod exposes nothing.
	if err := os.Chmod(path, 0o600); err != nil {
		_ = ln.Close()
		return fmt.Errorf("chmod socket: %w", err)
	}
	fi, err := os.Lstat(path)
	if err != nil {
		_ = ln.Close()
		return fmt.Errorf("stat socket: %w", err)
	}
	ln.(*net.UnixListener).SetUnlinkOnClose(false)
	s.mu.Lock()
	s.ln, s.lockFile, s.sockInfo = ln, lock, fi
	s.mu.Unlock()
	s.Log.Info("listening", "socket", path, "pid", os.Getpid(), "version", Version)
	return nil
}

// acquireLock opens path and takes a non-blocking exclusive flock on it.
func acquireLock(path string) (*os.File, error) {
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open lock file: %w", err)
	}
	if err := flockNB(f); err != nil {
		_ = f.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, fmt.Errorf("%w (lock %s is held)", ErrAlreadyRunning, path)
		}
		return nil, fmt.Errorf("lock %s: %w", path, err)
	}
	return f, nil
}

// flockNB takes LOCK_EX|LOCK_NB on f without f.Fd() (which would make the
// file blocking; harmless here, but SyscallConn is the clean path).
func flockNB(f *os.File) error {
	sc, err := f.SyscallConn()
	if err != nil {
		return err
	}
	var ferr error
	if cerr := sc.Control(func(fd uintptr) {
		for {
			ferr = syscall.Flock(int(fd), syscall.LOCK_EX|syscall.LOCK_NB)
			if !errors.Is(ferr, syscall.EINTR) {
				return
			}
		}
	}); cerr != nil {
		return cerr
	}
	return ferr
}

// Ping dials path and sends a ping, returning nil if a daemon answers.
func Ping(path string, timeout time.Duration) error {
	conn, err := net.DialTimeout("unix", path, timeout)
	if err != nil {
		return err
	}
	defer func() { _ = conn.Close() }()
	_ = conn.SetDeadline(time.Now().Add(timeout))
	c := proto.NewCodec(conn)
	if err := c.WriteRequest(proto.Request{Op: proto.OpPing}); err != nil {
		return err
	}
	resp, err := c.ReadResponse()
	if err != nil {
		return err
	}
	return resp.Decode(nil)
}

// Serve accepts connections until Shutdown is called (by the shutdown op or
// when ctx is done) and returns once shutdown has completed. It calls Listen
// if needed and runs the delivery loop.
func (s *Server) Serve(ctx context.Context) error {
	s.mu.Lock()
	ln := s.ln
	s.mu.Unlock()
	if ln == nil {
		if err := s.Listen(); err != nil {
			return err
		}
		s.mu.Lock()
		ln = s.ln
		s.mu.Unlock()
	}
	if s.Delivery != nil {
		dctx, cancel := context.WithCancel(context.Background())
		s.mu.Lock()
		s.stopDelivery = cancel
		s.deliveryDone = make(chan struct{})
		s.mu.Unlock()
		go func() {
			defer close(s.deliveryDone)
			if err := s.Delivery.Run(dctx); err != nil && !errors.Is(err, context.Canceled) {
				s.Log.Error("delivery loop", "err", err)
			}
		}()
	}
	s.startRetention()
	go func() {
		select {
		case <-ctx.Done():
			s.Log.Info("shutdown requested", "reason", context.Cause(ctx))
			s.Shutdown()
		case <-s.done:
		}
	}()

	if err := s.acceptLoop(ln); err != nil {
		return err
	}
	<-s.done
	return nil
}

// startRetention runs the mailbox retention loop ([mailbox] retention, 0 =
// off) until Shutdown.
func (s *Server) startRetention() {
	retention, interval := s.Cfg.Mailbox.RetentionDuration(), s.Cfg.Mailbox.PurgeEvery()
	if s.Store == nil || retention <= 0 {
		s.Log.Info("mailbox retention disabled")
		return
	}
	rctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	s.mu.Lock()
	if s.closing {
		s.mu.Unlock()
		cancel()
		return
	}
	s.stopRetention, s.retentionDone = cancel, done
	s.mu.Unlock()
	go func() {
		defer close(done)
		s.runRetention(rctx, retention, interval)
	}()
}

// Accept retry backoff bounds (same policy as net/http).
const (
	acceptBackoffMin = 5 * time.Millisecond
	acceptBackoffMax = time.Second
)

// acceptLoop accepts connections until the listener is closed by Shutdown.
// Accept errors never stop the daemon: transient ones (EMFILE, ENFILE,
// ECONNABORTED...) are retried with exponential backoff. It returns an error
// only if the listener was closed by something other than Shutdown.
func (s *Server) acceptLoop(ln net.Listener) error {
	var backoff time.Duration
	for {
		conn, err := ln.Accept()
		if err != nil {
			s.mu.Lock()
			closing := s.closing
			s.mu.Unlock()
			if closing {
				return nil
			}
			if errors.Is(err, net.ErrClosed) {
				s.Log.Error("listener closed unexpectedly", "err", err)
				return fmt.Errorf("accept: %w", err)
			}
			if backoff == 0 {
				backoff = acceptBackoffMin
			} else {
				backoff = min(2*backoff, acceptBackoffMax)
			}
			s.Log.Warn("accept failed; retrying", "err", err, "backoff", backoff)
			time.Sleep(backoff)
			continue
		}
		backoff = 0
		if err := checkPeer(conn); err != nil {
			s.Log.Warn("rejected connection", "err", err)
			_ = conn.Close()
			continue
		}
		if !s.track(conn) {
			_ = conn.Close()
			continue
		}
		go s.handleConn(conn)
	}
}

func (s *Server) track(c net.Conn) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closing {
		return false
	}
	maxConns := s.MaxConns
	if maxConns <= 0 {
		maxConns = DefaultMaxConns
	}
	if len(s.conns) >= maxConns {
		s.Log.Warn("connection limit reached; rejecting", "max", maxConns)
		return false
	}
	s.conns[c] = struct{}{}
	return true
}

func (s *Server) untrack(c net.Conn) {
	s.mu.Lock()
	delete(s.conns, c)
	s.mu.Unlock()
}

// Shutdown stops the server: closes the listener and every connection, stops
// delivery and terminates all sessions in parallel (closing a session aborts
// any injection blocked on its input, so delivery cannot hold shutdown up),
// closes the store, removes the socket if it is still the one this server
// bound, and releases the daemon lock. It is idempotent and blocks until done.
func (s *Server) Shutdown() {
	s.shutdownOnce.Do(func() {
		s.mu.Lock()
		s.closing = true
		ln, lock, sockInfo := s.ln, s.lockFile, s.sockInfo
		conns := make([]net.Conn, 0, len(s.conns))
		for c := range s.conns {
			conns = append(conns, c)
		}
		entries := make([]*entry, 0, len(s.sessions))
		for _, e := range s.sessions {
			entries = append(entries, e)
		}
		stopDelivery, deliveryDone := s.stopDelivery, s.deliveryDone
		stopRetention, retentionDone := s.stopRetention, s.retentionDone
		s.mu.Unlock()

		if ln != nil {
			_ = ln.Close()
		}
		for _, c := range conns {
			_ = c.Close()
		}
		if stopDelivery != nil {
			stopDelivery()
		}
		if stopRetention != nil {
			stopRetention()
		}
		var wg sync.WaitGroup
		for _, e := range entries {
			wg.Add(1)
			go func() {
				defer wg.Done()
				_ = e.sess.Close()
			}()
		}
		wg.Wait()
		if deliveryDone != nil {
			<-deliveryDone
		}
		if retentionDone != nil {
			<-retentionDone // before the store is closed
		}
		if s.Store != nil {
			if err := s.Store.Close(); err != nil {
				s.Log.Error("close store", "err", err)
			}
		}
		if sockInfo != nil {
			path := s.socketPath()
			if cur, err := os.Lstat(path); err == nil && os.SameFile(sockInfo, cur) {
				_ = os.Remove(path)
			} else {
				s.Log.Warn("socket replaced by another process; not removing", "socket", path)
			}
		}
		if lock != nil {
			_ = lock.Close() // releases the flock
		}
		s.Log.Info("shutdown complete")
		close(s.done)
	})
	<-s.done
}

// Done is closed when Shutdown has completed.
func (s *Server) Done() <-chan struct{} { return s.done }

// ---- Registry ----

// entry is one registered session plus its attached clients.
type entry struct {
	sess    *session.Session
	profile config.Profile

	mu      sync.Mutex
	clients map[*attachClient]struct{}
	active  *attachClient
}

type attachClient struct {
	cols, rows uint16
}

func (s *Server) lookup(name string) (*entry, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok := s.sessions[name]
	if !ok {
		if _, starting := s.starting[name]; starting {
			return nil, fmt.Errorf("session %q is starting", name)
		}
		return nil, fmt.Errorf("no such session %q", name)
	}
	return e, nil
}

// sortedEntries returns the registry sorted by name.
func (s *Server) sortedEntries() []*entry {
	s.mu.Lock()
	out := make([]*entry, 0, len(s.sessions))
	for _, e := range s.sessions {
		out = append(out, e)
	}
	s.mu.Unlock()
	sort.Slice(out, func(i, j int) bool { return out[i].sess.Name() < out[j].sess.Name() })
	return out
}

// StateStarting is the ls state of a session whose process is being started.
const StateStarting = "starting"

// startingInfos returns the placeholders of sessions being started.
func (s *Server) startingInfos() []proto.SessionInfo {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]proto.SessionInfo, 0, len(s.starting))
	for _, info := range s.starting {
		out = append(out, info)
	}
	return out
}

func (s *Server) kick(recipient string) {
	if s.Delivery != nil {
		s.Delivery.Kick(recipient)
	}
}

func (e *entry) attachedCount() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return len(e.clients)
}

// addClient registers an attached client. While at least one client is
// attached, the client's real terminal answers terminal queries, so the
// emulator's own replies are suppressed (docs/spike-vt.md, limitation 3).
func (e *entry) addClient(c *attachClient) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if len(e.clients) == 0 {
		e.sess.SetReplyForwarding(false)
	}
	if e.clients == nil {
		e.clients = make(map[*attachClient]struct{})
	}
	e.clients[c] = struct{}{}
	e.activateLocked(c)
}

func (e *entry) removeClient(c *attachClient) {
	e.mu.Lock()
	defer e.mu.Unlock()
	delete(e.clients, c)
	if e.active == c {
		e.active = nil
	}
	if len(e.clients) == 0 {
		e.sess.SetReplyForwarding(true)
	}
}

// activate makes c the most recently active client; its size wins.
func (e *entry) activate(c *attachClient) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.active != c {
		e.activateLocked(c)
	}
}

func (e *entry) resize(c *attachClient, cols, rows uint16) {
	e.mu.Lock()
	defer e.mu.Unlock()
	c.cols, c.rows = cols, rows
	e.activateLocked(c)
}

func (e *entry) activateLocked(c *attachClient) {
	e.active = c
	if c.cols == 0 || c.rows == 0 || e.sess.State() != session.StateRunning {
		return
	}
	if cols, rows := e.sess.Size(); cols != c.cols || rows != c.rows {
		_ = e.sess.Resize(c.cols, c.rows)
	}
}
