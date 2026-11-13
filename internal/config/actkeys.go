package config

import "fmt"

// Act key profile keys (op act, docs/design/api/daemon-ops.md §3.1):
// interrupt_keys and cancel_keys are lists of abstract key names (the table
// in internal/daemon/keys.go, e.g. "esc", "ctrl-c"). Names are validated when
// the action runs, not here (the table lives in the daemon).
//
// Defaults: interrupt = ["ctrl-c"] for the default and pando profiles,
// ["esc"] for claude and codex (TO CONFIRM against the real TUIs, plan F8:
// both stop the running turn on Esc, Ctrl-C twice quits them); cancel =
// ["esc"] everywhere.

// MaxActKeys bounds the keys of one act request and of a profile key list.
const MaxActKeys = 32

// ActConfig is the partial (config.toml) form, embedded in ProfileConfig.
type ActConfig struct {
	InterruptKeys *[]string `toml:"interrupt_keys"`
	CancelKeys    *[]string `toml:"cancel_keys"`
}

// ActProfile is the resolved form, Profile.Act.
type ActProfile struct {
	InterruptKeys []string
	CancelKeys    []string
}

func defaultActConfig() ActConfig {
	return ActConfig{InterruptKeys: strsp([]string{"ctrl-c"}), CancelKeys: strsp([]string{"esc"})}
}

func (a *ActConfig) merge(o ActConfig) {
	if o.InterruptKeys != nil {
		a.InterruptKeys = o.InterruptKeys
	}
	if o.CancelKeys != nil {
		a.CancelKeys = o.CancelKeys
	}
}

func (a ActConfig) validate() error {
	for _, kv := range []struct {
		name string
		v    *[]string
	}{{"interrupt_keys", a.InterruptKeys}, {"cancel_keys", a.CancelKeys}} {
		if kv.v == nil {
			continue
		}
		if len(*kv.v) == 0 || len(*kv.v) > MaxActKeys {
			return fmt.Errorf("%s: want 1..%d key names", kv.name, MaxActKeys)
		}
		for _, k := range *kv.v {
			if k == "" {
				return fmt.Errorf("%s: empty key name", kv.name)
			}
		}
	}
	return nil
}

func (a ActConfig) resolve() ActProfile {
	return ActProfile{
		InterruptKeys: append([]string(nil), *a.InterruptKeys...),
		CancelKeys:    append([]string(nil), *a.CancelKeys...),
	}
}

func (a ActProfile) clone() ActProfile {
	return ActProfile{
		InterruptKeys: append([]string(nil), a.InterruptKeys...),
		CancelKeys:    append([]string(nil), a.CancelKeys...),
	}
}
