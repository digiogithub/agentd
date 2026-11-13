package httpapi

import (
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"unicode/utf8"

	"github.com/digiogithub/agentd/internal/client"
	"github.com/digiogithub/agentd/internal/proto"
)

// The mailbox endpoints act as the gateway's identity (human:<user>, resolved
// by the daemon from the socket peer): there is no sender argument.

const (
	defaultMailboxLimit = 50
	maxMailboxLimit     = 200
)

func messageID(r *http.Request) (int64, error) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id < 1 {
		return 0, errInvalid("invalid message id")
	}
	return id, nil
}

// mailboxParam validates ?mailbox=. Looking into another mailbox than the
// gateway's own is a peek into somebody else's messages: admin scope only.
func mailboxParam(r *http.Request) (string, error) {
	m := r.URL.Query().Get("mailbox")
	if m == "" {
		return "", nil
	}
	if !recipientRE.MatchString(m) {
		return "", errInvalid("invalid mailbox")
	}
	if p := PrincipalFrom(r.Context()); p == nil || !p.Scope.Allows(ScopeAdmin) {
		return "", &Error{Status: http.StatusForbidden, Code: "forbidden", Message: "the mailbox parameter requires the admin scope"}
	}
	return m, nil
}

func (s *Server) handleListMailbox(w http.ResponseWriter, r *http.Request) error {
	q := r.URL.Query()
	all := false
	var err error
	if v := q.Get("all"); v != "" {
		if all, err = strconv.ParseBool(v); err != nil {
			return errInvalid("all must be a boolean")
		}
	}
	limit := defaultMailboxLimit
	if v := q.Get("limit"); v != "" {
		if limit, err = strconv.Atoi(v); err != nil || limit < 1 || limit > maxMailboxLimit {
			return errInvalid("limit must be an integer between 1 and %d", maxMailboxLimit)
		}
	}
	var before int64
	if v := q.Get("before"); v != "" {
		if before, err = strconv.ParseInt(v, 10, 64); err != nil || before < 1 {
			return errInvalid("before must be a positive message id")
		}
	}
	box, err := mailboxParam(r)
	if err != nil {
		return err
	}
	var msgs []proto.Message
	if err := s.WithDaemon(r.Context(), func(c *client.Client) (e error) {
		msgs, e = c.Inbox(box, all)
		return e
	}); err != nil {
		return err
	}
	sort.Slice(msgs, func(i, j int) bool { return msgs[i].ID > msgs[j].ID }) // newest first
	page := make([]proto.Message, 0, min(limit, len(msgs)))
	more := false
	for _, m := range msgs {
		if before > 0 && m.ID >= before {
			continue
		}
		if len(page) == limit {
			more = true
			break
		}
		page = append(page, m)
	}
	out := map[string]any{"messages": page}
	if more {
		out["next_before"] = page[len(page)-1].ID
	}
	WriteJSON(w, http.StatusOK, out)
	return nil
}

// handleGetMessage shows one message without marking it read. Another
// mailbox is peeked with `read {as}`; the gateway's own mailbox has no peek in
// the daemon (as == self counts as reading), so it is looked up through
// `inbox {all}`, which at most moves queued to delivered, like listing does.
func (s *Server) handleGetMessage(w http.ResponseWriter, r *http.Request) error {
	id, err := messageID(r)
	if err != nil {
		return err
	}
	box, err := mailboxParam(r)
	if err != nil {
		return err
	}
	var msg proto.Message
	err = s.WithDaemon(r.Context(), func(c *client.Client) error {
		self, err := c.Whoami()
		if err != nil {
			return err
		}
		if box != "" && box != self.Identity {
			msg, err = c.Read(box, id)
			return err
		}
		msgs, err := c.Inbox("", true)
		if err != nil {
			return err
		}
		for _, m := range msgs {
			if m.ID == id {
				msg = m
				return nil
			}
		}
		return notFoundErr(fmt.Sprintf("no message #%d in %s", id, self.Identity))
	})
	if err != nil {
		return err
	}
	WriteJSON(w, http.StatusOK, msg)
	return nil
}

func (s *Server) handleReadMessage(w http.ResponseWriter, r *http.Request) error {
	id, err := messageID(r)
	if err != nil {
		return err
	}
	var msg proto.Message
	if err := s.WithDaemonWrite(r.Context(), 0, func(c *client.Client) (e error) {
		msg, e = c.Read("", id)
		return e
	}); err != nil {
		return err
	}
	WriteJSON(w, http.StatusOK, msg)
	return nil
}

type sendRequest struct {
	To      string `json:"to"`
	Subject string `json:"subject"`
	Body    string `json:"body"`
	ReplyTo *int64 `json:"reply_to"`
}

func (s *Server) handleSendMessage(w http.ResponseWriter, r *http.Request) (err error) {
	ar := s.auditOp(r, "send")
	defer ar.done(&err)
	var req sendRequest
	if err := DecodeJSON(r, &req); err != nil {
		return err
	}
	if !recipientRE.MatchString(req.To) {
		return errInvalid("to must be a session name or human:<name>")
	}
	if req.Body == "" {
		return errInvalid("body is required")
	}
	// Ids and sizes only: never the subject or the body.
	ar.set("to", req.To)
	ar.set("subject_len", utf8.RuneCountInString(req.Subject))
	ar.set("body_len", utf8.RuneCountInString(req.Body))
	if req.ReplyTo != nil {
		ar.set("reply_to", *req.ReplyTo)
	}
	var msg proto.Message
	if err := s.WithDaemonWrite(r.Context(), 0, func(c *client.Client) (e error) {
		msg, e = c.Send(proto.SendArgs{To: req.To, Subject: req.Subject, Body: req.Body, ReplyTo: req.ReplyTo})
		return e
	}); err != nil {
		return err
	}
	ar.set("id", msg.ID)
	WriteJSON(w, http.StatusCreated, msg)
	return nil
}

func (s *Server) handleReplyMessage(w http.ResponseWriter, r *http.Request) (err error) {
	ar := s.auditOp(r, "reply")
	defer ar.done(&err)
	id, err := messageID(r)
	if err != nil {
		return err
	}
	ar.set("to_message", id)
	var req struct {
		Body string `json:"body"`
	}
	if err := DecodeJSON(r, &req); err != nil {
		return err
	}
	if req.Body == "" {
		return errInvalid("body is required")
	}
	ar.set("body_len", utf8.RuneCountInString(req.Body))
	var msg proto.Message
	if err := s.WithDaemonWrite(r.Context(), 0, func(c *client.Client) (e error) {
		msg, e = c.Reply(id, req.Body)
		return e
	}); err != nil {
		return err
	}
	ar.set("id", msg.ID)
	WriteJSON(w, http.StatusCreated, msg)
	return nil
}
