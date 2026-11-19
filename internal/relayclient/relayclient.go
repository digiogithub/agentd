// Package relayclient is the client side of the relay transport for Go
// clients (agentd-desktop): pairing through an invite link and a reconnecting
// tunnel whose streams are daemon connections (wrap them with
// client.NewConn(conn, nil)). Wire format: internal/relaywire.
package relayclient

import (
	"context"
	"errors"
	"fmt"
	"net"
	"sync"
	"time"

	"github.com/flynn/noise"

	"github.com/digiogithub/agentd/internal/relaywire"
)

// Paired is the result of a successful pairing.
type Paired struct {
	Device relaywire.Device
	Host   string // host label
}

// ErrDenied is returned by Pair when the host refused the device.
type ErrDenied struct{ Msg string }

func (e *ErrDenied) Error() string { return "pairing denied: " + e.Msg }

// Pair redeems an invite with the device key dev. It blocks until the host
// approves or denies (up to the invite's lifetime, or ctx).
func Pair(ctx context.Context, inv relaywire.Invite, dev noise.DHKey, name, platform string) (Paired, error) {
	ws, err := relaywire.DialTunnel(ctx, inv.RelayURL, inv.Route)
	if err != nil {
		return Paired{}, err
	}
	defer func() { _ = ws.Close() }()
	hello := relaywire.Hello{Mode: relaywire.ModePair, Invite: inv.ID, Name: name, Platform: platform}
	sec, _, err := relaywire.ClientHandshake(ctx, ws, inv.Route, inv.HostKey, dev, hello, inv.Code, nil)
	if err != nil {
		if mapped := relaywire.TunnelError(err); mapped != err {
			return Paired{}, mapped
		}
		// The host closes without answering a handshake whose invite it does
		// not know (the client could not read the answer anyway).
		return Paired{}, fmt.Errorf("relay: invite refused (unknown, used or expired): %w", err)
	}
	if err := sec.SendJSON(ctx, relaywire.PairMsg{T: "confirm"}); err != nil {
		return Paired{}, err
	}
	var m relaywire.PairMsg
	if err := sec.RecvJSON(ctx, &m); err != nil {
		return Paired{}, relaywire.TunnelError(err)
	}
	switch {
	case m.T == "paired" && m.Device != nil:
		return Paired{Device: *m.Device, Host: m.Host}, nil
	case m.T == "denied":
		return Paired{}, &ErrDenied{Msg: m.Error}
	}
	return Paired{}, errors.New("relay: unexpected pairing answer")
}

// Target is a paired host as a client stores it.
type Target struct {
	RelayURL string
	Route    string
	HostKey  []byte // pinned host X25519 public key
	Device   noise.DHKey
	Name     string // device name sent in the hello
	Platform string
}

// Tunnel is one established tunnel to a host.
type Tunnel struct {
	mux     *relaywire.Mux
	Welcome relaywire.Welcome
	cancel  context.CancelFunc
}

// Open opens a new stream: one daemon connection.
func (t *Tunnel) Open() (net.Conn, error) { return t.mux.Open() }

// Done is closed when the tunnel ends.
func (t *Tunnel) Done() <-chan struct{} { return t.mux.Done() }

// Close ends the tunnel.
func (t *Tunnel) Close() error {
	t.cancel()
	return t.mux.Close()
}

// Connect opens a tunnel to a paired host.
func Connect(ctx context.Context, tg Target) (*Tunnel, error) {
	ws, err := relaywire.DialTunnel(ctx, tg.RelayURL, tg.Route)
	if err != nil {
		return nil, err
	}
	hello := relaywire.Hello{Mode: relaywire.ModeConnect, Name: tg.Name, Platform: tg.Platform}
	sec, w, err := relaywire.ClientHandshake(ctx, ws, tg.Route, tg.HostKey, tg.Device, hello, nil, nil)
	if err != nil {
		_ = ws.C.CloseNow()
		return nil, relaywire.TunnelError(err)
	}
	kctx, cancel := context.WithCancel(context.Background())
	go relaywire.KeepAlive(kctx, ws.C, 25*time.Second)
	t := &Tunnel{mux: relaywire.NewClientMux(sec), Welcome: w, cancel: cancel}
	go func() { <-t.mux.Done(); cancel() }()
	return t, nil
}

// Dialer keeps one tunnel to a host and reopens it when it drops. Dial
// returns a fresh stream on the live tunnel.
type Dialer struct {
	Target Target

	mu  sync.Mutex
	cur *Tunnel
}

// Dial returns a new daemon connection through the tunnel, connecting first
// if needed.
func (d *Dialer) Dial(ctx context.Context) (net.Conn, error) {
	t, err := d.tunnel(ctx)
	if err != nil {
		return nil, err
	}
	c, err := t.Open()
	if err == nil {
		return c, nil
	}
	// The tunnel died between the check and the open: one retry.
	d.drop(t)
	if t, err = d.tunnel(ctx); err != nil {
		return nil, err
	}
	return t.Open()
}

// Welcome returns the host's welcome of the live tunnel, connecting if needed.
func (d *Dialer) Welcome(ctx context.Context) (relaywire.Welcome, error) {
	t, err := d.tunnel(ctx)
	if err != nil {
		return relaywire.Welcome{}, err
	}
	return t.Welcome, nil
}

func (d *Dialer) tunnel(ctx context.Context) (*Tunnel, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.cur != nil {
		select {
		case <-d.cur.Done():
			d.cur = nil
		default:
			return d.cur, nil
		}
	}
	t, err := Connect(ctx, d.Target)
	if err != nil {
		return nil, err
	}
	d.cur = t
	return t, nil
}

func (d *Dialer) drop(t *Tunnel) {
	d.mu.Lock()
	if d.cur == t {
		d.cur = nil
	}
	d.mu.Unlock()
	_ = t.Close()
}

// Close ends the current tunnel.
func (d *Dialer) Close() error {
	d.mu.Lock()
	t := d.cur
	d.cur = nil
	d.mu.Unlock()
	if t != nil {
		return t.Close()
	}
	return nil
}
