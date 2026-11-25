package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// PushConfig is [push]: push notifications through Firebase Cloud Messaging
// (plan docs/plans/agentd/push-fcm_plan.md). Off by default.
type PushConfig struct {
	Enabled bool `toml:"enabled"`
	// CredentialsFile is the Google service account JSON (~ expanded).
	CredentialsFile string `toml:"credentials_file"`
	// On lists the reasons that push: question, permission (default), waiting,
	// blocked, exited, turn_done.
	On []string `toml:"on"`
	// IncludePreview adds the question / dialog text to the payload (it goes
	// through Google; default false).
	IncludePreview bool `toml:"include_preview"`
	// Debounce is the minimum gap per session and reason.
	Debounce Duration `toml:"debounce"`
	// HostLabel names the host in payloads (default: hostname).
	HostLabel string `toml:"host_label"`
}

// DefaultPushCredentials is the default credentials_file.
const DefaultPushCredentials = "~/.config/agentd/fcm-service-account.json"

// DefaultPushOn is the default `on` of [push].
var DefaultPushOn = []string{"question", "permission"}

var pushReasons = map[string]bool{"question": true, "permission": true, "waiting": true, "blocked": true, "exited": true, "turn_done": true}

func (p *PushConfig) applyDefaults() {
	if p.CredentialsFile == "" {
		p.CredentialsFile = DefaultPushCredentials
	}
	if len(p.On) == 0 {
		p.On = append([]string(nil), DefaultPushOn...)
	}
	if p.Debounce == 0 {
		p.Debounce = Duration(DefaultNotifyDebounce)
	}
}

func (p PushConfig) validate() error {
	for _, r := range p.On {
		if !pushReasons[r] {
			return fmt.Errorf("push.on: unknown reason %q (question, permission, waiting, blocked, exited, turn_done)", r)
		}
	}
	if time.Duration(p.Debounce) < 0 {
		return fmt.Errorf("push.debounce must be >= 0")
	}
	return nil
}

// CredentialsPath returns credentials_file with ~ expanded; it must be absolute.
func (p PushConfig) CredentialsPath() (string, error) {
	path := p.CredentialsFile
	if path == "" {
		path = DefaultPushCredentials
	}
	if path == "~" || strings.HasPrefix(path, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("push.credentials_file: %w", err)
		}
		path = filepath.Join(home, strings.TrimPrefix(path, "~"))
	}
	if !filepath.IsAbs(path) {
		return "", fmt.Errorf("push.credentials_file must be an absolute path (or start with ~/)")
	}
	return filepath.Clean(path), nil
}

// CheckCredentialsFile verifies that path is a regular file owned by the user
// with no group/other permission bits. The content is never part of an error.
func CheckCredentialsFile(path string) error { return checkPrivateFile(path) }
