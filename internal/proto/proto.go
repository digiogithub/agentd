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
	"strings"
	"sync"
	"time"
)

// Op names (AGENTS.md, "Ops").
const (
	OpPing      = "ping"
	OpRun       = "run"
	OpLs        = "ls"
	OpKill      = "kill"
	OpRm        = "rm"
	OpDump      = "dump"
	OpType      = "type"
	OpSend      = "send"
	OpInbox     = "inbox"
	OpRead      = "read"
	OpReply     = "reply"
	OpPeers     = "peers"
	OpAttach    = "attach"
	OpShutdown  = "shutdown"
	OpWhoami    = "whoami"
	OpPurge     = "purge"
	OpInfo      = "info"
	OpView      = "view"
	OpViewAck   = "view_ack"
	OpSubscribe = "subscribe"
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
// {"ok": false, "error": "...", "code": "..."}. Code is an optional stable
// machine-readable error class (see the Code* constants): old clients ignore
// it and keep reading Error; an absent code means "unclassified". Data may
// also accompany an error (CodedError.Data).
type Response struct {
	OK    bool            `json:"ok"`
	Data  json.RawMessage `json:"data,omitempty"`
	Error string          `json:"error,omitempty"`
	Code  string          `json:"code,omitempty"`
}

// Error codes carried in Response.Code (docs/design/api/daemon-ops.md, 3.5).
// Clients must treat an unknown or empty code as "internal"/unclassified.
const (
	CodeInvalid      = "invalid"       // malformed or invalid arguments
	CodeNotFound     = "not_found"     // session or message does not exist
	CodeStarting     = "starting"      // session is reserved but not running yet (retryable)
	CodeExited       = "exited"        // session has already exited
	CodeExists       = "exists"        // name already taken
	CodeForbidden    = "forbidden"     // identity / ACL / permission refusal
	CodeConflict     = "conflict"      // state conflict (e.g. still running, stale revision)
	CodeBusy         = "busy"          // pending human draft or recent keystroke
	CodeNotReady     = "not_ready"     // status precondition not met
	CodeVerifyFailed = "verify_failed" // paste/selection not visible; nothing submitted
	CodeTimeout      = "timeout"       // PTY write timeout
	CodeInternal     = "internal"
)

// CodedError is an error with a stable Code. Msg is the human message
// (unchanged wording, it is what old clients print); Data is optional
// structured context sent in Response.Data.
//
// Err is the optional wrapped cause (errors.Is/As see through Unwrap).
type CodedError struct {
	Code string
	Msg  string
	Data any
	Err  error
}

func (e *CodedError) Error() string { return e.Msg }

// Unwrap returns the wrapped cause, if any.
func (e *CodedError) Unwrap() error { return e.Err }

// Errorf builds a *CodedError with a formatted message. Like fmt.Errorf it
// supports the %w verb: the wrapped operand becomes Err.
func Errorf(code, format string, a ...any) error {
	err := fmt.Errorf(format, a...)
	ce := &CodedError{Code: code, Msg: err.Error()}
	if strings.Contains(format, "%w") {
		ce.Err = err // fmt's wrapper: Is/As see every %w operand
	}
	return ce
}

// WithCode wraps err with code (nil stays nil). The message is err's text.
// An error that already carries a code keeps it.
func WithCode(code string, err error) error {
	if err == nil {
		return nil
	}
	var ce *CodedError
	if errors.As(err, &ce) {
		return err
	}
	return &CodedError{Code: code, Msg: err.Error(), Err: err}
}

// CodeOf returns the error code of err (a *CodedError or *RemoteError), or ""
// when it has none.
func CodeOf(err error) string {
	var ce *CodedError
	if errors.As(err, &ce) {
		return ce.Code
	}
	var re *RemoteError
	if errors.As(err, &re) {
		return re.Code
	}
	return ""
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
		return Errorf(CodeInvalid, "invalid %s args: %v", r.Op, err)
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
	resp := Response{OK: false, Error: msg}
	var ce *CodedError
	if errors.As(err, &ce) {
		resp.Code = ce.Code
		if ce.Data != nil {
			if b, merr := json.Marshal(ce.Data); merr == nil {
				resp.Data = b
			}
		}
	}
	return resp
}

// RemoteError is a daemon-side error returned to a client. Code is the
// daemon's error code ("" from old daemons or for unclassified errors) and
// Data any structured context that came with the error.
type RemoteError struct {
	Msg  string
	Code string
	Data json.RawMessage
}

func (e *RemoteError) Error() string { return e.Msg }

// Decode turns a response into an error (if !OK) or unmarshals Data into v
// (if v is non-nil).
func (r Response) Decode(v any) error {
	if !r.OK {
		return &RemoteError{Msg: r.Error, Code: r.Code, Data: r.Data}
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
	// Epoch identifies this daemon start (random-ish, unique per start);
	// sequence numbers and revisions reset with it. "" from old daemons.
	Epoch string `json:"epoch,omitempty"`
}

// InfoArgs are the optional args of info. EnvBase is the caller's environment
// ("KEY=VALUE", same meaning as RunArgs.EnvBase): the daemon loads project
// aliases from AGENTD_ALIASES, AGENTD_ALIASES_FILE, XDG_CONFIG_HOME and HOME
// found there, exactly as run does, so info shows what run would resolve.
type InfoArgs struct {
	EnvBase []string `json:"env_base,omitempty"`
}

// InfoResult answers info: what a client needs to render the host and launch
// agents on it. Profiles, aliases and harnesses are the daemon's view (its
// config and AGENTD_ALIASES), never the client's.
type InfoResult struct {
	Host    string `json:"host"`
	Version string `json:"version"`
	Pid     int    `json:"pid"`
	Epoch   string `json:"epoch"`
	// Harnesses are the harness names `run` knows by profile (every profile
	// except "default"); a harness is also a binary name.
	Harnesses []string      `json:"harnesses"`
	Profiles  []ProfileInfo `json:"profiles"`
	Aliases   []AliasInfo   `json:"aliases"`
	// AgentView mirrors [security] agent_view: sessions may use op view on
	// other sessions (the MCP server registers agentd_view only then).
	AgentView bool `json:"agent_view"`
}

// ProfileInfo describes a harness profile (element of InfoResult.Profiles).
type ProfileInfo struct {
	Name    string `json:"name"`
	Builtin bool   `json:"builtin"`
	// Cols and Rows are the profile's fixed PTY size (sessions started
	// without an explicit size).
	Cols int `json:"cols"`
	Rows int `json:"rows"`
}

// AliasInfo describes a project alias; Source is "file" or "env".
type AliasInfo struct {
	Name   string `json:"name"`
	Dir    string `json:"dir"`
	Source string `json:"source"`
}

// RunArgs starts a session. Cwd must be absolute; Cmd is the argv. Cols/Rows
// of 0 mean the profile's size (160x50 by default).
//
// Target + Harness make the daemon resolve the run with the CLI's rules
// (config.Aliases.ResolveRun): Target is a project alias or a directory
// (relative paths are resolved against Cwd, which must then be the caller's
// absolute working directory; the resolved directory becomes the session's
// cwd); Harness defaults to Cmd[0] or "claude"; empty Cmd means [Harness];
// empty Name means "<harness>@<label>"; empty Profile means the harness name.
// Without Target, Cwd/Cmd/Name are used as given (Harness only supplies the
// default Cmd and Profile). EnvBase is the caller's
// environment ("KEY=VALUE", normally os.Environ() of the CLI): the daemon uses
// it as the child's base environment instead of its own (AGENTS.md, "Child
// env"); Env (--env) is applied on top.
type RunArgs struct {
	Name    string            `json:"name"`
	Target  string            `json:"target,omitempty"`
	Harness string            `json:"harness,omitempty"`
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

	// Summary of the semantic view (op view): Status is the session's
	// semantic status (starting, working, idle, waiting, exited), Attention
	// whether it needs a human, Revision the view revision and Preview the
	// last non-blank transcript line (sanitized, at most 160 characters).
	// They come from the daemon's cached view and are empty while starting.
	Status    string `json:"status,omitempty"`
	Attention bool   `json:"attention,omitempty"`
	Revision  uint64 `json:"revision,omitempty"`
	Preview   string `json:"preview,omitempty"`
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

// ---- View (op view) ----

// ViewArgs are the arguments of op view.
type ViewArgs struct {
	Name string `json:"name"`
	// SinceRevision: when it equals the session's current revision (and Epoch,
	// if set, the daemon's epoch) the answer is ViewNotModified instead of
	// the view. 0 = always the full view.
	SinceRevision uint64 `json:"since_revision,omitempty"`
	Epoch         string `json:"epoch,omitempty"`
	// LinesLimit caps the number of lines: the newest N, or N from FromLine.
	// 0 = 500; at most 10000.
	LinesLimit int `json:"lines_limit,omitempty"`
	// FromLine pages the transcript: lines start at this absolute index
	// (View.FirstLine of an earlier answer); nil = the newest lines.
	FromLine *int `json:"from_line,omitempty"`
	// Scrollback returns the main-screen scrollback and screen as one
	// transcript (FirstLine 0, not pageable) instead of the stored history.
	Scrollback bool `json:"scrollback,omitempty"`
}

// ViewNotModified answers op view when since_revision is still current.
type ViewNotModified struct {
	NotModified bool   `json:"not_modified"`
	Epoch       string `json:"epoch"`
	Revision    uint64 `json:"revision"`
}

// ViewAckResult answers op view_ack.
type ViewAckResult struct {
	Revision uint64 `json:"revision"`
}

// ViewAttention is the "look at me" flag of a session.
type ViewAttention struct {
	Set    bool      `json:"set"`
	Reason string    `json:"reason,omitempty"` // turn_done | waiting | bell | notify
	Since  time.Time `json:"since,omitzero"`
}

// ViewLine is one logical transcript line.
type ViewLine struct {
	Text string `json:"text"`
	Role string `json:"role"` // transcript | status | dialog | gap
}

// ViewInput is the harness input line.
type ViewInput struct {
	Text  string `json:"text"`
	Ghost string `json:"ghost"`
	State string `json:"state"` // empty | suggestion | draft
}

// ViewOption is one selectable entry of a detected menu. Key sequences are
// never exposed: choosing is the daemon's job.
type ViewOption struct {
	ID         string  `json:"id"`
	Label      string  `json:"label"`
	Index      int     `json:"index"`
	Kind       string  `json:"kind"` // numbered | pointer | checkbox | yesno
	Selected   bool    `json:"selected"`
	Checked    *bool   `json:"checked,omitempty"`
	Confidence float64 `json:"confidence"`
}

// ViewHint is a footer hint ("esc" / "cancel").
type ViewHint struct {
	Key    string `json:"key"`
	Action string `json:"action"`
}

// ViewDialog is the dialog on screen, if any.
type ViewDialog struct {
	Question string `json:"question"`
}

// View is the semantic view of one session (wire mirror of the daemon's
// interpretation; design: docs/design/api/semantic-model.md).
type View struct {
	NotModified  bool          `json:"not_modified,omitempty"`
	Name         string        `json:"name"`
	Epoch        string        `json:"epoch"`
	Revision     uint64        `json:"revision"`
	Status       string        `json:"status"`
	StatusReason string        `json:"status_reason,omitempty"`
	Attention    ViewAttention `json:"attention"`
	Title        string        `json:"title,omitempty"`
	Cwd          string        `json:"cwd,omitempty"`
	// FirstLine is the absolute index of Lines[0] in the session's line stream.
	FirstLine int          `json:"first_line"`
	Lines     []ViewLine   `json:"lines"`
	Input     ViewInput    `json:"input"`
	Options   []ViewOption `json:"options"`
	Hints     []ViewHint   `json:"hints"`
	Dialog    *ViewDialog  `json:"dialog,omitempty"`
	IdleMS    int64        `json:"idle_ms"`
	ExitCode  *int         `json:"exit_code,omitempty"`
}

// ---- Subscribe (op subscribe, daemon event bus) ----

// Event kinds published on the daemon event bus (op subscribe).
const (
	EvHello            = "hello"
	EvSessionCreated   = "session.created"
	EvSessionStarted   = "session.started"
	EvSessionExited    = "session.exited"
	EvSessionRemoved   = "session.removed"
	EvStatusChanged    = "status.changed"
	EvViewChanged      = "view.changed"
	EvInputChanged     = "input.changed"
	EvAttentionChanged = "attention.changed"
	EvMailQueued       = "mail.queued"
	EvMailDelivered    = "mail.delivered"
	EvMailRead         = "mail.read"
	EvResync           = "resync"
	EvHeartbeat        = "heartbeat"
	EvBye              = "bye"
)

// Resync reasons (ResyncData.Reason).
const (
	ResyncOverflow     = "overflow"
	ResyncSinceTooOld  = "since_seq_too_old"
	ResyncEpochChanged = "epoch_mismatch"
)

// SubscribeArgs are the arguments of op subscribe.
type SubscribeArgs struct {
	// Sessions limits events to these session names (empty = all).
	Sessions []string `json:"sessions,omitempty"`
	// Kinds are exact kinds or prefixes ("session", "status", "view",
	// "input", "attention", "mail"); empty = all. hello, resync, heartbeat
	// and bye are always delivered.
	Kinds []string `json:"kinds,omitempty"`
	// SinceSeq asks for a replay of the events after this seq (with Epoch,
	// when known). 0 = live only.
	SinceSeq uint64 `json:"since_seq,omitempty"`
	Epoch    string `json:"epoch,omitempty"`
	// HeartbeatMS overrides the heartbeat period (default 15000, min 1000).
	HeartbeatMS int `json:"heartbeat_ms,omitempty"`
}

// SubscribeResult is the OK response line of subscribe, sent before the
// connection becomes an event stream (newline-delimited JSON Events).
type SubscribeResult struct {
	Epoch    string        `json:"epoch"`
	Seq      uint64        `json:"seq"` // last seq at subscribe time
	Sessions []SessionInfo `json:"sessions"`
}

// Event is one line of the subscribe stream. Seq is monotonic per Epoch for
// bus events; control events (hello, resync, heartbeat, bye) carry seq 0.
type Event struct {
	Seq     uint64          `json:"seq"`
	Epoch   string          `json:"epoch"`
	Time    time.Time       `json:"time"`
	Kind    string          `json:"kind"`
	Session string          `json:"session,omitempty"`
	Data    json.RawMessage `json:"data,omitempty"`
}

// HelloData is the data of the hello event.
type HelloData struct {
	Epoch string `json:"epoch"`
	Seq   uint64 `json:"seq"`
}

// ResyncData is the data of the resync event.
type ResyncData struct {
	Reason string `json:"reason"`
}

// ExitedData is the data of session.exited.
type ExitedData struct {
	ExitCode int `json:"exit_code"`
}

// StatusChangedData is the data of status.changed.
type StatusChangedData struct {
	From      string `json:"from,omitempty"`
	Status    string `json:"status"`
	Reason    string `json:"reason,omitempty"`
	Attention bool   `json:"attention"`
	Revision  uint64 `json:"revision"`
}

// ViewChangedData is the data of view.changed (no content: call view).
type ViewChangedData struct {
	Revision uint64 `json:"revision"`
}

// InputChangedData is the data of input.changed.
type InputChangedData struct {
	State string `json:"state"`
}

// AttentionChangedData is the data of attention.changed.
type AttentionChangedData struct {
	Attention bool   `json:"attention"`
	Reason    string `json:"reason,omitempty"`
}

// MailEventData is the data of mail.* events; never carries the body.
type MailEventData struct {
	ID   int64  `json:"id"`
	From string `json:"from"`
	To   string `json:"to"`
}
