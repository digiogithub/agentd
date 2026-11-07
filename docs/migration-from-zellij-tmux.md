# Migrating from zellij/tmux launcher scripts

A common way to run CLI coding agents in the background is a small script that starts each agent
in a detached zellij or tmux session per project, then drives it with `zellij action write-chars` /
`tmux send-keys` and reads it back with `zellij action dump-screen` / `tmux capture-pane`.
agentd replaces both parts: the multiplexer (persistent PTY, attach/detach) and the typing
(`type`, or the mailbox with `send`), and adds a mailbox so agents can answer each other.

## Command mapping

| zellij / tmux | agentd |
|---|---|
| `zellij -s claude-webapp` / `tmux new -d -s claude-webapp -c ~/src/webapp claude` | `agentd run ~/src/webapp --harness claude` (session `claude@webapp`) |
| same, with a project alias table in the script | `agentd run webapp` with `~/.config/agentd/aliases.toml` (see `contrib/aliases.example.toml`) or `AGENTD_ALIASES` |
| custom session name | `agentd run … --name NAME` |
| extra agent arguments | `agentd run … -- claude <args>` |
| extra environment (`env FOO=1 claude`) | `agentd run … -e FOO=1` (repeatable) |
| `zellij list-sessions` / `tmux ls` | `agentd ls` (sessions) and `agentd aliases` (project aliases) |
| `zellij attach NAME` / `tmux attach -t NAME` | `agentd attach NAME` (detach: `Ctrl-]` then `d`) |
| `zellij action write-chars "…"` + Enter / `tmux send-keys -t NAME "…" Enter` | `agentd type NAME "…" --submit` (raw), or `agentd send NAME "…"` (mailbox: injected when idle, tracked, the agent can `agentd reply`) |
| `zellij action dump-screen` / `tmux capture-pane -p -t NAME` | `agentd dump NAME` (`--scrollback`, `--ansi`) |
| `zellij kill-session NAME` / `tmux kill-session -t NAME` | `agentd kill NAME --rm` |

## Example: Claude Code with Remote Control

Remote Control lets you follow and steer a local Claude Code session from another device. Run it
inside agentd so it keeps a persistent terminal you can also attach to locally:

```sh
agentd run webapp --harness claude -- claude --remote-control claude@webapp
agentd attach claude@webapp        # answer the workspace trust dialog the first time
```

The Remote Control name is whatever you pass after `--remote-control`. To use a separate Claude
Code configuration directory (another account), pass it as an environment variable:

```sh
agentd run api --harness claude -e CLAUDE_CONFIG_DIR=$HOME/.claude-work -- claude --remote-control claude@api
```

If the agent is launched from another Claude Code session and you want it to keep saving its
transcripts, add `-e CLAUDE_CODE_FORCE_SESSION_PERSISTENCE=1`.

## Differences to keep in mind

- **Session names**: launcher scripts often use `claude-<project>`; agentd defaults to
  `<harness>@<label>` (`claude@webapp`). The label is the alias name, else the directory's basename
  (generic basenames `devops`, `app`, `src`, `repo` become `<parent>-<base>`).
- **Targets**: agentd's session commands (`attach`, `dump`, `type`, `kill`) take the session name,
  not an alias or a path.
- **Idempotent start**: `agentd run` fails if the name already exists; `agentd rm` an exited session
  first, or attach to the running one.
- **Pre-flight dialogs**: agentd does not answer first-run or trust dialogs for you. Attach once
  and answer them.
- **Persistence**: zellij/tmux sessions survive independently of any daemon; agentd sessions die
  with the daemon (no persistence across daemon restarts yet, see `ROADMAP.md`). The mailbox
  survives.
- **No panes**: one attached session per client terminal; tiling stays with your terminal or
  multiplexer.
