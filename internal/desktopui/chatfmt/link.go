package chatfmt

import (
	"regexp"
	"strings"
)

// Segment is a run of text, a URL when URL is set (Text is then the URL as
// shown).
type Segment struct {
	Text string
	URL  string
}

var urlRe = regexp.MustCompile(`https?://[^\s<>"'` + "`" + `]+`)

// TrimURL removes trailing punctuation that is not part of a URL: .,;:!?
// quotes and closing brackets without a matching opener inside it.
func TrimURL(u string) string {
	for u != "" {
		last := u[len(u)-1]
		switch last {
		case '.', ',', ';', ':', '!', '?', '\'', '"':
			u = u[:len(u)-1]
			continue
		case ')', ']', '}':
			open := map[byte]string{')': "(", ']': "[", '}': "{"}[last]
			if strings.Count(u, open) >= strings.Count(u, string(last)) {
				return u
			}
			u = u[:len(u)-1]
			continue
		}
		return u
	}
	return u
}

// Linkify splits text into plain and URL segments (http and https only).
func Linkify(text string) []Segment {
	var out []Segment
	pos := 0
	for _, m := range urlRe.FindAllStringIndex(text, -1) {
		u := TrimURL(text[m[0]:m[1]])
		if len(u) <= len("https://") || strings.HasSuffix(u, "://") {
			continue
		}
		if m[0] > pos {
			out = append(out, Segment{Text: text[pos:m[0]]})
		}
		out = append(out, Segment{Text: u, URL: u})
		pos = m[0] + len(u)
	}
	if pos < len(text) {
		out = append(out, Segment{Text: text[pos:]})
	}
	return out
}

// HasURL reports whether text holds a link.
func HasURL(text string) bool {
	for _, s := range Linkify(text) {
		if s.URL != "" {
			return true
		}
	}
	return false
}
