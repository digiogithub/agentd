package daemon

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
