package config

import "testing"

func TestRunConfig(t *testing.T) {
	if got := Default().Run.LogMaxBytes(); got != DefaultLogMaxMB<<20 {
		t.Fatalf("default log cap = %d", got)
	}
	c, err := Load(writeConfig(t, "[run]\nstrip_env = [\"AWS_*\", \"GITHUB_TOKEN\"]\nlog_max_mb = 0\n"))
	if err != nil {
		t.Fatal(err)
	}
	if c.Run.LogMaxBytes() != 0 || len(c.Run.StripEnv) != 2 {
		t.Fatalf("run = %+v", c.Run)
	}
	for _, bad := range []string{
		"[run]\nlog_max_mb = -1\n",
		"[run]\nstrip_env = [\"[\"]\n",
		"[run]\nstrip_env = [\"\"]\n",
		"[run]\nbogus = 1\n",
	} {
		if _, err := Load(writeConfig(t, bad)); err == nil {
			t.Errorf("accepted %q", bad)
		}
	}
}
