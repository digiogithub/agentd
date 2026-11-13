package httpapi

import "net/http"

// WrapHandler wraps the server's complete handler (outermost layer). It must
// be called before Serve/Handler is used for serving, never concurrently with
// requests. cmd/agentd uses it to add the device-token guard (rejects tokens
// in the query string, cancels the requests of a revoked device so open SSE
// streams close within ~1 s; AGED-US-0021).
func (s *Server) WrapHandler(mw func(http.Handler) http.Handler) {
	s.init()
	s.handler = mw(s.handler)
}
