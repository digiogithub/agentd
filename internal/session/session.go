// Package session runs one child process in a PTY, mirrors its output into a
// headless VT emulator (github.com/charmbracelet/x/vt), fans the raw output out
// to subscribers (attached clients), keeps a ring buffer and an optional log
// file, serializes input from humans and the delivery injector, and tracks
// output / human-activity timestamps for idle detection.
//
// Concurrency model:
//
//   - One reader goroutine owns the PTY output. For every chunk it writes the
//     log file, then, under Session.outMu (write lock), feeds the emulator,
//     appends to the ring buffer and fans the chunk out to subscribers.
//   - Every emulator mutation (Write, Resize) happens under outMu's write lock,
//     so snapshot methods (Screen, Redraw, ...) take the read lock and observe
//     a consistent screen, and SubscribeWithRedraw can atomically pair a
//     snapshot with a subscription.
//   - The emulator answers terminal queries (DA1/DA2, DSR, CPR, DECRQM, OSC
//     color queries, in-band resize) by writing to an unbuffered io.Pipe while
//     holding its own lock. A dedicated pump goroutine drains that pipe into a
//     bounded in-memory queue, and a separate writer goroutine forwards the
//     queue into the PTY input. The pump never blocks on the PTY, so the
//     emulator (and therefore the reader) can never deadlock even if the child
//     stops reading its stdin.
//   - PTY input is serialized by inMu: each Write/Paste call is written as one
//     unit, so injected pastes and human keystrokes never interleave mid-sequence.
//     Writes use per-chunk deadlines on the non-blocking master, so a child that
//     stops reading stdin makes Write fail with ErrWriteTimeout instead of
//     holding inMu forever; Close aborts pending writes.
package session

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
	"unsafe"

	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/vt"
	"github.com/creack/pty"

	"github.com/digiogithub/agentd/internal/screen"
)

const (
	// DefaultCols and DefaultRows are used when Spec.Cols / Spec.Rows are zero.
	DefaultCols = 120
	DefaultRows = 40

	// RingSize is the number of most recent raw output bytes kept in memory.
	RingSize = 1 << 20

	// SubscriberBuffer is the capacity (in chunks) of each subscriber channel.
	// A subscriber whose channel is full when a new chunk arrives is dropped:
	// its channel is closed and it is removed. The reader never blocks.
	SubscriberBuffer = 256

	// maxPendingReplies bounds the queue of emulator replies waiting to be
	// written to the PTY input. Replies beyond it are discarded.
	maxPendingReplies = 64 << 10

	// exitDrainTimeout is how long, after the child exits, the session waits for
	// the PTY to reach EOF (all output drained) before force-closing the master.
	// A grandchild that keeps the slave open would otherwise keep it alive.
	exitDrainTimeout = 2 * time.Second

	// closeGrace is how long Close waits after SIGHUP before sending SIGKILL.
	closeGrace = 2 * time.Second

	// DefaultWriteTimeout is used when Spec.WriteTimeout is zero: a PTY input
	// write that makes no progress for this long fails with ErrWriteTimeout.
	DefaultWriteTimeout = 5 * time.Second

	// writeChunk is the unit of PTY input writes; the write timeout applies to
	// each chunk, so a slow but steady reader never times out.
	writeChunk = 4096
)

// State values returned by Session.State.
const (
	StateRunning = "running"
	StateExited  = "exited"
)

// ErrExited is returned by input and signal operations on an exited session.
var ErrExited = errors.New("session: process has exited")

// ErrWriteTimeout is returned by Write when the child does not drain its
// input for Spec.WriteTimeout (it stopped reading stdin).
var ErrWriteTimeout = errors.New("session: input write timed out (child not reading)")

// Bracketed paste markers.
const (
	pasteStart = "\x1b[200~"
	pasteEnd   = "\x1b[201~"
)

// Spec describes the child process of a session.
type Spec struct {
	Name string
	Cwd  string   // working directory; empty inherits the daemon's
	Cmd  []string // argv; Cmd[0] is looked up in PATH
	// BaseEnv is the base environment ("KEY=VALUE" entries); nil means
	// os.Environ(). Env is applied on top of it, then TERM=xterm-256color.
	BaseEnv []string
	Env     map[string]string // added on top of BaseEnv; TERM is forced to xterm-256color
	Cols    uint16            // initial size; 0 means DefaultCols
	Rows    uint16            // initial size; 0 means DefaultRows
	LogPath string            // if set, raw PTY output is appended to this file
	// LogMaxBytes caps the log file: when a write would exceed it, the file is
	// rotated to LogPath+".1" (replacing any previous one). 0 = no cap.
	LogMaxBytes int64
	// WriteTimeout bounds each PTY input chunk write; 0 means
	// DefaultWriteTimeout.
	WriteTimeout time.Duration
}

// Session is one running (or exited) PTY child plus its emulated screen.
type Session struct {
	spec      Spec
	cmd       *exec.Cmd
	ptmx      *os.File
	pid       int
	startedAt time.Time

	emu    *vt.SafeEmulator
	replyW *io.PipeWriter // the emulator's reply pipe; closed on exit

	// outMu guards every emulator mutation and the fields below.
	outMu    sync.RWMutex
	subs     map[int]chan []byte
	nextID   int
	ring     *ring
	cols     uint16
	rows     uint16
	decModes map[int]bool // DEC private modes the child set/reset (via callbacks)
	// Synchronized output (DEC 2026), guarded by outMu: syncSince is when the
	// open update began (zero = none open), syncFrames counts completed ones.
	// Updated in order by trackMode, so a set and reset inside one PTY chunk
	// still count one frame.
	syncSince  time.Time
	syncFrames uint64
	title      string
	exited     bool

	// Interpretation state (snapshot.go); written under outMu by the emulator
	// callbacks and handleOutput.
	oscCwd     string // last OSC 7 directory
	bells      uint64
	lastBell   time.Time
	notifies   uint64
	lastNotify screen.Notify

	outputGen atomic.Uint64 // PTY output chunks fed to the emulator (OutputGen)
	chMu      sync.Mutex
	changed   chan struct{} // closed and replaced after every chunk (Changed)
	snapCache snapshotCache // last emulator-derived Snapshot (snapshot.go)

	strFilter stringFilter // fixes UTF-8 inside OSC/DCS strings before the emulator (oscfix.go)
	emuBuf    []byte       // reused filter output buffer

	// Frame-aligned chunking for children that use synchronized updates
	// (syncgate.go): readMu serializes handleOutput between readLoop and the
	// hold timer; held is the trailing, still open frame kept back.
	readMu    sync.Mutex
	held      []byte
	holdTimer *time.Timer

	inMu sync.Mutex // serializes PTY input

	// dlMu orders write-deadline updates against Close: once inputClosed is
	// set (under dlMu) no Write arms a new deadline, and Close's expired
	// deadline aborts any write in progress.
	dlMu        sync.Mutex
	inputClosed bool

	lastOutput atomic.Int64 // unix nanos
	lastHuman  atomic.Int64 // unix nanos; real human keystrokes (attach clients) only
	lastAct    atomic.Int64 // unix nanos; draft-neutral keys written by the act op (WriteAct)
	draftAt    atomic.Int64 // unix nanos of the last uncommitted human input; 0 = none (draft.go)

	replyMu  sync.Mutex
	replyBuf []byte
	replySig chan struct{}
	replyOff atomic.Bool // true = drop emulator replies (see SetReplyForwarding)

	logf    *os.File // touched only by the reader goroutine, then by the waiter after it ends
	logSize int64    // bytes in logf; reader goroutine only

	readerDone chan struct{}
	done       chan struct{}

	exitMu   sync.Mutex
	exitCode int
	hasExit  bool

	closeOnce sync.Once
}

// Start spawns spec.Cmd in a new PTY (new session, controlling terminal) and
// starts the I/O goroutines.
func Start(spec Spec) (*Session, error) {
	if len(spec.Cmd) == 0 || spec.Cmd[0] == "" {
		return nil, errors.New("session: empty command")
	}
	if spec.Cols == 0 {
		spec.Cols = DefaultCols
	}
	if spec.Rows == 0 {
		spec.Rows = DefaultRows
	}
	if spec.WriteTimeout <= 0 {
		spec.WriteTimeout = DefaultWriteTimeout
	}
	spec = cloneSpec(spec)

	var logf *os.File
	var logSize int64
	if spec.LogPath != "" {
		if err := os.MkdirAll(filepath.Dir(spec.LogPath), 0o700); err != nil {
			return nil, fmt.Errorf("session: log dir: %w", err)
		}
		f, err := os.OpenFile(spec.LogPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
		if err != nil {
			return nil, fmt.Errorf("session: log file: %w", err)
		}
		if fi, err := f.Stat(); err == nil {
			logSize = fi.Size()
		}
		logf = f
	}

	cmd := exec.Command(spec.Cmd[0], spec.Cmd[1:]...)
	cmd.Dir = spec.Cwd
	cmd.Env = buildEnv(spec.BaseEnv, spec.Env)
	// pty.StartWithSize sets Setsid and Setctty: the child leads a new session
	// and process group, so Signal can target the whole group.
	ptmx, err := pty.StartWithSize(cmd, &pty.Winsize{Cols: spec.Cols, Rows: spec.Rows})
	if err != nil {
		if logf != nil {
			_ = logf.Close()
		}
		return nil, fmt.Errorf("session: start %q: %w", spec.Cmd[0], err)
	}
	ptmx = pollable(ptmx)

	s := &Session{
		spec:       spec,
		cmd:        cmd,
		ptmx:       ptmx,
		pid:        cmd.Process.Pid,
		startedAt:  time.Now(),
		emu:        vt.NewSafeEmulator(int(spec.Cols), int(spec.Rows)),
		subs:       make(map[int]chan []byte),
		ring:       newRing(RingSize),
		cols:       spec.Cols,
		rows:       spec.Rows,
		decModes:   make(map[int]bool),
		replySig:   make(chan struct{}, 1),
		logf:       logf,
		logSize:    logSize,
		readerDone: make(chan struct{}),
		done:       make(chan struct{}),
		changed:    make(chan struct{}),
	}
	s.replyW, _ = s.emu.InputPipe().(*io.PipeWriter)
	// Callbacks fire inside emu.Write, i.e. under outMu's write lock.
	s.emu.SetCallbacks(vt.Callbacks{
		Title:            func(t string) { s.title = t },
		Bell:             s.onBell,
		WorkingDirectory: s.onWorkingDirectory,
		EnableMode:       func(m ansi.Mode) { s.trackMode(m, true) },
		DisableMode:      func(m ansi.Mode) { s.trackMode(m, false) },
	})

	s.registerNotifyHandlers()

	go s.readLoop()
	go s.replyPump()
	go s.replyWriter()
	go s.wait()
	return s, nil
}

// pollable re-opens the PTY master as a non-blocking, poller-registered file so
// that Close reliably unblocks a pending Read. creack/pty calls Fd() during
// setup, which switches the *os.File to blocking mode permanently.
func pollable(f *os.File) *os.File {
	syscall.ForkLock.RLock()
	nfd, err := syscall.Dup(int(f.Fd()))
	if err == nil {
		syscall.CloseOnExec(nfd)
	}
	syscall.ForkLock.RUnlock()
	if err != nil {
		return f
	}
	if err := syscall.SetNonblock(nfd, true); err != nil {
		_ = syscall.Close(nfd)
		return f
	}
	_ = f.Close()
	return os.NewFile(uintptr(nfd), "/dev/ptmx")
}

func buildEnv(base []string, extra map[string]string) []string {
	if base == nil {
		base = os.Environ()
	}
	env := append([]string(nil), base...)
	keys := make([]string, 0, len(extra))
	for k := range extra {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		env = append(env, k+"="+extra[k])
	}
	// Later entries win in exec.Cmd (duplicates are removed keeping the last).
	return append(env, "TERM=xterm-256color")
}

func cloneSpec(s Spec) Spec {
	s.Cmd = append([]string(nil), s.Cmd...)
	if s.BaseEnv != nil {
		s.BaseEnv = append([]string(nil), s.BaseEnv...)
	}
	if s.Env != nil {
		env := make(map[string]string, len(s.Env))
		for k, v := range s.Env {
			env[k] = v
		}
		s.Env = env
	}
	return s
}

func (s *Session) trackMode(m ansi.Mode, on bool) {
	if d, ok := m.(ansi.DECMode); ok {
		s.decModes[int(d)] = on
		if d == 2026 {
			switch {
			case on && s.syncSince.IsZero():
				s.syncSince = time.Now()
			case !on && !s.syncSince.IsZero():
				s.syncSince = time.Time{}
				s.syncFrames++
			}
		}
	}
}

// SyncTimeout is how long an unterminated synchronized update (DEC 2026) is
// honoured; terminals give up on a stuck BSU too.
const SyncTimeout = time.Second

// InSyncUpdate reports whether the child has an open synchronized update
// (BSU seen, ESU not yet) younger than SyncTimeout: the screen is then a
// half-drawn frame.
func (s *Session) InSyncUpdate() bool {
	s.outMu.RLock()
	defer s.outMu.RUnlock()
	return !s.syncSince.IsZero() && time.Since(s.syncSince) < SyncTimeout
}

// SyncFrames is the number of completed synchronized updates (BSU..ESU) the
// child has drawn; 0 means it does not use DEC 2026.
func (s *Session) SyncFrames() uint64 {
	s.outMu.RLock()
	defer s.outMu.RUnlock()
	return s.syncFrames
}

// writeLog appends chunk to the log file, rotating it to LogPath+".1" first
// when the write would exceed Spec.LogMaxBytes. Called by the reader only.
func (s *Session) writeLog(chunk []byte) {
	if s.logf == nil {
		return
	}
	if limit := s.spec.LogMaxBytes; limit > 0 && s.logSize > 0 && s.logSize+int64(len(chunk)) > limit {
		_ = s.logf.Close()
		s.logf = nil
		_ = os.Rename(s.spec.LogPath, s.spec.LogPath+".1")
		f, err := os.OpenFile(s.spec.LogPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC|os.O_APPEND, 0o600)
		if err != nil {
			return // logging stops; the session keeps running
		}
		s.logf, s.logSize = f, 0
	}
	n, _ := s.logf.Write(chunk)
	s.logSize += int64(n)
}

// readLoop is the single owner of PTY output.
func (s *Session) readLoop() {
	defer close(s.readerDone)
	buf := make([]byte, 32*1024)
	for {
		n, err := s.ptmx.Read(buf)
		if n > 0 {
			s.feed(append([]byte(nil), buf[:n]...))
		}
		if err != nil {
			s.flushHeld()
			return
		}
	}
}

func (s *Session) handleOutput(chunk []byte) {
	s.lastOutput.Store(time.Now().UnixNano())
	s.writeLog(chunk)
	s.outMu.Lock()
	defer s.outMu.Unlock()
	s.emuBuf = s.strFilter.filter(s.emuBuf[:0], chunk)
	_, _ = s.emu.Write(s.emuBuf)
	s.outputGen.Add(1)
	// Stamp again now that the screen shows the chunk: an observer that ran
	// between the first stamp and the emulator write saw the old screen with
	// the new timestamp and must still see this output as newer (AGED-US-0037).
	s.lastOutput.Store(time.Now().UnixNano())
	s.ring.Write(chunk)
	for id, ch := range s.subs {
		select {
		case ch <- chunk:
		default:
			// Slow subscriber: drop it rather than block the reader.
			close(ch)
			delete(s.subs, id)
		}
	}
	s.signalChanged()
}

// replyPump drains the emulator's reply pipe into a bounded queue. It never
// blocks on anything but the pipe itself, so emulator writes cannot deadlock.
func (s *Session) replyPump() {
	buf := make([]byte, 4096)
	for {
		n, err := s.emu.Read(buf)
		if n > 0 && !s.replyOff.Load() {
			s.replyMu.Lock()
			if len(s.replyBuf)+n <= maxPendingReplies {
				s.replyBuf = append(s.replyBuf, buf[:n]...)
			}
			s.replyMu.Unlock()
			select {
			case s.replySig <- struct{}{}:
			default:
			}
		}
		if err != nil {
			return
		}
	}
}

// replyWriter forwards queued emulator replies into the PTY input.
func (s *Session) replyWriter() {
	for {
		select {
		case <-s.replySig:
		case <-s.done:
			return
		}
		s.replyMu.Lock()
		b := s.replyBuf
		s.replyBuf = nil
		s.replyMu.Unlock()
		if len(b) > 0 && !s.replyOff.Load() {
			_, _ = s.Write(b, false)
		}
	}
}

// wait reaps the child, drains output and finalizes the session.
func (s *Session) wait() {
	err := s.cmd.Wait()
	code := exitCodeOf(s.cmd.ProcessState, err)

	select {
	case <-s.readerDone:
	case <-time.After(exitDrainTimeout):
	}
	_ = s.ptmx.Close() // unblocks the reader if a grandchild still holds the slave
	<-s.readerDone

	if s.replyW != nil {
		_ = s.replyW.CloseWithError(io.EOF) // stops replyPump; the emulator stays readable
	}
	if s.logf != nil {
		_ = s.logf.Close()
	}

	s.outMu.Lock()
	s.exited = true
	for id, ch := range s.subs {
		close(ch)
		delete(s.subs, id)
	}
	s.outMu.Unlock()

	s.exitMu.Lock()
	s.exitCode, s.hasExit = code, true
	s.exitMu.Unlock()
	close(s.done)
	s.signalChanged()
}

// exitCodeOf returns the exit status, or 128+signal if the child was killed by
// a signal (shell convention), or -1 if unknown.
func exitCodeOf(ps *os.ProcessState, _ error) int {
	if ps == nil {
		return -1
	}
	if ws, ok := ps.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
		return 128 + int(ws.Signal())
	}
	return ps.ExitCode()
}

// ---- Subscribers ----

// Subscribe registers a subscriber that receives every raw PTY output chunk
// from now on. Chunks are shared between subscribers and must not be modified.
//
// The channel has SubscriberBuffer slots; if it is full when a chunk arrives
// the subscriber is dropped and its channel closed (check Done/State to tell a
// drop from an exit). The channel is also closed on Unsubscribe and when the
// child exits. Subscribing to an exited session returns a closed channel.
func (s *Session) Subscribe() (int, <-chan []byte) {
	s.outMu.Lock()
	defer s.outMu.Unlock()
	return s.subscribeLocked()
}

// SubscribeWithRedraw atomically takes a Redraw snapshot and subscribes, so
// that redraw followed by the channel's chunks reproduces the screen with no
// output lost or duplicated in between.
func (s *Session) SubscribeWithRedraw() ([]byte, int, <-chan []byte) {
	s.outMu.Lock()
	defer s.outMu.Unlock()
	redraw := s.redrawLocked()
	id, ch := s.subscribeLocked()
	return redraw, id, ch
}

func (s *Session) subscribeLocked() (int, <-chan []byte) {
	s.nextID++
	id := s.nextID
	ch := make(chan []byte, SubscriberBuffer)
	if s.exited {
		close(ch)
		return id, ch
	}
	s.subs[id] = ch
	return id, ch
}

// Unsubscribe removes a subscriber and closes its channel. Unknown or already
// dropped ids are ignored.
func (s *Session) Unsubscribe(id int) {
	s.outMu.Lock()
	defer s.outMu.Unlock()
	if ch, ok := s.subs[id]; ok {
		close(ch)
		delete(s.subs, id)
	}
}

// ---- Input ----

// Write sends p to the child's PTY input as one unit (serialized with every
// other Write/Paste). human marks the bytes as typed by a person, which
// updates LastHumanInput.
//
// Write never blocks indefinitely: the bytes are written in chunks, and if
// the child does not drain a chunk within Spec.WriteTimeout (it stopped
// reading stdin), Write returns the bytes written so far and an error
// wrapping ErrWriteTimeout. Close aborts a pending Write (ErrExited).
func (s *Session) Write(p []byte, human bool) (int, error) {
	return s.write(p, human, false)
}

// WriteAct is Write for keys typed by the act op on behalf of an API caller:
// like human input it clears the attention flag, holds back delivery and
// tracks drafts (Enter / Ctrl-C clear one), but it records the time in
// LastActInput, never in LastHumanInput, so an act is never mistaken for a
// person typing.
func (s *Session) WriteAct(p []byte) (int, error) { return s.write(p, false, true) }

func (s *Session) write(p []byte, human, act bool) (int, error) {
	select {
	case <-s.done:
		return 0, ErrExited
	default:
	}
	s.inMu.Lock()
	defer s.inMu.Unlock()
	if human {
		s.lastHuman.Store(time.Now().UnixNano())
		s.trackDraft(p)
	}
	if act {
		s.lastAct.Store(time.Now().UnixNano())
		s.trackDraft(p)
	}
	written := 0
	for written < len(p) {
		if !s.armWriteDeadline() {
			return written, ErrExited
		}
		end := min(written+writeChunk, len(p))
		n, err := s.ptmx.Write(p[written:end])
		written += n
		if err != nil {
			return written, s.writeErr(err)
		}
	}
	return written, nil
}

// armWriteDeadline sets the deadline for the next input chunk. It returns
// false once Close has started.
func (s *Session) armWriteDeadline() bool {
	s.dlMu.Lock()
	defer s.dlMu.Unlock()
	if s.inputClosed {
		return false
	}
	// Fails only if the master is not pollable (see pollable); the write is
	// then unbounded, as before.
	_ = s.ptmx.SetWriteDeadline(time.Now().Add(s.spec.WriteTimeout))
	return true
}

func (s *Session) writeErr(err error) error {
	switch {
	case errors.Is(err, os.ErrClosed):
		return ErrExited
	case errors.Is(err, os.ErrDeadlineExceeded):
		s.dlMu.Lock()
		closed := s.inputClosed
		s.dlMu.Unlock()
		if closed {
			return ErrExited
		}
		return fmt.Errorf("%w after %s", ErrWriteTimeout, s.spec.WriteTimeout)
	}
	return err
}

// abortInput makes every pending and future Write fail immediately.
func (s *Session) abortInput() {
	s.dlMu.Lock()
	defer s.dlMu.Unlock()
	s.inputClosed = true
	_ = s.ptmx.SetWriteDeadline(time.Unix(1, 0))
}

// Paste writes text as a bracketed paste (ESC[200~ text ESC[201~) in a single
// serialized write. text is cleaned with PasteSafe, so the content cannot
// terminate the paste early or carry escape sequences. It does not count as
// human input.
func (s *Session) Paste(text string) error {
	_, err := s.Write([]byte(pasteStart+PasteSafe(text)+pasteEnd), false)
	return err
}

// SetReplyForwarding controls whether the emulator's answers to terminal
// queries (DA, CPR, DECRQM, OSC color queries...) are written to the child's
// input. It is enabled by default. The daemon disables it while at least one
// client is attached, because the client's real terminal receives the raw
// output and answers the same queries; forwarding both would make the second
// answer arrive as typed input. Replies produced while disabled are dropped,
// including any still queued when it is disabled.
func (s *Session) SetReplyForwarding(enabled bool) {
	s.replyOff.Store(!enabled)
	if !enabled {
		s.replyMu.Lock()
		s.replyBuf = nil
		s.replyMu.Unlock()
	}
}

// DECMode reports whether the child set DEC private mode m (e.g. 1004 focus
// events).
func (s *Session) DECMode(m int) bool {
	s.outMu.RLock()
	defer s.outMu.RUnlock()
	return s.decModes[m]
}

// SubscribeWithHistory is SubscribeWithRedraw with up to n scrollback lines
// painted first (main screen only): they are printed and scrolled off the
// top, so they land in the client's own scrollback, then the redraw paints
// the screen.
func (s *Session) SubscribeWithHistory(n int) ([]byte, int, <-chan []byte) {
	s.outMu.Lock()
	defer s.outMu.Unlock()
	var b bytes.Buffer
	if e := s.emu.Emulator; n > 0 && !e.IsAltScreen() {
		if sb := e.Scrollback(); sb != nil && sb.Len() > 0 {
			lines := sb.Lines()
			if len(lines) > n {
				lines = lines[len(lines)-n:]
			}
			rows := e.Height()
			b.WriteString("\x1b[?2026h\x1b[0m\x1b[r\x1b[H\x1b[2J")
			for _, l := range lines {
				b.WriteString(l.Render())
				b.WriteString("\x1b[0m\r\n")
			}
			// The last min(len, rows-1) lines are still on screen: scroll them
			// off from the bottom row.
			if k := min(len(lines), rows-1); k > 0 {
				b.WriteString("\x1b[" + strconv.Itoa(rows) + ";1H")
				b.WriteString(strings.Repeat("\n", k))
			}
		}
	}
	b.Write(s.redrawLocked())
	id, ch := s.subscribeLocked()
	return b.Bytes(), id, ch
}

// BracketedPasteEnabled reports whether the child has enabled bracketed paste
// mode (DECSET 2004).
func (s *Session) BracketedPasteEnabled() bool {
	s.outMu.RLock()
	defer s.outMu.RUnlock()
	return s.decModes[2004]
}

// ---- Size ----

// Resize resizes the PTY (the kernel sends SIGWINCH to the child) and the
// emulator together.
func (s *Session) Resize(cols, rows uint16) error {
	if cols == 0 || rows == 0 {
		return fmt.Errorf("session: invalid size %dx%d", cols, rows)
	}
	s.outMu.Lock()
	defer s.outMu.Unlock()
	if !s.exited {
		if err := setWinsize(s.ptmx, cols, rows); err != nil && !errors.Is(err, os.ErrClosed) {
			return fmt.Errorf("session: resize: %w", err)
		}
	}
	s.emu.Resize(int(cols), int(rows))
	s.cols, s.rows = cols, rows
	s.snapCache.reset() // a resize changes the screen without any output
	return nil
}

// setWinsize issues TIOCSWINSZ without calling f.Fd() (which would switch the
// file back to blocking mode).
func setWinsize(f *os.File, cols, rows uint16) error {
	sc, err := f.SyscallConn()
	if err != nil {
		return err
	}
	ws := pty.Winsize{Cols: cols, Rows: rows}
	var errno syscall.Errno
	cerr := sc.Control(func(fd uintptr) {
		_, _, errno = syscall.Syscall(syscall.SYS_IOCTL, fd, uintptr(syscall.TIOCSWINSZ), uintptr(unsafe.Pointer(&ws)))
	})
	if cerr != nil {
		return cerr
	}
	if errno != 0 {
		return errno
	}
	return nil
}

// Size returns the current terminal size.
func (s *Session) Size() (cols, rows uint16) {
	s.outMu.RLock()
	defer s.outMu.RUnlock()
	return s.cols, s.rows
}

// ---- Snapshots ----
//
// All snapshot methods hold outMu's read lock. Since every emulator mutation
// happens under the write lock, it is safe to call the non-synchronized
// *vt.Emulator methods (String, Scrollback) here.

// Screen returns the visible screen as plain text: one line per row, trailing
// spaces trimmed on each line.
func (s *Session) Screen() string {
	s.outMu.RLock()
	defer s.outMu.RUnlock()
	return s.emu.Emulator.String() //nolint:staticcheck // explicit: unsynchronized method, outMu is held
}

// ScreenANSI returns the visible screen with SGR styles and hyperlinks encoded
// as ANSI escape codes (lines separated by "\n").
func (s *Session) ScreenANSI() string {
	s.outMu.RLock()
	defer s.outMu.RUnlock()
	return s.emu.Emulator.Render()
}

// Scrollback returns the main screen's scrollback (lines scrolled off the top,
// oldest first, up to vt.DefaultScrollbackSize) followed by the visible screen,
// as plain text. When the child is in the alternate screen, the visible part
// is the alternate screen.
func (s *Session) Scrollback() string {
	s.outMu.RLock()
	defer s.outMu.RUnlock()
	var b strings.Builder
	if sb := s.emu.Emulator.Scrollback(); sb != nil {
		for _, l := range sb.Lines() {
			b.WriteString(strings.TrimRight(l.String(), " "))
			b.WriteByte('\n')
		}
	}
	b.WriteString(s.emu.Emulator.String()) //nolint:staticcheck // explicit: unsynchronized method, outMu is held
	return b.String()
}

// Cursor returns the 0-based cursor position (column, row).
func (s *Session) Cursor() (x, y int) {
	s.outMu.RLock()
	defer s.outMu.RUnlock()
	p := s.emu.Emulator.CursorPosition()
	return p.X, p.Y
}

// IsAltScreen reports whether the child is using the alternate screen.
func (s *Session) IsAltScreen() bool {
	s.outMu.RLock()
	defer s.outMu.RUnlock()
	return s.emu.Emulator.IsAltScreen()
}

// Title returns the last window title set by the child (OSC 0/2).
func (s *Session) Title() string {
	s.outMu.RLock()
	defer s.outMu.RUnlock()
	return s.title
}

// RecentOutput returns a copy of the last RingSize bytes of raw PTY output.
func (s *Session) RecentOutput() []byte {
	s.outMu.RLock()
	defer s.outMu.RUnlock()
	return s.ring.Bytes()
}

// Redraw returns bytes that reproduce the current screen on a fresh client
// terminal. See redrawLocked.
func (s *Session) Redraw() []byte {
	s.outMu.RLock()
	defer s.outMu.RUnlock()
	return s.redrawLocked()
}

// ---- Activity ----

// LastOutput returns when the child last produced output (zero if never).
func (s *Session) LastOutput() time.Time { return unixNano(s.lastOutput.Load()) }

// LastHumanInput returns when a real human (an attached client) last wrote
// input (zero if never). Keys written by the act op are not included: see
// LastActInput and LastInput.
func (s *Session) LastHumanInput() time.Time { return unixNano(s.lastHuman.Load()) }

// LastActInput returns when WriteAct last wrote (zero if never).
func (s *Session) LastActInput() time.Time { return unixNano(s.lastAct.Load()) }

// LastInput is the later of LastHumanInput and LastActInput: the activity
// that holds back delivery and clears the attention flag.
func (s *Session) LastInput() time.Time {
	h, a := s.lastHuman.Load(), s.lastAct.Load()
	return unixNano(max(h, a))
}

// StartedAt returns when the child was started.
func (s *Session) StartedAt() time.Time { return s.startedAt }

func unixNano(n int64) time.Time {
	if n == 0 {
		return time.Time{}
	}
	return time.Unix(0, n)
}

// ---- Lifecycle ----

// Name returns the session name.
func (s *Session) Name() string { return s.spec.Name }

// Spec returns a copy of the session spec (with size defaults applied).
func (s *Session) Spec() Spec { return cloneSpec(s.spec) }

// Pid returns the child's process id (also its process group id).
func (s *Session) Pid() int { return s.pid }

// Done is closed once the child has exited, output has been drained and all
// subscriber channels have been closed.
func (s *Session) Done() <-chan struct{} { return s.done }

// ExitCode returns the child's exit code and true once it has exited. A child
// killed by a signal reports 128+signal.
func (s *Session) ExitCode() (int, bool) {
	s.exitMu.Lock()
	defer s.exitMu.Unlock()
	return s.exitCode, s.hasExit
}

// State returns StateRunning or StateExited.
func (s *Session) State() string {
	select {
	case <-s.done:
		return StateExited
	default:
		return StateRunning
	}
}

// Signal sends sig to the child's process group.
func (s *Session) Signal(sig os.Signal) error {
	select {
	case <-s.done:
		return ErrExited
	default:
	}
	if ss, ok := sig.(syscall.Signal); ok {
		err := syscall.Kill(-s.pid, ss)
		if err == nil || !errors.Is(err, syscall.ESRCH) {
			return err
		}
	}
	err := s.cmd.Process.Signal(sig)
	if errors.Is(err, os.ErrProcessDone) {
		return ErrExited
	}
	return err
}

// Close terminates the child if it is still running (SIGHUP to the process
// group, then SIGKILL after a grace period) and waits for the session to
// finish. Pending and later Writes fail with ErrExited from the moment Close
// is called. The emulator is kept, so snapshots keep working afterwards.
// Close is idempotent.
func (s *Session) Close() error {
	s.closeOnce.Do(func() {
		s.abortInput() // a Write blocked on a child that stopped reading returns now
		select {
		case <-s.done:
			return
		default:
		}
		_ = s.Signal(syscall.SIGHUP)
		select {
		case <-s.done:
			return
		case <-time.After(closeGrace):
		}
		_ = s.Signal(syscall.SIGKILL)
		_ = s.cmd.Process.Kill()
	})
	<-s.done
	return nil
}
