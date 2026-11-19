package core_test

import (
	"context"
	"errors"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/digiogithub/agentd/internal/client"
	"github.com/digiogithub/agentd/internal/config"
	"github.com/digiogithub/agentd/internal/desktopui/backend"
	"github.com/digiogithub/agentd/internal/desktopui/remote"
	"github.com/digiogithub/agentd/internal/proto"
	"github.com/digiogithub/agentd/internal/relayserver"
	"github.com/digiogithub/agentd/internal/relaywire"
)

const joinToken = "0123456789abcdef-token"

// startRelayDaemon runs an in-process agentd-relay and a daemon linked to
// it (label "box"), and waits for the link.
func startRelayDaemon(t *testing.T) (sock string) {
	t.Helper()
	rs := relayserver.New(relayserver.Config{JoinTokens: []string{joinToken}})
	ts := httptest.NewServer(rs.Handler())
	t.Cleanup(ts.Close)
	cfg := config.Default()
	cfg.Relay = config.RelayConfig{Enabled: true, URL: ts.URL, Label: "box", JoinToken: joinToken, MaxScope: "admin"}
	sock = startDaemonCfg(t, cfg)
	c := dialDaemon(t, sock)
	waitFor(t, "relay link online", func() bool {
		st, err := c.Relay(proto.RelayArgs{Action: proto.RelayStatus})
		return err == nil && st.State == "online"
	})
	return sock
}

// invite creates an invite on the host and answers its approval prompt in
// the background once the device asks.
func invite(t *testing.T, sock string, args proto.RelayArgs, approve bool) string {
	t.Helper()
	c := dialDaemon(t, sock)
	args.Action = proto.RelayInvite
	res, err := c.Relay(args)
	if err != nil {
		t.Fatal(err)
	}
	link := res.Invite.Link
	inv, err := relaywire.ParseInvite(link)
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		c2, err := client.Dial(sock)
		if err != nil {
			return
		}
		defer func() { _ = c2.Close() }()
		if _, err := c2.Relay(proto.RelayArgs{Action: proto.RelayPending, Invite: inv.ID, WaitMS: 10000}); err != nil {
			return
		}
		_, _ = c2.Relay(proto.RelayArgs{Action: proto.RelayApprove, Invite: inv.ID, Accept: approve})
	}()
	return link
}

func TestRelayHostPairListActAndRevoke(t *testing.T) {
	dir := isolate(t)
	sock := startRelayDaemon(t)
	runCat(t, sock, "cat@proj", nil)
	b := newBackend(t, sshOpts(dir, sock))
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	link := invite(t, sock, proto.RelayArgs{Scope: "operate"}, true)
	h, err := b.PairRelay(ctx, link, "desk")
	if err != nil {
		t.Fatal(err)
	}
	if !h.IsRelay() || h.Name != "box" || h.ID != "box" || h.Relay.Scope != backend.ScopeOperate || h.Relay.DeviceID == "" ||
		h.Relay.Kind != "owner" || h.Addr != "" {
		t.Fatalf("paired host %+v %+v", h, h.Relay)
	}
	// The device key is in its own 0600 file; hosts.json never holds it.
	fi, err := os.Stat(filepath.Join(dir, remote.RelayKeyFile))
	if err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("relay key: %v %v", fi, err)
	}
	key, _ := remote.RelayKey(dir)
	hostsJSON, _ := os.ReadFile(filepath.Join(dir, remote.HostsFile))
	if strings.Contains(string(hostsJSON), relaywire.B64.EncodeToString(key)) || !strings.Contains(string(hostsJSON), `"relay_url"`) {
		t.Fatalf("hosts.json: %s", hostsJSON)
	}

	hosts := b.WatchHosts(ctx)
	sessions := b.WatchSessions(ctx)
	if err := b.Connect(ctx, h.ID); err != nil {
		t.Fatal(err)
	}
	waitHosts(t, hosts, "relay host online", func(hs []backend.HostStatus) bool {
		st, ok := hostStatus(hs, "box")
		return ok && st.State == backend.HostOnline && st.AgentdVer != ""
	})
	ss := waitSessions(t, sessions, "session through the relay", func(ss []backend.Session) bool {
		_, ok := find(ss, "box/agentd/cat@proj")
		return ok
	})
	s, _ := find(ss, "box/agentd/cat@proj")

	// View and act (operate) go through; kill (admin) is refused by the daemon.
	views, err := b.WatchView(ctx, s)
	if err != nil {
		t.Fatal(err)
	}
	if v := <-views; v == nil || v.Name != "cat@proj" {
		t.Fatalf("view %+v", v)
	}
	if res, err := b.Act(ctx, s, proto.ActArgs{Action: proto.ActKeys, Keys: []string{"h", "i", "enter"}}); err != nil || res.Result != "keys_sent" {
		t.Fatalf("act over the relay: %+v %v", res, err)
	}
	if err := b.Kill(ctx, s); client.ErrorCode(err) != proto.CodeForbidden {
		t.Fatalf("kill with operate: %v", err)
	}
	if _, err := b.Inbox(ctx, "box"); err != nil {
		t.Fatalf("owner inbox: %v", err)
	}
	if qs, err := b.Queues(ctx, "box"); err != nil || len(qs) != 0 {
		t.Fatalf("queues: %v %v", qs, err)
	}
	// Nothing that needs a shell.
	if _, err := b.ListDir(ctx, "box", "", false); !errors.Is(err, backend.ErrNeedsSSH) {
		t.Fatalf("list dir: %v", err)
	}
	if _, err := b.Detect(ctx, "box"); !errors.Is(err, backend.ErrNeedsSSH) {
		t.Fatalf("detect: %v", err)
	}
	if _, err := b.Launch(ctx, backend.LaunchSpec{HostID: "box", Where: backend.SourceAgentd, Project: "/tmp", TerminalOnly: true}); !errors.Is(err, backend.ErrNeedsSSH) {
		t.Fatalf("terminal-only launch: %v", err)
	}
	if _, err := b.Launch(ctx, backend.LaunchSpec{HostID: "box", Where: backend.SourceAgentd, Project: "/tmp", Harness: "claude"}); client.ErrorCode(err) != proto.CodeForbidden {
		t.Fatalf("launch with operate: %v", err)
	}

	// Revoking the device closes the tunnel; the host shows it as revoked.
	c := dialDaemon(t, sock)
	if _, err := c.Relay(proto.RelayArgs{Action: proto.RelayRevoke, Device: h.Relay.DeviceID}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "revoked on connect", func() bool {
		return errors.Is(b.Connect(ctx, "box"), backend.ErrDeviceRevoked)
	})
	waitHosts(t, hosts, "relay host revoked", func(hs []backend.HostStatus) bool {
		st, ok := hostStatus(hs, "box")
		return ok && st.State == backend.HostError && st.ErrCode == backend.HostErrDeviceRevoked
	})
}

func TestRelayAdminLaunchesAndTerminates(t *testing.T) {
	dir := isolate(t)
	sock := startRelayDaemon(t)
	b := newBackend(t, sshOpts(dir, sock))
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	h, err := b.PairRelay(ctx, invite(t, sock, proto.RelayArgs{Scope: "admin", NoApprove: true}, true), "")
	if err != nil || h.Relay.Scope != backend.ScopeAdmin {
		t.Fatalf("pair: %+v %v", h, err)
	}
	// "cat" is not a profile with a binary named after it here: use a
	// directory target and a harness whose binary exists (sh).
	s, err := b.Launch(ctx, backend.LaunchSpec{HostID: h.ID, Where: backend.SourceAgentd, Project: t.TempDir(), Harness: "sh"})
	if err != nil {
		t.Fatalf("launch over the relay: %v", err)
	}
	if s.HostID != h.ID || !strings.HasPrefix(s.Name, "sh@") {
		t.Fatalf("launched %+v", s)
	}
	if err := b.Terminate(ctx, s); err != nil {
		t.Fatalf("terminate over the relay: %v", err)
	}
	c := dialDaemon(t, sock)
	waitFor(t, "session removed", func() bool {
		ls, err := c.Ls()
		if err != nil {
			return false
		}
		for _, si := range ls {
			if si.Name == s.Name {
				return false
			}
		}
		return true
	})
}

func TestRelayPairDeniedAndBadLinks(t *testing.T) {
	dir := isolate(t)
	sock := startRelayDaemon(t)
	b := newBackend(t, sshOpts(dir, sock))
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	if _, err := b.PairRelay(ctx, invite(t, sock, proto.RelayArgs{Scope: "view"}, false), "desk"); !errors.Is(err, backend.ErrPairDenied) {
		t.Fatalf("denied: %v", err)
	}
	link := invite(t, sock, proto.RelayArgs{Scope: "view"}, true)
	bad := strings.Replace(link, "agentd://relay", "agentd://%zz", 1)
	_, err := b.PairRelay(ctx, bad, "desk")
	if !errors.Is(err, backend.ErrBadInvite) {
		t.Fatalf("bad link: %v", err)
	}
	inv, _ := relaywire.ParseInvite(link)
	if strings.Contains(err.Error(), relaywire.B64.EncodeToString(inv.Code)) {
		t.Fatalf("the error leaks the invite code: %v", err)
	}
	hosts, _ := remote.NewHostStore(dir).List()
	if len(hosts) != 1 {
		t.Fatalf("hosts saved after failed pairings: %+v", hosts)
	}
}

func TestRelayHostOffline(t *testing.T) {
	dir := isolate(t)
	rs := relayserver.New(relayserver.Config{JoinTokens: []string{joinToken}})
	ts := httptest.NewServer(rs.Handler())
	t.Cleanup(ts.Close)
	b := newBackend(t, sshOpts(dir, filepath.Join(t.TempDir(), "none.sock")))
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	hk, _ := relaywire.GenerateHostKey()
	h := backend.Host{ID: "gone", Name: "gone", Relay: &backend.RelayTarget{URL: ts.URL, Route: hk.Route(),
		HostKey: relaywire.B64.EncodeToString(hk.DH.Public), DeviceID: "dx", Scope: backend.ScopeView}}
	if err := b.SaveHost(ctx, h); err != nil {
		t.Fatal(err)
	}
	hosts := b.WatchHosts(ctx)
	if err := b.Connect(ctx, "gone"); !errors.Is(err, backend.ErrRelayHostOffline) {
		t.Fatalf("connect: %v", err)
	}
	waitHosts(t, hosts, "relay host offline", func(hs []backend.HostStatus) bool {
		st, ok := hostStatus(hs, "gone")
		return ok && st.State == backend.HostError && st.ErrCode == backend.HostErrRelayOffline
	})
	if err := b.TrustHostKey(ctx, "gone", "SHA256:x"); !errors.Is(err, backend.ErrNeedsSSH) {
		t.Fatalf("trust: %v", err)
	}
}
