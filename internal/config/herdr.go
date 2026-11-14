package config

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
)

// HerdrConfig is the [herdr] table: the herdr-core (hc) protocol served by
// `agentd hc connect` (Herdr Desktop, the Herdr mobile app, Whip) and, when
// Listen is set, by the daemon on a herdr-core style unix socket (Whip over
// SSH direct-streamlocal, herdr-mobile-relay's hc backend).
type HerdrConfig struct {
	// Enabled (default true): false makes `hc connect` refuse to start and
	// keeps the hc socket listener off.
	Enabled *bool `toml:"enabled"`
	// ReadOnly: hc clients may list, read and watch (attach as observers)
	// but never type, run, kill, label or write documents.
	ReadOnly bool `toml:"read_only"`
	// AllowRun lists the programs (argv[0] base names, globs allowed)
	// terminal.run may start; empty = any (the hc client already has the
	// user's shell through SSH or the desktop session).
	AllowRun []string `toml:"allow_run"`
	// Listen (default false) makes the daemon serve hc on a unix socket at
	// herdr-core's place (see Paths) when no live herdr-core owns it.
	Listen bool `toml:"listen"`
	// Socket overrides the listener's socket path ("~" expanded; absolute).
	Socket string `toml:"socket"`
	// Session is the herdr-core session name the listener stands in for
	// (default "default"): it names the default socket and the state folder
	// holding core.lock.
	Session string `toml:"session"`
}

// DefaultHCSession is herdr-core's default session name.
const DefaultHCSession = "default"

// On reports whether the hc protocol is enabled.
func (h HerdrConfig) On() bool { return h.Enabled == nil || *h.Enabled }

// RunAllowed reports whether terminal.run may start program (argv[0]).
func (h HerdrConfig) RunAllowed(program string) bool {
	if len(h.AllowRun) == 0 {
		return true
	}
	base := filepath.Base(program)
	return slices.ContainsFunc(h.AllowRun, func(p string) bool {
		ok, _ := filepath.Match(p, base)
		return ok
	})
}

// SessionName returns Session, or DefaultHCSession when unset.
func (h HerdrConfig) SessionName() string {
	if s := strings.TrimSpace(h.Session); s != "" {
		return s
	}
	return DefaultHCSession
}

// HCPaths is where the hc socket listener binds and the herdr-core
// discovery lock it holds.
type HCPaths struct {
	// Socket is the unix socket path.
	Socket string
	// Lock is <state>/core.lock: herdr-core's own lifetime lock (flock) whose
	// content is the socket path. Whip reads it to find the socket; a real
	// herdr-core that cannot take it does not start.
	Lock string
}

// Paths resolves the listener paths the way herdr-core does (paths.rs,
// platform/unix.rs): state = ${XDG_STATE_HOME:-$HOME/.local/state}/herdr-core/<session>;
// socket = Socket, else $XDG_RUNTIME_DIR/herdr-core/<session>.sock, else
// /run/user/<uid>/herdr-core/<session>.sock when that directory exists, else
// <state>/<session>.sock. getenv nil means os.Getenv. HC_SOCKET is not read:
// it is a client variable, and the daemon's environment is not the client's.
func (h HerdrConfig) Paths(getenv func(string) string) (HCPaths, error) {
	if getenv == nil {
		getenv = os.Getenv
	}
	session := h.SessionName()
	stateHome := getenv("XDG_STATE_HOME")
	if stateHome == "" {
		home, err := homeEnv(getenv)
		if err != nil {
			return HCPaths{}, fmt.Errorf("config: [herdr] state dir: %w", err)
		}
		stateHome = filepath.Join(home, ".local", "state")
	}
	state := filepath.Join(stateHome, "herdr-core", session)
	p := HCPaths{Lock: filepath.Join(state, "core.lock")}
	switch {
	case h.Socket != "":
		s, err := expandAliasDir(h.Socket, getenv)
		if err != nil {
			return HCPaths{}, fmt.Errorf("config: [herdr] socket: %w", err)
		}
		p.Socket = s
	case getenv("XDG_RUNTIME_DIR") != "":
		p.Socket = filepath.Join(getenv("XDG_RUNTIME_DIR"), "herdr-core", session+".sock")
	default:
		run := "/run/user/" + strconv.Itoa(os.Getuid())
		if fi, err := os.Stat(run); err == nil && fi.IsDir() {
			p.Socket = filepath.Join(run, "herdr-core", session+".sock")
		} else {
			p.Socket = filepath.Join(state, session+".sock")
		}
	}
	return p, nil
}

// HCCoreIDPath is $STATE/hc/core-id: the hc core identity shared by
// `hc connect` and the daemon's hc listener.
func HCCoreIDPath() (string, error) {
	dir, err := StateDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "hc", "core-id"), nil
}

// validHCSession mirrors herdr-core's session rule (it must be a folder name).
func validHCSession(s string) bool {
	return s != "." && s != ".." && !strings.ContainsFunc(s, func(r rune) bool {
		return r < 0x20 || r == 0x7f || strings.ContainsRune(`/\:*?"<>|`, r)
	})
}

func (h HerdrConfig) validate() error {
	for _, p := range h.AllowRun {
		if strings.TrimSpace(p) == "" || strings.Contains(p, "/") {
			return fmt.Errorf("config: [herdr] allow_run: %q must be a program name or glob (no path)", p)
		}
		if _, err := filepath.Match(p, "x"); err != nil {
			return fmt.Errorf("config: [herdr] allow_run: %q: %v", p, err)
		}
	}
	if !validHCSession(h.SessionName()) {
		return fmt.Errorf("config: [herdr] session: %q cannot be a folder name", h.Session)
	}
	if h.Socket != "" && !filepath.IsAbs(h.Socket) && h.Socket != "~" && !strings.HasPrefix(h.Socket, "~/") {
		return fmt.Errorf("config: [herdr] socket: %q must be absolute (or start with ~/)", h.Socket)
	}
	return nil
}
