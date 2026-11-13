package tlsutil

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func fakeAddrs() ([]net.Addr, error) {
	return []net.Addr{&net.IPNet{IP: net.ParseIP("192.168.1.20"), Mask: net.CIDRMask(24, 32)}}, nil
}

func testCfg(dir string) Config {
	return Config{Dir: dir, Hostname: "myhost", Addrs: fakeAddrs, Hosts: []string{"agent.example.net", "10.9.8.7"}}
}

func TestGeneratePersistReloadSamePin(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "api", "tls")
	cfg := testCfg(dir)
	_, pin, err := Load(ModeSelfSigned, cfg)
	if err != nil || !strings.HasPrefix(pin, "sha256:") || len(pin) != len("sha256:")+64 {
		t.Fatalf("pin %q err %v", pin, err)
	}
	_, pin2, err := Load(ModeSelfSigned, cfg)
	if err != nil || pin2 != pin {
		t.Fatalf("reload pin %q != %q (%v)", pin2, pin, err)
	}
	if cp, err := CurrentPin(ModeSelfSigned, cfg); err != nil || cp != pin {
		t.Fatalf("CurrentPin %q %v", cp, err)
	}
	// Perms.
	if fi, _ := os.Stat(dir); fi.Mode().Perm() != 0o700 {
		t.Errorf("dir perm %v", fi.Mode().Perm())
	}
	if fi, _ := os.Stat(filepath.Join(dir, KeyFile)); fi.Mode().Perm() != 0o600 {
		t.Errorf("key perm %v", fi.Mode().Perm())
	}
	// SANs.
	in, err := Describe(filepath.Join(dir, CertFile))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"myhost", "localhost", "agent.example.net"} {
		if !contains(in.DNSNames, want) {
			t.Errorf("DNS SANs %v lack %s", in.DNSNames, want)
		}
	}
	for _, want := range []string{"127.0.0.1", "::1", "192.168.1.20", "10.9.8.7"} {
		if !contains(in.IPs, want) {
			t.Errorf("IP SANs %v lack %s", in.IPs, want)
		}
	}
	if in.NotAfter.Sub(time.Now()) < 4*365*24*time.Hour {
		t.Errorf("validity too short: %v", in.NotAfter)
	}
	if in.Pin != pin {
		t.Errorf("Describe pin %q != %q", in.Pin, pin)
	}
}

func contains(l []string, s string) bool {
	for _, v := range l {
		if v == s {
			return true
		}
	}
	return false
}

func TestRotateChangesPin(t *testing.T) {
	cfg := testCfg(filepath.Join(t.TempDir(), "tls"))
	_, pin, _ := Load(ModeSelfSigned, cfg)
	if _, _, err := Generate(cfg); err != nil {
		t.Fatal(err)
	}
	_, pin2, err := Load(ModeSelfSigned, cfg)
	if err != nil || pin2 == pin {
		t.Fatalf("pin unchanged after rotate: %q %v", pin2, err)
	}
}

func TestExpiredIsRegeneratedAndBrokenIsAnError(t *testing.T) {
	cfg := testCfg(filepath.Join(t.TempDir(), "tls"))
	_, pin, _ := Load(ModeSelfSigned, cfg)
	cfg.Now = func() time.Time { return time.Now().Add(Validity + 24*time.Hour) }
	if _, pin2, err := Load(ModeSelfSigned, cfg); err != nil || pin2 == pin {
		t.Fatalf("expired cert not regenerated: %v", err)
	}
	cfg = testCfg(filepath.Join(t.TempDir(), "tls"))
	_ = os.MkdirAll(cfg.Dir, 0o700)
	_ = os.WriteFile(filepath.Join(cfg.Dir, CertFile), []byte("junk"), 0o644)
	_ = os.WriteFile(filepath.Join(cfg.Dir, KeyFile), []byte("junk"), 0o600)
	if _, _, err := Load(ModeSelfSigned, cfg); err == nil {
		t.Fatal("corrupt state accepted (would silently change the pin)")
	}
}

func TestOffAndUnknown(t *testing.T) {
	if c, p, err := Load(ModeOff, Config{}); c != nil || p != "" || err != nil {
		t.Fatalf("off: %v %q %v", c, p, err)
	}
	if _, _, err := Load("bogus", Config{}); err == nil {
		t.Fatal("unknown mode accepted")
	}
}

// writeCert writes a self-signed cert/key pair and returns the pin.
func writeCert(t *testing.T, dir, name string) (certPath, keyPath, pin string) {
	t.Helper()
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(time.Now().UnixNano()), Subject: pkix.Name{CommonName: name},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(24 * time.Hour),
		DNSNames: []string{name}, IPAddresses: []net.IP{net.IPv4(127, 0, 0, 1)},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	kd, _ := x509.MarshalPKCS8PrivateKey(key)
	certPath, keyPath = filepath.Join(dir, "c.pem"), filepath.Join(dir, "k.pem")
	if err := os.WriteFile(certPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keyPath, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: kd}), 0o600); err != nil {
		t.Fatal(err)
	}
	leaf, _ := x509.ParseCertificate(der)
	return certPath, keyPath, Pin(leaf)
}

func TestFilesModeAndReload(t *testing.T) {
	dir := t.TempDir()
	c, k, pin := writeCert(t, dir, "one.ts.net")
	now := time.Now()
	cfg := Config{Cert: c, Key: k, Now: func() time.Time { return now }}
	src, err := Open(ModeFiles, cfg)
	if err != nil || src.Pin() != pin {
		t.Fatalf("files pin %v %v", src, err)
	}
	if got, err := CurrentPin(ModeFiles, cfg); err != nil || got != pin {
		t.Fatalf("CurrentPin %q %v", got, err)
	}
	// Renewal: new pair on disk, picked up by the handshake path after the
	// recheck interval (mtime change) and by explicit Reload.
	time.Sleep(20 * time.Millisecond)
	_, _, pin2 := writeCert(t, dir, "one.ts.net")
	now = now.Add(2 * time.Second)
	crt, err := src.TLSConfig().GetCertificate(nil)
	if err != nil {
		t.Fatal(err)
	}
	leaf, _ := x509.ParseCertificate(crt.Certificate[0])
	if Pin(leaf) != pin2 || src.Pin() != pin2 {
		t.Fatalf("not reloaded: %s vs %s", Pin(leaf), pin2)
	}
	// A broken replacement keeps the old certificate.
	_ = os.WriteFile(k, []byte("junk"), 0o600)
	if err := src.Reload(); err == nil {
		t.Fatal("broken reload succeeded")
	}
	if src.Pin() != pin2 {
		t.Fatal("pin changed after failed reload")
	}
	if _, err := Open(ModeFiles, Config{Cert: "/nope", Key: "/nope"}); err == nil {
		t.Fatal("missing files accepted")
	}
}

func TestPinnedClientAndVersions(t *testing.T) {
	cfg := testCfg(filepath.Join(t.TempDir(), "tls"))
	tc, pin, err := Load(ModeSelfSigned, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if tc.MinVersion != tls.VersionTLS12 {
		t.Fatalf("min version %x", tc.MinVersion)
	}
	ln, err := tls.Listen("tcp", "127.0.0.1:0", tc)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() { _ = c.(*tls.Conn).Handshake(); c.Close() }()
		}
	}()
	dial := func(want string, max uint16) error {
		c, err := tls.Dial("tcp", ln.Addr().String(), &tls.Config{
			InsecureSkipVerify: true, VerifyPeerCertificate: Verifier(want), MaxVersion: max,
		})
		if err == nil {
			c.Close()
		}
		return err
	}
	if err := dial(pin, 0); err != nil {
		t.Fatalf("right pin: %v", err)
	}
	if err := dial(pin, tls.VersionTLS12); err != nil {
		t.Fatalf("TLS 1.2 client: %v", err)
	}
	if err := dial("sha256:"+strings.Repeat("a", 64), 0); err == nil {
		t.Fatal("wrong pin accepted")
	}
	// TLS 1.1 is refused.
	if c, err := tls.Dial("tcp", ln.Addr().String(), &tls.Config{InsecureSkipVerify: true, MaxVersion: tls.VersionTLS11}); err == nil {
		c.Close()
		t.Fatal("TLS 1.1 accepted")
	}
}

func TestPinBase64URL(t *testing.T) {
	cfg := testCfg(filepath.Join(t.TempDir(), "tls"))
	if _, _, err := Load(ModeSelfSigned, cfg); err != nil {
		t.Fatal(err)
	}
	_, leaf, err := readPair(filepath.Join(cfg.Dir, CertFile), filepath.Join(cfg.Dir, KeyFile))
	if err != nil {
		t.Fatal(err)
	}
	if b := PinBase64URL(leaf); len(b) != 43 || strings.ContainsAny(b, "=+/") {
		t.Fatalf("base64url pin %q", b)
	}
}
