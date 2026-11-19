package relayserver

import (
	"context"
	"errors"
	"io"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/digiogithub/agentd/internal/relaywire"
)

func startRelay(t *testing.T, cfg Config) (*Server, string) {
	t.Helper()
	s := New(cfg)
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(ts.Close)
	return s, ts.URL
}

// echoHost runs a host link whose tunnels accept any device and echo every stream.
func echoHost(t *testing.T, url, token string, key relaywire.HostKey) (states chan string, stop func()) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	states = make(chan string, 64)
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		relaywire.RunHostLink(ctx, relaywire.HostLinkConfig{
			URL: url, Key: key, JoinToken: token, MinBackoff: 20 * time.Millisecond, MaxBackoff: 50 * time.Millisecond,
			OnState: func(s string, err error) { states <- s },
			Serve: func(ctx context.Context, c relaywire.MsgConn) {
				sec, _, _, err := relaywire.ServerHandshake(ctx, c, key.Route(), key.DH, nil, func(pub []byte, h relaywire.Hello) relaywire.Decision {
					return relaywire.Decision{Welcome: relaywire.Welcome{OK: true, Host: "box"}}
				})
				if err != nil {
					return
				}
				m := relaywire.NewServerMux(sec)
				defer func() { _ = m.Close() }()
				for {
					s, err := m.Accept(ctx)
					if err != nil {
						return
					}
					go func() { _, _ = io.Copy(s, s); _ = s.Close() }()
				}
			},
		})
	}()
	return states, func() { cancel(); wg.Wait() }
}

func waitState(t *testing.T, states chan string, want string) {
	t.Helper()
	deadline := time.After(5 * time.Second)
	for {
		select {
		case s := <-states:
			if s == want {
				return
			}
		case <-deadline:
			t.Fatalf("host link never reached %q", want)
		}
	}
}

func TestTunnelEcho(t *testing.T) {
	_, url := startRelay(t, Config{JoinTokens: []string{"secret"}})
	key, _ := relaywire.GenerateHostKey()
	states, stop := echoHost(t, url, "secret", key)
	defer stop()
	waitState(t, states, "online")

	dev, _ := relaywire.GenerateDHKey()
	ctx := context.Background()
	ws, err := relaywire.DialTunnel(ctx, url, key.Route())
	if err != nil {
		t.Fatal(err)
	}
	sec, w, err := relaywire.ClientHandshake(ctx, ws, key.Route(), key.DH.Public, dev, relaywire.Hello{Mode: relaywire.ModeConnect}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if w.Host != "box" {
		t.Fatalf("welcome %+v", w)
	}
	m := relaywire.NewClientMux(sec)
	defer func() { _ = m.Close() }()
	s, err := m.Open()
	if err != nil {
		t.Fatal(err)
	}
	msg := make([]byte, 200<<10)
	for i := range msg {
		msg[i] = byte(i)
	}
	go func() { _, _ = s.Write(msg) }()
	got := make([]byte, len(msg))
	if _, err := io.ReadFull(s, got); err != nil {
		t.Fatal(err)
	}
	for i := range got {
		if got[i] != msg[i] {
			t.Fatal("echo mismatch")
		}
	}
}

func TestHostOfflineAndRefused(t *testing.T) {
	_, url := startRelay(t, Config{JoinTokens: []string{"secret"}, AcceptTimeout: time.Second})
	key, _ := relaywire.GenerateHostKey()
	ws, err := relaywire.DialTunnel(context.Background(), url, key.Route())
	if err != nil {
		t.Fatal(err)
	}
	_, err = ws.ReadMsg(context.Background())
	if !errors.Is(relaywire.TunnelError(err), relaywire.ErrHostOffline) {
		t.Fatalf("want host offline, got %v", err)
	}

	states, stop := echoHost(t, url, "wrong", key)
	defer stop()
	waitState(t, states, "offline")
}

func TestRelinkReplacesOldLink(t *testing.T) {
	s, url := startRelay(t, Config{Open: true})
	key, _ := relaywire.GenerateHostKey()
	st1, stop1 := echoHost(t, url, "", key)
	defer stop1()
	waitState(t, st1, "online")
	st2, stop2 := echoHost(t, url, "", key)
	defer stop2()
	waitState(t, st2, "online")
	if n := s.Routes(); n != 1 {
		t.Fatalf("routes = %d", n)
	}
}
