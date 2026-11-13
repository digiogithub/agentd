package main

import (
	"bytes"
	"context"
	"crypto/tls"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/digiogithub/agentd/internal/client"
	"github.com/digiogithub/agentd/internal/config"
	"github.com/digiogithub/agentd/internal/tlsutil"
)

func pinnedClient(pin string) *http.Client {
	return &http.Client{Timeout: 5 * time.Second, Transport: &http.Transport{TLSClientConfig: &tls.Config{
		InsecureSkipVerify: true, VerifyPeerCertificate: tlsutil.Verifier(pin),
	}}}
}

// startTLSServe runs the gateway with self-signed TLS on a non-loopback bind.
func startTLSServe(t *testing.T, api config.APIConfig, tlsDir string) (addr net.Addr, stop func()) {
	t.Helper()
	sock := serveTestDaemon(t)
	ctx, cancel := context.WithCancel(context.Background())
	addrc := make(chan net.Addr, 1)
	done := make(chan error, 1)
	go func() {
		done <- runServe(ctx, serveOptions{
			API: api, Listen: "0.0.0.0:0", Log: discardLog(), TLSDir: tlsDir,
			Dial:  func() (*client.Client, error) { return client.Dial(sock) },
			Ready: func(a net.Addr) { addrc <- a },
		})
	}()
	select {
	case addr = <-addrc:
	case err := <-done:
		t.Fatalf("runServe returned %v", err)
	case <-time.After(5 * time.Second):
		t.Fatal("not ready")
	}
	return addr, func() {
		cancel()
		select {
		case <-done:
		case <-time.After(8 * time.Second):
			t.Error("no shutdown")
		}
	}
}

func TestServeSelfSignedPinned(t *testing.T) {
	dir := t.TempDir()
	api := config.Default().API
	api.TLS = config.APITLSSelfSigned
	addr, stop := startTLSServe(t, api, dir)
	defer stop()
	_, port, _ := net.SplitHostPort(addr.String())
	url := "https://127.0.0.1:" + port + "/v1/health"

	pin, err := tlsutil.CurrentPin(api.TLS, tlsutil.Config{Dir: dir})
	if err != nil || !strings.HasPrefix(pin, "sha256:") {
		t.Fatalf("pin %q %v", pin, err)
	}
	resp, err := pinnedClient(pin).Get(url)
	if err != nil {
		t.Fatalf("pinned client: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("status %d", resp.StatusCode)
	}
	if _, err := pinnedClient("sha256:" + strings.Repeat("0", 64)).Get(url); err == nil {
		t.Fatal("wrong pin accepted")
	}
	// An unpinned default client rejects the self-signed cert.
	if _, err := (&http.Client{Timeout: 5 * time.Second}).Get(url); err == nil {
		t.Fatal("system-roots client accepted a self-signed cert")
	}
}

func TestTLSShowRotate(t *testing.T) {
	dir := t.TempDir()
	api := config.Default().API
	api.TLS, api.TLSHosts = config.APITLSSelfSigned, []string{"box.tailnet.ts.net"}
	var a, b bytes.Buffer
	if err := tlsShow(&a, api, dir); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(a.String(), "box.tailnet.ts.net") || !strings.Contains(a.String(), "pin:     sha256:") {
		t.Fatalf("show:\n%s", a.String())
	}
	if err := tlsRotate(api, dir); err != nil {
		t.Fatal(err)
	}
	if err := tlsShow(&b, api, dir); err != nil {
		t.Fatal(err)
	}
	if a.String() == b.String() {
		t.Fatal("rotate did not change the pin")
	}
	api.TLS = config.APITLSOff
	if err := tlsRotate(api, dir); err == nil {
		t.Fatal("rotate accepted for off")
	}
	var c bytes.Buffer
	if err := tlsShow(&c, api, dir); err != nil || !strings.Contains(c.String(), "off") {
		t.Fatalf("%v %q", err, c.String())
	}
	_ = io.Discard
}
