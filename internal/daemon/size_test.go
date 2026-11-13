package daemon_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/digiogithub/agentd/internal/config"
	"github.com/digiogithub/agentd/internal/proto"
	"github.com/digiogithub/agentd/internal/testutil"
)

func sizeCfg(t *testing.T, body string) *config.Config {
	t.Helper()
	p := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(p)
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}

func waitSize(t *testing.T, c interface {
	Ls() ([]proto.SessionInfo, error)
}, name string, cols, rows uint16) {
	t.Helper()
	var last proto.SessionInfo
	testutil.WaitFor(t, timeout, func() bool {
		ss, _ := c.Ls()
		for _, s := range ss {
			if s.Name == name {
				last = s
			}
		}
		return last.Cols == cols && last.Rows == rows
	}, name+" size", func() string { return "" })
}

func TestRunUsesProfileSize(t *testing.T) {
	e := startServer(t, sizeCfg(t, "[profiles.codex]\ncols = 100\nrows = 30\n"), nil)
	c := e.dial(t)
	for _, tt := range []struct {
		args       proto.RunArgs
		cols, rows uint16
	}{
		{proto.RunArgs{Name: "d", Cmd: []string{"cat"}}, 160, 50},
		{proto.RunArgs{Name: "p", Cmd: []string{"cat"}, Profile: "codex"}, 100, 30},
		{proto.RunArgs{Name: "x", Cmd: []string{"cat"}, Cols: 200, Rows: 60}, 200, 60},
		{proto.RunArgs{Name: "h", Cmd: []string{"cat"}, Cols: 90}, 90, 50}, // partial: rows from the profile
	} {
		info, err := c.Run(tt.args)
		if err != nil {
			t.Fatal(err)
		}
		if info.Cols != tt.cols || info.Rows != tt.rows {
			t.Fatalf("%s started at %dx%d, want %dx%d", tt.args.Name, info.Cols, info.Rows, tt.cols, tt.rows)
		}
	}
	pi, _ := c.Info()
	for _, p := range pi.Profiles {
		want := [2]int{160, 50}
		if p.Name == "codex" {
			want = [2]int{100, 30}
		}
		if [2]int{p.Cols, p.Rows} != want {
			t.Fatalf("info profile %+v, want %v", p, want)
		}
	}
}

func TestRestoreSizeOnDetach(t *testing.T) {
	e := startServer(t, nil, nil)
	c := e.dial(t)
	if _, err := c.Run(proto.RunArgs{Name: "r", Cmd: []string{"cat"}}); err != nil {
		t.Fatal(err)
	}
	waitSize(t, c, "r", 160, 50)

	// Two attached clients: the most recently active one's size wins; the
	// profile size comes back only when the last one detaches.
	a1 := e.dial(t)
	conn1, err := a1.Attach("r", 100, 30)
	if err != nil {
		t.Fatal(err)
	}
	fr1 := newFrameReader(conn1)
	fr1.waitFor(t, "") // redraw arrived
	waitSize(t, c, "r", 100, 30)
	a2 := e.dial(t)
	conn2, err := a2.Attach("r", 80, 24)
	if err != nil {
		t.Fatal(err)
	}
	_ = newFrameReader(conn2)
	waitSize(t, c, "r", 80, 24)

	_ = proto.NewFrameWriter(conn2).Detach()
	_ = conn2.Close()
	// One client left (100x30): the session is not restored yet. It keeps the
	// last size set until that client acts or detaches.
	testutil.WaitFor(t, timeout, func() bool { return findSession(t, c, "r").Attached == 1 }, "one client left", nil)
	if s := findSession(t, c, "r"); s.Cols == 160 {
		t.Fatalf("restored too early: %dx%d", s.Cols, s.Rows)
	}
	_ = proto.NewFrameWriter(conn1).Detach()
	_ = conn1.Close()
	waitSize(t, c, "r", 160, 50)
}

func TestRestoreSizeDisabled(t *testing.T) {
	e := startServer(t, sizeCfg(t, "[run]\nrestore_size_on_detach = false\n"), nil)
	c := e.dial(t)
	if _, err := c.Run(proto.RunArgs{Name: "r", Cmd: []string{"cat"}}); err != nil {
		t.Fatal(err)
	}
	conn, err := e.dial(t).Attach("r", 100, 30)
	if err != nil {
		t.Fatal(err)
	}
	_ = newFrameReader(conn)
	waitSize(t, c, "r", 100, 30)
	_ = proto.NewFrameWriter(conn).Detach()
	_ = conn.Close()
	testutil.WaitFor(t, timeout, func() bool { return findSession(t, c, "r").Attached == 0 }, "detached", nil)
	if s := findSession(t, c, "r"); s.Cols != 100 || s.Rows != 30 {
		t.Fatalf("size changed after detach: %dx%d", s.Cols, s.Rows)
	}
}
