package hcompat

import (
	"encoding/json"
	"errors"
	"io"
	"net"
	"sync"

	"github.com/digiogithub/agentd/internal/proto"
)

// win32Off is part of herdr-core's first paint: whether the program asked for
// win32-input-mode (never, on a Unix PTY).
const win32Off = "\x1b[?9001l"

// can (CAN) starts a paint: it cancels any half-parsed sequence the viewer
// still holds.
const can = 0x18

func terminalAttach(c *conn, params json.RawMessage) (any, func(), *rpcError) {
	var p struct {
		Terminal string `json:"terminal"`
		Cols     uint16 `json:"cols"`
		Rows     uint16 `json:"rows"`
		Observe  bool   `json:"observe"`
		Resumed  bool   `json:"resumed"`
		History  int    `json:"history"`
		// display/FACTS: agentd does not answer display queries itself; the
		// viewers do (as AUTOMATIC_INPUT), so display facts are not needed.
	}
	if err := decode(params, &p); err != nil {
		return nil, nil, err
	}
	if !c.framed {
		return nil, nil, newError("invalid_params", "attach needs framed mode (hello with framed:true)")
	}
	si, rerr := c.s.lookup(p.Terminal)
	if rerr != nil {
		return nil, nil, rerr
	}
	if si.State == "exited" {
		return nil, nil, newError("finished", "terminal "+si.Name+" has finished")
	}
	if si.State == "starting" {
		return nil, nil, newError("unavailable", "terminal "+si.Name+" is starting")
	}
	cols, rows := p.Cols, p.Rows
	if cols == 0 || rows == 0 {
		cols, rows = si.Cols, si.Rows
	}
	a := &attachment{c: c, name: si.Name, observe: p.Observe, cols: cols, rows: rows, max: c.s.MaxPending}
	a.cond = sync.NewCond(&a.mu)
	dc, err := a.dial(p.Resumed, max(p.History, 0))
	if err != nil {
		return nil, nil, fromDaemon(err)
	}
	a.dc = dc
	a.st = c.newStream()
	a.st.onInput = a.input
	go func() {
		select {
		case <-c.ctx.Done():
		case <-a.st.done:
		}
		a.shutdown()
	}()
	result := map[string]any{
		"channel":              a.st.ch,
		"terminal":             c.s.record(si),
		"resize_notifications": false,
		"automatic_input":      true,
	}
	// The pumps start once the reply is on the wire: the reply precedes
	// every stream frame.
	return result, a.start, nil
}

// attachment bridges one hc attach channel and a daemon attach stream. The
// daemon side is always drained (the daemon drops attach clients that stop
// reading); bytes wait in pending until the viewer has credit. A viewer more
// than max bytes behind gets a fresh repaint: the daemon stream is reopened
// and its redraw replaces the backlog (herdr-core's rule).
type attachment struct {
	c       *conn
	st      *stream
	name    string
	observe bool
	max     int

	mu       sync.Mutex
	cond     *sync.Cond
	dc       net.Conn // current daemon attach connection
	cols     uint16
	rows     uint16
	pending  []byte
	first    bool // the next daemon DATA frame is a redraw (first paint)
	end      any  // CLOSE reason once the daemon stream ended
	ended    bool
	shut     bool
	repaints int
}

// dial opens a daemon attach. passive: take the size only once active (hc
// resumed, and every repaint, so a lagging viewer never steals the size).
func (a *attachment) dial(passive bool, history int) (net.Conn, error) {
	cl, err := a.c.s.Dial()
	if err != nil {
		return nil, err
	}
	a.mu.Lock()
	cols, rows := a.cols, a.rows
	a.first = true
	a.mu.Unlock()
	dc, err := cl.AttachWith(proto.AttachArgs{Name: a.name, Cols: cols, Rows: rows,
		Observe: a.observe, Passive: passive, History: history})
	if err != nil {
		_ = cl.Close()
		return nil, err
	}
	return dc, nil
}

func (a *attachment) start() {
	go a.reader()
	go a.sender()
}

// reader drains the daemon stream into pending; after a repaint it goes on
// with the new daemon stream (a.dc).
func (a *attachment) reader() {
	for {
		a.mu.Lock()
		dc, shut := a.dc, a.shut
		a.mu.Unlock()
		if shut {
			return
		}
		f, err := proto.ReadFrame(dc)
		if err != nil {
			a.finish(dc, map[string]any{"reason": "detached", "exit_code": nil, "signal": nil}, err)
			return
		}
		switch f.Type {
		case proto.FrameData:
			if !a.queue(dc, f.Payload) {
				return
			}
		case proto.FrameExit:
			code, _ := proto.ParseExit(f.Payload)
			a.finish(dc, map[string]any{"reason": "exited", "exit_code": code, "signal": nil}, nil)
			return
		case proto.FrameDetach:
			a.finish(dc, map[string]any{"reason": "detached", "exit_code": nil, "signal": nil}, nil)
			return
		}
	}
}

// queue adds daemon output; past max it reopens the daemon stream for a
// repaint. It returns false when the attachment is shut.
func (a *attachment) queue(dc net.Conn, p []byte) bool {
	a.mu.Lock()
	if a.shut || dc != a.dc {
		a.mu.Unlock()
		return !a.shut
	}
	if a.first {
		a.first = false
		a.pending = append(a.pending, can)
		a.pending = append(a.pending, p...)
		a.pending = append(a.pending, win32Off...)
		a.cond.Broadcast()
		a.mu.Unlock()
		return true
	}
	if len(a.pending)+len(p) <= a.max {
		a.pending = append(a.pending, p...)
		a.cond.Broadcast()
		a.mu.Unlock()
		return true
	}
	// Too far behind: drop the backlog, reattach, let the redraw repaint.
	a.pending = a.pending[:0]
	a.repaints++
	a.mu.Unlock()
	_ = dc.Close()
	ndc, err := a.dial(true, 0)
	a.mu.Lock()
	defer a.mu.Unlock()
	if err != nil {
		a.end, a.ended = map[string]any{"reason": "detached", "exit_code": nil, "signal": nil}, true
		a.cond.Broadcast()
		return false
	}
	if a.shut {
		_ = ndc.Close()
		return false
	}
	a.dc = ndc
	return true
}

// finish records the end of the daemon stream dc (ignored for a stream
// already replaced by a repaint).
func (a *attachment) finish(dc net.Conn, reason any, err error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if dc != a.dc && !a.shut {
		return // replaced: the new stream's reader goes on
	}
	if err != nil && !errors.Is(err, io.EOF) && !errors.Is(err, net.ErrClosed) {
		reason = map[string]any{"reason": "server_stop", "exit_code": nil, "signal": nil}
	}
	a.end, a.ended = reason, true
	a.cond.Broadcast()
}

// sender moves pending bytes to the viewer within its credit, then closes
// the channel with the end reason.
func (a *attachment) sender() {
	for {
		a.mu.Lock()
		for len(a.pending) == 0 && !a.ended && !a.shut {
			a.cond.Wait()
		}
		if a.shut {
			a.mu.Unlock()
			return
		}
		if len(a.pending) == 0 && a.ended {
			reason := a.end
			a.mu.Unlock()
			a.st.close(reason)
			return
		}
		data := a.pending
		a.pending = nil
		a.mu.Unlock()
		if !a.st.send(data) {
			return
		}
	}
}

func (a *attachment) shutdown() {
	a.mu.Lock()
	if a.shut {
		a.mu.Unlock()
		return
	}
	a.shut = true
	dc := a.dc
	a.cond.Broadcast()
	a.mu.Unlock()
	if dc != nil {
		_ = dc.Close()
	}
}

func (a *attachment) conn() net.Conn {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.dc
}

// input handles one client frame on the channel (CREDIT is handled by the
// stream). It runs on the connection's reader goroutine.
func (a *attachment) input(f Frame) {
	switch f.Kind {
	case KindData, KindAutomaticInput:
		if a.observe {
			return
		}
		typ := proto.FrameData
		if f.Kind == KindAutomaticInput {
			typ = proto.FrameAuto
		}
		dc := a.conn()
		for p := f.Payload; len(p) > 0; {
			n := min(len(p), proto.MaxFrameSize)
			if err := proto.WriteFrame(dc, typ, p[:n]); err != nil {
				return
			}
			p = p[n:]
		}
	case KindResize:
		if a.observe {
			return
		}
		if cols, rows, ok := parseResize(f.Payload); ok {
			a.mu.Lock()
			a.cols, a.rows = cols, rows
			dc := a.dc
			a.mu.Unlock()
			_ = proto.WriteFrame(dc, proto.FrameResize, proto.ResizePayload(cols, rows))
		}
	case KindClose:
		// Answer with an empty CLOSE before the daemon stream can end and
		// report a reason.
		a.st.close(nil)
		_ = proto.WriteFrame(a.conn(), proto.FrameDetach, nil)
		a.shutdown()
	case KindFocus:
		if a.observe || len(f.Payload) == 0 {
			return
		}
		_ = proto.WriteFrame(a.conn(), proto.FrameFocus, f.Payload[:1])
	case KindFacts:
		// Ignored: viewers answer display queries themselves (see attach).
	}
}
