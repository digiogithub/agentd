package mux

// zellij adapter (CLI 0.44.x; checked against `zellij --help`,
// `zellij list-sessions --help`, `zellij attach --help`,
// `zellij delete-session --help`, `zellij action new-pane --help` and
// `zellij action list-panes --help` of zellij 0.44.3, and
// https://zellij.dev/documentation/cli-actions).
//
//   list:    zellij list-sessions --no-formatting
//   panes:   zellij --session S action list-panes --json --command --state
//            (0.44+; older versions lack it and simply report no panes)
//   attach:  zellij attach S                     (PTY; resurrects EXITED ones)
//   launch:  zellij attach --create-background S
//            zellij --session S action new-pane --cwd DIR --name H -- CMD...
//   kill:    zellij delete-session --force S

import (
	"context"
	"encoding/json"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/digiogithub/agentd/internal/desktopui/backend"
)

// ZellijSessionLine is one line of `zellij list-sessions --no-formatting`.
type ZellijSessionLine struct {
	Name       string
	CreatedAgo time.Duration
	HasCreated bool // CreatedAgo was parsed
	Current    bool
	Exited     bool
}

// ZellijPane is one terminal pane of `zellij action list-panes --json`.
type ZellijPane struct {
	ID      int
	Title   string
	Command string
	Exited  bool
	Focused bool
	TabName string
}

var (
	zellijLineRe = regexp.MustCompile(`^(\S+)\s*\[Created\s+(.*?)\s+ago\]\s*(.*)$`)
	zellijDurRe  = regexp.MustCompile(`(\d+)\s*(years?|weeks?|days?|d|h|m|s)\b`)
)

// ParseZellijSessions parses `zellij list-sessions --no-formatting` output.
// Colours are stripped too, so plain `list-sessions` output also works. "No
// active zellij sessions found." (or empty output) gives an empty list.
func ParseZellijSessions(out string) []ZellijSessionLine {
	var res []ZellijSessionLine
	for _, line := range splitLines(StripANSI(out)) {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(strings.ToLower(line), "no active zellij sessions") {
			continue
		}
		m := zellijLineRe.FindStringSubmatch(line)
		if m == nil {
			// `--short` output or an unknown format: the first word is the name.
			res = append(res, ZellijSessionLine{
				Name:    strings.Fields(line)[0],
				Current: strings.Contains(line, "(current)"),
				Exited:  strings.Contains(line, "EXITED"),
			})
			continue
		}
		d, ok := ParseZellijDuration(m[2])
		res = append(res, ZellijSessionLine{
			Name:       m[1],
			CreatedAgo: d,
			HasCreated: ok,
			Current:    strings.Contains(m[3], "(current)"),
			Exited:     strings.Contains(m[3], "EXITED"),
		})
	}
	return res
}

// ParseZellijDuration parses zellij's age text (`13days 15h 3m 59s`,
// `1h 45m 46s`, `5s`). ok is false when s holds no duration.
func ParseZellijDuration(s string) (d time.Duration, ok bool) {
	var secs int64
	for _, m := range zellijDurRe.FindAllStringSubmatch(s, -1) {
		ok = true
		n, _ := strconv.ParseInt(m[1], 10, 64)
		switch m[2][0] {
		case 'y':
			n *= 365 * 86400
		case 'w':
			n *= 7 * 86400
		case 'd':
			n *= 86400
		case 'h':
			n *= 3600
		case 'm':
			n *= 60
		}
		secs += n
	}
	return time.Duration(secs) * time.Second, ok
}

// ParseZellijPanes parses `zellij action list-panes --json` output, keeping
// terminal panes only (plugins such as the tab bar are dropped). Invalid JSON
// gives nil.
func ParseZellijPanes(out string) []ZellijPane {
	var data []json.RawMessage
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &data); err != nil {
		return nil
	}
	res := []ZellijPane{}
	for _, raw := range data {
		var p map[string]any
		if json.Unmarshal(raw, &p) != nil || p == nil {
			continue
		}
		if b, _ := p["is_plugin"].(bool); b {
			continue
		}
		id, _ := p["id"].(float64)
		title, _ := p["title"].(string)
		cmd, _ := p["terminal_command"].(string)
		exited, _ := p["exited"].(bool)
		focused, _ := p["is_focused"].(bool)
		tab, _ := p["tab_name"].(string)
		res = append(res, ZellijPane{ID: int(id), Title: title, Command: cmd, Exited: exited, Focused: focused, TabName: tab})
	}
	return res
}

const zellijPaneMarker = "@@agentd-app pane "

// ZellijListCommand lists sessions.
const ZellijListCommand = "zellij list-sessions --no-formatting"

// ZellijPanesCommand is one script listing the panes of every session in
// names, each block introduced by a marker line.
func ZellijPanesCommand(names []string) string {
	return "for s in " + ShellJoin(names) + "; do " +
		`printf '` + zellijPaneMarker + `%s\n' "$s"; ` +
		`zellij --session "$s" action list-panes --json --command --state 2>/dev/null; echo; done`
}

// ParseZellijPanesScript splits the output of ZellijPanesCommand by session.
func ParseZellijPanesScript(out string) map[string][]ZellijPane {
	res := map[string][]ZellijPane{}
	cur, has := "", false
	var buf strings.Builder
	flush := func() {
		if has && strings.TrimSpace(buf.String()) != "" {
			res[cur] = ParseZellijPanes(buf.String())
		}
		buf.Reset()
	}
	for _, line := range splitLines(out) {
		if rest, ok := strings.CutPrefix(line, zellijPaneMarker); ok {
			flush()
			cur, has = strings.TrimSpace(rest), true
		} else {
			buf.WriteString(line)
			buf.WriteByte('\n')
		}
	}
	flush()
	return res
}

// ZellijAttachCommand attaches to (or resurrects) session name.
func ZellijAttachCommand(name string) string { return "zellij attach " + ShellQuote(name) }

// ZellijLaunchCommands are the commands Launch runs, in order.
func ZellijLaunchCommands(session, cwd string, command []string, paneName string) []string {
	s := ShellQuote(session)
	n := ""
	if paneName != "" {
		n = " --name " + ShellQuote(paneName)
	}
	return []string{
		"zellij attach --create-background " + s,
		"zellij --session " + s + " action new-pane --cwd " + ShellDir(cwd) + n + " -- " + ShellJoin(command),
	}
}

// Zellij is the zellij Adapter of one host.
type Zellij struct {
	sh     backend.Shell
	hostID string
	now    func() time.Time
}

// NewZellij returns the zellij adapter for the host behind sh.
func NewZellij(sh backend.Shell, hostID string) *Zellij {
	return &Zellij{sh: sh, hostID: hostID, now: time.Now}
}

// Source is backend.SourceZellij.
func (z *Zellij) Source() backend.Source { return backend.SourceZellij }

// List lists the sessions and, in a second exec, the panes of the running
// ones (for the harness guess and the pane count).
func (z *Zellij) List(ctx context.Context) ([]backend.Session, error) {
	r, err := z.sh.Exec(ctx, ZellijListCommand, nil)
	if err != nil {
		return nil, err
	}
	if err := checkInstalled("zellij", r); err != nil {
		return nil, err
	}
	text := strings.ToLower(r.Stdout + "\n" + r.Stderr)
	if r.Exit != 0 && !strings.Contains(text, "no active zellij sessions") && strings.TrimSpace(r.Stdout) == "" {
		return nil, failed(r, "zellij list-sessions failed: %s", strings.TrimSpace(r.Stderr))
	}
	lines := ParseZellijSessions(r.Stdout)
	var live []string
	for _, l := range lines {
		if !l.Exited {
			live = append(live, l.Name)
		}
	}
	panes := map[string][]ZellijPane{}
	if len(live) > 0 {
		p, err := z.sh.Exec(ctx, ZellijPanesCommand(live), nil)
		if err != nil {
			return nil, err
		}
		panes = ParseZellijPanesScript(p.Stdout)
	}
	now := z.now()
	res := make([]backend.Session, 0, len(lines))
	for _, l := range lines {
		p, ok := panes[l.Name]
		res = append(res, z.toSession(l, p, ok, now))
	}
	return res, nil
}

// toSession maps one line; withPanes says whether panes were reported.
func (z *Zellij) toSession(l ZellijSessionLine, panes []ZellijPane, withPanes bool, now time.Time) backend.Session {
	harness, proj := SplitSessionName(l.Name)
	// The focused pane first, then any other pane.
	var focused, others []ZellijPane
	for _, p := range panes {
		switch {
		case p.Exited:
		case p.Focused:
			focused = append(focused, p)
		default:
			others = append(others, p)
		}
	}
	alive := append(focused, others...)
	for _, p := range alive {
		if h := GuessHarness(p.Command, p.Title); h != "" {
			harness = h
			break
		}
	}
	var detail []string
	if l.Current {
		detail = append(detail, "current")
	}
	if l.Exited {
		detail = append(detail, "exited")
	}
	if withPanes {
		detail = append(detail, plural(len(alive), "pane", "panes"))
	}
	s := backend.Session{
		HostID:  z.hostID,
		Source:  backend.SourceZellij,
		Name:    l.Name,
		Harness: harness,
		Project: proj,
		Status:  StatusUnknown,
		Detail:  strings.Join(detail, " · "),
	}
	if l.Exited {
		s.Status = StatusExited
	}
	if l.HasCreated {
		s.Updated = now.Add(-l.CreatedAgo)
	}
	return s
}

// Attach opens `zellij attach S` in a PTY.
func (z *Zellij) Attach(ctx context.Context, name string, cols, rows int) (backend.PTY, error) {
	return z.sh.PTY(ctx, ZellijAttachCommand(name), cols, rows, nil)
}

// Launch creates the session in the background (if needed) and opens a pane
// running the harness in the project directory.
func (z *Zellij) Launch(ctx context.Context, spec backend.LaunchSpec) (backend.Session, error) {
	lp, err := planLaunch(spec)
	if err != nil {
		return backend.Session{}, err
	}
	for _, c := range ZellijLaunchCommands(lp.session, lp.cwd, lp.argv, lp.harness) {
		r, err := z.sh.Exec(ctx, c, nil)
		if err != nil {
			return backend.Session{}, err
		}
		if err := checkInstalled("zellij", r); err != nil {
			return backend.Session{}, err
		}
		if r.Exit != 0 {
			return backend.Session{}, failed(r, "`%s` failed: %s", c, strings.TrimSpace(r.Stderr))
		}
	}
	return backend.Session{
		HostID:  z.hostID,
		Source:  backend.SourceZellij,
		Name:    lp.session,
		Harness: lp.harness,
		Project: project(lp.session, lp.cwd),
		Status:  StatusUnknown,
		Updated: z.now(),
	}, nil
}

// Kill deletes the session; --force kills a running one first, and deleting
// also drops the resurrectable (EXITED) state.
func (z *Zellij) Kill(ctx context.Context, name string) error {
	r, err := z.sh.Exec(ctx, "zellij delete-session --force "+ShellQuote(name), nil)
	if err != nil {
		return err
	}
	if err := checkInstalled("zellij", r); err != nil {
		return err
	}
	if r.Exit != 0 && !strings.Contains(strings.ToLower(r.Stderr), "not found") {
		return failed(r, "zellij delete-session failed: %s", strings.TrimSpace(r.Stderr))
	}
	return nil
}
