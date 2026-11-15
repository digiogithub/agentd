package provision

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/digiogithub/agentd/internal/desktopui/backend"
	"github.com/digiogithub/agentd/internal/desktopui/mux/muxtest"
)

// Ported from the agentd-app Dart tests (test/provision/provision_test.dart).

func caps(systemd bool, tools ...string) *backend.Capabilities {
	c := &backend.Capabilities{OS: "Linux", Arch: "x86_64", SystemdUser: systemd, Tools: map[string]backend.Tool{}}
	for _, t := range tools {
		c.Tools[t] = backend.Tool{Name: t, Installed: true, Path: "/usr/bin/" + t}
	}
	return c
}

func all(clis ...string) backend.ProvisionOptions {
	return backend.ProvisionOptions{Agentd: true, HCListener: true, MCP: true, CLIs: clis}
}

func stepNames(steps []backend.PlannedStep) []string {
	var r []string
	for _, s := range steps {
		r = append(r, s.Step)
	}
	return r
}

func TestPlan(t *testing.T) {
	svc := New()
	tests := []struct {
		name string
		caps *backend.Capabilities
		opts backend.ProvisionOptions
		want []string
	}{
		{"fresh host, everything", caps(true, "node", "npm"), all("claude", "codex", "pando", "gemini", "opencode"), []string{
			"download", "verify", "install", "hc_listener", "service",
			"cli:claude", "cli:codex", "cli:pando", "cli:opencode", "cli:gemini",
			"mcp", "mcp", "mcp",
		}},
		{"installed agentd and CLIs are not reinstalled", caps(true, "agentd", "claude", "npm"), all("claude"), []string{"hc_listener", "service", "mcp"}},
		{"no systemd: the daemon is started by the CLI", caps(false), backend.ProvisionOptions{Agentd: true}, []string{"download", "verify", "install", "service"}},
		{"nothing selected", caps(true, "agentd"), backend.ProvisionOptions{}, nil},
		{"nil caps", nil, backend.ProvisionOptions{Agentd: true}, []string{"download", "verify", "install", "service"}},
		{"unknown CLI ignored", caps(true, "agentd"), backend.ProvisionOptions{CLIs: []string{"aider"}}, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := stepNames(svc.Plan(tt.caps, tt.opts)); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("steps = %v\nwant    %v", got, tt.want)
			}
		})
	}

	steps := svc.Plan(caps(true, "node", "npm"), all("claude", "codex", "pando", "gemini", "opencode"))
	contains := []struct {
		i    int
		want string
	}{
		{0, `curl -fsSL -o "$d/install.sh" https://github.com/digiogithub/agentd/releases/latest/download/install.sh`},
		{0, "releases/latest/download/SHA256SUMS"},
		{1, "sha256sum"},
		{1, "shasum -a 256"},
		{2, `sh "$d/install.sh"`},
		{3, "listen = true"},
		{3, "agentd hc server start --json"},
		{4, "systemctl --user enable --now agentd"},
		{4, "systemctl --user daemon-reload"},
		{5, "npm install -g @anthropic-ai/claude-code"},
		{5, `npm install -g --prefix "$HOME/.local" @anthropic-ai/claude-code`},
		{6, "@openai/codex"},
		{7, "https://raw.githubusercontent.com/digiogithub/pando/main/scripts/install.sh"},
		{7, "--no-desktop --cli-only"},
		{8, "opencode-ai"},
		{9, "@google/gemini-cli"},
		{10, `claude mcp add --scope user agentd -- "$a" mcp`},
		{11, "[mcp_servers.agentd]"},
		{12, "[MCPServers.agentd]"},
	}
	for _, c := range contains {
		if !strings.Contains(steps[c.i].Command, c.want) {
			t.Errorf("step %d (%s) lacks %q", c.i, steps[c.i].Step, c.want)
		}
	}
	for _, s := range steps {
		if s.Title == "" {
			t.Errorf("step %s has no title", s.Step)
		}
	}

	last := svc.Plan(caps(false), backend.ProvisionOptions{Agentd: true})
	svcStep := last[len(last)-1]
	if !strings.Contains(svcStep.Command, `"$a" ls >/dev/null`) || strings.Contains(svcStep.Command, "systemctl") {
		t.Errorf("autostart service = %q", svcStep.Command)
	}
}

func TestScriptsAreValidSh(t *testing.T) {
	svc := New()
	scripts := []string{DetectScript()}
	for _, s := range svc.Plan(caps(true), all(InstallableCLIs()...)) {
		scripts = append(scripts, s.Command)
	}
	for _, s := range svc.Plan(caps(false), all()) {
		scripts = append(scripts, s.Command)
	}
	for _, c := range scripts {
		if out, err := exec.Command("sh", "-n", "-c", c).CombinedOutput(); err != nil {
			t.Errorf("sh -n: %v: %s\n%s", err, out, c)
		}
	}
}

type evt struct{ step, kind string }

func kinds(events []backend.ProvisionEvent) []evt {
	var r []evt
	for _, e := range events {
		k := "start"
		switch {
		case e.Skipped:
			k = "skipped"
		case e.Err != "":
			k = "error"
		case e.Done:
			k = "done"
		}
		r = append(r, evt{e.Step, k})
	}
	return r
}

func collect(ch <-chan backend.ProvisionEvent) []backend.ProvisionEvent {
	var r []backend.ProvisionEvent
	for e := range ch {
		r = append(r, e)
	}
	return r
}

func TestInstallFailingStep(t *testing.T) {
	sh := (&muxtest.FakeShell{}).
		On("releases/latest/download/install.sh", backend.ExecResult{Stdout: "downloaded install.sh and SHA256SUMS to /c\n"}).
		On(`SHA256SUMS")`, backend.ExecResult{Exit: 1, Stderr: "checksum mismatch for install.sh\n"}).
		On("@anthropic-ai/claude-code", backend.ExecResult{Stdout: "added 3 packages\n/home/u/.local/bin/claude\n"})
	events := collect(New().Install(context.Background(), sh, caps(true, "npm"), all("claude")))
	want := []evt{
		{"download", "start"},
		{"download", "done"},
		{"verify", "start"},
		{"verify", "error"},
		{"install", "skipped"},     // same group
		{"hc_listener", "skipped"}, // needs agentd
		{"service", "skipped"},
		{"cli:claude", "start"},
		{"cli:claude", "done"}, // independent of agentd
		{"mcp", "skipped"},     // needs agentd
	}
	if got := kinds(events); !reflect.DeepEqual(got, want) {
		t.Fatalf("events = %v", got)
	}
	if events[1].Message != "downloaded install.sh and SHA256SUMS to /c" {
		t.Errorf("download message = %q", events[1].Message)
	}
	if !strings.HasPrefix(events[3].Err, "exit 1") || !strings.Contains(events[3].Err, "checksum mismatch") {
		t.Errorf("verify err = %q", events[3].Err)
	}
	if !strings.Contains(events[4].Message, "Verify install.sh") || events[4].Err != "" {
		t.Errorf("skipped = %+v", events[4])
	}
	if events[8].Message != "/home/u/.local/bin/claude" {
		t.Errorf("cli message = %q", events[8].Message)
	}
	// Skipped steps never ran.
	if n := len(sh.Commands()); n != 3 {
		t.Errorf("ran %d commands", n)
	}
}

func TestInstallAllSucceedExecError(t *testing.T) {
	sh := (&muxtest.FakeShell{Fallback: backend.ExecResult{Stdout: "ok\n"}}).OnError("claude mcp", errors.New("connection lost"))
	c := caps(true, "agentd", "claude")
	events := collect(New().Install(context.Background(), sh, c, all()))
	var done []string
	for _, e := range events {
		if e.Done {
			done = append(done, e.Step)
		}
	}
	if !reflect.DeepEqual(done, []string{"hc_listener", "service"}) {
		t.Errorf("done = %v", done)
	}
	lastEv := events[len(events)-1]
	if lastEv.Step != "mcp" || !strings.Contains(lastEv.Err, "connection lost") {
		t.Errorf("last = %+v", lastEv)
	}
	var plan []string
	for _, s := range New().Plan(c, all()) {
		plan = append(plan, s.Command)
	}
	if !reflect.DeepEqual(sh.Commands(), plan) {
		t.Error("commands differ from the plan")
	}
}

func TestInstallCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	sh := &muxtest.FakeShell{}
	events := collect(New().Install(ctx, sh, caps(true, "agentd"), all()))
	if len(events) != 2 || !events[0].Skipped || !events[1].Skipped {
		t.Errorf("events = %+v", events)
	}
	if len(sh.Commands()) != 0 {
		t.Errorf("ran %v", sh.Commands())
	}
}

// The config-editing scripts run for real under sh in a sandbox HOME with a
// fake `agentd` that only answers `hc server status` as "no daemon".
type sandbox struct {
	t    *testing.T
	home string
	env  []string
}

func newSandbox(t *testing.T) *sandbox {
	home := t.TempDir()
	bin := filepath.Join(home, ".local", "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bin, "agentd"), []byte("#!/bin/sh\nexit 2\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return &sandbox{t: t, home: home, env: []string{"HOME=" + home, "PATH=/usr/bin:/bin", "XDG_CONFIG_HOME=" + filepath.Join(home, ".config")}}
}

func (s *sandbox) run(script string) (stdout string, exit int) {
	cmd := exec.Command("/bin/sh", "-c", script)
	cmd.Env = s.env
	var stderr strings.Builder
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return string(out), ee.ExitCode()
	}
	if err != nil {
		s.t.Fatal(err)
	}
	return string(out), 0
}

// planStep returns the script of the first planned step named name whose
// command contains sub.
func planStep(t *testing.T, name, sub string) string {
	for _, s := range New().Plan(caps(true, "agentd", "claude", "codex", "pando"), all()) {
		if s.Step == name && strings.Contains(s.Command, sub) {
			return s.Command
		}
	}
	t.Fatalf("no step %s with %q", name, sub)
	return ""
}

func (s *sandbox) cfg() string { return filepath.Join(s.home, ".config", "agentd", "config.toml") }

func (s *sandbox) write(path, body string) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		s.t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		s.t.Fatal(err)
	}
}

func (s *sandbox) read(path string) string {
	b, err := os.ReadFile(path)
	if err != nil {
		s.t.Fatal(err)
	}
	return string(b)
}

func TestHCListenerScript(t *testing.T) {
	script := planStep(t, StepHCListener, "")
	tests := []struct {
		name     string
		initial  *string
		want     string
		outputs  []string
		runTwice bool
	}{
		{
			name:     "new file, then idempotent",
			want:     "\n[herdr]\nlisten = true\n",
			outputs:  []string{"appended [herdr] listen = true", "agentd daemon not running"},
			runTwice: true,
		},
		{
			name:    "inserts into an existing [herdr] table",
			initial: ptr("[run]\nlog_max_mb = 10\n\n[herdr]  # compat\nread_only = true\n\n[acl]\n\"x\" = [\"*\"]\n"),
			want:    "[run]\nlog_max_mb = 10\n\n[herdr]  # compat\nlisten = true\nread_only = true\n\n[acl]\n\"x\" = [\"*\"]\n",
			outputs: []string{"added listen = true to [herdr]"},
		},
		{
			name:    "an explicit listen = false is kept",
			initial: ptr("[herdr]\nlisten = false # off on purpose\n"),
			want:    "[herdr]\nlisten = false # off on purpose\n",
			outputs: []string{"kept [herdr] listen = false"},
		},
		{
			name:    "other tables only, [herdr.x] does not count",
			initial: ptr("[herdr.extra]\nlisten = false\n"),
			want:    "[herdr.extra]\nlisten = false\n\n[herdr]\nlisten = true\n",
		},
		{
			name:    "listen in another table does not count",
			initial: ptr("[herdr]\nread_only = true\n[api]\nlisten = \"127.0.0.1:7477\"\n"),
			want:    "[herdr]\nlisten = true\nread_only = true\n[api]\nlisten = \"127.0.0.1:7477\"\n",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := newSandbox(t)
			if tt.initial != nil {
				s.write(s.cfg(), *tt.initial)
			}
			out, exit := s.run(script)
			if exit != 0 {
				t.Fatalf("exit %d: %s", exit, out)
			}
			for _, o := range tt.outputs {
				if !strings.Contains(out, o) {
					t.Errorf("output %q lacks %q", out, o)
				}
			}
			if got := s.read(s.cfg()); got != tt.want {
				t.Errorf("config = %q\nwant     %q", got, tt.want)
			}
			// Running again changes nothing and keeps the value.
			out, _ = s.run(script)
			if got := s.read(s.cfg()); got != tt.want {
				t.Errorf("second run config = %q", got)
			}
			if !strings.Contains(out, "kept [herdr] listen = ") {
				t.Errorf("second run output = %q", out)
			}
		})
	}
}

func ptr(s string) *string { return &s }

func TestMCPScripts(t *testing.T) {
	s := newSandbox(t)
	codex := planStep(t, StepMCP, "codex")
	pando := planStep(t, StepMCP, "MCPServers")
	for i := 0; i < 2; i++ {
		if out, exit := s.run(codex); exit != 0 {
			t.Fatalf("codex exit %d: %s", exit, out)
		}
		if out, exit := s.run(pando); exit != 0 {
			t.Fatalf("pando exit %d: %s", exit, out)
		}
	}
	a := filepath.Join(s.home, ".local", "bin", "agentd")
	if got, want := s.read(filepath.Join(s.home, ".codex", "config.toml")), "\n[mcp_servers.agentd]\ncommand = \""+a+"\"\nargs = [\"mcp\"]\n"; got != want {
		t.Errorf("codex config = %q", got)
	}
	if got, want := s.read(filepath.Join(s.home, ".pando.toml")), "\n[MCPServers.agentd]\nType = 'stdio'\nCommand = '"+a+"'\nArgs = ['mcp']\n"; got != want {
		t.Errorf("pando config = %q", got)
	}
}

func TestPandoMCPRefusesJSON(t *testing.T) {
	s := newSandbox(t)
	s.write(filepath.Join(s.home, ".pando.json"), "{}")
	if _, exit := s.run(planStep(t, StepMCP, "MCPServers")); exit != 1 {
		t.Errorf("exit = %d", exit)
	}
	if _, err := os.Stat(filepath.Join(s.home, ".pando.toml")); !os.IsNotExist(err) {
		t.Errorf(".pando.toml exists: %v", err)
	}
}
