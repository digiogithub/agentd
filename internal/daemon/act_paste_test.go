package daemon_test

import (
	"encoding/json"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/digiogithub/agentd/internal/proto"
	"github.com/digiogithub/agentd/internal/testutil"
)

// pasteConfig adds a profile whose harness collapses long pastes into a
// Claude Code-like placeholder, and one with a human grace period.
const pasteConfig = `
[profiles.collapse]
idle_ms = 300
human_grace_ms = 0
prompt_marker = "❯"
ghost_right_of_cursor = true
paste_placeholder_regex = '\[Pasted text #\d+( \+\d+ lines)?\]'

[profiles.collapsebox]
idle_ms = 300
human_grace_ms = 0
prompt_marker = "❯"
prompt_regex = '(?m)^❯([ \x{a0}]|$)'
ghost_right_of_cursor = true
paste_placeholder_regex = '\[Pasted text #\d+( \+\d+ lines)?\]'

[profiles.grace]
idle_ms = 300
human_grace_ms = 60000
prompt_marker = "❯"
ghost_right_of_cursor = true
`

// longPrompt is longer than the fake's -collapse threshold and multi-line.
var longPrompt = "first line of a long prompt\n" + strings.Repeat("more words in the body of the prompt ", 8) + "\nlast line"

func busyReason(t *testing.T, err error) string {
	t.Helper()
	re := actCode(t, err, proto.CodeBusy)
	if re == nil || len(re.Data) == 0 {
		t.Fatalf("busy error without data: %v", err)
	}
	var b proto.ActBusy
	if err := json.Unmarshal(re.Data, &b); err != nil {
		t.Fatalf("busy data %s: %v", re.Data, err)
	}
	return b.Reason
}

// A harness that shows "[Pasted text #1 +2 lines]" instead of the text: with
// the profile's paste_placeholder_regex the paste is verified and submitted.
func TestActPromptPastePlaceholderVerifies(t *testing.T) {
	t.Run("line", func(t *testing.T) { testPastePlaceholder(t, "collapse") })
	t.Run("box", func(t *testing.T) { testPastePlaceholder(t, "collapsebox", "-box") })
}

func testPastePlaceholder(t *testing.T, profile string, args ...string) {
	e := startViewServer(t, pasteConfig)
	c := e.dial(t)
	runView(t, c, "ph", profile, append([]string{testutil.BuildFakeAgent(t), "-collapse", "100"}, args...)...)
	waitStatus(t, c, "ph", "idle")
	r, err := c.Act(proto.ActArgs{Name: "ph", Action: proto.ActPrompt, Text: longPrompt})
	if err != nil {
		t.Fatal(err)
	}
	if !r.Submitted {
		t.Fatalf("result = %+v", r)
	}
	waitDump(t, c, "ph", `GOT: first line of a long prompt\nmore words`)
	// A second long prompt gets "#2" and is verified as well.
	waitStatus(t, c, "ph", "idle")
	if _, err := c.Act(proto.ActArgs{Name: "ph", Action: proto.ActPrompt, Text: longPrompt + " again"}); err != nil {
		t.Fatal(err)
	}
	waitDump(t, c, "ph", `\nlast line again`)
}

// The bug as users saw it with Claude Code: without a placeholder rule the
// collapsed paste is never verified (verify_failed, no Enter), the
// placeholder stays in the input line, and every later prompt is busy with
// reason "input" until the line is cleared in a terminal.
func TestActPromptCollapsedPasteWithoutRule(t *testing.T) {
	e := startViewServer(t, pasteConfig)
	c := e.dial(t)
	runView(t, c, "np", "fakebox", testutil.BuildFakeAgent(t), "-collapse", "100", "-box")
	waitStatus(t, c, "np", "idle")
	_, err := c.Act(proto.ActArgs{Name: "np", Action: proto.ActPrompt, Text: longPrompt})
	actCode(t, err, proto.CodeVerifyFailed)
	waitDump(t, c, "np", "[Pasted text #1 +2 lines]")
	if d, _ := c.Dump(proto.DumpArgs{Name: "np"}); strings.Contains(d, "GOT:") {
		t.Fatalf("submitted an unverified paste:\n%s", d)
	}
	waitStatus(t, c, "np", "idle")
	_, err = c.Act(proto.ActArgs{Name: "np", Action: proto.ActPrompt, Text: "short"})
	if got := busyReason(t, err); got != proto.BusyInput {
		t.Fatalf("busy reason = %q, want %q", got, proto.BusyInput)
	}
}

// Focus reports, pointer motion and wheel events from an attached terminal
// (a desktop Terminal tab under Claude Code's any-event mouse tracking) are
// not keystrokes: they do not make acts busy. A real key still does.
func TestActAmbientAttachInputIsNotHuman(t *testing.T) {
	e := startViewServer(t, pasteConfig)
	c := e.dial(t)
	runView(t, c, "am", "grace", testutil.BuildFakeAgent(t))
	waitStatus(t, c, "am", "idle")
	conn, err := e.dial(t).Attach("am", 160, 50)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	go func() { _, _ = io.Copy(io.Discard, conn) }()
	for _, p := range []string{
		"\x1b[I", "\x1b[<35;10;5M\x1b[<35;11;5M", "\x1b[<64;10;5M", "\x1b[O",
		"\x1b[?997;1n", "\x1b[8;50;160t", // reports, never keys
	} {
		if err := proto.WriteFrame(conn, 'D', []byte(p)); err != nil {
			t.Fatal(err)
		}
	}
	time.Sleep(200 * time.Millisecond) // let the attach loop process the frames
	if _, err := c.Act(proto.ActArgs{Name: "am", Action: proto.ActPrompt, Text: "hello"}); err != nil {
		t.Fatalf("prompt after pointer/focus input: %v", err)
	}
	waitDump(t, c, "am", "GOT: hello")
	waitStatus(t, c, "am", "idle")

	// A cursor key is a real keystroke: busy (human grace), no draft.
	if err := proto.WriteFrame(conn, 'D', []byte("\x1b[A")); err != nil {
		t.Fatal(err)
	}
	testutil.WaitFor(t, actWait, func() bool {
		_, err := c.Act(proto.ActArgs{Name: "am", Action: proto.ActPrompt, Text: "again"})
		return proto.CodeOf(err) == proto.CodeBusy && busyReason(t, err) == proto.BusyHuman
	}, "a key should make the prompt busy (human)", func() string { return "" })
}
