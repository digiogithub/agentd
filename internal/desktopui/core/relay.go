package core

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"runtime"
	"slices"
	"sync"
	"time"

	"github.com/flynn/noise"

	"github.com/digiogithub/agentd/internal/desktopui/backend"
	"github.com/digiogithub/agentd/internal/desktopui/remote"
	"github.com/digiogithub/agentd/internal/relayclient"
	"github.com/digiogithub/agentd/internal/relaywire"
)

// Relay hosts (backend.Host.Relay) are reached only through agentd-relay:
// one tunnel per host (relayLink), each daemon op on a fresh stream of it
// wrapped with client.NewConn(conn, nil). The daemon sees the device
// identity and enforces its scope; there is no shell, so zellij, tmux, plain
// shells, provisioning and the folder browser are not offered.

// relayDialTimeout bounds one tunnel connect (WebSocket + Noise handshake).
const relayDialTimeout = 30 * time.Second

// relayLink keeps one tunnel to a relay host and reopens it on demand.
type relayLink struct {
	b      *Backend
	id     string
	sig    string // what the link was built for (url, route, host key)
	target relayclient.Target

	dialMu sync.Mutex // one connect at a time

	mu      sync.Mutex
	cur     *relayclient.Tunnel
	dialing bool
	lastErr error
	closed  bool
}

func relaySig(rt *backend.RelayTarget) string { return rt.URL + "|" + rt.Route + "|" + rt.HostKey }

// deviceKey is this install's relay device key (remote.RelayKey).
func (b *Backend) deviceKey() (noise.DHKey, error) {
	b.mu.Lock()
	if b.devKey != nil {
		k := *b.devKey
		b.mu.Unlock()
		return k, nil
	}
	b.mu.Unlock()
	priv, err := remote.RelayKey(b.dir)
	if err != nil {
		return noise.DHKey{}, err
	}
	k, err := relaywire.DHKeyFromPrivate(priv)
	if err != nil {
		return noise.DHKey{}, err
	}
	b.mu.Lock()
	b.devKey = &k
	b.mu.Unlock()
	return k, nil
}

// deviceName is the name this computer gives itself to relay hosts.
func deviceName() string {
	n, err := os.Hostname()
	if err != nil || n == "" {
		return "agentd-desktop"
	}
	if len(n) > 64 {
		n = n[:64]
	}
	return n
}

// relayHost returns the saved host id when it is a relay host.
func (b *Backend) relayHost(id string) (backend.Host, bool) {
	if id == backend.LocalHostID {
		return backend.Host{}, false
	}
	for _, h := range b.savedHosts() {
		if h.ID == id {
			return h, h.IsRelay()
		}
	}
	return backend.Host{}, false
}

// relayLinkFor returns the link of relay host h, (re)built when its target
// changed.
func (b *Backend) relayLinkFor(h backend.Host) (*relayLink, error) {
	sig := relaySig(h.Relay)
	b.mu.Lock()
	l := b.relays[h.ID]
	b.mu.Unlock()
	if l != nil && l.sig == sig {
		return l, nil
	}
	hostKey, err := relaywire.B64.DecodeString(h.Relay.HostKey)
	if err != nil || len(hostKey) != 32 {
		return nil, fmt.Errorf("core: relay host %s has a bad host key; pair it again", h.ID)
	}
	dev, err := b.deviceKey()
	if err != nil {
		return nil, err
	}
	nl := &relayLink{b: b, id: h.ID, sig: sig, target: relayclient.Target{
		RelayURL: h.Relay.URL, Route: h.Relay.Route, HostKey: hostKey, Device: dev,
		Name: deviceName(), Platform: runtime.GOOS,
	}}
	b.mu.Lock()
	if cur := b.relays[h.ID]; cur != nil && cur.sig == sig {
		b.mu.Unlock()
		return cur, nil
	}
	old := b.relays[h.ID]
	b.relays[h.ID] = nl
	b.mu.Unlock()
	if old != nil {
		old.close()
	}
	return nl, nil
}

// dropRelay closes and forgets the link of host id.
func (b *Backend) dropRelay(id string) {
	b.mu.Lock()
	l := b.relays[id]
	delete(b.relays, id)
	b.mu.Unlock()
	if l != nil {
		l.close()
	}
}

// dialRelay opens a fresh daemon connection to relay host h.
func (b *Backend) dialRelay(ctx context.Context, h backend.Host) (net.Conn, error) {
	l, err := b.relayLinkFor(h)
	if err != nil {
		return nil, err
	}
	return l.open(ctx)
}

func tunnelDone(t *relayclient.Tunnel) bool {
	select {
	case <-t.Done():
		return true
	default:
		return false
	}
}

// open returns a new stream on the live tunnel, connecting first if needed.
func (l *relayLink) open(ctx context.Context) (net.Conn, error) {
	t, err := l.tunnel(ctx)
	if err != nil {
		return nil, err
	}
	c, err := t.Open()
	if err == nil {
		return c, nil
	}
	// The tunnel died between the check and the open: one retry.
	l.drop(t)
	if t, err = l.tunnel(ctx); err != nil {
		return nil, err
	}
	return t.Open()
}

func (l *relayLink) live() *relayclient.Tunnel {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.cur != nil && tunnelDone(l.cur) {
		l.cur = nil
	}
	return l.cur
}

// tunnel returns the live tunnel or connects one.
func (l *relayLink) tunnel(ctx context.Context) (*relayclient.Tunnel, error) {
	if t := l.live(); t != nil {
		return t, nil
	}
	l.dialMu.Lock()
	defer l.dialMu.Unlock()
	if t := l.live(); t != nil {
		return t, nil
	}
	l.mu.Lock()
	if l.closed {
		l.mu.Unlock()
		return nil, errors.New("core: relay host removed")
	}
	l.dialing = true
	l.mu.Unlock()
	go l.b.publishHosts() // "connecting"

	// The tunnel outlives the op that opened it: its connect is bounded by
	// the backend and a timeout, and aborted when ctx ends first.
	cctx, cancel := context.WithTimeout(l.b.ctx, relayDialTimeout)
	stop := context.AfterFunc(ctx, cancel)
	t, err := relayclient.Connect(cctx, l.target)
	stop()
	cancel()
	if err != nil {
		err = relayError(err)
		if ctx.Err() != nil {
			err = ctx.Err()
		}
	}

	l.mu.Lock()
	l.dialing = false
	switch {
	case err != nil:
		if ctx.Err() == nil {
			l.lastErr = err
		}
	case l.closed:
		_ = t.Close()
		err = errors.New("core: relay host removed")
	default:
		l.cur, l.lastErr = t, nil
	}
	l.mu.Unlock()
	l.b.publishHosts()
	if err != nil {
		l.b.log.Debug("relay connect", "host", l.id, "err", err)
		return nil, err
	}
	l.b.syncDevice(l.id, t.Welcome)
	l.b.wg.Add(1)
	go func() {
		defer l.b.wg.Done()
		select {
		case <-t.Done():
		case <-l.b.ctx.Done():
			_ = t.Close()
		}
		l.mu.Lock()
		if l.cur == t {
			l.cur = nil
		}
		l.mu.Unlock()
		l.b.publishHosts()
	}()
	return t, nil
}

func (l *relayLink) drop(t *relayclient.Tunnel) {
	l.mu.Lock()
	if l.cur == t {
		l.cur = nil
	}
	l.mu.Unlock()
	_ = t.Close()
}

func (l *relayLink) close() {
	l.mu.Lock()
	l.closed = true
	t := l.cur
	l.cur = nil
	l.mu.Unlock()
	if t != nil {
		_ = t.Close()
	}
}

// reset clears the last error (Connect retries now).
func (l *relayLink) reset() {
	l.mu.Lock()
	l.lastErr = nil
	l.mu.Unlock()
}

// state is the link's host state.
func (l *relayLink) state() (backend.HostState, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	switch {
	case l.cur != nil && !tunnelDone(l.cur):
		return backend.HostOnline, nil
	case l.dialing:
		return backend.HostConnecting, nil
	case l.lastErr != nil:
		return backend.HostError, l.lastErr
	}
	return backend.HostOffline, nil
}

// relayState is the state of relay host id ("offline" before any dial).
func (b *Backend) relayState(id string) (backend.HostState, error) {
	b.mu.Lock()
	l := b.relays[id]
	b.mu.Unlock()
	if l == nil {
		return backend.HostOffline, nil
	}
	return l.state()
}

// relayError maps tunnel errors onto the backend's relay errors.
func relayError(err error) error {
	var re *relaywire.RemoteError
	switch {
	case errors.Is(err, relaywire.ErrHostOffline):
		return backend.ErrRelayHostOffline
	case errors.As(err, &re) && re.Code == "forbidden":
		return fmt.Errorf("%w: %s", backend.ErrDeviceRevoked, re.Msg)
	}
	return err
}

// syncDevice stores what the host's welcome says about this device (a
// scope downgraded on the host applies at once).
func (b *Backend) syncDevice(id string, w relaywire.Welcome) {
	d := w.Device
	if d == nil {
		return
	}
	h, ok := b.relayHost(id)
	if !ok {
		return
	}
	r := h.Relay
	if r.DeviceID == d.ID && r.Scope == d.Scope && r.Kind == d.Kind && slices.Equal(r.Sessions, d.Sessions) {
		return
	}
	err := b.hosts.Update(id, func(x *backend.Host) {
		if x.Relay != nil {
			x.Relay.DeviceID, x.Relay.Scope, x.Relay.Kind = d.ID, d.Scope, d.Kind
			x.Relay.Sessions = slices.Clone(d.Sessions)
		}
	})
	if err != nil {
		b.log.Warn("store relay device", "host", id, "err", err)
		return
	}
	b.invalidateHosts()
	b.publishHosts()
}

// PairRelay redeems an invite link and saves the host (re-pairing a host
// already saved keeps its id). The link is never logged: it holds the
// one-time code.
func (b *Backend) PairRelay(ctx context.Context, link, name string) (backend.Host, error) {
	inv, err := relaywire.ParseInvite(link)
	if err != nil {
		var ue *url.Error
		if errors.As(err, &ue) {
			// url errors quote the input, which carries the code.
			return backend.Host{}, backend.ErrBadInvite
		}
		return backend.Host{}, fmt.Errorf("%w (%v)", backend.ErrBadInvite, err)
	}
	dev, err := b.deviceKey()
	if err != nil {
		return backend.Host{}, err
	}
	if name == "" {
		name = deviceName()
	}
	p, err := relayclient.Pair(ctx, inv, dev, name, runtime.GOOS)
	if err != nil {
		var denied *relayclient.ErrDenied
		if errors.As(err, &denied) {
			return backend.Host{}, fmt.Errorf("%w: %s", backend.ErrPairDenied, denied.Msg)
		}
		if ctx.Err() != nil {
			return backend.Host{}, ctx.Err()
		}
		return backend.Host{}, relayError(err)
	}
	label := p.Host
	if label == "" {
		label = inv.Label
	}
	if label == "" {
		label = "relay"
	}
	h := backend.Host{Name: label, Relay: &backend.RelayTarget{
		URL: inv.RelayURL, Route: inv.Route, HostKey: relaywire.B64.EncodeToString(inv.HostKey),
		DeviceID: p.Device.ID, Scope: p.Device.Scope, Sessions: slices.Clone(p.Device.Sessions), Kind: p.Device.Kind,
	}}
	for _, o := range b.savedHosts() {
		if o.Relay != nil && o.Relay.Route == inv.Route {
			h.ID = o.ID
		}
	}
	saved, err := b.hosts.Save(h)
	b.invalidateHosts()
	if err != nil {
		return backend.Host{}, err
	}
	b.log.Info("relay host paired", "host", saved.ID, "device", p.Device.ID, "scope", p.Device.Scope)
	b.dropRelay(saved.ID)
	b.activate(saved.ID)
	b.wakeMonitor(saved.ID)
	b.publishHosts()
	return saved, nil
}

// connectRelay connects relay host h now (clearing the last error) and
// starts watching it.
func (b *Backend) connectRelay(ctx context.Context, h backend.Host) error {
	l, err := b.relayLinkFor(h)
	if err != nil {
		return err
	}
	l.reset()
	b.activate(h.ID)
	if _, err := l.tunnel(ctx); err != nil {
		b.publishHosts()
		return err
	}
	b.wakeMonitor(h.ID)
	b.publishHosts()
	return nil
}
