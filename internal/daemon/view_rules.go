package daemon

import (
	"time"

	"github.com/digiogithub/agentd/internal/config"
	"github.com/digiogithub/agentd/internal/screen"
)

// screenRules maps a resolved harness profile to the data driving the screen
// interpretation (reflow, region split, option detection): ghost rules, the
// prompt regex, the region keys and the menu keys. Pure; the daemon compiles
// it once per session.
func screenRules(p config.Profile) screen.Rules {
	r := screen.DefaultRules()
	r.Ghost = screen.GhostRules{
		Faint:         p.GhostFaint,
		Colors:        p.GhostColorSpecs,
		RightOfCursor: p.GhostRightOfCursor,
		PromptMarker:  p.PromptMarker,
	}
	r.PromptRegex = p.Prompt
	rg := p.Regions
	r.InputBgColors = rg.InputBgSpecs
	if rg.RuleChars != "" {
		r.RuleChars = rg.RuleChars
	}
	r.FooterRegex = rg.Footer
	r.BusyRegex = rg.Busy
	r.SpinnerChars = rg.SpinnerChars
	r.NoJoin = rg.ReflowBreak
	r.WrapMargin = rg.WrapMargin
	if rg.DialogContextRows > 0 {
		r.DialogContextRows = rg.DialogContextRows
	}
	m := p.Menu
	if len(m.Pointers) > 0 {
		r.Menu.Pointers = append([]string(nil), m.Pointers...)
	}
	if m.Number != nil {
		r.Menu.NumberRegex = m.Number
	}
	if len(m.SelectedStyles) > 0 {
		r.Menu.SelectedStyles = append([]string(nil), m.SelectedStyles...)
	}
	return r
}

// chooseKeys maps the profile's menu_* key settings to the key plan settings
// used when choosing an option; an empty menu_submit resolves to the profile's
// submit sequence. Not used by view itself (options never expose keys); it is
// here so the act op shares one mapping.
func chooseKeys(p config.Profile) screen.ChooseKeys {
	submit := p.Menu.Submit
	if submit == "" {
		submit = p.Submit
	}
	return screen.ChooseKeys{
		Select: p.Menu.Select, DigitAction: p.Menu.DigitAction, NavUp: p.Menu.NavUp, NavDown: p.Menu.NavDown,
		Submit: submit, Toggle: p.Menu.ToggleKey,
	}
}

// statusInput builds the classifier input for profile p at the given time.
func statusInput(p config.Profile, rules screen.Rules, now time.Time, promptMatch bool) screen.StatusInput {
	return screen.StatusInput{
		Now:                 now,
		Idle:                p.Idle(),
		HumanGrace:          p.HumanGrace(),
		PromptRegexSet:      p.Prompt != nil,
		PromptMatch:         promptMatch,
		BusyRegex:           rules.BusyRegex,
		WaitingRegex:        p.Status.Waiting,
		OptionMinConfidence: rules.Menu.MinConfidence,
	}
}
