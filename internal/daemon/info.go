package daemon

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/digiogithub/agentd/internal/config"
	"github.com/digiogithub/agentd/internal/proto"
)

// newEpoch returns an identifier unique to one daemon start
// ("<unix>-<pid>-<random>"); revisions and sequence numbers reset with it.
func newEpoch() string {
	var b [3]byte
	_, _ = rand.Read(b[:])
	return fmt.Sprintf("%d-%d-%s", time.Now().Unix(), os.Getpid(), hex.EncodeToString(b[:]))
}

// Epoch returns the server's start epoch (reported by ping and info).
func (s *Server) Epoch() string { return s.epoch }

// currentAliases returns the aliases to resolve with. When AliasesLoader is
// set (the daemon binary) the file/env are re-read on every call, so editing
// aliases.toml takes effect without restarting the daemon (the CLI used to
// read the file on each `run`). With a non-empty envBase (the caller's
// environment from run/info) AGENTD_ALIASES, AGENTD_ALIASES_FILE,
// XDG_CONFIG_HOME and HOME are read from it, as when the CLI loaded aliases
// itself; otherwise from the daemon's own environment. A failing reload is
// returned as an error.
func (s *Server) currentAliases(envBase []string) (*config.Aliases, error) {
	if s.AliasesLoader == nil {
		return s.Aliases, nil
	}
	getenv := os.Getenv
	if len(envBase) > 0 {
		getenv = config.EnvLookup(envBase)
	}
	return s.AliasesLoader(getenv)
}

func (s *Server) ping() proto.PingResult {
	return proto.PingResult{Version: Version, Pid: os.Getpid(), Epoch: s.epoch}
}

// infoOp answers the info op.
func (s *Server) infoOp(a proto.InfoArgs) proto.InfoResult {
	host, _ := os.Hostname()
	res := proto.InfoResult{
		Host: host, Version: Version, Pid: os.Getpid(), Epoch: s.epoch,
		Harnesses: []string{}, Profiles: []proto.ProfileInfo{}, Aliases: []proto.AliasInfo{},
		AgentView: s.Cfg.Security.AgentView,
	}
	builtin := map[string]bool{}
	for _, n := range config.BuiltinProfileNames() {
		builtin[n] = true
	}
	for _, n := range s.Cfg.ProfileNames() {
		p := s.Cfg.Profile(n)
		res.Profiles = append(res.Profiles, proto.ProfileInfo{Name: n, Builtin: builtin[n], Cols: p.Cols, Rows: p.Rows})
		if n != config.DefaultProfile {
			res.Harnesses = append(res.Harnesses, n)
		}
	}
	aliases, err := s.currentAliases(a.EnvBase)
	if err != nil {
		s.Log.Warn("load aliases", "err", err)
		aliases = s.Aliases // last good set
	}
	for _, al := range aliases.List() {
		res.Aliases = append(res.Aliases, proto.AliasInfo{Name: al.Name, Dir: al.Dir, Source: al.Source})
	}
	return res
}

// resolveRun fills a RunArgs from Target/Harness with the CLI's rules
// (config.Aliases.ResolveRun). Without Target, only the harness defaults
// (command and profile) apply. Cwd keeps its meaning (absolute working
// directory) except that, with a Target, it is the base for a relative path.
func (s *Server) resolveRun(a proto.RunArgs) (proto.RunArgs, error) {
	if a.Target == "" {
		if a.Harness != "" {
			if len(a.Cmd) == 0 {
				a.Cmd = []string{a.Harness}
			}
			if a.Profile == "" {
				a.Profile = filepath.Base(a.Harness)
			}
		}
		return a, nil
	}
	aliases, err := s.currentAliases(a.EnvBase)
	if err != nil {
		return a, proto.WithCode(proto.CodeInvalid, err)
	}
	if a.Cwd != "" && !filepath.IsAbs(a.Cwd) {
		return a, proto.Errorf(proto.CodeInvalid, "run: cwd %q is not absolute", a.Cwd)
	}
	if _, isAlias := aliases.Lookup(a.Target); !isAlias && !filepath.IsAbs(a.Target) && a.Cwd == "" {
		return a, proto.Errorf(proto.CodeInvalid, "run: relative target %q needs the caller's cwd", a.Target)
	}
	spec, err := aliases.ResolveRun(config.RunRequest{
		Target: a.Target, Base: a.Cwd, Harness: a.Harness, Cmd: a.Cmd, Name: a.Name, Profile: a.Profile,
	})
	if err != nil {
		return a, proto.WithCode(proto.CodeInvalid, err)
	}
	a.Cwd, a.Name, a.Cmd, a.Profile = spec.Cwd, spec.Name, spec.Cmd, spec.Profile
	a.Target, a.Harness = "", ""
	return a, nil
}
