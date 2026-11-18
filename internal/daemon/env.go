package daemon

import (
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"slices"
	"strings"

	"github.com/digiogithub/agentd/internal/config"
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

// envLookup returns the value of name in a KEY=VALUE list ("" when absent).
func envLookup(env []string, name string) string {
	for i := len(env) - 1; i >= 0; i-- {
		if k, v, ok := strings.Cut(env[i], "="); ok && k == name {
			return v
		}
	}
	return ""
}

// resolveCmd gives a bare command name an absolute path only when the daemon's
// own PATH (a service or SSH shell often lacks the user's bun/npm/cargo/nvm
// directories) would not find it: the caller's PATH from base, the daemon's,
// then config.ExtraBinDirs. When the binary came from a directory that is not
// on the child's PATH, that directory is prepended in env (a script's
// `#!/usr/bin/env node` needs its siblings). Nothing found: cmd unchanged and
// the start fails with the usual exec error.
func resolveCmd(cmd []string, base []string, env map[string]string) []string {
	if len(cmd) == 0 || strings.ContainsRune(cmd[0], '/') {
		return cmd
	}
	if _, err := exec.LookPath(cmd[0]); err == nil {
		return cmd // the daemon's PATH finds it: exec does the same
	}
	pathEnv := envLookup(base, "PATH")
	if pathEnv == "" {
		pathEnv = os.Getenv("PATH")
	} else {
		pathEnv += string(os.PathListSeparator) + os.Getenv("PATH")
	}
	home := envLookup(base, "HOME")
	if home == "" {
		home, _ = os.UserHomeDir()
	}
	p, ok := config.FindBinary(cmd[0], pathEnv, home)
	if !ok {
		return cmd
	}
	if _, set := env["PATH"]; !set {
		dir := filepath.Dir(p)
		childPath := envLookup(base, "PATH")
		if childPath == "" {
			childPath = os.Getenv("PATH")
		}
		if !slices.Contains(filepath.SplitList(childPath), dir) {
			env["PATH"] = dir + string(os.PathListSeparator) + childPath
		}
	}
	out := append([]string(nil), cmd...)
	out[0] = p
	return out
}
