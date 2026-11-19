package relaywire

import (
	"context"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"os"
	"sync"
	"time"
)

// Mux frame types.
const (
	FrameOpen   byte = 1
	FrameData   byte = 2
	FrameClose  byte = 3
	FrameCredit byte = 4
)

// Mux limits.
const (
	MaxData       = 32 << 10  // payload of one DATA frame
	InitialWindow = 256 << 10 // per stream, per direction
	MaxStreams    = 64        // open streams per tunnel
)

// ErrMuxClosed is returned once the tunnel is gone.
var ErrMuxClosed = errors.New("relay: tunnel closed")

// Transport is what a Mux runs on: an established Secure in production.
type Transport interface {
	Send(ctx context.Context, p []byte) error
	Recv(ctx context.Context) ([]byte, error)
	Close() error
}

// Mux multiplexes streams over one encrypted tunnel. The client side opens
// streams (odd ids); the host side accepts them.
type Mux struct {
	t      Transport
	client bool

	mu      sync.Mutex
	streams map[uint32]*Stream
	nextID  uint32
	err     error
	accept  chan *Stream
	done    chan struct{}
	maxOpen int
}

// NewClientMux starts a client-side mux (Open only).
func NewClientMux(t Transport) *Mux { return newMux(t, true) }

// NewServerMux starts a host-side mux (Accept only).
func NewServerMux(t Transport) *Mux { return newMux(t, false) }

func newMux(t Transport, client bool) *Mux {
	m := &Mux{
		t:       t,
		client:  client,
		streams: map[uint32]*Stream{},
		nextID:  1,
		accept:  make(chan *Stream, MaxStreams),
		done:    make(chan struct{}),
		maxOpen: MaxStreams,
	}
	go m.readLoop()
	return m
}

// Done is closed when the tunnel ends.
func (m *Mux) Done() <-chan struct{} { return m.done }

// Err is the reason the tunnel ended (nil while it is up).
func (m *Mux) Err() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.err
}

// Close ends the tunnel and every stream.
func (m *Mux) Close() error {
	m.fail(ErrMuxClosed)
	return nil
}

func (m *Mux) fail(err error) {
	m.mu.Lock()
	if m.err != nil {
		m.mu.Unlock()
		return
	}
	m.err = err
	streams := m.streams
	m.streams = map[uint32]*Stream{}
	m.mu.Unlock()
	close(m.done)
	_ = m.t.Close()
	for _, s := range streams {
		s.remoteClosed(true)
	}
}

func (m *Mux) sendFrame(typ byte, id uint32, payload []byte) error {
	b := make([]byte, 5+len(payload))
	b[0] = typ
	binary.BigEndian.PutUint32(b[1:5], id)
	copy(b[5:], payload)
	if err := m.t.Send(context.Background(), b); err != nil {
		m.fail(err)
		return err
	}
	return nil
}

// Open opens a new stream (client side).
func (m *Mux) Open() (*Stream, error) {
	if !m.client {
		return nil, errors.New("relay: only the client opens streams")
	}
	m.mu.Lock()
	if m.err != nil {
		m.mu.Unlock()
		return nil, m.err
	}
	id := m.nextID
	m.nextID += 2
	s := newStream(m, id)
	m.streams[id] = s
	m.mu.Unlock()
	if err := m.sendFrame(FrameOpen, id, nil); err != nil {
		return nil, err
	}
	return s, nil
}

// Accept waits for the next stream opened by the client (host side).
func (m *Mux) Accept(ctx context.Context) (*Stream, error) {
	select {
	case s := <-m.accept:
		return s, nil
	case <-m.done:
		return nil, m.Err()
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (m *Mux) readLoop() {
	for {
		b, err := m.t.Recv(context.Background())
		if err != nil {
			m.fail(err)
			return
		}
		if len(b) < 5 {
			m.fail(errors.New("relay: short mux frame"))
			return
		}
		typ, id, payload := b[0], binary.BigEndian.Uint32(b[1:5]), b[5:]
		m.mu.Lock()
		s := m.streams[id]
		m.mu.Unlock()
		switch typ {
		case FrameOpen:
			if m.client || id%2 == 0 || s != nil {
				m.fail(errors.New("relay: bad OPEN"))
				return
			}
			m.mu.Lock()
			full := len(m.streams) >= m.maxOpen
			if !full {
				s = newStream(m, id)
				m.streams[id] = s
			}
			m.mu.Unlock()
			if full {
				_ = m.sendFrame(FrameClose, id, nil)
				continue
			}
			select {
			case m.accept <- s:
			default:
				s.remoteClosed(false)
				_ = m.sendFrame(FrameClose, id, nil)
			}
		case FrameData:
			if s == nil {
				continue // closed locally; data in flight
			}
			if !s.push(payload) {
				m.fail(errors.New("relay: peer exceeded the stream window"))
				return
			}
		case FrameClose:
			if s != nil {
				s.remoteClosed(false)
			}
		case FrameCredit:
			if s != nil && len(payload) == 4 {
				s.addCredit(int(binary.BigEndian.Uint32(payload)))
			}
		default:
			m.fail(errors.New("relay: unknown mux frame"))
			return
		}
	}
}

func (m *Mux) forget(id uint32) {
	m.mu.Lock()
	delete(m.streams, id)
	m.mu.Unlock()
}

// Stream is one bidirectional byte stream of a tunnel; it implements net.Conn.
type Stream struct {
	m  *Mux
	id uint32

	mu        sync.Mutex
	cond      *sync.Cond
	buf       []byte
	consumed  int // read since the last CREDIT
	credit    int // bytes we may still send
	rclosed   bool
	lclosed   bool
	broken    bool // tunnel failed
	rdeadline time.Time
	wdeadline time.Time
	rtimer    *time.Timer
	wtimer    *time.Timer
}

func newStream(m *Mux, id uint32) *Stream {
	s := &Stream{m: m, id: id, credit: InitialWindow}
	s.cond = sync.NewCond(&s.mu)
	return s
}

// ID is the stream id.
func (s *Stream) ID() uint32 { return s.id }

func (s *Stream) push(p []byte) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.lclosed {
		return true
	}
	if len(s.buf)+len(p) > InitialWindow {
		return false
	}
	s.buf = append(s.buf, p...)
	s.cond.Broadcast()
	return true
}

func (s *Stream) remoteClosed(broken bool) {
	s.mu.Lock()
	s.rclosed = true
	if broken {
		s.broken = true
	}
	s.cond.Broadcast()
	s.mu.Unlock()
}

func (s *Stream) addCredit(n int) {
	s.mu.Lock()
	s.credit += n
	s.cond.Broadcast()
	s.mu.Unlock()
}

func expired(d time.Time) bool { return !d.IsZero() && !time.Now().Before(d) }

// Read reads buffered data; io.EOF after the peer closed and the buffer drained.
func (s *Stream) Read(p []byte) (int, error) {
	s.mu.Lock()
	for len(s.buf) == 0 {
		switch {
		case s.lclosed:
			s.mu.Unlock()
			return 0, net.ErrClosed
		case s.rclosed:
			s.mu.Unlock()
			return 0, io.EOF
		case expired(s.rdeadline):
			s.mu.Unlock()
			return 0, os.ErrDeadlineExceeded
		}
		s.cond.Wait()
	}
	n := copy(p, s.buf)
	s.buf = s.buf[n:]
	if len(s.buf) == 0 {
		s.buf = nil
	}
	s.consumed += n
	grant := 0
	if s.consumed >= InitialWindow/4 && !s.rclosed {
		grant, s.consumed = s.consumed, 0
	}
	s.mu.Unlock()
	if grant > 0 {
		var b [4]byte
		binary.BigEndian.PutUint32(b[:], uint32(grant))
		_ = s.m.sendFrame(FrameCredit, s.id, b[:])
	}
	return n, nil
}

// Write sends p, waiting for credit as needed.
func (s *Stream) Write(p []byte) (int, error) {
	written := 0
	for len(p) > 0 {
		s.mu.Lock()
		for s.credit == 0 && !s.lclosed && !s.rclosed && !expired(s.wdeadline) {
			s.cond.Wait()
		}
		switch {
		case s.lclosed:
			s.mu.Unlock()
			return written, net.ErrClosed
		case s.rclosed:
			s.mu.Unlock()
			return written, io.ErrClosedPipe
		case expired(s.wdeadline):
			s.mu.Unlock()
			return written, os.ErrDeadlineExceeded
		}
		n := min(len(p), s.credit, MaxData)
		s.credit -= n
		s.mu.Unlock()
		if err := s.m.sendFrame(FrameData, s.id, p[:n]); err != nil {
			return written, err
		}
		written += n
		p = p[n:]
	}
	return written, nil
}

// Close closes the stream in both directions.
func (s *Stream) Close() error {
	s.mu.Lock()
	if s.lclosed {
		s.mu.Unlock()
		return nil
	}
	s.lclosed = true
	notify := !s.rclosed
	s.buf = nil
	s.cond.Broadcast()
	for _, t := range []*time.Timer{s.rtimer, s.wtimer} {
		if t != nil {
			t.Stop()
		}
	}
	s.mu.Unlock()
	s.m.forget(s.id)
	if notify {
		_ = s.m.sendFrame(FrameClose, s.id, nil)
	}
	return nil
}

type relayAddr struct{}

func (relayAddr) Network() string { return "relay" }
func (relayAddr) String() string  { return "relay" }

// LocalAddr implements net.Conn.
func (s *Stream) LocalAddr() net.Addr { return relayAddr{} }

// RemoteAddr implements net.Conn.
func (s *Stream) RemoteAddr() net.Addr { return relayAddr{} }

// SetDeadline implements net.Conn.
func (s *Stream) SetDeadline(t time.Time) error {
	_ = s.SetReadDeadline(t)
	return s.SetWriteDeadline(t)
}

// SetReadDeadline implements net.Conn.
func (s *Stream) SetReadDeadline(t time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.rdeadline = t
	s.rtimer = s.arm(s.rtimer, t)
	return nil
}

// SetWriteDeadline implements net.Conn.
func (s *Stream) SetWriteDeadline(t time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.wdeadline = t
	s.wtimer = s.arm(s.wtimer, t)
	return nil
}

// arm (s.mu held) wakes waiters when deadline t passes.
func (s *Stream) arm(old *time.Timer, t time.Time) *time.Timer {
	if old != nil {
		old.Stop()
	}
	s.cond.Broadcast()
	if t.IsZero() {
		return nil
	}
	return time.AfterFunc(time.Until(t), func() {
		s.mu.Lock()
		s.cond.Broadcast()
		s.mu.Unlock()
	})
}
