package remote

import (
	"errors"
	"fmt"
	"io/fs"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"

	"github.com/digiogithub/agentd/internal/desktopui/backend"
)

// HostKeyMismatch is returned when a host presents a key other than the one
// accepted before (Host.HostKey, the app's known_hosts or ~/.ssh/known_hosts).
// It is never overridden by the trust callback: the user must remove the old
// key on purpose.
type HostKeyMismatch struct {
	HostID string
	Addr   string   // host:port as dialled
	Got    string   // SHA256 fingerprint presented now
	Want   []string // fingerprints accepted before
}

func (e *HostKeyMismatch) Error() string {
	return fmt.Sprintf("remote: host key of %s changed: got %s, expected %s (possible man-in-the-middle; remove the old key to accept the new one)",
		e.Addr, e.Got, strings.Join(e.Want, ", "))
}

// Is makes a mismatch match backend.ErrHostKeyChanged.
func (e *HostKeyMismatch) Is(target error) bool { return target == backend.ErrHostKeyChanged }

// ErrHostKeyRejected is returned when the trust callback declines an unknown
// host key (or there is no callback). The error also carries a
// *backend.HostKeyError (errors.As) with the fingerprint to confirm.
var ErrHostKeyRejected = errors.New("remote: host key not trusted")

func rejected(host backend.Host, fp string, key ssh.PublicKey) error {
	return fmt.Errorf("%w: %w", ErrHostKeyRejected, &backend.HostKeyError{HostID: host.ID, Fingerprint: fp, KeyType: key.Type()})
}

// TrustFunc decides whether to trust an unknown host key (trust on first use).
type TrustFunc func(host backend.Host, fingerprint string) bool

// HostKeys checks host keys: Host.HostKey first, then the app's known_hosts
// and the user's known_hosts files, then the trust callback. Accepted keys are
// appended to the app's known_hosts.
type HostKeys struct {
	// AppFile is the app's known_hosts (<dir>/known_hosts).
	AppFile string
	// UserFiles are extra known_hosts files read but never written
	// (default ~/.ssh/known_hosts).
	UserFiles []string

	mu sync.Mutex
}

// NewHostKeys returns the checker for the desktop dir, reading
// ~/.ssh/known_hosts too.
func NewHostKeys(dir string) *HostKeys {
	hk := &HostKeys{AppFile: filepath.Join(dir, KnownHostsFile)}
	if home, err := os.UserHomeDir(); err == nil {
		hk.UserFiles = []string{filepath.Join(home, ".ssh", "known_hosts")}
	}
	return hk
}

// Callback returns the ssh.HostKeyCallback for host. trust decides about an
// unknown key; accepted (may be nil) is told the fingerprint of every
// accepted key so the caller can store it in Host.HostKey.
func (hk *HostKeys) Callback(host backend.Host, trust TrustFunc, accepted func(fingerprint string)) ssh.HostKeyCallback {
	return func(hostname string, remote net.Addr, key ssh.PublicKey) error {
		fp := ssh.FingerprintSHA256(key)
		if host.HostKey != "" {
			if host.HostKey != fp {
				return &HostKeyMismatch{HostID: host.ID, Addr: hostname, Got: fp, Want: []string{host.HostKey}}
			}
			return nil
		}
		known, want, err := hk.lookup(hostname, remote, key)
		if err != nil {
			return err
		}
		if known {
			if accepted != nil {
				accepted(fp)
			}
			return nil
		}
		if len(want) > 0 {
			return &HostKeyMismatch{HostID: host.ID, Addr: hostname, Got: fp, Want: want}
		}
		if trust == nil || !trust(host, fp) {
			return rejected(host, fp, key)
		}
		if err := hk.add(hostname, remote, key); err != nil {
			return err
		}
		if accepted != nil {
			accepted(fp)
		}
		return nil
	}
}

// lookup reports whether key is known for hostname; when it is not but other
// keys are, want lists their fingerprints.
func (hk *HostKeys) lookup(hostname string, remote net.Addr, key ssh.PublicKey) (known bool, want []string, err error) {
	hk.mu.Lock()
	defer hk.mu.Unlock()
	var files []string
	for _, f := range append([]string{hk.AppFile}, hk.UserFiles...) {
		if f == "" {
			continue
		}
		if _, err := os.Stat(f); err == nil {
			files = append(files, f)
		}
	}
	if len(files) == 0 {
		return false, nil, nil
	}
	cb, err := knownhosts.New(files...)
	if err != nil {
		return false, nil, fmt.Errorf("remote: read known_hosts: %w", err)
	}
	err = cb(hostname, remote, key)
	if err == nil {
		return true, nil, nil
	}
	var ke *knownhosts.KeyError
	if errors.As(err, &ke) {
		for _, w := range ke.Want {
			want = append(want, ssh.FingerprintSHA256(w.Key))
		}
		return false, want, nil
	}
	return false, nil, err
}

func (hk *HostKeys) add(hostname string, remote net.Addr, key ssh.PublicKey) error {
	hk.mu.Lock()
	defer hk.mu.Unlock()
	if err := ensureDir(filepath.Dir(hk.AppFile)); err != nil {
		return err
	}
	addrs := []string{knownhosts.Normalize(hostname)}
	if remote != nil {
		if r := knownhosts.Normalize(remote.String()); r != addrs[0] {
			addrs = append(addrs, r)
		}
	}
	f, err := os.OpenFile(hk.AppFile, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o600)
	if err != nil {
		return fmt.Errorf("remote: write known_hosts: %w", err)
	}
	_, werr := f.WriteString(knownhosts.Line(addrs, key) + "\n")
	cerr := f.Close()
	if werr != nil {
		return werr
	}
	return cerr
}

// Forget removes every line of the app's known_hosts for host:port (used
// when the user accepts a changed key on purpose). ~/.ssh/known_hosts is
// never edited.
func (hk *HostKeys) Forget(addr string, port int) error {
	hk.mu.Lock()
	defer hk.mu.Unlock()
	data, err := os.ReadFile(hk.AppFile)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if port == 0 {
		port = 22
	}
	target := knownhosts.Normalize(net.JoinHostPort(addr, strconv.Itoa(port)))
	var out []string
	for _, line := range strings.Split(string(data), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		fields := strings.Fields(line)
		drop := false
		if len(fields) > 0 {
			for _, h := range strings.Split(fields[0], ",") {
				if h == target {
					drop = true
				}
			}
		}
		if !drop {
			out = append(out, line)
		}
	}
	body := strings.Join(out, "\n")
	if body != "" {
		body += "\n"
	}
	return writeFileAtomic(hk.AppFile, []byte(body), 0o600)
}
