package relaywire

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sync"

	"github.com/flynn/noise"
)

// MsgConn carries whole messages (a WebSocket in production, a pipe in tests).
type MsgConn interface {
	ReadMsg(ctx context.Context) ([]byte, error)
	WriteMsg(ctx context.Context, b []byte) error
	Close() error
}

// MaxMsg is the largest message a tunnel carries (a Noise message).
const MaxMsg = noise.MaxMsgLen

// CipherSuite is Noise 25519 / ChaChaPoly / SHA256.
var CipherSuite = noise.NewCipherSuite(noise.DH25519, noise.CipherChaChaPoly, noise.HashSHA256)

// Handshake modes (Hello.Mode).
const (
	ModeConnect = "connect"
	ModePair    = "pair"
)

// Hello is the payload of the first handshake message (client → host).
type Hello struct {
	V        int    `json:"v"`
	Mode     string `json:"mode"`
	Invite   string `json:"invite,omitempty"`
	Name     string `json:"name,omitempty"`
	Platform string `json:"platform,omitempty"`
}

// Device describes a paired device as the host reports it.
type Device struct {
	ID       string   `json:"id"`
	Name     string   `json:"name,omitempty"`
	Scope    string   `json:"scope"`
	Sessions []string `json:"sessions,omitempty"`
	Kind     string   `json:"kind,omitempty"`
}

// Welcome is the payload of the second handshake message (host → client).
type Welcome struct {
	V       int     `json:"v"`
	OK      bool    `json:"ok"`
	Pending bool    `json:"pending,omitempty"`
	Host    string  `json:"host,omitempty"`
	Device  *Device `json:"device,omitempty"`
	Code    string  `json:"code,omitempty"`
	Error   string  `json:"error,omitempty"`
}

// PairMsg is a transport message of the pairing exchange.
type PairMsg struct {
	T      string  `json:"t"` // confirm | paired | denied
	Device *Device `json:"device,omitempty"`
	Host   string  `json:"host,omitempty"`
	Error  string  `json:"error,omitempty"`
}

// RemoteError is a refusal reported by the host in its Welcome.
type RemoteError struct{ Code, Msg string }

func (e *RemoteError) Error() string {
	if e.Code != "" {
		return fmt.Sprintf("relay host refused: %s (%s)", e.Msg, e.Code)
	}
	return "relay host refused: " + e.Msg
}

// Secure is an established Noise transport over a MsgConn. Send is safe for
// concurrent use; Recv must be called from one goroutine.
type Secure struct {
	c    MsgConn
	wmu  sync.Mutex
	send *noise.CipherState
	recv *noise.CipherState
}

// Send encrypts p (<= MaxMsg-16 bytes) and writes it as one message.
func (s *Secure) Send(ctx context.Context, p []byte) error {
	s.wmu.Lock()
	defer s.wmu.Unlock()
	ct, err := s.send.Encrypt(nil, nil, p)
	if err != nil {
		return err
	}
	return s.c.WriteMsg(ctx, ct)
}

// Recv reads and decrypts one message.
func (s *Secure) Recv(ctx context.Context) ([]byte, error) {
	ct, err := s.c.ReadMsg(ctx)
	if err != nil {
		return nil, err
	}
	p, err := s.recv.Decrypt(nil, nil, ct)
	if err != nil {
		return nil, fmt.Errorf("relay: decrypt: %w", err)
	}
	return p, nil
}

// SendJSON sends v as a JSON transport message.
func (s *Secure) SendJSON(ctx context.Context, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return s.Send(ctx, b)
}

// RecvJSON receives one JSON transport message into v.
func (s *Secure) RecvJSON(ctx context.Context, v any) error {
	b, err := s.Recv(ctx)
	if err != nil {
		return err
	}
	return json.Unmarshal(b, v)
}

// Close closes the underlying connection.
func (s *Secure) Close() error { return s.c.Close() }

func config(route string, initiator, psk bool) noise.Config {
	cfg := noise.Config{
		CipherSuite: CipherSuite,
		Pattern:     noise.HandshakeIK,
		Initiator:   initiator,
		Prologue:    Prologue(route),
	}
	if psk {
		// Placement 2 = IKpsk2. A placeholder key marks the handshake as a psk
		// one; the responder sets the real key once it knows the invite.
		cfg.PresharedKey = make([]byte, 32)
		cfg.PresharedKeyPlacement = 2
	}
	return cfg
}

// ClientHandshake runs the initiator side. psk is the invite code for a
// pairing (Hello.Mode == ModePair) and nil for a paired device. rng may be nil
// (crypto/rand); tests pass a fixed source to generate vectors.
func ClientHandshake(ctx context.Context, c MsgConn, route string, hostPub []byte, dev noise.DHKey, hello Hello, psk []byte, rng io.Reader) (*Secure, Welcome, error) {
	cfg := config(route, true, psk != nil)
	if psk != nil {
		cfg.PresharedKey = psk
	}
	cfg.StaticKeypair = dev
	cfg.PeerStatic = hostPub
	cfg.Random = rng
	hs, err := noise.NewHandshakeState(cfg)
	if err != nil {
		return nil, Welcome{}, err
	}
	hello.V = 1
	payload, _ := json.Marshal(hello)
	msg1, _, _, err := hs.WriteMessage(nil, payload)
	if err != nil {
		return nil, Welcome{}, err
	}
	if err := c.WriteMsg(ctx, msg1); err != nil {
		return nil, Welcome{}, err
	}
	msg2, err := c.ReadMsg(ctx)
	if err != nil {
		return nil, Welcome{}, err
	}
	out, cs1, cs2, err := hs.ReadMessage(nil, msg2)
	if err != nil {
		if psk != nil {
			return nil, Welcome{}, errors.New("relay: handshake failed (wrong, used or expired invite?)")
		}
		return nil, Welcome{}, fmt.Errorf("relay: handshake failed: %w", err)
	}
	var w Welcome
	if err := json.Unmarshal(out, &w); err != nil {
		return nil, Welcome{}, fmt.Errorf("relay: bad welcome: %w", err)
	}
	if !w.OK {
		return nil, w, &RemoteError{Code: w.Code, Msg: w.Error}
	}
	return &Secure{c: c, send: cs1, recv: cs2}, w, nil
}

// Decision is the host's answer to a Hello, given by the Accept callback.
type Decision struct {
	PSK     []byte  // pairing: the invite code (required when Hello.Mode == ModePair)
	Welcome Welcome // OK false = refuse (sent to the client, then closed)
}

// ServerHandshake runs the responder side. msg1 is tried as IK first and then
// as IKpsk2 (an invite). decide receives the client's static key and Hello
// (with the mode it was actually encrypted with already checked) and returns
// the answer. A refused handshake returns a *RemoteError after sending it.
func ServerHandshake(ctx context.Context, c MsgConn, route string, host noise.DHKey, rng io.Reader, decide func(clientPub []byte, h Hello) Decision) (*Secure, []byte, Hello, error) {
	msg1, err := c.ReadMsg(ctx)
	if err != nil {
		return nil, nil, Hello{}, err
	}
	var (
		hs    *noise.HandshakeState
		hello Hello
		isPSK bool
	)
	for _, psk := range []bool{false, true} {
		cfg := config(route, false, psk)
		cfg.StaticKeypair = host
		cfg.Random = rng
		cand, err := noise.NewHandshakeState(cfg)
		if err != nil {
			return nil, nil, Hello{}, err
		}
		out, _, _, err := cand.ReadMessage(nil, msg1)
		if err != nil {
			continue
		}
		var h Hello
		if json.Unmarshal(out, &h) != nil {
			continue
		}
		hs, hello, isPSK = cand, h, psk
		break
	}
	if hs == nil {
		return nil, nil, Hello{}, errors.New("relay: bad handshake message")
	}
	clientPub := append([]byte(nil), hs.PeerStatic()...)
	var d Decision
	switch {
	case hello.V != 1:
		d.Welcome = Welcome{Code: "invalid", Error: "unsupported protocol version"}
	case isPSK != (hello.Mode == ModePair):
		d.Welcome = Welcome{Code: "invalid", Error: "mode does not match the handshake"}
	default:
		d = decide(clientPub, hello)
	}
	d.Welcome.V = 1
	if d.Welcome.OK && isPSK {
		if err := hs.SetPresharedKey(d.PSK); err != nil {
			d.Welcome = Welcome{V: 1, Code: "invalid", Error: "invalid invite"}
		}
	}
	if !d.Welcome.OK && isPSK {
		// Without a valid psk the client could not read msg2 anyway: just close.
		_ = c.Close()
		return nil, clientPub, hello, &RemoteError{Code: d.Welcome.Code, Msg: d.Welcome.Error}
	}
	payload, _ := json.Marshal(d.Welcome)
	msg2, cs1, cs2, err := hs.WriteMessage(nil, payload)
	if err != nil {
		return nil, clientPub, hello, err
	}
	if err := c.WriteMsg(ctx, msg2); err != nil {
		return nil, clientPub, hello, err
	}
	if !d.Welcome.OK {
		_ = c.Close()
		return nil, clientPub, hello, &RemoteError{Code: d.Welcome.Code, Msg: d.Welcome.Error}
	}
	// Responder: cs1 decrypts (client → host), cs2 encrypts.
	return &Secure{c: c, send: cs2, recv: cs1}, clientPub, hello, nil
}
