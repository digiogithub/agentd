package config

import (
	"fmt"
	"path/filepath"
	"slices"
	"strings"
)

// HerdrConfig is the [herdr] table: the herdr-core (hc) protocol served by
// `agentd hc connect` (Herdr Desktop, the Herdr mobile app, Whip).
type HerdrConfig struct {
	// Enabled (default true): false makes `hc connect` refuse to start.
	Enabled *bool `toml:"enabled"`
	// ReadOnly: hc clients may list, read and watch (attach as observers)
	// but never type, run, kill, label or write documents.
	ReadOnly bool `toml:"read_only"`
	// AllowRun lists the programs (argv[0] base names, globs allowed)
	// terminal.run may start; empty = any (the hc client already has the
	// user's shell through SSH or the desktop session).
	AllowRun []string `toml:"allow_run"`
}

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

func (h HerdrConfig) validate() error {
	for _, p := range h.AllowRun {
		if strings.TrimSpace(p) == "" || strings.Contains(p, "/") {
			return fmt.Errorf("config: [herdr] allow_run: %q must be a program name or glob (no path)", p)
		}
		if _, err := filepath.Match(p, "x"); err != nil {
			return fmt.Errorf("config: [herdr] allow_run: %q: %v", p, err)
		}
	}
	return nil
}
