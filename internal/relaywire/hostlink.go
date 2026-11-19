package relaywire

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand/v2"
	"net/url"
	"sync"
	"time"

	"github.com/coder/websocket"
)

// HostLinkConfig configures a host's link to a relay.
type HostLinkConfig struct {
	URL       string // relay base URL
	Key       HostKey
	JoinToken string
	// Serve is called (in its own goroutine) for every client tunnel, with
	// the tunnel's message connection. It owns the connection.
	Serve func(ctx context.Context, c MsgConn)
	// OnState reports link changes: "connecting", "online", "offline" (with err).
	OnState func(state string, err error)
	// Backoff bounds (defaults 1 s, 60 s).
	MinBackoff, MaxBackoff time.Duration
	PingInterval           time.Duration // default 30 s
}

type ctrl struct {
	T         string `json:"t"`
	Nonce     string `json:"nonce,omitempty"`
	Pub       string `json:"pub,omitempty"`
	Sig       string `json:"sig,omitempty"`
	JoinToken string `json:"join_token,omitempty"`
	Route     string `json:"route,omitempty"`
	Tunnel    string `json:"tunnel,omitempty"`
	Error     string `json:"error,omitempty"`
}

// ErrRefused is a registration refused by the relay (bad join token, signature).
type ErrRefused struct{ Msg string }

func (e *ErrRefused) Error() string { return "relay refused the host: " + e.Msg }

// RunHostLink keeps the host registered on the relay until ctx ends,
// reconnecting with exponential backoff. A refusal is retried at the maximum
// backoff (the token may be fixed in the config meanwhile).
func RunHostLink(ctx context.Context, cfg HostLinkConfig) {
	if cfg.MinBackoff <= 0 {
		cfg.MinBackoff = time.Second
	}
	if cfg.MaxBackoff <= 0 {
		cfg.MaxBackoff = time.Minute
	}
	if cfg.PingInterval <= 0 {
		cfg.PingInterval = 30 * time.Second
	}
	state := func(s string, err error) {
		if cfg.OnState != nil {
			cfg.OnState(s, err)
		}
	}
	backoff := cfg.MinBackoff
	var wg sync.WaitGroup
	defer wg.Wait()
	for ctx.Err() == nil {
		state("connecting", nil)
		online, err := hostLinkOnce(ctx, cfg, &wg, func() { state("online", nil) })
		if ctx.Err() != nil {
			break
		}
		state("offline", err)
		if online {
			backoff = cfg.MinBackoff
		}
		wait := backoff
		var refused *ErrRefused
		if errors.As(err, &refused) {
			wait = cfg.MaxBackoff
		}
		wait += time.Duration(rand.Int64N(int64(wait)/4 + 1))
		t := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			t.Stop()
		case <-t.C:
		}
		backoff = min(backoff*2, cfg.MaxBackoff)
	}
	state("offline", nil)
}

func hostLinkOnce(ctx context.Context, cfg HostLinkConfig, wg *sync.WaitGroup, online func()) (bool, error) {
	u, err := Endpoint(cfg.URL, "/v1/host", nil)
	if err != nil {
		return false, err
	}
	dctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	c, err := Dial(dctx, u, nil)
	cancel()
	if err != nil {
		return false, err
	}
	defer func() { _ = c.CloseNow() }()

	read := func(d time.Duration) (ctrl, error) {
		rctx, cancel := context.WithTimeout(ctx, d)
		defer cancel()
		typ, b, err := c.Read(rctx)
		if err != nil {
			return ctrl{}, err
		}
		var m ctrl
		if typ != websocket.MessageText || json.Unmarshal(b, &m) != nil {
			return ctrl{}, errors.New("relay: bad control message")
		}
		return m, nil
	}
	ch, err := read(15 * time.Second)
	if err != nil {
		return false, err
	}
	nonce, err := B64.DecodeString(ch.Nonce)
	if ch.T != "challenge" || err != nil || len(nonce) < 16 {
		return false, errors.New("relay: bad challenge")
	}
	reg := ctrl{
		T:         "register",
		Pub:       B64.EncodeToString(cfg.Key.Sign.Public().(ed25519.PublicKey)),
		Sig:       B64.EncodeToString(ed25519.Sign(cfg.Key.Sign, RegisterMessage(nonce))),
		JoinToken: cfg.JoinToken,
	}
	b, _ := json.Marshal(reg)
	if err := c.Write(ctx, websocket.MessageText, b); err != nil {
		return false, err
	}
	ok, err := read(15 * time.Second)
	if err != nil {
		return false, err
	}
	switch {
	case ok.T == "error":
		return false, &ErrRefused{Msg: ok.Error}
	case ok.T != "ok":
		return false, errors.New("relay: unexpected answer to register")
	case ok.Route != cfg.Key.Route():
		return false, fmt.Errorf("relay: route mismatch (%s)", ok.Route)
	}
	online()

	lctx, lcancel := context.WithCancel(ctx)
	defer lcancel()
	go KeepAlive(lctx, c, cfg.PingInterval)
	for {
		typ, b, err := c.Read(lctx)
		if err != nil {
			return true, err
		}
		var m ctrl
		if typ != websocket.MessageText || json.Unmarshal(b, &m) != nil || m.T != "incoming" || m.Tunnel == "" {
			continue
		}
		wg.Add(1)
		go func(id string) {
			defer wg.Done()
			acceptTunnel(ctx, cfg, id)
		}(m.Tunnel)
	}
}

func acceptTunnel(ctx context.Context, cfg HostLinkConfig, id string) {
	u, err := Endpoint(cfg.URL, "/v1/accept", url.Values{"tunnel": {id}})
	if err != nil {
		return
	}
	dctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	c, err := Dial(dctx, u, nil)
	cancel()
	if err != nil {
		return
	}
	tctx, tcancel := context.WithCancel(ctx)
	defer tcancel()
	go KeepAlive(tctx, c, cfg.PingInterval)
	ws := NewWS(c)
	defer func() { _ = c.CloseNow() }()
	cfg.Serve(tctx, ws)
}
