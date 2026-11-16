package mux

// tmux adapter. Commands and format variables from tmux(1)
// (https://man7.org/linux/man-pages/man1/tmux.1.html, FORMATS and the
// list-sessions / list-panes / attach-session / new-session / kill-session
// entries); tmux >= 2.6 for `-c` on new-session and `=` exact targets:
//
//   list:    tmux list-sessions -F '<session vars joined by TAB>'
//   panes:   tmux list-panes -a -F '<pane vars joined by TAB>'
//   attach:  tmux attach-session -t =S              (PTY)
//   launch:  tmux new-session -d -s S -c DIR 'CMD'  (or new-window -t =S:)
//   kill:    tmux kill-session -t =S
//
// "no server running" / "error connecting to ..." mean no sessions.

import (
	"context"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/digiogithub/agentd/internal/desktopui/backend"
)

// TmuxSessionLine is one line of TmuxListCommand.
type TmuxSessionLine struct {
	Name     string
	Windows  int
	Attached int // number of attached clients
	Activity time.Time
	Created  time.Time
	Path     string
}

// TmuxPane is one line of TmuxPanesCommand.
type TmuxPane struct {
	Session      string
	Window       int
	Pane         int
	Active       bool
	WindowActive bool
	Command      string
	Path         string
	Title        string
	Dead         bool
}

var (
	tmuxSessionVars = []string{"session_name", "session_windows", "session_attached", "session_activity", "session_created", "session_path"}
	tmuxPaneVars    = []string{"session_name", "window_index", "pane_index", "pane_active", "window_active", "pane_current_command", "pane_current_path", "pane_title", "pane_dead"}
)

func tmuxFormat(vars []string) string {
	f := make([]string, len(vars))
	for i, v := range vars {
		f[i] = "#{" + v + "}"
	}
	return strings.Join(f, "\t")
}

var (
	// TmuxListCommand lists sessions.
	TmuxListCommand = "tmux list-sessions -F " + ShellQuote(tmuxFormat(tmuxSessionVars))
	// TmuxPanesCommand lists the panes of every session.
	TmuxPanesCommand = "tmux list-panes -a -F " + ShellQuote(tmuxFormat(tmuxPaneVars))
)

// TmuxNoServer reports whether r is tmux saying there is no server (hence
// no sessions).
func TmuxNoServer(r backend.ExecResult) bool {
	e := strings.ToLower(r.Stderr)
	return strings.Contains(e, "no server running") || strings.Contains(e, "error connecting to") ||
		strings.Contains(e, "no sessions") || strings.Contains(e, "failed to connect to server")
}

func tmuxEpoch(s string) time.Time {
	n, err := strconv.ParseInt(strings.TrimSpace(s), 10, 64)
	if err != nil || n <= 0 {
		return time.Time{}
	}
	return time.Unix(n, 0).UTC()
}

func atoi(s string) int {
	n, _ := strconv.Atoi(s)
	return n
}

// tmuxFields splits a format line. Some tmux builds print non-printable
// characters of a format as octal escapes; `\011` and a literal `\t` are
// accepted as the separator too.
func tmuxFields(line string) func(int) string {
	line = strings.ReplaceAll(line, `\011`, "\t")
	line = strings.ReplaceAll(line, `\t`, "\t")
	f := strings.Split(line, "\t")
	return func(i int) string {
		if i < len(f) {
			return f[i]
		}
		return ""
	}
}

// ParseTmuxSessions parses the output of TmuxListCommand.
func ParseTmuxSessions(out string) []TmuxSessionLine {
	var res []TmuxSessionLine
	for _, line := range splitLines(out) {
		if strings.TrimSpace(line) == "" {
			continue
		}
		at := tmuxFields(line)
		res = append(res, TmuxSessionLine{
			Name:     at(0),
			Windows:  atoi(at(1)),
			Attached: atoi(at(2)),
			Activity: tmuxEpoch(at(3)),
			Created:  tmuxEpoch(at(4)),
			Path:     at(5),
		})
	}
	return res
}

// ParseTmuxPanes parses the output of TmuxPanesCommand.
func ParseTmuxPanes(out string) []TmuxPane {
	var res []TmuxPane
	for _, line := range splitLines(out) {
		if strings.TrimSpace(line) == "" {
			continue
		}
		at := tmuxFields(line)
		res = append(res, TmuxPane{
			Session:      at(0),
			Window:       atoi(at(1)),
			Pane:         atoi(at(2)),
			Active:       at(3) == "1",
			WindowActive: at(4) == "1",
			Command:      at(5),
			Path:         at(6),
			Title:        at(7),
			Dead:         at(8) == "1",
		})
	}
	return res
}

func tmuxTarget(name string) string { return ShellQuote("=" + name) }

// TmuxAttachCommand attaches to session name (exact match).
func TmuxAttachCommand(name string) string { return "tmux attach-session -t " + tmuxTarget(name) }

// TmuxLaunchCommand is the command Launch runs: a new detached session, or a
// new window when the session already exists. tmux runs the command string
// with the user's default shell.
func TmuxLaunchCommand(session, cwd string, command []string, windowName string) string {
	s := ShellQuote(session)
	dir := ShellDir(cwd)
	cmd := ""
	if len(command) > 0 { // none = the default shell
		cmd = " " + ShellQuote(ShellJoin(command))
	}
	n := ""
	if windowName != "" {
		n = " -n " + ShellQuote(windowName)
	}
	return "if tmux has-session -t " + tmuxTarget(session) + " 2>/dev/null; " +
		"then tmux new-window -t " + ShellQuote("="+session+":") + n + " -c " + dir + cmd + "; " +
		"else tmux new-session -d -s " + s + n + " -c " + dir + cmd + "; fi"
}

// Tmux is the tmux Adapter of one host.
type Tmux struct {
	sh     backend.Shell
	hostID string
	now    func() time.Time
}

// NewTmux returns the tmux adapter for the host behind sh.
func NewTmux(sh backend.Shell, hostID string) *Tmux {
	return &Tmux{sh: sh, hostID: hostID, now: time.Now}
}

// Source is backend.SourceTmux.
func (t *Tmux) Source() backend.Source { return backend.SourceTmux }

// List lists sessions and (second exec) every pane.
func (t *Tmux) List(ctx context.Context) ([]backend.Session, error) {
	r, err := t.sh.Exec(ctx, TmuxListCommand, nil)
	if err != nil {
		return nil, err
	}
	if err := checkInstalled("tmux", r); err != nil {
		return nil, err
	}
	if r.Exit != 0 {
		if TmuxNoServer(r) {
			return []backend.Session{}, nil
		}
		return nil, failed(r, "tmux list-sessions failed: %s", strings.TrimSpace(r.Stderr))
	}
	sessions := ParseTmuxSessions(r.Stdout)
	var panes []TmuxPane
	if len(sessions) > 0 {
		p, err := t.sh.Exec(ctx, TmuxPanesCommand, nil)
		if err != nil {
			return nil, err
		}
		if p.Exit == 0 {
			panes = ParseTmuxPanes(p.Stdout)
		}
	}
	res := make([]backend.Session, 0, len(sessions))
	for _, s := range sessions {
		var mine []TmuxPane
		for _, p := range panes {
			if p.Session == s.Name {
				mine = append(mine, p)
			}
		}
		res = append(res, t.toSession(s, mine))
	}
	return res, nil
}

func tmuxRank(p TmuxPane) int {
	r := 0
	if p.WindowActive {
		r += 2
	}
	if p.Active {
		r++
	}
	return r
}

func (t *Tmux) toSession(s TmuxSessionLine, panes []TmuxPane) backend.Session {
	harness, proj := SplitSessionName(s.Name)
	var live []TmuxPane
	for _, p := range panes {
		if !p.Dead {
			live = append(live, p)
		}
	}
	// The active pane of the active window first.
	sort.SliceStable(live, func(i, j int) bool { return tmuxRank(live[i]) > tmuxRank(live[j]) })
	for _, p := range live {
		if h := GuessHarness(p.Command, p.Title); h != "" {
			harness = h
			break
		}
	}
	cwd := s.Path
	if len(live) > 0 {
		cwd = live[0].Path
	}
	if proj == "" {
		proj = baseName(cwd)
	}
	detail := []string{plural(s.Windows, "window", "windows"), plural(len(live), "pane", "panes")}
	if s.Attached > 0 {
		detail = append(detail, "attached")
	}
	status := StatusUnknown
	if len(panes) > 0 && len(live) == 0 {
		status = StatusExited
	}
	updated := s.Activity
	if updated.IsZero() {
		updated = s.Created
	}
	return backend.Session{
		HostID:  t.hostID,
		Source:  backend.SourceTmux,
		Name:    s.Name,
		Harness: harness,
		Project: proj,
		Status:  status,
		Detail:  strings.Join(detail, " · "),
		Updated: updated,
	}
}

// Attach opens `tmux attach-session -t =S` in a PTY.
func (t *Tmux) Attach(ctx context.Context, name string, cols, rows int) (backend.PTY, error) {
	return t.sh.PTY(ctx, TmuxAttachCommand(name), cols, rows, nil)
}

// Launch starts the harness in a new session, or a new window of the
// existing session of that name.
func (t *Tmux) Launch(ctx context.Context, spec backend.LaunchSpec) (backend.Session, error) {
	lp, err := planLaunch(spec)
	if err != nil {
		return backend.Session{}, err
	}
	r, err := t.sh.Exec(ctx, TmuxLaunchCommand(lp.session, lp.cwd, lp.argv, lp.harness), nil)
	if err != nil {
		return backend.Session{}, err
	}
	if err := checkInstalled("tmux", r); err != nil {
		return backend.Session{}, err
	}
	if r.Exit != 0 {
		return backend.Session{}, failed(r, "tmux new-session failed: %s", strings.TrimSpace(r.Stderr))
	}
	return backend.Session{
		HostID:  t.hostID,
		Source:  backend.SourceTmux,
		Name:    lp.session,
		Harness: lp.harness,
		Project: project(lp.session, lp.cwd),
		Status:  StatusUnknown,
		Updated: t.now(),
	}, nil
}

// Kill ends the session; a missing session or server is not an error.
func (t *Tmux) Kill(ctx context.Context, name string) error {
	r, err := t.sh.Exec(ctx, "tmux kill-session -t "+tmuxTarget(name), nil)
	if err != nil {
		return err
	}
	if err := checkInstalled("tmux", r); err != nil {
		return err
	}
	if r.Exit != 0 && !TmuxNoServer(r) && !strings.Contains(r.Stderr, "can't find session") {
		return failed(r, "tmux kill-session failed: %s", strings.TrimSpace(r.Stderr))
	}
	return nil
}

var (
	_ Adapter = (*Tmux)(nil)
	_ Adapter = (*Zellij)(nil)
)
