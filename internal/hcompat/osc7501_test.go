package hcompat_test

import (
	"bytes"
	"testing"
	"time"

	"github.com/digiogithub/agentd/internal/testutil"
)

// A program that reports OSC 7501 is shown with its exact words (like
// herdr-core), and the daemon's own status follows it.
func TestProgramReportedStatus(t *testing.T) {
	sock := startDaemon(t)
	c := newHCClient(t, sock)
	c.helloFramed()
	sub := result(t, c.call("events.subscribe", map[string]any{}))
	evCh := uint32(sub["channel"].(float64))
	runSession(t, sock, "rep@proj", "sh", "-c",
		`stty -echo; r(){ printf '\033]7501;%s\033\\' "$1"; }; r state=working:app=claude-code:progress=30; sleep 0.5; r state=blocked:kind=permission:app=claude-code; r state=working:id=build; sleep 60`)
	got := c.readStream(evCh, func(b []byte) bool { return bytes.Contains(b, []byte(`"kind":"permission"`)) })
	if !bytes.Contains(got, []byte(`"progress":30`)) {
		t.Fatalf("working report missing: %s", got)
	}
	testutil.WaitFor(t, 10*time.Second, func() bool {
		r := result(t, c.call("terminal.get", map[string]any{"terminal": "rep@proj"}))
		st, _ := r["status"].(map[string]any)
		recs, _ := r["records"].(map[string]any)
		return st != nil && st["state"] == "blocked" && st["app"] == "claude-code" && recs["build"] != nil && r["reported"] == true
	}, "record shows the reported status", nil)
	checkResult(t, loadSchema(t), "terminal.get", result(t, c.call("terminal.get", map[string]any{"terminal": "rep@proj"})))
	if s := sessionInfo(t, sock, "rep@proj"); s.Status != "waiting" || s.ProgramStatus == nil || s.ProgramStatus.State != "blocked" {
		t.Fatalf("daemon status = %q program %+v", s.Status, s.ProgramStatus)
	}
}
