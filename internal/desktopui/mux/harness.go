package mux

import (
	"regexp"
	"strings"
)

// Harness guessing for multiplexer panes and session names. zellij and tmux
// know nothing about agents: the harness is inferred from the program running
// in a pane and from naming conventions.

// KnownHarnesses are the harness names recognised, in matching order.
var KnownHarnesses = []string{
	"claude", "codex", "pando", "opencode", "gemini", "grok", "aider", "crush",
	"goose", "amp", "qwen", "cursor-agent",
}

func isKnownHarness(s string) bool {
	for _, h := range KnownHarnesses {
		if h == s {
			return true
		}
	}
	return false
}

var versionLike = regexp.MustCompile(`^\d+\.\d+\.\d+`)

// GuessHarness guesses the harness from a pane's program (tmux
// `#{pane_current_command}`, zellij `terminal_command`, or a full command
// line) and an optional pane title. It returns "" when nothing matches.
// `node` / `bun` alone are ambiguous (many harnesses are Node programs) and
// only resolve through the title or the command line.
func GuessHarness(command, title string) string {
	for _, src := range []string{command, title} {
		if h := fromCommandLine(src); h != "" {
			return h
		}
	}
	// Claude Code's native installer runs ~/.local/share/claude/versions/X.Y.Z,
	// so tmux reports the version as the pane command.
	c := strings.TrimSpace(command)
	if versionLike.MatchString(c) && !strings.Contains(c, " ") {
		return "claude"
	}
	return ""
}

func fromCommandLine(line string) string {
	words := strings.Fields(line)
	if len(words) > 4 {
		words = words[:4]
	}
	for _, w := range words {
		base := strings.ToLower(w[strings.LastIndex(w, "/")+1:])
		if strings.HasSuffix(base, ".js") || strings.HasSuffix(base, ".mjs") {
			base = base[:strings.LastIndex(base, ".")]
		}
		if isKnownHarness(base) {
			return base
		}
		// npm package paths: .../@anthropic-ai/claude-code/cli.js, @openai/codex
		switch {
		case strings.Contains(w, "claude-code"):
			return "claude"
		case strings.Contains(w, "@openai/codex"):
			return "codex"
		case strings.Contains(w, "gemini-cli"):
			return "gemini"
		case strings.Contains(w, "opencode"):
			return "opencode"
		case strings.Contains(w, "/claude/versions/"):
			return "claude"
		}
	}
	return ""
}

// SplitSessionName splits a session name that follows a harness convention
// into (harness, project): agentd's `<harness>@<project>` (`codex@webapp`) or
// `<harness>-<project>` (`claude-agentd`, also `_` and `.`) when the prefix is
// a known harness. Otherwise both are "".
func SplitSessionName(name string) (harness, project string) {
	if at := strings.Index(name, "@"); at > 0 && at < len(name)-1 {
		return name[:at], name[at+1:]
	}
	lower := strings.ToLower(name)
	for _, h := range KnownHarnesses {
		for _, sep := range []string{"-", "_", "."} {
			p := h + sep
			if len(name) > len(p) && strings.HasPrefix(lower, p) {
				return h, name[len(p):]
			}
		}
	}
	if isKnownHarness(lower) {
		return lower, ""
	}
	return "", ""
}

var trailingSlashes = regexp.MustCompile(`/+$`)

// baseName is the last path element (`/home/u/src/webapp` → `webapp`), "" for
// an empty path.
func baseName(path string) string {
	p := trailingSlashes.ReplaceAllString(strings.TrimSpace(path), "")
	if p == "" {
		return ""
	}
	return p[strings.LastIndex(p, "/")+1:]
}
