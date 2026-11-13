package main

import (
	"context"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/digiogithub/agentd/internal/client"
	"github.com/digiogithub/agentd/internal/config"
	"github.com/digiogithub/agentd/internal/daemon"
	"github.com/digiogithub/agentd/internal/mailbox"
)

func serveTestDaemon(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "agentd-s")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	sock := filepath.Join(dir, "agentd.sock")
	t.Setenv(config.EnvSocket, sock)
	t.Setenv(config.EnvStateDir, filepath.Join(dir, "state"))
	t.Setenv(config.EnvConfig, filepath.Join(dir, "config.toml"))
	if err := config.EnsureDirs(); err != nil {
		t.Fatal(err)
	}
	dbPath, _ := config.MailboxPath()
	store, err := mailbox.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	srv := daemon.New(config.Default(), nil, store, nil)
	if err := srv.Listen(); err != nil {
		t.Fatal(err)
	}
	go func() { _ = srv.Serve(context.Background()) }()
	t.Cleanup(srv.Shutdown)
	return sock
}

func discardLog() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func TestRunServeRefusesNonLoopbackWithoutTLS(t *testing.T) {
	sock := serveTestDaemon(t)
	dialed := false
	err := runServe(context.Background(), serveOptions{
		API: config.Default().API, Listen: "0.0.0.0:0", Log: discardLog(),
		Dial: func() (*client.Client, error) { dialed = true; return client.Dial(sock) },
	})
	if err == nil || !strings.Contains(err.Error(), "not loopback") {
		t.Fatalf("err = %v", err)
	}
	if dialed {
		t.Fatal("dialed the daemon before validating")
	}
}

func TestRunServeBadFiles(t *testing.T) {
	api := config.Default().API
	api.Listen = "0.0.0.0:0"
	api.TLS, api.TLSCert, api.TLSKey = config.APITLSFiles, "/nonexistent.pem", "/nonexistent.key"
	if err := runServe(context.Background(), serveOptions{API: api, Log: discardLog(),
		Dial: func() (*client.Client, error) { return nil, io.EOF }}); err == nil || !strings.Contains(err.Error(), "tls_cert") {
		t.Fatalf("files err = %v", err)
	}
}

func TestRunServeServesHealth(t *testing.T) {
	sock := serveTestDaemon(t)
	ctx, cancel := context.WithCancel(context.Background())
	addrc := make(chan net.Addr, 1)
	done := make(chan error, 1)
	go func() {
		done <- runServe(ctx, serveOptions{
			API: config.Default().API, Listen: "127.0.0.1:0", Log: discardLog(),
			Dial:  func() (*client.Client, error) { return client.Dial(sock) },
			Ready: func(a net.Addr) { addrc <- a },
		})
	}()
	var addr net.Addr
	select {
	case addr = <-addrc:
	case err := <-done:
		t.Fatalf("runServe returned %v", err)
	case <-time.After(5 * time.Second):
		t.Fatal("not ready")
	}
	resp, err := http.Get("http://" + addr.String() + "/v1/health")
	if err != nil || resp.StatusCode != 200 {
		t.Fatalf("health: %v %v", resp, err)
	}
	resp.Body.Close()
	// No authenticator configured: info is refused.
	resp, err = http.Get("http://" + addr.String() + "/v1/info")
	if err != nil || resp.StatusCode != 401 {
		t.Fatalf("info: %v %v", resp, err)
	}
	resp.Body.Close()
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("runServe = %v", err)
		}
	case <-time.After(8 * time.Second):
		t.Fatal("no shutdown")
	}
}

func TestServeCmdConfigError(t *testing.T) {
	p := filepath.Join(t.TempDir(), "bad.toml")
	if err := os.WriteFile(p, []byte("[api]\nlisten = \"0.0.0.0:7477\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cmd := newRootCmd()
	cmd.SetArgs([]string{"serve", "--config", p})
	cmd.SetErr(io.Discard)
	err := cmd.Execute()
	if err == nil || !strings.Contains(err.Error(), "not loopback") {
		t.Fatalf("err = %v", err)
	}
	// --listen override is validated too.
	cmd = newRootCmd()
	cmd.SetArgs([]string{"serve", "--config", filepath.Join(t.TempDir(), "none.toml"), "--listen", "0.0.0.0:7477"})
	cmd.SetErr(io.Discard)
	if err := cmd.Execute(); err == nil || !strings.Contains(err.Error(), "not loopback") {
		t.Fatalf("override err = %v", err)
	}
}

func TestRunServeRefusesPlainNotifySecretInOpenConfig(t *testing.T) {
	p := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	api := config.Default().API
	api.Notify = config.NotifyConfig{Enabled: true, WebhookURL: "https://example.com/h", WebhookSecret: "s3cret"}
	dialed := false
	err := runServe(context.Background(), serveOptions{
		API: api, ConfigPath: p, Log: discardLog(),
		Dial: func() (*client.Client, error) { dialed = true; return nil, io.EOF },
	})
	if err == nil || !strings.Contains(err.Error(), "insecure permissions") || strings.Contains(err.Error(), "s3cret") {
		t.Fatalf("err = %v", err)
	}
	if dialed {
		t.Fatal("dialed the daemon before the secret check")
	}
}
