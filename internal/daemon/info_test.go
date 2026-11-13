package daemon_test

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/digiogithub/agentd/internal/client"
	"github.com/digiogithub/agentd/internal/config"
	"github.com/digiogithub/agentd/internal/daemon"
	"github.com/digiogithub/agentd/internal/proto"
)

func TestPingEpochAndInfo(t *testing.T) {
	proj := t.TempDir()
	a, err := config.LoadAliases("", "webapp="+proj)
	if err != nil {
		t.Fatal(err)
	}
	e := startServer(t, nil, func(s *daemon.Server) { s.Aliases = a })
	c := e.dial(t)
	p, err := c.Ping()
	if err != nil || p.Epoch == "" || p.Epoch != e.srv.Epoch() {
		t.Fatalf("ping = %+v %v", p, err)
	}
	info, err := c.Info()
	if err != nil {
		t.Fatal(err)
	}
	host, _ := os.Hostname()
	if info.Host != host || info.Epoch != p.Epoch || info.Pid != p.Pid || info.Version != p.Version {
		t.Fatalf("info = %+v", info)
	}
	if !slices.Equal(info.Harnesses, []string{"claude", "codex", "pando"}) {
		t.Fatalf("harnesses = %v", info.Harnesses)
	}
	if len(info.Profiles) != 4 || info.Profiles[2].Name != "default" || !info.Profiles[2].Builtin {
		t.Fatalf("profiles = %+v", info.Profiles)
	}
	if len(info.Aliases) != 1 || info.Aliases[0] != (proto.AliasInfo{Name: "webapp", Dir: proj, Source: "env"}) {
		t.Fatalf("aliases = %+v", info.Aliases)
	}
	// Two daemon starts have different epochs.
	e2 := startServer(t, nil, nil)
	if p2, _ := e2.dial(t).Ping(); p2.Epoch == p.Epoch {
		t.Fatal("epoch must differ per start")
	}
}

func TestRunTargetHarness(t *testing.T) {
	root := t.TempDir()
	proj := filepath.Join(root, "proj")
	other := filepath.Join(root, "other")
	for _, d := range []string{proj, other} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	a, _ := config.LoadAliases("", "webapp="+proj)
	e := startServer(t, nil, func(s *daemon.Server) { s.Aliases = a })
	c := e.dial(t)
	wait := func(name string) {
		waitDump(t, c, name, "") // session exists and answers
	}

	// Alias + explicit command whose binary is the harness (unknown profile "cat" falls back to default).
	info, err := c.Run(proto.RunArgs{Target: "webapp", Cmd: []string{"cat"}})
	if err != nil {
		t.Fatal(err)
	}
	if info.Name != "cat@webapp" || info.Cwd != proj || info.Profile != "default" || info.Cmd[0] != "cat" {
		t.Fatalf("alias run = %+v", info)
	}
	wait(info.Name)

	// Relative path resolved against the caller-provided cwd; harness
	// basename drives name/profile, the explicit cmd is kept.
	info, err = c.Run(proto.RunArgs{Target: "other", Cwd: root, Harness: "codex", Cmd: []string{"cat"}, Name: "mine"})
	if err != nil {
		t.Fatal(err)
	}
	if info.Name != "mine" || info.Cwd != other || info.Profile != "codex" {
		t.Fatalf("relative run = %+v", info)
	}

	// Errors are coded invalid.
	for _, ra := range []proto.RunArgs{
		{Target: "other", Cmd: []string{"cat"}}, // relative without cwd
		{Target: "nonexistent-xyz", Cwd: root, Cmd: []string{"cat"}},
		{Target: "webapp", Cwd: "rel", Cmd: []string{"cat"}},
	} {
		if _, err := c.Run(ra); !client.IsCode(err, proto.CodeInvalid) {
			t.Fatalf("run %+v: err = %v (code %q), want invalid", ra, err, client.ErrorCode(err))
		}
	}
	// Same derived name twice -> exists.
	if _, err := c.Run(proto.RunArgs{Target: "webapp", Cmd: []string{"cat"}}); !client.IsCode(err, proto.CodeExists) {
		t.Fatalf("duplicate = %v", err)
	}
	// Without target, Harness only supplies default cmd/profile.
	info, err = c.Run(proto.RunArgs{Name: "plain", Cwd: root, Harness: "cat"})
	if err != nil || info.Profile != "default" || strings.Join(info.Cmd, " ") != "cat" || info.Cwd != root {
		t.Fatalf("harness-only run = %+v %v", info, err)
	}
}

func TestAliasesReload(t *testing.T) {
	d1, d2 := t.TempDir(), t.TempDir()
	cur := d1
	e := startServer(t, nil, func(s *daemon.Server) {
		s.AliasesLoader = func(func(string) string) (*config.Aliases, error) { return config.LoadAliases("", "p="+cur) }
	})
	c := e.dial(t)
	info, err := c.Run(proto.RunArgs{Name: "r1", Target: "p", Cmd: []string{"cat"}})
	if err != nil || info.Cwd != d1 {
		t.Fatalf("first = %+v %v", info, err)
	}
	cur = d2
	info, err = c.Run(proto.RunArgs{Name: "r2", Target: "p", Cmd: []string{"cat"}})
	if err != nil || info.Cwd != d2 {
		t.Fatalf("after edit = %+v %v", info, err)
	}
	if got, _ := c.Info(); len(got.Aliases) != 1 || got.Aliases[0].Dir != d2 {
		t.Fatalf("info aliases = %+v", got.Aliases)
	}
}

// run and info resolve aliases from the caller's env_base (AGENTD_ALIASES,
// AGENTD_ALIASES_FILE, XDG_CONFIG_HOME, HOME), not the daemon's environment.
func TestAliasesFromEnvBase(t *testing.T) {
	dEnv, dFile, dXDG, dHome := t.TempDir(), t.TempDir(), t.TempDir(), t.TempDir()
	if err := os.MkdirAll(filepath.Join(dHome, "proj"), 0o755); err != nil {
		t.Fatal(err)
	}
	write := func(path, body string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	custom := filepath.Join(t.TempDir(), "my.toml")
	write(custom, fmt.Sprintf("viafile = %q\n", dFile))
	xdgBase := t.TempDir()
	write(filepath.Join(xdgBase, "agentd", "aliases.toml"), fmt.Sprintf("viaxdg = %q\n", dXDG))
	write(filepath.Join(dHome, ".config", "agentd", "aliases.toml"), "viahome = \"~/proj\"\n")

	e := startServer(t, nil, func(s *daemon.Server) { s.AliasesLoader = config.LoadAliasesEnv })
	c := e.dial(t)
	cases := []struct {
		alias string
		env   []string
		dir   string
	}{
		{"viaenv", []string{"AGENTD_ALIASES=viaenv=" + dEnv}, dEnv},
		{"viafile", []string{"AGENTD_ALIASES_FILE=" + custom}, dFile},
		{"viaxdg", []string{"XDG_CONFIG_HOME=" + xdgBase}, dXDG},
		{"viahome", []string{"HOME=" + dHome}, filepath.Join(dHome, "proj")},
	}
	for _, tc := range cases {
		info, err := c.Run(proto.RunArgs{Name: "r-" + tc.alias, Target: tc.alias, Cmd: []string{"cat"}, EnvBase: tc.env})
		if err != nil || info.Cwd != tc.dir {
			t.Fatalf("%s: run = %+v %v, want cwd %s", tc.alias, info, err, tc.dir)
		}
		got, err := c.InfoFor(tc.env)
		if err != nil {
			t.Fatal(err)
		}
		found := false
		for _, a := range got.Aliases {
			found = found || (a.Name == tc.alias && a.Dir == tc.dir)
		}
		if !found {
			t.Fatalf("%s: info aliases = %+v", tc.alias, got.Aliases)
		}
	}
	// Without env_base the daemon's own environment applies: none of them.
	if _, err := c.Run(proto.RunArgs{Name: "r-none", Target: "viaenv", Cwd: "/", Cmd: []string{"cat"}}); err == nil {
		t.Fatal("alias leaked from another caller's env")
	}
}
