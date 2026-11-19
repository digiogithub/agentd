package relaywire

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"strings"
)

// InviteScheme is the scheme of invite links.
const InviteScheme = "agentd"

// Invite is the content of an invite link: where the relay is, which host to
// reach, the host's pinned Noise key and the one-time code (Noise psk).
type Invite struct {
	RelayURL string // base URL of the relay (https://, wss://; http/ws only on loopback)
	Route    string
	HostKey  []byte // host X25519 public key
	ID       string // invite id (not secret)
	Code     []byte // 32-byte one-time code (secret)
	Label    string // host label shown to the user
}

// String renders the link `agentd://relay?v=1&u=…&r=…&k=…&i=…&c=…&h=…`.
func (inv Invite) String() string {
	q := url.Values{}
	q.Set("v", "1")
	q.Set("u", inv.RelayURL)
	q.Set("r", inv.Route)
	q.Set("k", B64.EncodeToString(inv.HostKey))
	q.Set("i", inv.ID)
	q.Set("c", B64.EncodeToString(inv.Code))
	if inv.Label != "" {
		q.Set("h", inv.Label)
	}
	// Fixed order keeps links stable and readable.
	var b strings.Builder
	b.WriteString(InviteScheme + "://relay?")
	for i, key := range []string{"v", "u", "r", "k", "i", "c", "h"} {
		v := q.Get(key)
		if v == "" {
			continue
		}
		if i > 0 {
			b.WriteByte('&')
		}
		b.WriteString(key + "=" + url.QueryEscape(v))
	}
	return b.String()
}

// ParseInvite parses and validates an invite link.
func ParseInvite(s string) (Invite, error) {
	u, err := url.Parse(strings.TrimSpace(s))
	if err != nil {
		return Invite{}, fmt.Errorf("invite: %w", err)
	}
	if u.Scheme != InviteScheme || u.Host != "relay" {
		return Invite{}, errors.New("invite: not an agentd relay link")
	}
	q := u.Query()
	if q.Get("v") != "1" {
		return Invite{}, errors.New("invite: unsupported version")
	}
	inv := Invite{RelayURL: q.Get("u"), Route: q.Get("r"), ID: q.Get("i"), Label: q.Get("h")}
	if err := CheckRelayURL(inv.RelayURL); err != nil {
		return Invite{}, err
	}
	if !ValidRoute(inv.Route) {
		return Invite{}, errors.New("invite: bad route")
	}
	if inv.HostKey, err = B64.DecodeString(q.Get("k")); err != nil || len(inv.HostKey) != 32 {
		return Invite{}, errors.New("invite: bad host key")
	}
	if inv.Code, err = B64.DecodeString(q.Get("c")); err != nil || len(inv.Code) != 32 {
		return Invite{}, errors.New("invite: bad code")
	}
	if !ValidInviteID(inv.ID) {
		return Invite{}, errors.New("invite: bad invite id")
	}
	if len(inv.Label) > 64 {
		return Invite{}, errors.New("invite: label too long")
	}
	return inv, nil
}

// ValidInviteID reports whether s is a 16-char lowercase hex invite id.
func ValidInviteID(s string) bool {
	if len(s) != 16 {
		return false
	}
	for _, r := range s {
		if !(r >= '0' && r <= '9' || r >= 'a' && r <= 'f') {
			return false
		}
	}
	return true
}

// CheckRelayURL accepts https/wss URLs, and http/ws only for loopback hosts,
// with no credentials, query or fragment.
func CheckRelayURL(s string) error {
	u, err := url.Parse(s)
	if err != nil || u.Host == "" {
		return errors.New("relay url: invalid")
	}
	if u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return errors.New("relay url: must not carry credentials, query or fragment")
	}
	switch u.Scheme {
	case "https", "wss":
		return nil
	case "http", "ws":
		host := u.Hostname()
		if host == "localhost" {
			return nil
		}
		if ip := net.ParseIP(host); ip != nil && ip.IsLoopback() {
			return nil
		}
		return errors.New("relay url: plain http/ws is only allowed on loopback")
	default:
		return errors.New("relay url: scheme must be https or wss")
	}
}

// Endpoint builds the WebSocket URL of an endpoint path (e.g. "/v1/connect")
// from the relay base URL.
func Endpoint(base, path string, query url.Values) (string, error) {
	u, err := url.Parse(base)
	if err != nil {
		return "", err
	}
	switch u.Scheme {
	case "https":
		u.Scheme = "wss"
	case "http":
		u.Scheme = "ws"
	}
	u.Path = strings.TrimRight(u.Path, "/") + path
	u.RawQuery = query.Encode()
	return u.String(), nil
}
