// Package proto defines agentd's wire protocol: newline-delimited JSON
// requests/responses over the daemon's unix socket, and the binary frame
// stream an attach connection switches to (see AGENTS.md, "Wire protocol").
//
// It depends only on the standard library so that any client (CLI, MCP
// server, tests) can speak the protocol.
package proto

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sync"
	"time"
)

// Op names (AGENTS.md, "Ops").
const (
	OpPing     = "ping"
	OpRun      = "run"
	OpLs       = "ls"
	OpKill     = "kill"
	OpRm       = "rm"
	OpDump     = "dump"
	OpType     = "type"
	OpSend     = "send"
	OpInbox    = "inbox"
	OpRead     = "read"
	OpReply    = "reply"
	OpPeers    = "peers"
	OpAttach   = "attach"
	OpShutdown = "shutdown"
	OpWhoami   = "whoami"
	OpPurge    = "purge"
)

// MaxLineSize bounds one JSON request or response line.
const MaxLineSize = 8 << 20

// ErrLineTooLong is returned when a JSON line exceeds MaxLineSize.
var ErrLineTooLong = errors.New("proto: line too long")

// Request is one client request: {"op": "...", "args": {...}, "auth": {...}}.
type Request struct {
	Op   string          `json:"op"`
	Args json.RawMessage `json:"args,omitempty"`
	Auth *Auth           `json:"auth,omitempty"`
}

// Auth carries the caller's claimed session identity (AGENTS.md, "Identity").
// ID ($AGENTD_ID) is only a hint: the daemon resolves the identity itself and
// rejects a mismatching hint. Token ($AGENTD_TOKEN) is the per-session secret
// the daemon put in the session's environment; a valid token identifies the
// session when the peer's process ancestry does not.
type Auth struct {
	ID    string `json:"id,omitempty"`
	Token string `json:"token,omitempty"`
}

// Response is the daemon's answer: {"ok": true, "data": ...} or
// {"ok": false, "error": "..."}.
type Response struct {
	OK    bool            `json:"ok"`
	Data  json.RawMessage `json:"data,omitempty"`
	Error string          `json:"error,omitempty"`
}

// NewRequest builds a request, marshalling args (nil means no args).
func NewRequest(op string, args any) (Request, error) {
	req := Request{Op: op}
	if args != nil {
		b, err := json.Marshal(args)
		if err != nil {
			return req, fmt.Errorf("proto: marshal %s args: %w", op, err)
		}
		req.Args = b
	}
	return req, nil
}

// DecodeArgs unmarshals the request args into v. Empty args leave v as is.
// Unknown fields are rejected so typos in clients surface early.
func (r Request) DecodeArgs(v any) error {
	if len(r.Args) == 0 || string(r.Args) == "null" {
		return nil
	}
	dec := json.NewDecoder(bytes.NewReader(r.Args))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return fmt.Errorf("invalid %s args: %w", r.Op, err)
	}
	return nil
}

// OKResponse builds a successful response carrying data (nil means none).
func OKResponse(data any) (Response, error) {
	resp := Response{OK: true}
	if data != nil {
		b, err := json.Marshal(data)
		if err != nil {
			return Response{}, fmt.Errorf("proto: marshal data: %w", err)
		}
		resp.Data = b
	}
	return resp, nil
}

// ErrorResponse builds a failed response.
func ErrorResponse(err error) Response {
	msg := "unknown error"
	if err != nil {
		msg = err.Error()
	}
	return Response{OK: false, Error: msg}
}

// RemoteError is a daemon-side error returned to a client.
type RemoteError struct{ Msg string }

func (e *RemoteError) Error() string { return e.Msg }

// Decode turns a response into an error (if !OK) or unmarshals Data into v
// (if v is non-nil).
func (r Response) Decode(v any) error {
	if !r.OK {
		return &RemoteError{Msg: r.Error}
	}
	if v == nil || len(r.Data) == 0 {
		return nil
	}
	if err := json.Unmarshal(r.Data, v); err != nil {
		return fmt.Errorf("proto: decode response: %w", err)
	}
	return nil
}

// Codec reads and writes newline-delimited JSON values on a stream. Reads go
// through a buffered reader that is exposed by Reader, so that after an attach
// upgrade the frame decoder continues on the same buffer and no bytes are lost.
// Writes are serialized; reads must come from a single goroutine.
type Codec struct {
	r  *bufio.Reader
	mu sync.Mutex
	w  io.Writer
}

// NewCodec wraps rw.
func NewCodec(rw io.ReadWriter) *Codec {
	return &Codec{r: bufio.NewReaderSize(rw, 64<<10), w: rw}
}

// Reader returns the codec's buffered reader.
func (c *Codec) Reader() *bufio.Reader { return c.r }

// WriteJSON writes v as one JSON line.
func (c *Codec) WriteJSON(v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	if len(b)+1 > MaxLineSize {
		return ErrLineTooLong
	}
	b = append(b, '\n')
	c.mu.Lock()
	defer c.mu.Unlock()
	_, err = c.w.Write(b)
	return err
}

// ReadJSON reads one JSON line into v.
func (c *Codec) ReadJSON(v any) error {
	line, err := c.readLine()
	if err != nil {
		return err
	}
	if err := json.Unmarshal(line, v); err != nil {
		return fmt.Errorf("proto: invalid json: %w", err)
	}
	return nil
}

func (c *Codec) readLine() ([]byte, error) {
	var buf []byte
	for {
		chunk, err := c.r.ReadSlice('\n')
		if len(buf)+len(chunk) > MaxLineSize {
			return nil, ErrLineTooLong
		}
		buf = append(buf, chunk...)
		switch {
		case err == nil:
			return bytes.TrimRight(buf, "\r\n"), nil
		case errors.Is(err, bufio.ErrBufferFull):
			continue
		case errors.Is(err, io.EOF) && len(buf) > 0:
			return nil, io.ErrUnexpectedEOF
		default:
			return nil, err
		}
	}
}

// WriteRequest writes a request line.
func (c *Codec) WriteRequest(r Request) error { return c.WriteJSON(r) }

// ReadRequest reads a request line.
func (c *Codec) ReadRequest() (Request, error) {
	var r Request
	err := c.ReadJSON(&r)
	return r, err
}

// WriteResponse writes a response line.
func (c *Codec) WriteResponse(r Response) error { return c.WriteJSON(r) }

// ReadResponse reads a response line.
func (c *Codec) ReadResponse() (Response, error) {
	var r Response
	err := c.ReadJSON(&r)
	return r, err
}

// ---- Typed args and results ----

// PingResult answers ping.
type PingResult struct {
	Version string `json:"version"`
	Pid     int    `json:"pid"`
}

// RunArgs starts a session. Cwd should be absolute (the CLI resolves it);
// Cmd is the argv. Cols/Rows of 0 mean the defaults. EnvBase is the caller's
// environment ("KEY=VALUE", normally os.Environ() of the CLI): the daemon uses
// it as the child's base environment instead of its own (AGENTS.md, "Child
// env"); Env (--env) is applied on top.
type RunArgs struct {
	Name    string            `json:"name"`
	Cwd     string            `json:"cwd,omitempty"`
	Cmd     []string          `json:"cmd"`
	EnvBase []string          `json:"env_base,omitempty"`
	Env     map[string]string `json:"env,omitempty"`
	Profile string            `json:"profile,omitempty"`
	Cols    uint16            `json:"cols,omitempty"`
	Rows    uint16            `json:"rows,omitempty"`
}

// SessionInfo describes a session (result of run, element of ls).
type SessionInfo struct {
	Name       string    `json:"name"`
	State      string    `json:"state"`
	Pid        int       `json:"pid"`
	Cwd        string    `json:"cwd"`
	Cmd        []string  `json:"cmd"`
	Profile    string    `json:"profile"`
	ExitCode   *int      `json:"exit_code,omitempty"`
	StartedAt  time.Time `json:"started_at"`
	LastOutput time.Time `json:"last_output,omitzero"`
	IdleMS     int64     `json:"idle_ms"`
	Unread     int       `json:"unread"`
	Attached   int       `json:"attached"`
	Cols       uint16    `json:"cols"`
	Rows       uint16    `json:"rows"`
	Title      string    `json:"title,omitempty"`
	// InputState is the agent's input line: "empty", "draft" (a human typed
	// text not yet submitted) or "suggestion" (the harness shows ghost text:
	// a placeholder or suggested prompt, not input). Empty for exited sessions.
	InputState string `json:"input_state,omitempty"`
}

// LsResult answers ls.
type LsResult struct {
	Sessions []SessionInfo `json:"sessions"`
}

// NameArgs addresses one session (rm).
type NameArgs struct {
	Name string `json:"name"`
}

// KillArgs signals a session. Signal is a name ("TERM", "SIGKILL") or a
// number; empty means TERM.
type KillArgs struct {
	Name   string `json:"name"`
	Signal string `json:"signal,omitempty"`
}

// DumpArgs requests a session's screen. By default the harness's ghost text
// (placeholder / suggested prompt, per the session's profile) is blanked;
// Ghost returns the raw screen. ANSI output is always raw.
type DumpArgs struct {
	Name       string `json:"name"`
	Scrollback bool   `json:"scrollback,omitempty"`
	ANSI       bool   `json:"ansi,omitempty"`
	Ghost      bool   `json:"ghost,omitempty"`
	// Cells adds a per-cell description of one row (diagnostics): Row, or
	// the cursor's row when Row is nil.
	Cells bool `json:"cells,omitempty"`
	Row   *int `json:"row,omitempty"`
}

// DumpResult answers dump. Ghost is the ghost text found on the input line
// ("" if none); GhostHidden reports whether it was blanked from Text.
type DumpResult struct {
	Text        string     `json:"text"`
	Ghost       string     `json:"ghost,omitempty"`
	GhostHidden bool       `json:"ghost_hidden,omitempty"`
	Cells       *CellsDump `json:"cells,omitempty"`
}

// CellsDump describes the cells of one screen row (DumpArgs.Cells).
type CellsDump struct {
	CursorX int        `json:"cursor_x"`
	CursorY int        `json:"cursor_y"`
	Row     int        `json:"row"`
	Cells   []CellInfo `json:"cells"`
}

// CellInfo is one non-blank cell: colours are "" (default), "N" (palette
// index) or "#rrggbb"; Rune is "U+XXXX" of the first rune.
type CellInfo struct {
	X       int      `json:"x"`
	Content string   `json:"content"`
	Rune    string   `json:"rune"`
	Fg      string   `json:"fg,omitempty"`
	Bg      string   `json:"bg,omitempty"`
	Attrs   []string `json:"attrs,omitempty"`
	Width   int      `json:"width"`
}

// TypeArgs types raw text into a session; Submit then sends the profile's
// submit sequence after its submit delay.
type TypeArgs struct {
	Name   string `json:"name"`
	Text   string `json:"text"`
	Submit bool   `json:"submit,omitempty"`
}

// SendArgs sends a mailbox message. The sender is the caller's identity as
// resolved by the daemon.
type SendArgs struct {
	To      string `json:"to"`
	Subject string `json:"subject,omitempty"`
	Body    string `json:"body"`
	ReplyTo *int64 `json:"reply_to,omitempty"`
}

// InboxArgs lists the caller's mailbox; All includes read messages. As
// (humans only) lists another mailbox without changing message states.
type InboxArgs struct {
	As  string `json:"as,omitempty"`
	All bool   `json:"all,omitempty"`
}

// InboxResult answers inbox.
type InboxResult struct {
	Messages []Message `json:"messages"`
}

// ReadArgs reads (and marks read) one message addressed to the caller. As
// (humans only) reads a message addressed to another mailbox without marking it.
type ReadArgs struct {
	As string `json:"as,omitempty"`
	ID int64  `json:"id"`
}

// ReplyArgs replies to message ID as the caller (who must be its recipient).
type ReplyArgs struct {
	ID   int64  `json:"id"`
	Body string `json:"body"`
}

// PurgeArgs deletes mailbox messages. Filters combine (AND): Before (RFC
// 3339) or OlderThan (a duration such as "48h" or "5d", relative to the
// daemon's clock), Recipient and States. At least one filter or All is
// required. Sessions may only purge read/replied messages of their own
// mailbox (AGENTS.md, "Identity"). DryRun only counts; Vacuum (humans only)
// compacts the database file afterwards.
type PurgeArgs struct {
	Before    string   `json:"before,omitempty"`
	OlderThan string   `json:"older_than,omitempty"`
	Recipient string   `json:"recipient,omitempty"`
	States    []string `json:"states,omitempty"`
	All       bool     `json:"all,omitempty"`
	DryRun    bool     `json:"dry_run,omitempty"`
	Vacuum    bool     `json:"vacuum,omitempty"`
}

// PurgeResult answers purge: the number of messages deleted (or, with
// DryRun, that would be deleted).
type PurgeResult struct {
	Count  int  `json:"count"`
	DryRun bool `json:"dry_run,omitempty"`
}

// WhoamiResult answers whoami: the caller's identity as resolved by the
// daemon. Session is true when it is a session name (else "human:<user>").
type WhoamiResult struct {
	Identity string `json:"identity"`
	Session  bool   `json:"session"`
	// Via says how it was resolved: "process" (peer process ancestry),
	// "token" (AGENTD_TOKEN) or "uid" (not inside a session).
	Via string `json:"via"`
}

// Message is a mailbox message on the wire (same JSON as mailbox.Message).
type Message struct {
	ID          int64      `json:"id"`
	Sender      string     `json:"sender"`
	Recipient   string     `json:"recipient"`
	Subject     string     `json:"subject"`
	Body        string     `json:"body"`
	ReplyTo     *int64     `json:"reply_to,omitempty"`
	State       string     `json:"state"`
	Attempts    int        `json:"attempts"`
	CreatedAt   time.Time  `json:"created_at"`
	DeliveredAt *time.Time `json:"delivered_at,omitempty"`
	ReadAt      *time.Time `json:"read_at,omitempty"`
	RepliedAt   *time.Time `json:"replied_at,omitempty"`
	// Warning is set by the daemon when the operation succeeded with a caveat
	// (e.g. a reply to a sender that is not a known mailbox).
	Warning string `json:"warning,omitempty"`
}

// Peer is an addressable session (element of peers).
type Peer struct {
	Name    string `json:"name"`
	State   string `json:"state"`
	Profile string `json:"profile"`
	Cwd     string `json:"cwd"`
}

// PeersResult answers peers.
type PeersResult struct {
	Peers []Peer `json:"peers"`
}

// AttachArgs attaches to a session with the client's terminal size.
type AttachArgs struct {
	Name string `json:"name"`
	Cols uint16 `json:"cols,omitempty"`
	Rows uint16 `json:"rows,omitempty"`
}
