package main

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/digiogithub/agentd/internal/client"
	"github.com/digiogithub/agentd/internal/config"
)

func runCLI(t *testing.T, args ...string) (string, string, error) {
	t.Helper()
	cmd := newRootCmd()
	var out, errb bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&errb)
	cmd.SetArgs(args)
	err := cmd.Execute()
	return out.String(), errb.String(), err
}

func TestDevicesCLI(t *testing.T) {
	t.Setenv(config.EnvStateDir, filepath.Join(t.TempDir(), "state"))
	if out, _, err := runCLI(t, "serve", "devices"); err != nil || !strings.Contains(out, "no devices") {
		t.Fatalf("empty list: %q %v", out, err)
	}
	if _, _, err := runCLI(t, "serve", "devices", "add"); err == nil {
		t.Fatal("add without --name")
	}
	if _, _, err := runCLI(t, "serve", "devices", "add", "--name", "x", "--scope", "root"); err == nil {
		t.Fatal("bad scope accepted")
	}
	out, errOut, err := runCLI(t, "serve", "devices", "add", "--name", "ci", "--scope", "write", "--ttl", "2d")
	if err != nil {
		t.Fatal(err)
	}
	tok := strings.TrimSpace(out)
	if !strings.HasPrefix(tok, "agd_") || strings.Contains(errOut, tok) {
		t.Fatalf("token output: %q / %q", out, errOut)
	}
	list, _, err := runCLI(t, "serve", "devices", "list")
	if err != nil || !strings.Contains(list, "ci") || !strings.Contains(list, "write") || !strings.Contains(list, "active") || strings.Contains(list, tok) {
		t.Fatalf("list: %q %v", list, err)
	}
	if _, _, err := runCLI(t, "serve", "devices", "revoke", "nope"); err == nil {
		t.Fatal("revoked a missing device")
	}
	if rev, _, err := runCLI(t, "serve", "devices", "revoke", "ci"); err != nil || !strings.Contains(rev, "revoked") {
		t.Fatalf("revoke: %q %v", rev, err)
	}
	if list, _, _ := runCLI(t, "serve", "devices"); !strings.Contains(list, "no devices") {
		t.Fatalf("after revoke: %q", list)
	}
	// Unsafe file: the CLI refuses instead of overwriting.
	dir, _ := devicesDir()
	if err := os.WriteFile(filepath.Join(dir, "devices.json"), []byte(`{"version":1,"devices":[]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(filepath.Join(dir, "devices.json"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := runCLI(t, "serve", "devices"); err == nil {
		t.Fatal("listed a world-readable file")
	}
}

func startServe(t *testing.T, sock string, o serveOptions) (net.Addr, context.CancelFunc, chan error) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	addrc := make(chan net.Addr, 1)
	done := make(chan error, 1)
	o.Log = discardLog()
	o.Dial = func() (*client.Client, error) { return client.Dial(sock) }
	o.Ready = func(a net.Addr) { addrc <- a }
	go func() { done <- runServe(ctx, o) }()
	select {
	case a := <-addrc:
		return a, cancel, done
	case err := <-done:
		t.Fatalf("runServe returned %v", err)
	case <-time.After(5 * time.Second):
		t.Fatal("not ready")
	}
	return nil, cancel, done
}

func TestServeAuthenticatesWithDeviceTokens(t *testing.T) {
	sock := serveTestDaemon(t)
	devDir := filepath.Join(t.TempDir(), "api")
	t.Setenv(config.EnvStateDir, filepath.Dir(devDir))
	out, _, err := runCLI(t, "serve", "devices", "add", "--name", "ci", "--scope", "read")
	if err != nil {
		t.Fatal(err)
	}
	tok := strings.TrimSpace(out)
	api := config.Default().API
	addr, cancel, done := startServe(t, sock, serveOptions{API: api, Listen: "127.0.0.1:0", DevicesDir: devDir})
	base := "http://" + addr.String()
	do := func(path, auth string) int {
		req, _ := http.NewRequest("GET", base+path, nil)
		if auth != "" {
			req.Header.Set("Authorization", auth)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}
	if c := do("/v1/info", ""); c != 401 {
		t.Errorf("no token: %d", c)
	}
	if c := do("/v1/info", "Bearer "+tok); c != 200 {
		t.Errorf("valid token: %d", c)
	}
	if c := do("/v1/info?token="+tok, "Bearer "+tok); c != 400 {
		t.Errorf("query token: %d", c)
	}
	// Revoke through the CLI while serving: effective on the next request.
	if _, _, err := runCLI(t, "serve", "devices", "revoke", "ci"); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for do("/v1/info", "Bearer "+tok) != 401 {
		if time.Now().After(deadline) {
			t.Fatal("revoked token still accepted after 3 s")
		}
		time.Sleep(100 * time.Millisecond)
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(8 * time.Second):
		t.Fatal("no shutdown")
	}
}

func TestServeBrokenStore(t *testing.T) {
	sock := serveTestDaemon(t)
	devDir := filepath.Join(t.TempDir(), "api")
	if err := os.MkdirAll(devDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(devDir, "devices.json"), []byte("{broken"), 0o600); err != nil {
		t.Fatal(err)
	}
	// Non-loopback (TLS on) refuses to start.
	cert, key := selfSignedFiles(t)
	api := config.Default().API
	api.TLS, api.TLSCert, api.TLSKey = config.APITLSFiles, cert, key
	err := runServe(context.Background(), serveOptions{API: api, Listen: "0.0.0.0:0", DevicesDir: devDir, Log: discardLog(),
		Dial: func() (*client.Client, error) { return client.Dial(sock) }})
	if err == nil || !strings.Contains(err.Error(), "device tokens") {
		t.Fatalf("err = %v", err)
	}
	// Loopback starts but denies everything except health.
	addr, cancel, done := startServe(t, sock, serveOptions{API: config.Default().API, Listen: "127.0.0.1:0", DevicesDir: devDir})
	resp, err := http.Get("http://" + addr.String() + "/v1/health")
	if err != nil || resp.StatusCode != 200 {
		t.Fatalf("health: %v %v", resp, err)
	}
	resp.Body.Close()
	cancel()
	<-done
}

func selfSignedFiles(t *testing.T) (certPath, keyPath string) {
	t.Helper()
	k, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	tpl := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "t"}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour)}
	der, _ := x509.CreateCertificate(rand.Reader, tpl, tpl, &k.PublicKey, k)
	kb, _ := x509.MarshalECPrivateKey(k)
	d := t.TempDir()
	certPath, keyPath = filepath.Join(d, "c.pem"), filepath.Join(d, "k.pem")
	_ = os.WriteFile(certPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o600)
	_ = os.WriteFile(keyPath, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: kb}), 0o600)
	return
}
