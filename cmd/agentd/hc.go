package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/spf13/cobra"

	"github.com/digiogithub/agentd/internal/client"
	"github.com/digiogithub/agentd/internal/config"
	"github.com/digiogithub/agentd/internal/hcompat"
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
		Use:   "hc [connect|--version|machine ls --json|install-shim]",
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
	if len(args) == 0 {
		return fail("agentd's herdr-core shim needs a command", "supported: connect, --version, machine ls --json")
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
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGHUP)
	defer stop()
	return srv.Serve(ctx, stdin, stdout)
}

// hcCoreID is the permanent random core id (hello.core.id), kept in the
// state dir; it changes only when the state is wiped.
func hcCoreID() (string, error) {
	dir, err := config.StateDir()
	if err != nil {
		return "", err
	}
	p := filepath.Join(dir, "hc", "core-id")
	if b, err := os.ReadFile(p); err == nil && len(strings.TrimSpace(string(b))) > 0 {
		return strings.TrimSpace(string(b)), nil
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return "", err
	}
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	id := hex.EncodeToString(raw[:])
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, []byte(id+"\n"), 0o600); err != nil {
		return "", err
	}
	return id, os.Rename(tmp, p)
}

// installShim creates herdr-core and hc symlinks to this executable in a
// dedicated directory. It never replaces a file or a symlink to anything
// else (a real herdr-core install stays untouched).
func installShim(w io.Writer, args []string) error {
	dir := ""
	for i := 0; i < len(args); i++ {
		switch {
		case args[i] == "--dir" && i+1 < len(args):
			dir = args[i+1]
			i++
		case strings.HasPrefix(args[i], "--dir="):
			dir = strings.TrimPrefix(args[i], "--dir=")
		default:
			return fmt.Errorf("install-shim: unknown argument %q (usage: agentd hc install-shim [--dir DIR])", args[i])
		}
	}
	if dir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return err
		}
		dir = filepath.Join(home, ".local", "share", "agentd", "hc")
	}
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	if exe, err = filepath.EvalSymlinks(exe); err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	for _, name := range hcNames {
		link := filepath.Join(dir, name)
		if cur, err := os.Readlink(link); err == nil {
			if cur == exe {
				continue
			}
			return fmt.Errorf("install-shim: %s already points to %s; not replacing it", link, cur)
		} else if _, serr := os.Lstat(link); serr == nil {
			return fmt.Errorf("install-shim: %s exists and is not agentd's symlink; not replacing it", link)
		} else if !errors.Is(serr, os.ErrNotExist) {
			return serr
		}
		if err := os.Symlink(exe, link); err != nil {
			return err
		}
	}
	fmt.Fprintf(w, "herdr-core shim: %s\n", filepath.Join(dir, "herdr-core"))
	fmt.Fprintln(w, "Herdr Desktop: set \"core\" to that path in its settings.json.")
	return nil
}
