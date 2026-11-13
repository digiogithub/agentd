package daemon

import "github.com/digiogithub/agentd/internal/session"

// SessionTokenForTest returns session name's AGENTD_TOKEN, so external tests
// can authenticate as a session without running inside it.
func (s *Server) SessionTokenForTest(name string) string {
	e, err := s.lookup(name)
	if err != nil {
		return ""
	}
	return e.sess.Spec().Env[EnvToken]
}

// SetRetentionHookForTest sets a callback run after each retention purge.
// Call it before Serve.
func (s *Server) SetRetentionHookForTest(f func(purged int, err error)) {
	s.retentionHook = f
}

// ViewDoneForTest returns the channel closed when session name's view
// watcher goroutine has ended (nil when the session is unknown).
func (s *Server) ViewDoneForTest(name string) <-chan struct{} {
	e, err := s.lookup(name)
	if err != nil || e.view == nil {
		return nil
	}
	return e.view.done
}

// PublishForTest publishes an event on the bus.
func (s *Server) PublishForTest(kind, session string, data any) {
	s.bus.publish(kind, session, "", "", data)
}

// BusSeqForTest is the last published seq.
func (s *Server) BusSeqForTest() uint64 { return s.bus.lastSeq() }

// BusSubscribersForTest is the number of live subscribers.
func (s *Server) BusSubscribersForTest() int {
	s.bus.mu.Lock()
	defer s.bus.mu.Unlock()
	return len(s.bus.subs)
}

// SessionForTest returns session name's *session.Session (nil if unknown).
func (s *Server) SessionForTest(name string) *session.Session {
	e, err := s.lookup(name)
	if err != nil {
		return nil
	}
	return e.sess
}
