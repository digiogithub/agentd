package hcompat

import (
	"crypto/rand"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
)

// LoadCoreID returns the permanent random core id (hello.core.id) kept in
// path ($STATE/hc/core-id), creating it on first use; it changes only when
// the state is wiped. `hc connect` and the daemon's hc listener share it, so
// a client sees one core whichever way it connects.
func LoadCoreID(path string) (string, error) {
	if b, err := os.ReadFile(path); err == nil && len(strings.TrimSpace(string(b))) > 0 {
		return strings.TrimSpace(string(b)), nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return "", err
	}
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	id := hex.EncodeToString(raw[:])
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(id+"\n"), 0o600); err != nil {
		return "", err
	}
	return id, os.Rename(tmp, path)
}
