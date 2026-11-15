package mux

import (
	"regexp"
	"strings"
)

// POSIX shell quoting for the commands the mux and provision layers build
// (port of agentd-app lib/src/mux/quote.dart).

var safeWord = regexp.MustCompile(`^[A-Za-z0-9_@%+=:,./-]+$`)

// ShellQuote quotes s as one POSIX shell word. Safe words are returned
// unchanged.
func ShellQuote(s string) string {
	if s == "" {
		return "''"
	}
	if safeWord.MatchString(s) {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// ShellJoin quotes every element of argv and joins them with spaces.
func ShellJoin(argv []string) string {
	q := make([]string, len(argv))
	for i, a := range argv {
		q[i] = ShellQuote(a)
	}
	return strings.Join(q, " ")
}

// ShellDir quotes a directory path for the remote shell, keeping a leading
// `~` or `~/` expandable (as `"$HOME"`).
func ShellDir(dir string) string {
	if dir == "~" {
		return `"$HOME"`
	}
	if rest, ok := strings.CutPrefix(dir, "~/"); ok {
		if rest == "" {
			return `"$HOME"`
		}
		return `"$HOME"/` + ShellQuote(rest)
	}
	return ShellQuote(dir)
}
