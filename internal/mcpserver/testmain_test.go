package mcpserver_test

import (
	"os"
	"testing"
)

// TestMain clears the session-scoped AGENTD_* variables so the tests pass
// when run from inside an agentd session (where they are set).
func TestMain(m *testing.M) {
	for _, k := range []string{"AGENTD_TOKEN", "AGENTD_ID", "AGENTD_SOCKET"} {
		os.Unsetenv(k)
	}
	os.Exit(m.Run())
}
