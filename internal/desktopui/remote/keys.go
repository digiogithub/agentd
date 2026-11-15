package remote

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"golang.org/x/crypto/ssh"
)

// KeyComment is the comment of the app key's public line.
const KeyComment = "agentd-desktop"

var keyMu sync.Mutex

// AppKey returns the app's own ed25519 key at <dir>/id_ed25519, generating
// it (OpenSSH format, mode 0600) the first time. The public half is also
// written to id_ed25519.pub.
func AppKey(dir string) (ssh.Signer, error) {
	keyMu.Lock()
	defer keyMu.Unlock()
	path := filepath.Join(dir, KeyFile)
	data, err := os.ReadFile(path)
	if err == nil {
		s, perr := ssh.ParsePrivateKey(data)
		if perr != nil {
			return nil, fmt.Errorf("remote: app key %s: %w", path, perr)
		}
		return s, nil
	}
	if !errors.Is(err, fs.ErrNotExist) {
		return nil, fmt.Errorf("remote: app key: %w", err)
	}
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("remote: generate app key: %w", err)
	}
	block, err := ssh.MarshalPrivateKey(priv, KeyComment)
	if err != nil {
		return nil, fmt.Errorf("remote: encode app key: %w", err)
	}
	if err := writeFileAtomic(path, pem.EncodeToMemory(block), 0o600); err != nil {
		return nil, fmt.Errorf("remote: write app key: %w", err)
	}
	s, err := ssh.NewSignerFromKey(priv)
	if err != nil {
		return nil, err
	}
	_ = writeFileAtomic(path+".pub", []byte(PublicKeyLine(s.PublicKey())+"\n"), 0o644)
	return s, nil
}

// PublicKeyLine is the authorized_keys line of pub with the app comment
// ("ssh-ed25519 AAAA... agentd-desktop").
func PublicKeyLine(pub ssh.PublicKey) string {
	return strings.TrimSpace(string(ssh.MarshalAuthorizedKey(pub))) + " " + KeyComment
}

// AppPublicKey returns the app key's public line, generating the key if
// needed.
func AppPublicKey(dir string) (string, error) {
	s, err := AppKey(dir)
	if err != nil {
		return "", err
	}
	return PublicKeyLine(s.PublicKey()), nil
}

// LoadKeyFile parses a private key file; for an encrypted key it asks
// passphrase (nil = the key cannot be used).
func LoadKeyFile(path string, passphrase func(path string) ([]byte, error)) (ssh.Signer, error) {
	path = expandHome(path)
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("remote: key file: %w", err)
	}
	s, err := ssh.ParsePrivateKey(data)
	if err == nil {
		return s, nil
	}
	var missing *ssh.PassphraseMissingError
	if !errors.As(err, &missing) {
		return nil, fmt.Errorf("remote: key file %s: %w", path, err)
	}
	if passphrase == nil {
		return nil, fmt.Errorf("remote: key file %s is encrypted and no passphrase prompt is set", path)
	}
	pass, err := passphrase(path)
	if err != nil {
		return nil, err
	}
	s, err = ssh.ParsePrivateKeyWithPassphrase(data, pass)
	clear(pass)
	if err != nil {
		return nil, fmt.Errorf("remote: key file %s: %w", path, err)
	}
	return s, nil
}
