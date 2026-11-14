package hcompat

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"sync"
	"time"

	"github.com/digiogithub/agentd/internal/client"
	"github.com/digiogithub/agentd/internal/proto"
)

// Server translates hc connections into daemon ops. It keeps no state of its
// own: labels, documents and turn counters live in the daemon, so every
// connection (and every `hc connect` process) sees the same terminals.
type Server struct {
	// Dial opens a new daemon connection (each attach and subscription uses
	// its own, since the daemon upgrades the connection).
	Dial func() (*client.Client, error)
	// Core is what hello and server.status report as the core identity.
	Core CoreInfo
	// Version is the hc version reported (default CompatVersion).
	Version string
	// EnvBase is the environment terminal.run passes as the child's base.
	EnvBase []string
	// MaxPending is how far (bytes) an attach may fall behind before it gets
	// a fresh repaint instead of the backlog (default MaxPending).
	MaxPending int
	// ReadOnly ([herdr] read_only): clients may list, read and watch;
	// every method that types, starts, stops, labels or writes is refused
	// and attaches are observers.
	ReadOnly bool
	// AllowRun ([herdr] allow_run) decides whether terminal.run may start a
	// program (argv[0]); nil = any.
	AllowRun func(program string) bool
	// Log receives diagnostics (default: discard). Never terminal contents.
	Log *slog.Logger

	started time.Time
	once    sync.Once
}

func (s *Server) init() {
	s.once.Do(func() {
		s.started = time.Now()
		if s.MaxPending <= 0 {
			s.MaxPending = MaxPending
		}
		if s.Version == "" {
			s.Version = CompatVersion
		}
		if s.Log == nil {
			s.Log = slog.New(slog.NewTextHandler(io.Discard, nil))
		}
	})
}

// request is a JSON-RPC 2.0 request or notification.
type request struct {
	ID     json.RawMessage `json:"id,omitempty"`
	Method string          `json:"method"`
	Params json.RawMessage `json:"params,omitempty"`
}

type response struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

// conn is one client connection.
type conn struct {
	s      *Server
	ctx    context.Context
	cancel context.CancelFunc

	wmu    sync.Mutex
	w      io.Writer
	framed bool

	smu     sync.Mutex
	nextCh  uint32
	streams map[uint32]*stream
}

// Serve speaks hc on r/w until r ends or ctx is cancelled. It starts in plain
// mode (one JSON-RPC message per line) and switches to framed mode after a
// hello with framed:true.
func (s *Server) Serve(ctx context.Context, r io.Reader, w io.Writer) error {
	s.init()
	ctx, cancel := context.WithCancel(ctx)
	c := &conn{s: s, ctx: ctx, cancel: cancel, w: w, nextCh: 1, streams: map[uint32]*stream{}}
	defer c.closeAll()
	br := bufio.NewReaderSize(r, 64*1024)
	for {
		line, err := br.ReadBytes('\n')
		if len(bytes.TrimSpace(line)) > 0 {
			var req request
			if jerr := json.Unmarshal(line, &req); jerr != nil {
				c.writeLine(response{JSONRPC: "2.0", ID: json.RawMessage("null"),
					Error: &rpcError{Code: -32700, Message: "parse error", Data: errorData{Kind: "invalid_params"}}})
			} else {
				res, after, rerr := c.call(req)
				if req.ID != nil {
					c.writeLine(reply(req.ID, res, rerr))
				}
				if after != nil {
					after()
				}
				if req.Method == "hello" && rerr == nil && c.framed {
					return c.serveFramed(br)
				}
			}
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				c.waitPlain()
				return nil
			}
			return err
		}
	}
}

func reply(id json.RawMessage, res any, err *rpcError) response {
	if err != nil {
		return response{JSONRPC: "2.0", ID: id, Error: err}
	}
	if res == nil {
		res = struct{}{}
	}
	return response{JSONRPC: "2.0", ID: id, Result: res}
}

// waitPlain keeps a plain-mode connection whose input ended alive while a
// subscription still streams notifications on it (like `hc events`).
func (c *conn) waitPlain() {
	c.smu.Lock()
	var done []<-chan struct{}
	for _, st := range c.streams {
		done = append(done, st.done)
	}
	c.smu.Unlock()
	for _, d := range done {
		select {
		case <-d:
		case <-c.ctx.Done():
			return
		}
	}
}

func (c *conn) serveFramed(r io.Reader) error {
	for {
		f, err := ReadFrame(r)
		if err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return err
		}
		if f.Channel == 0 {
			if f.Kind != KindData {
				continue
			}
			var req request
			if err := json.Unmarshal(f.Payload, &req); err != nil {
				continue
			}
			if req.Method == "$/cancelRequest" {
				continue
			}
			// Calls run side by side so a slow one never holds up typing.
			go func() {
				res, after, rerr := c.call(req)
				if req.ID != nil {
					c.writeControl(reply(req.ID, res, rerr))
				}
				if after != nil {
					after()
				}
			}()
			continue
		}
		c.smu.Lock()
		st := c.streams[f.Channel]
		c.smu.Unlock()
		if st != nil {
			st.input(f)
		}
	}
}

func (c *conn) writeLine(v any) {
	b, err := json.Marshal(v)
	if err != nil {
		return
	}
	c.wmu.Lock()
	defer c.wmu.Unlock()
	_, _ = c.w.Write(append(b, '\n'))
}

func (c *conn) writeControl(v any) {
	b, err := json.Marshal(v)
	if err != nil {
		return
	}
	c.writeFrame(0, KindData, b)
}

func (c *conn) writeFrame(ch uint32, kind byte, payload []byte) {
	buf := AppendFrame(make([]byte, 0, HeaderSize+len(payload)), ch, kind, payload)
	c.wmu.Lock()
	defer c.wmu.Unlock()
	if _, err := c.w.Write(buf); err != nil {
		c.cancel()
	}
}

// call dispatches one request. after, when non-nil, runs once the reply is
// on the wire (streams must not send frames before their attach reply).
func (c *conn) call(req request) (any, func(), *rpcError) {
	h, ok := methods[req.Method]
	if !ok {
		return nil, nil, &rpcError{Code: -32601, Message: "no method " + req.Method,
			Data: errorData{Kind: "not_supported", Hint: "agentd implements part of the herdr-core API"}}
	}
	if c.s.ReadOnly && writeMethods[req.Method] {
		e := newError("forbidden", req.Method+" is disabled: agentd serves hc read-only")
		e.Data.Hint = "set [herdr] read_only = false in agentd's config.toml"
		return nil, nil, e
	}
	params := req.Params
	if len(params) == 0 || string(params) == "null" {
		params = json.RawMessage("{}")
	}
	return h(c, params)
}

func decode(params json.RawMessage, v any) *rpcError {
	if err := json.Unmarshal(params, v); err != nil {
		return invalidParams(err.Error())
	}
	return nil
}

// ---- streams ----

// stream is one non-zero channel: an attach or an event subscription. Output
// respects the client's credit (Window bytes to start with).
type stream struct {
	c    *conn
	ch   uint32
	mu   sync.Mutex
	cond *sync.Cond
	cred int
	shut bool
	done chan struct{}
	// onInput handles client frames other than CREDIT (nil: ignored).
	onInput func(Frame)
}

func (c *conn) newStream() *stream {
	c.smu.Lock()
	defer c.smu.Unlock()
	st := &stream{c: c, ch: c.nextCh, cred: Window, done: make(chan struct{})}
	st.cond = sync.NewCond(&st.mu)
	c.nextCh++
	c.streams[st.ch] = st
	return st
}

func (st *stream) input(f Frame) {
	if f.Kind == KindCredit {
		if len(f.Payload) == 4 {
			n := int(uint32(f.Payload[0]) | uint32(f.Payload[1])<<8 | uint32(f.Payload[2])<<16 | uint32(f.Payload[3])<<24)
			st.mu.Lock()
			st.cred += n
			st.cond.Broadcast()
			st.mu.Unlock()
		}
		return
	}
	if st.onInput != nil {
		st.onInput(f)
	}
}

// send writes stream bytes as DATA frames within the client's credit,
// blocking while it has none. It returns false once the stream is shut.
func (st *stream) send(p []byte) bool {
	for len(p) > 0 {
		st.mu.Lock()
		for st.cred <= 0 && !st.shut {
			st.cond.Wait()
		}
		if st.shut {
			st.mu.Unlock()
			return false
		}
		n := min(len(p), st.cred, chunk)
		st.cred -= n
		st.mu.Unlock()
		st.c.writeFrame(st.ch, KindData, p[:n])
		p = p[n:]
	}
	return true
}

// close ends the stream once: it sends CLOSE with reason (nil = empty) and
// forgets the channel.
func (st *stream) close(reason any) {
	st.mu.Lock()
	if st.shut {
		st.mu.Unlock()
		return
	}
	st.shut = true
	st.cond.Broadcast()
	st.mu.Unlock()
	var payload []byte
	if reason != nil {
		payload, _ = json.Marshal(reason)
	}
	if st.c.framed {
		st.c.writeFrame(st.ch, KindClose, payload)
	}
	st.c.smu.Lock()
	delete(st.c.streams, st.ch)
	st.c.smu.Unlock()
	close(st.done)
}

func (st *stream) isShut() bool {
	st.mu.Lock()
	defer st.mu.Unlock()
	return st.shut
}

func (c *conn) closeAll() {
	c.cancel()
	c.smu.Lock()
	all := make([]*stream, 0, len(c.streams))
	for _, st := range c.streams {
		all = append(all, st)
	}
	c.smu.Unlock()
	for _, st := range all {
		st.mu.Lock()
		st.shut = true
		st.cond.Broadcast()
		st.mu.Unlock()
	}
}

// ---- records ----

// Reserved labels: kept in the daemon, never shown to hc clients.
const (
	// labelKeep stores hc's keep flag (agentd has no in-place recovery).
	labelKeep = "agentd.hc.keep"
	// labelExplicit marks a session whose labels a client set: the default
	// herdr.space/herdr.tab are then not added (a client may delete them).
	labelExplicit = "agentd.hc.labels"
)

func reservedLabel(k string) bool { return k == labelKeep || k == labelExplicit }

// visibleLabels are a session's labels as hc clients see them.
func visibleLabels(si proto.SessionInfo) map[string]string {
	labels := map[string]string{}
	if si.Labels[labelExplicit] == "" {
		labels[LabelSpace] = projectLabel(si)
		labels[LabelTab] = si.Name
		if si.Profile != "" {
			labels[LabelHarness] = si.Profile
		}
	}
	for k, v := range si.Labels {
		if !reservedLabel(k) {
			labels[k] = v
		}
	}
	return labels
}

// record builds the hc record of a session.
func (s *Server) record(si proto.SessionInfo) Terminal {
	name := si.Name
	t := Terminal{
		Address: si.Name, Name: &name, Labels: visibleLabels(si), Argv: si.Cmd, Cwd: si.Cwd,
		State: "live", CreatedMS: si.StartedAt.UnixMilli(), Screen: "exact",
		Cols: si.Cols, Rows: si.Rows, Title: si.Title, Records: map[string]Status{},
		Turn: si.Turn, Keep: si.Labels[labelKeep] != "", Foreground: json.RawMessage("null"),
	}
	if t.Argv == nil {
		t.Argv = []string{}
	}
	if si.State == "exited" {
		t.State = "finished"
		reason := "exited"
		t.Reason = &reason
		t.ExitCode = si.ExitCode
		fin := si.LastOutput.UnixMilli()
		if si.LastOutput.IsZero() {
			fin = si.StartedAt.UnixMilli()
		}
		t.FinishedMS = &fin
	} else {
		if si.Pid > 0 {
			pid := si.Pid
			t.Pid = &pid
		}
		t.Status, t.Records, t.Reported = statusOf(si)
	}
	return t
}

// sessions lists the daemon's sessions.
func (s *Server) sessions() ([]proto.SessionInfo, error) {
	cl, err := s.Dial()
	if err != nil {
		return nil, err
	}
	defer cl.Close()
	return cl.Ls()
}

// lookup finds one session by hc address (= agentd session name).
func (s *Server) lookup(addr string) (proto.SessionInfo, *rpcError) {
	if addr == "" {
		return proto.SessionInfo{}, invalidParams("terminal is required")
	}
	all, err := s.sessions()
	if err != nil {
		return proto.SessionInfo{}, fromDaemon(err)
	}
	for _, si := range all {
		if si.Name == addr {
			return si, nil
		}
	}
	return proto.SessionInfo{}, newError("not_found", "no terminal "+addr)
}
