package httpapi

import (
	"mime"
	"net/http"
	"sort"
	"strings"

	"github.com/digiogithub/agentd/internal/client"
)

// Handler handles one route. Returning an error writes the JSON envelope
// (FromDaemonError is applied to non-*Error values).
type Handler func(w http.ResponseWriter, r *http.Request) error

// Route is one entry of the routing table.
type Route struct {
	Method  string
	Pattern string // Go 1.22 ServeMux path pattern, e.g. /v1/sessions/{name}
	Scope   Scope  // minimum scope; ScopeNone = unauthenticated
	// Feature, when set, is advertised in GET /v1/info features[] (ADR-A16).
	Feature string
	Handler Handler
}

func (s *Server) baseRoutes() []Route {
	rs := []Route{
		{Method: "GET", Pattern: "/v1/health", Scope: ScopeNone, Handler: s.handleHealth},
		{Method: "GET", Pattern: "/v1/info", Scope: ScopeRead, Feature: "info", Handler: s.handleInfo},
		{Method: "GET", Pattern: "/v1/events", Scope: ScopeRead, Feature: "events", Handler: s.handleEvents},
		{Method: "GET", Pattern: "/v1/sessions/{name}/view/stream", Scope: ScopeRead, Feature: "view_stream", Handler: s.handleViewStream},
	}
	return append(rs, s.restRoutes()...) // sessions, view, act, terminal, mailbox (sessions.go)
}

// buildHandler assembles the mux and the middleware chain:
// request id -> access log -> recover -> guard (device-token guard, set by
// cmd/agentd) -> secure (headers, Host 421, Origin/CORS) -> mux; per route:
// method/content-type checks -> body cap -> auth -> rate limit -> handler.
func (s *Server) buildHandler() http.Handler {
	mux := http.NewServeMux()
	for _, rt := range s.routes {
		mux.Handle(rt.Method+" "+rt.Pattern, s.wrapRoute(rt))
	}
	mux.HandleFunc("/", s.notFound)
	var h http.Handler = mux
	h = s.secure(h)
	if s.Guard != nil {
		h = s.Guard(h)
	}
	h = s.recoverer(h)
	h = s.accessLog(h)
	h = s.requestID(h)
	return h
}

func (s *Server) wrapRoute(rt Route) http.Handler {
	limit := s.Config.maxBody()
	noHead := strings.Contains(rt.Pattern, "/stream") || rt.Pattern == "/v1/events" || strings.HasPrefix(rt.Pattern, "/v1/mailbox")
	isAct := strings.HasSuffix(rt.Pattern, "/act")
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodHead && noHead {
			w.Header().Set("Allow", "GET")
			WriteError(w, &Error{Status: http.StatusMethodNotAllowed, Code: "method_not_allowed", Message: "HEAD is not supported on this route"})
			return
		}
		if r.ContentLength > limit {
			WriteError(w, errTooLarge(limit))
			return
		}
		if r.Method == http.MethodPost && r.ContentLength != 0 && !isJSONContent(r.Header.Get("Content-Type")) {
			WriteError(w, &Error{Status: http.StatusUnsupportedMediaType, Code: "unsupported_media_type",
				Message: "requests with a body must be Content-Type: application/json"})
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, limit)
		if rt.Scope > ScopeNone {
			addr := remoteIP(r)
			if retry, blocked := s.limits.authBlocked(addr); blocked {
				WriteError(w, errRateLimited(retry))
				return
			}
			p, err := s.Auth.Authenticate(r, rt.Scope)
			if err == nil && (p == nil || !p.Scope.Allows(rt.Scope)) {
				err = ErrForbidden
			}
			if err != nil {
				ae := authError(err)
				if ae.Status == http.StatusUnauthorized {
					s.limits.authFailed(addr)
					w.Header().Set("WWW-Authenticate", "Bearer")
				}
				WriteError(w, ae)
				return
			}
			if retry, ok := s.limits.allow(p.Device, isAct); !ok {
				markDevice(w, p.Device)
				WriteError(w, errRateLimited(retry))
				return
			}
			r = r.WithContext(contextWithPrincipal(r.Context(), p))
			markDevice(w, p.Device)
		}
		if err := rt.Handler(w, r); err != nil {
			ae := FromDaemonError(err)
			if ae.cause != nil {
				s.Log.Warn("request failed", "request_id", RequestIDFrom(r.Context()), "route", rt.Pattern,
					"code", ae.Code, "err", ae.cause)
			}
			WriteError(w, ae)
		}
	})
}

func isJSONContent(ct string) bool {
	mt, _, err := mime.ParseMediaType(ct)
	return err == nil && mt == "application/json"
}

// notFound answers unmatched requests: 405 (with Allow) when the path exists
// for other methods, else 404, both as JSON envelopes.
func (s *Server) notFound(w http.ResponseWriter, r *http.Request) {
	var allow []string
	for _, rt := range s.routes {
		if matchPattern(rt.Pattern, r.URL.Path) {
			allow = append(allow, rt.Method)
		}
	}
	if len(allow) > 0 {
		sort.Strings(allow)
		w.Header().Set("Allow", strings.Join(allow, ", "))
		WriteError(w, &Error{Status: http.StatusMethodNotAllowed, Code: "method_not_allowed", Message: "method not allowed"})
		return
	}
	WriteError(w, &Error{Status: http.StatusNotFound, Code: "not_found", Message: "no such route"})
}

// matchPattern matches a ServeMux path pattern ({x} and {x...} wildcards).
func matchPattern(pattern, path string) bool {
	ps, xs := strings.Split(pattern, "/"), strings.Split(path, "/")
	for i, p := range ps {
		if strings.HasPrefix(p, "{") && strings.HasSuffix(p, "...}") {
			return true
		}
		if i >= len(xs) {
			return false
		}
		if strings.HasPrefix(p, "{") && strings.HasSuffix(p, "}") {
			if xs[i] == "" {
				return false
			}
			continue
		}
		if p != xs[i] {
			return false
		}
	}
	return len(ps) == len(xs)
}

// ---- endpoints ----

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) error {
	if err := s.ping(); err != nil {
		return err
	}
	WriteJSON(w, http.StatusOK, map[string]any{"ok": true, "version": s.Config.Version})
	return nil
}

func (s *Server) features() []string {
	seen := map[string]bool{}
	out := []string{}
	for _, rt := range s.routes {
		if rt.Feature != "" && !seen[rt.Feature] {
			seen[rt.Feature] = true
			out = append(out, rt.Feature)
		}
	}
	sort.Strings(out)
	return out
}

func (s *Server) handleInfo(w http.ResponseWriter, r *http.Request) error {
	var info infoDaemon
	err := s.WithDaemon(r.Context(), func(c *client.Client) error {
		i, err := c.Info()
		if err != nil {
			return err
		}
		who, err := c.Whoami()
		if err != nil {
			return err
		}
		info = infoDaemon{i: i, identity: who.Identity}
		return nil
	})
	if err != nil {
		return err
	}
	p := PrincipalFrom(r.Context())
	profiles := make([]map[string]any, 0, len(info.i.Profiles))
	for _, pr := range info.i.Profiles {
		profiles = append(profiles, map[string]any{"name": pr.Name, "builtin": pr.Builtin})
	}
	harnesses := s.Config.RunHarnesses
	if harnesses == nil {
		harnesses = []string{}
	}
	WriteJSON(w, http.StatusOK, map[string]any{
		"api_version": APIVersion,
		"features":    s.features(),
		"host":        info.i.Host,
		"gateway":     map[string]any{"version": s.Config.Version},
		"daemon":      map[string]any{"version": info.i.Version, "pid": info.i.Pid, "epoch": info.i.Epoch},
		"identity":    info.identity,
		"scopes":      p.Scope.Scopes(),
		"harnesses":   harnesses,
		"profiles":    profiles,
		"aliases":     orEmptyAliases(info.i),
		"limits":      map[string]any{"max_body_kb": s.Config.maxBody() >> 10, "prompt_max_chars": PromptMaxChars},
	})
	return nil
}
