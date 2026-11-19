package config

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"strings"
)

// Relay scopes, weakest first (design docs/design/relay-transport.md §5).
const (
	RelayScopeView    = "view"
	RelayScopeOperate = "operate"
	RelayScopeAdmin   = "admin"
)

// RelayScopeRank orders scopes (0 = unknown).
func RelayScopeRank(s string) int {
	switch s {
	case RelayScopeView:
		return 1
	case RelayScopeOperate:
		return 2
	case RelayScopeAdmin:
		return 3
	}
	return 0
}

// RelayConfig is the [relay] table: the daemon's outbound link to an
// agentd-relay, through which paired devices (agentd-desktop, agentd-app)
// reach it without SSH.
type RelayConfig struct {
	// Enabled (default false) starts the link when URL is set.
	Enabled bool `toml:"enabled"`
	// URL is the relay base URL (https://… or wss://…; http/ws only on loopback).
	URL string `toml:"url"`
	// Label is the host name offered to clients (default: hostname).
	Label string `toml:"label"`
	// JoinToken admits the host on the relay. JoinTokenEnv names an
	// environment variable holding it instead (wins when set).
	JoinToken    string `toml:"join_token"`
	JoinTokenEnv string `toml:"join_token_env"`
	// MaxScope caps what any invite may grant (default "operate").
	MaxScope string `toml:"max_scope"`
	// MaxDevices caps paired devices (default 16).
	MaxDevices int `toml:"max_devices"`
}

// Relay defaults.
const (
	DefaultRelayMaxDevices = 16
)

// ScopeCap returns MaxScope or the default.
func (r RelayConfig) ScopeCap() string {
	if r.MaxScope == "" {
		return RelayScopeOperate
	}
	return r.MaxScope
}

// Devices returns MaxDevices or the default.
func (r RelayConfig) Devices() int {
	if r.MaxDevices <= 0 {
		return DefaultRelayMaxDevices
	}
	return r.MaxDevices
}

// HostLabel returns Label or the hostname.
func (r RelayConfig) HostLabel() string {
	if l := strings.TrimSpace(r.Label); l != "" {
		return l
	}
	h, _ := os.Hostname()
	if h == "" {
		return "host"
	}
	return h
}

// Token resolves the join token (JoinTokenEnv wins when that variable is set).
func (r RelayConfig) Token(getenv func(string) string) string {
	if getenv == nil {
		getenv = os.Getenv
	}
	if r.JoinTokenEnv != "" {
		if v := getenv(r.JoinTokenEnv); v != "" {
			return v
		}
	}
	return r.JoinToken
}

func (r RelayConfig) validate() error {
	if r.MaxScope != "" && RelayScopeRank(r.MaxScope) == 0 {
		return fmt.Errorf("relay.max_scope: %q is not view, operate or admin", r.MaxScope)
	}
	if len(r.Label) > 64 {
		return errors.New("relay.label: at most 64 characters")
	}
	if r.URL == "" {
		if r.Enabled {
			return errors.New("relay.enabled needs relay.url")
		}
		return nil
	}
	u, err := url.Parse(r.URL)
	if err != nil || u.Host == "" {
		return fmt.Errorf("relay.url: invalid %q", r.URL)
	}
	if u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return errors.New("relay.url: must not carry credentials, query or fragment")
	}
	switch u.Scheme {
	case "https", "wss":
	case "http", "ws":
		h := u.Hostname()
		ip := net.ParseIP(h)
		if h != "localhost" && (ip == nil || !ip.IsLoopback()) {
			return errors.New("relay.url: plain http/ws is only allowed on loopback")
		}
	default:
		return fmt.Errorf("relay.url: scheme must be https or wss")
	}
	return nil
}
