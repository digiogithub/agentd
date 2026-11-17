package screen

import (
	"fmt"
	"regexp"
	"strings"
)

// toolHeaderRe matches a tool-call header as plain-style TUIs print it:
// `- Bash: jj diff`, `- View: path (limit=260)`, `- Grep: pattern`. The dash is
// at column 0 or 1 and the tool name is a capitalized word or a snake_case
// MCP tool name (`kb_search_documents`); plain lowercase words are list items.
var toolHeaderRe = regexp.MustCompile(`^ ?- ([A-Z][A-Za-z0-9_]*|[a-z0-9]+_[a-z0-9_]+)(\.[A-Za-z0-9_]+)?: `)

// thinkHeaderRe matches a reasoning block title (`~ Reviewing imports`).
var thinkHeaderRe = regexp.MustCompile(`^ ?~ \S`)

// spinnerRe matches transient "Thinking..." status rows.
var spinnerRe = regexp.MustCompile(`^ ?[^\s\w]? ?(Thinking|Working|Generating)\.\.\.\s*$`)

// CompactTools groups the verbose parts of an agent transcript the way Claude
// Code shows them folded: a tool call keeps its header line and its indented
// output (file dumps, diffs, grep hits) becomes one `… N lines` note; a
// reasoning block keeps only its title; transient `Thinking...` rows are
// dropped. Input is PlainText output (or any text laid out the same way).
// Lines that are not part of such blocks are untouched, so a harness without
// these markers passes through unchanged.
func CompactTools(s string) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	var out []string
	for i := 0; i < len(lines); {
		l := lines[i]
		switch {
		case spinnerRe.MatchString(l):
			i++
		case toolHeaderRe.MatchString(l) || thinkHeaderRe.MatchString(l):
			out = append(out, l)
			j := i + 1
			hidden := 0
			for j < len(lines) && (strings.HasPrefix(lines[j], "   ") || strings.TrimSpace(lines[j]) == "") {
				if strings.TrimSpace(lines[j]) != "" {
					hidden++
				}
				j++
			}
			// Blank lines after the body belong to the gap, not to the block.
			for j > i+1 && strings.TrimSpace(lines[j-1]) == "" {
				j--
			}
			if hidden > 0 && toolHeaderRe.MatchString(l) {
				out = append(out, fmt.Sprintf("   … %d lines", hidden))
			}
			i = j
		default:
			out = append(out, l)
			i++
		}
	}
	if len(out) == 0 {
		return ""
	}
	return strings.Join(out, "\n") + "\n"
}
