// Command agentd is a terminal session manager for CLI coding agents: it runs
// agents in persistent PTYs, lets you attach/detach, and lets agents exchange
// messages through a mailbox. See README.md.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/digiogithub/agentd/internal/attach"
	"github.com/digiogithub/agentd/internal/client"
	"github.com/digiogithub/agentd/internal/config"
	"github.com/digiogithub/agentd/internal/daemon"
	"github.com/digiogithub/agentd/internal/mcpserver"
	"github.com/digiogithub/agentd/internal/proto"
	"github.com/digiogithub/agentd/internal/version"
)

func main() {
	daemon.Version = version.Version
	if invokedAsHC() {
		os.Exit(hcMain(os.Args[1:], os.Stdin, os.Stdout, os.Stderr))
	}
	if err := newRootCmd().Execute(); err != nil {
		fmt.Fprintln(os.Stderr, "agentd:", err)
		os.Exit(1)
	}
}

func newRootCmd() *cobra.Command {
	root := &cobra.Command{
		Use:           "agentd",
		Short:         "Terminal session manager and mailbox for CLI coding agents",
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	root.AddCommand(
		daemonCmd(), runCmd(), lsCmd(), attachCmd(), dumpCmd(), viewCmd(), eventsCmd(), typeCmd(), actCmd(), killCmd(), rmCmd(),
		sendCmd(), inboxCmd(), readCmd(), replyCmd(), peersCmd(), aliasesCmd(), shutdownCmd(),
		versionCmd(), mcpCmd(), whoamiCmd(), purgeCmd(), serveCmd(), hcCmd(), turnsCmd(),
	)
	return root
}

// ---- helpers ----

// connect dials the daemon, starting it if needed.
func connect() (*client.Client, error) {
	return client.DialAuto(config.SocketPath(), client.AutoOptions{})
}

func termSize() (uint16, uint16) {
	for _, f := range []*os.File{os.Stdout, os.Stdin, os.Stderr} {
		if cols, rows, err := term.GetSize(int(f.Fd())); err == nil && cols > 0 && rows > 0 {
			return uint16(cols), uint16(rows)
		}
	}
	return 0, 0
}

func printJSON(w io.Writer, v any) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

func parseID(s string) (int64, error) {
	id, err := strconv.ParseInt(strings.TrimPrefix(s, "#"), 10, 64)
	if err != nil || id <= 0 {
		return 0, fmt.Errorf("invalid message id %q", s)
	}
	return id, nil
}

func ago(t time.Time) string {
	if t.IsZero() {
		return "-"
	}
	return humanDuration(time.Since(t))
}

func humanDuration(d time.Duration) string {
	switch {
	case d < time.Second:
		return "0s"
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd", int(d.Hours()/24))
	}
}

func oneLine(s string, max int) string {
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); len(r) > max {
		return string(r[:max-1]) + "…"
	}
	return s
}

// ---- commands ----

func daemonCmd() *cobra.Command {
	var level string
	cmd := &cobra.Command{
		Use:   "daemon",
		Short: "Run the daemon in the foreground (normally auto-started)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			var lv slog.Level
			if err := lv.UnmarshalText([]byte(level)); err != nil {
				return fmt.Errorf("invalid --log-level %q", level)
			}
			return daemon.Main(context.Background(), daemon.Options{LogLevel: lv, Stderr: true})
		},
	}
	cmd.Flags().StringVar(&level, "log-level", "info", "log level: debug, info, warn, error")
	return cmd
}

func runCmd() *cobra.Command {
	var harness, name, profile, size string
	var envs []string
	cmd := &cobra.Command{
		Use:   "run [alias|path] [--harness claude|codex|pando|<bin>] [--name NAME] [--profile P] [--size COLSxROWS] [-- cmd...]",
		Short: "Start an agent session in a project directory",
		Long: `Start a session. The target is a project alias (see 'agentd aliases') or a
directory (default "."), resolved by the daemon. The command defaults to the
harness binary; the name defaults to <harness>@<label> and the profile to the
harness name. The session gets the profile's fixed terminal size (160x50 by
default, profile cols/rows) unless --size is given; this terminal's size is
never sent (attaching resizes it, see restore_size_on_detach).`,
		RunE: func(cmd *cobra.Command, args []string) error {
			pos, argv := args, []string(nil)
			if dash := cmd.ArgsLenAtDash(); dash >= 0 {
				pos, argv = args[:dash], args[dash:]
			}
			if len(pos) > 1 {
				return fmt.Errorf("expected at most one alias or path before --, got %q", pos)
			}
			target := "."
			if len(pos) == 1 {
				target = pos[0]
			}
			cols, rows, err := parseSize(size)
			if err != nil {
				return err
			}
			cwd, err := os.Getwd()
			if err != nil {
				return err
			}
			env := map[string]string{}
			for _, kv := range envs {
				k, v, ok := strings.Cut(kv, "=")
				if !ok || k == "" {
					return fmt.Errorf("invalid --env %q (want KEY=VALUE)", kv)
				}
				env[k] = v
			}
			c, err := connect()
			if err != nil {
				return err
			}
			defer c.Close()
			// The daemon resolves target + harness (aliases, label, default
			// name/command/profile) so the CLI and the API share one path.
			info, err := c.Run(proto.RunArgs{
				Name: name, Target: target, Harness: harness, Cwd: cwd, Cmd: argv,
				EnvBase: os.Environ(), Env: env, Profile: profile, Cols: cols, Rows: rows,
			})
			if err != nil {
				return runHint(err)
			}
			fmt.Printf("started %s (pid %d, profile %s) in %s\n", info.Name, info.Pid, info.Profile, info.Cwd)
			return nil
		},
	}
	f := cmd.Flags()
	f.StringVar(&harness, "harness", "", "agent harness: claude, codex, pando or a binary (default claude, or the command after --)")
	f.StringVar(&name, "name", "", "session name (default <harness>@<label>)")
	f.StringVar(&profile, "profile", "", "harness profile for idle detection/injection (default: harness name)")
	f.StringVar(&size, "size", "", "terminal size COLSxROWS (default: the profile's cols/rows, 160x50)")
	f.StringArrayVarP(&envs, "env", "e", nil, "extra environment variable KEY=VALUE (repeatable)")
	return cmd
}

// runHint adds a restart hint when a daemon started before an upgrade rejects
// the target field of run (it predates daemon-side alias resolution).
func runHint(err error) error {
	if err != nil && strings.Contains(err.Error(), `unknown field "target"`) {
		return fmt.Errorf("%w\nhint: the running daemon is older than this agentd binary; "+
			"restart it with 'agentd shutdown' (this stops all sessions) and run again", err)
	}
	return err
}

// parseSize parses "COLSxROWS" ("" = no explicit size, 0x0).
func parseSize(s string) (cols, rows uint16, err error) {
	if s == "" {
		return 0, 0, nil
	}
	c, r, ok := strings.Cut(strings.ToLower(s), "x")
	cn, cerr := strconv.ParseUint(c, 10, 16)
	rn, rerr := strconv.ParseUint(r, 10, 16)
	if !ok || cerr != nil || rerr != nil || cn == 0 || rn == 0 {
		return 0, 0, fmt.Errorf("invalid --size %q (want COLSxROWS, e.g. 200x60)", s)
	}
	return uint16(cn), uint16(rn), nil
}

func lsCmd() *cobra.Command {
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "ls",
		Short: "List sessions",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, err := connect()
			if err != nil {
				return err
			}
			defer c.Close()
			ss, err := c.Ls()
			if err != nil {
				return err
			}
			if asJSON {
				return printJSON(os.Stdout, ss)
			}
			tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
			fmt.Fprintln(tw, "NAME\tSTATE\tPID\tIDLE\tUNREAD\tATTACHED\tPROFILE\tCWD")
			for _, s := range ss {
				state := s.State
				if s.ExitCode != nil {
					state = fmt.Sprintf("exited(%d)", *s.ExitCode)
				}
				fmt.Fprintf(tw, "%s\t%s\t%d\t%s\t%d\t%d\t%s\t%s\n", s.Name, state, s.Pid,
					humanDuration(time.Duration(s.IdleMS)*time.Millisecond), s.Unread, s.Attached, s.Profile, s.Cwd)
			}
			return tw.Flush()
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "JSON output")
	return cmd
}

func attachCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "attach NAME",
		Short: "Attach this terminal to a session (detach with Ctrl-] then d)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := connect()
			if err != nil {
				return err
			}
			cols, rows := termSize()
			conn, err := c.Attach(args[0], cols, rows)
			if err != nil {
				_ = c.Close()
				return err
			}
			return attach.Run(conn, args[0])
		},
	}
}

// dumpText formats a dump for stdout: empty rows at the bottom dropped and,
// when the harness showed ghost text (a suggested prompt or placeholder, never
// user input), a final note so a reading agent cannot mistake it for a draft.
func dumpText(res proto.DumpResult, ansi bool) string {
	// The screen has one line per row: drop the empty rows at the bottom.
	txt := strings.TrimRight(res.Text, " \n") + "\n"
	switch {
	case res.Ghost == "":
	case res.GhostHidden:
		txt += "[agentd: suggested prompt hidden: " + strconv.Quote(res.Ghost) + "]\n"
	case ansi: // shown (dimmed) on screen; --ghost asked for the raw screen only
		txt += "[agentd: suggested prompt shown, not user input: " + strconv.Quote(res.Ghost) + "]\n"
	}
	return txt
}

// cellsText formats a --cells dump, one line per cell.
func cellsText(d proto.CellsDump) string {
	var b strings.Builder
	fmt.Fprintf(&b, "cursor x=%d y=%d, row %d: %d cells\n", d.CursorX, d.CursorY, d.Row, len(d.Cells))
	orDefault := func(s string) string {
		if s == "" {
			return "default"
		}
		return s
	}
	for _, c := range d.Cells {
		attrs := strings.Join(c.Attrs, ",")
		if attrs == "" {
			attrs = "-"
		}
		fmt.Fprintf(&b, "x=%-3d %s %-4s fg=%-8s bg=%-8s attrs=%s w=%d\n",
			c.X, c.Rune, strconv.Quote(c.Content), orDefault(c.Fg), orDefault(c.Bg), attrs, c.Width)
	}
	return b.String()
}

// viewCmd prints the semantic view of a session (op view).
func eventsCmd() *cobra.Command {
	var (
		asJSON   bool
		sessions []string
		kinds    []string
		since    uint64
		epoch    string
	)
	cmd := &cobra.Command{
		Use:   "events",
		Short: "Stream daemon events (debugging aid for op subscribe)",
		Long: `Subscribe to the daemon event bus and print events until interrupted or the
daemon shuts down. --session and --kind (exact kind or prefix: session, status,
view, input, attention, mail) filter; --json prints one JSON object per line.
Humans see every session; an agent session only its own events and mail.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := connect()
			if err != nil {
				return err
			}
			sub, err := c.Subscribe(proto.SubscribeArgs{Sessions: sessions, Kinds: kinds, SinceSeq: since, Epoch: epoch}, 0)
			if err != nil {
				_ = c.Close()
				return err
			}
			defer sub.Close()
			go func() { // interrupt: close the stream, which ends the loop below
				ch := make(chan os.Signal, 1)
				signal.Notify(ch, os.Interrupt, syscall.SIGTERM)
				<-ch
				_ = sub.Close()
			}()
			for ev := range sub.Events {
				if asJSON {
					b, _ := json.Marshal(ev)
					if _, err := fmt.Fprintf(os.Stdout, "%s\n", b); err != nil {
						return err
					}
					continue
				}
				fmt.Fprintf(os.Stdout, "%s #%d %s %s %s\n", ev.Time.Local().Format("15:04:05.000"), ev.Seq, ev.Kind, ev.Session, ev.Data)
			}
			return sub.Err()
		},
	}
	f := cmd.Flags()
	f.BoolVar(&asJSON, "json", false, "one JSON event per line")
	f.StringSliceVar(&sessions, "session", nil, "only events of this session (repeatable)")
	f.StringSliceVar(&kinds, "kind", nil, "only this event kind or prefix (repeatable)")
	f.Uint64Var(&since, "since-seq", 0, "replay events after this seq when still buffered")
	f.StringVar(&epoch, "epoch", "", "daemon epoch the --since-seq belongs to")
	return cmd
}

func viewCmd() *cobra.Command {
	var (
		asJSON     bool
		since      uint64
		epoch      string
		lines      int
		scrollback bool
	)
	cmd := &cobra.Command{
		Use:   "view [NAME]",
		Short: "Print a session's semantic view (status, transcript, input, options)",
		Long: `Print the daemon's interpretation of a session's screen: status (starting,
working, idle, waiting, exited), the attention flag, the reflowed transcript
lines, the input line and the options of a menu or dialog on screen.

NAME defaults to $AGENTD_ID, or to the only session when there is just one.
--since N prints nothing (and exits 0) when the revision is still N.
Humans may view any session; an agent session only itself unless
[security] agent_view = true.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := connect()
			if err != nil {
				return err
			}
			defer c.Close()
			name := os.Getenv(daemon.EnvID)
			if len(args) == 1 {
				name = args[0]
			}
			if name == "" {
				ss, err := c.Ls()
				if err != nil {
					return err
				}
				if len(ss) != 1 {
					return fmt.Errorf("view: %d sessions; give a NAME", len(ss))
				}
				name = ss[0].Name
			}
			v, err := c.View(proto.ViewArgs{Name: name, SinceRevision: since, Epoch: epoch, LinesLimit: lines, Scrollback: scrollback})
			if err != nil {
				return err
			}
			if asJSON {
				if v.NotModified {
					return printJSON(os.Stdout, proto.ViewNotModified{NotModified: true, Epoch: v.Epoch, Revision: v.Revision})
				}
				return printJSON(os.Stdout, v)
			}
			if v.NotModified {
				return nil
			}
			_, err = io.WriteString(os.Stdout, viewText(v))
			return err
		},
	}
	f := cmd.Flags()
	f.BoolVar(&asJSON, "json", false, "JSON output")
	f.Uint64Var(&since, "since", 0, "print nothing when the view revision is still N (needs --epoch)")
	f.StringVar(&epoch, "epoch", "", "daemon epoch the --since revision belongs to (view/not_modified needs it)")
	f.IntVarP(&lines, "lines", "n", 0, "newest N transcript lines (default 500, max 10000)")
	f.BoolVar(&scrollback, "scrollback", false, "include the main-screen scrollback")
	return cmd
}

// viewText renders a View as text: transcript, then a status footer, the
// input line and the numbered options.
func viewText(v proto.View) string {
	var b strings.Builder
	for _, l := range v.Lines {
		b.WriteString(l.Text)
		b.WriteByte('\n')
	}
	status := v.Status
	if v.StatusReason != "" {
		status += " (" + v.StatusReason + ")"
	}
	if v.ExitCode != nil {
		status += fmt.Sprintf(" exit %d", *v.ExitCode)
	}
	attn := ""
	if v.Attention.Set {
		attn = " · attention: " + v.Attention.Reason
	}
	fmt.Fprintf(&b, "[agentd: %s%s · rev %d]\n", status, attn, v.Revision)
	if v.Input.Text != "" || v.Input.Ghost != "" || v.Input.State != "empty" {
		fmt.Fprintf(&b, "input (%s): %q", v.Input.State, v.Input.Text)
		if v.Input.Ghost != "" {
			fmt.Fprintf(&b, " ghost: %q", v.Input.Ghost)
		}
		b.WriteByte('\n')
	}
	if v.Dialog != nil && v.Dialog.Question != "" {
		fmt.Fprintf(&b, "dialog: %s\n", strings.ReplaceAll(v.Dialog.Question, "\n", " / "))
	}
	if len(v.Options) > 0 {
		b.WriteString("options:")
		for _, o := range v.Options {
			mark := ""
			if o.Selected {
				mark = "*"
			}
			fmt.Fprintf(&b, " [%d]%s %s ", o.Index, mark, o.Label)
		}
		b.WriteByte('\n')
	}
	return b.String()
}

func dumpCmd() *cobra.Command {
	var a proto.DumpArgs
	var row int
	cmd := &cobra.Command{
		Use:   "dump NAME",
		Short: "Print a session's current screen",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := connect()
			if err != nil {
				return err
			}
			defer c.Close()
			a.Name = args[0]
			if cmd.Flags().Changed("row") {
				a.Cells = true
				a.Row = &row
			}
			res, err := c.DumpResult(a)
			if err != nil {
				return err
			}
			if a.Cells {
				if res.Cells == nil {
					return errors.New("dump --cells: the daemon did not return cells (older daemon? restart it)")
				}
				_, err = io.WriteString(os.Stdout, cellsText(*res.Cells))
				return err
			}
			_, err = io.WriteString(os.Stdout, dumpText(res, a.ANSI))
			return err
		},
	}
	cmd.Flags().BoolVar(&a.Cells, "cells", false, "print the cursor position and every non-blank cell of one row (rune, colours, attributes)")
	cmd.Flags().IntVar(&row, "row", 0, "row for --cells, 0-based (default: the cursor's row; implies --cells)")
	cmd.Flags().BoolVar(&a.Scrollback, "scrollback", false, "include the scrollback")
	cmd.Flags().BoolVar(&a.ANSI, "ansi", false, "keep colors and styles as ANSI escapes (ghost text is not hidden)")
	cmd.Flags().BoolVar(&a.Ghost, "ghost", false, "show the raw screen, including the harness's ghost text (suggested prompt / placeholder)")
	return cmd
}

func typeCmd() *cobra.Command {
	var submit bool
	cmd := &cobra.Command{
		Use:   "type NAME [TEXT...]",
		Short: "Type raw text into a session (no mailbox)",
		Long: `Type raw text into a session (no mailbox). Humans may type into any session;
an agent session may only type into itself unless [security] agent_type = true.`,
		Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			text := strings.Join(args[1:], " ")
			if text == "" && !submit {
				return errors.New("nothing to type (give TEXT and/or --submit)")
			}
			c, err := connect()
			if err != nil {
				return err
			}
			defer c.Close()
			return c.Type(proto.TypeArgs{Name: args[0], Text: text, Submit: submit})
		},
	}
	cmd.Flags().BoolVar(&submit, "submit", false, "press the profile's submit key afterwards")
	return cmd
}

func killCmd() *cobra.Command {
	var signal string
	var rm bool
	cmd := &cobra.Command{
		Use:   "kill NAME",
		Short: "Signal a session (default TERM)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := connect()
			if err != nil {
				return err
			}
			defer c.Close()
			name := args[0]
			if err := c.Kill(name, signal); err != nil {
				return err
			}
			if !rm {
				return nil
			}
			deadline := time.Now().Add(5 * time.Second)
			for {
				err := c.Rm(name)
				if err == nil {
					return nil
				}
				if time.Now().After(deadline) {
					return fmt.Errorf("session did not exit in time: %w", err)
				}
				time.Sleep(100 * time.Millisecond)
			}
		},
	}
	cmd.Flags().StringVarP(&signal, "signal", "s", "TERM", "signal name or number")
	cmd.Flags().BoolVar(&rm, "rm", false, "remove the session once it has exited")
	return cmd
}

func rmCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "rm NAME...",
		Short: "Remove exited sessions",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := connect()
			if err != nil {
				return err
			}
			defer c.Close()
			for _, n := range args {
				if err := c.Rm(n); err != nil {
					return err
				}
			}
			return nil
		},
	}
}

func sendCmd() *cobra.Command {
	var subject string
	cmd := &cobra.Command{
		Use:   "send TO MESSAGE...",
		Short: `Send a message to a session or "human:<user>" ("-" reads the body from stdin)`,
		Long: `Send a message to a session or "human:<user>". The sender is not chosen by
the caller: the daemon identifies it (the session this process runs in, or
human:<user> outside sessions). See 'agentd whoami'.`,
		Args: cobra.MinimumNArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			body := strings.Join(args[1:], " ")
			if body == "-" {
				b, err := io.ReadAll(os.Stdin)
				if err != nil {
					return err
				}
				body = string(b)
			}
			c, err := connect()
			if err != nil {
				return err
			}
			defer c.Close()
			m, err := c.Send(proto.SendArgs{To: args[0], Subject: subject, Body: body})
			if err != nil {
				return err
			}
			fmt.Printf("queued message #%d to %s\n", m.ID, m.Recipient)
			return nil
		},
	}
	cmd.Flags().StringVar(&subject, "subject", "", "message subject")
	return cmd
}

func inboxCmd() *cobra.Command {
	var all, asJSON bool
	var as string
	cmd := &cobra.Command{
		Use:   "inbox",
		Short: "List your messages (your session's, or human:<user> outside sessions)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, err := connect()
			if err != nil {
				return err
			}
			defer c.Close()
			msgs, err := c.Inbox(as, all)
			if err != nil {
				return err
			}
			if asJSON {
				return printJSON(os.Stdout, msgs)
			}
			if len(msgs) == 0 {
				fmt.Println("no messages")
				return nil
			}
			tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
			fmt.Fprintln(tw, "ID\tFROM\tSTATE\tAGE\tMESSAGE")
			for _, m := range msgs {
				text := m.Subject
				if text == "" {
					text = m.Body
				}
				fmt.Fprintf(tw, "%d\t%s\t%s\t%s\t%s\n", m.ID, m.Sender, m.State, ago(m.CreatedAt), oneLine(text, 60))
			}
			return tw.Flush()
		},
	}
	cmd.Flags().BoolVar(&all, "all", false, "include read messages")
	cmd.Flags().StringVar(&as, "as", "", "humans only: peek at another mailbox (no state changes)")
	cmd.Flags().BoolVar(&asJSON, "json", false, "JSON output")
	return cmd
}

func readCmd() *cobra.Command {
	var asJSON bool
	var as string
	cmd := &cobra.Command{
		Use:   "read ID",
		Short: "Show a message and mark it read",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			id, err := parseID(args[0])
			if err != nil {
				return err
			}
			c, err := connect()
			if err != nil {
				return err
			}
			defer c.Close()
			m, err := c.Read(as, id)
			if err != nil {
				return err
			}
			if asJSON {
				return printJSON(os.Stdout, m)
			}
			fmt.Printf("#%d from %s to %s, %s\n", m.ID, m.Sender, m.Recipient, m.CreatedAt.Local().Format(time.DateTime))
			if m.ReplyTo != nil {
				fmt.Printf("In reply to: #%d\n", *m.ReplyTo)
			}
			if m.Subject != "" {
				fmt.Printf("Subject: %s\n", m.Subject)
			}
			fmt.Printf("\n%s\n", strings.TrimRight(m.Body, "\n"))
			return nil
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "JSON output")
	cmd.Flags().StringVar(&as, "as", "", "humans only: read from another mailbox (not marked read)")
	return cmd
}

func replyCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "reply ID TEXT...",
		Short: `Reply to a message ("-" reads the body from stdin)`,
		Args:  cobra.MinimumNArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			id, err := parseID(args[0])
			if err != nil {
				return err
			}
			body := strings.Join(args[1:], " ")
			if body == "-" {
				b, err := io.ReadAll(os.Stdin)
				if err != nil {
					return err
				}
				body = string(b)
			}
			c, err := connect()
			if err != nil {
				return err
			}
			defer c.Close()
			m, err := c.Reply(id, body)
			if err != nil {
				return err
			}
			fmt.Printf("queued reply #%d to %s\n", m.ID, m.Recipient)
			if m.Warning != "" {
				fmt.Fprintf(os.Stderr, "warning: %s\n", m.Warning)
			}
			return nil
		},
	}
	return cmd
}

func purgeCmd() *cobra.Command {
	return newPurgeCmd(func(a proto.PurgeArgs) (proto.PurgeResult, error) {
		c, err := connect()
		if err != nil {
			return proto.PurgeResult{}, err
		}
		defer c.Close()
		return c.Purge(a)
	})
}

// newPurgeCmd builds the purge command around do (the daemon call), so the
// flag handling can be tested without a daemon.
func newPurgeCmd(do func(proto.PurgeArgs) (proto.PurgeResult, error)) *cobra.Command {
	var a proto.PurgeArgs
	cmd := &cobra.Command{
		Use:   "purge [--older-than 48h|5d | --before TIME] [--recipient X] [--state read,replied] [--all] [--dry-run] [--vacuum]",
		Short: "Delete mailbox messages",
		Long: `Delete mailbox messages matching every given filter. Without a filter, --all
is required. Humans may purge any mailbox; inside a session only the session's
own read/replied messages can be purged. The daemon also deletes messages
older than [mailbox] retention (default 5d) on its own.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if a.OlderThan != "" {
				if _, err := config.ParseDuration(a.OlderThan); err != nil {
					return fmt.Errorf("--older-than: %w", err)
				}
			}
			if a.Before != "" {
				t, err := time.Parse(time.RFC3339, a.Before)
				if err != nil {
					return fmt.Errorf("--before %q: want an RFC 3339 time such as 2026-11-01T00:00:00Z", a.Before)
				}
				a.Before = t.Format(time.RFC3339Nano)
			}
			if a.Before != "" && a.OlderThan != "" {
				return errors.New("--before and --older-than are mutually exclusive")
			}
			if !a.All && a.Before == "" && a.OlderThan == "" && a.Recipient == "" && len(a.States) == 0 {
				return errors.New("refusing to purge without a filter: give --older-than, --before, --recipient or --state, or --all")
			}
			for i, st := range a.States {
				a.States[i] = strings.ToLower(strings.TrimSpace(st))
			}
			res, err := do(a)
			if err != nil {
				return err
			}
			noun := "messages"
			if res.Count == 1 {
				noun = "message"
			}
			if a.DryRun {
				fmt.Fprintf(cmd.OutOrStdout(), "would purge %d %s\n", res.Count, noun)
			} else {
				fmt.Fprintf(cmd.OutOrStdout(), "purged %d %s\n", res.Count, noun)
			}
			return nil
		},
	}
	f := cmd.Flags()
	f.StringVar(&a.OlderThan, "older-than", "", "only messages older than this (Go duration or days: 48h, 5d, 1d12h)")
	f.StringVar(&a.Before, "before", "", "only messages created before this RFC 3339 time")
	f.StringVar(&a.Recipient, "recipient", "", "only this mailbox (session name or human:<user>)")
	f.StringSliceVar(&a.States, "state", nil, "only these states, comma-separated: queued, delivered, read, replied")
	f.BoolVar(&a.All, "all", false, "allow purging without filters (everything you may purge)")
	f.BoolVar(&a.DryRun, "dry-run", false, "only count what would be purged")
	f.BoolVar(&a.Vacuum, "vacuum", false, "humans only: compact the database file afterwards")
	return cmd
}

func peersCmd() *cobra.Command {
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "peers",
		Short: "List sessions you can message",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, err := connect()
			if err != nil {
				return err
			}
			defer c.Close()
			ps, err := c.Peers()
			if err != nil {
				return err
			}
			if asJSON {
				return printJSON(os.Stdout, ps)
			}
			tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
			fmt.Fprintln(tw, "NAME\tSTATE\tPROFILE\tCWD")
			for _, p := range ps {
				fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", p.Name, p.State, p.Profile, p.Cwd)
			}
			return tw.Flush()
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "JSON output")
	return cmd
}

func aliasesCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "aliases",
		Short: "List project aliases and their source (file/env)",
		Long: `List the project aliases and where each comes from. Aliases are read here
from this process's environment (AGENTD_ALIASES, AGENTD_ALIASES_FILE,
XDG_CONFIG_HOME, HOME), which is exactly what 'agentd run' sends the daemon
(env_base) to resolve its target, so both always agree for the same caller.
The daemon's info op accepts the same env_base for other clients.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			a, err := config.LoadDefaultAliases()
			if err != nil {
				return err
			}
			list := a.List()
			if len(list) == 0 {
				p, _ := config.AliasesPath()
				fmt.Printf("no aliases (file %s, env %s)\n", p, config.EnvAliases)
				return nil
			}
			tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
			fmt.Fprintln(tw, "NAME\tDIR\tSOURCE")
			for _, al := range list {
				fmt.Fprintf(tw, "%s\t%s\t%s\n", al.Name, al.Dir, al.Source)
			}
			return tw.Flush()
		},
	}
}

func shutdownCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "shutdown",
		Short: "Stop the daemon (terminates all sessions)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, err := client.Dial(config.SocketPath())
			if err != nil {
				fmt.Println("daemon is not running")
				return nil
			}
			defer c.Close()
			return c.Shutdown()
		},
	}
}

func versionCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print the version",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			fmt.Printf("agentd %s\n", version.Version)
			if c, err := client.Dial(config.SocketPath()); err == nil {
				defer c.Close()
				if p, err := c.Ping(); err == nil {
					fmt.Printf("daemon %s (pid %d)\n", p.Version, p.Pid)
				}
			}
			return nil
		},
	}
}

func mcpCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "mcp",
		Short: "Run the MCP stdio server (agentd_peers/send/inbox/read/reply)",
		Long: `Run the MCP server on stdin/stdout. Register it in each agent's MCP config
(see 'agentd mcp install --print'). The daemon identifies the calling session
from the process tree (this process descends from the session's child), with
$AGENTD_TOKEN as a fallback, so no environment variable is required. Outside a
session only agentd_peers works.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
			defer stop()
			return mcpserver.Run(ctx, mcpserver.Options{
				ID:      os.Getenv(daemon.EnvID),
				Dial:    connect,
				Version: version.Version,
			})
		},
	}
	cmd.AddCommand(mcpInstallCmd())
	return cmd
}

func whoamiCmd() *cobra.Command {
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "whoami",
		Short: "Show your mailbox identity as the daemon resolves it",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, err := connect()
			if err != nil {
				return err
			}
			defer c.Close()
			w, err := c.Whoami()
			if err != nil {
				return err
			}
			if asJSON {
				return printJSON(os.Stdout, w)
			}
			fmt.Printf("%s (via %s)\n", w.Identity, w.Via)
			return nil
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "JSON output")
	return cmd
}

func mcpInstallCmd() *cobra.Command {
	var printFlag bool
	var harness string
	cmd := &cobra.Command{
		Use:   "install --print [--harness claude|codex|pando|all]",
		Short: "Print MCP config snippets for agent harnesses (never writes config files)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if !printFlag {
				return errors.New("only --print is supported: agentd never edits agent config files; " +
					"run 'agentd mcp install --print' and apply the snippet yourself")
			}
			exe, err := os.Executable()
			if err != nil {
				return fmt.Errorf("find agentd binary: %w", err)
			}
			if abs, err := filepath.EvalSymlinks(exe); err == nil {
				exe = abs
			}
			return printMCPSnippets(os.Stdout, exe, harness)
		},
	}
	cmd.Flags().BoolVar(&printFlag, "print", false, "print the snippets to stdout (required)")
	cmd.Flags().StringVar(&harness, "harness", "all", "claude, codex, pando or all")
	return cmd
}

// printMCPSnippets writes the MCP registration snippets for harness ("all"
// for every known one), using exe as the absolute path of agentd.
func printMCPSnippets(w io.Writer, exe, harness string) error {
	qexe, _ := json.Marshal(exe) // a JSON string is also a valid TOML basic string
	snippets := map[string]string{
		"claude": fmt.Sprintf(`# Claude Code: register for all projects (user scope)
claude mcp add --scope user agentd -- %[1]s mcp

# or as JSON (project .mcp.json, or "mcpServers" in ~/.claude.json):
{
  "mcpServers": {
    "agentd": { "type": "stdio", "command": %[2]s, "args": ["mcp"] }
  }
}
`, shellQuote(exe), qexe),
		"codex": fmt.Sprintf(`# Codex: ~/.codex/config.toml
[mcp_servers.agentd]
command = %[1]s
args = ["mcp"]
# The daemon identifies the session from the process tree, so a filtered MCP
# environment is fine. If you use a non-default socket, forward it, e.g.
# env_vars = ["AGENTD_SOCKET"]
`, qexe),
		"pando": fmt.Sprintf(`# Pando: ~/.pando.toml, $XDG_CONFIG_HOME/pando/.pando.toml or ./.pando.toml
[MCPServers.agentd]
Type = 'stdio'
Command = %[1]s
Args = ['mcp']
# Keep agentd outside the host sandbox (it must reach the agentd socket): if
# sandbox.extendTo includes "mcp", also set NoSandbox = true.

# or in .pando.json:
{
  "mcpServers": {
    "agentd": { "type": "stdio", "command": %[1]s, "args": ["mcp"] }
  }
}
`, qexe),
	}
	order := []string{"claude", "codex", "pando"}
	switch harness {
	case "all", "":
	case "claude", "codex", "pando":
		order = []string{harness}
	default:
		return fmt.Errorf("unknown --harness %q (want claude, codex, pando or all)", harness)
	}
	for i, h := range order {
		if i > 0 {
			fmt.Fprintln(w)
		}
		if _, err := io.WriteString(w, snippets[h]); err != nil {
			return err
		}
	}
	return nil
}

// shellQuote quotes s for a POSIX shell when it contains special characters.
func shellQuote(s string) string {
	safe := func(r rune) bool {
		return r == '/' || r == '.' || r == '-' || r == '_' || r == '@' || r == '+' || r == ':' ||
			(r >= '0' && r <= '9') || (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z')
	}
	if s != "" && strings.IndexFunc(s, func(r rune) bool { return !safe(r) }) < 0 {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
