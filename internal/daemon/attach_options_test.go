package daemon_test

import (
	"strings"
	"testing"
	"time"

	"github.com/digiogithub/agentd/internal/proto"
	"github.com/digiogithub/agentd/internal/testutil"
)

func attachWith(t *testing.T, e *env, a proto.AttachArgs) (*frameReader, *proto.FrameWriter) {
	t.Helper()
	conn, err := e.dial(t).AttachWith(a)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return newFrameReader(conn), proto.NewFrameWriter(conn)
}

func screenOf(t *testing.T, e *env, name string) string {
	t.Helper()
	s, _ := e.dial(t).Dump(proto.DumpArgs{Name: name})
	return s
}

func TestAttachObserveNeverTypesOrResizes(t *testing.T) {
	e := startServer(t, nil, nil)
	c := e.dial(t)
	if _, err := c.Run(proto.RunArgs{Name: "o", Cmd: []string{"cat"}}); err != nil {
		t.Fatal(err)
	}
	waitSize(t, c, "o", 160, 50)
	fr, w := attachWith(t, e, proto.AttachArgs{Name: "o", Cols: 90, Rows: 20, Observe: true})
	fr.waitFor(t, "")
	_ = w.Resize(70, 15)
	_ = w.Write(proto.FrameData, []byte("observer-typed\r"))
	_ = w.Write(proto.FrameFocus, []byte{1})
	time.Sleep(300 * time.Millisecond)
	if s := findSession(t, c, "o"); s.Cols != 160 || s.Rows != 50 {
		t.Fatalf("observer resized the session: %dx%d", s.Cols, s.Rows)
	}
	if strings.Contains(screenOf(t, e, "o"), "observer-typed") {
		t.Fatal("observer input reached the child")
	}
	// The observer still sees what others type.
	_, w2 := attachWith(t, e, proto.AttachArgs{Name: "o", Cols: 100, Rows: 30})
	_ = w2.Data([]byte("typist\r"))
	fr.waitFor(t, "typist")
}

func TestAttachPassiveTakesSizeOnlyWhenActive(t *testing.T) {
	e := startServer(t, nil, nil)
	c := e.dial(t)
	if _, err := c.Run(proto.RunArgs{Name: "p", Cmd: []string{"cat"}}); err != nil {
		t.Fatal(err)
	}
	waitSize(t, c, "p", 160, 50)
	fr, w := attachWith(t, e, proto.AttachArgs{Name: "p", Cols: 100, Rows: 30, Passive: true})
	fr.waitFor(t, "")
	time.Sleep(200 * time.Millisecond)
	if s := findSession(t, c, "p"); s.Cols != 160 {
		t.Fatalf("passive attach resized at once: %dx%d", s.Cols, s.Rows)
	}
	_ = w.Write(proto.FrameFocus, []byte{1})
	waitSize(t, c, "p", 100, 30)
}

func TestAttachAutomaticInputIsNotHuman(t *testing.T) {
	e := startServer(t, nil, nil)
	c := e.dial(t)
	if _, err := c.Run(proto.RunArgs{Name: "a", Cmd: []string{"cat"}}); err != nil {
		t.Fatal(err)
	}
	fr, w := attachWith(t, e, proto.AttachArgs{Name: "a", Cols: 80, Rows: 24})
	fr.waitFor(t, "")
	// Unsubmitted bytes from an automatic reply never count as a human draft.
	_ = w.Write(proto.FrameAuto, []byte("autoreply"))
	fr.waitFor(t, "autoreply") // the PTY echoes it: it reached the child
	time.Sleep(100 * time.Millisecond)
	if st := findSession(t, c, "a").InputState; st == "draft" {
		t.Fatalf("automatic input counted as a human draft (input_state %q)", st)
	}
	_ = w.Data([]byte("typed"))
	testutil.WaitFor(t, timeout, func() bool { return findSession(t, c, "a").InputState == "draft" },
		"human bytes make a draft", nil)
}

func TestAttachFocusReportsOnlyWhenRequested(t *testing.T) {
	e := startServer(t, nil, nil)
	c := e.dial(t)
	// cat -v shows ESC as ^[ once the line is submitted.
	if _, err := c.Run(proto.RunArgs{Name: "f", Cmd: []string{"sh", "-c", `printf '\033[?1004h'; exec cat -v`}}); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Run(proto.RunArgs{Name: "nf", Cmd: []string{"cat", "-v"}}); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"f", "nf"} {
		fr, w := attachWith(t, e, proto.AttachArgs{Name: name, Cols: 80, Rows: 24})
		fr.waitFor(t, "")
		time.Sleep(200 * time.Millisecond) // printf has run
		_ = w.Write(proto.FrameFocus, []byte{0})
		_ = w.Write(proto.FrameFocus, []byte{1})
		_ = w.Data([]byte("end-" + name + "\r"))
		waitDump(t, c, name, "end-"+name)
	}
	if s := screenOf(t, e, "f"); !strings.Contains(s, "^[[O^[[I") {
		t.Fatalf("focus reports missing with 1004 on:\n%s", s)
	}
	if s := screenOf(t, e, "nf"); strings.Contains(s, "^[[") {
		t.Fatalf("focus reports sent without 1004:\n%s", s)
	}
}

func TestAttachHistoryPaintsScrollback(t *testing.T) {
	e := startServer(t, nil, nil)
	c := e.dial(t)
	if _, err := c.Run(proto.RunArgs{Name: "h", Cols: 80, Rows: 24,
		Cmd: []string{"sh", "-c", `i=1; while [ $i -le 300 ]; do echo hist-$i; i=$((i+1)); done; exec cat`}}); err != nil {
		t.Fatal(err)
	}
	waitDump(t, c, "h", "hist-300")
	fr, _ := attachWith(t, e, proto.AttachArgs{Name: "h", Cols: 80, Rows: 24, History: 100})
	fr.waitFor(t, "hist-300")
	d, _, _ := fr.snapshot()
	if !strings.Contains(d, "hist-250\x1b[0m\r\n") || strings.Contains(d, "hist-150\x1b") {
		t.Fatalf("history paint: has 250=%v has 150=%v", strings.Contains(d, "hist-250"), strings.Contains(d, "hist-150\x1b"))
	}
	plain, _ := attachWith(t, e, proto.AttachArgs{Name: "h", Cols: 80, Rows: 24})
	plain.waitFor(t, "hist-300")
	if d, _, _ := plain.snapshot(); strings.Contains(d, "hist-250") {
		t.Fatal("history painted without being asked")
	}
}
