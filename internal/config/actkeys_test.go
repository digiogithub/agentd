package config

import (
	"slices"
	"testing"
)

func TestActKeyDefaults(t *testing.T) {
	c := Default()
	for name, want := range map[string][]string{
		"default": {"ctrl-c"}, "pando": {"ctrl-c"}, "claude": {"esc"}, "codex": {"esc"},
	} {
		p := c.Profile(name)
		if !slices.Equal(p.Act.InterruptKeys, want) || !slices.Equal(p.Act.CancelKeys, []string{"esc"}) {
			t.Errorf("%s act keys = %+v", name, p.Act)
		}
	}
	// Profile() returns a copy.
	p := c.Profile("claude")
	p.Act.InterruptKeys[0] = "x"
	if c.Profile("claude").Act.InterruptKeys[0] != "esc" {
		t.Error("Profile shares the key slice")
	}
}

func TestActKeyValidation(t *testing.T) {
	for _, bad := range []ActConfig{
		{InterruptKeys: strsp(nil)},
		{CancelKeys: strsp([]string{""})},
		{InterruptKeys: strsp(make([]string, MaxActKeys+1))},
	} {
		if bad.validate() == nil {
			t.Errorf("%+v accepted", bad)
		}
	}
}
