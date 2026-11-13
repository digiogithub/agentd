package httpapi

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// AuditEvent is one line of the audit trail (api/audit.log, JSONL). It records
// who did what, never prompt text, message bodies or tokens: act carries the
// text LENGTH, send/reply the ids and body lengths.
type AuditEvent struct {
	Time      time.Time      `json:"ts"`
	Device    string         `json:"device,omitempty"`
	Remote    string         `json:"remote,omitempty"`
	RequestID string         `json:"request_id,omitempty"`
	Action    string         `json:"action"`
	Session   string         `json:"session,omitempty"`
	Result    string         `json:"result"` // "ok" or the error code
	Detail    map[string]any `json:"detail,omitempty"`
}

// Auditor receives audit events. Implementations must not block for long and
// must be safe for concurrent use.
type Auditor interface{ Audit(AuditEvent) }

// AuditFile appends events to a JSONL file (0600), rotating once to
// <path>.1 when it would exceed maxBytes (0 = no cap), like session logs.
// Several processes (the gateway and `agentd serve devices`) may append to the
// same path: the file is re-opened when it was rotated under us.
type AuditFile struct {
	path     string
	maxBytes int64
	mu       sync.Mutex
	f        *os.File
}

// OpenAuditFile prepares path (its directory is created 0700).
func OpenAuditFile(path string, maxBytes int64) (*AuditFile, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	a := &AuditFile{path: path, maxBytes: maxBytes}
	if err := a.reopenLocked(); err != nil {
		return nil, err
	}
	return a, nil
}

func (a *AuditFile) reopenLocked() error {
	if a.f != nil {
		_ = a.f.Close()
		a.f = nil
	}
	f, err := os.OpenFile(a.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	_ = f.Chmod(0o600)
	a.f = f
	return nil
}

// Audit appends ev; failures are swallowed (auditing must never break a
// request) after one retry on a fresh handle.
func (a *AuditFile) Audit(ev AuditEvent) {
	if ev.Time.IsZero() {
		ev.Time = time.Now().UTC()
	}
	b, err := json.Marshal(ev)
	if err != nil {
		return
	}
	b = append(b, '\n')
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.f != nil {
		cur, e1 := a.f.Stat()
		onDisk, e2 := os.Stat(a.path)
		if e1 != nil || e2 != nil || !os.SameFile(cur, onDisk) {
			a.f = nil // rotated or removed by another process
		} else if a.maxBytes > 0 && cur.Size() > 0 && cur.Size()+int64(len(b)) > a.maxBytes {
			_ = a.f.Close()
			a.f = nil
			_ = os.Rename(a.path, a.path+".1")
		}
	}
	if a.f == nil {
		if a.reopenLocked() != nil {
			return
		}
	}
	_, _ = a.f.Write(b)
}

// Close closes the file.
func (a *AuditFile) Close() error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.f == nil {
		return nil
	}
	err := a.f.Close()
	a.f = nil
	return err
}

// auditRec accumulates the audit line of one request.
type auditRec struct {
	s  *Server
	ev AuditEvent
}

// auditOp starts the audit line of the current request: call it at the top
// of a handler, fill Session/Detail, and `defer ar.done(&err)`.
func (s *Server) auditOp(r *http.Request, action string) *auditRec {
	ev := AuditEvent{Action: action, Remote: remoteIP(r), RequestID: RequestIDFrom(r.Context()), Detail: map[string]any{}}
	if p := PrincipalFrom(r.Context()); p != nil {
		ev.Device = p.Device
	}
	return &auditRec{s: s, ev: ev}
}

func (a *auditRec) set(k string, v any) { a.ev.Detail[k] = v }

// done records the outcome; err is the handler's named return.
func (a *auditRec) done(err *error) {
	if a.s.Audit == nil {
		return
	}
	a.ev.Result = "ok"
	if err != nil && *err != nil {
		a.ev.Result = FromDaemonError(*err).Code
	}
	if len(a.ev.Detail) == 0 {
		a.ev.Detail = nil
	}
	a.ev.Time = time.Now().UTC()
	a.s.Audit.Audit(a.ev)
}
