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
	NotifyWaiting  bool `json:"notify_waiting"`
	NotifyTurnDone bool `json:"notify_turn_done"`
	NotifyExit     bool `json:"notify_exit"`
	// NotifyQuestion: an agent asks the human (a mailbox question);
	// NotifyBlocked: an agent marks a task blocked. Both default on.
	NotifyQuestion bool     `json:"notify_question"`
	NotifyBlocked  bool     `json:"notify_blocked"`
	ReplyFromAlert bool     `json:"reply_from_notification"`
	Theme          kit.Mode `json:"theme"`
	Muted          []Mute   `json:"muted,omitempty"`
	// AgentsGroup is how the agents list groups sessions: GroupProject
	// (default) or GroupState.
	AgentsGroup string `json:"agents_group,omitempty"`
	// PanelView is the Panel's view: PanelByAgent ("" = default) or
	// PanelBoard.
	PanelView string `json:"panel_view,omitempty"`
	// ChatFontSize is the size of the chat view's prose in points (0 = the
	// default, DefaultChatFontSize; kept within MinChatFontSize..MaxChatFontSize).
	ChatFontSize float32 `json:"chat_font_size,omitempty"`
	// ChatFont is the chat prose font: ChatFontSans ("" = default),
	// ChatFontSerif, ChatFontMono, ChatFontLegible or ChatFontReading.
	ChatFont string `json:"chat_font,omitempty"`
	// ChatLineHeight is the line spacing of the chat prose: ChatLineCompact,
	// ChatLineNormal ("" = default) or ChatLineWide.
	ChatLineHeight string `json:"chat_line_height,omitempty"`
}

// Chat font defaults and limits.
const (
	DefaultChatFontSize float32 = 14.5
	MinChatFontSize     float32 = 11
	MaxChatFontSize     float32 = 24
)

// Values of Settings.ChatFont.
const (
	ChatFontSans  = "sans"
	ChatFontSerif = "serif"
	ChatFontMono  = "mono"
	// ChatFontLegible is Atkinson Hyperlegible Next, ChatFontReading
	// Literata (both embedded, kit.FontLegible / kit.FontReading).
	ChatFontLegible = "legible"
	ChatFontReading = "reading"
)

// ChatFonts lists the chat fonts in the order the UI offers them.
var ChatFonts = []string{ChatFontSans, ChatFontSerif, ChatFontMono, ChatFontLegible, ChatFontReading}

// ChatFontLabels name the chat fonts (ChatFonts order) in the UI.
var ChatFontLabels = []string{"Sans", "Serif", "Monoespaciada", "Legible (Atkinson Hyperlegible)", "Lectura (Literata)"}

// Values of Settings.ChatLineHeight.
const (
	ChatLineCompact = "compact"
	ChatLineNormal  = "normal"
	ChatLineWide    = "wide"
)

// ChatLineHeights lists the line spacings in the order the UI offers them.
var ChatLineHeights = []string{ChatLineCompact, ChatLineNormal, ChatLineWide}

// ChatLineLabels name the line spacings (ChatLineHeights order) in the UI.
var ChatLineLabels = []string{"Compacto", "Normal", "Amplio"}

// Line heights of the chat prose at ChatLineNormal: agent text (Markdown)
// and the user's own messages.
const (
	ChatAgentLineHeight float32 = 1.5
	ChatUserLineHeight  float32 = 1.45
)

// ChatSize is ChatFontSize with the default applied and clamped.
func (s Settings) ChatSize() float32 {
	if s.ChatFontSize == 0 {
		return DefaultChatFontSize
	}
	return min(max(s.ChatFontSize, MinChatFontSize), MaxChatFontSize)
}

// ChatFontOrDefault is ChatFont with the default applied ("" or unknown = sans).
func (s Settings) ChatFontOrDefault() string {
	if slices.Contains(ChatFonts, s.ChatFont) {
		return s.ChatFont
	}
	return ChatFontSans
}

// ChatLineHeightOrDefault is ChatLineHeight with the default applied ("" or
// unknown = normal).
func (s Settings) ChatLineHeightOrDefault() string {
	if slices.Contains(ChatLineHeights, s.ChatLineHeight) {
		return s.ChatLineHeight
	}
	return ChatLineNormal
}

// ChatLineScale is the factor ChatLineHeight applies to the chat prose's
// line heights and paragraph gaps: 0.87 compact (agent lines 1.5 -> 1.3),
// 1 normal, 1.2 wide (1.5 -> 1.8).
func (s Settings) ChatLineScale() float32 {
	switch s.ChatLineHeightOrDefault() {
	case ChatLineCompact:
		return 0.87
	case ChatLineWide:
		return 1.2
	}
	return 1
}

// Values of Settings.AgentsGroup.
const (
	GroupProject = "project"
	GroupState   = "state"
)

// Values of Settings.PanelView.
const (
	PanelByAgent = "agent"
	PanelBoard   = "board"
)

// Mute silences the notifications of one session until a time.
type Mute struct {
	Session string    `json:"session"` // Session.Key()
	Label   string    `json:"label"`   // shown name
	Until   time.Time `json:"until"`
}

// DefaultSettings match the mockup: waiting, turn done, questions, blocked
// tasks and reply on.
var DefaultSettings = Settings{NotifyWaiting: true, NotifyTurnDone: true, NotifyExit: false, NotifyQuestion: true, NotifyBlocked: true,
	ReplyFromAlert: true, Theme: kit.ModeDark, AgentsGroup: GroupProject, PanelView: PanelByAgent}

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

// ChatFamily is the font family of a Settings.ChatFont value.
func ChatFamily(font string) string {
	switch font {
	case ChatFontSerif:
		return "serif"
	case ChatFontMono:
		return kit.MonoFamily()
	case ChatFontLegible:
		return kit.FontLegible + ", sans-serif"
	case ChatFontReading:
		return kit.FontReading + ", serif"
	}
	return kit.FontUI + ", sans-serif"
}
