package daemon

import (
	"log/slog"

	"github.com/digiogithub/agentd/internal/config"
	"github.com/digiogithub/agentd/internal/delivery"
	"github.com/digiogithub/agentd/internal/mailbox"
	"github.com/digiogithub/agentd/internal/proto"
)

// Lookup implements delivery.Registry: it resolves a session name to the
// session and the harness profile it was started with.
func (s *Server) Lookup(name string) (delivery.Target, config.Profile, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok := s.sessions[name]
	if !ok {
		return nil, config.Profile{}, false
	}
	return e.sess, e.profile, true
}

var _ delivery.Registry = (*Server)(nil)

// NewDeliveryLoop returns a delivery loop bound to the server's store and
// registry. Assign it to Server.Delivery before Serve.
func (s *Server) NewDeliveryLoop(log *slog.Logger) *delivery.Loop {
	return &delivery.Loop{
		Store: s.Store, Reg: s, Log: log,
		OnDelivered: func(m mailbox.Message) { s.publishMail(proto.EvMailDelivered, m.ID, m.Sender, m.Recipient) },
	}
}
