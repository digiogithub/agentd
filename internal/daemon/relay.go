package daemon

// Relay transport, daemon side (design docs/design/relay-transport.md, plan
// docs/plans/agentd/relay_plan.md): the outbound link to agentd-relay, the
// host key, paired devices, invites and the encrypted tunnels whose streams
// are served like socket connections with a preset device caller (the op gate
// is in relaygate.go).

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"slices"
	"sort"
	"sync"
	"time"

	"github.com/digiogithub/agentd/internal/config"
	"github.com/digiogithub/agentd/internal/proto"
	"github.com/digiogithub/agentd/internal/relaywire"
)

// Relay limits.
const (
	relayMaxInvites        = 3
	relayInviteTTL         = 10 * time.Minute
	relayInviteMaxTTL      = time.Hour
	relayGuestTTL          = 24 * time.Hour
	relayGuestMaxTTL       = 30 * 24 * time.Hour
	relayInviteFailures    = 5
	relayLockoutFailures   = 20
	relayLockoutWindow     = 10 * time.Minute
	relayTunnelsPerDevice  = 4
	relayConfirmTimeout    = 15 * time.Second
	relayLastSeenEvery     = time.Minute
	relayAuditMaxBytes     = 5 << 20
	relayPendingMaxWait    = 30 * time.Second
	relayDeviceKindOwner   = "owner"
	relayDeviceKindGuest   = "guest"
	relayInviteWaiting     = "waiting"
	relayInviteRequest     = "request"
	relayInvitePaired      = "paired"
	relayInviteDenied      = "denied"
	relayInviteExpired     = "expired"
	relayInviteCancelled   = "cancelled"
	relayStateOff          = "off"
	relayDevicesFileName   = "devices.json"
	relayHostKeyFileName   = "host_key.json"
	relayAuditFileName     = "audit.log"
	relayDefaultDeviceName = "device"
)

// relayDevice is a paired device. Scope, Sessions and Kind never change after
// pairing (a new right needs a new invite), so a tunnel may keep the pointer.
type relayDevice struct {
	ID        string     `json:"id"`
	Name      string     `json:"name"`
	Platform  string     `json:"platform,omitempty"`
	PubKey    []byte     `json:"pubkey"`
	Scope     string     `json:"scope"`
	Sessions  []string   `json:"sessions,omitempty"`
	Kind      string     `json:"kind"`
	PairedVia string     `json:"paired_via"`
	CreatedAt time.Time  `json:"created_at"`
	ExpiresAt *time.Time `json:"expires_at,omitempty"`
	LastSeen  *time.Time `json:"last_seen,omitempty"`
}

func (d *relayDevice) atLeast(scope string) bool {
	return config.RelayScopeRank(d.Scope) >= config.RelayScopeRank(scope)
}

// allows reports whether the device may see session name.
func (d *relayDevice) allows(name string) bool {
	if len(d.Sessions) == 0 {
		return true
	}
	return slices.ContainsFunc(d.Sessions, func(g string) bool {
		ok, _ := path.Match(g, name)
		return ok
	})
}

func (d *relayDevice) expired(now time.Time) bool {
	return d.ExpiresAt != nil && !now.Before(*d.ExpiresAt)
}

func (d *relayDevice) wire() *relaywire.Device {
	return &relaywire.Device{ID: d.ID, Name: d.Name, Scope: d.Scope, Sessions: d.Sessions, Kind: d.Kind}
}

func (d *relayDevice) info(online bool) proto.RelayDeviceInfo {
	return proto.RelayDeviceInfo{ID: d.ID, Name: d.Name, Platform: d.Platform, Scope: d.Scope, Sessions: d.Sessions,
		Kind: d.Kind, CreatedAt: d.CreatedAt, ExpiresAt: d.ExpiresAt, LastSeen: d.LastSeen, Online: online}
}

type relayInvite struct {
	id        string
	code      []byte
	name      string
	scope     string
	kind      string
	sessions  []string
	approve   bool
	deviceTTL time.Duration
	expiresAt time.Time
	state     string
	failures  int
	request   *proto.RelayRequest
	device    *relayDevice
	decision  chan bool     // approval answer (buffered 1)
	changed   chan struct{} // closed and replaced on every state change (long-poll)
}

func (inv *relayInvite) info(withLink string) proto.RelayInviteInfo {
	out := proto.RelayInviteInfo{ID: inv.id, Link: withLink, Scope: inv.scope, Sessions: inv.sessions, Kind: inv.kind,
		ExpiresAt: inv.expiresAt, State: inv.state, Request: inv.request}
	if inv.device != nil {
		di := inv.device.info(false)
		out.Device = &di
	}
	return out
}

// relayState is the daemon's relay side. Its zero value is usable; init loads
// the state directory on first use.
type relayState struct {
	mu       sync.Mutex
	loaded   bool
	loadErr  error
	dir      string
	key      relaywire.HostKey
	devices  map[string]*relayDevice
	invites  map[string]*relayInvite
	tunnels  map[string]map[*relaywire.Mux]struct{}
	failures []time.Time
	saved    time.Time

	state   string
	lastErr string
	since   time.Time
	cancel  context.CancelFunc
	done    chan struct{}
	url     string

	auditMu sync.Mutex
}

// relayInit loads (or creates) the host key and the device list.
func (s *Server) relayInit() error {
	r := &s.relay
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.loaded {
		return r.loadErr
	}
	r.loaded = true
	r.invites = map[string]*relayInvite{}
	r.tunnels = map[string]map[*relaywire.Mux]struct{}{}
	r.devices = map[string]*relayDevice{}
	r.state = relayStateOff
	r.loadErr = func() error {
		dir, err := config.RelayDir()
		if err != nil {
			return err
		}
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return err
		}
		_ = os.Chmod(dir, 0o700)
		r.dir = dir
		kp := filepath.Join(dir, relayHostKeyFileName)
		b, err := os.ReadFile(kp)
		switch {
		case errors.Is(err, os.ErrNotExist):
			k, err := relaywire.GenerateHostKey()
			if err != nil {
				return err
			}
			kb, _ := json.Marshal(k)
			if err := writeFile0600(kp, kb); err != nil {
				return err
			}
			r.key = k
		case err != nil:
			return err
		default:
			if err := json.Unmarshal(b, &r.key); err != nil {
				return fmt.Errorf("relay: %s: %w", kp, err)
			}
		}
		b, err = os.ReadFile(filepath.Join(dir, relayDevicesFileName))
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		if err != nil {
			return err
		}
		var f struct {
			Devices []*relayDevice `json:"devices"`
		}
		if err := json.Unmarshal(b, &f); err != nil {
			return fmt.Errorf("relay: devices.json: %w", err)
		}
		for _, d := range f.Devices {
			if d.ID != "" && d.ID == relaywire.DeviceID(d.PubKey) {
				r.devices[d.ID] = d
			}
		}
		return nil
	}()
	return r.loadErr
}

func writeFile0600(p string, b []byte) error {
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, p)
}

// saveDevicesLocked writes devices.json (r.mu held).
func (r *relayState) saveDevicesLocked() error {
	list := make([]*relayDevice, 0, len(r.devices))
	for _, d := range r.devices {
		list = append(list, d)
	}
	sort.Slice(list, func(i, j int) bool { return list[i].CreatedAt.Before(list[j].CreatedAt) })
	b, err := json.MarshalIndent(struct {
		Devices []*relayDevice `json:"devices"`
	}{list}, "", "  ")
	if err != nil {
		return err
	}
	r.saved = time.Now()
	return writeFile0600(filepath.Join(r.dir, relayDevicesFileName), b)
}

// relayAudit appends one JSON line to relay/audit.log (never prompt text or bodies).
func (s *Server) relayAudit(event string, fields map[string]any) {
	r := &s.relay
	r.mu.Lock()
	dir := r.dir
	r.mu.Unlock()
	if dir == "" {
		return
	}
	rec := map[string]any{"time": time.Now().UTC().Format(time.RFC3339), "event": event}
	for k, v := range fields {
		rec[k] = v
	}
	b, _ := json.Marshal(rec)
	b = append(b, '\n')
	r.auditMu.Lock()
	defer r.auditMu.Unlock()
	p := filepath.Join(dir, relayAuditFileName)
	if st, err := os.Stat(p); err == nil && st.Size()+int64(len(b)) > relayAuditMaxBytes {
		_ = os.Rename(p, p+".1")
	}
	f, err := os.OpenFile(p, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return
	}
	_, _ = f.Write(b)
	_ = f.Close()
}

// startRelay starts the host link when [relay] is enabled.
func (s *Server) startRelay() {
	rc := s.Cfg.Relay
	if !rc.Enabled || rc.URL == "" {
		return
	}
	if err := s.relayInit(); err != nil {
		s.Log.Error("relay: state", "err", err)
		return
	}
	if rc.JoinTokenEnv != "" && os.Getenv(rc.JoinTokenEnv) == "" && rc.JoinToken == "" {
		// Typical under systemd --user: the variable lives in a shell profile
		// the unit never reads, and the relay then refuses the host.
		s.Log.Error("relay: [relay] join_token_env names a variable that is not set in the daemon's environment; the relay will refuse this host if it asks for a join token",
			"var", rc.JoinTokenEnv)
	}
	r := &s.relay
	r.mu.Lock()
	if r.cancel != nil {
		r.mu.Unlock()
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	r.cancel, r.done, r.url = cancel, make(chan struct{}), rc.URL
	key := r.key
	r.mu.Unlock()
	go func() {
		defer close(r.done)
		relaywire.RunHostLink(ctx, relaywire.HostLinkConfig{
			URL:       rc.URL,
			Key:       key,
			JoinToken: rc.Token(nil),
			Serve:     s.serveRelayTunnel,
			OnState: func(state string, err error) {
				r.mu.Lock()
				if r.state != state {
					r.since = time.Now()
				}
				r.state, r.lastErr = state, ""
				if err != nil {
					r.lastErr = err.Error()
				}
				r.mu.Unlock()
				switch {
				case state == "online":
					s.Log.Info("relay: linked", "url", rc.URL, "route", key.Route())
				case state == "offline" && err != nil:
					s.Log.Warn("relay: link down", "err", err)
				}
			},
		})
	}()
}

// stopRelay ends the link and every tunnel.
func (s *Server) stopRelay() {
	r := &s.relay
	r.mu.Lock()
	cancel, done := r.cancel, r.done
	r.cancel = nil
	var muxes []*relaywire.Mux
	for _, set := range r.tunnels {
		for m := range set {
			muxes = append(muxes, m)
		}
	}
	for _, inv := range r.invites {
		if inv.state == relayInviteRequest {
			select {
			case inv.decision <- false:
			default:
			}
		}
	}
	r.mu.Unlock()
	if cancel == nil {
		return
	}
	cancel()
	for _, m := range muxes {
		_ = m.Close()
	}
	select {
	case <-done:
	case <-time.After(3 * time.Second):
	}
}

// serveRelayTunnel runs one client tunnel: handshake, then pairing or the
// stream loop.
func (s *Server) serveRelayTunnel(ctx context.Context, c relaywire.MsgConn) {
	r := &s.relay
	r.mu.Lock()
	key, label := r.key, s.Cfg.Relay.HostLabel()
	route := key.Route()
	r.mu.Unlock()

	hctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	var (
		dev *relayDevice
		inv *relayInvite
	)
	sec, clientPub, hello, err := relaywire.ServerHandshake(hctx, c, route, key.DH, nil, func(pub []byte, h relaywire.Hello) relaywire.Decision {
		if h.Mode == relaywire.ModePair {
			var d relaywire.Decision
			inv, d = s.relayDecidePair(h)
			return d
		}
		var d relaywire.Decision
		dev, d = s.relayDecideConnect(pub, label)
		return d
	})
	cancel()
	if err != nil {
		var re *relaywire.RemoteError
		if errors.As(err, &re) {
			s.Log.Info("relay: tunnel refused", "mode", hello.Mode, "device", relaywire.DeviceID(clientPub), "reason", re.Msg)
		}
		_ = c.Close()
		return
	}
	if hello.Mode == relaywire.ModePair {
		s.relayPair(ctx, sec, inv, clientPub, hello, label)
		_ = sec.Close()
		return
	}
	s.relayServeDevice(ctx, sec, dev)
}

func (s *Server) relayDecideConnect(pub []byte, label string) (*relayDevice, relaywire.Decision) {
	r := &s.relay
	r.mu.Lock()
	defer r.mu.Unlock()
	d := r.devices[relaywire.DeviceID(pub)]
	refuse := func(code, msg string) (*relayDevice, relaywire.Decision) {
		return nil, relaywire.Decision{Welcome: relaywire.Welcome{Code: code, Error: msg}}
	}
	switch {
	case d == nil || !bytes.Equal(d.PubKey, pub):
		return refuse(proto.CodeForbidden, "unknown device: pair it again with an invite")
	case d.expired(time.Now()):
		return refuse(proto.CodeForbidden, "this device's access has expired")
	case len(r.tunnels[d.ID]) >= relayTunnelsPerDevice:
		return refuse(proto.CodeBusy, "too many connections from this device")
	}
	return d, relaywire.Decision{Welcome: relaywire.Welcome{OK: true, Host: label, Device: d.wire()}}
}

func (r *relayState) lockedOut(now time.Time) bool {
	keep := r.failures[:0]
	for _, t := range r.failures {
		if now.Sub(t) < relayLockoutWindow {
			keep = append(keep, t)
		}
	}
	r.failures = keep
	return len(r.failures) >= relayLockoutFailures
}

func (s *Server) relayDecidePair(h relaywire.Hello) (*relayInvite, relaywire.Decision) {
	r := &s.relay
	r.mu.Lock()
	defer r.mu.Unlock()
	now := time.Now()
	refuse := func(msg string) (*relayInvite, relaywire.Decision) {
		r.failures = append(r.failures, now)
		return nil, relaywire.Decision{Welcome: relaywire.Welcome{Code: proto.CodeInvalid, Error: msg}}
	}
	if r.lockedOut(now) {
		return nil, relaywire.Decision{Welcome: relaywire.Welcome{Code: proto.CodeBusy, Error: "pairing locked after too many failures"}}
	}
	inv := r.invites[h.Invite]
	if inv == nil || inv.state != relayInviteWaiting || !now.Before(inv.expiresAt) {
		return refuse("unknown, used or expired invite")
	}
	return inv, relaywire.Decision{PSK: inv.code, Welcome: relaywire.Welcome{OK: true, Pending: true, Host: s.Cfg.Relay.HostLabel()}}
}

// setInviteState changes an invite's state and wakes long-polls (r.mu held).
func (inv *relayInvite) setState(st string) {
	inv.state = st
	close(inv.changed)
	inv.changed = make(chan struct{})
}

func (s *Server) relayPair(ctx context.Context, sec *relaywire.Secure, inv *relayInvite, pub []byte, h relaywire.Hello, label string) {
	r := &s.relay
	cctx, cancel := context.WithTimeout(ctx, relayConfirmTimeout)
	var m relaywire.PairMsg
	err := sec.RecvJSON(cctx, &m)
	cancel()
	if err != nil || m.T != "confirm" {
		// Wrong code: the confirm does not decrypt.
		r.mu.Lock()
		r.failures = append(r.failures, time.Now())
		inv.failures++
		if inv.failures >= relayInviteFailures && inv.state == relayInviteWaiting {
			inv.setState(relayInviteExpired)
		}
		r.mu.Unlock()
		s.Log.Info("relay: pairing attempt failed", "invite", inv.id)
		return
	}
	id := relaywire.DeviceID(pub)
	name := inv.name
	if name == "" {
		name = clip(h.Name, 64)
	}
	if name == "" {
		name = relayDefaultDeviceName + "-" + id[1:5]
	}
	deny := func(msg string) {
		_ = sec.SendJSON(ctx, relaywire.PairMsg{T: "denied", Error: msg})
	}

	r.mu.Lock()
	if inv.state != relayInviteWaiting || !time.Now().Before(inv.expiresAt) {
		r.mu.Unlock()
		deny("invite already used or expired")
		return
	}
	inv.request = &proto.RelayRequest{DeviceID: id, Name: name, Platform: clip(h.Platform, 32)}
	inv.setState(relayInviteRequest)
	approve, decision, expires := inv.approve, inv.decision, inv.expiresAt
	r.mu.Unlock()

	accepted := true
	if approve {
		t := time.NewTimer(time.Until(expires))
		select {
		case accepted = <-decision:
		case <-t.C:
			accepted = false
		case <-ctx.Done():
			accepted = false
		}
		t.Stop()
	}

	r.mu.Lock()
	if !accepted {
		if inv.state == relayInviteRequest {
			inv.setState(relayInviteDenied)
		}
		r.mu.Unlock()
		deny("the host did not approve this device")
		s.relayAudit("pair_denied", map[string]any{"invite": inv.id, "device": id, "name": name})
		return
	}
	if _, exists := r.devices[id]; !exists && len(r.devices) >= s.Cfg.Relay.Devices() {
		inv.setState(relayInviteDenied)
		r.mu.Unlock()
		deny("too many paired devices on this host")
		return
	}
	now := time.Now().UTC()
	d := &relayDevice{ID: id, Name: name, Platform: clip(h.Platform, 32), PubKey: append([]byte(nil), pub...),
		Scope: inv.scope, Sessions: inv.sessions, Kind: inv.kind, PairedVia: "invite", CreatedAt: now}
	if inv.deviceTTL > 0 {
		exp := now.Add(inv.deviceTTL)
		d.ExpiresAt = &exp
	}
	r.devices[id] = d
	err = r.saveDevicesLocked()
	inv.device = d
	inv.setState(relayInvitePaired)
	r.mu.Unlock()
	if err != nil {
		s.Log.Error("relay: save devices", "err", err)
	}
	s.Log.Info("relay: device paired", "device", id, "name", name, "scope", d.Scope, "kind", d.Kind)
	s.relayAudit("pair", map[string]any{"device": id, "name": name, "scope": d.Scope, "kind": d.Kind, "sessions": d.Sessions})
	_ = sec.SendJSON(ctx, relaywire.PairMsg{T: "paired", Device: d.wire(), Host: label})
}

func clip(s string, n int) string {
	r := []rune(s)
	if len(r) > n {
		r = r[:n]
	}
	out := make([]rune, 0, len(r))
	for _, c := range r {
		if c >= 0x20 && c != 0x7f && !(c >= 0x80 && c < 0xa0) {
			out = append(out, c)
		}
	}
	return string(out)
}

func (s *Server) relayServeDevice(ctx context.Context, sec *relaywire.Secure, d *relayDevice) {
	r := &s.relay
	m := relaywire.NewServerMux(sec)
	r.mu.Lock()
	if r.tunnels[d.ID] == nil {
		r.tunnels[d.ID] = map[*relaywire.Mux]struct{}{}
	}
	r.tunnels[d.ID][m] = struct{}{}
	r.touchLocked(d)
	r.mu.Unlock()
	s.Log.Info("relay: device connected", "device", d.ID, "name", d.Name)
	s.relayAudit("connect", map[string]any{"device": d.ID, "name": d.Name})
	defer func() {
		_ = m.Close()
		r.mu.Lock()
		delete(r.tunnels[d.ID], m)
		if len(r.tunnels[d.ID]) == 0 {
			delete(r.tunnels, d.ID)
		}
		r.touchLocked(d)
		r.mu.Unlock()
		s.Log.Info("relay: device disconnected", "device", d.ID)
		s.relayAudit("disconnect", map[string]any{"device": d.ID})
	}()
	peer := peerInfo{uid: os.Getuid(), device: d}
	for {
		st, err := m.Accept(ctx)
		if err != nil {
			return
		}
		r.mu.Lock()
		gone := r.devices[d.ID] != d
		r.mu.Unlock()
		if gone {
			_ = st.Close()
			return
		}
		go s.serveConn(st, peer)
	}
}

// touchLocked records last_seen, persisted at most once a minute (r.mu held).
func (r *relayState) touchLocked(d *relayDevice) {
	now := time.Now().UTC()
	d.LastSeen = &now
	if time.Since(r.saved) >= relayLastSeenEvery && r.devices[d.ID] == d {
		_ = r.saveDevicesLocked()
	}
}

// relayOp answers op relay (humans on the socket only).
func (s *Server) relayOp(ctx context.Context, who caller, a proto.RelayArgs) (proto.RelayResult, error) {
	if !who.human() {
		return proto.RelayResult{}, proto.Errorf(proto.CodeForbidden, "relay: only a human on this host may manage the relay")
	}
	if err := s.relayInit(); err != nil {
		return proto.RelayResult{}, proto.Errorf(proto.CodeInternal, "relay: %v", err)
	}
	if who.dev != nil {
		return s.relayDeviceOp(who.dev, a)
	}
	r := &s.relay
	switch a.Action {
	case "", proto.RelayStatus:
		return s.relayStatus(), nil
	case proto.RelayInvite:
		return s.relayNewInvite(a)
	case proto.RelayPending:
		return s.relayPending(ctx, a)
	case proto.RelayApprove, proto.RelayCancel:
		r.mu.Lock()
		defer r.mu.Unlock()
		inv := r.invites[a.Invite]
		if inv == nil {
			return proto.RelayResult{}, proto.Errorf(proto.CodeNotFound, "relay: no invite %q", a.Invite)
		}
		if a.Action == proto.RelayCancel {
			if inv.state == relayInviteWaiting || inv.state == relayInviteRequest {
				if inv.state == relayInviteRequest {
					select {
					case inv.decision <- false:
					default:
					}
				}
				inv.setState(relayInviteCancelled)
			}
			delete(r.invites, inv.id)
			info := inv.info("")
			return proto.RelayResult{Invite: &info}, nil
		}
		if inv.state != relayInviteRequest {
			return proto.RelayResult{}, proto.Errorf(proto.CodeConflict, "relay: invite %s is %s, not waiting for approval", inv.id, inv.state)
		}
		select {
		case inv.decision <- a.Accept:
		default:
		}
		info := inv.info("")
		return proto.RelayResult{Invite: &info}, nil
	case proto.RelayDevices:
		return s.relayListDevices(nil), nil
	case proto.RelayRevoke:
		return s.relayRevoke(a.Device, "", false)
	}
	return proto.RelayResult{}, proto.Errorf(proto.CodeInvalid, "relay: unknown action %q (status, invite, pending, approve, cancel, devices, revoke)", a.Action)
}

// relayDeviceAdmin reports whether d may manage the host's devices: an owner
// device with the admin scope that is not limited to some sessions.
func relayDeviceAdmin(d *relayDevice) bool {
	return d.Kind == relayDeviceKindOwner && d.atLeast(config.RelayScopeAdmin) && len(d.Sessions) == 0
}

// relayDeviceOp is op relay for a paired device (relayGate let it through):
// devices lists every device for a device admin and only the caller's own
// entry otherwise; revoke takes the caller itself (any device) or, for a
// device admin, any device. Nothing else is offered remotely.
func (s *Server) relayDeviceOp(d *relayDevice, a proto.RelayArgs) (proto.RelayResult, error) {
	switch a.Action {
	case proto.RelayDevices:
		if relayDeviceAdmin(d) {
			return s.relayListDevices(nil), nil
		}
		return s.relayListDevices(d), nil
	case proto.RelayRevoke:
		if a.Device == "" || a.Device == d.ID {
			return s.relayRevoke(d.ID, d.ID, true)
		}
		if !relayDeviceAdmin(d) {
			return proto.RelayResult{}, proto.Errorf(proto.CodeForbidden,
				"relay: this device may only revoke itself (revoking others needs an owner device with the admin scope)")
		}
		return s.relayRevoke(a.Device, d.ID, false)
	}
	return proto.RelayResult{}, proto.Errorf(proto.CodeForbidden, "relay: %q is not allowed over the relay", a.Action)
}

// relayListDevices answers relay devices. only == nil lists every device and
// the live invites (humans on the socket: device admins get no invites, see
// relayDeviceOp); otherwise just that device's entry.
func (s *Server) relayListDevices(only *relayDevice) proto.RelayResult {
	r := &s.relay
	r.mu.Lock()
	defer r.mu.Unlock()
	res := proto.RelayResult{Devices: []proto.RelayDeviceInfo{}}
	for _, d := range r.devices {
		if only != nil && d.ID != only.ID {
			continue
		}
		res.Devices = append(res.Devices, d.info(len(r.tunnels[d.ID]) > 0))
	}
	sort.Slice(res.Devices, func(i, j int) bool { return res.Devices[i].CreatedAt.Before(res.Devices[j].CreatedAt) })
	if only != nil {
		return res
	}
	for _, inv := range r.invites {
		if inv.state == relayInviteWaiting || inv.state == relayInviteRequest {
			res.Invites = append(res.Invites, inv.info(""))
		}
	}
	return res
}

// relaySelfRevokeGrace lets the answer of a self-revoke reach the device
// before its tunnels are closed.
const relaySelfRevokeGrace = 300 * time.Millisecond

// relayRevoke removes the device named by id or name and closes its tunnels.
// by is the device that asked ("" = a human on the socket); self = the
// caller revokes itself, so its tunnels are closed a moment later.
func (s *Server) relayRevoke(device, by string, self bool) (proto.RelayResult, error) {
	r := &s.relay
	r.mu.Lock()
	var d *relayDevice
	for _, x := range r.devices {
		if x.ID == device || x.Name == device {
			if d != nil {
				r.mu.Unlock()
				return proto.RelayResult{}, proto.Errorf(proto.CodeConflict, "relay: several devices are named %q; use the id", device)
			}
			d = x
		}
	}
	if d == nil {
		r.mu.Unlock()
		return proto.RelayResult{}, proto.Errorf(proto.CodeNotFound, "relay: no device %q", device)
	}
	delete(r.devices, d.ID)
	err := r.saveDevicesLocked()
	var muxes []*relaywire.Mux
	for m := range r.tunnels[d.ID] {
		muxes = append(muxes, m)
	}
	r.mu.Unlock()
	closeAll := func() {
		for _, m := range muxes {
			_ = m.Close()
		}
	}
	if self {
		time.AfterFunc(relaySelfRevokeGrace, closeAll)
	} else {
		closeAll()
	}
	s.Log.Info("relay: device revoked", "device", d.ID, "name", d.Name, "by", by)
	f := map[string]any{"device": d.ID, "name": d.Name}
	if by != "" {
		f["by"] = by
	}
	s.relayAudit("revoke", f)
	info := d.info(false)
	if err != nil {
		return proto.RelayResult{Device: &info}, proto.Errorf(proto.CodeInternal, "relay: revoked, but saving devices.json failed: %v", err)
	}
	return proto.RelayResult{Device: &info}, nil
}

func (s *Server) relayStatus() proto.RelayResult {
	r := &s.relay
	r.mu.Lock()
	defer r.mu.Unlock()
	rc := s.Cfg.Relay
	res := proto.RelayResult{Enabled: rc.Enabled && rc.URL != "", URL: rc.URL, Label: rc.HostLabel(), Route: r.key.Route(),
		State: r.state, Error: r.lastErr}
	if !res.Enabled {
		res.State = relayStateOff
	}
	if !r.since.IsZero() {
		t := r.since
		res.Since = &t
	}
	return res
}

func (s *Server) relayNewInvite(a proto.RelayArgs) (proto.RelayResult, error) {
	rc := s.Cfg.Relay
	if !rc.Enabled || rc.URL == "" {
		return proto.RelayResult{}, proto.Errorf(proto.CodeNotReady, "relay: not enabled (set [relay] enabled = true and url in config.toml, then restart the daemon)")
	}
	kind := relayDeviceKindOwner
	if a.Guest {
		kind = relayDeviceKindGuest
	}
	scope := a.Scope
	if scope == "" {
		scope = config.RelayScopeOperate
		if config.RelayScopeRank(rc.ScopeCap()) < config.RelayScopeRank(scope) {
			scope = rc.ScopeCap()
		}
	}
	switch {
	case config.RelayScopeRank(scope) == 0:
		return proto.RelayResult{}, proto.Errorf(proto.CodeInvalid, "relay: scope %q is not view, operate or admin", scope)
	case config.RelayScopeRank(scope) > config.RelayScopeRank(rc.ScopeCap()):
		return proto.RelayResult{}, proto.Errorf(proto.CodeForbidden, "relay: scope %q is above [relay] max_scope = %q", scope, rc.ScopeCap())
	case a.Guest && scope == config.RelayScopeAdmin:
		return proto.RelayResult{}, proto.Errorf(proto.CodeInvalid, "relay: guests cannot have the admin scope")
	case a.Guest && len(a.Sessions) == 0:
		return proto.RelayResult{}, proto.Errorf(proto.CodeInvalid, "relay: a guest invite needs --sessions")
	}
	for _, g := range a.Sessions {
		if _, err := path.Match(g, ""); err != nil || g == "" {
			return proto.RelayResult{}, proto.Errorf(proto.CodeInvalid, "relay: bad session glob %q", g)
		}
	}
	ttl := relayInviteTTL
	if a.TTLSec > 0 {
		ttl = time.Duration(a.TTLSec) * time.Second
	}
	if ttl > relayInviteMaxTTL {
		return proto.RelayResult{}, proto.Errorf(proto.CodeInvalid, "relay: invite ttl above %s", relayInviteMaxTTL)
	}
	var devTTL time.Duration
	if a.Expires > 0 {
		devTTL = time.Duration(a.Expires) * time.Second
	} else if a.Guest {
		devTTL = relayGuestTTL
	}
	if a.Guest && devTTL > relayGuestMaxTTL {
		return proto.RelayResult{}, proto.Errorf(proto.CodeInvalid, "relay: guest access above %s", relayGuestMaxTTL)
	}

	idb := make([]byte, 8)
	code := make([]byte, 32)
	if _, err := rand.Read(idb); err != nil {
		return proto.RelayResult{}, err
	}
	if _, err := rand.Read(code); err != nil {
		return proto.RelayResult{}, err
	}
	inv := &relayInvite{id: hex.EncodeToString(idb), code: code, name: clip(a.Name, 64), scope: scope, kind: kind,
		sessions: a.Sessions, approve: !a.NoApprove, deviceTTL: devTTL, expiresAt: time.Now().Add(ttl),
		state: relayInviteWaiting, decision: make(chan bool, 1), changed: make(chan struct{})}

	r := &s.relay
	r.mu.Lock()
	now := time.Now()
	var live []*relayInvite
	for id, x := range r.invites {
		if !now.Before(x.expiresAt) || (x.state != relayInviteWaiting && x.state != relayInviteRequest) {
			delete(r.invites, id)
			continue
		}
		live = append(live, x)
	}
	if len(live) >= relayMaxInvites {
		sort.Slice(live, func(i, j int) bool { return live[i].expiresAt.Before(live[j].expiresAt) })
		old := live[0]
		if old.state == relayInviteRequest {
			select {
			case old.decision <- false:
			default:
			}
		}
		old.setState(relayInviteCancelled)
		delete(r.invites, old.id)
	}
	r.invites[inv.id] = inv
	link := relaywire.Invite{RelayURL: rc.URL, Route: r.key.Route(), HostKey: r.key.DH.Public, ID: inv.id, Code: code, Label: rc.HostLabel()}.String()
	info := inv.info(link)
	r.mu.Unlock()
	s.relayAudit("invite", map[string]any{"invite": inv.id, "scope": scope, "kind": kind, "sessions": a.Sessions})
	return proto.RelayResult{Invite: &info}, nil
}

func (s *Server) relayPending(ctx context.Context, a proto.RelayArgs) (proto.RelayResult, error) {
	r := &s.relay
	wait := min(time.Duration(a.WaitMS)*time.Millisecond, relayPendingMaxWait)
	deadline := time.Now().Add(wait)
	for {
		r.mu.Lock()
		inv := r.invites[a.Invite]
		if inv == nil {
			r.mu.Unlock()
			return proto.RelayResult{}, proto.Errorf(proto.CodeNotFound, "relay: no invite %q", a.Invite)
		}
		if inv.state == relayInviteWaiting && !time.Now().Before(inv.expiresAt) {
			inv.setState(relayInviteExpired)
		}
		info := inv.info("")
		changed := inv.changed
		r.mu.Unlock()
		left := time.Until(deadline)
		if info.State != relayInviteWaiting || left <= 0 {
			return proto.RelayResult{Invite: &info}, nil
		}
		t := time.NewTimer(min(left, time.Until(info.ExpiresAt)+10*time.Millisecond))
		select {
		case <-changed:
		case <-t.C:
		case <-ctx.Done():
			t.Stop()
			return proto.RelayResult{Invite: &info}, nil
		}
		t.Stop()
	}
}
