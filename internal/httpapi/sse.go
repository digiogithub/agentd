package httpapi

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"
)

const (
	defaultSSEHeartbeat = 15 * time.Second
	defaultEventBuffer  = 1024
	// sseQueueSize bounds the per-client event queue; a client that falls
	// further behind gets a resync instead of unbounded memory growth.
	sseQueueSize = 256
	// sseWriteTimeout bounds one frame write to a client; a stuck client
	// releases its handler goroutine instead of holding it forever.
	sseWriteTimeout = 15 * time.Second
	sseRetryMS      = 3000
)

func (s *Server) sseHeartbeat() time.Duration {
	if s.Config.SSEHeartbeat > 0 {
		return s.Config.SSEHeartbeat
	}
	return defaultSSEHeartbeat
}

func (s *Server) eventBuffer() int {
	if s.Config.EventBuffer > 0 {
		return s.Config.EventBuffer
	}
	return defaultEventBuffer
}

// sseWriter writes SSE frames and flushes after each one.
type sseWriter struct {
	w  http.ResponseWriter
	rc *http.ResponseController
}

// startSSE writes the stream headers and the retry hint. Call it only after
// every error that should be a JSON envelope has been ruled out.
func startSSE(w http.ResponseWriter) (*sseWriter, error) {
	sw := &sseWriter{w: w, rc: http.NewResponseController(w)}
	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-cache")
	h.Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	if err := sw.raw("retry: " + itoa(sseRetryMS) + "\n\n"); err != nil {
		return nil, err
	}
	return sw, nil
}

func itoa(n int) string {
	b, _ := json.Marshal(n)
	return string(b)
}

func (sw *sseWriter) raw(frame string) error {
	_ = sw.rc.SetWriteDeadline(time.Now().Add(sseWriteTimeout))
	if _, err := sw.w.Write([]byte(frame)); err != nil {
		return err
	}
	return sw.rc.Flush()
}

// event writes one frame. id may be empty (the client keeps its last id).
// data must be a single line (compact JSON).
func (sw *sseWriter) event(id, name string, data []byte) error {
	var b strings.Builder
	if id != "" {
		b.WriteString("id: " + id + "\n")
	}
	b.WriteString("event: " + name + "\n")
	b.WriteString("data: ")
	b.Write(data)
	b.WriteString("\n\n")
	return sw.raw(b.String())
}

func (sw *sseWriter) comment(text string) error { return sw.raw(": " + text + "\n\n") }

// envelopeData is the `data` payload of every gateway-formed event.
type envelopeData struct {
	Type    string          `json:"type"`
	Session string          `json:"session,omitempty"`
	TS      time.Time       `json:"ts"`
	Data    json.RawMessage `json:"data,omitempty"`
}

func controlPayload(kind string, data any) []byte {
	raw, _ := json.Marshal(data)
	b, _ := json.Marshal(envelopeData{Type: kind, TS: time.Now().UTC(), Data: raw})
	return b
}
