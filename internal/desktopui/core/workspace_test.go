package core_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/digiogithub/agentd/internal/desktopui/backend"
	"github.com/digiogithub/agentd/internal/desktopui/core"
)

func TestListDirLocalAndSSH(t *testing.T) {
	dir := isolate(t)
	b := newBackend(t, sshOpts(dir, "/nonexistent"))
	sshHost(t, b)
	root := t.TempDir()
	for _, d := range []string{"b dir", "A", "quo'te"} {
		if err := os.Mkdir(filepath.Join(root, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	ctx := context.Background()
	for _, host := range []string{backend.LocalHostID, "box"} {
		l, err := b.ListDir(ctx, host, root, false)
		if err != nil {
			t.Fatalf("%s: %v", host, err)
		}
		var names []string
		for _, e := range l.Entries {
			names = append(names, e.Name)
			if !e.Enterable {
				t.Errorf("%s: %q not enterable", host, e.Name)
			}
		}
		if l.Path != root || strings.Join(names, "|") != "A|b dir|quo'te" || l.Home == "" {
			t.Errorf("%s: %+v", host, l)
		}
		if _, err := b.ListDir(ctx, host, filepath.Join(root, "missing"), false); !errors.Is(err, backend.ErrDirNotFound) {
			t.Errorf("%s missing: %v", host, err)
		}
	}
	// No path = the home directory.
	l, err := b.ListDir(ctx, backend.LocalHostID, "", false)
	if err != nil || l.Path != l.Home || l.Home != os.Getenv("HOME") {
		t.Errorf("home listing: %+v %v", l, err)
	}
	if os.Getuid() != 0 {
		locked := filepath.Join(root, "A")
		_ = os.Chmod(locked, 0)
		defer os.Chmod(locked, 0o755)
		if _, err := b.ListDir(ctx, backend.LocalHostID, locked, false); !errors.Is(err, backend.ErrDirDenied) {
			t.Errorf("denied: %v", err)
		}
		l, _ := b.ListDir(ctx, backend.LocalHostID, root, false)
		for _, e := range l.Entries {
			if e.Name == "A" && e.Enterable {
				t.Errorf("locked dir reported enterable")
			}
		}
	}
}

func TestTerminalOnlyAgentdAndTerminate(t *testing.T) {
	dir := isolate(t)
	sock := startDaemon(t)
	b := newBackend(t, sshOpts(dir, sock))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sessions := b.WatchSessions(ctx)
	proj := filepath.Join(t.TempDir(), "my-proj")
	if err := os.Mkdir(proj, 0o755); err != nil {
		t.Fatal(err)
	}
	s, err := b.Launch(ctx, backend.LaunchSpec{HostID: "local", Where: backend.SourceAgentd, Project: proj, TerminalOnly: true, FirstPrompt: "ignored"})
	if err != nil {
		t.Fatal(err)
	}
	if s.Name != "shell@my-proj" || s.Harness != "shell" || s.Info == nil || s.Info.Cwd != proj ||
		len(s.Info.Cmd) != 3 || s.Info.Cmd[0] != "sh" || !strings.Contains(s.Info.Cmd[2], `exec "${SHELL:-/bin/sh}" -l`) {
		t.Fatalf("terminal session: %+v", s)
	}
	waitSessions(t, sessions, "shell listed", func(ss []backend.Session) bool { _, ok := find(ss, s.Key()); return ok })

	// A second terminal in the same project gets a numbered name.
	s2, err := b.Launch(ctx, backend.LaunchSpec{HostID: "local", Where: backend.SourceAgentd, Project: proj, TerminalOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	if s2.Name != "shell@my-proj-2" {
		t.Fatalf("second terminal %q", s2.Name)
	}

	// Running session: kill, wait for the exit, rm.
	for _, x := range []backend.Session{s, s2} {
		if err := b.Terminate(ctx, x); err != nil {
			t.Fatal(err)
		}
	}
	waitSessions(t, sessions, "shell removed", func(ss []backend.Session) bool {
		_, ok := find(ss, s.Key())
		_, ok2 := find(ss, s2.Key())
		return !ok && !ok2
	})
	c := dialDaemon(t, sock)
	if list, _ := c.Ls(); len(list) != 0 {
		t.Fatalf("daemon still lists %+v", list)
	}

	// Already exited: only rm. Already gone: not an error.
	info := runCat(t, sock, "cat@old", nil)
	if err := c.Kill("cat@old", "KILL"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "exit", func() bool {
		l, _ := c.Ls()
		return len(l) == 1 && l[0].State == "exited"
	})
	cs := backend.Session{HostID: "local", Source: backend.SourceAgentd, Name: info.Name, Status: "exited"}
	if err := b.Terminate(ctx, cs); err != nil {
		t.Fatal(err)
	}
	if list, _ := c.Ls(); len(list) != 0 {
		t.Fatalf("exited session not removed: %+v", list)
	}
	if err := b.Terminate(ctx, cs); err != nil {
		t.Fatalf("terminating a removed session: %v", err)
	}
}

func TestTerminateMuxAndShell(t *testing.T) {
	dir := isolate(t)
	z := &fakeMux{src: backend.SourceZellij}
	opts := sshOpts(dir, "/nonexistent")
	opts.Mux = func(sh backend.Shell, hostID string) []core.MuxAdapter { return []core.MuxAdapter{z} }
	b := newBackend(t, opts)
	ctx := context.Background()
	if err := b.Terminate(ctx, backend.Session{HostID: "local", Source: backend.SourceZellij, Name: "main"}); err != nil {
		t.Fatal(err)
	}
	if len(z.kills) != 1 || z.kills[0] != "main" {
		t.Fatalf("kills %v", z.kills)
	}
	s, err := b.Launch(ctx, backend.LaunchSpec{HostID: "local", Where: backend.SourceShell, Project: "~", TerminalOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	term, err := b.Terminal(ctx, s, 80, 24, false)
	if err != nil {
		t.Fatal(err)
	}
	// "~" expands: the shell starts in $HOME.
	if _, err := term.Write([]byte("pwd\r")); err != nil {
		t.Fatal(err)
	}
	readUntil(t, term, os.Getenv("HOME"))
	if err := b.Terminate(ctx, s); err != nil {
		t.Fatal(err)
	}
	if _, err := term.Wait(); err != nil {
		t.Fatal(err)
	}
	if err := b.Terminate(ctx, s); err != nil {
		t.Fatalf("closing a closed shell: %v", err)
	}
}
