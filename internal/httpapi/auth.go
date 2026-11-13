package httpapi

import (
	"context"
	"errors"
	"net/http"
)

// Scope is a device-token permission level: read < write < admin.
type Scope int

const (
	// ScopeNone marks a route that needs no authentication (GET /v1/health).
	ScopeNone Scope = iota
	ScopeRead
	ScopeWrite
	ScopeAdmin
)

func (s Scope) String() string {
	switch s {
	case ScopeNone:
		return "none"
	case ScopeRead:
		return "read"
	case ScopeWrite:
		return "write"
	case ScopeAdmin:
		return "admin"
	}
	return "unknown"
}

// Allows reports whether a token holding s may use a route needing need.
func (s Scope) Allows(need Scope) bool { return s >= need }

// Scopes returns the scope names s grants, lowest first (["read","write"]).
func (s Scope) Scopes() []string {
	var out []string
	for _, c := range []Scope{ScopeRead, ScopeWrite, ScopeAdmin} {
		if s >= c {
			out = append(out, c.String())
		}
	}
	return out
}

// Principal is an authenticated caller. Device is a label for the access log
// (never the token itself).
type Principal struct {
	Device string
	Scope  Scope
}

// Errors an Authenticator returns.
var (
	// ErrUnauthenticated: missing, unknown or revoked token (401).
	ErrUnauthenticated = errors.New("unauthenticated")
	// ErrForbidden: valid token, but not allowed (403).
	ErrForbidden = errors.New("forbidden")
)

// Authenticator decides who calls. It is invoked for every route whose scope
// is above ScopeNone, with the minimum scope the route needs. The real
// device-token implementation (security-remote 2) is wired by AGED-US-0021;
// until then Server.Auth is nil and only /v1/health is reachable.
type Authenticator interface {
	Authenticate(r *http.Request, need Scope) (*Principal, error)
}

// AuthenticatorFunc adapts a function to Authenticator.
type AuthenticatorFunc func(r *http.Request, need Scope) (*Principal, error)

func (f AuthenticatorFunc) Authenticate(r *http.Request, need Scope) (*Principal, error) {
	return f(r, need)
}

// DenyAll is the stub used while no real authenticator is configured: every
// authenticated route answers 401.
type DenyAll struct{}

func (DenyAll) Authenticate(*http.Request, Scope) (*Principal, error) {
	return nil, ErrUnauthenticated
}

type ctxKey int

const (
	keyPrincipal ctxKey = iota
	keyRequestID
)

// PrincipalFrom returns the authenticated caller of a request (nil on
// unauthenticated routes).
func PrincipalFrom(ctx context.Context) *Principal {
	p, _ := ctx.Value(keyPrincipal).(*Principal)
	return p
}

// RequestIDFrom returns the request id assigned by the middleware.
func RequestIDFrom(ctx context.Context) string {
	s, _ := ctx.Value(keyRequestID).(string)
	return s
}

func authError(err error) *Error {
	switch {
	case errors.Is(err, ErrUnauthenticated):
		return &Error{Status: http.StatusUnauthorized, Code: "unauthenticated", Message: "missing, unknown or revoked token"}
	case errors.Is(err, ErrForbidden):
		return &Error{Status: http.StatusForbidden, Code: "forbidden", Message: "not allowed"}
	}
	return errInternal("authentication failed")
}
