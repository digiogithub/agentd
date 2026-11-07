package daemon

// Daemon-verified caller identity (AGENTS.md, "Identity").
//
// The daemon never trusts a client-supplied sender. For every request that
// needs an identity it resolves the caller itself:
//
//  1. Process ancestry: the peer pid (SO_PEERCRED on Linux, LOCAL_PEERPID on
//     macOS) and its ancestors, walked through the kernel's process table up
//     to maxAncestry levels or pid 1. If one of them is the child process of a
//     running session, the caller is that session. This is kernel-verified
//     and works for harnesses that start MCP servers with a filtered
//     environment (no AGENTD_* variables).
//  2. Token: if the ancestry does not identify a session and the request
//     carries auth.token, a running session whose AGENTD_TOKEN matches is the
//     caller (covers processes that left the session's process tree, e.g.
//     daemonized helpers, and platforms without peer pids). A token that
//     matches a different session than the ancestry is rejected.
//  3. Otherwise the caller is "human:<user>" for the peer uid.
//
// auth.id (AGENTD_ID) is only a hint: if it is set and differs from the
// resolved identity the request is rejected, so a misconfigured or spoofed
// AGENTD_ID never silently changes who a message is from.

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"os/user"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/digiogithub/agentd/internal/proto"
	"github.com/digiogithub/agentd/internal/session"
)

// EnvToken is the child environment variable holding the session's token.
const EnvToken = "AGENTD_TOKEN"

// maxAncestry bounds the process ancestry walk.
const maxAncestry = 64

// startingWait is how long resolution waits for a session that is still
// being registered when the caller's AGENTD_ID hint names it.
const startingWait = 2 * time.Second

// Resolution methods (proto.WhoamiResult.Via).
const (
	viaProcess = "process"
	viaToken   = "token"
	viaUID     = "uid"
)

// caller is a resolved identity.
type caller struct {
	id      string // session name or human:<user>
	session bool
	via     string
}

func (c caller) human() bool { return !c.session }

// newToken returns a fresh session token: 32 random bytes, hex encoded.
func newToken() string {
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic("agentd: crypto/rand failed: " + err.Error())
	}
	return hex.EncodeToString(b[:])
}

// validHumanName reports whether name is a valid <name> in "human:<name>".
func validHumanName(name string) bool {
	if name == "" || len(name) > 64 {
		return false
	}
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '.', r == '_', r == '-':
		default:
			return false
		}
	}
	return true
}

// validHumanAddr reports whether addr is "human:<name>" with a valid name.
func validHumanAddr(addr string) bool {
	name, ok := strings.CutPrefix(addr, HumanPrefix)
	return ok && validHumanName(name)
}

var (
	humanNameMu    sync.Mutex
	humanNameCache = map[int]string{}
)

// humanAddr returns "human:<user>" for uid, with the user name mapped to the
// human address charset (other characters become '_').
func humanAddr(uid int) string {
	humanNameMu.Lock()
	defer humanNameMu.Unlock()
	if a, ok := humanNameCache[uid]; ok {
		return a
	}
	name := strconv.Itoa(uid)
	if u, err := user.LookupId(name); err == nil && u.Username != "" {
		name = u.Username
	}
	name = strings.Map(func(r rune) rune {
		if r < 0x80 && validHumanName(string(r)) {
			return r
		}
		return '_'
	}, name)
	if len(name) > 64 {
		name = name[:64]
	}
	a := HumanPrefix + name
	humanNameCache[uid] = a
	return a
}

// sessionByPid returns the running session whose child pid is one of pid's
// ancestors (pid itself included).
func (s *Server) sessionByAncestry(pid int) (string, bool) {
	if pid <= 1 {
		return "", false
	}
	pids := make(map[int]string)
	s.mu.Lock()
	for name, e := range s.sessions {
		if e.sess.State() == session.StateRunning {
			pids[e.sess.Pid()] = name
		}
	}
	s.mu.Unlock()
	if len(pids) == 0 {
		return "", false
	}
	for range maxAncestry {
		if name, ok := pids[pid]; ok {
			return name, true
		}
		ppid, err := parentPID(pid)
		if err != nil || ppid <= 1 || ppid == pid {
			return "", false
		}
		pid = ppid
	}
	return "", false
}

// sessionByToken returns the session whose token is token. hint (the
// claimed AGENTD_ID), if set, is tried first.
func (s *Server) sessionByToken(token, hint string) (name string, running, ok bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	match := func(e *entry) bool {
		t := e.sess.Spec().Env[EnvToken]
		return t != "" && len(t) == len(token) && subtle.ConstantTimeCompare([]byte(t), []byte(token)) == 1
	}
	if e, found := s.sessions[hint]; found && match(e) {
		return hint, e.sess.State() == session.StateRunning, true
	}
	for n, e := range s.sessions {
		if match(e) {
			return n, e.sess.State() == session.StateRunning, true
		}
	}
	return "", false, false
}

func (s *Server) isStarting(name string) bool {
	if name == "" {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.starting[name]
	return ok
}

// resolveCaller resolves the identity of the peer p sending a request with
// auth. See the package comment above for the rules.
func (s *Server) resolveCaller(p peerInfo, auth *proto.Auth) (caller, error) {
	var a proto.Auth
	if auth != nil {
		a = *auth
	}
	deadline := time.Now().Add(startingWait)
	for {
		c, err := s.resolveOnce(p, a)
		if err == nil && (a.ID == "" || a.ID == c.id) {
			return c, nil
		}
		// A child may call in before its session is registered.
		if s.isStarting(a.ID) && time.Now().Before(deadline) {
			time.Sleep(20 * time.Millisecond)
			continue
		}
		if err != nil {
			return caller{}, err
		}
		if c.session {
			return caller{}, fmt.Errorf("identity mismatch: AGENTD_ID=%q, but this process runs inside session %q", a.ID, c.id)
		}
		return caller{}, fmt.Errorf("identity mismatch: AGENTD_ID=%q, but this process is not running inside that session "+
			"(the daemon sees it as %q). AGENTD_ID is set by agentd inside sessions; do not set it by hand", a.ID, c.id)
	}
}

func (s *Server) resolveOnce(p peerInfo, a proto.Auth) (caller, error) {
	byProc, procOK := s.sessionByAncestry(p.pid)
	if a.Token != "" {
		name, running, ok := s.sessionByToken(a.Token, a.ID)
		switch {
		case !ok:
			if !procOK {
				return caller{}, errors.New("invalid AGENTD_TOKEN: no session has this token (stale environment from a removed session?)")
			}
			// Kernel-verified ancestry wins over an unknown token.
		case procOK && name != byProc:
			return caller{}, fmt.Errorf("identity mismatch: AGENTD_TOKEN belongs to session %q, but this process runs inside session %q", name, byProc)
		case !procOK && !running:
			return caller{}, fmt.Errorf("session %q has exited: its AGENTD_TOKEN is no longer valid", name)
		case !procOK:
			return caller{id: name, session: true, via: viaToken}, nil
		}
	}
	if procOK {
		return caller{id: byProc, session: true, via: viaProcess}, nil
	}
	return caller{id: humanAddr(p.uid), via: viaUID}, nil
}

// whoami answers the whoami op.
func (s *Server) whoami(c caller) proto.WhoamiResult {
	return proto.WhoamiResult{Identity: c.id, Session: c.session, Via: c.via}
}
