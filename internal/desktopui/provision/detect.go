package provision

// Host detection: one POSIX sh script (no jq/python/bash needed on the host)
// printing `@@agentd-app <section>` marker lines, and its parser. Port of
// agentd-app lib/src/provision/detect.dart.

import (
	"errors"
	"regexp"
	"strings"

	"github.com/digiogithub/agentd/internal/desktopui/backend"
)

// detectTools are the tools probed by the detect script, in order, with the
// arguments that print their version (tmux has no --version; agentd prints it
// with `version`).
var detectTools = []struct{ name, args string }{
	{"agentd", "version"},
	{"zellij", "--version"},
	{"tmux", "-V"},
	{"node", "--version"},
	{"npm", "--version"},
	{"claude", "--version"},
	{"codex", "--version"},
	{"pando", "--version"},
	{"opencode", "--version"},
	{"gemini", "--version"},
}

// ToolNames returns the names of the tools Detect probes, in order.
func ToolNames() []string {
	r := make([]string, len(detectTools))
	for i, t := range detectTools {
		r[i] = t.name
	}
	return r
}

const marker = "@@agentd-app "

const detectHead = `PATH0=$PATH
PATH="$HOME/.local/bin:$HOME/bin:$PATH"; export PATH
m() { printf '@@agentd-app %s\n' "$*"; }
if command -v timeout >/dev/null 2>&1; then TO=timeout
elif command -v gtimeout >/dev/null 2>&1; then TO=gtimeout
else TO=; fi
run5() {
  if [ -n "$TO" ]; then "$TO" 5 "$@"; return; fi
  o5=$(mktemp 2>/dev/null) || o5="$HOME/.agentd-app-run5.$$"
  "$@" >"$o5" 2>&1 & p5=$!
  ( sleep 5; kill "$p5" 2>/dev/null ) >/dev/null 2>&1 &
  w5=$!
  wait "$p5" 2>/dev/null; s5=$?
  kill "$w5" 2>/dev/null
  cat "$o5" 2>/dev/null; rm -f "$o5" 2>/dev/null
  return $s5
}
tool() {
  n=$1; shift
  m "tool $n"
  p=$(command -v "$n" 2>/dev/null) || p=
  case $p in /*) ;; *) return 0 ;; esac
  printf 'path=%s\n' "$p"
  v=$(run5 "$p" "$@" </dev/null 2>&1 | sed -n '/[^[:space:]]/{p;q;}')
  printf 'version=%s\n' "$v"
}
m os; uname -s 2>/dev/null
m arch; uname -m 2>/dev/null
m os-release; [ -r /etc/os-release ] && cat /etc/os-release
m macos
if command -v sw_vers >/dev/null 2>&1; then
  printf 'name=%s\nversion=%s\n' "$(sw_vers -productName 2>/dev/null)" "$(sw_vers -productVersion 2>/dev/null)"
fi
m uid; id -u
m home; printf '%s\n' "$HOME"
m path; printf '%s\n' "$PATH0"
m runtime-dir; printf '%s\n' "${XDG_RUNTIME_DIR:-}"
m systemd
if command -v systemctl >/dev/null 2>&1 && run5 systemctl --user show-environment </dev/null >/dev/null 2>&1; then echo yes; else echo no; fi
`

const detectTail = `m socket
u=$(id -u)
for s in "${AGENTD_SOCKET:-}" "${XDG_RUNTIME_DIR:+$XDG_RUNTIME_DIR/agentd/agentd.sock}" "/run/user/$u/agentd/agentd.sock" "/tmp/agentd-$u/agentd.sock"; do
  [ -n "$s" ] && [ -S "$s" ] && printf 'live=%s\n' "$s"
done
if [ -n "${AGENTD_SOCKET:-}" ]; then printf 'default=%s\n' "$AGENTD_SOCKET"
elif [ -n "${XDG_RUNTIME_DIR:-}" ]; then printf 'default=%s\n' "$XDG_RUNTIME_DIR/agentd/agentd.sock"
else printf 'default=/tmp/agentd-%s/agentd.sock\n' "$u"; fi
m end
`

var detectScript = func() string {
	var b strings.Builder
	b.WriteString(detectHead)
	for _, t := range detectTools {
		b.WriteString("tool " + t.name + " " + t.args + "\n")
	}
	b.WriteString(detectTail)
	return b.String()
}()

// DetectScript is the detection script, run through the host's login shell.
// Every probe has its stdin closed and a 5 s limit (`timeout`/`gtimeout` when
// present, else a sleep-and-kill watchdog), so a tool that waits for input or
// hangs cannot stall detection. Deviation from the Dart original, found by
// the sandbox test of the watchdog path (hosts without timeout/gtimeout, such
// as stock macOS): the probe writes to a temp file instead of the pipe (a
// grandchild the watchdog does not kill, e.g. the `sleep` of a shell wrapper,
// kept the pipe open and stalled detection until it ended) and the watchdog's
// `wait` has its stderr discarded (the shell reports the killed job there as
// "Terminated", which became the tool's version). `~/.local/bin` and `~/bin` are searched as
// well: non-interactive login shells often do not have them on PATH, and that
// is where the installers put binaries. Tool paths are absolute (`command -v`
// results that are not absolute, such as aliases or functions, are ignored).
func DetectScript() string { return detectScript }

// report is everything the detect script printed, by section.
type report struct {
	sections map[string][]string
}

func (r report) first(s string) string {
	for _, l := range r.sections[s] {
		if strings.TrimSpace(l) != "" {
			return strings.TrimSpace(l)
		}
	}
	return ""
}

func (r report) kv(s string) map[string]string {
	res := map[string]string{}
	for _, l := range r.sections[s] {
		if i := strings.Index(l, "="); i > 0 {
			k := strings.TrimSpace(l[:i])
			if _, ok := res[k]; !ok {
				res[k] = strings.TrimSpace(l[i+1:])
			}
		}
	}
	return res
}

// complete reports whether the end marker was seen (a truncated run is
// suspect).
func (r report) complete() bool {
	_, ok := r.sections["end"]
	return ok
}

// localBinOnPath: `~/.local/bin` is on the login PATH (binaries installed
// there are then found by `sh -lc`).
func (r report) localBinOnPath() bool {
	h, p := r.first("home"), r.first("path")
	if h == "" || p == "" {
		return false
	}
	for _, d := range strings.Split(p, ":") {
		if d == h+"/.local/bin" {
			return true
		}
	}
	return false
}

func unquote(v string) string {
	if len(v) >= 2 && (v[0] == '"' && v[len(v)-1] == '"' || v[0] == '\'' && v[len(v)-1] == '\'') {
		return v[1 : len(v)-1]
	}
	return v
}

func joinNonEmpty(parts ...string) string {
	var r []string
	for _, p := range parts {
		if p != "" {
			r = append(r, p)
		}
	}
	return strings.Join(r, " ")
}

func (r report) distro() string {
	osr := r.kv("os-release")
	for k, v := range osr {
		osr[k] = unquote(v)
	}
	if p := osr["PRETTY_NAME"]; p != "" {
		return p
	}
	if n := osr["NAME"]; n != "" {
		return joinNonEmpty(n, osr["VERSION_ID"])
	}
	mac := r.kv("macos")
	if n := mac["name"]; n != "" {
		return joinNonEmpty(n, mac["version"])
	}
	return ""
}

func (r report) tools() map[string]backend.Tool {
	res := make(map[string]backend.Tool, len(detectTools))
	for _, t := range detectTools {
		kv := r.kv("tool " + t.name)
		path := kv["path"]
		tool := backend.Tool{Name: t.name, Installed: path != "", Path: path}
		if path != "" {
			tool.Version = CleanVersion(kv["version"])
		}
		res[t.name] = tool
	}
	return res
}

// liveSockets are the live daemon sockets found, most specific first.
func (r report) liveSockets() []string {
	var res []string
	for _, l := range r.sections["socket"] {
		if s, ok := strings.CutPrefix(l, "live="); ok {
			res = append(res, strings.TrimSpace(s))
		}
	}
	return res
}

func (r report) defaultSocket() string { return r.kv("socket")["default"] }

func (r report) capabilities() *backend.Capabilities {
	tools := r.tools()
	sock := ""
	if live := r.liveSockets(); len(live) > 0 {
		sock = live[0]
	} else if tools["agentd"].Installed {
		// Where the daemon will create it.
		sock = r.defaultSocket()
	}
	return &backend.Capabilities{
		OS:             r.first("os"),
		Arch:           r.first("arch"),
		Distro:         r.distro(),
		SystemdUser:    r.first("systemd") == "yes",
		RuntimeDir:     r.first("runtime-dir"),
		AgentdSocket:   sock,
		Home:           r.first("home"),
		LocalBinOnPath: r.localBinOnPath(),
		Tools:          tools,
	}
}

var semver = regexp.MustCompile(`v?(\d+\.\d+(?:\.\d+)?(?:[-+][0-9A-Za-z.+-]+)?)`)

// CleanVersion extracts the version number of a `--version` line
// (`zellij 0.44.3` → `0.44.3`, `v22.11.0` → `22.11.0`, `2.0.76 (Claude Code)`
// → `2.0.76`, `tmux 3.4` → `3.4`, `tmux next-3.5` → `3.5`); the trimmed line
// when it has no number; "" when empty.
func CleanVersion(line string) string {
	l := strings.TrimSpace(line)
	if l == "" {
		return ""
	}
	if m := semver.FindStringSubmatch(l); m != nil {
		return m[1]
	}
	return l
}

func parseReport(out string) report {
	sections := map[string][]string{}
	cur := ""
	inSection := false
	out = strings.ReplaceAll(out, "\r\n", "\n")
	out = strings.ReplaceAll(out, "\r", "\n")
	for _, l := range strings.Split(out, "\n") {
		if name, ok := strings.CutPrefix(l, marker); ok {
			cur, inSection = strings.TrimSpace(name), true
			if _, ok := sections[cur]; !ok {
				sections[cur] = []string{}
			}
		} else if inSection {
			sections[cur] = append(sections[cur], l)
		}
	}
	return report{sections: sections}
}

// ErrNoOS is returned by ParseDetect when the output has no os section (the
// script did not run).
var ErrNoOS = errors.New("provision: detect output has no os section")

// ParseDetect parses the output of DetectScript.
func ParseDetect(out string) (*backend.Capabilities, error) {
	r := parseReport(out)
	c := r.capabilities()
	if c.OS == "" {
		return c, ErrNoOS
	}
	return c, nil
}
