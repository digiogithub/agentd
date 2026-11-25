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
	// DefaultCols and DefaultRows are the fixed PTY size of daemon-started
	// sessions (profile cols/rows); see AGENTS.md "PTY size".
	DefaultCols = 160
	DefaultRows = 50
	// MaxPTYDim bounds profile cols/rows.
	MaxPTYDim = 1000
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
	// PastePlaceholderRegex matches the placeholder a harness draws in its
	// input line instead of a long paste (Claude Code: "[Pasted text #1 +12
	// lines]"); a new match after a paste verifies it. "" = none.
	PastePlaceholderRegex *string `toml:"paste_placeholder_regex"`
	// Cols and Rows are the fixed PTY size of sessions started without an
	// explicit size (default 160x50).
	Cols *int `toml:"cols"`
	Rows *int `toml:"rows"`

	// Ghost text (suggested prompt / placeholder) classification, see
	// AGENTS.md "Ghost text" and docs/ghost-text.md.
	GhostFaint         *bool     `toml:"ghost_faint"`
	GhostColors        *[]string `toml:"ghost_colors"`
	GhostRightOfCursor *bool     `toml:"ghost_right_of_cursor"`
	PromptMarker       *string   `toml:"prompt_marker"`

	// Menu / option detection keys (menu_*), see menu.go.
	MenuConfig

	// Region / reflow keys (rule_chars, input_bg_colors, ...), see regions.go.
	RegionConfig

	// Status classifier key (waiting_regex), see status.go.
	StatusConfig

	// Act key lists (interrupt_keys, cancel_keys), see actkeys.go.
	ActConfig
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
	// RestoreSizeOnDetach resizes a session back to its start size (the
	// profile's cols/rows unless `run` gave one) when the last attached client
	// detaches; nil means true.
	RestoreSizeOnDetach *bool `toml:"restore_size_on_detach"`
}

// RestoreSize reports whether sessions are resized back when the last client
// detaches (default true).
func (r RunConfig) RestoreSize() bool {
	return r.RestoreSizeOnDetach == nil || *r.RestoreSizeOnDetach
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
	// Claim maps a work queue name (without "queue:", or "*") to the
	// session globs allowed to claim its items ([claim]). Posting to a queue
	// is decided by ACL["queue:<name>"], like any recipient.
	Claim map[string][]string `toml:"claim"`
	// Security holds identity-related switches.
	Security Security `toml:"security"`
	// Run holds session-start settings ([run]).
	Run RunConfig `toml:"run"`
	// Mailbox holds message retention settings ([mailbox]).
	Mailbox MailboxConfig `toml:"mailbox"`
	// API holds the HTTP gateway settings ([api], see api.go).
	API APIConfig `toml:"api"`
	// Herdr holds the Herdr client compatibility settings ([herdr]).
	Herdr HerdrConfig `toml:"herdr"`
	// Relay holds the relay transport settings ([relay], see relay.go).
	Relay RelayConfig `toml:"relay"`
	// Notify is [notify]: the daemon's own ntfy / webhook notifications
	// (same keys as [api.notify]; reasons question, waiting, blocked,
	// turn_done, exited). Off by default.
	Notify NotifyConfig `toml:"notify"`
	// Push is [push]: FCM push notifications to registered apps (push.go).
	Push PushConfig `toml:"push"`

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
	// AgentView allows sessions (agents) to use the view op on other
	// sessions. Default false: only humans may view another session.
	AgentView bool `toml:"agent_view"`
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
	// Cols and Rows are the fixed PTY size for sessions started without an
	// explicit size.
	Cols, Rows int
	// Prompt is PromptRegex compiled; nil when PromptRegex is empty.
	Prompt *regexp.Regexp
	// PastePlaceholderRegex matches the placeholder the harness shows in its
	// input line instead of a long paste ("" = none); PastePlaceholder is it
	// compiled.
	PastePlaceholderRegex string
	PastePlaceholder      *regexp.Regexp

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

	// Menu holds the resolved menu_* keys (option detection).
	Menu MenuProfile
	// Regions holds the resolved region / reflow keys.
	Regions RegionProfile
	// Status holds the resolved status classifier keys.
	Status StatusProfile
	// Act holds the resolved interrupt / cancel key lists.
	Act ActProfile
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

// ClaudeInfoRegex is the built-in claude info_regex.
const ClaudeInfoRegex = `^[^\s\w●⏺❯›>⎿│] \p{Lu}[\p{L}'’ -]*…( \(.*\))?$|^\d[\d.,]*[kKmM]? tokens$|^⎿\s+Tip: `

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
		IdleMS:                intp(DefaultIdleMS),
		HumanGraceMS:          intp(DefaultHumanGraceMS),
		PromptRegex:           strp(""),
		PastePlaceholderRegex: strp(""),
		Submit:                strp(DefaultSubmit),
		SubmitDelayMS:         intp(DefaultSubmitDelayMS),
		InlineMax:             intp(DefaultInlineMax),
		Cols:                  intp(DefaultCols),
		Rows:                  intp(DefaultRows),
		GhostFaint:            boolp(true),
		GhostColors:           strsp(nil),
		GhostRightOfCursor:    boolp(false),
		PromptMarker:          strp(""),
		MenuConfig:            defaultMenuConfig(),
		RegionConfig:          defaultRegionConfig(),
		StatusConfig:          defaultStatusConfig(),
		ActConfig:             defaultActConfig(),
	},
	"claude": {
		PromptRegex: strp(`(?m)^❯([ \x{a0}]|$)`),
		// Claude Code collapses a long or multi-line paste into
		// "[Pasted text #N]" or "[Pasted text #N +M lines]" (seen in session
		// logs, v2.1.x).
		PastePlaceholderRegex: strp(`\[Pasted text #\d+( \+\d+ lines)?\]`),
		GhostRightOfCursor:    boolp(true),
		PromptMarker:          strp("❯"),
		MenuConfig:            MenuConfig{MenuPointers: strsp([]string{"❯"}), MenuDigitAction: strp(MenuDigitSelect)}, // digit action to confirm (F8)
		ActConfig:             ActConfig{InterruptKeys: strsp([]string{"esc"})},                                       // to confirm (F8)
		// Rows Claude Code keeps between the transcript and its input box
		// (seen in session logs, v2.1.x): the spinner row ("✶ Brewing… (23s ·
		// ↓ 1.4k tokens)"), the context counter ("134920 tokens") and the tip
		// under the spinner. They change on every frame, so as transcript
		// they broke the alignment of the history. Not busy_regex: the
		// status comes from the output the spinner itself produces.
		//
		// Claude Code draws fenced code blocks without the fences, syntax
		// highlighted in ANSI palette colours; its inline code and its own
		// chrome use truecolor theme colours.
		RegionConfig: RegionConfig{InfoRegex: strp(ClaudeInfoRegex), CodeANSIColors: boolp(true)},
	},
	"codex": {
		PromptRegex:        strp(`(?m)^›( |$)`),
		GhostRightOfCursor: boolp(true),
		PromptMarker:       strp("›"),
		MenuConfig:         MenuConfig{MenuPointers: strsp([]string{"›"}), MenuDigitAction: strp(MenuDigitSelect)}, // digit action to confirm (F8)
		ActConfig:          ActConfig{InterruptKeys: strsp([]string{"esc"})},                                       // to confirm (F8)
		// Codex draws its input row on a #1e1e1e background (docs/ghost-text.md).
		RegionConfig: RegionConfig{InputBgColors: strsp([]string{"#1e1e1e"})},
	},
	// Pando's TUI draws a tab bar on the first row ("Pando  Chat  Editor
	// Editor+Chat  v1.5.5"), an info panel on the right and a status bar
	// ("ctrl+h help ... No diagnostics") on the last row; all are chrome.
	"pando": {RegionConfig: RegionConfig{
		HeaderRegex: strp(`\bChat\b.*\bEditor\+Chat\b`),
		FooterRegex: strp(`ctrl\+h help`),
		// The spinner row ("▓ Thinking...") and the hint above the input
		// ("press enter to send the message...") are status, not transcript.
		BusyRegex: strp(`Thinking\.\.\.|press enter to`),
	}},
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
	if err := c.Herdr.validate(); err != nil {
		return err
	}
	if err := c.Relay.validate(); err != nil {
		return err
	}
	c.API.applyDefaults()
	if err := c.API.Validate(); err != nil {
		return err
	}
	c.Notify.applyDefaultsOn(DefaultDaemonNotifyOn)
	if err := c.Notify.validateAs("notify", daemonNotifyReasons, "question, waiting, blocked, turn_done, exited"); err != nil {
		return err
	}
	c.Push.applyDefaults()
	if err := c.Push.validate(); err != nil {
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
	for queue, globs := range c.Claim {
		for _, g := range globs {
			if _, err := path.Match(g, ""); err != nil {
				return fmt.Errorf("claim %q: bad glob %q: %w", queue, g, err)
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
	return c.validateAPIHarnesses()
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
	for _, f := range []struct {
		key string
		v   *int
	}{{"cols", pc.Cols}, {"rows", pc.Rows}} {
		if f.v != nil && (*f.v < 1 || *f.v > MaxPTYDim) {
			return fmt.Errorf("%s must be between 1 and %d, got %d", f.key, MaxPTYDim, *f.v)
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
	if err := pc.MenuConfig.validate(); err != nil {
		return err
	}
	if err := pc.ActConfig.validate(); err != nil {
		return err
	}
	if err := pc.StatusConfig.validate(); err != nil {
		return err
	}
	if err := pc.RegionConfig.validate(); err != nil {
		return err
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
		Name:                  name,
		IdleMS:                *pc.IdleMS,
		HumanGraceMS:          *pc.HumanGraceMS,
		PromptRegex:           *pc.PromptRegex,
		PastePlaceholderRegex: *pc.PastePlaceholderRegex,
		Submit:                *pc.Submit,
		SubmitDelayMS:         *pc.SubmitDelayMS,
		InlineMax:             *pc.InlineMax,
		Cols:                  *pc.Cols,
		Rows:                  *pc.Rows,

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
	menu, err := pc.MenuConfig.resolve()
	if err != nil {
		return fmt.Errorf("profiles.%s: %w", name, err)
	}
	if menu.Submit == "" {
		menu.Submit = p.Submit // menu_submit "" = the profile's submit key
	}
	p.Menu = menu
	regions, err := pc.RegionConfig.resolve()
	if err != nil {
		return fmt.Errorf("profiles.%s: %w", name, err)
	}
	p.Regions = regions
	status, err := pc.StatusConfig.resolve()
	if err != nil {
		return fmt.Errorf("profiles.%s: %w", name, err)
	}
	p.Status = status
	p.Act = pc.ActConfig.resolve()
	if p.PromptRegex != "" {
		re, err := regexp.Compile(p.PromptRegex)
		if err != nil {
			return fmt.Errorf("profiles.%s: invalid prompt_regex: %w", name, err)
		}
		p.Prompt = re
	}
	if p.PastePlaceholderRegex != "" {
		re, err := regexp.Compile(p.PastePlaceholderRegex)
		if err != nil {
			return fmt.Errorf("profiles.%s: invalid paste_placeholder_regex: %w", name, err)
		}
		p.PastePlaceholder = re
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
	if o.PastePlaceholderRegex != nil {
		pc.PastePlaceholderRegex = o.PastePlaceholderRegex
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
	if o.Cols != nil {
		pc.Cols = o.Cols
	}
	if o.Rows != nil {
		pc.Rows = o.Rows
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
	pc.MenuConfig.merge(o.MenuConfig)
	pc.RegionConfig.merge(o.RegionConfig)
	pc.StatusConfig.merge(o.StatusConfig)
	pc.ActConfig.merge(o.ActConfig)
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
	cp.Menu = p.Menu.clone()
	cp.Regions = p.Regions.clone()
	cp.Act = p.Act.clone()
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

// ClaimAllowed reports whether session may claim items of work queue (the
// name without "queue:"). The [claim] entry for queue is used, else the "*"
// entry, else every session may claim. An entry with an empty list denies
// all. A nil *Config allows everything.
func (c *Config) ClaimAllowed(queue, session string) bool {
	if c == nil {
		return true
	}
	globs, ok := c.Claim[queue]
	if !ok {
		globs, ok = c.Claim["*"]
	}
	if !ok {
		return true
	}
	for _, g := range globs {
		if m, err := path.Match(g, session); err == nil && m {
			return true
		}
	}
	return false
}

// ProfileNames returns the names of every profile (built-in and defined in
// the file, "default" included), sorted.
func (c *Config) ProfileNames() []string {
	seen := map[string]bool{}
	for n := range builtinProfiles {
		seen[n] = true
	}
	if c != nil {
		for n := range c.Profiles {
			seen[n] = true
		}
	}
	names := make([]string, 0, len(seen))
	for n := range seen {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}
