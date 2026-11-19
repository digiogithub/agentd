package main

import (
	"bytes"
	"context"
	"net/http/httptest"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/digiogithub/agentd/internal/client"
	"github.com/digiogithub/agentd/internal/config"
	"github.com/digiogithub/agentd/internal/daemon"
	"github.com/digiogithub/agentd/internal/mailbox"
	"github.com/digiogithub/agentd/internal/proto"
	"github.com/digiogithub/agentd/internal/relayclient"
	"github.com/digiogithub/agentd/internal/relayserver"
	"github.com/digiogithub/agentd/internal/relaywire"
)

// syncBuf is a goroutine-safe output buffer.
type syncBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuf) Write(p []byte) (int, error) { s.mu.Lock(); defer s.mu.Unlock(); return s.b.Write(p) }
func (s *syncBuf) String() string              { s.mu.Lock(); defer s.mu.Unlock(); return s.b.String() }

func TestRelayInviteApproved(t *testing.T) {
	dir := t.TempDir()
	t.Setenv(config.EnvStateDir, filepath.Join(dir, "state"))
	sock := filepath.Join(dir, "s.sock")
	t.Setenv(config.EnvSocket, sock)
	if err := config.EnsureDirs(); err != nil {
		t.Fatal(err)
	}
	rs := httptest.NewServer(relayserver.New(relayserver.Config{Open: true}).Handler())
	defer rs.Close()
	cfg := config.Default()
	cfg.Relay = config.RelayConfig{Enabled: true, URL: rs.URL, Label: "box"}
	st, err := mailbox.Open(filepath.Join(dir, "m.db"))
	if err != nil {
		t.Fatal(err)
	}
	srv := daemon.New(cfg, nil, st, nil)
	if err := srv.Listen(); err != nil {
		t.Fatal(err)
	}
	go func() { _ = srv.Serve(context.Background()) }()
	defer srv.Shutdown()
	dial := func() (*client.Client, error) { return client.Dial(sock) }

	c, _ := dial()
	for i := 0; ; i++ {
		s, err := c.Relay(proto.RelayArgs{Action: proto.RelayStatus})
		if err == nil && s.State == "online" {
			break
		}
		if i > 250 {
			t.Fatalf("relay never online: %+v %v", s, err)
		}
		time.Sleep(20 * time.Millisecond)
	}
	_ = c.Close()

	out := &syncBuf{}
	done := make(chan error, 1)
	go func() {
		done <- runRelayInvite(context.Background(), proto.RelayArgs{Scope: "view"}, strings.NewReader("y\n"), out, &syncBuf{}, dial)
	}()
	var link string
	re := regexp.MustCompile(`Link:  (\S+)`)
	for i := 0; link == ""; i++ {
		if m := re.FindStringSubmatch(out.String()); m != nil {
			link = m[1]
		}
		if i > 250 {
			t.Fatal("no link printed")
		}
		time.Sleep(20 * time.Millisecond)
	}
	inv, err := relaywire.ParseInvite(link)
	if err != nil {
		t.Fatal(err)
	}
	dev, _ := relaywire.GenerateDHKey()
	p, err := relayclient.Pair(context.Background(), inv, dev, "pixel", "android")
	if err != nil {
		t.Fatal(err)
	}
	if p.Device.Scope != "view" || p.Host != "box" {
		t.Fatalf("paired %+v", p)
	}
	if err := <-done; err != nil {
		t.Fatal(err, out.String())
	}
	if !strings.Contains(out.String(), "Paired: device "+p.Device.ID) {
		t.Fatalf("output: %s", out.String())
	}
}
