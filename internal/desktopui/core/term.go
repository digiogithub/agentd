package core

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
	"sync"
	"time"

	"github.com/digiogithub/agentd/internal/desktopui/backend"
	"github.com/digiogithub/agentd/internal/desktopui/remote"
	"github.com/digiogithub/agentd/internal/proto"
)

// Terminal opens an interactive terminal on a session: agentd attach (frames
// wrapped into a PTY), a zellij/tmux attach, or the app's login shell. ctx
// bounds the terminal's lifetime. Closing it detaches (the session keeps
// running).
func (b *Backend) Terminal(ctx context.Context, s backend.Session, cols, rows int, observe bool) (backend.PTY, error) {
	switch s.Source {
	case backend.SourceAgentd:
		return b.attachAgentd(ctx, s, cols, rows, observe)
	case backend.SourceShell:
		return b.attachShell(ctx, s)
	default:
		a, err := b.muxAdapter(ctx, s.HostID, s.Source)
		if err != nil {
			return nil, err
		}
		return a.Attach(ctx, s.Name, cols, rows)
	}
}

func clampU16(n int) uint16 {
	if n <= 0 {
		return 0
	}
	if n > 65535 {
		return 65535
	}
	return uint16(n)
}

func (b *Backend) attachAgentd(ctx context.Context, s backend.Session, cols, rows int, observe bool) (backend.PTY, error) {
	if _, err := b.hosts.Get(s.HostID); err != nil {
		return nil, err
	}
	b.activate(s.HostID)
	c, err := b.dialAgentd(ctx, s.HostID)
	if err != nil {
		return nil, err
	}
	stop := context.AfterFunc(ctx, func() { _ = c.Close() })
	// Passive: opening the Terminal tab neither resizes the session nor takes
	// it over; the view becomes the active client on its first key, resize
	// or focus (AGENTS.md "Wire protocol", attach).
	conn, err := c.AttachWith(proto.AttachArgs{Name: s.Name, Cols: clampU16(cols), Rows: clampU16(rows), Observe: observe, Passive: !observe})
	stop()
	if err != nil {
		_ = c.Close()
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, err
	}
	p := newAttachPTY(conn, observe)
	context.AfterFunc(ctx, func() { _ = p.Close() })
	return p, nil
}

// attachPTY is an agentd attach connection seen as a PTY: D frames are the
// output, writes become D frames, Resize an R frame; an E frame ends the
// stream with the exit code.
type attachPTY struct {
	conn    net.Conn
	fw      *proto.FrameWriter
	observe bool

	rmu     sync.Mutex
	pending []byte

	mu     sync.Mutex
	code   int
	done   chan struct{}
	closed bool
	once   sync.Once
}

func newAttachPTY(conn net.Conn, observe bool) *attachPTY {
	return &attachPTY{conn: conn, fw: proto.NewFrameWriter(conn), observe: observe, code: -1, done: make(chan struct{})}
}

func (p *attachPTY) end(code int, exited bool) {
	p.once.Do(func() {
		p.mu.Lock()
		if exited {
			p.code = code
		}
		p.mu.Unlock()
		close(p.done)
	})
}

func (p *attachPTY) Read(b []byte) (int, error) {
	p.rmu.Lock()
	defer p.rmu.Unlock()
	for len(p.pending) == 0 {
		f, err := proto.ReadFrame(p.conn)
		if err != nil {
			p.end(-1, false)
			if errors.Is(err, net.ErrClosed) || errors.Is(err, io.ErrUnexpectedEOF) {
				return 0, io.EOF
			}
			return 0, err
		}
		switch f.Type {
		case proto.FrameData:
			p.pending = f.Payload
		case proto.FrameExit:
			code, perr := proto.ParseExit(f.Payload)
			if perr != nil {
				code = -1
			}
			p.end(code, true)
			return 0, io.EOF
		case proto.FrameDetach:
			p.end(-1, false)
			return 0, io.EOF
		}
	}
	n := copy(b, p.pending)
	p.pending = p.pending[n:]
	return n, nil
}

func (p *attachPTY) Write(b []byte) (int, error) {
	// The terminal writes a focus report (DEC 1004) as one write of its own:
	// send it as an F frame, so the daemon makes this client active on focus
	// and never takes the report for a keystroke (human_grace_ms).
	switch string(b) {
	case "\x1b[I", "\x1b[O":
		in := byte(0)
		if b[2] == 'I' {
			in = 1
		}
		if err := p.fw.Write(proto.FrameFocus, []byte{in}); err != nil {
			return 0, err
		}
		return len(b), nil
	}
	if err := p.fw.Data(b); err != nil {
		return 0, err
	}
	return len(b), nil
}

func (p *attachPTY) Resize(cols, rows int) error {
	if p.observe {
		return nil
	}
	return p.fw.Resize(clampU16(cols), clampU16(rows))
}

// Wait returns the session's exit code once it exits (-1 when the terminal
// was detached or the connection ended first).
func (p *attachPTY) Wait() (int, error) {
	<-p.done
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.code, nil
}

func (p *attachPTY) Close() error {
	p.mu.Lock()
	already := p.closed
	p.closed = true
	p.mu.Unlock()
	if already {
		return nil
	}
	_ = p.conn.SetWriteDeadline(time.Now().Add(time.Second))
	_ = p.fw.Detach()
	err := p.conn.Close()
	p.end(-1, false)
	return err
}

// ---- plain login shells (SourceShell) ----

// shellBacklog is how much output a shell keeps for the next attach.
const shellBacklog = 256 << 10

type shellSession struct {
	hostID, name, project string
	pty                   backend.PTY
	started               time.Time

	mu       sync.Mutex
	buf      []byte
	updated  time.Time
	attached *shellAttach
}

func (s *shellSession) session() backend.Session {
	s.mu.Lock()
	defer s.mu.Unlock()
	return backend.Session{
		HostID: s.hostID, Source: backend.SourceShell, Name: s.name, Harness: "shell",
		Project: s.project, Status: "unknown", Detail: "login shell", Updated: s.updated,
	}
}

// shellAttachMax caps the output queued for a terminal that is not read
// (oldest bytes dropped), so a stalled UI never blocks the shell.
const shellAttachMax = 1 << 20

type shellAttach struct {
	s    *shellSession
	gone chan struct{}
	once sync.Once

	mu   sync.Mutex
	cond *sync.Cond
	data []byte
	eof  bool // shell ended or terminal closed
}

func newShellAttach(s *shellSession, backlog []byte) *shellAttach {
	a := &shellAttach{s: s, gone: make(chan struct{}), data: backlog}
	a.cond = sync.NewCond(&a.mu)
	return a
}

// push queues output (never blocks).
func (a *shellAttach) push(p []byte) {
	a.mu.Lock()
	a.data = append(a.data, p...)
	if len(a.data) > shellAttachMax {
		a.data = append([]byte(nil), a.data[len(a.data)-shellAttachMax:]...)
	}
	a.mu.Unlock()
	a.cond.Broadcast()
}

func (a *shellAttach) finish() {
	a.mu.Lock()
	a.eof = true
	a.mu.Unlock()
	a.cond.Broadcast()
}

func (a *shellAttach) Read(b []byte) (int, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	for len(a.data) == 0 && !a.eof {
		a.cond.Wait()
	}
	if len(a.data) == 0 {
		return 0, io.EOF
	}
	n := copy(b, a.data)
	a.data = a.data[n:]
	return n, nil
}

func (a *shellAttach) Write(b []byte) (int, error) { return a.s.pty.Write(b) }
func (a *shellAttach) Resize(cols, rows int) error { return a.s.pty.Resize(cols, rows) }

// Wait returns the shell's exit code, or -1 once this terminal is detached.
func (a *shellAttach) Wait() (int, error) {
	type res struct {
		code int
		err  error
	}
	ch := make(chan res, 1)
	go func() {
		c, err := a.s.pty.Wait()
		ch <- res{c, err}
	}()
	select {
	case r := <-ch:
		return r.code, r.err
	case <-a.gone:
		return -1, nil
	}
}

func (a *shellAttach) Close() error {
	a.once.Do(func() {
		a.s.mu.Lock()
		if a.s.attached == a {
			a.s.attached = nil
		}
		a.s.mu.Unlock()
		a.finish()
		close(a.gone)
	})
	return nil
}

func (b *Backend) launchShell(ctx context.Context, spec backend.LaunchSpec) (backend.Session, error) {
	sh, err := b.shell(ctx, spec.HostID)
	if err != nil {
		return backend.Session{}, err
	}
	cmd := ""
	if spec.Project != "" {
		cmd = "cd " + remote.ShellQuote(spec.Project) + ` 2>/dev/null; exec "${SHELL:-sh}" -l`
	}
	// The shell outlives the request: bound it by the backend's lifetime.
	p, err := sh.PTY(b.ctx, cmd, 120, 40, nil)
	if err != nil {
		return backend.Session{}, err
	}
	b.mu.Lock()
	b.shellSeq++
	name := "shell-" + strconv.Itoa(b.shellSeq)
	s := &shellSession{hostID: spec.HostID, name: name, project: spec.Project, pty: p, started: time.Now(), updated: time.Now()}
	if b.shells[spec.HostID] == nil {
		b.shells[spec.HostID] = map[string]*shellSession{}
	}
	b.shells[spec.HostID][name] = s
	b.mu.Unlock()
	b.wg.Add(1)
	go func() {
		defer b.wg.Done()
		b.pumpShell(s)
	}()
	b.publishSessions()
	return s.session(), nil
}

// pumpShell copies shell output into the backlog and the attached
// terminal; it removes the session when the shell ends.
func (b *Backend) pumpShell(s *shellSession) {
	buf := make([]byte, 32<<10)
	for {
		n, err := s.pty.Read(buf)
		if n > 0 {
			chunk := append([]byte(nil), buf[:n]...)
			s.mu.Lock()
			s.buf = append(s.buf, chunk...)
			if len(s.buf) > shellBacklog {
				s.buf = append([]byte(nil), s.buf[len(s.buf)-shellBacklog:]...)
			}
			s.updated = time.Now()
			a := s.attached
			s.mu.Unlock()
			if a != nil {
				a.push(chunk)
			}
		}
		if err != nil {
			break
		}
	}
	_, _ = s.pty.Wait()
	s.mu.Lock()
	a := s.attached
	s.attached = nil
	s.mu.Unlock()
	if a != nil {
		a.finish()
	}
	b.mu.Lock()
	if m := b.shells[s.hostID]; m != nil && m[s.name] == s {
		delete(m, s.name)
		if len(m) == 0 {
			delete(b.shells, s.hostID)
		}
	}
	b.mu.Unlock()
	b.publishSessions()
}

func (b *Backend) findShell(s backend.Session) (*shellSession, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if ss := b.shells[s.HostID][s.Name]; ss != nil {
		return ss, nil
	}
	return nil, fmt.Errorf("core: no open shell %s on %s", s.Name, s.HostID)
}

// attachShell attaches to an open shell: the backlog first, then live
// output. A new attach replaces the previous one.
func (b *Backend) attachShell(ctx context.Context, s backend.Session) (backend.PTY, error) {
	ss, err := b.findShell(s)
	if err != nil {
		return nil, err
	}
	ss.mu.Lock()
	prev := ss.attached
	a := newShellAttach(ss, append([]byte(nil), ss.buf...))
	ss.attached = a
	ss.mu.Unlock()
	if prev != nil {
		_ = prev.Close()
	}
	context.AfterFunc(ctx, func() { _ = a.Close() })
	return a, nil
}

func (b *Backend) killShell(s backend.Session) error {
	ss, err := b.findShell(s)
	if err != nil {
		return err
	}
	return ss.pty.Close()
}
