package daemon

import (
	"context"
	"fmt"
	"time"

	"github.com/digiogithub/agentd/internal/mailbox"
	"github.com/digiogithub/agentd/internal/proto"
)

// Identity-aware op handlers: type and the mailbox ops. who is the caller as
// resolved by resolveCaller (identity.go); clients cannot choose it.

// typeText types raw text into a session. Humans may type into any session;
// an agent only into its own unless [security] agent_type = true.
func (s *Server) typeText(who caller, a proto.TypeArgs) error {
	if who.session && who.id != a.Name && !s.Cfg.Security.AgentType {
		return proto.Errorf(proto.CodeForbidden, "type: session %q may not type into session %q "+
			"(agents message each other with send; set [security] agent_type = true to allow)", who.id, a.Name)
	}
	e, err := s.lookup(a.Name)
	if err != nil {
		return err
	}
	if a.Text != "" {
		if _, err := e.sess.Write([]byte(a.Text), false); err != nil {
			return err
		}
	}
	if a.Submit {
		if a.Text != "" {
			time.Sleep(e.profile.SubmitDelay())
		}
		if _, err := e.sess.Write([]byte(e.profile.Submit), false); err != nil {
			return err
		}
	}
	return nil
}

// isSession reports whether name is a registered session (running or exited).
func (s *Server) isSession(name string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.sessions[name]
	return ok
}

// knownRecipient reports whether to can receive messages: a registered
// session (running or exited) or a valid human address.
func (s *Server) knownRecipient(to string) bool {
	return validHumanAddr(to) || s.isSession(to)
}

func (s *Server) send(ctx context.Context, who caller, a proto.SendArgs) (proto.Message, error) {
	if a.To == "" {
		return proto.Message{}, proto.Errorf(proto.CodeInvalid, "send: missing recipient")
	}
	if !s.knownRecipient(a.To) {
		return proto.Message{}, proto.Errorf(proto.CodeInvalid, "send: unknown recipient %q (want a session name or human:<name> with [a-zA-Z0-9._-])", a.To)
	}
	if !s.Cfg.Allowed(a.To, who.id) {
		return proto.Message{}, proto.Errorf(proto.CodeForbidden, "send: %q is not allowed to message %q", who.id, a.To)
	}
	m, err := s.Store.Send(ctx, mailbox.Message{
		Sender: who.id, Recipient: a.To, Subject: a.Subject, Body: a.Body, ReplyTo: a.ReplyTo,
	})
	if err != nil {
		return proto.Message{}, err
	}
	s.Log.Info("message queued", "id", m.ID, "from", m.Sender, "to", m.Recipient, "via", who.via)
	s.publishMail(proto.EvMailQueued, m.ID, m.Sender, m.Recipient)
	s.kick(m.Recipient)
	return toWire(m), nil
}

// mailboxOf returns the mailbox an inbox/read request addresses and whether
// it is the caller's own. Only humans may address another mailbox (as).
func (s *Server) mailboxOf(op string, who caller, as string) (string, bool, error) {
	if as == "" || as == who.id {
		return who.id, true, nil
	}
	if !who.human() {
		return "", false, proto.Errorf(proto.CodeForbidden, "%s: session %q can only access its own mailbox", op, who.id)
	}
	if !s.knownRecipient(as) {
		return "", false, proto.Errorf(proto.CodeInvalid, "%s: unknown mailbox %q", op, as)
	}
	return as, false, nil
}

// inbox lists a mailbox. Listing one's own mailbox counts as fetching: queued
// messages it returns move to delivered, so they are not injected afterwards.
// A human listing another mailbox (as) only peeks: no state changes.
func (s *Server) inbox(ctx context.Context, who caller, a proto.InboxArgs) (proto.InboxResult, error) {
	box, own, err := s.mailboxOf("inbox", who, a.As)
	if err != nil {
		return proto.InboxResult{}, err
	}
	msgs, err := s.Store.Inbox(ctx, box, a.All)
	if err != nil {
		return proto.InboxResult{}, err
	}
	res := proto.InboxResult{Messages: make([]proto.Message, 0, len(msgs))}
	for _, m := range msgs {
		if own && m.State == mailbox.StateQueued {
			if err := s.Store.MarkDelivered(ctx, m.ID); err == nil {
				if fresh, err := s.Store.Get(ctx, m.ID); err == nil {
					m = fresh
				}
				s.publishMail(proto.EvMailDelivered, m.ID, m.Sender, m.Recipient)
			}
		}
		res.Messages = append(res.Messages, toWire(m))
	}
	return res, nil
}

// read shows one message of the caller's mailbox and marks it read. A human
// reading another mailbox (as) gets the message without any state change.
func (s *Server) read(ctx context.Context, who caller, a proto.ReadArgs) (proto.Message, error) {
	box, own, err := s.mailboxOf("read", who, a.As)
	if err != nil {
		return proto.Message{}, err
	}
	if own {
		m, err := s.Store.MarkRead(ctx, box, a.ID)
		if err != nil {
			return proto.Message{}, mailboxErr(err, a.ID, box)
		}
		s.publishMail(proto.EvMailRead, m.ID, m.Sender, m.Recipient)
		return toWire(m), nil
	}
	m, err := s.Store.Get(ctx, a.ID)
	if err != nil {
		return proto.Message{}, mailboxErr(err, a.ID, box)
	}
	if m.Recipient != box {
		return proto.Message{}, mailboxErr(mailbox.ErrForbidden, a.ID, box)
	}
	return toWire(m), nil
}

// reply answers message a.ID as the caller, who must be its recipient. The
// reply goes to the original sender; if that sender is no longer a known
// mailbox (e.g. a removed session, or a malformed sender stored before
// identities were verified) the reply is still stored and a warning returned.
func (s *Server) reply(ctx context.Context, who caller, a proto.ReplyArgs) (proto.Message, error) {
	orig, err := s.Store.Get(ctx, a.ID)
	if err != nil {
		return proto.Message{}, mailboxErr(err, a.ID, who.id)
	}
	if orig.Recipient == who.id && !s.Cfg.Allowed(orig.Sender, who.id) {
		return proto.Message{}, proto.Errorf(proto.CodeForbidden, "reply: %q is not allowed to message %q", who.id, orig.Sender)
	}
	m, err := s.Store.Reply(ctx, who.id, a.ID, a.Body)
	if err != nil {
		return proto.Message{}, mailboxErr(err, a.ID, who.id)
	}
	s.Log.Info("reply queued", "id", m.ID, "from", m.Sender, "to", m.Recipient, "reply_to", a.ID, "via", who.via)
	s.publishMail(proto.EvMailQueued, m.ID, m.Sender, m.Recipient)
	s.kick(m.Recipient)
	w := toWire(m)
	if !s.knownRecipient(m.Recipient) {
		w.Warning = fmt.Sprintf("the original sender %q is not a known session or human address: the reply is stored but may never be delivered", m.Recipient)
		s.Log.Warn("reply to unknown sender", "id", m.ID, "to", m.Recipient)
	}
	return w, nil
}
