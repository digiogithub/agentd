package chatfmt

import (
	"regexp"
	"strings"
)

// listItemRe matches a line that starts a list item.
var listItemRe = regexp.MustCompile(`^(?:[-*•–] |\d{1,3}[.)] )`)

// Reflow turns the text of a code block that is really prose back into
// paragraphs: indentation is dropped, consecutive lines are joined with a
// space (the terminal wrapped them), a line starting a list item stays on its
// own line and blank lines separate the paragraphs it returns.
func Reflow(code string) []string {
	var out []string
	cur := ""
	flush := func() {
		if cur != "" {
			out = append(out, cur)
			cur = ""
		}
	}
	for _, line := range strings.Split(code, "\n") {
		line = strings.TrimSpace(line)
		switch {
		case line == "":
			flush()
		case cur == "":
			cur = line
		case listItemRe.MatchString(line):
			cur += "\n" + line
		default:
			cur += " " + line
		}
	}
	flush()
	return out
}
