package main

import (
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/BurntSushi/toml"

	"github.com/digiogithub/agentd/internal/proto"
)

func TestPrintMCPSnippets(t *testing.T) {
	const exe = "/home/u/bin/agentd"
	var all strings.Builder
	if err := printMCPSnippets(&all, exe, "all"); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"claude mcp add --scope user agentd -- /home/u/bin/agentd mcp",
		"[mcp_servers.agentd]",
		"[MCPServers.agentd]",
	} {
		if !strings.Contains(all.String(), want) {
			t.Errorf("snippets lack %q:\n%s", want, all.String())
		}
	}
	if err := printMCPSnippets(&strings.Builder{}, exe, "gemini"); err == nil {
		t.Error("unknown harness accepted")
	}

	// The TOML snippets parse and point at exe.
	for _, h := range []string{"codex", "pando"} {
		var b strings.Builder
		if err := printMCPSnippets(&b, exe, h); err != nil {
			t.Fatal(err)
		}
		tomlPart, _, _ := strings.Cut(b.String(), "# or in .pando.json")
		var v map[string]map[string]map[string]any
		if _, err := toml.Decode(tomlPart, &v); err != nil {
			t.Fatalf("%s snippet is not valid TOML: %v\n%s", h, err, tomlPart)
		}
		srv := v["mcp_servers"]["agentd"]
		cmdKey := "command"
		if h == "pando" {
			srv, cmdKey = v["MCPServers"]["agentd"], "Command"
		}
		if srv[cmdKey] != exe {
			t.Errorf("%s snippet command = %v", h, srv[cmdKey])
		}
	}

	// The JSON blocks parse.
	for _, h := range []string{"claude", "pando"} {
		var b strings.Builder
		if err := printMCPSnippets(&b, exe, h); err != nil {
			t.Fatal(err)
		}
		s := b.String()
		js := s[strings.Index(s, "{"):]
		var v struct {
			MCPServers map[string]struct {
				Command string   `json:"command"`
				Args    []string `json:"args"`
			} `json:"mcpServers"`
		}
		if err := json.Unmarshal([]byte(js), &v); err != nil {
			t.Fatalf("%s JSON snippet: %v\n%s", h, err, js)
		}
		if got := v.MCPServers["agentd"]; got.Command != exe || len(got.Args) != 1 || got.Args[0] != "mcp" {
			t.Errorf("%s JSON snippet %+v", h, got)
		}
	}

	if got := shellQuote("/a b/it's"); got != `'/a b/it'\''s'` {
		t.Errorf("shellQuote = %s", got)
	}
}

func TestDumpText(t *testing.T) {
	res := proto.DumpResult{Text: "> \n\n\n", Ghost: `Try "fix it"`, GhostHidden: true}
	want := ">\n[agentd: suggested prompt hidden: \"Try \\\"fix it\\\"\"]\n"
	if got := dumpText(res, false); got != want {
		t.Fatalf("hidden: %q, want %q", got, want)
	}
	res = proto.DumpResult{Text: "> Try\n\n", Ghost: "Try"}
	if got := dumpText(res, false); got != "> Try\n" {
		t.Fatalf("--ghost: %q", got)
	}
	if got := dumpText(res, true); !strings.HasSuffix(got, "[agentd: suggested prompt shown, not user input: \"Try\"]\n") {
		t.Fatalf("--ansi: %q", got)
	}
	if got := dumpText(proto.DumpResult{Text: "x\n"}, false); got != "x\n" {
		t.Fatalf("no ghost: %q", got)
	}
}

func TestCellsText(t *testing.T) {
	got := cellsText(proto.CellsDump{CursorX: 2, CursorY: 36, Row: 36, Cells: []proto.CellInfo{
		{X: 0, Content: "❯", Rune: "U+276F", Width: 1},
		{X: 2, Content: "¿", Rune: "U+00BF", Fg: "#999999", Attrs: []string{"faint"}, Width: 1},
	}})
	want := "cursor x=2 y=36, row 36: 2 cells\n" +
		"x=0   U+276F \"❯\"  fg=default  bg=default  attrs=- w=1\n" +
		"x=2   U+00BF \"¿\"  fg=#999999  bg=default  attrs=faint w=1\n"
	if got != want {
		t.Fatalf("got:\n%s\nwant:\n%s", got, want)
	}
}

func TestPurgeCmdFlags(t *testing.T) {
	run := func(args ...string) (proto.PurgeArgs, string, error) {
		var got proto.PurgeArgs
		called := false
		cmd := newPurgeCmd(func(a proto.PurgeArgs) (proto.PurgeResult, error) {
			got, called = a, true
			return proto.PurgeResult{Count: 3, DryRun: a.DryRun}, nil
		})
		var out strings.Builder
		cmd.SetOut(&out)
		cmd.SetErr(&out)
		cmd.SetArgs(args)
		err := cmd.Execute()
		if err == nil && !called {
			t.Fatalf("%v: daemon not called", args)
		}
		return got, out.String(), err
	}

	a, out, err := run("--older-than", "5d", "--state", "read,replied", "--recipient", "codex@models", "--dry-run")
	if err != nil {
		t.Fatal(err)
	}
	if a.OlderThan != "5d" || a.Recipient != "codex@models" || !a.DryRun || a.All || a.Vacuum ||
		len(a.States) != 2 || a.States[0] != "read" || a.States[1] != "replied" {
		t.Fatalf("args = %+v", a)
	}
	if out != "would purge 3 messages\n" {
		t.Fatalf("dry-run output %q", out)
	}

	a, out, err = run("--all", "--vacuum", "--state", "Read", "--state", "queued")
	if err != nil || !a.All || !a.Vacuum || len(a.States) != 2 || a.States[0] != "read" || a.States[1] != "queued" {
		t.Fatalf("args = %+v, %v", a, err)
	}
	if out != "purged 3 messages\n" {
		t.Fatalf("output %q", out)
	}

	a, _, err = run("--before", "2026-11-01T00:00:00+01:00")
	if err != nil || a.Before != "2026-11-01T00:00:00+01:00" {
		t.Fatalf("before = %q, %v", a.Before, err)
	}

	for _, bad := range [][]string{
		{},                     // no filter, no --all
		{"--dry-run"},          // still no filter
		{"--older-than", "5x"}, // bad duration
		{"--before", "yesterday"},
		{"--before", "2026-11-01T00:00:00Z", "--older-than", "1h"},
		{"extra-arg", "--all"},
	} {
		if _, _, err := run(bad...); err == nil {
			t.Errorf("%v accepted", bad)
		}
	}
}

func TestParseSize(t *testing.T) {
	for _, tt := range []struct {
		in         string
		cols, rows uint16
		bad        bool
	}{
		{"", 0, 0, false}, {"200x60", 200, 60, false}, {"80X24", 80, 24, false},
		{"0x10", 0, 0, true}, {"80", 0, 0, true}, {"axb", 0, 0, true}, {"70000x1", 0, 0, true},
	} {
		c, r, err := parseSize(tt.in)
		if (err != nil) != tt.bad || c != tt.cols || r != tt.rows {
			t.Errorf("parseSize(%q) = %d,%d,%v", tt.in, c, r, err)
		}
	}
}

func TestRunHint(t *testing.T) {
	old := errors.New(`invalid run args: json: unknown field "target"`)
	if got := runHint(old); !strings.Contains(got.Error(), "agentd shutdown") || !errors.Is(got, old) {
		t.Fatalf("hint missing: %v", got)
	}
	other := errors.New("boom")
	if runHint(other) != other || runHint(nil) != nil {
		t.Fatal("unrelated errors must pass through")
	}
}

func TestViewText(t *testing.T) {
	code := 0
	got := viewText(proto.View{
		Status: "waiting", StatusReason: "options", Revision: 9, ExitCode: &code,
		Attention: proto.ViewAttention{Set: true, Reason: "waiting"},
		Lines:     []proto.ViewLine{{Text: "hello", Role: "transcript"}},
		Input:     proto.ViewInput{State: "suggestion", Ghost: "try"},
		Dialog:    &proto.ViewDialog{Question: "Proceed?"},
		Options:   []proto.ViewOption{{Index: 1, Label: "Yes", Selected: true}, {Index: 2, Label: "No"}},
	})
	for _, want := range []string{"hello\n", "[agentd: waiting (options) exit 0 · attention: waiting · rev 9]", `ghost: "try"`, "dialog: Proceed?", "[1]* Yes", "[2] No"} {
		if !strings.Contains(got, want) {
			t.Errorf("viewText missing %q in:\n%s", want, got)
		}
	}
}

func TestActCmdArgs(t *testing.T) {
	var got proto.ActArgs
	cmd := newActCmd(func(a proto.ActArgs) (proto.ActResult, error) {
		got = a
		return proto.ActResult{Result: "keys_sent", Status: "idle", Revision: 3}, nil
	})
	run := func(args ...string) error {
		cmd.SetArgs(args)
		cmd.SetOut(io.Discard)
		return cmd.Execute()
	}
	if err := run("s1", "keys", "down", "enter"); err != nil || got.Name != "s1" || got.Action != "keys" || len(got.Keys) != 2 {
		t.Errorf("keys: %+v, %v", got, err)
	}
	if err := run("s1", "prompt", "hello", "there", "--no-submit"); err != nil || got.Text != "hello there" || got.Submit == nil || *got.Submit {
		t.Errorf("prompt: %+v, %v", got, err)
	}
	if err := run("s1", "choose", "o1234", "--label", "Yes", "--revision", "7", "--force"); err != nil ||
		got.OptionID != "o1234" || got.Label != "Yes" || got.Revision != 7 || !got.Force {
		t.Errorf("choose: %+v, %v", got, err)
	}
	for _, bad := range [][]string{{"s1", "choose"}, {"s1", "interrupt", "x"}, {"s1", "teleport"}, {"s1", "prompt"}} {
		if err := run(bad...); err == nil {
			t.Errorf("%v accepted", bad)
		}
	}
}
