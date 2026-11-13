package config

import (
	"strings"
	"testing"

	"github.com/digiogithub/agentd/internal/screen"
)

func TestMenuDefaults(t *testing.T) {
	c := Default()
	d := c.Profile("default").Menu
	if len(d.Pointers) != 6 || d.Select != "auto" || d.NavUp != "auto" || d.ToggleKey != " " || d.Submit != DefaultSubmit || d.Number == nil {
		t.Fatalf("default menu = %+v", d)
	}
	if got := c.Profile("claude").Menu.Pointers; len(got) != 1 || got[0] != "❯" {
		t.Errorf("claude pointers = %q", got)
	}
	if got := c.Profile("codex").Menu.Pointers; len(got) != 1 || got[0] != "›" {
		t.Errorf("codex pointers = %q", got)
	}
	if got := c.Profile("pando").Menu.Pointers; len(got) != 6 {
		t.Errorf("pando pointers = %q", got)
	}
	if got := c.Profile("claude").Menu.SelectedStyles; len(got) != 1 || got[0] != "reverse" {
		t.Errorf("claude styles = %q", got)
	}
	if DefaultMenuNumberRegex != screen.DefaultNumberPattern {
		t.Error("DefaultMenuNumberRegex drifted from screen.DefaultNumberPattern")
	}
	// Profile() returns a copy.
	p := c.Profile("claude")
	p.Menu.Pointers[0] = "x"
	if c.Profile("claude").Menu.Pointers[0] != "❯" {
		t.Error("Profile() leaked shared menu slice")
	}
}

func TestMenuOverrides(t *testing.T) {
	c, err := loadTOML(t, `[profiles.default]
menu_select = "arrows"
menu_toggle_key = "x"
[profiles.codex]
menu_pointers = ["▶"]
menu_nav_down = "j"
menu_submit = "\r\r"
`)
	if err != nil {
		t.Fatal(err)
	}
	m := c.Profile("codex").Menu
	if m.Select != "arrows" || m.ToggleKey != "x" || m.NavDown != "j" || m.Submit != "\r\r" || len(m.Pointers) != 1 || m.Pointers[0] != "▶" {
		t.Errorf("codex menu = %+v", m)
	}
	if c.Profile("claude").Menu.Select != "arrows" || c.Profile("claude").Menu.Pointers[0] != "❯" {
		t.Errorf("claude menu = %+v", c.Profile("claude").Menu)
	}
}

func TestMenuValidation(t *testing.T) {
	bad := map[string]string{
		"menu_pointers = [\"ab\"]":                              "menu_pointers",
		"menu_pointers = [\"\"]":                                "menu_pointers",
		"menu_pointers = [\"a\"]":                               "menu_pointers",
		"menu_pointers = [\" \"]":                               "menu_pointers",
		"menu_pointers = [\"＞\"]":                               "width 1",
		"menu_number_regex = \"(\"":                             "menu_number_regex",
		"menu_number_regex = \"^(\\\\d+)\\\\. x\"":              "named groups",
		"menu_selected_styles = [\"glow\"]":                     "menu_selected_styles",
		"menu_select = \"mouse\"":                               "menu_select",
		"menu_digit_action = \"enter\"":                         "menu_digit_action",
		"menu_nav_up = \"\"":                                    "menu_nav_up",
		"menu_toggle_key = \"\"":                                "menu_toggle_key",
		"menu_submit = \"a\\u0000\"":                            "NUL",
		"menu_submit = \"\\u001b[200~\"":                        "paste markers",
		"menu_toggle_key = \"" + strings.Repeat("a", 33) + "\"": "longer",
	}
	for line, want := range bad {
		_, err := loadTOML(t, "[profiles.default]\n"+line+"\n")
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: err = %v, want %q", line, err, want)
		}
	}
	if _, err := loadTOML(t, "[profiles.default]\nmenu_pointers = [\"❯\", \">\"]\nmenu_selected_styles = [\"bold\"]\nmenu_submit = \"\"\n"); err != nil {
		t.Errorf("valid config rejected: %v", err)
	}
}

// The screen package is the source of the built-in pointers; the config
// default must not drift (">" is intentionally not a default).
func TestMenuPointersMatchScreenDefaults(t *testing.T) {
	got := Default().Profile("default").Menu.Pointers
	want := screen.DefaultMenuPointers
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("config default pointers %q != screen.DefaultMenuPointers %q", got, want)
	}
	for _, p := range got {
		if p == ">" {
			t.Error(`">" must not be a default pointer`)
		}
	}
}

func TestMenuSubmitResolvesToProfileSubmit(t *testing.T) {
	c, err := loadTOML(t, "[profiles.x]\nsubmit = \"\\r\\n\"\n[profiles.y]\nsubmit = \"\\r\\n\"\nmenu_submit = \"\\r\"\n")
	if err != nil {
		t.Fatal(err)
	}
	if got := c.Profile("x").Menu.Submit; got != "\r\n" {
		t.Errorf("empty menu_submit resolved to %q, want the profile submit", got)
	}
	if got := c.Profile("y").Menu.Submit; got != "\r" {
		t.Errorf("explicit menu_submit = %q", got)
	}
	if got := Default().Profile("claude").Menu.Submit; got != DefaultSubmit {
		t.Errorf("default menu submit = %q", got)
	}
}

func TestMenuNumberRegexMustMatchSample(t *testing.T) {
	for name, re := range map[string]string{
		"no match":    `^NOPE(?P<n>\d)(?P<label>.*)$`,
		"n is not 1":  `^\s*(?P<n>2)[.)]\s+(?P<label>\S.*)$`,
		"empty label": `^\s*(?P<n>\d)[.)](?P<label>\s*)$`,
	} {
		if _, err := loadTOML(t, "[profiles.x]\nmenu_number_regex = '"+re+"'\n"); err == nil || !strings.Contains(err.Error(), "menu_number_regex") {
			t.Errorf("%s: err = %v", name, err)
		}
	}
	if _, err := loadTOML(t, "[profiles.x]\nmenu_number_regex = '^\\s*(?P<n>\\d+)\\)\\s+(?P<label>\\S.*)$'\n"); err != nil {
		t.Errorf("valid custom regex rejected: %v", err)
	}
}

// Review 1a: menu_digit_action defaults (claude/codex select, others move)
// and can be overridden.
func TestMenuDigitAction(t *testing.T) {
	c := Default()
	for name, want := range map[string]string{"default": "move", "pando": "move", "unknown": "move", "claude": "select", "codex": "select"} {
		if got := c.Profile(name).Menu.DigitAction; got != want {
			t.Errorf("%s menu_digit_action = %q, want %q", name, got, want)
		}
	}
	c, err := loadTOML(t, "[profiles.claude]\nmenu_digit_action = \"move\"\n[profiles.mine]\nmenu_digit_action = \"select\"\n")
	if err != nil {
		t.Fatal(err)
	}
	if c.Profile("claude").Menu.DigitAction != "move" || c.Profile("mine").Menu.DigitAction != "select" || c.Profile("codex").Menu.DigitAction != "select" {
		t.Errorf("overrides: claude=%q mine=%q codex=%q", c.Profile("claude").Menu.DigitAction, c.Profile("mine").Menu.DigitAction, c.Profile("codex").Menu.DigitAction)
	}
}
