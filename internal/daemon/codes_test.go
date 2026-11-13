package daemon_test

import (
	"testing"

	"github.com/digiogithub/agentd/internal/client"
	"github.com/digiogithub/agentd/internal/proto"
	"github.com/digiogithub/agentd/internal/testutil"
)

func TestErrorCodes(t *testing.T) {
	e := startDaemon(t)
	c := e.dial(t)
	bin := testutil.BuildFakeAgent(t)
	cwd := t.TempDir()

	expect := func(err error, code, msg string) {
		t.Helper()
		if err == nil {
			t.Fatalf("want %s error, got nil", code)
		}
		if got := client.ErrorCode(err); got != code {
			t.Fatalf("code = %q, want %q (err %v)", got, code, err)
		}
		if err.Error() != msg {
			t.Fatalf("message = %q, want %q (wording must not change)", err.Error(), msg)
		}
	}

	_, err := c.Dump(proto.DumpArgs{Name: "nope"})
	expect(err, proto.CodeNotFound, `no such session "nope"`)
	expect(c.Rm("nope"), proto.CodeNotFound, `no such session "nope"`)
	if err := c.Call("bogus", nil, nil); client.ErrorCode(err) != proto.CodeInvalid {
		t.Fatalf("unknown op code = %q", client.ErrorCode(err))
	}
	if _, err := c.Run(proto.RunArgs{Name: "bad name", Cmd: []string{"cat"}}); client.ErrorCode(err) != proto.CodeInvalid {
		t.Fatalf("bad name code = %q (%v)", client.ErrorCode(err), err)
	}
	if err := c.Call(proto.OpKill, map[string]any{"nme": "x"}, nil); client.ErrorCode(err) != proto.CodeInvalid {
		t.Fatalf("bad args code = %q", client.ErrorCode(err))
	}

	if _, err := c.Run(proto.RunArgs{Name: "a", Cwd: cwd, Cmd: []string{bin}}); err != nil {
		t.Fatal(err)
	}
	_, err = c.Run(proto.RunArgs{Name: "a", Cwd: cwd, Cmd: []string{bin}})
	expect(err, proto.CodeExists, `session "a" already exists`)
	expect(c.Rm("a"), proto.CodeConflict, `session "a" is still running (kill it first)`)
	if !client.IsCode(c.Kill("a", "BOGUS"), proto.CodeInvalid) {
		t.Fatal("bad signal should be invalid")
	}
	// An uncoded old-style error has no code.
	if client.ErrorCode(nil) != "" {
		t.Fatal("nil error has a code")
	}
}

func TestPurgeAndWriteCodes(t *testing.T) {
	e := startDaemon(t)
	c := e.dial(t)
	for name, a := range map[string]proto.PurgeArgs{
		"no filter":  {},
		"bad before": {Before: "yesterday"},
		"both":       {Before: "2026-01-01T00:00:00Z", OlderThan: "1d"},
		"bad dur":    {OlderThan: "soon"},
		"bad state":  {States: []string{"bogus"}},
	} {
		if _, err := c.Purge(a); client.ErrorCode(err) != proto.CodeInvalid {
			t.Fatalf("%s: code = %q (%v), want invalid", name, client.ErrorCode(err), err)
		}
	}
}
