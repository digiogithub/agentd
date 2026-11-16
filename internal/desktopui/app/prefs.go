package app

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"time"

	"github.com/digiogithub/agentd/internal/desktopui/kit"
)

// Settings are the local preferences of the desktop app (Ajustes). They
// live in $XDG_CONFIG_HOME/agentd/desktop/settings.json (0600).
type Settings struct {
	NotifyWaiting  bool     `json:"notify_waiting"`
	NotifyTurnDone bool     `json:"notify_turn_done"`
	NotifyExit     bool     `json:"notify_exit"`
	ReplyFromAlert bool     `json:"reply_from_notification"`
	Theme          kit.Mode `json:"theme"`
	Muted          []Mute   `json:"muted,omitempty"`
	// AgentsGroup is how the agents list groups sessions: GroupProject
	// (default) or GroupState.
	AgentsGroup string `json:"agents_group,omitempty"`
}

// Values of Settings.AgentsGroup.
const (
	GroupProject = "project"
	GroupState   = "state"
)

// Mute silences the notifications of one session until a time.
type Mute struct {
	Session string    `json:"session"` // Session.Key()
	Label   string    `json:"label"`   // shown name
	Until   time.Time `json:"until"`
}

// DefaultSettings match the mockup: waiting, turn done and reply on.
var DefaultSettings = Settings{NotifyWaiting: true, NotifyTurnDone: true, NotifyExit: false, ReplyFromAlert: true, Theme: kit.ModeDark, AgentsGroup: GroupProject}

// Prefs stores Settings, in a file or in memory.
type Prefs struct {
	mu   sync.Mutex
	path string
	s    Settings
}

// DefaultPrefsPath is where the settings file lives.
func DefaultPrefsPath() string {
	dir := os.Getenv("XDG_CONFIG_HOME")
	if dir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return ""
		}
		dir = filepath.Join(home, ".config")
	}
	return filepath.Join(dir, "agentd", "desktop", "settings.json")
}

// NewPrefs loads path ("" keeps the settings in memory). A missing or broken
// file gives the defaults.
func NewPrefs(path string) *Prefs {
	p := &Prefs{path: path, s: DefaultSettings}
	if path != "" {
		if b, err := os.ReadFile(path); err == nil {
			s := DefaultSettings
			if json.Unmarshal(b, &s) == nil {
				p.s = s
			}
		}
	}
	return p
}

// Get returns a copy of the settings.
func (p *Prefs) Get() Settings {
	p.mu.Lock()
	defer p.mu.Unlock()
	s := p.s
	s.Muted = slices.Clone(p.s.Muted)
	return s
}

// Update changes the settings and saves them.
func (p *Prefs) Update(fn func(s *Settings)) error {
	p.mu.Lock()
	fn(&p.s)
	s := p.s
	path := p.path
	p.mu.Unlock()
	if path == "" {
		return nil
	}
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// IsMuted reports whether key is muted at now.
func (s Settings) IsMuted(key string, now time.Time) bool {
	for _, m := range s.Muted {
		if m.Session == key && now.Before(m.Until) {
			return true
		}
	}
	return false
}
