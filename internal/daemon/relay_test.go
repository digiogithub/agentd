package daemon_test

import (
	"context"
	"errors"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/digiogithub/agentd/internal/client"
	"github.com/digiogithub/agentd/internal/config"
	"github.com/digiogithub/agentd/internal/proto"
	"github.com/digiogithub/agentd/internal/relayclient"
	"github.com/digiogithub/agentd/internal/relayserver"
	"github.com/digiogithub/agentd/internal/relaywire"
)

// relayEnv runs an in-process relay and a daemon linked to it.
func relayEnv(t *testing.T) (*env, string) {
	t.Helper()
	rs := relayserver.New(relayserver.Config{JoinTokens: []string{"0123456789abcdef-token"}})
	ts := httptest.NewServer(rs.Handler())
	t.Cleanup(ts.Close)
	cfg := config.Default()
	cfg.Relay = config.RelayConfig{Enabled: true, URL: ts.URL, Label: "box", JoinToken: "0123456789abcdef-token", MaxScope: "admin"}
	e := startServer(t, cfg, nil)
	c := e.dial(t)
	deadline := time.Now().Add(5 * time.Second)
	for {
		st, err := c.Relay(proto.RelayArgs{Action: proto.RelayStatus})
		if err != nil {
			t.Fatal(err)
		}
		if st.State == "online" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("relay link never online: %+v", st)
		}
		time.Sleep(20 * time.Millisecond)
	}
	return e, ts.URL
}

// pairDevice invites and pairs a new device; approve answers the host prompt.
func pairDevice(t *testing.T, c *client.Client, args proto.RelayArgs, approve bool) (relayclient.Target, relaywire.Device) {
	t.Helper()
	args.Action = proto.RelayInvite
	res, err := c.Relay(args)
	if err != nil {
		t.Fatal(err)
	}
	inv, err := relaywire.ParseInvite(res.Invite.Link)
	if err != nil {
		t.Fatal(err)
	}
	dev, _ := relaywire.GenerateDHKey()
	type out struct {
		p   relayclient.Paired
		err error
	}
	ch := make(chan out, 1)
	go func() {
		p, err := relayclient.Pair(context.Background(), inv, dev, "pixel", "android")
		ch <- out{p, err}
	}()
	if !args.NoApprove {
		pend, err := c.Relay(proto.RelayArgs{Action: proto.RelayPending, Invite: inv.ID, WaitMS: 5000})
		if err != nil || pend.Invite.State != "request" || pend.Invite.Request.Name != "pixel" {
			t.Fatalf("pending: %+v %v", pend.Invite, err)
		}
		if _, err := c.Relay(proto.RelayArgs{Action: proto.RelayApprove, Invite: inv.ID, Accept: approve}); err != nil {
			t.Fatal(err)
		}
	}
	r := <-ch
	if !approve {
		var d *relayclient.ErrDenied
		if !errors.As(r.err, &d) {
			t.Fatalf("want denied, got %v", r.err)
		}
		return relayclient.Target{}, relaywire.Device{}
	}
	if r.err != nil {
		t.Fatal(r.err)
	}
	return relayclient.Target{RelayURL: inv.RelayURL, Route: inv.Route, HostKey: inv.HostKey, Device: dev, Name: "pixel"}, r.p.Device
}

func remoteClient(t *testing.T, d *relayclient.Dialer) *client.Client {
	t.Helper()
	conn, err := d.Dial(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	rc := client.NewConn(conn, nil)
	t.Cleanup(func() { _ = rc.Close() })
	return rc
}

func TestRelayPairConnectAndScopes(t *testing.T) {
	e, _ := relayEnv(t)
	c := e.dial(t)
	if _, err := c.Run(proto.RunArgs{Name: "codex@web", Cmd: []string{"cat"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Run(proto.RunArgs{Name: "claude@api", Cmd: []string{"cat"}}); err != nil {
		t.Fatal(err)
	}

	// A denied pairing leaves no device.
	pairDevice(t, c, proto.RelayArgs{Scope: "view"}, false)

	// A view device limited to codex@*.
	tg, dev := pairDevice(t, c, proto.RelayArgs{Scope: "view", Sessions: []string{"codex@*"}}, true)
	if dev.Scope != "view" || dev.ID != relaywire.DeviceID(tg.Device.Public) {
		t.Fatalf("device %+v", dev)
	}
	d := &relayclient.Dialer{Target: tg}
	defer func() { _ = d.Close() }()
	rc := remoteClient(t, d)

	who, err := rc.Whoami()
	if err != nil || who.Via != "relay" || who.Device == nil || who.Device.Scope != "view" {
		t.Fatalf("whoami %+v %v", who, err)
	}
	ls, err := rc.Ls()
	if err != nil {
		t.Fatal(err)
	}
	if len(ls) != 1 || ls[0].Name != "codex@web" {
		t.Fatalf("ls through the relay: %+v", ls)
	}
	if _, err := rc.Dump(proto.DumpArgs{Name: "claude@api"}); client.ErrorCode(err) != proto.CodeNotFound {
		t.Fatalf("dump outside the filter: %v", err)
	}
	if _, err := rc.Dump(proto.DumpArgs{Name: "codex@web"}); err != nil {
		t.Fatalf("dump allowed session: %v", err)
	}
	for name, call := range map[string]func() error{
		"act": func() error {
			_, err := rc.Act(proto.ActArgs{Name: "codex@web", Action: "prompt", Text: "hi"})
			return err
		},
		"type":     func() error { return rc.Type(proto.TypeArgs{Name: "codex@web", Text: "x"}) },
		"kill":     func() error { return rc.Kill("codex@web", "") },
		"shutdown": func() error { return rc.Call(proto.OpShutdown, nil, nil) },
		"relay":    func() error { _, err := rc.Relay(proto.RelayArgs{Action: "devices"}); return err },
		"run":      func() error { _, err := rc.Run(proto.RunArgs{Name: "codex@x", Cmd: []string{"sh"}}); return err },
	} {
		if err := call(); client.ErrorCode(err) != proto.CodeForbidden {
			t.Errorf("%s over the relay with view: %v", name, err)
		}
	}

	// The second stream on the same tunnel works too (one tunnel, many streams).
	rc2 := remoteClient(t, d)
	if _, err := rc2.Ls(); err != nil {
		t.Fatal(err)
	}

	// Revoke closes the tunnel and refuses the key.
	if _, err := c.Relay(proto.RelayArgs{Action: proto.RelayRevoke, Device: dev.ID}); err != nil {
		t.Fatal(err)
	}
	if _, err := rc.Ls(); err == nil {
		t.Fatal("stream still usable after revoke")
	}
	if _, err := relayclient.Connect(context.Background(), tg); err == nil {
		t.Fatal("revoked device reconnected")
	}
}

func TestRelayAdminRunAndAudit(t *testing.T) {
	e, _ := relayEnv(t)
	c := e.dial(t)
	tg, _ := pairDevice(t, c, proto.RelayArgs{Scope: "admin", NoApprove: true}, true)
	d := &relayclient.Dialer{Target: tg}
	defer func() { _ = d.Close() }()
	rc := remoteClient(t, d)
	if _, err := rc.Run(proto.RunArgs{Name: "cat1", Cmd: []string{"cat"}}); client.ErrorCode(err) != proto.CodeForbidden {
		t.Fatalf("run with cmd over the relay: %v", err)
	}
	dir, _ := config.RelayDir()
	b, err := os.ReadFile(filepath.Join(dir, "audit.log"))
	if err != nil || len(b) == 0 {
		t.Fatalf("audit log: %v", err)
	}
	st, err := os.Stat(filepath.Join(dir, "devices.json"))
	if err != nil || st.Mode().Perm() != 0o600 {
		t.Fatalf("devices.json: %v %v", st, err)
	}
	devs, err := c.Relay(proto.RelayArgs{Action: proto.RelayDevices})
	if err != nil || len(devs.Devices) != 1 || !devs.Devices[0].Online {
		t.Fatalf("devices: %+v %v", devs, err)
	}
}
