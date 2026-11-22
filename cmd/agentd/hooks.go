package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"
)

// hooksCmd is `agentd hooks`. Hooks are a separate concern from MCP
// registration (`agentd mcp install`): they are harness settings, not MCP
// servers, and come with the agent instruction block, so they get their own
// command instead of growing `mcp install`.
func hooksCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "hooks", Short: "Harness hook snippets for agentd"}
	var printFlag bool
	install := &cobra.Command{
		Use:   "install --print",
		Short: "Print the Claude Code hook snippet (task sync) and an agent instruction block (never writes files)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if !printFlag {
				return errors.New("only --print is supported: agentd never edits agent config files")
			}
			exe, err := os.Executable()
			if err != nil {
				return fmt.Errorf("find agentd binary: %w", err)
			}
			if abs, err := filepath.EvalSymlinks(exe); err == nil {
				exe = abs
			}
			return printHookSnippets(cmd.OutOrStdout(), exe)
		},
	}
	install.Flags().BoolVar(&printFlag, "print", false, "print the snippets to stdout (required)")
	cmd.AddCommand(install)
	return cmd
}

func printHookSnippets(w io.Writer, exe string) error {
	cmdline, _ := jsonString(shellQuote(exe) + " task sync --from claude")
	_, err := fmt.Fprintf(w, `# Claude Code hook: mirror the agent's todo list into agentd's task list.
# Merge into ~/.claude/settings.json (or a project .claude/settings.json).
# It needs no change to the agent's prompt; outside an agentd session it does nothing.
{
  "hooks": {
    "PostToolUse": [
      {
        "matcher": "TodoWrite|TaskCreate|TaskUpdate",
        "hooks": [
          { "type": "command", "command": %s, "timeout": 5 }
        ]
      }
    ]
  }
}

# Instruction block for your projects' CLAUDE.md / AGENTS.md (for agents without the
# hook, or other harnesses, that report their plan through the agentd MCP server):
## agentd
- Keep your plan visible: call agentd_tasks_set with your task list when you make or
  change a plan (stable ids), and agentd_task_update when a task starts, blocks or ends.
- When you need a decision or input from the human, call agentd_ask (with options when
  the answer is a choice) and continue with other work instead of stopping to wait;
  the answer arrives in your inbox.
`, cmdline)
	return err
}

func jsonString(s string) (string, error) {
	b, err := json.Marshal(s)
	return string(b), err
}
