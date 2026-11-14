package hcompat

import (
	"encoding/json"
	"path/filepath"
	"strings"

	"github.com/digiogithub/agentd/internal/proto"
)

// CoreInfo identifies the core (hello.core, server.status.core).
type CoreInfo struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// Status is one hc status record (the OSC 7501 root record in herdr-core).
type Status struct {
	State string `json:"state"`
	Kind  string `json:"kind,omitempty"`
	App   string `json:"app,omitempty"`
}

// Terminal is the hc terminal record (schema Terminal).
type Terminal struct {
	Address    string            `json:"address"`
	Name       *string           `json:"name"`
	Labels     map[string]string `json:"labels"`
	Argv       []string          `json:"argv"`
	Cwd        string            `json:"cwd"`
	State      string            `json:"state"`
	CreatedMS  int64             `json:"created_ms"`
	Screen     string            `json:"screen"`
	Cols       uint16            `json:"cols"`
	Rows       uint16            `json:"rows"`
	Pid        *int              `json:"pid"`
	Title      string            `json:"title"`
	Status     *Status           `json:"status"`
	Records    map[string]Status `json:"records"`
	Reported   bool              `json:"reported"`
	Turn       uint64            `json:"turn"`
	Keep       bool              `json:"keep"`
	Parent     *string           `json:"parent"`
	Reason     *string           `json:"reason"`
	ExitCode   *int              `json:"exit_code"`
	Signal     *int              `json:"signal"`
	FinishedMS *int64            `json:"finished_ms"`
	Foreground json.RawMessage   `json:"foreground"`
}

// rpcError is a JSON-RPC error with herdr-core's stable data.kind.
type rpcError struct {
	Code    int       `json:"code"`
	Message string    `json:"message"`
	Data    errorData `json:"data"`
}

type errorData struct {
	Kind string `json:"kind"`
	Hint string `json:"hint,omitempty"`
}

func (e *rpcError) Error() string { return e.Message }

func newError(kind, msg string) *rpcError {
	return &rpcError{Code: -32000, Message: msg, Data: errorData{Kind: kind}}
}

func invalidParams(msg string) *rpcError {
	return &rpcError{Code: -32602, Message: msg, Data: errorData{Kind: "invalid_params"}}
}

// fromDaemon maps a daemon error (proto.Code*) to an hc error kind.
func fromDaemon(err error) *rpcError {
	kind := "internal"
	switch proto.CodeOf(err) {
	case proto.CodeNotFound:
		kind = "not_found"
	case proto.CodeExited:
		kind = "finished"
	case proto.CodeStarting, proto.CodeBusy, proto.CodeNotReady, proto.CodeTimeout:
		kind = "unavailable"
	case proto.CodeInvalid:
		kind = "invalid_params"
	case proto.CodeConflict, proto.CodeExists:
		kind = "conflict"
	case proto.CodeForbidden:
		kind = "forbidden"
	}
	return newError(kind, err.Error())
}

// Label keys this package fills in by default. Herdr Desktop adopts a live
// terminal without a pane when it carries a non-empty herdr.space (and places
// it in herdr.tab), creating unknown spaces and tabs.
const (
	LabelSpace   = "herdr.space"
	LabelTab     = "herdr.tab"
	LabelHarness = "agentd.harness"
)

// projectLabel is the project a session belongs to: the part after '@' in
// the <harness>@<project> naming convention, else the base of its cwd.
func projectLabel(s proto.SessionInfo) string {
	if i := strings.LastIndexByte(s.Name, '@'); i >= 0 && i < len(s.Name)-1 {
		return s.Name[i+1:]
	}
	if s.Cwd != "" {
		return filepath.Base(s.Cwd)
	}
	return "agentd"
}

// mapStatus turns the daemon's semantic status into an hc status word. hc
// stores only what the program reports (OSC 7501); agentd classifies the
// screen instead, which is a documented deviation.
func mapStatus(s proto.SessionInfo) *Status {
	st := Status{App: s.Profile}
	switch s.Status {
	case "working":
		st.State = "working"
	case "waiting":
		st.State = "blocked"
		st.Kind = "question"
	case "idle":
		st.State = "idle"
	default: // starting, exited, unknown
		return nil
	}
	return &st
}
