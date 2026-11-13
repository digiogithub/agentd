package config

import (
	"strings"
	"testing"

	"github.com/digiogithub/agentd/internal/screen"
)

func TestRegionDefaults(t *testing.T) {
	c := Default()
	d := c.Profile("default").Regions
	if d.RuleChars != "─━═╌┄" || d.WrapMargin != 2 || d.DialogContextRows != 6 || len(d.InputBgSpecs) != 0 || d.Footer != nil || d.Busy != nil || d.ReflowBreak != nil {
		t.Fatalf("default regions = %+v", d)
	}
	if got := c.Profile("codex").Regions; len(got.InputBgColors) != 1 || got.InputBgColors[0] != "#1e1e1e" || len(got.InputBgSpecs) != 1 {
		t.Errorf("codex regions = %+v", got)
	}
	if got := c.Profile("claude").Regions.InputBgColors; len(got) != 0 {
		t.Errorf("claude input_bg_colors = %q", got)
	}
	if DefaultRuleChars != screen.DefaultRuleChars || DefaultWrapMargin != screen.DefaultWrapMargin || DefaultDialogContextRows != screen.DefaultDialogContextRows {
		t.Error("region defaults drifted from the screen package")
	}
	p := c.Profile("codex")
	p.Regions.InputBgColors[0] = "x"
	if c.Profile("codex").Regions.InputBgColors[0] != "#1e1e1e" {
		t.Error("Profile() leaked shared slice")
	}
}

func TestRegionOverrides(t *testing.T) {
	c, err := loadTOML(t, `[profiles.default]
wrap_margin = 4
footer_regex = "(?i)for shortcuts"
[profiles.mine]
busy_regex = "esc to interrupt"
spinner_chars = "✻✳"
reflow_break_regex = "^[⏺●] "
input_bg_colors = ["#101010", "236"]
dialog_context_rows = 3
`)
	if err != nil {
		t.Fatal(err)
	}
	p := c.Profile("mine").Regions
	if p.WrapMargin != 4 || p.Footer == nil || !p.Footer.MatchString("? for shortcuts") || p.Busy == nil || p.SpinnerChars != "✻✳" ||
		p.ReflowBreak == nil || len(p.InputBgSpecs) != 2 || p.DialogContextRows != 3 {
		t.Fatalf("mine = %+v", p)
	}
	if c.Profile("claude").Regions.WrapMargin != 4 {
		t.Error("default layer not inherited by claude")
	}
}

func TestRegionValidation(t *testing.T) {
	for key, body := range map[string]string{
		"rule_chars":                        `rule_chars = "a"`,
		"rule_chars empty":                  `rule_chars = ""`,
		"spinner_chars":                     `spinner_chars = "中"`,
		"input_bg_colors":                   `input_bg_colors = ["nope"]`,
		"footer_regex":                      `footer_regex = "("`,
		"busy_regex":                        `busy_regex = "x*"`,
		"reflow_break_regex":                `reflow_break_regex = "["`,
		"reflow_break_regex empty-matching": `reflow_break_regex = "x*"`,
		"wrap_margin":                       `wrap_margin = 9`,
		"dialog_context_rows":               `dialog_context_rows = 21`,
	} {
		_, err := loadTOML(t, "[profiles.x]\n"+body+"\n")
		if err == nil {
			t.Errorf("%s: expected an error", key)
			continue
		}
		if !strings.Contains(err.Error(), strings.Fields(key)[0]) {
			t.Errorf("%s: error does not name the key: %v", key, err)
		}
	}
}

func TestWaitingRegex(t *testing.T) {
	if c := Default(); c.Profile("claude").Status.Waiting != nil || c.Profile("default").Status.WaitingRegex != "" {
		t.Fatal("waiting_regex must default to unset")
	}
	c, err := loadTOML(t, "[profiles.default]\nwaiting_regex = \"(?i)press any key\"\n[profiles.mine]\nwaiting_regex = \"\\\\? $\"\n")
	if err != nil {
		t.Fatal(err)
	}
	if re := c.Profile("claude").Status.Waiting; re == nil || !re.MatchString("Press any key") {
		t.Errorf("default layer not inherited: %v", re)
	}
	if re := c.Profile("mine").Status.Waiting; re == nil || !re.MatchString("ok? ") {
		t.Errorf("mine: %v", re)
	}
	for name, body := range map[string]string{"invalid": `waiting_regex = "("`, "empty match": `waiting_regex = "x*"`} {
		if _, err := loadTOML(t, "[profiles.default]\n"+body+"\n"); err == nil || !strings.Contains(err.Error(), "waiting_regex") {
			t.Errorf("%s: err = %v", name, err)
		}
	}
}
