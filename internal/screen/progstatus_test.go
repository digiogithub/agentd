package screen

import (
	"encoding/base64"
	"testing"
)

func TestParseOSC7501(t *testing.T) {
	b64 := func(s string) string { return base64.StdEncoding.EncodeToString([]byte(s)) }
	st, ok := ParseOSC7501("7501;state=blocked:kind=permission:progress=40:app=claude-code:title=" + b64("Run tests?") + ":msg=" + b64("bash\x1b[31m"))
	if !ok || st.State != "blocked" || st.Kind != "permission" || st.Progress == nil || *st.Progress != 40 ||
		st.App != "claude-code" || st.Title != "Run tests?" || st.Msg != "bash[31m" {
		t.Fatalf("parsed = %+v %v", st, ok)
	}
	if st, ok := ParseOSC7501("state=idle:kind=question:progress=10"); !ok || st.Kind != "" || st.Progress != nil {
		t.Fatalf("kind/progress kept outside blocked/working: %+v", st)
	}
	for _, bad := range []string{"", "state=sleeping", "kind=question", "state"} {
		if _, ok := ParseOSC7501(bad); ok {
			t.Errorf("accepted %q", bad)
		}
	}
	if st, _ := ParseOSC7501("state=working:progress=300:bogus=1"); st.Progress != nil {
		t.Fatalf("out of range progress kept: %+v", st)
	}
}

func TestProgramStatusesApply(t *testing.T) {
	var p ProgramStatuses
	p.Apply(ProgramStatus{State: "working"})
	p.Apply(ProgramStatus{State: "working", ID: "build"})
	p.Apply(ProgramStatus{State: "done", ID: "build.step1"})
	p.Apply(ProgramStatus{State: "idle", ID: "other"})
	if p.Root == nil || p.Root.State != "working" || len(p.Records) != 3 || !p.Reported {
		t.Fatalf("after reports: %+v", p)
	}
	p.Apply(ProgramStatus{State: "clear", ID: "build"})
	if _, ok := p.Records["build.step1"]; ok || len(p.Records) != 1 {
		t.Fatalf("clear build: %+v", p.Records)
	}
	c := p.Copy()
	p.Apply(ProgramStatus{State: "clear"})
	if p.Root != nil || len(p.Records) != 0 || !p.Reported || c.Root == nil {
		t.Fatalf("clear all / copy: %+v %+v", p, c)
	}
}
