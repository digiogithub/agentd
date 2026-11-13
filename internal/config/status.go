package config

import "regexp"

// Status classifier profile key, design: docs/design/api/semantic-model.md §5.
// waiting_regex has no built-in value: it marks screens that wait for the
// user but show no detectable option list (a free-text question, a "press
// any key" line). It is data; internal/daemon passes it to screen.Classify.

// StatusConfig is the partial (config.toml) form, embedded in ProfileConfig.
type StatusConfig struct {
	WaitingRegex *string `toml:"waiting_regex"`
}

// StatusProfile is the resolved form, Profile.Status.
type StatusProfile struct {
	WaitingRegex string
	// Waiting is WaitingRegex compiled; nil when empty.
	Waiting *regexp.Regexp
}

func defaultStatusConfig() StatusConfig { return StatusConfig{WaitingRegex: strp("")} }

func (s *StatusConfig) merge(o StatusConfig) {
	if o.WaitingRegex != nil {
		s.WaitingRegex = o.WaitingRegex
	}
}

func (s StatusConfig) validate() error {
	if s.WaitingRegex != nil {
		if _, err := stateRegex("waiting_regex", *s.WaitingRegex, true); err != nil {
			return err
		}
	}
	return nil
}

func (s StatusConfig) resolve() (StatusProfile, error) {
	p := StatusProfile{WaitingRegex: *s.WaitingRegex}
	var err error
	p.Waiting, err = stateRegex("waiting_regex", p.WaitingRegex, true)
	return p, err
}
