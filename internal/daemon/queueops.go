package daemon

import (
	"context"
	"errors"

	"github.com/digiogithub/agentd/internal/mailbox"
	"github.com/digiogithub/agentd/internal/proto"
)

// Shared work queues (AGENTS.md, "Mailbox states", "Identity"): anyone the
// [acl] lets message "queue:<name>" posts with send; a session the [claim]
// table allows takes the oldest (or a given) item with claim, after which it
// is an ordinary message to that session.

// claim assigns an unclaimed queue item to the calling session.
func (s *Server) claim(ctx context.Context, who caller, a proto.ClaimArgs) (proto.Message, error) {
	if !mailbox.ValidQueueName(a.Queue) {
		return proto.Message{}, proto.Errorf(proto.CodeInvalid, "claim: bad queue name %q (want [a-zA-Z0-9._-]{1,64}, without queue:)", a.Queue)
	}
	if a.ID < 0 {
		return proto.Message{}, proto.Errorf(proto.CodeInvalid, "claim: negative id")
	}
	if !who.session {
		return proto.Message{}, proto.Errorf(proto.CodeForbidden, "claim: only sessions claim queue items (%q is not a session; send the task to a session directly instead)", who.id)
	}
	if !s.Cfg.ClaimAllowed(a.Queue, who.id) {
		return proto.Message{}, proto.Errorf(proto.CodeForbidden, "claim: %q is not allowed to claim from queue %q", who.id, a.Queue)
	}
	m, err := s.Store.Claim(ctx, mailbox.ClaimOptions{Queue: a.Queue, Claimer: who.id, ID: a.ID, Inject: a.Inject})
	switch {
	case errors.Is(err, mailbox.ErrNotFound):
		if a.ID > 0 {
			return proto.Message{}, proto.Errorf(proto.CodeNotFound, "claim: message %d is not an item of queue %q", a.ID, a.Queue)
		}
		return proto.Message{}, proto.Errorf(proto.CodeNotFound, "claim: queue %q has no unclaimed item", a.Queue)
	case errors.Is(err, mailbox.ErrClaimed):
		return proto.Message{}, proto.Errorf(proto.CodeConflict, "claim: message %d of queue %q was already claimed", a.ID, a.Queue)
	case err != nil:
		return proto.Message{}, err
	}
	s.Log.Info("queue item claimed", "id", m.ID, "queue", m.Queue, "by", who.id, "from", m.Sender,
		"inject", a.Inject, "via", who.via)
	s.publishQueueMail(proto.EvMailClaimed, m, who.id)
	if a.Inject {
		s.kick(m.Recipient)
	} else {
		s.publishMail(proto.EvMailDelivered, m.ID, m.Sender, m.Recipient)
	}
	return toWire(m), nil
}

// queueList lists a queue's unclaimed items, or every non-empty queue.
// Humans may list any queue; a session only queues it may claim from (and,
// without a name, sees only those). Bodies are left out unless asked for.
func (s *Server) queueList(ctx context.Context, who caller, a proto.QueueArgs) (proto.QueueResult, error) {
	if a.Name == "" {
		if a.SinceDigest != "" {
			return proto.QueueResult{}, proto.Errorf(proto.CodeInvalid, "queue: since_digest needs a queue name")
		}
		qs, err := s.Store.Queues(ctx)
		if err != nil {
			return proto.QueueResult{}, err
		}
		res := proto.QueueResult{Queues: []proto.QueueCount{}}
		for _, q := range qs {
			if who.session && !s.Cfg.ClaimAllowed(q.Name, who.id) {
				continue
			}
			res.Queues = append(res.Queues, proto.QueueCount{Name: q.Name, Unclaimed: q.Unclaimed})
		}
		return res, nil
	}
	if !mailbox.ValidQueueName(a.Name) {
		return proto.QueueResult{}, proto.Errorf(proto.CodeInvalid, "queue: bad queue name %q (want [a-zA-Z0-9._-]{1,64}, without queue:)", a.Name)
	}
	if who.session && !s.Cfg.ClaimAllowed(a.Name, who.id) {
		return proto.QueueResult{}, proto.Errorf(proto.CodeForbidden, "queue: %q may not claim from queue %q, so it may not list it", who.id, a.Name)
	}
	if a.SinceDigest != "" {
		d, err := s.Store.QueueDigest(ctx, a.Name)
		if err != nil {
			return proto.QueueResult{}, err
		}
		if d == a.SinceDigest {
			return proto.QueueResult{Name: a.Name, Digest: d, NotModified: true}, nil
		}
	}
	items, err := s.Store.QueueItems(ctx, a.Name)
	if err != nil {
		return proto.QueueResult{}, err
	}
	res := proto.QueueResult{Name: a.Name, Items: make([]proto.Message, 0, len(items)), Digest: mailbox.Digest(items)}
	for _, m := range items {
		w := toWire(m)
		if !a.Bodies {
			w.Body = ""
		}
		res.Items = append(res.Items, w)
	}
	return res, nil
}
