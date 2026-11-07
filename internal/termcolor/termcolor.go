// Package termcolor parses terminal colour specifications used in config
// (harness profile ghost_colors) and matches them against emulator cell
// colours. It has no dependencies so that both internal/config (validation)
// and internal/session (matching) can use it.
package termcolor

import (
	"fmt"
	"strconv"
	"strings"
)

// Spec is one colour specification: a palette index (0–255, where 0–15 are
// the basic ANSI colours) or a 24-bit RGB colour.
type Spec struct {
	// Index is the palette index; -1 when the spec is an RGB colour.
	Index int
	// RGB is 0xRRGGBB when Index is -1.
	RGB uint32
}

// Parse parses "N" (decimal palette index 0–255, e.g. "8" or "244") or
// "#RRGGBB" / "#RGB" (case-insensitive).
func Parse(s string) (Spec, error) {
	v := strings.TrimSpace(s)
	if strings.HasPrefix(v, "#") {
		h := v[1:]
		if len(h) == 3 {
			h = string([]byte{h[0], h[0], h[1], h[1], h[2], h[2]})
		}
		if len(h) != 6 {
			return Spec{}, fmt.Errorf("bad colour %q: want #RRGGBB, #RGB or a palette index 0-255", s)
		}
		n, err := strconv.ParseUint(h, 16, 32)
		if err != nil {
			return Spec{}, fmt.Errorf("bad colour %q: want #RRGGBB, #RGB or a palette index 0-255", s)
		}
		return Spec{Index: -1, RGB: uint32(n)}, nil
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < 0 || n > 255 {
		return Spec{}, fmt.Errorf("bad colour %q: want #RRGGBB, #RGB or a palette index 0-255", s)
	}
	return Spec{Index: n}, nil
}

// ParseAll parses every entry of ss.
func ParseAll(ss []string) ([]Spec, error) {
	out := make([]Spec, 0, len(ss))
	for _, s := range ss {
		sp, err := Parse(s)
		if err != nil {
			return nil, err
		}
		out = append(out, sp)
	}
	return out, nil
}

// MatchIndex reports whether a palette colour with index idx matches any spec.
func MatchIndex(specs []Spec, idx int) bool {
	for _, s := range specs {
		if s.Index >= 0 && s.Index == idx {
			return true
		}
	}
	return false
}

// MatchRGB reports whether the 24-bit colour rgb (0xRRGGBB) matches any spec.
func MatchRGB(specs []Spec, rgb uint32) bool {
	for _, s := range specs {
		if s.Index < 0 && s.RGB == rgb {
			return true
		}
	}
	return false
}

// String formats the spec the way Parse accepts it.
func (s Spec) String() string {
	if s.Index >= 0 {
		return strconv.Itoa(s.Index)
	}
	return fmt.Sprintf("#%06x", s.RGB)
}
