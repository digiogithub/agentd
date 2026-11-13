// Package apitoken is the device-token store of the HTTP gateway
// (docs/design/api/security-remote.md 2): token format, the 0600
// api/devices.json file shared by `agentd serve` and the CLI under an flock,
// verification (constant time), expiry, throttled last-seen and revocation.
// It depends only on the standard library and internal/httpapi (for the
// Authenticator contract); httpapi never imports it.
package apitoken

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
)

const (
	// Prefix marks tokens so secret scanners can find them.
	Prefix    = "agd_"
	idLen     = 10 // Crockford base32, 50 bits
	secretLen = 43 // 32 bytes, base64url without padding
	tokenLen  = len(Prefix) + idLen + 1 + secretLen
	crockford = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"
	b64url    = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-_"
)

// ErrBadToken is returned for a malformed token.
var ErrBadToken = errors.New("malformed token")

// newToken returns a fresh token, its public id and the hash to store.
func newToken() (token, id, hash string, err error) {
	var ib [idLen]byte
	if _, err = rand.Read(ib[:]); err != nil {
		return
	}
	idb := make([]byte, idLen)
	for i, b := range ib {
		idb[i] = crockford[b&31] // 32 symbols: uniform
	}
	var sb [32]byte
	if _, err = rand.Read(sb[:]); err != nil {
		return
	}
	secret := base64.RawURLEncoding.EncodeToString(sb[:])
	id = string(idb)
	return Prefix + id + "_" + secret, id, hashSecret(secret), nil
}

// ParseToken splits agd_<id>_<secret>. It checks only the shape.
func ParseToken(tok string) (id, secret string, err error) {
	if len(tok) != tokenLen || !strings.HasPrefix(tok, Prefix) || tok[len(Prefix)+idLen] != '_' {
		return "", "", ErrBadToken
	}
	id = tok[len(Prefix) : len(Prefix)+idLen]
	secret = tok[len(Prefix)+idLen+1:]
	if strings.Trim(id, crockford) != "" || strings.Trim(secret, b64url) != "" {
		return "", "", ErrBadToken
	}
	return id, secret, nil
}

func validID(id string) bool {
	return len(id) == idLen && strings.Trim(id, crockford) == ""
}

// hashSecret = hex(SHA-256(secret)).
func hashSecret(secret string) string {
	h := sha256.Sum256([]byte(secret))
	return hex.EncodeToString(h[:])
}

func validHash(h string) bool {
	if len(h) != 64 {
		return false
	}
	_, err := hex.DecodeString(h)
	return err == nil
}

// Scopes are cumulative: read < write < admin.
const (
	ScopeRead  = "read"
	ScopeWrite = "write"
	ScopeAdmin = "admin"
)

func validScope(s string) error {
	switch s {
	case ScopeRead, ScopeWrite, ScopeAdmin:
		return nil
	}
	return fmt.Errorf("invalid scope %q (read, write or admin)", s)
}
