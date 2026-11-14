package hcompat

import (
	"encoding/json"
	"errors"
	"io"
	"net"

	"github.com/digiogithub/agentd/internal/proto"
)

// win32Off is part of herdr-core's first paint: whether the program asked for
// win32-input-mode (never, on a Unix PTY).
const win32Off = "\x1b[?9001l"

// can (CAN) starts a first paint: it cancels any half-parsed sequence the
// viewer still holds.
const can = 0x18

func terminalAttach(c *conn, params json.RawMessage) (any, func(), *rpcError) {
	var p struct {
		Terminal string `json:"terminal"`
		Cols     uint16 `json:"cols"`
		Rows     uint16 `json:"rows"`
		Observe  bool   `json:"observe"`
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
	cl, err := c.s.Dial()
	if err != nil {
		return nil, nil, fromDaemon(err)
	}
	dc, err := cl.Attach(si.Name, cols, rows)
	if err != nil {
		_ = cl.Close()
		return nil, nil, fromDaemon(err)
	}
	st := c.newStream()
	a := &attachment{st: st, dc: dc, observe: p.Observe}
	st.onInput = a.input
	go func() {
		<-c.ctx.Done()
		_ = dc.Close()
	}()
	result := map[string]any{
		"channel":              st.ch,
		"terminal":             c.s.record(si),
		"resize_notifications": false,
		"automatic_input":      false,
	}
	// The pump starts once the reply is on the wire: the reply precedes
	// every stream frame.
	return result, a.pump, nil
}

// attachment bridges one hc attach channel and one daemon attach stream.
type attachment struct {
	st      *stream
	dc      net.Conn
	observe bool
}

// pump forwards daemon output to the client: the first frame is the
// daemon's full redraw, sent as herdr-core's first paint.
func (a *attachment) pump() {
	defer a.dc.Close()
	first := true
	for {
		f, err := proto.ReadFrame(a.dc)
		if err != nil {
			if errors.Is(err, io.EOF) || errors.Is(err, net.ErrClosed) {
				a.st.close(map[string]any{"reason": "detached", "exit_code": nil, "signal": nil})
			} else {
				a.st.close(map[string]any{"reason": "server_stop", "exit_code": nil, "signal": nil})
			}
			return
		}
		switch f.Type {
		case proto.FrameData:
			data := f.Payload
			if first {
				first = false
				data = append(append([]byte{can}, data...), win32Off...)
			}
			if !a.st.send(data) {
				return
			}
		case proto.FrameExit:
			code, _ := proto.ParseExit(f.Payload)
			a.st.close(map[string]any{"reason": "exited", "exit_code": code, "signal": nil})
			return
		case proto.FrameDetach:
			a.st.close(map[string]any{"reason": "detached", "exit_code": nil, "signal": nil})
			return
		}
	}
}

// input handles one client frame on the channel (CREDIT is handled by the
// stream). It runs on the connection's reader goroutine.
func (a *attachment) input(f Frame) {
	switch f.Kind {
	case KindData, KindAutomaticInput:
		if a.observe {
			return
		}
		for p := f.Payload; len(p) > 0; {
			n := min(len(p), proto.MaxFrameSize)
			if err := proto.WriteFrame(a.dc, proto.FrameData, p[:n]); err != nil {
				return
			}
			p = p[n:]
		}
	case KindResize:
		if a.observe {
			return
		}
		if cols, rows, ok := parseResize(f.Payload); ok {
			_ = proto.WriteFrame(a.dc, proto.FrameResize, proto.ResizePayload(cols, rows))
		}
	case KindClose:
		// Answer with an empty CLOSE before the pump can see the daemon
		// connection end and report a reason.
		a.st.close(nil)
		_ = proto.WriteFrame(a.dc, proto.FrameDetach, nil)
		_ = a.dc.Close()
	case KindFocus, KindFacts:
		// Not forwarded yet (Phase 2: focus reports for mode 1004, display facts).
	}
}
