package httpapi

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"net/http"
	"runtime/debug"
	"time"

	"github.com/digiogithub/agentd/internal/proto"
)

type infoDaemon struct {
	i        proto.InfoResult
	identity string
}

func orEmptyAliases(i proto.InfoResult) []proto.AliasInfo {
	if i.Aliases == nil {
		return []proto.AliasInfo{}
	}
	return i.Aliases
}

func contextWithPrincipal(ctx context.Context, p *Principal) context.Context {
	return context.WithValue(ctx, keyPrincipal, p)
}

// statusWriter records status, size and the device label for the access log.
type statusWriter struct {
	http.ResponseWriter
	status int
	bytes  int64
	device string
	wrote  bool
}

func (w *statusWriter) WriteHeader(code int) {
	if !w.wrote {
		w.status, w.wrote = code, true
	}
	w.ResponseWriter.WriteHeader(code)
}

func (w *statusWriter) Write(b []byte) (int, error) {
	if !w.wrote {
		w.status, w.wrote = http.StatusOK, true
	}
	n, err := w.ResponseWriter.Write(b)
	w.bytes += int64(n)
	return n, err
}

// Flush keeps SSE working through the wrapper.
func (w *statusWriter) Flush() {
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func (w *statusWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

// markDevice tells the access logger which device made the request.
func markDevice(w http.ResponseWriter, device string) {
	for {
		switch v := w.(type) {
		case *statusWriter:
			v.device = device
			return
		case interface{ Unwrap() http.ResponseWriter }:
			w = v.Unwrap()
		default:
			return
		}
	}
}

func (s *Server) requestID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var b [8]byte
		_, _ = rand.Read(b[:])
		id := hex.EncodeToString(b[:])
		w.Header().Set("X-Request-Id", id)
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), keyRequestID, id)))
	})
}

func (s *Server) recoverer(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if v := recover(); v != nil {
				if v == http.ErrAbortHandler {
					panic(v)
				}
				s.Log.Error("panic in handler", "request_id", RequestIDFrom(r.Context()),
					"panic", v, "stack", string(debug.Stack()))
				WriteError(w, errInternal("internal error"))
			}
		}()
		next.ServeHTTP(w, r)
	})
}

// accessLog writes one line per request: method, route pattern (not the
// raw path: it may hold session names), status, duration, bytes, device
// label. Authorization headers, tokens, query strings and bodies are never
// logged.
func (s *Server) accessLog(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		sw := &statusWriter{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(sw, r)
		route := r.Pattern
		if route == "" {
			route = "-"
		}
		s.Log.Info("request", "request_id", RequestIDFrom(r.Context()), "device", sw.device,
			"method", r.Method, "route", route, "status", sw.status,
			"duration_ms", time.Since(start).Milliseconds(), "bytes", sw.bytes)
	})
}
