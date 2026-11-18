package provision

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/digiogithub/agentd/internal/desktopui/backend"
	"github.com/digiogithub/agentd/internal/desktopui/mux/muxtest"
)

// Fixtures from the agentd-app Dart tests (test/provision/detect_test.dart).

func section(name string, body ...string) string {
	s := "@@agentd-app " + name + "\n"
	if len(body) > 0 && body[0] != "" {
		s += body[0] + "\n"
	}
	return s
}

var linuxFull = strings.Join([]string{
	section("os", "Linux"),
	section("arch", "x86_64"),
	section("os-release", "PRETTY_NAME=\"Ubuntu 24.04.1 LTS\"\nNAME=\"Ubuntu\"\nVERSION_ID=\"24.04\"\nID=ubuntu"),
	section("macos"),
	section("uid", "1000"),
	section("home", "/home/u"),
	section("path", "/home/u/.local/bin:/usr/local/bin:/usr/bin:/bin"),
	section("runtime-dir", "/run/user/1000"),
	section("systemd", "yes"),
	section("tool agentd", "path=/home/u/.local/bin/agentd\nversion=agentd 0.4.0"),
	section("tool zellij", "path=/usr/bin/zellij\nversion=zellij 0.44.3"),
	section("tool tmux", "path=/usr/bin/tmux\nversion=tmux 3.4"),
	section("tool node", "path=/usr/bin/node\nversion=v22.11.0"),
	section("tool npm", "path=/usr/bin/npm\nversion=10.9.0"),
	section("tool claude", "path=/home/u/.local/bin/claude\nversion=2.0.76 (Claude Code)"),
	section("tool codex", "path=/home/u/.local/bin/codex\nversion=codex-cli 0.58.0"),
	section("tool pando", "path=/home/u/.local/bin/pando\nversion=pando version v1.4.2"),
	section("tool opencode", "path=/home/u/.local/bin/opencode\nversion=1.0.12"),
	section("tool gemini", "path=/home/u/.local/bin/gemini\nversion=0.15.0"),
	section("tool grok", "path=/home/u/.bun/bin/grok\nversion=0.1.0"),
	section("socket", "live=/run/user/1000/agentd/agentd.sock\nlive=/run/user/1000/agentd/agentd.sock\ndefault=/run/user/1000/agentd/agentd.sock"),
	section("end"),
}, "")

var macArm = strings.Join([]string{
	section("os", "Darwin"),
	section("arch", "arm64"),
	section("os-release"),
	section("macos", "name=macOS\nversion=15.1"),
	section("uid", "501"),
	section("home", "/Users/u"),
	section("path", "/usr/bin:/bin:/usr/sbin:/sbin"),
	section("runtime-dir"),
	section("systemd", "no"),
	section("tool agentd", "path=/usr/local/bin/agentd\nversion=0.4.0"),
	section("tool zellij"),
	section("tool tmux", "path=/opt/homebrew/bin/tmux\nversion=tmux next-3.6"),
	section("tool node"),
	section("tool npm"),
	section("tool claude"),
	section("tool codex"),
	section("tool pando"),
	section("tool opencode"),
	section("tool gemini"),
	section("tool grok"),
	section("socket", "default=/tmp/agentd-501/agentd.sock"),
	section("end"),
}, "")

var minimal = func() string {
	s := []string{
		section("os", "Linux"),
		section("arch", "aarch64"),
		section("os-release", "NAME=\"Alpine Linux\"\nID=alpine\nVERSION_ID=3.20.3"),
		section("macos"),
		section("uid", "1000"),
		section("home", "/home/u"),
		section("path", "/usr/bin:/bin"),
		section("runtime-dir"),
		section("systemd", "no"),
	}
	for _, t := range ToolNames() {
		s = append(s, section("tool "+t))
	}
	s = append(s, section("socket", "default=/tmp/agentd-1000/agentd.sock"), section("end"))
	return strings.Join(s, "")
}()

func TestParseDetect(t *testing.T) {
	type want struct {
		os, arch, distro, runtimeDir, socket, home string
		systemd, localBin                          bool
		installed                                  []string
		versions                                   map[string]string
	}
	tests := []struct {
		name string
		out  string
		want want
	}{
		{"Linux amd64 with everything", linuxFull, want{
			os: "Linux", arch: "x86_64", distro: "Ubuntu 24.04.1 LTS", runtimeDir: "/run/user/1000",
			socket: "/run/user/1000/agentd/agentd.sock", home: "/home/u", systemd: true, localBin: true,
			installed: ToolNames(),
			versions: map[string]string{
				"agentd": "0.4.0", "node": "22.11.0", "claude": "2.0.76", "codex": "0.58.0",
				"pando": "1.4.2", "tmux": "3.4", "zellij": "0.44.3",
			},
		}},
		{"macOS arm64", macArm, want{
			os: "Darwin", arch: "arm64", distro: "macOS 15.1", home: "/Users/u",
			// agentd installed, daemon not running: where it will listen.
			socket:    "/tmp/agentd-501/agentd.sock",
			installed: []string{"agentd", "tmux"},
			versions:  map[string]string{"tmux": "3.6", "agentd": "0.4.0"},
		}},
		{"minimal host without systemd (CRLF)", strings.ReplaceAll(minimal, "\n", "\r\n"), want{
			os: "Linux", arch: "aarch64", distro: "Alpine Linux 3.20.3", home: "/home/u",
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if !parseReport(tt.out).complete() {
				t.Error("not complete")
			}
			c, err := ParseDetect(tt.out)
			if err != nil {
				t.Fatal(err)
			}
			w := tt.want
			if c.OS != w.os || c.Arch != w.arch || c.Distro != w.distro || c.RuntimeDir != w.runtimeDir ||
				c.AgentdSocket != w.socket || c.Home != w.home || c.SystemdUser != w.systemd || c.LocalBinOnPath != w.localBin {
				t.Errorf("caps = %+v", c)
			}
			if len(c.Tools) != len(ToolNames()) {
				t.Errorf("tools = %v", c.Tools)
			}
			for _, n := range ToolNames() {
				if c.Has(n) != slices.Contains(w.installed, n) {
					t.Errorf("Has(%s) = %v", n, c.Has(n))
				}
				if v := w.versions[n]; v != "" && c.Tools[n].Version != v {
					t.Errorf("%s version = %q, want %q", n, c.Tools[n].Version, v)
				}
			}
			if w.installed != nil && c.Tools["agentd"].Path == "" {
				t.Error("agentd path empty")
			}
		})
	}
	if _, err := ParseDetect("garbage"); !errors.Is(err, ErrNoOS) {
		t.Errorf("garbage err = %v", err)
	}
}

func TestCleanVersion(t *testing.T) {
	for in, want := range map[string]string{
		"zellij 0.44.3": "0.44.3", "v1.2.3-rc.1": "1.2.3-rc.1", "weird": "weird", "": "",
		"tmux next-3.5": "3.5", "2.0.76 (Claude Code)": "2.0.76",
	} {
		if got := CleanVersion(in); got != want {
			t.Errorf("CleanVersion(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestDetectRunsScriptOnce(t *testing.T) {
	sh := (&muxtest.FakeShell{}).On("@@agentd-app", backend.ExecResult{Stdout: linuxFull})
	c, err := New().Detect(context.Background(), sh)
	if err != nil {
		t.Fatal(err)
	}
	if !c.Has("agentd") || c.Home != "/home/u" || !c.LocalBinOnPath {
		t.Errorf("caps = %+v", c)
	}
	if cmds := sh.Commands(); len(cmds) != 1 || cmds[0] != DetectScript() {
		t.Errorf("commands = %d", len(cmds))
	}
	bad := &muxtest.FakeShell{Fallback: backend.ExecResult{Exit: 1, Stderr: "sh: syntax error"}}
	if _, err := New().Detect(context.Background(), bad); err == nil || !strings.Contains(err.Error(), "syntax error") || !strings.Contains(err.Error(), "exit 1") {
		t.Errorf("garbage err = %v", err)
	}
	broken := (&muxtest.FakeShell{}).OnError("@@agentd-app", errors.New("connection lost"))
	if _, err := New().Detect(context.Background(), broken); err == nil {
		t.Error("exec error: no error")
	}
}

// The real script runs under /bin/sh in a sandbox: HOME is a temp dir and
// PATH holds only fake tools plus the few utilities the script uses, so no
// real CLI is ever started. One fake tool hangs to exercise the 5 s limit,
// once with `timeout` available and once with the shell watchdog.
func TestDetectScriptInSandbox(t *testing.T) {
	if testing.Short() {
		t.Skip("runs ~5 s per variant")
	}
	for _, withTimeout := range []bool{true, false} {
		name := "watchdog"
		if withTimeout {
			name = "timeout"
		}
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			tmp := t.TempDir()
			bin := filepath.Join(tmp, "bin")
			if err := os.Mkdir(bin, 0o755); err != nil {
				t.Fatal(err)
			}
			utils := []string{"uname", "cat", "sed", "id", "sleep", "grep", "rm"}
			if withTimeout {
				utils = append(utils, "timeout")
			}
			for _, u := range utils {
				for _, dir := range []string{"/usr/bin", "/bin"} {
					p := filepath.Join(dir, u)
					if _, err := os.Stat(p); err == nil {
						if err := os.Symlink(p, filepath.Join(bin, u)); err != nil {
							t.Fatal(err)
						}
						break
					}
				}
			}
			fake := func(name, body string) {
				if err := os.WriteFile(filepath.Join(bin, name), []byte("#!/bin/sh\n"+body+"\n"), 0o755); err != nil {
					t.Fatal(err)
				}
			}
			fake("zellij", `echo "zellij 0.44.3"`)
			fake("node", "echo v22.11.0")
			fake("claude", `echo "2.0.76 (Claude Code)"`)
			fake("gemini", "sleep 30")
			if err := os.MkdirAll(filepath.Join(tmp, "run", "agentd"), 0o755); err != nil {
				t.Fatal(err)
			}

			ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, "/bin/sh", "-c", DetectScript())
			cmd.Env = []string{"HOME=" + tmp, "PATH=" + bin, "XDG_RUNTIME_DIR=" + filepath.Join(tmp, "run")}
			var stderr strings.Builder
			cmd.Stderr = &stderr
			start := time.Now()
			out, err := cmd.Output()
			elapsed := time.Since(start)
			if err != nil {
				t.Fatalf("%v: %s", err, stderr.String())
			}
			rep := parseReport(string(out))
			if !rep.complete() {
				t.Fatalf("incomplete:\n%s", out)
			}
			c, err := ParseDetect(string(out))
			if err != nil {
				t.Fatal(err)
			}
			wantOS := "Linux"
			if runtime.GOOS == "darwin" {
				wantOS = "Darwin"
			}
			if c.OS != wantOS {
				t.Errorf("os = %q", c.OS)
			}
			if z := c.Tools["zellij"]; z.Version != "0.44.3" || z.Path != filepath.Join(bin, "zellij") {
				t.Errorf("zellij = %+v", z)
			}
			if c.Tools["claude"].Version != "2.0.76" {
				t.Errorf("claude = %+v", c.Tools["claude"])
			}
			if g := c.Tools["gemini"]; !g.Installed || g.Version != "" {
				t.Errorf("gemini = %+v", g)
			}
			if c.Has("tmux") || c.Has("agentd") || c.SystemdUser {
				t.Errorf("caps = %+v", c)
			}
			if c.RuntimeDir != filepath.Join(tmp, "run") || c.Home != tmp || c.LocalBinOnPath {
				t.Errorf("caps = %+v", c)
			}
			// The sandbox has no socket; a real daemon of this uid may still be
			// found at /run/user/<uid> (a fallback the script also checks).
			if c.AgentdSocket != "" && !strings.HasPrefix(c.AgentdSocket, "/run/user/") {
				t.Errorf("socket = %q", c.AgentdSocket)
			}
			if d := rep.defaultSocket(); d != filepath.Join(tmp, "run", "agentd", "agentd.sock") {
				t.Errorf("default socket = %q", d)
			}
			if left, _ := filepath.Glob(filepath.Join(tmp, ".agentd-app-run5.*")); len(left) != 0 {
				t.Errorf("watchdog temp files left: %v", left)
			}
			if elapsed > 20*time.Second {
				t.Errorf("took %v", elapsed)
			}
			t.Logf("detect took %v", elapsed)
		})
	}
}
