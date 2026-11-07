package config

import (
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func writeConfig(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestSocketPath(t *testing.T) {
	tmp := t.TempDir()
	tests := []struct {
		name    string
		socket  string
		runtime string
		want    string
	}{
		{"override", filepath.Join(tmp, "x.sock"), "/run/user/1", filepath.Join(tmp, "x.sock")},
		{"xdg runtime", "", "/run/user/1", "/run/user/1/agentd/agentd.sock"},
		{"fallback", "", "", filepath.Join(os.TempDir(), "agentd-"+strconv.Itoa(os.Getuid()), "agentd.sock")},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv(EnvSocket, tt.socket)
			t.Setenv("XDG_RUNTIME_DIR", tt.runtime)
			if got := SocketPath(); got != tt.want {
				t.Fatalf("SocketPath() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestStateAndConfigPaths(t *testing.T) {
	home := t.TempDir()
	tests := []struct {
		name                 string
		env                  map[string]string
		wantState, wantConfg string
	}{
		{
			name:      "overrides",
			env:       map[string]string{EnvStateDir: "/s", EnvConfig: "/c.toml", "XDG_STATE_HOME": "/x", "XDG_CONFIG_HOME": "/y"},
			wantState: "/s", wantConfg: "/c.toml",
		},
		{
			name:      "xdg",
			env:       map[string]string{"XDG_STATE_HOME": "/x", "XDG_CONFIG_HOME": "/y"},
			wantState: "/x/agentd", wantConfg: "/y/agentd/config.toml",
		},
		{
			name:      "home fallback",
			env:       map[string]string{},
			wantState: filepath.Join(home, ".local/state/agentd"), wantConfg: filepath.Join(home, ".config/agentd/config.toml"),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("HOME", home)
			for _, k := range []string{EnvStateDir, EnvConfig, "XDG_STATE_HOME", "XDG_CONFIG_HOME"} {
				t.Setenv(k, tt.env[k])
			}
			st, err := StateDir()
			if err != nil || st != tt.wantState {
				t.Fatalf("StateDir() = %q, %v; want %q", st, err, tt.wantState)
			}
			cp, err := ConfigPath()
			if err != nil || cp != tt.wantConfg {
				t.Fatalf("ConfigPath() = %q, %v; want %q", cp, err, tt.wantConfg)
			}
			for fn, want := range map[string]string{
				"mailbox": filepath.Join(tt.wantState, "mailbox.db"),
				"logs":    filepath.Join(tt.wantState, "logs"),
				"daemon":  filepath.Join(tt.wantState, "daemon.log"),
				"session": filepath.Join(tt.wantState, "logs", "codex@models.log"),
			} {
				var got string
				switch fn {
				case "mailbox":
					got, err = MailboxPath()
				case "logs":
					got, err = LogDir()
				case "daemon":
					got, err = DaemonLogPath()
				case "session":
					got, err = SessionLogPath("codex@models")
				}
				if err != nil || got != want {
					t.Errorf("%s path = %q, %v; want %q", fn, got, err, want)
				}
			}
		})
	}
}

func TestEnsureDirs(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv(EnvSocket, filepath.Join(tmp, "run", "agentd.sock"))
	t.Setenv(EnvStateDir, filepath.Join(tmp, "state"))
	if err := EnsureDirs(); err != nil {
		t.Fatal(err)
	}
	for _, d := range []string{"run", "state", "state/logs"} {
		fi, err := os.Stat(filepath.Join(tmp, d))
		if err != nil {
			t.Fatal(err)
		}
		if !fi.IsDir() || fi.Mode().Perm() != 0o700 {
			t.Errorf("%s: mode %v, want dir 0700", d, fi.Mode())
		}
	}
	// Idempotent.
	if err := EnsureDirs(); err != nil {
		t.Fatal(err)
	}
}

func TestCheckPrivateDir(t *testing.T) {
	d := filepath.Join(t.TempDir(), "p")
	if err := os.Mkdir(d, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := checkPrivateDir(d); err != nil {
		t.Fatalf("private dir rejected: %v", err)
	}
	if err := os.Chmod(d, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := checkPrivateDir(d); err == nil {
		t.Fatal("0755 dir accepted")
	}
	link := d + "-link"
	if err := os.Symlink(d, link); err != nil {
		t.Fatal(err)
	}
	if err := checkPrivateDir(link); err == nil {
		t.Fatal("symlink accepted")
	}
}

func TestValidName(t *testing.T) {
	tests := []struct {
		in string
		ok bool
	}{
		{"codex@models", true},
		{"pando@api", true},
		{"a.b_c-d@E9", true},
		{"", false},
		{".", false},
		{"..", false},
		{"a/b", false},
		{"a b", false},
		{"human:alice", false},
		{"ñ", false},
	}
	for _, tt := range tests {
		err := ValidName(tt.in)
		if (err == nil) != tt.ok {
			t.Errorf("ValidName(%q) = %v, want ok=%v", tt.in, err, tt.ok)
		}
		if err != nil && !errors.Is(err, ErrInvalidName) {
			t.Errorf("ValidName(%q) error not ErrInvalidName: %v", tt.in, err)
		}
	}
	if _, err := SessionLogPath("../x"); err == nil {
		t.Error("SessionLogPath accepted invalid name")
	}
}

func TestLoadMissingFileIsDefaults(t *testing.T) {
	c, err := Load(filepath.Join(t.TempDir(), "nope.toml"))
	if err != nil {
		t.Fatal(err)
	}
	if c.Loaded {
		t.Error("Loaded = true for missing file")
	}
	p := c.Profile("claude")
	if p.IdleMS != DefaultIdleMS || p.Name != "claude" {
		t.Errorf("unexpected profile %+v", p)
	}
	if !c.Allowed("anyone", "anybody") {
		t.Error("missing file should allow all")
	}
}

func TestLoadDefaultUsesEnv(t *testing.T) {
	p := writeConfig(t, "[profiles.default]\nidle_ms = 42\n")
	t.Setenv(EnvConfig, p)
	c, err := LoadDefault()
	if err != nil {
		t.Fatal(err)
	}
	if !c.Loaded || c.Path != p || c.Profile("x").IdleMS != 42 {
		t.Fatalf("LoadDefault did not load %s: %+v", p, c)
	}
}

func TestProfileResolution(t *testing.T) {
	file := `
[profiles.default]
idle_ms = 2000
submit = "\n"

[profiles.codex]
prompt_regex = '(?m)^\s*›'
human_grace_ms = 7000

[profiles.custom]
inline_max = 10
submit_delay_ms = 0
`
	c, err := Load(writeConfig(t, file))
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name          string
		wantName      string
		idle, grace   int
		submit        string
		delay, inline int
		regex         string
	}{
		{"default", "default", 2000, 5000, "\n", 150, 400, ""},
		{"", "default", 2000, 5000, "\n", 150, 400, ""},
		{"unknown", "default", 2000, 5000, "\n", 150, 400, ""},
		{"claude", "claude", 2000, 5000, "\n", 150, 400, `(?m)^❯([ \x{a0}]|$)`},
		{"pando", "pando", 2000, 5000, "\n", 150, 400, ""},
		{"codex", "codex", 2000, 7000, "\n", 150, 400, `(?m)^\s*›`},
		{"custom", "custom", 2000, 5000, "\n", 0, 10, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := c.Profile(tt.name)
			if p.Name != tt.wantName || p.IdleMS != tt.idle || p.HumanGraceMS != tt.grace ||
				p.Submit != tt.submit || p.SubmitDelayMS != tt.delay || p.InlineMax != tt.inline ||
				p.PromptRegex != tt.regex {
				t.Fatalf("Profile(%q) = %+v", tt.name, p)
			}
			if (p.Prompt != nil) != (tt.regex != "") {
				t.Fatalf("Prompt compiled = %v, regex %q", p.Prompt != nil, tt.regex)
			}
			if p.Idle() != time.Duration(tt.idle)*time.Millisecond ||
				p.HumanGrace() != time.Duration(tt.grace)*time.Millisecond ||
				p.SubmitDelay() != time.Duration(tt.delay)*time.Millisecond {
				t.Fatal("duration helpers mismatch")
			}
		})
	}
	codex := c.Profile("codex")
	if !codex.PromptMatches("output\n  › ") || codex.PromptMatches("busy...") {
		t.Error("codex PromptMatches wrong")
	}
	if !c.Profile("pando").PromptMatches("anything") {
		t.Error("empty regex should always match")
	}
	if !c.HasProfile("custom") || !c.HasProfile("claude") || c.HasProfile("unknown") {
		t.Error("HasProfile wrong")
	}
}

func TestBuiltinDefaults(t *testing.T) {
	for _, c := range []*Config{Default(), nil} {
		for _, name := range BuiltinProfileNames() {
			p := c.Profile(name)
			if p.IdleMS != 1500 || p.HumanGraceMS != 5000 || p.Submit != "\r" ||
				p.SubmitDelayMS != 150 || p.InlineMax != 400 || !p.GhostFaint || len(p.GhostColors) != 0 {
				t.Errorf("built-in %q = %+v", name, p)
			}
			hasPrompt := name == "claude" || name == "codex"
			if (p.Prompt != nil) != hasPrompt || p.GhostRightOfCursor != hasPrompt || (p.PromptMarker != "") != hasPrompt {
				t.Errorf("built-in %q prompt/ghost settings = %+v", name, p)
			}
		}
	}
	if got := strings.Join(BuiltinProfileNames(), ","); got != "claude,codex,default,pando" {
		t.Errorf("BuiltinProfileNames = %s", got)
	}
}

func TestProfileCopyIsIndependent(t *testing.T) {
	c := Default()
	p := c.Profile("codex")
	p.IdleMS = 1
	if c.Profile("codex").IdleMS != DefaultIdleMS {
		t.Error("mutating returned profile changed config")
	}
}

func TestLoadErrors(t *testing.T) {
	tests := []struct {
		name, body, want string
	}{
		{"invalid regex in named profile", "[profiles.codex]\nprompt_regex = '(['\n", "prompt_regex"},
		{"invalid regex in default", "[profiles.default]\nprompt_regex = '(['\n", "prompt_regex"},
		{"invalid regex in custom", "[profiles.x]\nprompt_regex = '*'\n", "prompt_regex"},
		{"negative value", "[profiles.default]\nidle_ms = -1\n", "idle_ms"},
		{"bad acl glob", "[acl]\n\"a\" = [\"[\"]\n", "bad glob"},
		{"unknown key", "[profiles.default]\nidle = 3\n", "unknown keys"},
		{"bad toml", "[profiles\n", "load"},
		{"wrong type", "[profiles.default]\nidle_ms = \"x\"\n", "load"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Load(writeConfig(t, tt.body))
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("Load error = %v, want containing %q", err, tt.want)
			}
		})
	}
}

func TestAllowed(t *testing.T) {
	file := `
[acl]
"codex@models" = ["lead", "human:*", "*@models"]
"locked" = []
"*" = ["human:*", "claude@*"]
`
	c, err := Load(writeConfig(t, file))
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		recipient, sender string
		want              bool
	}{
		{"codex@models", "lead", true},
		{"codex@models", "human:alice", true},
		{"codex@models", "pando@models", true},
		{"codex@models", "pando@api", false},
		{"codex@models", "leadx", false},
		{"locked", "human:alice", false},
		{"other", "human:alice", true},
		{"other", "claude@x", true},
		{"other", "codex@x", false},
	}
	for _, tt := range tests {
		if got := c.Allowed(tt.recipient, tt.sender); got != tt.want {
			t.Errorf("Allowed(%q, %q) = %v, want %v", tt.recipient, tt.sender, got, tt.want)
		}
	}

	// Without a "*" entry, unlisted recipients allow everyone.
	c2, err := Load(writeConfig(t, "[acl]\n\"a\" = [\"b\"]\n"))
	if err != nil {
		t.Fatal(err)
	}
	if !c2.Allowed("z", "anyone") || c2.Allowed("a", "c") || !c2.Allowed("a", "b") {
		t.Error("ACL without wildcard wrong")
	}
	var nilCfg *Config
	if !nilCfg.Allowed("a", "b") {
		t.Error("nil config should allow")
	}
}

func TestGhostProfileSettings(t *testing.T) {
	c := Default()
	cl := c.Profile("claude")
	if cl.PromptMarker != "❯" || !cl.GhostRightOfCursor || !cl.GhostFaint {
		t.Fatalf("claude = %+v", cl)
	}
	// Claude Code follows the marker with a no-break space; the ghost-filtered
	// screen may also end right after the marker (trailing spaces trimmed).
	for _, scr := range []string{"x\n❯\u00a0Try \"fix lint\"", "❯ hello", "──\n❯\n──"} {
		if !cl.PromptMatches(scr) {
			t.Errorf("claude prompt should match %q", scr)
		}
	}
	if cl.PromptMatches("  ❯ not at line start") || cl.PromptMatches("❯x") {
		t.Error("claude prompt matched a non-prompt line")
	}
	cx := c.Profile("codex")
	if cx.PromptMarker != "›" || !cx.GhostRightOfCursor || !cx.PromptMatches("›") || !cx.PromptMatches("› Ask Codex") {
		t.Fatalf("codex = %+v", cx)
	}

	file := `
[profiles.default]
ghost_colors = ["8", "#808080"]

[profiles.claude]
ghost_faint = false
ghost_right_of_cursor = false
prompt_marker = ">"

[profiles.mytool]
ghost_colors = []
`
	c, err := Load(writeConfig(t, file))
	if err != nil {
		t.Fatal(err)
	}
	cl = c.Profile("claude")
	if cl.GhostFaint || cl.GhostRightOfCursor || cl.PromptMarker != ">" ||
		strings.Join(cl.GhostColors, ",") != "8,#808080" || len(cl.GhostColorSpecs) != 2 {
		t.Fatalf("claude override = %+v", cl)
	}
	if cl.GhostColorSpecs[0].Index != 8 || cl.GhostColorSpecs[1].RGB != 0x808080 {
		t.Fatalf("specs = %+v", cl.GhostColorSpecs)
	}
	if m := c.Profile("mytool"); len(m.GhostColors) != 0 || !m.GhostFaint {
		t.Fatalf("mytool = %+v", m)
	}
	// The returned slices are copies.
	p := c.Profile("claude")
	p.GhostColors[0] = "1"
	if c.Profile("claude").GhostColors[0] != "8" {
		t.Fatal("mutating returned ghost colours changed config")
	}

	for _, bad := range []string{
		"[profiles.x]\nghost_colors = [\"grey\"]\n",
		"[profiles.x]\nghost_colors = [\"300\"]\n",
		"[profiles.x]\nghost_faint = \"yes\"\n",
		"[profiles.x]\nprompt_marker = \"a\\nb\"\n",
		"[profiles.x]\nghost_colour = [\"8\"]\n",
	} {
		if _, err := Load(writeConfig(t, bad)); err == nil {
			t.Errorf("Load(%q): want error", bad)
		}
	}
}

func TestParseDuration(t *testing.T) {
	good := map[string]time.Duration{
		"0": 0, "120h": 120 * time.Hour, "90m": 90 * time.Minute, "5d": 120 * time.Hour,
		"1d12h": 36 * time.Hour, " 2d ": 48 * time.Hour, "0d": 0, "1.5h": 90 * time.Minute,
	}
	for in, want := range good {
		if got, err := ParseDuration(in); err != nil || got != want {
			t.Errorf("ParseDuration(%q) = %v, %v; want %v", in, got, err, want)
		}
	}
	for _, in := range []string{"", "d", "5", "-1h", "-5d", "5d-1h", "1.5d", "abc", "5dd", "1h5d"} {
		if got, err := ParseDuration(in); err == nil {
			t.Errorf("ParseDuration(%q) = %v, want error", in, got)
		}
	}
}

func TestMailboxConfig(t *testing.T) {
	c := Default()
	if c.Mailbox.RetentionDuration() != DefaultRetention || DefaultRetention != 120*time.Hour {
		t.Fatalf("default retention = %v", c.Mailbox.RetentionDuration())
	}
	if c.Mailbox.PurgeEvery() != time.Hour {
		t.Fatalf("default purge interval = %v", c.Mailbox.PurgeEvery())
	}

	c, err := Load(writeConfig(t, "[mailbox]\nretention = \"3d\"\npurge_interval = \"15m\"\n"))
	if err != nil {
		t.Fatal(err)
	}
	if c.Mailbox.RetentionDuration() != 72*time.Hour || c.Mailbox.PurgeEvery() != 15*time.Minute {
		t.Fatalf("parsed %v / %v", c.Mailbox.RetentionDuration(), c.Mailbox.PurgeEvery())
	}
	c, err = Load(writeConfig(t, "[mailbox]\nretention = \"0\"\n"))
	if err != nil || c.Mailbox.RetentionDuration() != 0 {
		t.Fatalf("retention 0: %v %v", c, err)
	}

	for name, body := range map[string]string{
		"bad duration":   "[mailbox]\nretention = \"five days\"\n",
		"negative":       "[mailbox]\nretention = \"-1h\"\n",
		"not a string":   "[mailbox]\nretention = 5\n",
		"interval zero":  "[mailbox]\npurge_interval = \"0\"\n",
		"interval small": "[mailbox]\npurge_interval = \"10s\"\n",
		"unknown key":    "[mailbox]\nretention_days = 5\n",
	} {
		if _, err := Load(writeConfig(t, body)); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}
