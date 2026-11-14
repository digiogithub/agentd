package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/digiogithub/agentd/internal/client"
	"github.com/digiogithub/agentd/internal/config"
	"github.com/digiogithub/agentd/internal/hcompat"
	"github.com/digiogithub/agentd/internal/proto"
)

// hcNames are the program names Herdr clients look for. Invoked under one of
// them (a symlink made by `agentd hc install-shim`), agentd behaves as the
// herdr-core CLI subset those clients run.
var hcNames = []string{"herdr-core", "hc"}

func invokedAsHC() bool {
	base := filepath.Base(os.Args[0])
	for _, n := range hcNames {
		if base == n {
			return true
		}
	}
	return false
}

// hcCmd is `agentd hc ...`: the same herdr-core CLI subset, plus install-shim.
func hcCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "hc [connect|--version|server start|status|stop|machine ls --json|install-shim]",
		Short: "Herdr client compatibility (herdr-core protocol over stdio)",
		Long: "Speaks the herdr-core (hc) client protocol on top of agentd so Herdr clients can list and\n" +
			"attach to agentd sessions. `agentd hc install-shim` creates herdr-core/hc symlinks to point\n" +
			"a client at (Herdr Desktop: settings `core`). See docs/architecture/herdr-compatibility-analysis.md.",
		DisableFlagParsing: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) > 0 && args[0] == "install-shim" {
				return installShim(cmd.OutOrStdout(), args[1:])
			}
			if code := hcMain(args, os.Stdin, os.Stdout, os.Stderr); code != 0 {
				os.Exit(code)
			}
			return nil
		},
	}
	return cmd
}

// hcMain runs the herdr-core CLI subset and returns the exit code. Errors go
// to stderr as {"error":{message,hint}}, which Herdr Desktop shows when the
// hello reply does not come.
func hcMain(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	fail := func(msg, hint string) int {
		b, _ := json.Marshal(map[string]any{"error": map[string]string{"message": msg, "hint": hint}})
		fmt.Fprintln(stderr, string(b))
		return 2
	}
	// With AGENTD_HC_TRACE, record every CLI invocation too (argument
	// names only up to 6, to see what a client runs over SSH).
	if p := os.Getenv("AGENTD_HC_TRACE"); p != "" {
		if f, err := os.OpenFile(p, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600); err == nil {
			shown := args
			if len(shown) > 6 {
				shown = shown[:6]
			}
			fmt.Fprintf(f, "%s cli pid %d: %s %q\n", time.Now().Format("15:04:05.000"), os.Getpid(), filepath.Base(os.Args[0]), shown)
			_ = f.Close()
		}
	}
	if len(args) == 0 {
		return fail("agentd's herdr-core shim needs a command", "supported: connect, --version, server start|status|stop, machine ls --json")
	}
	switch args[0] {
	case "--version", "-V", "version":
		fmt.Fprintln(stdout, "hc "+hcompat.CompatVersion)
		return 0
	case "connect":
		// --session S selects a herdr-core session; agentd has one daemon per
		// socket (AGENTD_SOCKET), so it is accepted and ignored.
		if err := hcConnect(stdin, stdout); err != nil {
			return fail(err.Error(), "check `agentd ls`; the agentd daemon must be reachable")
		}
		return 0
	case "server":
		return hcServer(args[1:], stdout, fail)
	case "machine":
		if len(args) >= 2 && (args[1] == "ls" || args[1] == "list") {
			fmt.Fprintln(stdout, `{"machines":[]}`)
			return 0
		}
	case "update":
		return fail("agentd provides this core; it is updated with agentd", "update agentd instead")
	}
	return fail("unsupported herdr-core command: "+strings.Join(args, " "), "agentd implements part of the herdr-core CLI")
}

func hcConnect(stdin io.Reader, stdout io.Writer) error {
	sock := config.SocketPath()
	// Fail early (and start the daemon) before speaking the protocol.
	probe, err := client.DialAuto(sock, client.AutoOptions{})
	if err != nil {
		return err
	}
	_ = probe.Close()
	cfg, err := config.LoadDefault()
	if err != nil {
		return err
	}
	if !cfg.Herdr.On() {
		return fmt.Errorf("the herdr-core protocol is disabled ([herdr] enabled = false in %s)", cfg.Path)
	}
	id, err := hcCoreID()
	if err != nil {
		return err
	}
	host, _ := os.Hostname()
	srv := &hcompat.Server{
		Dial:     func() (*client.Client, error) { return client.Dial(sock) },
		Core:     hcompat.CoreInfo{ID: id, Name: host},
		EnvBase:  os.Environ(),
		ReadOnly: cfg.Herdr.ReadOnly,
		AllowRun: cfg.Herdr.RunAllowed,
	}
	// AGENTD_HC_TRACE=<file>: append a trace of the calls a client makes
	// (methods, ids, param names, outcomes; never values or screen bytes).
	if p := os.Getenv("AGENTD_HC_TRACE"); p != "" {
		f, err := os.OpenFile(p, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
		if err != nil {
			return err
		}
		defer f.Close()
		srv.Trace = f
		fmt.Fprintf(f, "--- hc connect pid %d\n", os.Getpid())
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGHUP)
	defer stop()
	return srv.Serve(ctx, stdin, stdout)
}

// hcCoreID is the permanent random core id (hello.core.id), kept in the
// state dir and shared with the daemon's hc listener.
func hcCoreID() (string, error) {
	p, err := config.HCCoreIDPath()
	if err != nil {
		return "", err
	}
	return hcompat.LoadCoreID(p)
}

// hcServer is `hc server start|status|stop|restart [--json]`. The "core" is
// the agentd daemon; start/stop act only on its hc socket listener
// ([herdr] listen), never on the daemon or its sessions. start exits 0 even
// when the listener stays off (disabled, or a real herdr-core owns the
// socket): Whip then re-discovers, fails to open the socket and falls back to
// `hc connect --session S`, which always works.
func hcServer(args []string, stdout io.Writer, fail func(msg, hint string) int) int {
	jsonOut := false
	action := ""
	for _, a := range args {
		switch {
		case a == "--json":
			jsonOut = true
		case action == "" && !strings.HasPrefix(a, "-"):
			action = a
		case a == "--fresh" && action == "restart":
		default:
			return fail("unsupported argument to hc server: "+a, "usage: hc server start|status|stop|restart [--json]")
		}
	}
	sock := config.SocketPath()
	out := func(v any, human string) int {
		if jsonOut {
			b, _ := json.Marshal(v)
			fmt.Fprintln(stdout, string(b))
		} else {
			fmt.Fprintln(stdout, human)
		}
		return 0
	}
	switch action {
	case "stop":
		c, err := client.Dial(sock)
		if err != nil {
			return out(map[string]any{"stopped": false}, "no core running")
		}
		defer c.Close()
		r, err := c.HCListener(proto.HCListenStop)
		if err != nil {
			return fail(err.Error(), "only the user (not an agentd session) may stop the hc listener")
		}
		return out(map[string]any{"stopped": r.WasListening},
			"hc listener stopped (the agentd daemon and its sessions keep running)")
	case "start", "status", "restart":
	default:
		return fail("unsupported herdr-core command: server "+action, "supported: server start|status|stop|restart [--json]")
	}
	var c *client.Client
	var err error
	if action == "status" {
		c, err = client.Dial(sock)
	} else {
		c, err = client.DialAuto(sock, client.AutoOptions{})
	}
	if err != nil {
		return fail("no core running: "+err.Error(), "start it with `hc server start` (starts the agentd daemon)")
	}
	defer c.Close()
	cfg, err := config.LoadDefault()
	if err != nil {
		return fail(err.Error(), "fix the agentd config")
	}
	if !cfg.Herdr.On() {
		return fail(fmt.Sprintf("the herdr-core protocol is disabled ([herdr] enabled = false in %s)", cfg.Path), "enable it in the agentd config")
	}
	var r proto.HCListenResult
	switch action {
	case "restart":
		if _, err = c.HCListener(proto.HCListenStop); err == nil {
			r, err = c.HCListener(proto.HCListenStart)
		}
	case "start":
		r, err = c.HCListener(proto.HCListenStart)
	default:
		r, err = c.HCListener(proto.HCListenStatus)
	}
	if err != nil {
		return fail(err.Error(), "only the user (not an agentd session) may manage the hc listener")
	}
	id, err := hcCoreID()
	if err != nil {
		return fail(err.Error(), "check the agentd state directory")
	}
	host, _ := os.Hostname()
	srv := &hcompat.Server{
		Dial:    func() (*client.Client, error) { return client.Dial(sock) },
		Core:    hcompat.CoreInfo{ID: id, Name: host},
		PID:     r.PID,
		Started: r.StartedAt,
	}
	st, err := srv.Status()
	if err != nil {
		return fail(err.Error(), "check `agentd ls`")
	}
	st["agentd"] = map[string]any{"listener": r}
	human := fmt.Sprintf("core %s (%s) running, pid %d (agentd daemon)", host, id[:min(8, len(id))], r.PID)
	if r.Listening {
		human += "; hc socket " + r.Socket
	} else {
		human += "; hc socket off: " + r.Reason + " (clients use `hc connect`)"
	}
	return out(st, human)
}

// installShim creates herdr-core and hc symlinks to this executable in a
// dedicated directory (and, with --local-bin, in ~/.local/bin, where Herdr's
// mobile app and remote cores look over SSH). It never replaces a file or a
// symlink to anything else, and warns about other herdr-core binaries or a
// running herdr-core: clients prefer those over agentd.
func installShim(w io.Writer, args []string) error {
	dir, localBin := "", false
	for i := 0; i < len(args); i++ {
		switch {
		case args[i] == "--dir" && i+1 < len(args):
			dir = args[i+1]
			i++
		case strings.HasPrefix(args[i], "--dir="):
			dir = strings.TrimPrefix(args[i], "--dir=")
		case args[i] == "--local-bin":
			localBin = true
		default:
			return fmt.Errorf("install-shim: unknown argument %q (usage: agentd hc install-shim [--dir DIR] [--local-bin])", args[i])
		}
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	if dir == "" {
		dir = filepath.Join(home, ".local", "share", "agentd", "hc")
	}
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	if exe, err = filepath.EvalSymlinks(exe); err != nil {
		return err
	}
	dirs := []string{dir}
	if localBin {
		dirs = append(dirs, filepath.Join(home, ".local", "bin"))
	}
	for _, d := range dirs {
		if err := linkShim(d, exe); err != nil {
			return err
		}
	}
	fmt.Fprintf(w, "herdr-core shim: %s\n", filepath.Join(dir, "herdr-core"))
	fmt.Fprintln(w, "Herdr Desktop: set \"core\" to that path in its settings.json.")
	if localBin {
		fmt.Fprintf(w, "Herdr mobile app and remote cores (SSH): %s\n", filepath.Join(home, ".local", "bin", "herdr-core"))
	} else {
		fmt.Fprintln(w, "Herdr mobile app over SSH: run again with --local-bin (links ~/.local/bin/herdr-core).")
	}
	for _, warn := range shimConflicts(exe, shimSearchDirs(home), herdrRuntimeDirs()) {
		fmt.Fprintln(w, "warning: "+warn)
	}
	return nil
}

// linkShim makes dir/herdr-core and dir/hc symlinks to exe; an existing link
// that already resolves to exe is kept, anything else is an error.
func linkShim(dir, exe string) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	for _, name := range hcNames {
		link := filepath.Join(dir, name)
		if _, err := os.Lstat(link); err == nil {
			if resolved, rerr := filepath.EvalSymlinks(link); rerr == nil && resolved == exe {
				continue
			}
			cur, _ := os.Readlink(link)
			if cur == "" {
				cur = "a file"
			}
			return fmt.Errorf("install-shim: %s exists (%s) and is not agentd; not replacing it", link, cur)
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
		if err := os.Symlink(exe, link); err != nil {
			return err
		}
	}
	return nil
}

// shimSearchDirs are the places Herdr clients look for herdr-core / hc
// (Desktop's core_bin.rs, herdr-core's remote dial, SSH's minimal PATH).
func shimSearchDirs(home string) []string {
	dirs := filepath.SplitList(os.Getenv("PATH"))
	dirs = append(dirs, filepath.Join(home, ".local", "bin"), "/usr/local/bin", "/usr/bin", "/bin",
		"/opt/homebrew/bin", filepath.Join(home, ".cargo", "bin"), filepath.Join(home, ".nix-profile", "bin"))
	return dirs
}

// herdrRuntimeDirs are where a running herdr-core keeps its socket.
func herdrRuntimeDirs() []string {
	var out []string
	if rt := os.Getenv("XDG_RUNTIME_DIR"); rt != "" {
		out = append(out, filepath.Join(rt, "herdr-core"))
	}
	return append(out, filepath.Join("/run/user", strconv.Itoa(os.Getuid()), "herdr-core"))
}

// shimConflicts lists herdr-core/hc binaries that are not agentd and live
// herdr-core sockets: Herdr clients pick those (the mobile app prefers a
// running core), so agentd's sessions would not show.
func shimConflicts(exe string, dirs, runtimeDirs []string) []string {
	var out []string
	seen := map[string]bool{}
	for _, d := range dirs {
		for _, name := range hcNames {
			p := filepath.Join(d, name)
			if d == "" || seen[p] {
				continue
			}
			seen[p] = true
			fi, err := os.Stat(p)
			if err != nil || fi.IsDir() {
				continue
			}
			if resolved, err := filepath.EvalSymlinks(p); err == nil && resolved == exe {
				continue
			}
			out = append(out, fmt.Sprintf("%s is another herdr-core (not agentd): Herdr clients may use it instead; remove it (check its package: dpkg -S %s)", p, p))
		}
	}
	for _, d := range runtimeDirs {
		if seen[d] {
			continue
		}
		seen[d] = true
		socks, _ := filepath.Glob(filepath.Join(d, "*.sock"))
		for _, sock := range socks {
			if strings.Contains(filepath.Base(sock), ".holder.") {
				continue
			}
			if live, ours := probeHCSocket(sock); !live || ours {
				continue // stale, or agentd's own hc listener ([herdr] listen)
			}
			out = append(out, fmt.Sprintf("a herdr-core is running (%s): the Herdr mobile app connects to it instead of agentd; stop it (herdr-core server stop) and remove that herdr-core", sock))
		}
	}
	return out
}

// probeHCSocket reports whether something answers on sock and whether it is
// agentd (server.status says installed_by "agentd").
func probeHCSocket(sock string) (live, ours bool) {
	c, err := net.DialTimeout("unix", sock, 300*time.Millisecond)
	if err != nil {
		return false, false
	}
	defer c.Close()
	_ = c.SetDeadline(time.Now().Add(time.Second))
	if _, err := io.WriteString(c, `{"jsonrpc":"2.0","id":1,"method":"server.status","params":{}}`+"\n"); err != nil {
		return true, false
	}
	line, _ := bufio.NewReader(c).ReadString('\n')
	return true, strings.Contains(line, `"installed_by":"agentd"`)
}
