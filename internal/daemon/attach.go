package daemon

import (
	"errors"
	"net"
	"sync"
	"time"

	"github.com/digiogithub/agentd/internal/proto"
	"github.com/digiogithub/agentd/internal/session"
)

const (
	// exitLinger is how long the daemon keeps reading an attach connection after
	// sending the exit frame, waiting for the client to close it.
	exitLinger = 5 * time.Second
	// dropRecheck is how long the output pump waits, after its subscriber
	// channel closed, to tell a session exit from a slow-subscriber drop.
	dropRecheck = 200 * time.Millisecond

	// DefaultAttachWriteTimeout is the default Server.AttachWriteTimeout.
	DefaultAttachWriteTimeout = 10 * time.Second

	// Bounds of the per-attach input queue between the frame reader and the
	// PTY writer. Input beyond them is dropped (the child is not reading).
	inputQueueFrames = 64
	inputQueueBytes  = 1 << 20
)

// handleAttach serves the attach op. It returns false if the request failed
// before the upgrade (an error response was sent and the connection stays in
// JSON mode), true once the connection was upgraded and the attach has ended.
//
// Three goroutines serve an attach: the output pump (session → client, each
// frame under a write deadline: a client that stops reading is dropped), the
// frame reader (this goroutine) and an input writer (client keystrokes → PTY,
// via a bounded queue), so a child that stops reading its input never stalls
// resize or detach handling.
func (s *Server) handleAttach(conn net.Conn, codec *proto.Codec, req proto.Request) bool {
	var a proto.AttachArgs
	err := req.DecodeArgs(&a)
	var e *entry
	if err == nil {
		e, err = s.lookup(a.Name)
	}
	if err != nil {
		_ = codec.WriteResponse(proto.ErrorResponse(err))
		return false
	}
	ok, _ := proto.OKResponse(nil)
	if err := codec.WriteResponse(ok); err != nil {
		return true
	}

	sess := e.sess
	writeTimeout := s.AttachWriteTimeout
	if writeTimeout <= 0 {
		writeTimeout = DefaultAttachWriteTimeout
	}
	fw := proto.NewFrameWriter(conn)
	// send writes one frame (or one split Data payload) under a deadline.
	send := func(write func() error) error {
		_ = conn.SetWriteDeadline(time.Now().Add(writeTimeout))
		return write()
	}
	c := &attachClient{cols: a.Cols, rows: a.Rows}
	e.addClient(c)
	defer e.removeClient(c)
	s.Log.Info("client attached", "name", a.Name, "cols", a.Cols, "rows", a.Rows)
	defer s.Log.Info("client detached", "name", a.Name)

	redraw, id, ch := sess.SubscribeWithRedraw()
	var subMu sync.Mutex
	subID := id
	stop := make(chan struct{})
	pumpDone := make(chan struct{})
	if err := send(func() error { return fw.Data(redraw) }); err != nil {
		sess.Unsubscribe(id)
		return true
	}

	go func() {
		defer close(pumpDone)
		for {
			for chunk := range ch {
				if err := send(func() error { return fw.Data(chunk) }); err != nil {
					s.Log.Debug("attach client dropped", "name", a.Name, "err", err)
					_ = conn.Close()
					return
				}
			}
			// The channel closed: we unsubscribed, the session exited, or the
			// subscriber was dropped for being slow.
			select {
			case <-stop:
				return
			case <-sess.Done():
				code, _ := sess.ExitCode()
				_ = send(func() error { return fw.Exit(code) })
				_ = conn.SetReadDeadline(time.Now().Add(exitLinger))
				return
			case <-time.After(dropRecheck):
			}
			// Dropped: resubscribe and resynchronize with a full redraw.
			var redraw []byte
			redraw, id, ch = sess.SubscribeWithRedraw()
			subMu.Lock()
			subID = id
			subMu.Unlock()
			select {
			case <-stop:
				sess.Unsubscribe(id)
				return
			default:
			}
			if err := send(func() error { return fw.Data(redraw) }); err != nil {
				_ = conn.Close()
				return
			}
		}
	}()

	in := newInputQueue()
	go in.run(func(p []byte) error {
		_, err := sess.Write(p, true)
		if err != nil && !errors.Is(err, session.ErrExited) {
			s.Log.Debug("attach write", "name", a.Name, "err", err)
		}
		return err
	})
	// Terminal replies (termreply.go) go through their own queue as non-human
	// input, and only from the most recently active client.
	replies := newInputQueue()
	go replies.run(func(p []byte) error {
		_, err := sess.Write(p, false)
		return err
	})

	r := codec.Reader()
loop:
	for {
		f, err := proto.ReadFrame(r)
		if err != nil {
			break
		}
		switch f.Type {
		case proto.FrameData:
			input, reply := splitTermReplies(f.Payload)
			if len(input) > 0 {
				e.activate(c)
				if !in.push(input) {
					s.Log.Debug("attach input dropped (child not reading)", "name", a.Name, "bytes", len(input))
				}
			}
			if len(reply) > 0 && e.isActive(c) {
				_ = replies.push(reply)
			}
		case proto.FrameResize:
			cols, rows, _ := proto.ParseResize(f.Payload) // validated by ReadFrame
			e.resize(c, cols, rows)
		case proto.FrameDetach:
			break loop
		}
	}
	in.close() // the writer finishes queued input in the background
	replies.close()
	close(stop)
	subMu.Lock()
	sess.Unsubscribe(subID)
	subMu.Unlock()
	<-pumpDone
	return true
}

// inputQueue is a bounded FIFO of input chunks drained by one writer
// goroutine. push never blocks.
type inputQueue struct {
	ch chan []byte

	mu     sync.Mutex
	bytes  int
	closed bool
}

func newInputQueue() *inputQueue {
	return &inputQueue{ch: make(chan []byte, inputQueueFrames)}
}

// push enqueues p, or reports false (dropping it) if the queue is full or
// closed.
func (q *inputQueue) push(p []byte) bool {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.closed || q.bytes+len(p) > inputQueueBytes {
		return false
	}
	select {
	case q.ch <- p:
		q.bytes += len(p)
		return true
	default:
		return false
	}
}

func (q *inputQueue) close() {
	q.mu.Lock()
	defer q.mu.Unlock()
	if !q.closed {
		q.closed = true
		close(q.ch)
	}
}

func (q *inputQueue) took(p []byte) {
	q.mu.Lock()
	q.bytes -= len(p)
	q.mu.Unlock()
}

// run writes queued chunks until the queue is closed and drained. After a
// failed write (timeout: the child is not reading; or exit) the chunks queued
// at that moment are discarded instead of each waiting for its own timeout.
func (q *inputQueue) run(write func([]byte) error) {
	for p := range q.ch {
		q.took(p)
		if err := write(p); err == nil {
			continue
		}
		for n := len(q.ch); n > 0; n-- {
			p, ok := <-q.ch
			if !ok {
				return
			}
			q.took(p)
		}
	}
}
