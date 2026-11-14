package hcompat

// The hc socket listener (Phase 4): serves the hc protocol on a unix socket
// at herdr-core's own place, for clients that reach a core through its
// socket instead of running `hc connect` (Whip over SSH direct-streamlocal,
// herdr-mobile-relay's hc backend).
//
// Ownership follows herdr-core's rules (server.rs serve()): the core holds
// an exclusive flock on <state>/core.lock for its whole lifetime and writes
// its socket path into it. Listen takes the same lock first, so a running
// herdr-core is never displaced and a herdr-core started later finds the
// lock held and does not start (its clients then reach this listener). A
// socket file is replaced only when nothing answers on it.

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"sync"
	"syscall"
	"time"
)

// ErrOwned is returned by Listener.Listen when a herdr-core (or another
// listener) owns the lock or answers on the socket.
var ErrOwned = errors.New("hcompat: the hc socket is owned by a running core")

// DefaultMaxConns caps concurrent hc socket connections.
const DefaultMaxConns = 128

// refuseTimeout bounds how long a refused peer may take to send its first
// request before the connection is just closed.
const refuseTimeout = 2 * time.Second

// Listener serves Server on a unix socket.
type Listener struct {
	// Server answers every admitted connection.
	Server *Server
	// Socket is the unix socket path (required).
	Socket string
	// Lock is herdr-core's core.lock path ("" = no lock, no discovery file).
	Lock string
	// Admit decides per connection; a non-nil error refuses it: the first
	// request gets a "forbidden" error carrying the message, then the
	// connection is closed. Nil admits everything (tests only).
	Admit func(net.Conn) error
	// MaxConns caps concurrent connections (0 = DefaultMaxConns).
	MaxConns int
	// Log receives accept/refusal diagnostics (default: discard).
	Log *slog.Logger

	mu       sync.Mutex
	ln       net.Listener
	lock     *os.File
	sockInfo os.FileInfo
	created  []string // directories Listen created, deepest first
	conns    map[net.Conn]struct{}
	closed   bool
	ctx      context.Context
	cancel   context.CancelFunc
	wg       sync.WaitGroup
	since    time.Time
}

// Listen takes core.lock, checks the socket path and binds it (dir 0700,
// socket 0600). It returns ErrOwned (wrapped) when a core owns either.
func (l *Listener) Listen() (err error) {
	if l.Log == nil {
		l.Log = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	if l.Socket == "" || !filepath.IsAbs(l.Socket) {
		return fmt.Errorf("hcompat: socket path %q must be absolute", l.Socket)
	}
	var created []string
	defer func() {
		if err != nil {
			removeDirs(created)
		}
	}()
	var lock *os.File
	if l.Lock != "" {
		c, derr := privateDir(filepath.Dir(l.Lock)) // no := on err here: the defers read the result
		created = append(c, created...)
		if derr != nil {
			return derr
		}
		made := true
		lock, err = os.OpenFile(l.Lock, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0o600)
		if errors.Is(err, os.ErrExist) {
			made = false
			lock, err = os.OpenFile(l.Lock, os.O_RDWR, 0)
		}
		if err != nil {
			return fmt.Errorf("hcompat: open %s: %w", l.Lock, err)
		}
		if made {
			// A file we made is ours to remove on failure (unless the flock
			// is lost to another core: lock is then nil).
			defer func() {
				if err != nil && lock != nil {
					_ = os.Remove(l.Lock)
				}
			}()
		}
		if err := flockNB(lock); err != nil {
			_ = lock.Close()
			lock = nil // someone else's now: never removed
			if errors.Is(err, syscall.EWOULDBLOCK) {
				return fmt.Errorf("%w (%s is held)", ErrOwned, l.Lock)
			}
			return fmt.Errorf("hcompat: lock %s: %w", l.Lock, err)
		}
		defer func() {
			if err != nil {
				_ = lock.Close()
			}
		}()
	}
	c, err := privateDir(filepath.Dir(l.Socket))
	created = append(c, created...)
	if err != nil {
		return err
	}
	if fi, err := os.Lstat(l.Socket); err == nil {
		if fi.Mode().Type() != os.ModeSocket {
			return fmt.Errorf("%w (%s exists and is not a socket)", ErrOwned, l.Socket)
		}
		if conn, err := net.DialTimeout("unix", l.Socket, 500*time.Millisecond); err == nil {
			_ = conn.Close()
			return fmt.Errorf("%w (something answers on %s)", ErrOwned, l.Socket)
		}
		if err := os.Remove(l.Socket); err != nil {
			return fmt.Errorf("hcompat: remove stale socket: %w", err)
		}
	}
	ln, err := net.Listen("unix", l.Socket)
	if err != nil {
		return fmt.Errorf("hcompat: listen %s: %w", l.Socket, err)
	}
	ln.(*net.UnixListener).SetUnlinkOnClose(false)
	// The directory is ours and 0700: the window before chmod exposes nothing.
	if err := os.Chmod(l.Socket, 0o600); err != nil {
		_ = ln.Close()
		return fmt.Errorf("hcompat: chmod socket: %w", err)
	}
	fi, err := os.Lstat(l.Socket)
	if err != nil {
		_ = ln.Close()
		return fmt.Errorf("hcompat: stat socket: %w", err)
	}
	if lock != nil {
		// Where we listen, as herdr-core writes it (no newline).
		if err := lock.Truncate(0); err == nil {
			_, err = lock.WriteAt([]byte(l.Socket), 0)
		}
		if err != nil {
			_ = ln.Close()
			_ = os.Remove(l.Socket)
			return fmt.Errorf("hcompat: write %s: %w", l.Lock, err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	l.mu.Lock()
	l.ln, l.lock, l.sockInfo, l.created = ln, lock, fi, created
	l.conns = map[net.Conn]struct{}{}
	l.ctx, l.cancel = ctx, cancel
	l.since = time.Now()
	l.mu.Unlock()
	return nil
}

// Since is when Listen bound the socket.
func (l *Listener) Since() time.Time {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.since
}

// Serve accepts connections until Close. Call it after a successful Listen.
func (l *Listener) Serve() {
	l.mu.Lock()
	ln := l.ln
	l.mu.Unlock()
	if ln == nil {
		return
	}
	var backoff time.Duration
	for {
		conn, err := ln.Accept()
		if err != nil {
			l.mu.Lock()
			closed := l.closed
			l.mu.Unlock()
			if closed || errors.Is(err, net.ErrClosed) {
				return
			}
			backoff = min(max(2*backoff, 5*time.Millisecond), time.Second)
			l.Log.Warn("hc accept failed; retrying", "err", err, "backoff", backoff)
			time.Sleep(backoff)
			continue
		}
		backoff = 0
		if !l.track(conn) {
			_ = conn.Close()
			continue
		}
		go func() {
			defer l.wg.Done()
			defer l.untrack(conn)
			defer conn.Close()
			if l.Admit != nil {
				if err := l.Admit(conn); err != nil {
					l.Log.Warn("hc connection refused", "err", err)
					refuse(conn, err.Error())
					return
				}
			}
			if err := l.Server.Serve(l.ctx, conn, conn); err != nil && l.ctx.Err() == nil {
				l.Log.Debug("hc connection ended", "err", err)
			}
		}()
	}
}

func (l *Listener) track(c net.Conn) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	maxConns := l.MaxConns
	if maxConns <= 0 {
		maxConns = DefaultMaxConns
	}
	if l.closed || len(l.conns) >= maxConns {
		if !l.closed {
			l.Log.Warn("hc connection limit reached; rejecting", "max", maxConns)
		}
		return false
	}
	l.conns[c] = struct{}{}
	l.wg.Add(1) // under mu: Close waits only after closed is set
	return true
}

func (l *Listener) untrack(c net.Conn) {
	l.mu.Lock()
	delete(l.conns, c)
	l.mu.Unlock()
}

// Close stops accepting, closes every connection, removes the socket (only
// if it is still the one bound), empties and removes core.lock while still
// holding it, removes the directories Listen created when they are empty and
// releases the lock. It is idempotent and waits for the connections.
func (l *Listener) Close() {
	l.mu.Lock()
	if l.closed || l.ln == nil {
		l.closed = true
		l.mu.Unlock()
		return
	}
	l.closed = true
	ln, lock, fi, created, cancel := l.ln, l.lock, l.sockInfo, l.created, l.cancel
	conns := make([]net.Conn, 0, len(l.conns))
	for c := range l.conns {
		conns = append(conns, c)
	}
	l.mu.Unlock()
	_ = ln.Close()
	cancel()
	for _, c := range conns {
		_ = c.Close()
	}
	l.wg.Wait()
	if cur, err := os.Lstat(l.Socket); err == nil && os.SameFile(fi, cur) {
		_ = os.Remove(l.Socket)
	} else if err == nil {
		l.Log.Warn("hc socket replaced by another process; not removing", "socket", l.Socket)
	}
	if lock != nil {
		_ = lock.Truncate(0)
		if cur, err := os.Lstat(l.Lock); err == nil {
			if mine, err := lock.Stat(); err == nil && os.SameFile(mine, cur) {
				_ = os.Remove(l.Lock)
			}
		}
		_ = lock.Close()
	}
	removeDirs(created)
}

// refuse answers the peer's first request (if it sends one in time) with a
// forbidden error, so a client shows why instead of a bare hang-up.
func refuse(conn net.Conn, msg string) {
	_ = conn.SetDeadline(time.Now().Add(refuseTimeout))
	id := json.RawMessage("null")
	line, _ := bufio.NewReaderSize(io.LimitReader(conn, 64*1024), 4096).ReadBytes('\n')
	var req request
	if json.Unmarshal(line, &req) == nil && req.ID != nil {
		id = req.ID
	}
	b, _ := json.Marshal(response{JSONRPC: "2.0", ID: id, Error: newError("forbidden", msg)})
	_, _ = conn.Write(append(b, '\n'))
}

// privateDir creates dir (and missing parents) with mode 0700 and returns
// the directories it created, deepest first. An existing dir must belong to
// this user.
func privateDir(dir string) ([]string, error) {
	var missing []string
	for d := dir; ; d = filepath.Dir(d) {
		fi, err := os.Lstat(d)
		if err == nil {
			if !fi.IsDir() {
				return nil, fmt.Errorf("hcompat: %s is not a directory", d)
			}
			if d == dir {
				if st, ok := fi.Sys().(*syscall.Stat_t); ok && int(st.Uid) != os.Getuid() {
					return nil, fmt.Errorf("hcompat: %s belongs to uid %d, not %d", d, st.Uid, os.Getuid())
				}
			}
			break
		}
		if !errors.Is(err, os.ErrNotExist) {
			return nil, err
		}
		missing = append(missing, d)
		if filepath.Dir(d) == d {
			break
		}
	}
	var created []string
	for i := len(missing) - 1; i >= 0; i-- {
		if err := os.Mkdir(missing[i], 0o700); err != nil && !errors.Is(err, os.ErrExist) {
			removeDirs(created)
			return nil, fmt.Errorf("hcompat: create %s: %w", missing[i], err)
		}
		_ = os.Chmod(missing[i], 0o700)
		created = append([]string{missing[i]}, created...)
	}
	return created, nil
}

// removeDirs removes each directory if it is empty (deepest first).
func removeDirs(dirs []string) {
	for _, d := range dirs {
		_ = syscall.Rmdir(d)
	}
}

// flockNB takes LOCK_EX|LOCK_NB on f, retrying EINTR.
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
