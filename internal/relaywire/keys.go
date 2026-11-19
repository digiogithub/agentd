// Package relaywire is the wire format of the relay transport (design
// docs/design/relay-transport.md, plan docs/plans/agentd/relay_plan.md): host and
// device keys, route and device ids, the invite link, the Noise handshake that
// runs end to end between a client and a daemon through agentd-relay, and the
// stream multiplexer carried inside the encrypted tunnel. It does no file I/O
// and knows nothing about the daemon.
package relaywire

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base32"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/flynn/noise"
	"golang.org/x/crypto/curve25519"
)

// Protocol identifiers.
const (
	// Subprotocol is the WebSocket subprotocol of every relay endpoint.
	Subprotocol = "agentd-relay.v1"
	// RegisterPrefix is signed by the host, followed by the relay's nonce.
	RegisterPrefix = "agentd-relay/1 register "
	// ProloguePrefix starts the Noise prologue; the route id follows.
	ProloguePrefix = "agentd-relay/1\n"
)

// B64 is the base64 flavour used everywhere in the protocol (URL safe, no padding).
var B64 = base64.RawURLEncoding

var b32 = base32.StdEncoding.WithPadding(base32.NoPadding)

// RouteLen is the length of a route id.
const RouteLen = 26

// RouteID derives the relay route of a host from its ed25519 public key.
func RouteID(signPub ed25519.PublicKey) string {
	sum := sha256.Sum256(signPub)
	return strings.ToLower(b32.EncodeToString(sum[:]))[:RouteLen]
}

// ValidRoute reports whether s has the shape of a route id.
func ValidRoute(s string) bool {
	if len(s) != RouteLen {
		return false
	}
	for _, r := range s {
		if !(r >= 'a' && r <= 'z' || r >= '2' && r <= '7') {
			return false
		}
	}
	return true
}

// DeviceID derives the id of a paired device from its X25519 public key.
func DeviceID(pub []byte) string {
	sum := sha256.Sum256(pub)
	return "d" + strings.ToLower(b32.EncodeToString(sum[:]))[:12]
}

// HostKey is a host's long-lived identity: an ed25519 key that registers the
// route on the relay and an X25519 key that is the Noise static key clients pin.
type HostKey struct {
	Sign ed25519.PrivateKey
	DH   noise.DHKey
}

// Route returns the host's route id.
func (k HostKey) Route() string { return RouteID(k.Sign.Public().(ed25519.PublicKey)) }

// GenerateHostKey makes a new host key.
func GenerateHostKey() (HostKey, error) {
	_, sign, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return HostKey{}, err
	}
	dh, err := GenerateDHKey()
	if err != nil {
		return HostKey{}, err
	}
	return HostKey{Sign: sign, DH: dh}, nil
}

// GenerateDHKey makes an X25519 key pair (a device key, or the host's DH half).
func GenerateDHKey() (noise.DHKey, error) {
	return noise.DH25519.GenerateKeypair(rand.Reader)
}

// DHKeyFromPrivate rebuilds an X25519 key pair from its private half.
func DHKeyFromPrivate(priv []byte) (noise.DHKey, error) {
	if len(priv) != 32 {
		return noise.DHKey{}, errors.New("relay: x25519 private key must be 32 bytes")
	}
	pub, err := curve25519.X25519(priv, curve25519.Basepoint)
	if err != nil {
		return noise.DHKey{}, err
	}
	return noise.DHKey{Private: append([]byte(nil), priv...), Public: pub}, nil
}

type hostKeyJSON struct {
	Ed25519Seed   string `json:"ed25519_seed"`
	X25519Private string `json:"x25519_private"`
}

// MarshalJSON stores the private halves only.
func (k HostKey) MarshalJSON() ([]byte, error) {
	return json.Marshal(hostKeyJSON{
		Ed25519Seed:   B64.EncodeToString(k.Sign.Seed()),
		X25519Private: B64.EncodeToString(k.DH.Private),
	})
}

// UnmarshalJSON is the inverse of MarshalJSON.
func (k *HostKey) UnmarshalJSON(b []byte) error {
	var j hostKeyJSON
	if err := json.Unmarshal(b, &j); err != nil {
		return err
	}
	seed, err := B64.DecodeString(j.Ed25519Seed)
	if err != nil || len(seed) != ed25519.SeedSize {
		return errors.New("relay: bad ed25519 seed in host key")
	}
	priv, err := B64.DecodeString(j.X25519Private)
	if err != nil {
		return fmt.Errorf("relay: bad x25519 key in host key: %w", err)
	}
	dh, err := DHKeyFromPrivate(priv)
	if err != nil {
		return err
	}
	k.Sign = ed25519.NewKeyFromSeed(seed)
	k.DH = dh
	return nil
}

// RegisterMessage is what the host signs to register its route.
func RegisterMessage(nonce []byte) []byte {
	return append([]byte(RegisterPrefix), nonce...)
}

// Prologue is the Noise prologue for a tunnel to route.
func Prologue(route string) []byte { return []byte(ProloguePrefix + route) }
