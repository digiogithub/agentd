package mux

import (
	"context"
	"errors"
	"os/exec"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/digiogithub/agentd/internal/desktopui/backend"
	"github.com/digiogithub/agentd/internal/desktopui/mux/muxtest"
)

// Fixtures from the agentd-app Dart tests (test/mux).

// Shape of zellij 0.44.3 `list-sessions --no-formatting` (trailing space on
// running sessions included).
const zellijSample044 = `claude-radar [Created 13days 15h 3m 59s ago] (EXITED - attach to resurrect)
rectangular-pigeon [Created 11days 7h 51m 34s ago] (EXITED - attach to resurrect)
codex@webapp [Created 23h 12m 23s ago] 
claude-agentd [Created 1h 45m 46s ago] (current)
`

// Coloured output (plain `zellij ls`) of an older version.
const zellijSampleColour = "\x1b[32;1mmain\x1b[m [Created \x1b[35;1m2days\x1b[m ago] \n" +
	"\x1b[32;1mold\x1b[m [Created \x1b[35;1m5s\x1b[m ago] (\x1b[31;1mEXITED\x1b[m - attach to resurrect)\n"

const zellijPanesJSON = `
[
  {"id": 0, "is_plugin": true, "is_focused": false, "title": "tab-bar", "exited": false, "terminal_command": null, "tab_name": "Tab #1"},
  {"id": 0, "is_plugin": false, "is_focused": true, "title": "~/src/agentd", "exited": false, "terminal_command": "/home/u/.local/bin/claude --resume", "tab_name": "Tab #1"},
  {"id": 1, "is_plugin": false, "is_focused": false, "title": "bash", "exited": false, "terminal_command": null, "tab_name": "Tab #1"},
  {"id": 2, "is_plugin": false, "is_focused": false, "title": "old", "exited": true, "terminal_command": "make", "tab_name": "Tab #1"}
]
`

const tmuxSessions = "main\t3\t1\t1763200000\t1763100000\t/home/u\n" +
	"codex@webapp\t1\t0\t1763201000\t1763101000\t/home/u/src/webapp\n" +
	"work\t2\t0\t1763202000\t1763102000\t\n"

const tmuxPanes = "main\t0\t0\t0\t0\tbash\t/home/u\tu@box\t0\n" +
	"main\t1\t0\t1\t1\t2.0.76\t/home/u/src/agentd\t✳ Claude Code\t0\n" +
	"main\t2\t0\t1\t0\tvim\t/home/u\tvim\t0\n" +
	"codex@webapp\t0\t0\t1\t1\tnode\t/home/u/src/webapp\tcodex\t0\n" +
	"work\t0\t0\t1\t1\tpando\t/srv/api\tpando\t1\n"

var testNow = time.Date(2026, 11, 15, 12, 0, 0, 0, time.UTC)

func res(exit int, stdout, stderr string) backend.ExecResult {
	return backend.ExecResult{Exit: exit, Stdout: stdout, Stderr: stderr}
}

func dur(d, h, m, s int) time.Duration {
	return time.Duration(d)*24*time.Hour + time.Duration(h)*time.Hour + time.Duration(m)*time.Minute + time.Duration(s)*time.Second
}

func names[T any](xs []T, f func(T) string) []string {
	var r []string
	for _, x := range xs {
		r = append(r, f(x))
	}
	return r
}

func TestParseZellijSessions(t *testing.T) {
	zname := func(l ZellijSessionLine) string { return l.Name }
	s := ParseZellijSessions(zellijSample044)
	if got, want := names(s, zname), []string{"claude-radar", "rectangular-pigeon", "codex@webapp", "claude-agentd"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("names = %v", got)
	}
	if !s[0].Exited || s[0].CreatedAgo != dur(13, 15, 3, 59) || !s[0].HasCreated {
		t.Errorf("radar = %+v", s[0])
	}
	if s[2].Exited || s[2].Current {
		t.Errorf("webapp = %+v", s[2])
	}
	if !s[3].Current || s[3].CreatedAgo != dur(0, 1, 45, 46) {
		t.Errorf("agentd = %+v", s[3])
	}

	c := ParseZellijSessions(zellijSampleColour)
	if got := names(c, zname); !reflect.DeepEqual(got, []string{"main", "old"}) {
		t.Fatalf("colour names = %v", got)
	}
	if c[0].CreatedAgo != dur(2, 0, 0, 0) || !c[1].Exited {
		t.Errorf("colour = %+v", c)
	}

	for _, empty := range []string{"", "No active zellij sessions found.\n"} {
		if got := ParseZellijSessions(empty); len(got) != 0 {
			t.Errorf("%q = %v", empty, got)
		}
	}
	if got := names(ParseZellijSessions("a\nb\n"), zname); !reflect.DeepEqual(got, []string{"a", "b"}) {
		t.Errorf("--short = %v", got)
	}
}

func TestParseZellijPanes(t *testing.T) {
	p := ParseZellijPanes(zellijPanesJSON)
	if len(p) != 3 {
		t.Fatalf("panes = %+v", p)
	}
	if !strings.Contains(p[0].Command, "claude") || !p[0].Focused || !p[2].Exited {
		t.Errorf("panes = %+v", p)
	}
	if got := ParseZellijPanes("error: unknown subcommand"); len(got) != 0 {
		t.Errorf("garbage = %v", got)
	}
}

func newZellij(sh backend.Shell) *Zellij {
	z := NewZellij(sh, "h1")
	z.now = func() time.Time { return testNow }
	return z
}

func TestZellijList(t *testing.T) {
	sh := (&muxtest.FakeShell{}).
		On("list-sessions", res(0, zellijSample044, "")).
		On("list-panes", res(0, "@@agentd-app pane codex@webapp\n[]\n\n@@agentd-app pane claude-agentd\n"+zellijPanesJSON+"\n", ""))
	list, err := newZellij(sh).List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	cmds := sh.Commands()
	if cmds[0] != "zellij list-sessions --no-formatting" {
		t.Errorf("cmd0 = %q", cmds[0])
	}
	// Only running sessions are asked for their panes, in one exec.
	if !strings.Contains(cmds[1], "for s in codex@webapp claude-agentd;") {
		t.Errorf("cmd1 = %q", cmds[1])
	}
	if len(list) != 4 {
		t.Fatalf("list = %+v", list)
	}
	radar := list[0]
	if radar.Source != backend.SourceZellij || radar.Status != StatusExited || radar.Harness != "claude" || radar.Project != "radar" {
		t.Errorf("radar = %+v", radar)
	}
	if !radar.Updated.Equal(testNow.Add(-dur(13, 15, 3, 59))) {
		t.Errorf("radar updated = %v", radar.Updated)
	}
	if list[1].Harness != "" || list[1].Status != StatusExited {
		t.Errorf("pigeon = %+v", list[1])
	}
	if list[2].Harness != "codex" || list[2].Project != "webapp" || list[2].Detail != "0 panes" || list[2].Status != StatusUnknown {
		t.Errorf("webapp = %+v", list[2])
	}
	cur := list[3]
	if cur.Harness != "claude" || cur.Detail != "current · 2 panes" || cur.Key() != "h1/zellij/claude-agentd" {
		t.Errorf("current = %+v", cur)
	}
}

func TestZellijListErrors(t *testing.T) {
	tests := []struct {
		name      string
		r         backend.ExecResult
		wantErr   bool
		installed bool
	}{
		{"no sessions (exit 1, message on stderr)", res(1, "", "No active zellij sessions found.\n"), false, true},
		{"not installed", res(127, "", "sh: 1: zellij: not found\n"), true, false},
		{"other failure", res(1, "", "boom\n"), true, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sh := (&muxtest.FakeShell{}).On("list-sessions", tt.r)
			list, err := newZellij(sh).List(context.Background())
			if (err != nil) != tt.wantErr {
				t.Fatalf("err = %v", err)
			}
			if err == nil && len(list) != 0 {
				t.Errorf("list = %v", list)
			}
			if err != nil && errors.Is(err, ErrNotInstalled) == tt.installed {
				t.Errorf("ErrNotInstalled = %v for %v", !tt.installed, err)
			}
			if len(sh.Commands()) != 1 {
				t.Errorf("commands = %v", sh.Commands())
			}
		})
	}
}

func TestZellijAttachLaunchKill(t *testing.T) {
	ctx := context.Background()
	sh := &muxtest.FakeShell{}
	z := newZellij(sh)
	if _, err := z.Attach(ctx, "my session", 100, 30); err != nil {
		t.Fatal(err)
	}
	if p := sh.PTYs(); len(p) != 1 || p[0].Command != "zellij attach 'my session'" || p[0].Cols != 100 || p[0].Rows != 30 {
		t.Errorf("ptys = %+v", p)
	}

	if got, want := ZellijLaunchCommands("claude@api", "~/src/api", []string{"claude", `fix the "tests"`}, "claude"), []string{
		"zellij attach --create-background claude@api",
		`zellij --session claude@api action new-pane --cwd "$HOME"/src/api --name claude -- claude 'fix the "tests"'`,
	}; !reflect.DeepEqual(got, want) {
		t.Errorf("launch commands = %q", got)
	}

	sh = &muxtest.FakeShell{}
	s, err := newZellij(sh).Launch(ctx, backend.LaunchSpec{Where: backend.SourceZellij, Project: "~/src/api", Harness: "claude", FirstPrompt: `fix the "tests"`})
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{
		"zellij attach --create-background claude@api",
		`zellij --session claude@api action new-pane --cwd "$HOME"/src/api --name claude -- claude 'fix the "tests"'`,
	}; !reflect.DeepEqual(sh.Commands(), want) {
		t.Errorf("commands = %q", sh.Commands())
	}
	if s.Name != "claude@api" || s.Harness != "claude" || s.Project != "api" || !s.Updated.Equal(testNow) || s.HostID != "h1" {
		t.Errorf("session = %+v", s)
	}

	sh = (&muxtest.FakeShell{}).On("new-pane", res(1, "", "boom"))
	if _, err := newZellij(sh).Launch(ctx, backend.LaunchSpec{Project: "/tmp", Harness: "codex"}); err == nil {
		t.Error("launch failure: no error")
	}

	sh = &muxtest.FakeShell{}
	if err := newZellij(sh).Kill(ctx, "s1"); err != nil {
		t.Fatal(err)
	}
	if c := sh.Commands(); len(c) != 1 || c[0] != "zellij delete-session --force s1" {
		t.Errorf("kill = %q", c)
	}
	sh = (&muxtest.FakeShell{}).On("delete-session", res(1, "", "Session x not found\n"))
	if err := newZellij(sh).Kill(ctx, "x"); err != nil {
		t.Errorf("kill missing: %v", err)
	}
}

func TestParseTmux(t *testing.T) {
	s := ParseTmuxSessions(tmuxSessions)
	if got := names(s, func(l TmuxSessionLine) string { return l.Name }); !reflect.DeepEqual(got, []string{"main", "codex@webapp", "work"}) {
		t.Fatalf("names = %v", got)
	}
	if s[0].Windows != 3 || s[0].Attached != 1 || !s[0].Activity.Equal(time.Unix(1763200000, 0)) || s[2].Path != "" {
		t.Errorf("sessions = %+v", s)
	}
	// Older tmux without session_created/session_path: missing fields.
	if old := ParseTmuxSessions("a\t1\t0\t1763200000\n"); len(old) != 1 || !old[0].Created.IsZero() {
		t.Errorf("old = %+v", old)
	}
	if len(ParseTmuxSessions("")) != 0 {
		t.Error("empty")
	}
	// Octal-escaped separators.
	if o := ParseTmuxSessions(`x\0112\0110\0111763200000`); len(o) != 1 || o[0].Windows != 2 {
		t.Errorf("octal = %+v", o)
	}

	p := ParseTmuxPanes(tmuxPanes)
	if len(p) != 5 || p[1].Command != "2.0.76" || !p[1].Active || !p[1].WindowActive || !p[4].Dead {
		t.Errorf("panes = %+v", p)
	}
}

func newTmux(sh backend.Shell) *Tmux {
	m := NewTmux(sh, "h")
	m.now = func() time.Time { return testNow }
	return m
}

func TestTmuxList(t *testing.T) {
	sh := (&muxtest.FakeShell{}).On("list-sessions", res(0, tmuxSessions, "")).On("list-panes", res(0, tmuxPanes, ""))
	list, err := newTmux(sh).List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	cmds := sh.Commands()
	if want := "tmux list-sessions -F '#{session_name}\t#{session_windows}\t#{session_attached}\t#{session_activity}\t#{session_created}\t#{session_path}'"; cmds[0] != want {
		t.Errorf("cmd0 = %q", cmds[0])
	}
	if !strings.HasPrefix(cmds[1], "tmux list-panes -a -F '#{session_name}\t") || !strings.Contains(cmds[1], "#{pane_current_command}") {
		t.Errorf("cmd1 = %q", cmds[1])
	}
	main := list[0]
	// version-named native binary
	if main.Source != backend.SourceTmux || main.Harness != "claude" || main.Project != "agentd" || main.Detail != "3 windows · 3 panes · attached" {
		t.Errorf("main = %+v", main)
	}
	if !main.Updated.Equal(time.Unix(1763200000, 0)) {
		t.Errorf("updated = %v", main.Updated)
	}
	// node, resolved by the title
	if list[1].Harness != "codex" || list[1].Project != "webapp" {
		t.Errorf("codex = %+v", list[1])
	}
	// dead pane ignored
	if list[2].Status != StatusExited || list[2].Harness != "" {
		t.Errorf("work = %+v", list[2])
	}
}

func TestTmuxListErrors(t *testing.T) {
	tests := []struct {
		name    string
		r       backend.ExecResult
		wantErr bool
	}{
		{"no server", res(1, "", "no server running on /tmp/tmux-1000/default\n"), false},
		{"error connecting", res(1, "", "error connecting to /tmp/tmux-1000/default (No such file or directory)\n"), false},
		{"other failure", res(1, "", "unknown option"), true},
		{"missing tmux", res(127, "", "sh: tmux: command not found"), true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sh := (&muxtest.FakeShell{}).On("list-sessions", tt.r)
			list, err := newTmux(sh).List(context.Background())
			if (err != nil) != tt.wantErr {
				t.Fatalf("err = %v", err)
			}
			if err == nil && len(list) != 0 {
				t.Errorf("list = %v", list)
			}
		})
	}
}

func TestTmuxAttachLaunchKill(t *testing.T) {
	ctx := context.Background()
	sh := &muxtest.FakeShell{}
	if _, err := newTmux(sh).Attach(ctx, "main", 120, 40); err != nil {
		t.Fatal(err)
	}
	if p := sh.PTYs(); len(p) != 1 || p[0].Command != "tmux attach-session -t =main" || p[0].Rows != 40 || p[0].Cols != 120 {
		t.Errorf("ptys = %+v", p)
	}

	cmd := ShellQuote(ShellJoin([]string{"codex", "it's"}))
	want := "if tmux has-session -t =codex@api 2>/dev/null; " +
		"then tmux new-window -t =codex@api: -n codex -c '/srv/my api' " + cmd + "; " +
		"else tmux new-session -d -s codex@api -n codex -c '/srv/my api' " + cmd + "; fi"
	sh = &muxtest.FakeShell{}
	s, err := newTmux(sh).Launch(ctx, backend.LaunchSpec{Project: "/srv/my api", Harness: "codex", FirstPrompt: "it's"})
	if err != nil {
		t.Fatal(err)
	}
	// The project "my api" becomes the name label "my-api".
	wantRenamed := strings.ReplaceAll(want, "codex@api", "codex@my-api")
	if c := sh.Commands(); len(c) != 1 || c[0] != wantRenamed {
		t.Errorf("launch = %q\nwant   %q", c, wantRenamed)
	}
	if s.Harness != "codex" || s.Project != "my-api" || s.Name != "codex@my-api" {
		t.Errorf("session = %+v", s)
	}
	if got := TmuxLaunchCommand("codex@api", "/srv/my api", []string{"codex", "it's"}, "codex"); got != want {
		t.Errorf("TmuxLaunchCommand = %q", got)
	}

	sh = (&muxtest.FakeShell{}).On("kill-session", res(1, "", "can't find session: x"))
	if err := newTmux(sh).Kill(ctx, "x"); err != nil {
		t.Fatal(err)
	}
	if c := sh.Commands(); len(c) != 1 || c[0] != "tmux kill-session -t =x" {
		t.Errorf("kill = %q", c)
	}
}

func TestPlanLaunch(t *testing.T) {
	tests := []struct {
		spec    backend.LaunchSpec
		session string
		cwd     string
		argv    []string
	}{
		{backend.LaunchSpec{Project: "/home/u/src/webapp/", Harness: "claude", FirstPrompt: "fix it"}, "claude@webapp", "/home/u/src/webapp/", []string{"claude", "fix it"}},
		{backend.LaunchSpec{Project: "", Harness: "bash", FirstPrompt: "ignored"}, "bash@home", "~", []string{"bash"}},
		{backend.LaunchSpec{Project: "~/api.v2", Harness: "pando"}, "pando@api-v2", "~/api.v2", []string{"pando"}},
	}
	for _, tt := range tests {
		lp, err := planLaunch(tt.spec)
		if err != nil {
			t.Fatal(err)
		}
		if lp.session != tt.session || lp.cwd != tt.cwd || !reflect.DeepEqual(lp.argv, tt.argv) {
			t.Errorf("planLaunch(%+v) = %+v", tt.spec, lp)
		}
	}
	if _, err := planLaunch(backend.LaunchSpec{Project: "/x"}); err == nil {
		t.Error("no harness: no error")
	}
}

func TestShellQuoteRoundTrip(t *testing.T) {
	words := []string{"plain", "with space", "it's", "$HOME `x` \"q\"", "", "a\nb", "*"}
	out, err := exec.Command("sh", "-c", "for a in "+ShellJoin(words)+`; do printf "[%s]" "$a"; done`).Output()
	if err != nil {
		t.Fatal(err)
	}
	var want strings.Builder
	for _, w := range words {
		want.WriteString("[" + w + "]")
	}
	if string(out) != want.String() {
		t.Errorf("round trip = %q", out)
	}
	nested, err := exec.Command("sh", "-c", "sh -c "+ShellQuote(ShellJoin([]string{"printf", "%s", "it's"}))).Output()
	if err != nil || string(nested) != "it's" {
		t.Errorf("nested = %q, %v", nested, err)
	}
	if got := ShellDir("~/src/my api"); got != `"$HOME"/'src/my api'` {
		t.Errorf("ShellDir = %q", got)
	}
}

func TestGuessHarness(t *testing.T) {
	tests := []struct{ cmd, title, want string }{
		{"claude", "", "claude"},
		{"/usr/lib/node_modules/@anthropic-ai/claude-code/cli.js", "", "claude"},
		{"node /usr/bin/codex", "", "codex"},
		{"2.1.3", "", "claude"},
		{"node", "", ""},
		{"node", "gemini", "gemini"},
		{"bash", "", ""},
		{"", "", ""},
	}
	for _, tt := range tests {
		if got := GuessHarness(tt.cmd, tt.title); got != tt.want {
			t.Errorf("GuessHarness(%q, %q) = %q, want %q", tt.cmd, tt.title, got, tt.want)
		}
	}
}

func TestSplitSessionName(t *testing.T) {
	tests := []struct{ name, harness, project string }{
		{"codex@webapp", "codex", "webapp"},
		{"claude-agentd", "claude", "agentd"},
		{"pando", "pando", ""},
		{"rectangular-pigeon", "", ""},
	}
	for _, tt := range tests {
		h, p := SplitSessionName(tt.name)
		if h != tt.harness || p != tt.project {
			t.Errorf("SplitSessionName(%q) = %q, %q", tt.name, h, p)
		}
	}
}

func TestForHost(t *testing.T) {
	a := ForHost(&muxtest.FakeShell{}, "h")
	if len(a) != 2 || a[0].Source() != backend.SourceZellij || a[1].Source() != backend.SourceTmux {
		t.Errorf("ForHost = %v", a)
	}
}

func TestTerminalOnlyLaunch(t *testing.T) {
	ctx := context.Background()
	spec := backend.LaunchSpec{HostID: "h1", Project: "~/src/my.api", TerminalOnly: true, Harness: "claude", FirstPrompt: "ignored"}

	sh := &muxtest.FakeShell{}
	s, err := newZellij(sh).Launch(ctx, spec)
	if err != nil {
		t.Fatal(err)
	}
	if s.Name != "shell@my-api" || s.Harness != "shell" {
		t.Errorf("zellij session %+v", s)
	}
	want := []string{
		"zellij attach --create-background shell@my-api",
		`zellij --session shell@my-api action new-pane --cwd "$HOME"/src/my.api --name shell`,
	}
	if got := sh.Commands(); !reflect.DeepEqual(got, want) {
		t.Errorf("zellij commands %q, want %q", got, want)
	}

	sh = &muxtest.FakeShell{}
	if _, err := newTmux(sh).Launch(ctx, spec); err != nil {
		t.Fatal(err)
	}
	c := sh.Commands()
	if len(c) != 1 || strings.Contains(c[0], "''") || !strings.HasSuffix(c[0], `-n shell -c "$HOME"/src/my.api; fi`) ||
		!strings.Contains(c[0], `tmux new-session -d -s shell@my-api -n shell -c "$HOME"/src/my.api;`) {
		t.Errorf("tmux commands %q", c)
	}
}
