package daemon

// The hc socket listener ([herdr] listen, Phase 4 of the Herdr compatibility
// plan): the daemon serves the herdr-core client protocol (internal/hcompat)
// on a unix socket at herdr-core's place, for clients that open the core's
// socket instead of running `hc connect` (Whip over SSH direct-streamlocal,
// herdr-mobile-relay's hc backend).
//
// It lives in the daemon, not in a separate `agentd hc serve` process,
// because of identity: hcompat dials the daemon as the process that runs it,
// so whoever is served gets that process's privileges. Inside the daemon
// the translator's own connections resolve as human:<user> (the daemon's pid
// is never a session), and every hc socket peer is checked with the same
// kernel-verified ancestry the daemon uses for its own socket before it is
// served: another uid, an unknown pid, a process running inside a session
// or one carrying AGENTD_TOKEN is refused (never served, not even read-only:
// an agent must not get the user's view or hands through this socket).

import (
	"fmt"
	"net"
	"os"
	"sync"

	"github.com/digiogithub/agentd/internal/client"
	"github.com/digiogithub/agentd/internal/config"
	"github.com/digiogithub/agentd/internal/hcompat"
	"github.com/digiogithub/agentd/internal/proto"
)

// hcListen is the daemon's hc listener state.
type hcListen struct {
	mu     sync.Mutex
	l      *hcompat.Listener
	paths  config.HCPaths
	reason string // why it is not listening
}

// hcEnabled reports whether config allows the listener.
func (s *Server) hcEnabled() bool { return s.Cfg.Herdr.On() && s.Cfg.Herdr.Listen }

// startHC binds the hc socket when config allows it and it is not bound yet.
// Failures (a live herdr-core owns the place, bind errors) are logged and
// reported by op hc_listener; the daemon keeps running either way.
func (s *Server) startHC() {
	h := &s.hc
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.l != nil {
		return
	}
	if !s.hcEnabled() {
		h.reason = "disabled ([herdr] listen = false)"
		if !s.Cfg.Herdr.On() {
			h.reason = "disabled ([herdr] enabled = false)"
		}
		return
	}
	s.mu.Lock()
	closing := s.closing
	s.mu.Unlock()
	if closing {
		h.reason = "daemon shutting down"
		return
	}
	paths, err := s.Cfg.Herdr.Paths(nil)
	if err != nil {
		h.reason = err.Error()
		s.Log.Warn("hc listener", "err", err)
		return
	}
	h.paths = paths
	idPath, err := config.HCCoreIDPath()
	if err == nil {
		var id string
		if id, err = hcompat.LoadCoreID(idPath); err == nil {
			host, _ := os.Hostname()
			srv := &hcompat.Server{
				Dial:     func() (*client.Client, error) { return client.Dial(s.socketPath()) },
				Core:     hcompat.CoreInfo{ID: id, Name: host},
				PID:      os.Getpid(),
				Started:  s.started,
				EnvBase:  os.Environ(),
				ReadOnly: s.Cfg.Herdr.ReadOnly,
				AllowRun: s.Cfg.Herdr.RunAllowed,
				Log:      s.Log.With("component", "hc"),
			}
			l := &hcompat.Listener{
				Server: srv,
				Socket: paths.Socket,
				Lock:   paths.Lock,
				Admit:  s.admitHC,
				Log:    s.Log.With("component", "hc"),
			}
			if err = l.Listen(); err == nil {
				h.l, h.reason = l, ""
				go l.Serve()
				s.Log.Info("hc listener", "socket", paths.Socket, "lock", paths.Lock)
				return
			}
		}
	}
	h.reason = err.Error()
	s.Log.Warn("hc listener not started", "socket", paths.Socket, "err", err)
}

// stopHC closes the listener (its connections, socket and core.lock). It
// reports whether one was running.
func (s *Server) stopHC(reason string) bool {
	h := &s.hc
	h.mu.Lock()
	l := h.l
	h.l = nil
	h.reason = reason
	h.mu.Unlock()
	if l == nil {
		return false
	}
	l.Close()
	s.Log.Info("hc listener stopped", "socket", l.Socket)
	return true
}

// hcListenerOp answers op hc_listener (humans only).
func (s *Server) hcListenerOp(who caller, a proto.HCListenArgs) (proto.HCListenResult, error) {
	if !who.human() {
		return proto.HCListenResult{}, proto.Errorf(proto.CodeForbidden, "hc_listener: humans only (session %q)", who.id)
	}
	var was bool
	switch a.Action {
	case "", proto.HCListenStatus:
	case proto.HCListenStart:
		s.startHC()
	case proto.HCListenStop:
		was = s.stopHC("stopped (hc server stop)")
	default:
		return proto.HCListenResult{}, proto.Errorf(proto.CodeInvalid, "hc_listener: unknown action %q (status, start, stop)", a.Action)
	}
	h := &s.hc
	h.mu.Lock()
	defer h.mu.Unlock()
	r := proto.HCListenResult{
		Enabled:      s.hcEnabled(),
		Listening:    h.l != nil,
		WasListening: was,
		Session:      s.Cfg.Herdr.SessionName(),
		Reason:       h.reason,
		PID:          os.Getpid(),
		StartedAt:    s.started,
	}
	if h.l != nil {
		r.Since = h.l.Since()
	}
	p := h.paths
	if p.Socket == "" {
		p, _ = s.Cfg.Herdr.Paths(nil)
	}
	r.Socket, r.Lock = p.Socket, p.Lock
	return r, nil
}

// admitHC decides whether an hc socket connection is served.
func (s *Server) admitHC(conn net.Conn) error {
	p, err := peerOf(conn)
	if err != nil {
		return fmt.Errorf("cannot verify the peer: %v", err)
	}
	return s.admitHCPeer(p)
}

// admitHCPeer refuses another uid, an unknown pid, a process inside a
// running session (ancestry) and a process that carries AGENTD_TOKEN (left
// a session's process tree, best effort, Linux only). The daemon's own pid
// (never a session) is admitted.
func (s *Server) admitHCPeer(p peerInfo) error {
	if p.uid != os.Getuid() {
		return fmt.Errorf("peer uid %d is not %d", p.uid, os.Getuid())
	}
	if p.pid == os.Getpid() {
		return nil
	}
	if p.pid <= 0 {
		return fmt.Errorf("the peer pid is unknown, so it cannot be checked against agentd sessions")
	}
	if name, ok := s.sessionByAncestry(p.pid); ok {
		return fmt.Errorf("agentd session %q may not use the hc socket (it would act as the user); "+
			"sessions use the agentd CLI or MCP tools", name)
	}
	if procEnvSet(p.pid, EnvToken) {
		return fmt.Errorf("the peer carries %s (started from an agentd session) and may not use the hc socket", EnvToken)
	}
	return nil
}
