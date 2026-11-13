package config

import (
	"fmt"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Menu (option detection) profile keys, design: docs/design/api/semantic-model.md
// §6-§7. All are data; internal/daemon compiles them into screen.MenuRules.

// Menu key defaults.
const (
	// DefaultMenuNumberRegex is screen.DefaultNumberPattern (kept as a literal
	// to avoid a config -> screen dependency; a test keeps them equal).
	DefaultMenuNumberRegex = `^\s*(?:(?P<ptr>[^\w\s])\s*)?(?:(?P<n>\d{1,2})[.)]|\[(?P<n2>\d{1,2})\])\s+(?P<label>\S.*)$`
	MenuSelectAuto         = "auto"
	MenuSelectDigit        = "digit"
	MenuSelectArrows       = "arrows"
	// menu_digit_action: what a digit does in a numbered menu. "select" = the
	// digit chooses and confirms by itself (act sends the digit only, never an
	// Enter afterwards); "move" = it only moves the pointer (act then sends
	// Enter, but only while the same menu instance is still on screen with
	// the pointer on the target).
	MenuDigitSelect = "select"
	MenuDigitMove   = "move"
	MenuNavAuto     = "auto"
	maxMenuKeyLen   = 32
)

var menuStyleNames = map[string]bool{
	"bold": true, "faint": true, "italic": true, "underline": true,
	"blink": true, "reverse": true, "conceal": true, "strike": true,
}

// MenuConfig is the partial (config.toml) form of the menu_* keys; it is
// embedded in ProfileConfig so the keys sit directly under [profiles.<name>].
type MenuConfig struct {
	MenuPointers       *[]string `toml:"menu_pointers"`
	MenuNumberRegex    *string   `toml:"menu_number_regex"`
	MenuSelectedStyles *[]string `toml:"menu_selected_styles"`
	MenuSelect         *string   `toml:"menu_select"`       // auto | digit | arrows
	MenuDigitAction    *string   `toml:"menu_digit_action"` // select | move
	MenuNavUp          *string   `toml:"menu_nav_up"`       // "auto" = CSI/SS3 by DECCKM
	MenuNavDown        *string   `toml:"menu_nav_down"`
	MenuSubmit         *string   `toml:"menu_submit"` // "" = profile submit
	MenuToggleKey      *string   `toml:"menu_toggle_key"`
}

// MenuProfile is the resolved form, Profile.Menu.
type MenuProfile struct {
	Pointers       []string
	NumberRegex    string
	Number         *regexp.Regexp
	SelectedStyles []string
	Select         string
	DigitAction    string // select | move
	NavUp, NavDown string
	// Submit is the key sent after a menu selection. Never empty once the
	// profile is resolved: menu_submit "" (the default) resolves to
	// Profile.Submit, so the daemon types the profile's submit sequence, not a
	// hard-coded "\r".
	Submit    string
	ToggleKey string
}

func defaultMenuConfig() MenuConfig {
	return MenuConfig{
		MenuPointers:       strsp([]string{"❯", "›", "▶", "▸", "➜", "→"}), // ">" stays configurable but is not a default (blockquotes, shell prompts); keep equal to screen.DefaultMenuPointers
		MenuNumberRegex:    strp(DefaultMenuNumberRegex),
		MenuSelectedStyles: strsp([]string{"reverse"}),
		MenuSelect:         strp(MenuSelectAuto),
		MenuDigitAction:    strp(MenuDigitMove),
		MenuNavUp:          strp(MenuNavAuto),
		MenuNavDown:        strp(MenuNavAuto),
		MenuSubmit:         strp(""),
		MenuToggleKey:      strp(" "),
	}
}

func (m *MenuConfig) merge(o MenuConfig) {
	if o.MenuPointers != nil {
		m.MenuPointers = o.MenuPointers
	}
	if o.MenuNumberRegex != nil {
		m.MenuNumberRegex = o.MenuNumberRegex
	}
	if o.MenuSelectedStyles != nil {
		m.MenuSelectedStyles = o.MenuSelectedStyles
	}
	if o.MenuSelect != nil {
		m.MenuSelect = o.MenuSelect
	}
	if o.MenuDigitAction != nil {
		m.MenuDigitAction = o.MenuDigitAction
	}
	if o.MenuNavUp != nil {
		m.MenuNavUp = o.MenuNavUp
	}
	if o.MenuNavDown != nil {
		m.MenuNavDown = o.MenuNavDown
	}
	if o.MenuSubmit != nil {
		m.MenuSubmit = o.MenuSubmit
	}
	if o.MenuToggleKey != nil {
		m.MenuToggleKey = o.MenuToggleKey
	}
}

func validMenuKey(key, v string, allowEmpty, allowAuto bool) error {
	if v == "" {
		if allowEmpty {
			return nil
		}
		return fmt.Errorf("%s must not be empty", key)
	}
	if allowAuto && v == MenuNavAuto {
		return nil
	}
	if len(v) > maxMenuKeyLen {
		return fmt.Errorf("%s longer than %d bytes", key, maxMenuKeyLen)
	}
	if strings.ContainsRune(v, 0) {
		return fmt.Errorf("%s must not contain NUL", key)
	}
	if strings.Contains(v, "\x1b[200~") || strings.Contains(v, "\x1b[201~") {
		return fmt.Errorf("%s must not contain paste markers", key)
	}
	return nil
}

func (m MenuConfig) validate() error {
	if m.MenuPointers != nil {
		for _, p := range *m.MenuPointers {
			r, n := utf8.DecodeRuneInString(p)
			if p == "" || n != len(p) || r == utf8.RuneError || unicode.IsSpace(r) ||
				unicode.IsLetter(r) || unicode.IsDigit(r) || unicode.Is(unicode.Mn, r) || unicode.Is(unicode.Me, r) ||
				unicode.Is(unicode.Cc, r) {
				return fmt.Errorf("menu_pointers: %q must be one non-blank, non-alphanumeric character", p)
			}
			if r >= 0x1100 && (r <= 0x115f || (r >= 0x2e80 && r <= 0xa4cf) || (r >= 0xac00 && r <= 0xd7a3) ||
				(r >= 0xf900 && r <= 0xfaff) || (r >= 0xfe30 && r <= 0xfe6f) || (r >= 0xff00 && r <= 0xff60) ||
				(r >= 0x1f300 && r <= 0x1faff)) {
				return fmt.Errorf("menu_pointers: %q must have width 1", p)
			}
		}
	}
	if m.MenuNumberRegex != nil {
		re, err := regexp.Compile(*m.MenuNumberRegex)
		if err != nil {
			return fmt.Errorf("menu_number_regex: %w", err)
		}
		if re.SubexpIndex("label") < 0 || (re.SubexpIndex("n") < 0 && re.SubexpIndex("n2") < 0) {
			return fmt.Errorf("menu_number_regex: needs named groups (?P<n>...) and (?P<label>...)")
		}
		if !numberRegexWorks(re) {
			return fmt.Errorf(`menu_number_regex: must match a numbered row such as "1. x", "1) x" or "[1] x" with n = 1 and a non-empty label`)
		}
	}
	if m.MenuSelectedStyles != nil {
		for _, s := range *m.MenuSelectedStyles {
			if !menuStyleNames[s] {
				return fmt.Errorf("menu_selected_styles: unknown style %q", s)
			}
		}
	}
	if m.MenuSelect != nil {
		switch *m.MenuSelect {
		case MenuSelectAuto, MenuSelectDigit, MenuSelectArrows:
		default:
			return fmt.Errorf("menu_select must be auto, digit or arrows, got %q", *m.MenuSelect)
		}
	}
	if m.MenuDigitAction != nil {
		switch *m.MenuDigitAction {
		case MenuDigitSelect, MenuDigitMove:
		default:
			return fmt.Errorf("menu_digit_action must be select or move, got %q", *m.MenuDigitAction)
		}
	}
	for _, f := range []struct {
		key        string
		v          *string
		empty, aut bool
	}{
		{"menu_nav_up", m.MenuNavUp, false, true},
		{"menu_nav_down", m.MenuNavDown, false, true},
		{"menu_submit", m.MenuSubmit, true, false},
		{"menu_toggle_key", m.MenuToggleKey, false, false},
	} {
		if f.v != nil {
			if err := validMenuKey(f.key, *f.v, f.empty, f.aut); err != nil {
				return err
			}
		}
	}
	return nil
}

// numberRegexWorks: re matches at least one of the sample rows with its n (or
// n2) group equal to 1 and a non-empty label.
func numberRegexWorks(re *regexp.Regexp) bool {
	iN, iN2, iL := re.SubexpIndex("n"), re.SubexpIndex("n2"), re.SubexpIndex("label")
	for _, sample := range []string{"1. x", "1) x", "[1] x", "❯ 1. x"} {
		m := re.FindStringSubmatch(sample)
		if m == nil || strings.TrimSpace(m[iL]) == "" {
			continue
		}
		for _, i := range []int{iN, iN2} {
			if i >= 0 && m[i] == "1" {
				return true
			}
		}
	}
	return false
}

// resolve builds the MenuProfile from a fully merged MenuConfig.
func (m MenuConfig) resolve() (MenuProfile, error) {
	p := MenuProfile{
		Pointers:       append([]string(nil), *m.MenuPointers...),
		NumberRegex:    *m.MenuNumberRegex,
		SelectedStyles: append([]string(nil), *m.MenuSelectedStyles...),
		Select:         *m.MenuSelect,
		DigitAction:    *m.MenuDigitAction,
		NavUp:          *m.MenuNavUp,
		NavDown:        *m.MenuNavDown,
		Submit:         *m.MenuSubmit,
		ToggleKey:      *m.MenuToggleKey,
	}
	re, err := regexp.Compile(p.NumberRegex)
	if err != nil {
		return p, fmt.Errorf("menu_number_regex: %w", err)
	}
	p.Number = re
	return p, nil
}

func (p MenuProfile) clone() MenuProfile {
	p.Pointers = append([]string(nil), p.Pointers...)
	p.SelectedStyles = append([]string(nil), p.SelectedStyles...)
	return p
}
