// Package tlsutil provides the TLS material of the HTTP gateway (`agentd
// serve`): a persisted self-signed ECDSA P-256 certificate for the
// "self-signed" mode, certificate/key files with live reload for the "files"
// mode (`tailscale cert` output), and the SPKI pin the pairing QR carries.
// Standard library only; it does not know the config package.
//
// Policy: TLS 1.2 minimum (older Android and embedded clients), restricted to
// ECDHE + AEAD suites; TLS 1.3 is negotiated whenever the client supports it.
package tlsutil

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

// Modes (mirror config.APITLS*).
const (
	ModeOff        = "off"
	ModeSelfSigned = "self-signed"
	ModeFiles      = "files"
)

const (
	// Validity of generated certificates: trust comes from the pin, not
	// from the expiry; rotation is explicit (`agentd serve tls rotate`).
	Validity = 5 * 365 * 24 * time.Hour
	// CertFile and KeyFile are the names under the TLS directory.
	CertFile = "cert.pem"
	KeyFile  = "key.pem"

	reloadCheckEvery = time.Second
)

// Config selects and parameterises a mode.
type Config struct {
	// Dir holds cert.pem/key.pem for self-signed ("<state>/api/tls").
	Dir string
	// Cert and Key are the files of the "files" mode.
	Cert, Key string
	// Hosts are extra SANs (DNS names or IPs) of the self-signed cert.
	Hosts []string

	// Test hooks; zero values use the real hostname, interfaces and clock.
	Hostname string
	Addrs    func() ([]net.Addr, error)
	Now      func() time.Time
}

func (c Config) now() time.Time {
	if c.Now != nil {
		return c.Now()
	}
	return time.Now()
}

// Info describes a certificate for `agentd serve tls show`.
type Info struct {
	Pin       string
	DNSNames  []string
	IPs       []string
	NotBefore time.Time
	NotAfter  time.Time
	Path      string
}

// Pin is "sha256:<hex>" of the certificate's SubjectPublicKeyInfo: the
// format of the pairing QR `fp` parameter ([[design/api/frontend-app]] 4.2).
func Pin(cert *x509.Certificate) string {
	sum := sha256.Sum256(cert.RawSubjectPublicKeyInfo)
	return "sha256:" + hex.EncodeToString(sum[:])
}

// PinBase64URL is the same digest as unpadded base64url, for clients that
// prefer it (HPKP-style pins).
func PinBase64URL(cert *x509.Certificate) string {
	sum := sha256.Sum256(cert.RawSubjectPublicKeyInfo)
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

// Source serves a certificate and can be reloaded.
type Source struct {
	cfg  Config
	mode string

	mu       sync.Mutex
	cert     *tls.Certificate
	leaf     *x509.Certificate
	stamp    [2]time.Time // mtimes of cert and key (files mode)
	lastStat time.Time
}

// Open prepares the TLS source for mode. Off returns (nil, nil).
func Open(mode string, cfg Config) (*Source, error) {
	s := &Source{cfg: cfg, mode: mode}
	switch mode {
	case ModeOff:
		return nil, nil
	case ModeSelfSigned:
		if err := s.loadOrGenerate(); err != nil {
			return nil, err
		}
	case ModeFiles:
		if err := s.loadFiles(); err != nil {
			return nil, err
		}
	default:
		return nil, fmt.Errorf("tlsutil: unknown mode %q", mode)
	}
	return s, nil
}

// Load is Open reduced to what serve needs: the tls.Config (nil for off) and
// the pin of the current certificate.
func Load(mode string, cfg Config) (*tls.Config, string, error) {
	s, err := Open(mode, cfg)
	if err != nil || s == nil {
		return nil, "", err
	}
	return s.TLSConfig(), s.Pin(), nil
}

// CurrentPin returns the pin the gateway presents for mode without serving:
// "" for off; for self-signed the persisted certificate (generated if none
// exists yet); for files the current files. Used by the pairing code.
func CurrentPin(mode string, cfg Config) (string, error) {
	s, err := Open(mode, cfg)
	if err != nil || s == nil {
		return "", err
	}
	return s.Pin(), nil
}

// TLSConfig returns a server config whose certificate follows Reload and,
// in files mode, file changes (checked at most once a second per handshake).
func (s *Source) TLSConfig() *tls.Config {
	return &tls.Config{
		MinVersion:     tls.VersionTLS12,
		CipherSuites:   cipherSuites,
		GetCertificate: s.getCertificate,
	}
}

var cipherSuites = []uint16{
	tls.TLS_ECDHE_ECDSA_WITH_AES_128_GCM_SHA256,
	tls.TLS_ECDHE_ECDSA_WITH_AES_256_GCM_SHA384,
	tls.TLS_ECDHE_ECDSA_WITH_CHACHA20_POLY1305_SHA256,
	tls.TLS_ECDHE_RSA_WITH_AES_128_GCM_SHA256,
	tls.TLS_ECDHE_RSA_WITH_AES_256_GCM_SHA384,
	tls.TLS_ECDHE_RSA_WITH_CHACHA20_POLY1305_SHA256,
}

func (s *Source) getCertificate(*tls.ClientHelloInfo) (*tls.Certificate, error) {
	if s.mode == ModeFiles {
		s.maybeReload()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.cert, nil
}

// Pin is the pin of the certificate currently served.
func (s *Source) Pin() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return Pin(s.leaf)
}

// Info describes the certificate currently served.
func (s *Source) Info() Info {
	s.mu.Lock()
	defer s.mu.Unlock()
	return infoOf(s.leaf, s.path())
}

func (s *Source) path() string {
	if s.mode == ModeFiles {
		return s.cfg.Cert
	}
	return filepath.Join(s.cfg.Dir, CertFile)
}

// Reload re-reads the files (files mode). On error the previous certificate
// stays in use. Self-signed sources have nothing to reload.
func (s *Source) Reload() error {
	if s.mode != ModeFiles {
		return nil
	}
	return s.loadFiles()
}

func (s *Source) maybeReload() {
	s.mu.Lock()
	if s.cfg.now().Sub(s.lastStat) < reloadCheckEvery {
		s.mu.Unlock()
		return
	}
	s.lastStat = s.cfg.now()
	stamp := s.stamp
	s.mu.Unlock()
	if stamp == statStamp(s.cfg.Cert, s.cfg.Key) {
		return
	}
	_ = s.loadFiles() // keep serving the old certificate if the new pair is broken or half-written
}

func statStamp(files ...string) (st [2]time.Time) {
	for i, f := range files {
		if fi, err := os.Stat(f); err == nil {
			st[i] = fi.ModTime()
		}
	}
	return st
}

func (s *Source) loadFiles() error {
	stamp := statStamp(s.cfg.Cert, s.cfg.Key)
	cert, err := tls.LoadX509KeyPair(s.cfg.Cert, s.cfg.Key)
	if err != nil {
		return fmt.Errorf("api.tls_cert/tls_key: %w", err)
	}
	leaf, err := x509.ParseCertificate(cert.Certificate[0])
	if err != nil {
		return err
	}
	cert.Leaf = leaf
	s.mu.Lock()
	s.cert, s.leaf, s.stamp = &cert, leaf, stamp
	s.mu.Unlock()
	return nil
}

func (s *Source) loadOrGenerate() error {
	cert, leaf, err := readPair(filepath.Join(s.cfg.Dir, CertFile), filepath.Join(s.cfg.Dir, KeyFile))
	if err == nil && s.cfg.now().Before(leaf.NotAfter) {
		s.cert, s.leaf = cert, leaf
		return nil
	}
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("tls state %s is unusable (%v): fix or run `agentd serve tls rotate`", s.cfg.Dir, err)
	}
	// Missing (first start) or expired: generate.
	cert, leaf, err = Generate(s.cfg)
	if err != nil {
		return err
	}
	s.cert, s.leaf = cert, leaf
	return nil
}

func readPair(certPath, keyPath string) (*tls.Certificate, *x509.Certificate, error) {
	if _, err := os.Stat(certPath); err != nil {
		return nil, nil, err
	}
	if _, err := os.Stat(keyPath); err != nil {
		return nil, nil, err
	}
	cert, err := tls.LoadX509KeyPair(certPath, keyPath)
	if err != nil {
		return nil, nil, err
	}
	leaf, err := x509.ParseCertificate(cert.Certificate[0])
	if err != nil {
		return nil, nil, err
	}
	cert.Leaf = leaf
	return &cert, leaf, nil
}

// SANs returns the DNS names and IPs of a new self-signed certificate:
// hostname, localhost, loopback, every interface address, plus cfg.Hosts.
func SANs(cfg Config) (dns []string, ips []net.IP) {
	seenD, seenI := map[string]bool{}, map[string]bool{}
	addD := func(n string) {
		if n != "" && !seenD[n] {
			seenD[n] = true
			dns = append(dns, n)
		}
	}
	addI := func(ip net.IP) {
		if ip != nil && !seenI[ip.String()] {
			seenI[ip.String()] = true
			ips = append(ips, ip)
		}
	}
	host := cfg.Hostname
	if host == "" {
		host, _ = os.Hostname()
	}
	addD(host)
	addD("localhost")
	addI(net.IPv4(127, 0, 0, 1))
	addI(net.IPv6loopback)
	addrs := cfg.Addrs
	if addrs == nil {
		addrs = net.InterfaceAddrs
	}
	if as, err := addrs(); err == nil {
		for _, a := range as {
			switch v := a.(type) {
			case *net.IPNet:
				addI(v.IP)
			case *net.IPAddr:
				addI(v.IP)
			}
		}
	}
	for _, h := range cfg.Hosts {
		if ip := net.ParseIP(h); ip != nil {
			addI(ip)
		} else {
			addD(h)
		}
	}
	return dns, ips
}

// Generate creates a new key and self-signed certificate and persists them
// under cfg.Dir (dir 0700, key 0600), replacing any existing pair. The pin
// changes: paired devices must re-pair or confirm the new pin.
func Generate(cfg Config) (*tls.Certificate, *x509.Certificate, error) {
	if cfg.Dir == "" {
		return nil, nil, errors.New("tlsutil: no TLS directory")
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, err
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 127))
	if err != nil {
		return nil, nil, err
	}
	dns, ips := SANs(cfg)
	now := cfg.now()
	tmpl := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: "agentd gateway", Organization: []string{"agentd"}},
		NotBefore:             now.Add(-time.Hour),
		NotAfter:              now.Add(Validity),
		KeyUsage:              x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		DNSNames:              dns,
		IPAddresses:           ips,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return nil, nil, err
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return nil, nil, err
	}
	if err := os.MkdirAll(cfg.Dir, 0o700); err != nil {
		return nil, nil, err
	}
	if err := os.Chmod(cfg.Dir, 0o700); err != nil {
		return nil, nil, err
	}
	// Key first: a cert without its key is detected as unusable, never the
	// other way around.
	if err := writeAtomic(filepath.Join(cfg.Dir, KeyFile), pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER}), 0o600); err != nil {
		return nil, nil, err
	}
	if err := writeAtomic(filepath.Join(cfg.Dir, CertFile), pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o644); err != nil {
		return nil, nil, err
	}
	leaf, err := x509.ParseCertificate(der)
	if err != nil {
		return nil, nil, err
	}
	return &tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key, Leaf: leaf}, leaf, nil
}

func writeAtomic(path string, data []byte, perm os.FileMode) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".tmp-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name)
	if err := tmp.Chmod(perm); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(name, path)
}

// Describe reads the certificate at path (PEM) without needing its key.
func Describe(path string) (Info, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return Info{}, err
	}
	blk, _ := pem.Decode(b)
	if blk == nil {
		return Info{}, fmt.Errorf("%s: no PEM certificate", path)
	}
	leaf, err := x509.ParseCertificate(blk.Bytes)
	if err != nil {
		return Info{}, err
	}
	return infoOf(leaf, path), nil
}

func infoOf(leaf *x509.Certificate, path string) Info {
	in := Info{Pin: Pin(leaf), DNSNames: append([]string(nil), leaf.DNSNames...), NotBefore: leaf.NotBefore, NotAfter: leaf.NotAfter, Path: path}
	for _, ip := range leaf.IPAddresses {
		in.IPs = append(in.IPs, ip.String())
	}
	sort.Strings(in.IPs)
	return in
}

// Verifier returns a tls.Config.VerifyPeerCertificate callback that accepts
// only a leaf whose pin equals want ("sha256:<hex>"), the way the app pins.
// Use it with InsecureSkipVerify: true (the pin is the only trust anchor).
func Verifier(want string) func([][]byte, [][]*x509.Certificate) error {
	return func(raw [][]byte, _ [][]*x509.Certificate) error {
		if len(raw) == 0 {
			return errors.New("no server certificate")
		}
		leaf, err := x509.ParseCertificate(raw[0])
		if err != nil {
			return err
		}
		if got := Pin(leaf); got != want {
			return fmt.Errorf("TLS pin mismatch: got %s, want %s", got, want)
		}
		return nil
	}
}
