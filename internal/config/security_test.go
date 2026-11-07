package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSecurityAgentType(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "config.toml")
	if err := os.WriteFile(p, []byte("[security]\nagent_type = true\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	c, err := Load(p)
	if err != nil || !c.Security.AgentType {
		t.Fatalf("Load: %+v %v", c, err)
	}
	if Default().Security.AgentType {
		t.Fatal("agent_type must default to false")
	}
	if err := os.WriteFile(p, []byte("[security]\nagent_typo = true\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(p); err == nil {
		t.Fatal("unknown [security] key accepted")
	}
}
