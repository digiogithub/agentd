package daemon

import (
	"os"
	"path"
	"strings"
)

// alwaysStripEnv are variables never inherited from the base environment of
// a child: AGENTD_* is re-set by the daemon, and the Claude Code markers would
// make a harness started from inside Claude Code believe it is nested.
var alwaysStripEnv = []string{"AGENTD_*", "CLAUDECODE", "CLAUDE_CODE_ENTRYPOINT"}

// childBaseEnv returns the base environment of a new session: base (the CLI's
// environment, sent in RunArgs.EnvBase) or, if nil, the daemon's own, minus
// alwaysStripEnv and the config's [run] strip_env names/globs.
func (s *Server) childBaseEnv(base []string) []string {
	if base == nil {
		base = os.Environ()
	}
	strip := alwaysStripEnv
	if s.Cfg != nil && len(s.Cfg.Run.StripEnv) > 0 {
		strip = append(append([]string(nil), alwaysStripEnv...), s.Cfg.Run.StripEnv...)
	}
	return filterEnv(base, strip)
}

// filterEnv returns env without the entries whose name matches one of the
// path.Match patterns in strip. Malformed entries (no '=' or empty name) are
// dropped too.
func filterEnv(env, strip []string) []string {
	out := make([]string, 0, len(env))
	for _, kv := range env {
		name, _, ok := strings.Cut(kv, "=")
		if !ok || name == "" || envMatches(name, strip) {
			continue
		}
		out = append(out, kv)
	}
	return out
}

func envMatches(name string, patterns []string) bool {
	for _, p := range patterns {
		if ok, _ := path.Match(p, name); ok {
			return true
		}
	}
	return false
}
