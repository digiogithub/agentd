package httpapi

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/digiogithub/agentd/internal/client"
	"github.com/digiogithub/agentd/internal/proto"
)

// handleTerminal is the L0 raw terminal snapshot (AGED-US-0020): the daemon
// `dump` as JSON (or text/plain). It is read-only and never resizes the PTY:
// the snapshot is rendered at the session's own size, clients scroll.
func (s *Server) handleTerminal(w http.ResponseWriter, r *http.Request) error {
	name, err := sessionName(r)
	if err != nil {
		return err
	}
	q := r.URL.Query()
	format := q.Get("format")
	switch format {
	case "":
		format = "text"
	case "text", "ansi":
	default:
		return errInvalid("format must be text or ansi")
	}
	boolParam := func(key string) (bool, error) {
		v := q.Get(key)
		if v == "" {
			return false, nil
		}
		b, err := strconv.ParseBool(v)
		if err != nil {
			return false, errInvalid("%s must be a boolean", key)
		}
		return b, nil
	}
	scrollback, err := boolParam("scrollback")
	if err != nil {
		return err
	}
	ghost, err := boolParam("ghost")
	if err != nil {
		return err
	}
	if format == "ansi" && scrollback {
		return errInvalid("format=ansi cannot be combined with scrollback=true")
	}
	var (
		res  proto.DumpResult
		size proto.SessionInfo
	)
	if err := s.WithDaemon(r.Context(), func(c *client.Client) (e error) {
		res, e = c.DumpResult(proto.DumpArgs{Name: name, Scrollback: scrollback, ANSI: format == "ansi", Ghost: ghost})
		if e != nil {
			return e
		}
		size, e = findSession(c, name)
		return e
	}); err != nil {
		return err
	}
	w.Header().Set("Cache-Control", "no-store")
	if accept := r.Header.Get("Accept"); strings.Contains(accept, "text/plain") && !strings.Contains(accept, "application/json") {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = w.Write([]byte(res.Text))
		return nil
	}
	WriteJSON(w, http.StatusOK, map[string]any{
		"format": format, "text": res.Text, "ghost": res.Ghost, "ghost_hidden": res.GhostHidden,
		"cols": size.Cols, "rows": size.Rows,
	})
	return nil
}
