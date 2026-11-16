// Package mux lists, attaches to, launches and kills zellij and tmux
// sessions on one host through a backend.Shell (exec + PTY). It is a port of
// the agentd-app Flutter layer lib/src/mux: same commands, same parsers.
// zellij and tmux know nothing about agents, so sessions carry status
// `unknown` (or `exited`) and a guessed harness.
package mux

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/digiogithub/agentd/internal/desktopui/backend"
)

// Adapter is one terminal multiplexer on one host.
type Adapter interface {
	Source() backend.Source
	// List returns the sessions on the host; empty when the multiplexer has
	// no server running. It fails with an *Error when the multiplexer is
	// missing or fails.
	List(ctx context.Context) ([]backend.Session, error)
	// Attach attaches to session name in a PTY.
	Attach(ctx context.Context, name string, cols, rows int) (backend.PTY, error)
	// Launch runs spec.Harness in spec.Project inside a session named
	// `<harness>@<project base name>`, creating the session when it does not
	// exist (a new window / pane otherwise).
	Launch(ctx context.Context, spec backend.LaunchSpec) (backend.Session, error)
	// Kill ends session name. A session that does not exist is not an error.
	Kill(ctx context.Context, name string) error
}

// ForHost returns the zellij and tmux adapters of one host.
func ForHost(sh backend.Shell, hostID string) []Adapter {
	return []Adapter{NewZellij(sh, hostID), NewTmux(sh, hostID)}
}

// Status values the adapters set (agentd view statuses).
const (
	StatusUnknown = "unknown"
	StatusExited  = "exited"
)

// Error is a multiplexer failure. Result is the command result, when there
// was one.
type Error struct {
	Msg          string
	NotInstalled bool
	Result       *backend.ExecResult
}

func (e *Error) Error() string { return "mux: " + e.Msg }

// ErrNotInstalled matches (errors.Is) an *Error about a missing multiplexer.
var ErrNotInstalled = errors.New("mux: not installed")

// Is reports whether target is ErrNotInstalled and e is about a missing tool.
func (e *Error) Is(target error) bool { return target == ErrNotInstalled && e.NotInstalled }

func failed(r backend.ExecResult, format string, a ...any) *Error {
	return &Error{Msg: fmt.Sprintf(format, a...), Result: &r}
}

var (
	ansiCSI = regexp.MustCompile(`\x1b\[[0-9;?]*[ -/]*[@-~]`)
	ansiOSC = regexp.MustCompile(`\x1b\][^\x07\x1b]*(\x07|\x1b\\)`)
)

// StripANSI removes ANSI escape sequences (CSI, OSC) from s.
func StripANSI(s string) string {
	return ansiOSC.ReplaceAllString(ansiCSI.ReplaceAllString(s, ""), "")
}

// checkInstalled returns an *Error when r says tool is not installed.
func checkInstalled(tool string, r backend.ExecResult) error {
	e := strings.ToLower(r.Stderr)
	if r.Exit == 127 || strings.Contains(e, "command not found") ||
		strings.Contains(e, "not found: "+tool) || strings.Contains(e, tool+": not found") {
		return &Error{Msg: tool + " is not installed", NotInstalled: true, Result: &r}
	}
	return nil
}

// splitLines splits like Dart's LineSplitter: on \n, \r\n or \r, without a
// final empty line after a trailing terminator.
func splitLines(s string) []string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.ReplaceAll(s, "\r", "\n")
	s = strings.TrimSuffix(s, "\n")
	if s == "" {
		return nil
	}
	return strings.Split(s, "\n")
}

// launchPlan is what Launch derives from a LaunchSpec.
type launchPlan struct {
	session string
	cwd     string
	argv    []string
	harness string
}

// TerminalHarness is the harness name of a terminal-only session
// (`shell@<label>`).
const TerminalHarness = "shell"

var unsafeNameChars = regexp.MustCompile(`[^A-Za-z0-9_@-]+`)

// promptArgHarnesses take a first prompt as their first positional argument
// and stay interactive (`claude "fix it"`, `codex "fix it"`).
var promptArgHarnesses = map[string]bool{"claude": true, "codex": true}

// planLaunch maps spec to a session name, working directory and argv. The
// session is `<harness>@<label>` (agentd's naming convention), label = base
// name of the project directory with characters tmux would rewrite (`.`,
// `:`) or shells dislike replaced by `-`.
func planLaunch(spec backend.LaunchSpec) (launchPlan, error) {
	h := strings.TrimSpace(spec.Harness)
	if spec.TerminalOnly {
		h = TerminalHarness
	}
	if h == "" {
		return launchPlan{}, errors.New("mux: launch needs a harness")
	}
	cwd := strings.TrimSpace(spec.Project)
	if cwd == "" {
		cwd = "~"
	}
	label := baseName(cwd)
	if label == "~" || label == "" {
		label = "home"
	}
	label = strings.Trim(unsafeNameChars.ReplaceAllString(label, "-"), "-")
	if label == "" {
		label = "home"
	}
	argv := []string{h}
	if spec.TerminalOnly {
		argv = nil // the multiplexer's default shell
	} else if p := strings.TrimSpace(spec.FirstPrompt); p != "" && promptArgHarnesses[h] {
		argv = append(argv, p)
	}
	return launchPlan{session: h + "@" + label, cwd: cwd, argv: argv, harness: h}, nil
}

// project is the session project: the name's convention, else the cwd base.
func project(name, cwd string) string {
	if _, p := SplitSessionName(name); p != "" {
		return p
	}
	return baseName(cwd)
}

func plural(n int, one, many string) string {
	if n == 1 {
		return fmt.Sprintf("%d %s", n, one)
	}
	return fmt.Sprintf("%d %s", n, many)
}
