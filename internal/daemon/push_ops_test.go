package daemon_test

import (
	"testing"

	"github.com/digiogithub/agentd/internal/proto"
)

func TestPushOpsOverTheSocket(t *testing.T) {
	e := startDaemon(t)
	c := e.dial(t)
	r, err := c.PushRegister(proto.PushRegisterArgs{Token: "TOKEN-1", Platform: "android", Name: "phone", HostRef: "ref"})
	if err != nil || r.ID == "" {
		t.Fatalf("register: %+v %v", r, err)
	}
	ls, err := c.PushLs()
	if err != nil || len(ls.Tokens) != 1 || ls.Tokens[0].ID != r.ID || ls.Enabled || ls.Reason == "" {
		t.Fatalf("ls: %+v %v", ls, err)
	}
	// A session is refused.
	mustRun(t, c, "ag", "cat")
	as := dialAs(t, e, "ag")
	if _, err := as.PushLs(); err == nil {
		t.Fatal("a session listed push tokens")
	}
	if _, err := as.PushRegister(proto.PushRegisterArgs{Token: "X", Platform: "ios"}); err == nil {
		t.Fatal("a session registered a push token")
	}
	// Push is off here: test is not_ready.
	if _, err := c.PushTest(""); err == nil {
		t.Fatal("test with push off succeeded")
	}
	u, err := c.PushUnregister(proto.PushUnregisterArgs{Token: "TOKEN-1"})
	if err != nil || u.Removed != 1 {
		t.Fatalf("unregister: %+v %v", u, err)
	}
}
