package screen

import (
	"regexp"

	"github.com/digiogithub/agentd/internal/termcolor"
)

// GhostRules classifies ghost cells. The scope is always the input line: the
// cursor's row, from the cursor column to the right edge (every capture put
// the cursor on the first cell of the ghost text). Rules are never applied to
// the rest of the screen: Codex, for one, draws ordinary secondary text faint.
// See docs/ghost-text.md.
type GhostRules struct {
	// Faint: faint (SGR 2) cells in scope are ghost.
	Faint bool
	// Colors: cells in scope whose foreground matches one of these are ghost.
	Colors []termcolor.Spec
	// RightOfCursor: the text in scope is ghost only when the cursor sits right
	// after PromptMarker (the text left of the cursor, ignoring spaces and
	// no-break spaces, ends with it; an empty marker always passes). If no
	// style rule (Faint, Colors) is set, every non-blank cell in scope is ghost;
	// otherwise a cell must match a style rule too.
	RightOfCursor bool
	// PromptMarker is the harness's prompt glyph, e.g. "❯" or "›".
	PromptMarker string
}

// StyleRules reports whether a style rule (Faint, Colors) is configured.
func (r GhostRules) StyleRules() bool { return r.Faint || len(r.Colors) > 0 }

// Enabled reports whether the rules can classify anything.
func (r GhostRules) Enabled() bool { return r.StyleRules() || r.RightOfCursor }

// Defaults used when a Rules field is left empty.
const (
	// DefaultRuleChars are the horizontal rule glyphs (semantic-model §4.1).
	DefaultRuleChars = "─━═╌┄"
	// DefaultWrapMargin absorbs TUIs that wrap a few columns early (§3.3).
	DefaultWrapMargin = 2
	// DefaultDialogContextRows is the number of non-blank rows above an option
	// list that belong to the dialog's question (§4.3).
	DefaultDialogContextRows = 6
)

// Rules is the per-profile data driving reflow and the region split
// (semantic-model §3, §4, §7). The zero value is usable: empty RuleChars and
// DialogContextRows 0 fall back to the defaults, nil regexes never match.
// internal/daemon compiles it once per resolved profile.
type Rules struct {
	// Ghost holds the ghost-text rules and, in Ghost.PromptMarker, the
	// harness's prompt glyph (profile prompt_marker).
	Ghost GhostRules
	// PromptRegex (profile prompt_regex) is matched per ghost-filtered row to
	// find the input line when the cursor is hidden.
	PromptRegex *regexp.Regexp
	// InputBgColors (profile input_bg_colors): an input row whose cells all
	// carry one of these backgrounds (Codex: #1e1e1e).
	InputBgColors []termcolor.Spec
	// RuleChars (profile rule_chars): glyphs of horizontal rules; "" = DefaultRuleChars.
	RuleChars string
	// FooterRegex (profile footer_regex): footer rows when there is no input box.
	FooterRegex *regexp.Regexp
	// BusyRegex (profile busy_regex): status-line rows.
	BusyRegex *regexp.Regexp
	// SpinnerChars (profile spinner_chars): glyphs that start a status line.
	SpinnerChars string
	// NoJoin (profile reflow_break_regex): a row matching it never continues
	// the previous row.
	NoJoin *regexp.Regexp
	// WrapMargin (profile wrap_margin): columns by which a TUI may wrap early.
	WrapMargin int
	// DialogContextRows (profile dialog_context_rows); 0 = default.
	DialogContextRows int
	// Menu holds the menu_* rules (options.go). Split uses DetectOptions with
	// it to find option-list dialogs; Menu.Pointers also stops reflow joins.
	Menu MenuRules
}

// DefaultRules returns Rules with the profile defaults of §7 (no regexes).
func DefaultRules() Rules {
	return Rules{
		Ghost:             GhostRules{Faint: true},
		RuleChars:         DefaultRuleChars,
		WrapMargin:        DefaultWrapMargin,
		DialogContextRows: DefaultDialogContextRows,
		Menu:              DefaultMenuRules(),
	}
}

func (r Rules) ruleChars() string {
	if r.RuleChars == "" {
		return DefaultRuleChars
	}
	return r.RuleChars
}

func (r Rules) contextRows() int {
	if r.DialogContextRows <= 0 {
		return DefaultDialogContextRows
	}
	return r.DialogContextRows
}

func (r Rules) pointers() []string {
	if r.Menu.Pointers == nil {
		return DefaultMenuPointers
	}
	return r.Menu.Pointers
}
