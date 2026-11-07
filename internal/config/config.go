package config

import (
	"errors"
	"fmt"
	"io/fs"
	"path"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/BurntSushi/toml"

	"github.com/digiogithub/agentd/internal/termcolor"
)

// DefaultProfile is the name of the fallback harness profile.
const DefaultProfile = "default"

// Built-in default values (AGENTS.md, "Idle detection" and "Injection").
const (
	DefaultIdleMS        = 1500
	DefaultHumanGraceMS  = 5000
	DefaultSubmit        = "\r"
	DefaultSubmitDelayMS = 150
	DefaultInlineMax     = 400
)

// ProfileConfig is a partial profile as written in config.toml. Nil fields
// are unset and inherit from the layer below.
type ProfileConfig struct {
	IdleMS        *int    `toml:"idle_ms"`
	HumanGraceMS  *int    `toml:"human_grace_ms"`
	PromptRegex   *string `toml:"prompt_regex"`
	Submit        *string `toml:"submit"`
	SubmitDelayMS *int    `toml:"submit_delay_ms"`
	InlineMax     *int    `toml:"inline_max"`

	// Ghost text (suggested prompt / placeholder) classification, see
	// AGENTS.md "Ghost text" and docs/ghost-text.md.
	GhostFaint         *bool     `toml:"ghost_faint"`
	GhostColors        *[]string `toml:"ghost_colors"`
	GhostRightOfCursor *bool     `toml:"ghost_right_of_cursor"`
	PromptMarker       *string   `toml:"prompt_marker"`
}

// DefaultLogMaxMB is the default session log cap ([run] log_max_mb).
const DefaultLogMaxMB = 50

// RunConfig is the [run] table of config.toml.
type RunConfig struct {
	// StripEnv lists variable names (path.Match globs) removed from the
	// child's base environment, on top of the always-stripped AGENTD_*,
	// CLAUDECODE and CLAUDE_CODE_ENTRYPOINT.
	StripEnv []string `toml:"strip_env"`
	// LogMaxMB caps each session log (rotated once to <log>.1); nil means
	// DefaultLogMaxMB, 0 disables the cap.
	LogMaxMB *int `toml:"log_max_mb"`
}

// LogMaxBytes returns the session log cap in bytes (0 = no cap).
func (r RunConfig) LogMaxBytes() int64 {
	mb := DefaultLogMaxMB
	if r.LogMaxMB != nil {
		mb = *r.LogMaxMB
	}
	return int64(mb) << 20
}

func (r RunConfig) validate() error {
	if r.LogMaxMB != nil && *r.LogMaxMB < 0 {
		return fmt.Errorf("run.log_max_mb must be >= 0, got %d", *r.LogMaxMB)
	}
	for _, g := range r.StripEnv {
		if _, err := path.Match(g, ""); err != nil || g == "" {
			return fmt.Errorf("run.strip_env: bad name or glob %q", g)
		}
	}
	return nil
}

// Mailbox retention defaults ([mailbox]).
const (
	DefaultRetention     = 5 * 24 * time.Hour
	DefaultPurgeInterval = time.Hour
	// MinPurgeInterval keeps a typo ("1s") from hammering the database.
	MinPurgeInterval = time.Minute
)

// MailboxConfig is the [mailbox] table of config.toml.
type MailboxConfig struct {
	// Retention deletes messages (any state) older than it; nil means
	// DefaultRetention, 0 disables automatic purging.
	Retention *Duration `toml:"retention"`
	// PurgeInterval is how often the retention purge runs; nil means
	// DefaultPurgeInterval.
	PurgeInterval *Duration `toml:"purge_interval"`
}

// RetentionDuration returns the effective retention (0 = disabled).
func (m MailboxConfig) RetentionDuration() time.Duration {
	if m.Retention == nil {
		return DefaultRetention
	}
	return time.Duration(*m.Retention)
}

// PurgeEvery returns the effective purge interval.
func (m MailboxConfig) PurgeEvery() time.Duration {
	if m.PurgeInterval == nil {
		return DefaultPurgeInterval
	}
	return time.Duration(*m.PurgeInterval)
}

func (m MailboxConfig) validate() error {
	if m.Retention != nil && *m.Retention < 0 {
		return fmt.Errorf("mailbox.retention must be >= 0, got %s", time.Duration(*m.Retention))
	}
	if m.PurgeInterval != nil && time.Duration(*m.PurgeInterval) < MinPurgeInterval {
		return fmt.Errorf("mailbox.purge_interval must be >= %s, got %s", MinPurgeInterval, time.Duration(*m.PurgeInterval))
	}
	return nil
}

// Duration is a time.Duration written in config.toml as a string accepted
// by ParseDuration ("120h", "90m", "5d", "1d12h", "0").
type Duration time.Duration

// UnmarshalText implements encoding.TextUnmarshaler.
func (d *Duration) UnmarshalText(b []byte) error {
	v, err := ParseDuration(string(b))
	if err != nil {
		return err
	}
	*d = Duration(v)
	return nil
}

// MarshalText implements encoding.TextMarshaler.
func (d Duration) MarshalText() ([]byte, error) { return []byte(time.Duration(d).String()), nil }

// ParseDuration parses a Go duration ("48h", "90m") that may also start with
// a whole number of days ("5d", "1d12h"). "0" is zero. Negative values are
// errors.
func ParseDuration(s string) (time.Duration, error) {
	in := strings.TrimSpace(s)
	if in == "" {
		return 0, errors.New("empty duration")
	}
	var days time.Duration
	rest := in
	if i := strings.IndexByte(rest, 'd'); i >= 0 {
		n := rest[:i]
		if n == "" || strings.Trim(n, "0123456789") != "" || len(n) > 5 {
			return 0, fmt.Errorf("invalid duration %q (want e.g. 120h, 90m, 5d or 1d12h)", s)
		}
		var v int64
		for _, c := range n {
			v = v*10 + int64(c-'0')
		}
		days = time.Duration(v) * 24 * time.Hour
		rest = rest[i+1:]
	}
	var d time.Duration
	if rest != "" {
		var err error
		if d, err = time.ParseDuration(rest); err != nil {
			return 0, fmt.Errorf("invalid duration %q (want e.g. 120h, 90m, 5d or 1d12h)", s)
		}
		if days > 0 && d < 0 {
			return 0, fmt.Errorf("invalid duration %q", s)
		}
	}
	if d < 0 {
		return 0, fmt.Errorf("duration %q must not be negative", s)
	}
	return days + d, nil
}

// Config is the parsed config.toml.
type Config struct {
	// Profiles maps profile name to its partial settings.
	Profiles map[string]ProfileConfig `toml:"profiles"`
	// ACL maps a recipient session name (or "*") to the sender globs
	// (path.Match syntax) allowed to message it.
	ACL map[string][]string `toml:"acl"`
	// Security holds identity-related switches.
	Security Security `toml:"security"`
	// Run holds session-start settings ([run]).
	Run RunConfig `toml:"run"`
	// Mailbox holds message retention settings ([mailbox]).
	Mailbox MailboxConfig `toml:"mailbox"`

	// Path is the file the config was loaded from ("" for defaults).
	Path string `toml:"-"`
	// Loaded reports whether the file existed.
	Loaded bool `toml:"-"`

	resolved map[string]*Profile
}

// Security is the [security] table.
type Security struct {
	// AgentType allows sessions (agents) to use the type op on other
	// sessions. Default false: only humans may type into another session.
	AgentType bool `toml:"agent_type"`
}

// Profile is a fully resolved harness profile.
type Profile struct {
	Name          string
	IdleMS        int
	HumanGraceMS  int
	PromptRegex   string // "" = no prompt check
	Submit        string
	SubmitDelayMS int
	InlineMax     int
	// Prompt is PromptRegex compiled; nil when PromptRegex is empty.
	Prompt *regexp.Regexp

	// GhostFaint classifies faint (SGR 2) cells of the input line as ghost
	// text (a suggested prompt or placeholder, never user input).
	GhostFaint bool
	// GhostColors lists foreground colours ("8", "244", "#808080") whose
	// cells on the input line are ghost text; GhostColorSpecs is it parsed.
	GhostColors     []string
	GhostColorSpecs []termcolor.Spec
	// GhostRightOfCursor treats the text right of the cursor as ghost text
	// when the cursor sits right after PromptMarker (with style rules
	// configured, a cell must match them too).
	GhostRightOfCursor bool
	// PromptMarker is the harness's input prompt glyph ("❯", "›"); "" = none.
	PromptMarker string
}

// Idle returns IdleMS as a duration.
func (p Profile) Idle() time.Duration { return ms(p.IdleMS) }

// HumanGrace returns HumanGraceMS as a duration.
func (p Profile) HumanGrace() time.Duration { return ms(p.HumanGraceMS) }

// SubmitDelay returns SubmitDelayMS as a duration.
func (p Profile) SubmitDelay() time.Duration { return ms(p.SubmitDelayMS) }

// PromptMatches reports whether the prompt check passes for screen: true
// when no prompt_regex is configured, else whether it matches.
func (p Profile) PromptMatches(screen string) bool {
	return p.Prompt == nil || p.Prompt.MatchString(screen)
}

func ms(n int) time.Duration { return time.Duration(n) * time.Millisecond }

func intp(n int) *int            { return &n }
func strp(s string) *string      { return &s }
func boolp(b bool) *bool         { return &b }
func strsp(s []string) *[]string { return &s }

// builtinProfiles are the profiles known without any config file.
//
// The claude and codex values come from captures of the real TUIs
// (docs/ghost-text.md): both draw the placeholder / suggested prompt faint,
// right after the prompt marker, with the cursor on its first cell. Claude
// Code follows "❯" with a no-break space (U+00A0). Their prompt_regex only
// checks that the harness's input line is on screen (it is also shown while
// the agent works), so idle detection still relies on output quiescence.
// Pando opened its first-run settings screen in the capture, so it keeps the
// generic defaults.
var builtinProfiles = map[string]ProfileConfig{
	DefaultProfile: {
		IdleMS:             intp(DefaultIdleMS),
		HumanGraceMS:       intp(DefaultHumanGraceMS),
		PromptRegex:        strp(""),
		Submit:             strp(DefaultSubmit),
		SubmitDelayMS:      intp(DefaultSubmitDelayMS),
		InlineMax:          intp(DefaultInlineMax),
		GhostFaint:         boolp(true),
		GhostColors:        strsp(nil),
		GhostRightOfCursor: boolp(false),
		PromptMarker:       strp(""),
	},
	"claude": {
		PromptRegex:        strp(`(?m)^❯([ \x{a0}]|$)`),
		GhostRightOfCursor: boolp(true),
		PromptMarker:       strp("❯"),
	},
	"codex": {
		PromptRegex:        strp(`(?m)^›( |$)`),
		GhostRightOfCursor: boolp(true),
		PromptMarker:       strp("›"),
	},
	"pando": {},
}

// BuiltinProfileNames returns the names of the built-in profiles, sorted.
func BuiltinProfileNames() []string {
	names := make([]string, 0, len(builtinProfiles))
	for n := range builtinProfiles {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

// Load reads and validates the config file at p. A missing file yields the
// defaults (Loaded=false). Invalid TOML, unknown keys, negative values, an
// invalid prompt_regex or an invalid ACL glob are errors.
func Load(p string) (*Config, error) {
	c := &Config{Path: p}
	md, err := toml.DecodeFile(p, c)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			c = &Config{Path: p}
			return c, c.init()
		}
		return nil, fmt.Errorf("config: load %s: %w", p, err)
	}
	c.Loaded = true
	if und := md.Undecoded(); len(und) > 0 {
		return nil, fmt.Errorf("config: load %s: unknown keys %v", p, und)
	}
	if err := c.init(); err != nil {
		return nil, fmt.Errorf("config: load %s: %w", p, err)
	}
	return c, nil
}

// LoadDefault loads the config from ConfigPath().
func LoadDefault() (*Config, error) {
	p, err := ConfigPath()
	if err != nil {
		return nil, err
	}
	return Load(p)
}

// Default returns a config with only the built-in settings.
func Default() *Config {
	c := &Config{}
	if err := c.init(); err != nil {
		panic(err) // built-ins are static and valid
	}
	return c
}

// init validates the config and resolves every known profile once.
func (c *Config) init() error {
	if err := c.Run.validate(); err != nil {
		return err
	}
	if err := c.Mailbox.validate(); err != nil {
		return err
	}
	for name, pc := range c.Profiles {
		if err := pc.validate(); err != nil {
			return fmt.Errorf("profiles.%s: %w", name, err)
		}
	}
	for recipient, globs := range c.ACL {
		for _, g := range globs {
			if _, err := path.Match(g, ""); err != nil {
				return fmt.Errorf("acl %q: bad glob %q: %w", recipient, g, err)
			}
		}
	}
	c.resolved = map[string]*Profile{}
	for name := range builtinProfiles {
		if err := c.resolve(name); err != nil {
			return err
		}
	}
	for name := range c.Profiles {
		if err := c.resolve(name); err != nil {
			return err
		}
	}
	return nil
}

func (pc ProfileConfig) validate() error {
	for _, f := range []struct {
		key string
		v   *int
	}{
		{"idle_ms", pc.IdleMS},
		{"human_grace_ms", pc.HumanGraceMS},
		{"submit_delay_ms", pc.SubmitDelayMS},
		{"inline_max", pc.InlineMax},
	} {
		if f.v != nil && *f.v < 0 {
			return fmt.Errorf("%s must be >= 0, got %d", f.key, *f.v)
		}
	}
	if pc.GhostColors != nil {
		if _, err := termcolor.ParseAll(*pc.GhostColors); err != nil {
			return fmt.Errorf("ghost_colors: %w", err)
		}
	}
	if pc.PromptMarker != nil && strings.ContainsAny(*pc.PromptMarker, "\r\n") {
		return errors.New("prompt_marker must be a single line")
	}
	return nil
}

// resolve builds profile name by layering, lowest to highest precedence:
// built-in default, built-in <name>, file profiles.default, file profiles.<name>.
func (c *Config) resolve(name string) error {
	var pc ProfileConfig
	layers := []ProfileConfig{builtinProfiles[DefaultProfile]}
	if name != DefaultProfile {
		layers = append(layers, builtinProfiles[name])
	}
	layers = append(layers, c.Profiles[DefaultProfile])
	if name != DefaultProfile {
		layers = append(layers, c.Profiles[name])
	}
	for _, l := range layers {
		pc.merge(l)
	}
	p := &Profile{
		Name:          name,
		IdleMS:        *pc.IdleMS,
		HumanGraceMS:  *pc.HumanGraceMS,
		PromptRegex:   *pc.PromptRegex,
		Submit:        *pc.Submit,
		SubmitDelayMS: *pc.SubmitDelayMS,
		InlineMax:     *pc.InlineMax,

		GhostFaint:         *pc.GhostFaint,
		GhostColors:        append([]string(nil), *pc.GhostColors...),
		GhostRightOfCursor: *pc.GhostRightOfCursor,
		PromptMarker:       *pc.PromptMarker,
	}
	specs, err := termcolor.ParseAll(p.GhostColors)
	if err != nil {
		return fmt.Errorf("profiles.%s: ghost_colors: %w", name, err)
	}
	p.GhostColorSpecs = specs
	if p.PromptRegex != "" {
		re, err := regexp.Compile(p.PromptRegex)
		if err != nil {
			return fmt.Errorf("profiles.%s: invalid prompt_regex: %w", name, err)
		}
		p.Prompt = re
	}
	c.resolved[name] = p
	return nil
}

func (pc *ProfileConfig) merge(o ProfileConfig) {
	if o.IdleMS != nil {
		pc.IdleMS = o.IdleMS
	}
	if o.HumanGraceMS != nil {
		pc.HumanGraceMS = o.HumanGraceMS
	}
	if o.PromptRegex != nil {
		pc.PromptRegex = o.PromptRegex
	}
	if o.Submit != nil {
		pc.Submit = o.Submit
	}
	if o.SubmitDelayMS != nil {
		pc.SubmitDelayMS = o.SubmitDelayMS
	}
	if o.InlineMax != nil {
		pc.InlineMax = o.InlineMax
	}
	if o.GhostFaint != nil {
		pc.GhostFaint = o.GhostFaint
	}
	if o.GhostColors != nil {
		pc.GhostColors = o.GhostColors
	}
	if o.GhostRightOfCursor != nil {
		pc.GhostRightOfCursor = o.GhostRightOfCursor
	}
	if o.PromptMarker != nil {
		pc.PromptMarker = o.PromptMarker
	}
}

// Profile returns the resolved profile for name. An empty or unknown name
// (neither built-in nor defined in the file) yields the default profile,
// whose Name is "default". The returned value is a copy safe to modify.
// A nil *Config behaves like Default().
func (c *Config) Profile(name string) Profile {
	if c == nil || c.resolved == nil {
		c = Default()
	}
	p, ok := c.resolved[name]
	if !ok {
		p = c.resolved[DefaultProfile]
	}
	cp := *p
	cp.GhostColors = append([]string(nil), p.GhostColors...)
	cp.GhostColorSpecs = append([]termcolor.Spec(nil), p.GhostColorSpecs...)
	return cp
}

// HasProfile reports whether name is a built-in or file-defined profile.
func (c *Config) HasProfile(name string) bool {
	if _, ok := builtinProfiles[name]; ok {
		return true
	}
	if c == nil {
		return false
	}
	_, ok := c.Profiles[name]
	return ok
}

// Allowed reports whether sender may message recipient. The ACL entry for
// recipient is used, else the "*" entry, else everything is allowed. Sender
// globs use path.Match syntax (e.g. "human:*", "*@models"). An entry with an
// empty list denies all senders. A nil *Config allows everything.
func (c *Config) Allowed(recipient, sender string) bool {
	if c == nil {
		return true
	}
	globs, ok := c.ACL[recipient]
	if !ok {
		globs, ok = c.ACL["*"]
	}
	if !ok {
		return true
	}
	for _, g := range globs {
		if m, err := path.Match(g, sender); err == nil && m {
			return true
		}
	}
	return false
}
