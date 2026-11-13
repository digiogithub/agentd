package apitoken

import (
	"context"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"

	"github.com/digiogithub/agentd/internal/httpapi"
)

// Auth implements httpapi.Authenticator on top of a Store and, through Guard,
// ties in-flight requests to their device so a revoke cancels them.
type Auth struct {
	Store *Store

	mu   sync.Mutex
	live map[string]map[*binding]struct{} // device id -> in-flight requests
}

// NewAuth subscribes to the store's revocations. Call Store.Run to have
// changes made by other processes noticed.
func NewAuth(s *Store) *Auth {
	a := &Auth{Store: s, live: map[string]map[*binding]struct{}{}}
	s.Subscribe(a.cancelDevices)
	return a
}

type bindKey struct{}

// binding is the per-request slot Guard puts in the context; Authenticate
// fills it once the token verified.
type binding struct {
	cancel context.CancelFunc
	id     string
}

func (a *Auth) cancelDevices(ids []string) {
	a.mu.Lock()
	var cs []context.CancelFunc
	for _, id := range ids {
		for b := range a.live[id] {
			cs = append(cs, b.cancel)
		}
	}
	a.mu.Unlock()
	for _, c := range cs {
		c()
	}
}

// Authenticate verifies `Authorization: Bearer agd_<id>_<secret>` (the only
// accepted place for a token) and the route's minimum scope.
func (a *Auth) Authenticate(r *http.Request, need httpapi.Scope) (*httpapi.Principal, error) {
	tok, ok := bearer(r)
	if !ok || queryHasToken(r) {
		return nil, httpapi.ErrUnauthenticated
	}
	d, err := a.Store.Verify(tok, r.RemoteAddr)
	if err != nil {
		return nil, httpapi.ErrUnauthenticated
	}
	sc := scopeOf(d.Scope)
	if !sc.Allows(need) {
		return nil, httpapi.ErrForbidden
	}
	if b, _ := r.Context().Value(bindKey{}).(*binding); b != nil {
		a.bind(b, d.ID)
		// A revoke between Verify and bind would have missed this request.
		if !a.Store.IsActive(d.ID) {
			b.cancel()
			return nil, httpapi.ErrUnauthenticated
		}
	}
	return &httpapi.Principal{Device: d.ID, Scope: sc}, nil
}

func (a *Auth) bind(b *binding, id string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	b.id = id
	if a.live[id] == nil {
		a.live[id] = map[*binding]struct{}{}
	}
	a.live[id][b] = struct{}{}
}

func (a *Auth) unbind(b *binding) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if b.id != "" {
		delete(a.live[b.id], b)
		if len(a.live[b.id]) == 0 {
			delete(a.live, b.id)
		}
	}
}

// Guard is the handler wrapper installed as httpapi.Server.Guard (inside the
// request-id / access-log chain, so its 400 is logged): it
// answers 400 when a token travels in the query string (they leak into logs,
// history and Referer), and gives every request a context that is cancelled
// when its device is revoked or expires, which ends open SSE streams and long
// requests within about a second.
func (a *Auth) Guard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if queryHasToken(r) {
			httpapi.WriteError(w, &httpapi.Error{Status: http.StatusBadRequest, Code: "invalid_argument",
				Message: "tokens are accepted only in the Authorization header, never in the URL"})
			return
		}
		ctx, cancel := context.WithCancel(r.Context())
		b := &binding{cancel: cancel}
		defer func() { a.unbind(b); cancel() }()
		next.ServeHTTP(w, r.WithContext(context.WithValue(ctx, bindKey{}, b)))
	})
}

func scopeOf(s string) httpapi.Scope {
	switch s {
	case ScopeAdmin:
		return httpapi.ScopeAdmin
	case ScopeWrite:
		return httpapi.ScopeWrite
	case ScopeRead:
		return httpapi.ScopeRead
	}
	return httpapi.ScopeNone
}

// bearer extracts the token from a single Authorization header.
func bearer(r *http.Request) (string, bool) {
	vs := r.Header.Values("Authorization")
	if len(vs) != 1 {
		return "", false
	}
	scheme, tok, ok := strings.Cut(vs[0], " ")
	if !ok || !strings.EqualFold(scheme, "Bearer") || tok == "" || strings.ContainsAny(tok, " \t") {
		return "", false
	}
	return tok, true
}

// tokenRE matches the shape of a device token (see ParseToken).
var tokenRE = regexp.MustCompile(`agd_[0-9A-Z]{10}_[A-Za-z0-9_-]{43}`)

// queryHasToken reports whether the URL query carries a credential: a
// token-ish parameter NAME, or any parameter value (or, for a malformed query,
// the raw text) that has the shape of a device token. A plain mention of
// "agd_" (a session called agd_build, say) is not a token.
func queryHasToken(r *http.Request) bool {
	q := r.URL.RawQuery
	if q == "" {
		return false
	}
	vals, err := url.ParseQuery(q)
	if err != nil {
		if tokenRE.MatchString(q) {
			return true
		}
	}
	for k, vs := range vals {
		switch strings.ToLower(k) {
		case "token", "access_token", "api_key", "apikey", "auth", "authorization", "bearer":
			return true
		}
		if tokenRE.MatchString(k) {
			return true
		}
		for _, v := range vs {
			if tokenRE.MatchString(v) {
				return true
			}
		}
	}
	return false
}
