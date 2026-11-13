package apitoken

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/digiogithub/agentd/internal/httpapi"
)

// Pairing HTTP limits (security-remote 3.3).
const (
	// AddrLimit is the number of POST /v1/pair requests one remote address
	// may make per AddrWindow (successful or not).
	AddrLimit  = 10
	AddrWindow = time.Minute
	// LockoutFails failed claims within LockoutWindow lock pairing for
	// LockoutFor.
	LockoutFails  = 20
	LockoutWindow = 10 * time.Minute
	LockoutFor    = 10 * time.Minute
	// DefaultPairPoll is how often a waiting claim re-reads pairing.json.
	DefaultPairPoll = 200 * time.Millisecond
)

// PairHandlerOptions configure PairRoute.
type PairHandlerOptions struct {
	Clock    func() time.Time
	Log      *slog.Logger
	PollEach time.Duration
	// Audit, if set, receives a "device_pair" event per issued token (never
	// the token or the code).
	Audit httpapi.Auditor
}

// PairRoute returns the unauthenticated route POST /v1/pair, to be mounted
// with httpapi.Server.AddRoute. The request {code, name, platform} presents
// the one-time code; the response is withheld (long poll, up to the life of the
// code) until the host approves on `agentd serve pair`, then
// 201 {token, device_id, scope}. Errors: 400 malformed body, 403
// pairing_invalid / pairing_denied, 410 pairing_expired, 429 rate limited or
// locked out. Codes and tokens are never logged.
func PairRoute(p *Pairing, devs *Store, o PairHandlerOptions) httpapi.Route {
	h := newPairHandler(p, devs, o)
	return httpapi.Route{Method: "POST", Pattern: "/v1/pair", Scope: httpapi.ScopeNone, Handler: h.serve}
}

type pairHandler struct {
	p    *Pairing
	devs *Store
	log  *slog.Logger
	now  func() time.Time
	poll time.Duration
	aud  httpapi.Auditor

	mu       sync.Mutex
	perAddr  map[string][]time.Time
	fails    []time.Time
	lockedTo time.Time
}

func newPairHandler(p *Pairing, devs *Store, o PairHandlerOptions) *pairHandler {
	if o.Clock == nil {
		o.Clock = time.Now
	}
	if o.Log == nil {
		o.Log = slog.New(slog.DiscardHandler)
	}
	if o.PollEach <= 0 {
		o.PollEach = DefaultPairPoll
	}
	return &pairHandler{p: p, devs: devs, log: o.Log, now: o.Clock, poll: o.PollEach, aud: o.Audit, perAddr: map[string][]time.Time{}}
}

func pairErr(status int, code, msg string) *httpapi.Error {
	return &httpapi.Error{Status: status, Code: code, Message: msg}
}

// admit applies the per-address limit and the global lockout; it returns the
// Retry-After seconds when refused.
func (h *pairHandler) admit(addr string) (retry int, ok bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	now := h.now()
	if now.Before(h.lockedTo) {
		return int(h.lockedTo.Sub(now).Seconds()) + 1, false
	}
	cut := now.Add(-AddrWindow)
	ts := slicesKeep(h.perAddr[addr], cut)
	if len(ts) >= AddrLimit {
		h.perAddr[addr] = ts
		return int(ts[0].Add(AddrWindow).Sub(now).Seconds()) + 1, false
	}
	h.perAddr[addr] = append(ts, now)
	if len(h.perAddr) > 1024 { // bound memory: drop idle addresses
		for a, l := range h.perAddr {
			if l = slicesKeep(l, cut); len(l) == 0 {
				delete(h.perAddr, a)
			}
		}
	}
	return 0, true
}

func slicesKeep(ts []time.Time, cut time.Time) []time.Time {
	i := 0
	for i < len(ts) && !ts[i].After(cut) {
		i++
	}
	return ts[i:]
}

// failed records a failed claim and starts the lockout when too many.
func (h *pairHandler) failed(addr string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	now := h.now()
	h.fails = append(slicesKeep(h.fails, now.Add(-LockoutWindow)), now)
	if len(h.fails) >= LockoutFails {
		h.lockedTo = now.Add(LockoutFor)
		h.fails = nil
		h.log.Warn("pairing locked out: too many failed attempts", "for", LockoutFor.String(), "last_addr", addr)
	}
}

type pairReq struct {
	Code     string `json:"code"`
	Name     string `json:"name"`
	Platform string `json:"platform"`
}

type pairResp struct {
	Token    string `json:"token"`
	DeviceID string `json:"device_id"`
	Scope    string `json:"scope"`
}

// printable reports whether s is 1..max printable runes without controls or
// format characters (it is shown on the host's terminal).
func printable(s string, max int, allowEmpty bool) bool {
	if s == "" {
		return allowEmpty
	}
	if !utf8.ValidString(s) || utf8.RuneCountInString(s) > max {
		return false
	}
	return !strings.ContainsFunc(s, func(r rune) bool { return !unicode.IsPrint(r) && r != ' ' || unicode.Is(unicode.Cf, r) })
}

func (h *pairHandler) serve(w http.ResponseWriter, r *http.Request) error {
	addr := remoteHost(r.RemoteAddr)
	if retry, ok := h.admit(addr); !ok {
		return &httpapi.Error{Status: http.StatusTooManyRequests, Code: "rate_limited", Message: "too many pairing attempts", Retryable: true, RetryAfter: retry}
	}
	if err := h.p.readable(); err != nil {
		h.log.Error("pairing store unusable", "err", err)
		return pairErr(http.StatusServiceUnavailable, "pairing_unavailable", "pairing is unavailable")
	}
	var req pairReq
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		return pairErr(http.StatusBadRequest, "invalid_argument", "body must be JSON {code, name, platform}")
	}
	req.Name, req.Platform = strings.TrimSpace(req.Name), strings.TrimSpace(req.Platform)
	if len(req.Code) > 64 || !printable(req.Name, 64, false) || !printable(req.Platform, 32, true) {
		return pairErr(http.StatusBadRequest, "invalid_argument", "invalid code, name (1-64 printable characters) or platform (up to 32)")
	}
	rec, err := h.p.ClaimCode(req.Code, Claim{Name: req.Name, Platform: req.Platform, Addr: addr})
	if err != nil {
		if errors.Is(err, ErrBadCode) {
			h.failed(addr)
			h.log.Warn("pairing attempt failed", "addr", addr)
			return pairErr(http.StatusForbidden, "pairing_invalid", "invalid or expired pairing code")
		}
		h.log.Error("pairing claim", "err", err)
		return pairErr(http.StatusServiceUnavailable, "pairing_unavailable", "pairing is unavailable")
	}
	h.log.Info("pairing code claimed", "pairing", rec.ID, "addr", addr, "device", req.Name, "needs_approval", !rec.NoApprove)

	// Long poll until the host decides, the code expires or the device leaves.
	t := time.NewTicker(h.poll)
	defer t.Stop()
	for {
		cur, err := h.p.Get(rec.ID)
		switch {
		case err != nil:
			return pairErr(http.StatusGone, "pairing_expired", "pairing expired")
		case cur.State == PairApproved:
			return h.issue(w, r, rec.ID, req, addr)
		case cur.State == PairDenied:
			h.log.Info("pairing denied by host", "pairing", rec.ID, "addr", addr)
			return pairErr(http.StatusForbidden, "pairing_denied", "the host denied the pairing")
		case cur.State != PairClaimed:
			h.log.Info("pairing ended before approval", "pairing", rec.ID, "state", cur.State)
			return pairErr(http.StatusGone, "pairing_expired", "pairing expired or cancelled")
		}
		select {
		case <-r.Context().Done():
			_ = h.p.Cancel(rec.ID) // the device went away: burn the code
			return pairErr(http.StatusGone, "pairing_expired", "pairing abandoned")
		case <-t.C:
		}
	}
}

func (h *pairHandler) issue(w http.ResponseWriter, r *http.Request, id string, req pairReq, addr string) error {
	rec, err := h.p.Take(id) // exactly one caller issues the token
	if err != nil {
		return pairErr(http.StatusGone, "pairing_expired", "pairing expired or cancelled")
	}
	dev, tok, err := h.devs.Create(CreateArgs{Name: req.Name, Platform: req.Platform, Scope: rec.Scope, PairedVia: "qr"})
	if err != nil {
		h.log.Error("pairing: creating the device failed", "pairing", id, "err", err)
		return pairErr(http.StatusInternalServerError, "internal", "could not create the device")
	}
	_ = h.p.SetDevice(id, dev.ID)
	h.log.Info("device paired", "device", dev.ID, "addr", addr, "scope", dev.Scope)
	if h.aud != nil {
		h.aud.Audit(httpapi.AuditEvent{Time: h.now().UTC(), Device: dev.ID, Remote: addr, RequestID: httpapi.RequestIDFrom(r.Context()),
			Action: "device_pair", Result: "ok",
			Detail: map[string]any{"name": req.Name, "platform": req.Platform, "scope": dev.Scope, "pairing": id}})
	}
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	return json.NewEncoder(w).Encode(pairResp{Token: tok, DeviceID: dev.ID, Scope: dev.Scope})
}

func remoteHost(a string) string {
	if h, _, err := net.SplitHostPort(a); err == nil {
		return h
	}
	return a
}

// readable reports whether pairing.json is usable.
func (p *Pairing) readable() error {
	_, err := p.readData()
	return err
}
