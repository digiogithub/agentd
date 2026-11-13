package httpapi

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/digiogithub/agentd/internal/client"
	"github.com/digiogithub/agentd/internal/proto"
)

var (
	sessionNameRE = regexp.MustCompile(`^[a-zA-Z0-9._@-]+$`)
	targetRE      = regexp.MustCompile(`^[a-zA-Z0-9._-]+$`)
	harnessRE     = regexp.MustCompile(`^[a-zA-Z0-9._/-]+$`)
	recipientRE   = regexp.MustCompile(`^([a-zA-Z0-9._@-]+|human:[a-zA-Z0-9._-]{1,64})$`)
)

const (
	maxPreview   = 200
	maxViewLines = 5000
)

// restRoutes returns the v1 REST routes of AGED-US-0017 / AGED-US-0020.
func (s *Server) restRoutes() []Route {
	idem := newIdemStore(s.Config.IdempotencyTTL)
	s.idem = idem
	w := func(h Handler) Handler { return idempotent(idem, h) }
	return []Route{
		{Method: "GET", Pattern: "/v1/sessions", Scope: ScopeRead, Feature: "sessions", Handler: s.handleListSessions},
		{Method: "POST", Pattern: "/v1/sessions", Scope: ScopeWrite, Handler: w(s.handleRunSession)},
		{Method: "GET", Pattern: "/v1/sessions/{name}", Scope: ScopeRead, Handler: s.handleGetSession},
		{Method: "DELETE", Pattern: "/v1/sessions/{name}", Scope: ScopeWrite, Handler: s.handleRemoveSession},
		{Method: "POST", Pattern: "/v1/sessions/{name}/kill", Scope: ScopeWrite, Handler: s.handleKillSession},
		{Method: "GET", Pattern: "/v1/sessions/{name}/view", Scope: ScopeRead, Feature: "view", Handler: s.handleView},
		{Method: "POST", Pattern: "/v1/sessions/{name}/view/ack", Scope: ScopeWrite, Handler: s.handleViewAck},
		{Method: "POST", Pattern: "/v1/sessions/{name}/act", Scope: ScopeWrite, Feature: "act", Handler: w(s.handleAct)},
		{Method: "GET", Pattern: "/v1/sessions/{name}/terminal", Scope: ScopeRead, Feature: "terminal", Handler: s.handleTerminal},
		{Method: "GET", Pattern: "/v1/mailbox", Scope: ScopeRead, Feature: "mailbox", Handler: s.handleListMailbox},
		{Method: "POST", Pattern: "/v1/mailbox/messages", Scope: ScopeWrite, Handler: w(s.handleSendMessage)},
		{Method: "GET", Pattern: "/v1/mailbox/messages/{id}", Scope: ScopeRead, Handler: s.handleGetMessage},
		{Method: "POST", Pattern: "/v1/mailbox/messages/{id}/read", Scope: ScopeWrite, Handler: s.handleReadMessage},
		{Method: "POST", Pattern: "/v1/mailbox/messages/{id}/reply", Scope: ScopeWrite, Handler: w(s.handleReplyMessage)},
	}
}

// ---- helpers ----

func sessionName(r *http.Request) (string, error) {
	n := r.PathValue("name")
	if !sessionNameRE.MatchString(n) {
		return "", errInvalid("invalid session name")
	}
	return n, nil
}

func sessionLocation(name string) string {
	return "/v1/sessions/" + strings.ReplaceAll(url.PathEscape(name), "@", "%40")
}

// readBody reads the (size-capped) body and re-arms r.Body.
func readBody(r *http.Request) ([]byte, error) {
	b, err := io.ReadAll(r.Body)
	if err != nil {
		return nil, bodyError(err)
	}
	r.Body = io.NopCloser(bytes.NewReader(b))
	return b, nil
}

// decodeOptional decodes the body into v; an absent body leaves v untouched.
func decodeOptional(r *http.Request, v any) error {
	b, err := readBody(r)
	if err != nil {
		return err
	}
	if len(bytes.TrimSpace(b)) == 0 {
		return nil
	}
	return DecodeJSON(r, v)
}

func findSession(c *client.Client, name string) (proto.SessionInfo, error) {
	list, err := c.Ls()
	if err != nil {
		return proto.SessionInfo{}, err
	}
	for _, si := range list {
		if si.Name == name {
			return clipPreview(si), nil
		}
	}
	return proto.SessionInfo{}, notFoundErr(fmt.Sprintf("no such session %q", name))
}

// notFoundErr is a daemon-style not_found. It must be a *proto.RemoteError:
// WithDaemon treats any other error from the call as a broken connection.
func notFoundErr(msg string) error { return &proto.RemoteError{Code: proto.CodeNotFound, Msg: msg} }

func clipPreview(si proto.SessionInfo) proto.SessionInfo {
	if r := []rune(si.Preview); len(r) > maxPreview {
		si.Preview = string(r[:maxPreview])
	}
	return si
}

// formatETag is the view ETag `"e<epoch>-i<instance>-r<revision>"`: the daemon
// epoch, the session instance (its start time, so a session re-created under
// the same name never matches an old ETag) and the view revision.
func formatETag(epoch, inst string, rev uint64) string {
	return `"e` + epoch + `-i` + inst + `-r` + strconv.FormatUint(rev, 10) + `"`
}

type etagRef struct {
	epoch, inst string
	rev         uint64
	weak        bool
}

// parseETagRef parses one view ETag (the instance part is optional, for
// ETags minted by older gateways).
func parseETagRef(v string) (etagRef, bool) {
	var e etagRef
	v = strings.TrimSpace(v)
	if strings.HasPrefix(v, "W/") {
		e.weak, v = true, v[2:]
	}
	if len(v) < 2 || v[0] != '"' || v[len(v)-1] != '"' {
		return e, false
	}
	v = v[1 : len(v)-1]
	if !strings.HasPrefix(v, "e") {
		return e, false
	}
	i := strings.LastIndex(v, "-r")
	if i < 2 {
		return e, false
	}
	n, err := strconv.ParseUint(v[i+2:], 10, 64)
	if err != nil {
		return e, false
	}
	e.rev, e.epoch = n, v[1:i]
	if k := strings.LastIndex(e.epoch, "-i"); k > 0 {
		e.epoch, e.inst = e.epoch[:k], e.epoch[k+2:]
	}
	return e, true
}

// splitETagList splits an If-Match / If-None-Match value into entity tags
// (commas inside quotes are not separators). star reports a bare "*".
func splitETagList(h string) (tags []string, star bool) {
	var cur strings.Builder
	inQ := false
	flush := func() {
		if t := strings.TrimSpace(cur.String()); t != "" {
			tags = append(tags, t)
		}
		cur.Reset()
	}
	for _, c := range h {
		switch {
		case c == '"':
			inQ = !inQ
			cur.WriteRune(c)
		case c == ',' && !inQ:
			flush()
		default:
			cur.WriteRune(c)
		}
	}
	flush()
	if len(tags) == 1 && tags[0] == "*" {
		return nil, true
	}
	return tags, false
}

// etagMatches implements the weak comparison of If-None-Match (RFC 9110
// 8.8.3.2): "*" matches, otherwise the opaque tags are compared ignoring W/.
func etagMatches(header, etag string) bool {
	tags, star := splitETagList(header)
	if star {
		return true
	}
	for _, t := range tags {
		if strings.TrimPrefix(t, "W/") == strings.TrimPrefix(etag, "W/") {
			return true
		}
	}
	return false
}

// sessionInstance identifies one incarnation of a session name.
func sessionInstance(si proto.SessionInfo) string {
	return strconv.FormatInt(si.StartedAt.UnixNano(), 36)
}

// currentInstance looks the instance up (best effort, "" when unknown).
func (s *Server) currentInstance(ctx context.Context, name string) string {
	var inst string
	_ = s.WithDaemon(ctx, func(c *client.Client) error {
		si, err := findSession(c, name)
		if err == nil {
			inst = sessionInstance(si)
		}
		return err
	})
	return inst
}

// ---- sessions ----

func (s *Server) handleListSessions(w http.ResponseWriter, r *http.Request) error {
	var list []proto.SessionInfo
	if err := s.WithDaemon(r.Context(), func(c *client.Client) (e error) {
		list, e = c.Ls()
		return e
	}); err != nil {
		return err
	}
	sort.Slice(list, func(i, j int) bool { return list[i].Name < list[j].Name })
	h := sha256.New()
	for i := range list {
		list[i] = clipPreview(list[i])
		fmt.Fprintf(h, "%s|%s|%d|%d|%s\n", list[i].Name, list[i].State, list[i].Revision, list[i].Unread, list[i].InputState)
	}
	etag := `W/"s-` + hex.EncodeToString(h.Sum(nil)[:8]) + `"`
	w.Header().Set("ETag", etag)
	if inm := r.Header.Get("If-None-Match"); inm != "" && etagMatches(inm, etag) {
		w.WriteHeader(http.StatusNotModified)
		return nil
	}
	if list == nil {
		list = []proto.SessionInfo{}
	}
	WriteJSON(w, http.StatusOK, map[string]any{"sessions": list})
	return nil
}

func (s *Server) handleGetSession(w http.ResponseWriter, r *http.Request) error {
	name, err := sessionName(r)
	if err != nil {
		return err
	}
	var si proto.SessionInfo
	if err := s.WithDaemon(r.Context(), func(c *client.Client) (e error) {
		si, e = findSession(c, name)
		return e
	}); err != nil {
		return err
	}
	WriteJSON(w, http.StatusOK, si)
	return nil
}

type runRequest struct {
	Target  string            `json:"target"`
	Harness string            `json:"harness"`
	Name    string            `json:"name"`
	Profile string            `json:"profile"`
	Args    []string          `json:"args"`
	Cmd     []string          `json:"cmd"`
	Env     map[string]string `json:"env"`
}

func (s *Server) handleRunSession(w http.ResponseWriter, r *http.Request) (err error) {
	ar := s.auditOp(r, "run")
	defer ar.done(&err)
	var req runRequest
	if err := DecodeJSON(r, &req); err != nil {
		return err
	}
	if req.Target == "" {
		return errInvalid("target is required")
	}
	ar.ev.Session = req.Name
	ar.set("target", req.Target)
	ar.set("harness", req.Harness)
	admin := PrincipalFrom(r.Context()).Scope.Allows(ScopeAdmin)
	// cmd, env and args all choose what the daemon executes: admin only.
	if (len(req.Cmd) > 0 || len(req.Env) > 0 || len(req.Args) > 0) && !admin {
		return &Error{Status: http.StatusForbidden, Code: "forbidden", Message: "cmd, args and env require the admin scope"}
	}
	if len(req.Cmd) > 0 && len(req.Args) > 0 {
		return errInvalid("args cannot be combined with cmd")
	}
	if req.Harness == "" {
		req.Harness = "claude"
	}
	if !harnessRE.MatchString(req.Harness) {
		return errInvalid("invalid harness")
	}
	if !admin && !slices.Contains(s.Config.RunHarnesses, req.Harness) {
		return &Error{Status: http.StatusForbidden, Code: "forbidden",
			Message: fmt.Sprintf("harness %q is not allowed for this token (admin scope required)", req.Harness)}
	}
	if req.Name != "" && !sessionNameRE.MatchString(req.Name) {
		return errInvalid("invalid session name")
	}
	args := proto.RunArgs{Name: req.Name, Target: req.Target, Harness: req.Harness, Profile: req.Profile, Env: req.Env}
	switch {
	case len(req.Cmd) > 0:
		args.Cmd = req.Cmd
	case len(req.Args) > 0:
		args.Cmd = append([]string{req.Harness}, req.Args...)
	}
	// Relative paths are meaningless over the network: a target is an alias
	// name or an absolute directory. Alias-looking targets are resolved by the
	// daemon (Cwd "/" only anchors an alias that is not defined).
	switch {
	case strings.HasPrefix(req.Target, "/"):
	case targetRE.MatchString(req.Target) && req.Target != "." && req.Target != "..":
		args.Cwd = "/"
	default:
		return errInvalid("target must be a project alias or an absolute directory on the daemon host")
	}
	// No EnvBase: the daemon uses its own environment (run_env = "daemon").
	var si proto.SessionInfo
	if err := s.WithDaemonWrite(r.Context(), 0, func(c *client.Client) (e error) {
		si, e = c.Run(args)
		return e
	}); err != nil {
		return err
	}
	ar.ev.Session = si.Name
	w.Header().Set("Location", sessionLocation(si.Name))
	WriteJSON(w, http.StatusCreated, clipPreview(si))
	return nil
}

func (s *Server) handleRemoveSession(w http.ResponseWriter, r *http.Request) (err error) {
	ar := s.auditOp(r, "rm")
	defer ar.done(&err)
	name, err := sessionName(r)
	if err != nil {
		return err
	}
	ar.ev.Session = name
	force := false
	if v := r.URL.Query().Get("force"); v != "" {
		if force, err = strconv.ParseBool(v); err != nil {
			return errInvalid("force must be a boolean")
		}
	}
	ar.set("force", force)
	err = s.WithDaemonWrite(r.Context(), 0, func(c *client.Client) error {
		if force {
			return killAndRemove(r, c, name)
		}
		return c.Rm(name)
	})
	if err != nil {
		if client.IsCode(err, proto.CodeConflict) {
			return &Error{Status: http.StatusConflict, Code: "still_running", Message: err.Error()}
		}
		return err
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}

// killAndRemove is `kill --rm`: TERM, wait up to 3 s, KILL, remove.
func killAndRemove(r *http.Request, c *client.Client, name string) error {
	si, err := findSession(c, name)
	if err != nil {
		return err
	}
	if si.State != "exited" {
		if err := c.Kill(name, "TERM"); err != nil && !client.IsCode(err, proto.CodeExited) {
			return err
		}
		if !waitExited(r, c, name, 3*time.Second) {
			if err := c.Kill(name, "KILL"); err != nil && !client.IsCode(err, proto.CodeExited) {
				return err
			}
			if !waitExited(r, c, name, 3*time.Second) {
				return &proto.RemoteError{Code: proto.CodeTimeout, Msg: "session did not exit"}
			}
		}
	}
	return c.Rm(name)
}

func waitExited(r *http.Request, c *client.Client, name string, d time.Duration) bool {
	deadline := time.Now().Add(d)
	for {
		if si, err := findSession(c, name); err != nil || si.State == "exited" {
			return true // gone or exited
		}
		if time.Now().After(deadline) || r.Context().Err() != nil {
			return false
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func (s *Server) handleKillSession(w http.ResponseWriter, r *http.Request) (err error) {
	ar := s.auditOp(r, "kill")
	defer ar.done(&err)
	name, err := sessionName(r)
	if err != nil {
		return err
	}
	ar.ev.Session = name
	var req struct {
		Signal string `json:"signal"`
	}
	if err := decodeOptional(r, &req); err != nil {
		return err
	}
	ar.set("signal", req.Signal)
	var si proto.SessionInfo
	if err := s.WithDaemonWrite(r.Context(), 0, func(c *client.Client) error {
		if err := c.Kill(name, req.Signal); err != nil {
			return err
		}
		var e error
		si, e = findSession(c, name)
		return e
	}); err != nil {
		return err
	}
	WriteJSON(w, http.StatusAccepted, si)
	return nil
}

// ---- view ----

func (s *Server) handleView(w http.ResponseWriter, r *http.Request) error {
	name, err := sessionName(r)
	if err != nil {
		return err
	}
	q := r.URL.Query()
	a := proto.ViewArgs{Name: name, Epoch: q.Get("epoch")}
	if v := q.Get("since"); v != "" {
		if a.SinceRevision, err = strconv.ParseUint(v, 10, 64); err != nil {
			return errInvalid("since must be a non-negative integer")
		}
	}
	lines := q.Get("lines")
	if lines == "" {
		lines = q.Get("tail")
	}
	if lines != "" {
		n, err := strconv.Atoi(lines)
		if err != nil || n < 1 || n > maxViewLines {
			return errInvalid("lines must be an integer between 1 and %d", maxViewLines)
		}
		a.LinesLimit = n
	}
	if v := q.Get("from"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 0 {
			return errInvalid("from must be a non-negative integer")
		}
		a.FromLine = &n
	}
	if v := q.Get("scrollback"); v != "" {
		if a.Scrollback, err = strconv.ParseBool(v); err != nil {
			return errInvalid("scrollback must be a boolean")
		}
	}
	// If-None-Match wins over since: it carries the epoch too. The first
	// parseable tag of the list drives the daemon's not-modified shortcut; the
	// final 304 decision compares the whole list (weakly) with the current
	// ETag below.
	inm := r.Header.Get("If-None-Match")
	var inmRef *etagRef
	if inm != "" {
		tags, _ := splitETagList(inm)
		for _, t := range tags {
			if ref, ok := parseETagRef(t); ok {
				inmRef = &ref
				a.Epoch, a.SinceRevision = ref.epoch, ref.rev
				break
			}
		}
	}
	var v proto.View
	var inst string
	if err := s.WithDaemon(r.Context(), func(c *client.Client) error {
		si, e := findSession(c, name)
		if e != nil {
			return e
		}
		inst = sessionInstance(si)
		if inmRef != nil && inmRef.inst != "" && inmRef.inst != inst {
			a.Epoch, a.SinceRevision = "", 0 // another incarnation: not a base
		}
		v, e = c.View(a)
		return e
	}); err != nil {
		return err
	}
	etag := formatETag(v.Epoch, inst, v.Revision)
	w.Header().Set("ETag", etag)
	if v.NotModified {
		if inmRef != nil && etagMatches(inm, etag) {
			w.WriteHeader(http.StatusNotModified)
			return nil
		}
		WriteJSON(w, http.StatusOK, map[string]any{"unchanged": true, "revision": v.Revision, "epoch": v.Epoch})
		return nil
	}
	if inm != "" && etagMatches(inm, etag) {
		w.WriteHeader(http.StatusNotModified)
		return nil
	}
	WriteJSON(w, http.StatusOK, v)
	return nil
}

func (s *Server) handleViewAck(w http.ResponseWriter, r *http.Request) (err error) {
	ar := s.auditOp(r, "view_ack")
	defer ar.done(&err)
	name, err := sessionName(r)
	if err != nil {
		return err
	}
	ar.ev.Session = name
	var rev uint64
	if err := s.WithDaemonWrite(r.Context(), 0, func(c *client.Client) (e error) {
		rev, e = c.ViewAck(name)
		return e
	}); err != nil {
		return err
	}
	ar.set("revision", rev)
	WriteJSON(w, http.StatusOK, map[string]any{"revision": rev})
	return nil
}

// ---- act ----

type actRequest struct {
	Action   string   `json:"action"`
	Text     string   `json:"text"`
	Submit   *bool    `json:"submit"`
	OptionID string   `json:"option_id"`
	Label    string   `json:"label"`
	Revision uint64   `json:"revision"`
	Epoch    string   `json:"epoch"`
	Keys     []string `json:"keys"`
	Force    bool     `json:"force"`
}

func (s *Server) handleAct(w http.ResponseWriter, r *http.Request) (err error) {
	ar := s.auditOp(r, "act")
	defer ar.done(&err)
	name, err := sessionName(r)
	if err != nil {
		return err
	}
	ar.ev.Session = name
	var req actRequest
	if err := DecodeJSON(r, &req); err != nil {
		return err
	}
	if req.Action == "" {
		return errInvalid("action is required")
	}
	// Never the text itself: only its length (and the key names, which are
	// not free text).
	ar.set("act", req.Action)
	ar.set("option_id", req.OptionID)
	ar.set("label", req.Label)
	ar.set("keys", req.Keys)
	ar.set("text_len", utf8.RuneCountInString(req.Text))
	ar.set("force", req.Force)
	if n := utf8.RuneCountInString(req.Text); n > PromptMaxChars {
		return &Error{Status: http.StatusRequestEntityTooLarge, Code: "payload_too_large",
			Message: fmt.Sprintf("text is %d characters, the limit is %d", n, PromptMaxChars)}
	}
	// If-Match (RFC 9110 13.1.1, strong comparison): "*" = no precondition; a
	// list holds acceptable view ETags (matched against the current view).
	var want []etagRef
	ifMatch := r.Header.Get("If-Match")
	if ifMatch != "" {
		tags, star := splitETagList(ifMatch)
		if !star {
			for _, t := range tags {
				ref, ok := parseETagRef(t)
				if !ok {
					return errInvalid("If-Match must be \"*\" or view ETags (\"e<epoch>-i<instance>-r<revision>\")")
				}
				want = append(want, ref)
			}
			if len(want) == 0 {
				return errInvalid("If-Match is empty")
			}
		}
	}
	var (
		res     proto.ActResult
		precond *Error
	)
	err = s.WithDaemonWrite(r.Context(), s.actDeadline(), func(c *client.Client) (e error) {
		if len(want) > 0 {
			ep, rev, pe, e2 := resolveIfMatch(c, name, want)
			if e2 != nil {
				return e2
			}
			if pe != nil {
				precond = pe
				return nil
			}
			req.Epoch, req.Revision = ep, rev
		}
		res, e = c.Act(proto.ActArgs{
			Name: name, Action: req.Action, Text: req.Text, Submit: req.Submit, OptionID: req.OptionID,
			Label: req.Label, Revision: req.Revision, Epoch: req.Epoch, Keys: req.Keys, Force: req.Force,
		})
		return e
	})
	if precond != nil {
		if precond.Details != nil {
			if v, ok := precond.Details["etag"].(string); ok {
				w.Header().Set("ETag", v)
				delete(precond.Details, "etag")
			}
		}
		return precond
	}
	if err != nil {
		return s.actError(w, r, name, err, len(want) > 0, req)
	}
	WriteJSON(w, http.StatusOK, struct {
		Action string `json:"action"`
		proto.ActResult
	}{req.Action, res})
	return nil
}

// resolveIfMatch checks the If-Match ETags against the session's current view
// and returns the epoch/revision to send to the daemon. A stale or foreign
// ETag (other session incarnation, moved revision) is 412 with the fresh view.
func resolveIfMatch(c *client.Client, name string, want []etagRef) (epoch string, rev uint64, pe *Error, err error) {
	si, err := findSession(c, name)
	if err != nil {
		return "", 0, nil, err
	}
	inst := sessionInstance(si)
	if len(want) == 1 && !want[0].weak && (want[0].inst == "" || want[0].inst == inst) {
		// Single, current-incarnation tag: the daemon checks epoch+revision
		// atomically with the action (and answers with the fresh view).
		return want[0].epoch, want[0].rev, nil, nil
	}
	cur, err := c.View(proto.ViewArgs{Name: name, LinesLimit: 100})
	if err != nil {
		return "", 0, nil, err
	}
	for _, t := range want {
		if !t.weak && (t.inst == "" || t.inst == inst) && t.epoch == cur.Epoch && t.rev == cur.Revision {
			return t.epoch, t.rev, nil, nil
		}
	}
	pe = &Error{Status: http.StatusPreconditionFailed, Code: "precondition_failed",
		Message: "the view changed since the If-Match ETag",
		Details: map[string]any{"revision": cur.Revision, "view": cur, "etag": formatETag(cur.Epoch, inst, cur.Revision)}}
	return "", 0, pe, nil
}

// actError maps a refused act. A conflict carries the fresh view (ETag header
// and details.{revision,view}); with If-Match and a view that moved on it is
// 412 precondition_failed. An act whose outcome is unknown (timeout, broken
// connection, client gone) is returned as outcome_unknown with a fresh view
// when one can be fetched, so the client can see what the terminal shows now.
func (s *Server) actError(w http.ResponseWriter, r *http.Request, name string, err error, ifMatch bool, req actRequest) error {
	var ae *Error
	if errors.As(err, &ae) && ae.Code == "outcome_unknown" {
		ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), 5*time.Second)
		defer cancel()
		var v proto.View
		var inst string
		if s.WithDaemon(ctx, func(c *client.Client) error {
			si, e := findSession(c, name)
			if e != nil {
				return e
			}
			inst = sessionInstance(si)
			v, e = c.View(proto.ViewArgs{Name: name, LinesLimit: 100})
			return e
		}) == nil {
			w.Header().Set("ETag", formatETag(v.Epoch, inst, v.Revision))
			ae.Details = map[string]any{"hint": ae.Details["hint"], "revision": v.Revision, "view": v}
		}
		return ae
	}
	var re *proto.RemoteError
	if !errors.As(err, &re) || re.Code != proto.CodeConflict || len(re.Data) == 0 {
		return err
	}
	var cf proto.ActConflict
	if json.Unmarshal(re.Data, &cf) != nil || cf.View.Name == "" && cf.Revision == 0 {
		return err
	}
	inst := s.currentInstance(r.Context(), name)
	w.Header().Set("ETag", formatETag(cf.View.Epoch, inst, cf.Revision))
	e := &Error{Status: http.StatusConflict, Code: "conflict", Message: re.Msg,
		Details: map[string]any{"revision": cf.Revision, "view": cf.View}}
	if ifMatch && (cf.View.Epoch != req.Epoch || cf.Revision != req.Revision) {
		e.Status, e.Code = http.StatusPreconditionFailed, "precondition_failed"
	}
	return e
}
