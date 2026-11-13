package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/digiogithub/agentd/internal/client"
	"github.com/digiogithub/agentd/internal/proto"
)

// Error is an API error: it becomes the JSON envelope
// {"error":{"code","message","retryable","details"}} with Status as the HTTP
// status (docs/design/api/http-api.md 2.2).
type Error struct {
	Status     int
	Code       string
	Message    string
	Retryable  bool
	Details    map[string]any
	RetryAfter int // seconds; 0 = no Retry-After header
	// cause is the underlying error, logged by the gateway but never sent.
	cause error
}

func (e *Error) Error() string { return e.Code + ": " + e.Message }

// Gateway-level error constructors.
func errInvalid(format string, a ...any) *Error {
	return &Error{Status: http.StatusBadRequest, Code: "invalid_argument", Message: sprintf(format, a...)}
}

func errUnavailable(msg string) *Error {
	return &Error{Status: http.StatusServiceUnavailable, Code: "daemon_unavailable", Message: msg, Retryable: true, RetryAfter: 1}
}

func errInternal(msg string) *Error {
	return &Error{Status: http.StatusInternalServerError, Code: "internal", Message: msg}
}

func errBusy() *Error {
	return &Error{Status: http.StatusServiceUnavailable, Code: "busy", Message: "gateway is busy, retry shortly", Retryable: true, RetryAfter: 1}
}

func errRateLimited(retry int) *Error {
	if retry < 1 {
		retry = 1
	}
	return &Error{Status: http.StatusTooManyRequests, Code: "rate_limited", Message: "too many requests", Retryable: true, RetryAfter: retry}
}

// errOutcomeUnknown reports a side-effecting request that reached (or may have
// reached) the daemon but whose result was not seen. It is never retryable:
// the caller must look at the resource (the view, the session list, the
// mailbox) before deciding to repeat it.
func errOutcomeUnknown(status int, cause error) *Error {
	return &Error{Status: status, Code: "outcome_unknown", Retryable: false, cause: cause,
		Message: "the request reached the daemon but its outcome is unknown; fetch the current state (e.g. GET the session view) before repeating it",
		Details: map[string]any{"hint": "refetch the resource; do not blindly retry"}}
}

type envelope struct {
	Error envelopeBody `json:"error"`
}

type envelopeBody struct {
	Code      string         `json:"code"`
	Message   string         `json:"message"`
	Retryable bool           `json:"retryable"`
	Details   map[string]any `json:"details,omitempty"`
}

// WriteError writes err as the JSON error envelope. A non-*Error is reported
// as 500 internal (its text is not leaked).
func WriteError(w http.ResponseWriter, err error) {
	var ae *Error
	if !errors.As(err, &ae) {
		ae = errInternal("internal error")
	}
	if ae.RetryAfter > 0 {
		w.Header().Set("Retry-After", strconv.Itoa(ae.RetryAfter))
	}
	writeJSON(w, ae.Status, envelope{Error: envelopeBody{
		Code: ae.Code, Message: ae.Message, Retryable: ae.Retryable, Details: ae.Details,
	}})
}

// FromDaemonError maps an error returned by a daemon call to an *Error: the
// one place that knows the daemon code -> HTTP status table (http-api 2.2,
// daemon-ops 3.5). Daemon codes pass through unchanged unless the HTTP doc
// renames them; legacy messages without a code are matched by text.
func FromDaemonError(err error) *Error {
	var ae *Error
	if errors.As(err, &ae) {
		return ae
	}
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		return &Error{Status: http.StatusGatewayTimeout, Code: "timeout", Message: "daemon call timed out", Retryable: true}
	case errors.Is(err, context.Canceled):
		return &Error{Status: 499, Code: "canceled", Message: "request canceled"}
	}
	if !client.IsRemote(err) {
		ue := errUnavailable("daemon unreachable")
		ue.cause = err
		return ue
	}
	msg := err.Error()
	e := &Error{Message: msg}
	switch code := proto.CodeOf(err); code {
	case proto.CodeInvalid:
		e.Status, e.Code = http.StatusBadRequest, "invalid_argument"
	case proto.CodeNotFound:
		e.Status, e.Code = http.StatusNotFound, "not_found"
	case proto.CodeStarting:
		e.Status, e.Code, e.Retryable, e.RetryAfter = http.StatusConflict, "starting", true, 1
	case proto.CodeExited:
		e.Status, e.Code = http.StatusConflict, "exited"
	case proto.CodeExists:
		e.Status, e.Code = http.StatusConflict, "already_exists"
	case proto.CodeForbidden:
		e.Status, e.Code = http.StatusForbidden, "forbidden"
	case proto.CodeConflict:
		e.Status, e.Code = http.StatusConflict, "conflict"
	case proto.CodeBusy:
		e.Status, e.Code, e.Retryable = http.StatusConflict, "busy", true
	case proto.CodeNotReady:
		e.Status, e.Code, e.Retryable = http.StatusConflict, "not_ready", true
	case proto.CodeVerifyFailed:
		// Paste never appeared; nothing was submitted (http-api 2.2).
		e.Status, e.Code, e.Retryable, e.RetryAfter = http.StatusServiceUnavailable, "paste_unverified", true, 1
	case proto.CodeTimeout:
		e.Status, e.Code, e.Retryable = http.StatusGatewayTimeout, "timeout", true
	case "":
		return legacyDaemonError(msg)
	default:
		// CodeInternal or a code this gateway does not know: the daemon's
		// text may hold paths or other host details, so it is only logged.
		ie := errInternal("internal error")
		ie.cause = err
		return ie
	}
	attachDetails(e, err)
	return e
}

// legacyDaemonError classifies the few uncoded daemon messages the gateway
// must tell apart (http-api 2.2); everything else is 500 daemon_error.
func legacyDaemonError(msg string) *Error {
	switch {
	case strings.Contains(msg, "no such session"):
		return &Error{Status: http.StatusNotFound, Code: "not_found", Message: msg}
	case strings.Contains(msg, "already exists"):
		return &Error{Status: http.StatusConflict, Code: "already_exists", Message: msg}
	case strings.Contains(msg, "is starting"):
		return &Error{Status: http.StatusConflict, Code: "starting", Message: msg, Retryable: true, RetryAfter: 1}
	}
	ie := errInternal("internal error")
	ie.cause = errors.New(msg)
	return ie
}

// attachDetails exposes the structured data the daemon attached to an error
// (e.g. the current view on a conflict) as details.daemon.
func attachDetails(e *Error, err error) {
	var re *proto.RemoteError
	if errors.As(err, &re) && len(re.Data) > 0 {
		var v any
		if json.Unmarshal(re.Data, &v) == nil {
			e.Details = map[string]any{"daemon": v}
		}
	}
}
