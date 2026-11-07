package client

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
)

func TestDaemonEnv(t *testing.T) {
	in := []string{
		"PATH=/bin", "HOME=/home/x",
		"AGENTD_ID=codex@x", "AGENTD_TOKEN=secret", "AGENTD_SOCKET=/old.sock",
		"AGENTD_STATE_DIR=/state", "AGENTD_CONFIG=rel/config.toml", "AGENTD_ALIASES=a=/x",
		"CLAUDECODE=1", "CLAUDECODE_FOO=1", "CLAUDE_CODE_ENTRYPOINT=cli",
	}
	got := DaemonEnv(in, "/run/agentd.sock")
	cwd, _ := os.Getwd()
	want := []string{
		"PATH=/bin", "HOME=/home/x", "AGENTD_STATE_DIR=/state",
		"AGENTD_CONFIG=" + filepath.Join(cwd, "rel/config.toml"), "AGENTD_ALIASES=a=/x",
		"AGENTD_SOCKET=/run/agentd.sock",
	}
	if !slices.Equal(got, want) {
		t.Fatalf("DaemonEnv =\n%q\nwant\n%q", got, want)
	}
	// Without an explicit socket, the caller's AGENTD_SOCKET is kept.
	if got := DaemonEnv([]string{"AGENTD_SOCKET=/s.sock", "AGENTD_ID=x"}, ""); !slices.Equal(got, []string{"AGENTD_SOCKET=/s.sock"}) {
		t.Fatalf("DaemonEnv no socket = %q", got)
	}
}
