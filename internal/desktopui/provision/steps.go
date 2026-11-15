package provision

// Install steps: every step is one self-contained, idempotent POSIX sh
// script, shown to the user before it runs (Service.Plan) and run as is
// (Service.Install). Runtime facts (npm prefix permissions, existing config,
// a running daemon) are checked by the scripts themselves on the host, so
// the plan is exact whatever detection missed. Port of agentd-app
// lib/src/provision/steps.dart (same scripts).
//
// Sources:
//   - agentd: install.sh and docs/release.md of this repo: the release
//     attaches `install.sh` and `SHA256SUMS` (which lists install.sh), so
//     `https://github.com/digiogithub/agentd/releases/latest/download/<file>`
//     always serves the latest; install.sh resolves the tag from the
//     `/releases/latest` redirect, picks `agentd_<v>_linux_<amd64|arm64>.tar.gz`
//     or `agentd_<v>_darwin_universal.zip`, verifies it against SHA256SUMS,
//     installs `~/.local/bin/agentd` and, on Linux, writes the systemd user
//     unit from the tarball unless one exists.
//   - service: README.md "systemd (optional)", contrib/agentd.service
//     (`systemctl --user daemon-reload && systemctl --user enable --now agentd`,
//     `loginctl enable-linger`).
//   - hc listener: AGENTS.md "Herdr compatibility": `[herdr] listen` is read at
//     daemon start; `hc server start --json` (`agentd hc server start`) asks a
//     running daemon to bind it and prints `agentd.listener.listening`.
//   - MCP: `agentd mcp install --print` only prints snippets (cmd/agentd
//     mcpInstallCmd, "never writes config files"), so the steps apply the
//     documented snippets themselves: `claude mcp add --scope user agentd --
//     <agentd> mcp`, `[mcp_servers.agentd]` in ~/.codex/config.toml,
//     `[MCPServers.agentd]` in ~/.pando.toml.
//   - CLIs: npm packages; Pando: the Pando README and scripts/install.sh
//     (`--no-desktop --cli-only`: no GTK packages or sudo).

import (
	"slices"
	"time"

	"github.com/digiogithub/agentd/internal/desktopui/backend"
	"github.com/digiogithub/agentd/internal/desktopui/mux"
)

// Step names (backend.PlannedStep.Step); CLI steps are "cli:<name>".
const (
	StepDownload   = "download"
	StepVerify     = "verify"
	StepInstall    = "install"
	StepService    = "service"
	StepHCListener = "hc_listener"
	StepMCP        = "mcp"
	StepCLIPrefix  = "cli:"
)

const (
	agentdLatest   = "https://github.com/digiogithub/agentd/releases/latest/download"
	pandoInstaller = "https://raw.githubusercontent.com/digiogithub/pando/main/scripts/install.sh"
)

// npmPackages are the npm packages of the npm-installed CLIs.
var npmPackages = map[string]string{
	"claude":   "@anthropic-ai/claude-code",
	"codex":    "@openai/codex",
	"opencode": "opencode-ai",
	"gemini":   "@google/gemini-cli",
}

var installableCLIs = []string{"claude", "codex", "pando", "opencode", "gemini"}

// mcpHarnesses are the harnesses whose MCP registration the mcp step writes.
var mcpHarnesses = []string{"claude", "codex", "pando"}

// InstallableCLIs returns the CLIs Plan knows how to install, in plan order.
func InstallableCLIs() []string { return slices.Clone(installableCLIs) }

// step is one planned step with what Install needs besides the contract.
type step struct {
	backend.PlannedStep
	// group: steps of one group stop at the first failure (download →
	// verify → install of agentd).
	group string
	// needs: groups that must not have failed for this step to run.
	needs   []string
	timeout time.Duration
}

const (
	shPath      = `PATH="$HOME/.local/bin:$PATH"; export PATH`
	shCache     = `d="${XDG_CACHE_HOME:-$HOME/.cache}/agentd-app"`
	shNeedAgent = `a=$(command -v agentd) || { echo "agentd is not installed" >&2; exit 127; }`
)

func scriptDownload() string {
	return `set -e
` + shPath + `
` + shCache + `
mkdir -p "$d"
command -v curl >/dev/null 2>&1 || { echo "curl is required" >&2; exit 127; }
curl -fsSL -o "$d/install.sh" ` + agentdLatest + `/install.sh
curl -fsSL -o "$d/SHA256SUMS" ` + agentdLatest + `/SHA256SUMS
echo "downloaded install.sh and SHA256SUMS to $d"`
}

func scriptVerify() string {
	return `set -e
` + shCache + `
want=$(awk '$2 == "install.sh" || $2 == "*install.sh" { print $1 }' "$d/SHA256SUMS")
[ -n "$want" ] || { echo "install.sh is not listed in SHA256SUMS" >&2; exit 1; }
if command -v sha256sum >/dev/null 2>&1; then got=$(sha256sum "$d/install.sh" | cut -d' ' -f1)
elif command -v shasum >/dev/null 2>&1; then got=$(shasum -a 256 "$d/install.sh" | cut -d' ' -f1)
else echo "needs sha256sum or shasum" >&2; exit 127; fi
[ "$got" = "$want" ] || { echo "checksum mismatch for install.sh (got $got, want $want)" >&2; exit 1; }
echo "install.sh sha256 $got"`
}

func scriptInstall() string {
	return `set -e
` + shPath + `
` + shCache + `
sh "$d/install.sh"
agentd version`
}

const shSock = `sock=${AGENTD_SOCKET:-}
if [ -z "$sock" ]; then
  if [ -n "${XDG_RUNTIME_DIR:-}" ]; then sock=$XDG_RUNTIME_DIR/agentd/agentd.sock; else sock=/tmp/agentd-$(id -u)/agentd.sock; fi
fi`

func scriptServiceSystemd() string {
	return `set -e
` + shPath + `
` + shSock + `
systemctl --user daemon-reload
systemctl --user cat agentd >/dev/null 2>&1 || { echo "no systemd user unit agentd.service (see contrib/agentd.service)" >&2; exit 1; }
if ! systemctl --user is-active --quiet agentd && [ -S "$sock" ]; then
  # A CLI-autostarted daemon owns the socket: starting the unit now would
  # fail (or, after a shutdown, end its sessions). Enable it for next time.
  systemctl --user enable agentd
  echo "agentd already runs outside systemd; the unit is enabled for the next login"
  exit 0
fi
systemctl --user enable --now agentd
loginctl enable-linger "$(id -un)" 2>/dev/null || echo "note: lingering not enabled: agentd stops when your last login session ends"
systemctl --user is-active agentd`
}

func scriptServiceAutostart() string {
	return `set -e
` + shPath + `
` + shNeedAgent + `
"$a" ls >/dev/null
echo "agentd daemon running (started by the CLI; no systemd user manager on this host)"`
}

func scriptHCListener() string {
	return `set -e
` + shPath + `
f="${XDG_CONFIG_HOME:-$HOME/.config}/agentd/config.toml"
mkdir -p "$(dirname "$f")"
hdr='^[[:space:]]*\[herdr\][[:space:]]*(#.*)?$'
if [ -f "$f" ] && grep -Eq "$hdr" "$f"; then
  cur=$(awk '/^[ \t]*\[/ { t = ($0 ~ /^[ \t]*\[herdr\][ \t]*(#.*)?$/) } t && /^[ \t]*listen[ \t]*=/ { sub(/^[^=]*=[ \t]*/, ""); sub(/[ \t]*(#.*)?$/, ""); print; exit }' "$f")
  if [ -n "$cur" ]; then
    echo "kept [herdr] listen = $cur in $f"
  else
    awk '{ print } !done && /^[ \t]*\[herdr\][ \t]*(#.*)?$/ { print "listen = true"; done = 1 }' "$f" > "$f.agentd-app.tmp"
    cat "$f.agentd-app.tmp" > "$f"; rm -f "$f.agentd-app.tmp"
    echo "added listen = true to [herdr] in $f"
  fi
else
  printf '\n[herdr]\nlisten = true\n' >> "$f"
  echo "appended [herdr] listen = true to $f"
fi
command -v agentd >/dev/null 2>&1 || { echo "agentd is not installed yet: the listener starts with the daemon"; exit 0; }
st=$(agentd hc server status --json 2>/dev/null) || { echo "agentd daemon not running: the listener starts with it"; exit 0; }
case $st in *'"listening":true'*) echo "hc listener already listening"; exit 0 ;; esac
out=$(agentd hc server start --json 2>/dev/null || true)
case $out in *'"listening":true'*) echo "hc listener listening"; exit 0 ;; esac
n=$(agentd ls --json 2>/dev/null | grep -c '"state": *"running"' || true)
if [ "${n:-0}" -gt 0 ]; then
  echo "restart agentd to start the hc listener ($n running sessions would end; not restarted)"
  exit 0
fi
if systemctl --user is-active --quiet agentd 2>/dev/null; then systemctl --user restart agentd
else agentd shutdown >/dev/null 2>&1 || true; fi
sleep 1
out=$(agentd hc server start --json 2>&1 || true)
case $out in *'"listening":true'*) echo "hc listener listening (daemon restarted; no session was running)" ;;
  *) echo "hc listener not listening: $out" >&2; exit 1 ;; esac`
}

func scriptMCPClaude() string {
	return `set -e
` + shPath + `
` + shNeedAgent + `
command -v claude >/dev/null 2>&1 || { echo "claude is not installed" >&2; exit 127; }
if claude mcp get agentd >/dev/null 2>&1; then echo "claude: agentd MCP server already registered"; exit 0; fi
claude mcp add --scope user agentd -- "$a" mcp`
}

func scriptMCPCodex() string {
	return `set -e
` + shPath + `
` + shNeedAgent + `
f="${CODEX_HOME:-$HOME/.codex}/config.toml"
if [ -f "$f" ] && grep -Eq '^[[:space:]]*\[mcp_servers\.("agentd"|agentd)\]' "$f"; then echo "codex: agentd MCP server already in $f"; exit 0; fi
mkdir -p "$(dirname "$f")"
printf '\n[mcp_servers.agentd]\ncommand = "%s"\nargs = ["mcp"]\n' "$a" >> "$f"
echo "codex: added [mcp_servers.agentd] to $f"`
}

func scriptMCPPando() string {
	return `set -e
` + shPath + `
` + shNeedAgent + `
x="${XDG_CONFIG_HOME:-$HOME/.config}/pando"
f=
for c in "$HOME/.pando.toml" "$x/.pando.toml"; do [ -f "$c" ] && { f=$c; break; }; done
if [ -z "$f" ]; then
  for j in "$HOME/.pando.json" "$x/.pando.json"; do
    [ -f "$j" ] && { echo "pando uses $j: add mcpServers.agentd there (agentd mcp install --print --harness pando)" >&2; exit 1; }
  done
  f="$HOME/.pando.toml"
fi
if grep -Eq '^[[:space:]]*\[MCPServers\.("agentd"|agentd)\]' "$f" 2>/dev/null; then echo "pando: agentd MCP server already in $f"; exit 0; fi
printf "\n[MCPServers.agentd]\nType = 'stdio'\nCommand = '%s'\nArgs = ['mcp']\n" "$a" >> "$f"
echo "pando: added [MCPServers.agentd] to $f"`
}

func scriptNPMCLI(cli, pkg string) string {
	q := mux.ShellQuote(pkg)
	return `set -e
` + shPath + `
if command -v ` + cli + ` >/dev/null 2>&1; then echo "` + cli + ` already installed: $(command -v ` + cli + `)"; exit 0; fi
command -v npm >/dev/null 2>&1 || { echo "npm is not installed (install Node.js first)" >&2; exit 127; }
p=$(npm config get prefix 2>/dev/null || true)
if [ -n "$p" ] && [ -w "$p" ] && { [ ! -e "$p/lib/node_modules" ] || [ -w "$p/lib/node_modules" ]; }; then
  npm install -g ` + q + `
else
  # The global prefix is not writable (system Node): install for this user.
  npm install -g --prefix "$HOME/.local" ` + q + `
fi
command -v ` + cli
}

func scriptPandoCLI() string {
	return `set -e
` + shPath + `
if command -v pando >/dev/null 2>&1; then echo "pando already installed: $(command -v pando)"; exit 0; fi
command -v bash >/dev/null 2>&1 || { echo "the Pando installer needs bash" >&2; exit 127; }
command -v curl >/dev/null 2>&1 || { echo "curl is required" >&2; exit 127; }
` + shCache + `
mkdir -p "$d"
curl -fsSL -o "$d/pando-install.sh" ` + pandoInstaller + `
bash "$d/pando-install.sh" --no-desktop --cli-only
command -v pando`
}

const (
	stepTimeout = 5 * time.Minute
	cliTimeout  = 10 * time.Minute
)

func newStep(name, title, cmd, group string, needs ...string) step {
	return step{
		PlannedStep: backend.PlannedStep{Step: name, Title: title, Command: cmd},
		group:       group,
		needs:       needs,
		timeout:     stepTimeout,
	}
}

// buildPlan builds the ordered install plan. Steps are left out when
// detection says their result is already there (agentd installed, CLI
// installed); the remaining scripts also check on the host and do nothing
// twice.
func buildPlan(caps *backend.Capabilities, opts backend.ProvisionOptions) []step {
	if caps == nil {
		caps = &backend.Capabilities{}
	}
	var steps []step
	if opts.Agentd && !caps.Has("agentd") {
		steps = append(steps,
			newStep(StepDownload, "Download the agentd installer", scriptDownload(), "agentd"),
			newStep(StepVerify, "Verify install.sh against SHA256SUMS", scriptVerify(), "agentd"),
			newStep(StepInstall, "Install agentd into ~/.local/bin", scriptInstall(), "agentd"),
		)
	}
	// The listener setting goes in before the service starts, so a fresh
	// daemon reads it at its first start.
	if opts.HCListener {
		steps = append(steps, newStep(StepHCListener, "Enable the hc socket listener ([herdr] listen = true)", scriptHCListener(), "hc", "agentd"))
	}
	if opts.Agentd {
		if caps.SystemdUser {
			steps = append(steps, newStep(StepService, "Enable the agentd systemd user service", scriptServiceSystemd(), "service", "agentd"))
		} else {
			steps = append(steps, newStep(StepService, "Start the agentd daemon", scriptServiceAutostart(), "service", "agentd"))
		}
	}
	for _, cli := range installableCLIs {
		if !slices.Contains(opts.CLIs, cli) || caps.Has(cli) {
			continue
		}
		name := StepCLIPrefix + cli
		var s step
		if pkg, ok := npmPackages[cli]; ok {
			s = newStep(name, "Install "+cli+" (npm "+pkg+")", scriptNPMCLI(cli, pkg), name)
		} else {
			s = newStep(name, "Install "+cli+" (official installer)", scriptPandoCLI(), name)
		}
		s.timeout = cliTimeout
		steps = append(steps, s)
	}
	if opts.MCP {
		for _, h := range mcpHarnesses {
			if !caps.Has(h) && !slices.Contains(opts.CLIs, h) {
				continue
			}
			var cmd string
			switch h {
			case "claude":
				cmd = scriptMCPClaude()
			case "codex":
				cmd = scriptMCPCodex()
			default:
				cmd = scriptMCPPando()
			}
			steps = append(steps, newStep(StepMCP, "Register the agentd MCP server in "+h, cmd, "mcp:"+h, "agentd", StepCLIPrefix+h))
		}
	}
	return steps
}
