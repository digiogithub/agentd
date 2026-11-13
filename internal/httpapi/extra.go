package httpapi

import "errors"

// AddRoute registers an additional route (e.g. the unauthenticated
// POST /v1/pair of internal/apitoken, mounted by cmd/agentd). It must be
// called before the handler is built (before Handler, Routes, Serve or
// WrapHandler).
func (s *Server) AddRoute(rt Route) error {
	if s.routes != nil {
		return errors.New("httpapi: AddRoute after the handler was built")
	}
	s.extra = append(s.extra, rt)
	return nil
}
