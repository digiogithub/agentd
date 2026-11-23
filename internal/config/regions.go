package config

import (
	"fmt"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/digiogithub/agentd/internal/termcolor"
)

// Region / reflow profile keys, design: docs/design/api/semantic-model.md §3,
// §4, §7. All are data; internal/daemon compiles them into screen.Rules.

// Region key defaults.
const (
	DefaultRuleChars         = "─━═╌┄"
	DefaultWrapMargin        = 2
	DefaultDialogContextRows = 6
	maxWrapMargin            = 8
	maxDialogContextRows     = 20
)

// RegionConfig is the partial (config.toml) form of the region keys; it is
// embedded in ProfileConfig so the keys sit directly under [profiles.<name>].
//
// busy_regex, footer_regex, header_regex, spinner_chars and reflow_break_regex have no
// built-in value for claude/codex yet: the semantic-model marks them
// provisional until a recorded capture confirms them.
type RegionConfig struct {
	RuleChars         *string   `toml:"rule_chars"`
	InputBgColors     *[]string `toml:"input_bg_colors"`
	FooterRegex       *string   `toml:"footer_regex"`
	HeaderRegex       *string   `toml:"header_regex"`
	BusyRegex         *string   `toml:"busy_regex"`
	InfoRegex         *string   `toml:"info_regex"`
	SpinnerChars      *string   `toml:"spinner_chars"`
	ReflowBreakRegex  *string   `toml:"reflow_break_regex"`
	WrapMargin        *int      `toml:"wrap_margin"`
	CodeANSIColors    *bool     `toml:"code_ansi_colors"`
	DialogContextRows *int      `toml:"dialog_context_rows"`
}

// RegionProfile is the resolved form, Profile.Regions.
type RegionProfile struct {
	RuleChars        string
	InputBgColors    []string
	InputBgSpecs     []termcolor.Spec
	FooterRegex      string
	Footer           *regexp.Regexp // nil when FooterRegex is ""
	HeaderRegex      string
	Header           *regexp.Regexp // nil when HeaderRegex is ""
	BusyRegex        string
	Busy             *regexp.Regexp
	InfoRegex        string
	Info             *regexp.Regexp // nil when InfoRegex is ""
	SpinnerChars     string
	ReflowBreakRegex string
	ReflowBreak      *regexp.Regexp
	WrapMargin       int
	// CodeANSIColors: text in an ANSI palette colour is syntax-highlighted
	// code (screen.Rules.CodeANSI).
	CodeANSIColors bool
	// DialogContextRows: 0..20 (0 = the screen package default, 6).
	DialogContextRows int
}

func defaultRegionConfig() RegionConfig {
	return RegionConfig{
		RuleChars:         strp(DefaultRuleChars),
		InputBgColors:     strsp(nil),
		FooterRegex:       strp(""),
		HeaderRegex:       strp(""),
		BusyRegex:         strp(""),
		InfoRegex:         strp(""),
		SpinnerChars:      strp(""),
		ReflowBreakRegex:  strp(""),
		WrapMargin:        intp(DefaultWrapMargin),
		CodeANSIColors:    boolp(false),
		DialogContextRows: intp(DefaultDialogContextRows),
	}
}

func (r *RegionConfig) merge(o RegionConfig) {
	if o.RuleChars != nil {
		r.RuleChars = o.RuleChars
	}
	if o.InputBgColors != nil {
		r.InputBgColors = o.InputBgColors
	}
	if o.FooterRegex != nil {
		r.FooterRegex = o.FooterRegex
	}
	if o.HeaderRegex != nil {
		r.HeaderRegex = o.HeaderRegex
	}
	if o.BusyRegex != nil {
		r.BusyRegex = o.BusyRegex
	}
	if o.InfoRegex != nil {
		r.InfoRegex = o.InfoRegex
	}
	if o.SpinnerChars != nil {
		r.SpinnerChars = o.SpinnerChars
	}
	if o.ReflowBreakRegex != nil {
		r.ReflowBreakRegex = o.ReflowBreakRegex
	}
	if o.WrapMargin != nil {
		r.WrapMargin = o.WrapMargin
	}
	if o.CodeANSIColors != nil {
		r.CodeANSIColors = o.CodeANSIColors
	}
	if o.DialogContextRows != nil {
		r.DialogContextRows = o.DialogContextRows
	}
}

// glyphSet checks that every rune of s is a single-width, non-blank,
// non-alphanumeric character.
func glyphSet(key, s string) error {
	for _, r := range s {
		if r == utf8.RuneError || unicode.IsSpace(r) || unicode.IsLetter(r) || unicode.IsDigit(r) ||
			unicode.Is(unicode.Mn, r) || unicode.Is(unicode.Me, r) || unicode.Is(unicode.Cc, r) {
			return fmt.Errorf("%s: %q must be non-blank, non-alphanumeric characters", key, string(r))
		}
		if r >= 0x1100 && (r <= 0x115f || (r >= 0x2e80 && r <= 0xa4cf) || (r >= 0xac00 && r <= 0xd7a3) ||
			(r >= 0xf900 && r <= 0xfaff) || (r >= 0xfe30 && r <= 0xfe6f) || (r >= 0xff00 && r <= 0xff60) ||
			(r >= 0x1f300 && r <= 0x1faff)) {
			return fmt.Errorf("%s: %q must have width 1", key, string(r))
		}
	}
	return nil
}

// stateRegex compiles re for key; when mustNotMatchEmpty, a regex matching ""
// is rejected (a match means a state).
func stateRegex(key, re string, mustNotMatchEmpty bool) (*regexp.Regexp, error) {
	if re == "" {
		return nil, nil
	}
	c, err := regexp.Compile(re)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", key, err)
	}
	if mustNotMatchEmpty && c.MatchString("") {
		return nil, fmt.Errorf("%s: must not match the empty string", key)
	}
	return c, nil
}

func (r RegionConfig) validate() error {
	if r.RuleChars != nil {
		if *r.RuleChars == "" {
			return fmt.Errorf("rule_chars must not be empty")
		}
		if err := glyphSet("rule_chars", *r.RuleChars); err != nil {
			return err
		}
	}
	if r.SpinnerChars != nil {
		if err := glyphSet("spinner_chars", *r.SpinnerChars); err != nil {
			return err
		}
	}
	if r.InputBgColors != nil {
		if _, err := termcolor.ParseAll(*r.InputBgColors); err != nil {
			return fmt.Errorf("input_bg_colors: %w", err)
		}
	}
	for _, f := range []struct {
		key   string
		v     *string
		empty bool
	}{
		{"footer_regex", r.FooterRegex, true},
		{"header_regex", r.HeaderRegex, true},
		{"busy_regex", r.BusyRegex, true},
		{"info_regex", r.InfoRegex, true},
		{"reflow_break_regex", r.ReflowBreakRegex, true},
	} {
		if f.v != nil {
			if _, err := stateRegex(f.key, *f.v, f.empty); err != nil {
				return err
			}
		}
	}
	if r.WrapMargin != nil && (*r.WrapMargin < 0 || *r.WrapMargin > maxWrapMargin) {
		return fmt.Errorf("wrap_margin must be between 0 and %d, got %d", maxWrapMargin, *r.WrapMargin)
	}
	if r.DialogContextRows != nil && (*r.DialogContextRows < 0 || *r.DialogContextRows > maxDialogContextRows) {
		return fmt.Errorf("dialog_context_rows must be between 0 and %d, got %d", maxDialogContextRows, *r.DialogContextRows)
	}
	return nil
}

// resolve builds the RegionProfile from a fully merged RegionConfig.
func (r RegionConfig) resolve() (RegionProfile, error) {
	p := RegionProfile{
		RuleChars:         *r.RuleChars,
		InputBgColors:     append([]string(nil), *r.InputBgColors...),
		FooterRegex:       *r.FooterRegex,
		HeaderRegex:       *r.HeaderRegex,
		BusyRegex:         *r.BusyRegex,
		InfoRegex:         *r.InfoRegex,
		SpinnerChars:      strings.Clone(*r.SpinnerChars),
		ReflowBreakRegex:  *r.ReflowBreakRegex,
		WrapMargin:        *r.WrapMargin,
		CodeANSIColors:    *r.CodeANSIColors,
		DialogContextRows: *r.DialogContextRows,
	}
	var err error
	if p.InputBgSpecs, err = termcolor.ParseAll(p.InputBgColors); err != nil {
		return p, fmt.Errorf("input_bg_colors: %w", err)
	}
	if p.Footer, err = stateRegex("footer_regex", p.FooterRegex, true); err != nil {
		return p, err
	}
	if p.Header, err = stateRegex("header_regex", p.HeaderRegex, true); err != nil {
		return p, err
	}
	if p.Busy, err = stateRegex("busy_regex", p.BusyRegex, true); err != nil {
		return p, err
	}
	if p.Info, err = stateRegex("info_regex", p.InfoRegex, true); err != nil {
		return p, err
	}
	if p.ReflowBreak, err = stateRegex("reflow_break_regex", p.ReflowBreakRegex, true); err != nil {
		return p, err
	}
	return p, nil
}

func (p RegionProfile) clone() RegionProfile {
	p.InputBgColors = append([]string(nil), p.InputBgColors...)
	p.InputBgSpecs = append([]termcolor.Spec(nil), p.InputBgSpecs...)
	return p
}
