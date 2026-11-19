package remote

import (
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// RelayKey returns the private half of this install's relay device key
// (X25519, 32 bytes) from <dir>/relay_key, generating it (mode 0600) the
// first time. One key per desktop install: every relay host knows this
// device by the same public key (its device id). The file holds the key as
// base64url without padding; it is never logged.
func RelayKey(dir string) ([]byte, error) {
	keyMu.Lock()
	defer keyMu.Unlock()
	path := filepath.Join(dir, RelayKeyFile)
	data, err := os.ReadFile(path)
	if err == nil {
		key, derr := base64.RawURLEncoding.DecodeString(strings.TrimSpace(string(data)))
		if derr != nil || len(key) != 32 {
			return nil, fmt.Errorf("remote: relay key %s is not a 32-byte base64url key", path)
		}
		return key, nil
	}
	if !errors.Is(err, fs.ErrNotExist) {
		return nil, fmt.Errorf("remote: relay key: %w", err)
	}
	// An X25519 private key is 32 random bytes (clamped when used).
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return nil, fmt.Errorf("remote: generate relay key: %w", err)
	}
	if err := writeFileAtomic(path, []byte(base64.RawURLEncoding.EncodeToString(key)+"\n"), 0o600); err != nil {
		return nil, fmt.Errorf("remote: write relay key: %w", err)
	}
	return key, nil
}
