package config

import (
	"errors"
	"path/filepath"
)

// DefaultHarness is the harness used when neither a harness nor a command is
// given.
const DefaultHarness = "claude"

// RunRequest is the input of ResolveRun: what `agentd run` (CLI or API)
// asks for before defaults are applied.
type RunRequest struct {
	// Target is a project alias or a directory ("" means "."), resolved with
	// Aliases.ResolveTargetIn against Base.
	Target string
	// Base is the directory a relative Target is resolved against ("" =
	// process cwd).
	Base string
	// Harness is "claude", "codex", "pando" or a binary; "" means the first
	// element of Cmd, else DefaultHarness.
	Harness string
	// Cmd is the explicit argv; empty means [Harness].
	Cmd []string
	// Name and Profile override the derived session name and profile.
	Name    string
	Profile string
}

// RunSpec is a resolved run request.
type RunSpec struct {
	Cwd     string
	Label   string
	Harness string
	Name    string
	Cmd     []string
	Profile string
}

// ResolveRun applies the `agentd run` rules shared by the CLI and the
// daemon: the target is resolved with ResolveTargetIn; the harness defaults to
// the command's binary or "claude"; the command defaults to the harness
// binary; the name to "<harness basename>@<label>"; the profile to the
// harness basename.
func (a *Aliases) ResolveRun(r RunRequest) (RunSpec, error) {
	target := r.Target
	if target == "" {
		target = "."
	}
	dir, label, err := a.ResolveTargetIn(target, r.Base)
	if err != nil {
		return RunSpec{}, err
	}
	harness, argv := r.Harness, r.Cmd
	if harness == "" {
		harness = DefaultHarness
		if len(argv) > 0 {
			harness = argv[0]
		}
	}
	if harness == "" {
		return RunSpec{}, errors.New("config: empty harness")
	}
	hname := filepath.Base(harness)
	if len(argv) == 0 {
		argv = []string{harness}
	}
	name, profile := r.Name, r.Profile
	if name == "" {
		name = hname + "@" + label
	}
	if profile == "" {
		profile = hname
	}
	return RunSpec{Cwd: dir, Label: label, Harness: harness, Name: name, Cmd: argv, Profile: profile}, nil
}
