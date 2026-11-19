// Package remote reaches hosts for agentd-desktop: backend.Shell over SSH
// (golang.org/x/crypto/ssh) and on this computer, the app's own SSH key,
// trust-on-first-use host keys, the saved hosts (hosts.json), the
// ~/.ssh/config import and a per-host connection pool.
//
// State lives in the desktop directory (Dir): $AGENTD_DESKTOP_DIR, else
// $XDG_CONFIG_HOME/agentd/desktop, else ~/.config/agentd/desktop. Nothing
// secret is logged; passwords and passphrases are asked through callbacks and
// never stored.
package remote

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// EnvDir overrides the desktop state directory (tests, portable installs).
const EnvDir = "AGENTD_DESKTOP_DIR"

// File names inside the desktop directory.
const (
	HostsFile      = "hosts.json"
	KnownHostsFile = "known_hosts"
	KeyFile        = "id_ed25519"
	// RelayKeyFile is the relay device key (X25519 private half, base64url):
	// one per desktop install, shared by every relay host.
	RelayKeyFile = "relay_key"
)

// Dir returns the desktop state directory (not created).
func Dir() (string, error) {
	if d := os.Getenv(EnvDir); d != "" {
		return d, nil
	}
	if d := os.Getenv("XDG_CONFIG_HOME"); d != "" {
		return filepath.Join(d, "agentd", "desktop"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("remote: resolve desktop dir: %w", err)
	}
	return filepath.Join(home, ".config", "agentd", "desktop"), nil
}

// ensureDir creates dir with mode 0700 when missing.
func ensureDir(dir string) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("remote: create %s: %w", dir, err)
	}
	return nil
}

// writeFileAtomic writes data to path with mode perm through a temp file in
// the same directory and a rename, so readers never see a partial file.
func writeFileAtomic(path string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	if err := ensureDir(dir); err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	ok := false
	defer func() {
		if !ok {
			_ = f.Close()
			_ = os.Remove(tmp)
		}
	}()
	if err := f.Chmod(perm); err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		return err
	}
	if err := f.Sync(); err != nil {
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		return err
	}
	ok = true
	return nil
}

// expandHome expands a leading "~/" (or a lone "~") with the user's home.
func expandHome(p string) string {
	if p != "~" && !strings.HasPrefix(p, "~/") {
		return p
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return p
	}
	if p == "~" {
		return home
	}
	return filepath.Join(home, p[2:])
}

// ShellQuote quotes s for a POSIX shell (single quotes).
func ShellQuote(s string) string {
	if s == "" {
		return "''"
	}
	safe := true
	for _, r := range s {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || strings.ContainsRune("@%+=:,./_-", r)) {
			safe = false
			break
		}
	}
	if safe {
		return s
	}
	var b strings.Builder
	b.WriteByte('\'')
	for _, r := range s {
		if r == '\'' {
			b.WriteString(`'\''`)
			continue
		}
		b.WriteRune(r)
	}
	b.WriteByte('\'')
	return b.String()
}
