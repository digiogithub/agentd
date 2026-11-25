package main

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"

	"github.com/digiogithub/agentd/internal/proto"
)

type fakePush struct {
	reg    proto.PushRegisterArgs
	unreg  proto.PushUnregisterArgs
	testID string
	test   proto.PushTestResult
	rm     int
	err    error
}

func (f *fakePush) PushRegister(a proto.PushRegisterArgs) (proto.PushRegisterResult, error) {
	f.reg = a
	return proto.PushRegisterResult{ID: "pabc"}, f.err
}
func (f *fakePush) PushUnregister(a proto.PushUnregisterArgs) (proto.PushUnregisterResult, error) {
	f.unreg = a
	return proto.PushUnregisterResult{Removed: f.rm}, f.err
}
func (f *fakePush) PushLs() (proto.PushLsResult, error) {
	return proto.PushLsResult{Enabled: true, ProjectID: "proj", Tokens: []proto.PushToken{
		{ID: "pabc", Platform: "android", Name: "phone", CreatedAt: time.Now(), LastError: "boom"}}}, f.err
}
func (f *fakePush) PushTest(id string) (proto.PushTestResult, error) {
	f.testID = id
	return f.test, f.err
}

func pushCmdWith(f *fakePush) *cobra.Command {
	return newPushCmd(func() (pushAPI, func(), error) { return f, func() {}, nil })
}

func TestPushLs(t *testing.T) {
	out, err := execCmd(t, pushCmdWith(&fakePush{}), "ls")
	if err != nil || !strings.Contains(out, "push: on (project proj)") || !strings.Contains(out, "pabc") || !strings.Contains(out, "boom") {
		t.Fatalf("%v %q", err, out)
	}
	if out, err := execCmd(t, pushCmdWith(&fakePush{}), "ls", "--json"); err != nil || !strings.Contains(out, `"project_id": "proj"`) {
		t.Fatalf("%v %q", err, out)
	}
}

func TestPushTestRmRegister(t *testing.T) {
	f := &fakePush{test: proto.PushTestResult{Sent: 2}}
	if out, err := execCmd(t, pushCmdWith(f), "test", "pabc"); err != nil || !strings.Contains(out, "sent 2") || f.testID != "pabc" {
		t.Fatalf("%v %q %q", err, out, f.testID)
	}
	f.test = proto.PushTestResult{Sent: 0, Errors: []string{"pabc: boom"}}
	if _, err := execCmd(t, pushCmdWith(f), "test"); err == nil || f.testID != "" {
		t.Fatalf("errors not reported: %v", err)
	}
	f.rm = 1
	if out, err := execCmd(t, pushCmdWith(f), "rm", "pabc"); err != nil || !strings.Contains(out, "removed") || f.unreg.ID != "pabc" {
		t.Fatalf("%v %q", err, out)
	}
	f.rm = 0
	if _, err := execCmd(t, pushCmdWith(f), "rm", "pnone"); err == nil {
		t.Fatal("missing token accepted")
	}
	if out, err := execCmd(t, pushCmdWith(f), "register", "--token", "T", "--platform", "android", "--name", "n"); err != nil ||
		!strings.Contains(out, "pabc") || f.reg.Token != "T" || f.reg.Platform != "android" {
		t.Fatalf("%v %q %+v", err, out, f.reg)
	}
	if _, err := execCmd(t, pushCmdWith(f), "register"); err == nil {
		t.Fatal("register without token accepted")
	}
	f.err = errors.New("daemon down")
	if _, err := execCmd(t, pushCmdWith(f), "ls"); err == nil {
		t.Fatal("error swallowed")
	}
}
