// Package kit is the component library of agentd-desktop: the palette of the
// validated mockups mapped onto MyGo's ui.Theme, the embedded fonts (Inter,
// JetBrains Mono), a lucide icon subset and small components built on MyGo's
// widget bases. Views import kit for their look; only kit, shell and app/...
// import MyGo.
package kit

import (
	"sync/atomic"

	"github.com/egoist/mygo/ui"
)

// Mode is the appearance preference (Ajustes > Apariencia).
type Mode string

// Modes.
const (
	ModeDark   Mode = "dark"
	ModeLight  Mode = "light"
	ModeSystem Mode = "system"
)

// Tokens are the resolved colours and sizes of one appearance. Names follow
// their role in the mockups, not their hue.
type Tokens struct {
	Dark bool

	Bg, Surface, Raised, Raised2, Border, Divider, NavBg, TerminalBg ui.Color
	Text, Secondary, Muted, Faint                                    ui.Color
	Accent, AccentBg, AccentLight, OnAccent                          ui.Color
	Waiting, WaitingBg, WaitingBorder, QuestionBg, QuestionBorder    ui.Color
	OnWaiting                                                        ui.Color
	Working, WorkingBg, OnWorking                                    ui.Color
	Success, SuccessBg, SuccessText                                  ui.Color
	Danger, DangerBg, DangerBorder                                   ui.Color
	Scrim                                                            ui.Color

	// Font is the UI family, Mono the code and terminal family.
	Font, Mono string

	uiTheme *ui.Theme
}

func dark() *Tokens {
	return &Tokens{
		Dark: true,
		Bg:   ui.Hex("#0D1117"), Surface: ui.Hex("#161B22"), Raised: ui.Hex("#1F2630"), Raised2: ui.Hex("#262E3A"),
		Border: ui.Hex("#2A3340"), Divider: ui.Hex("#1F2630"), NavBg: ui.Hex("#10151C"), TerminalBg: ui.Hex("#07090D"),
		Text: ui.Hex("#E6EDF3"), Secondary: ui.Hex("#C9D4DF"), Muted: ui.Hex("#9AA7B4"), Faint: ui.Hex("#6E7B88"),
		Accent: ui.Hex("#9DB4FF"), AccentBg: ui.Hex("#1B2233"), AccentLight: ui.Hex("#C3D1FF"), OnAccent: ui.Hex("#0D1117"),
		Waiting: ui.Hex("#F0A04B"), WaitingBg: ui.Hex("#2B2116"), WaitingBorder: ui.Hex("#3A2E1E"),
		QuestionBg: ui.Hex("#1A1610"), QuestionBorder: ui.Hex("#5A4220"), OnWaiting: ui.Hex("#1A1206"),
		Working: ui.Hex("#58A6FF"), WorkingBg: ui.Hex("#13233A"), OnWorking: ui.Hex("#08172B"),
		Success: ui.Hex("#3FB950"), SuccessBg: ui.Hex("#12261A"), SuccessText: ui.Hex("#7EE2A0"),
		Danger: ui.Hex("#FF9B9B"), DangerBg: ui.Hex("#2A1515"), DangerBorder: ui.Hex("#5A2A2A"),
		Scrim: ui.RGBA(0, 0, 0, 0.6),
		Font:  FontUI, Mono: FontMono,
	}
}

// light is the same roles on a light background (the mockups are dark; the
// light set keeps their hues with readable contrast).
func light() *Tokens {
	return &Tokens{
		Bg: ui.Hex("#FFFFFF"), Surface: ui.Hex("#F6F8FA"), Raised: ui.Hex("#EEF1F5"), Raised2: ui.Hex("#E3E8EF"),
		Border: ui.Hex("#D0D7DE"), Divider: ui.Hex("#E3E8EF"), NavBg: ui.Hex("#F3F5F8"), TerminalBg: ui.Hex("#FBFCFD"),
		Text: ui.Hex("#1F2328"), Secondary: ui.Hex("#3D4650"), Muted: ui.Hex("#59636E"), Faint: ui.Hex("#818B98"),
		Accent: ui.Hex("#3E5FD9"), AccentBg: ui.Hex("#E6ECFF"), AccentLight: ui.Hex("#2F4CB8"), OnAccent: ui.Hex("#FFFFFF"),
		Waiting: ui.Hex("#B4610A"), WaitingBg: ui.Hex("#FFF1E0"), WaitingBorder: ui.Hex("#F2D3AE"),
		QuestionBg: ui.Hex("#FFF8EE"), QuestionBorder: ui.Hex("#E8BE86"), OnWaiting: ui.Hex("#FFFFFF"),
		Working: ui.Hex("#0969DA"), WorkingBg: ui.Hex("#DDF4FF"), OnWorking: ui.Hex("#FFFFFF"),
		Success: ui.Hex("#1A7F37"), SuccessBg: ui.Hex("#DAFBE1"), SuccessText: ui.Hex("#116329"),
		Danger: ui.Hex("#CF222E"), DangerBg: ui.Hex("#FFEBE9"), DangerBorder: ui.Hex("#F5B5B0"),
		Scrim: ui.RGBA(24, 24, 27, 0.32),
		Font:  FontUI, Mono: FontMono,
	}
}

// Resolve returns the tokens of the dark or light appearance.
func Resolve(isDark bool) *Tokens {
	if isDark {
		return dark()
	}
	return light()
}

// UI maps the tokens onto MyGo's theme, so MyGo's own widgets (inputs,
// lists, menus, scroll bars, tooltips) match.
func (t *Tokens) UI() *ui.Theme {
	if t.uiTheme != nil {
		return t.uiTheme
	}
	th := ui.LightTheme()
	if t.Dark {
		th = ui.DarkTheme()
	}
	th.Dark = t.Dark
	th.Background = t.Bg
	th.Surface = t.Surface
	th.SurfaceHover = t.Raised
	th.SurfacePressed = t.Raised2
	th.Border = t.Border
	th.Text = t.Text
	th.TextMuted = t.Muted
	th.Accent = t.Accent
	th.AccentHover = t.AccentLight
	th.AccentPressed = t.AccentLight
	th.AccentText = t.OnAccent
	th.Danger, th.Warning, th.Success = t.Danger, t.Waiting, t.Success
	th.Selection = t.Accent.Alpha(0.3)
	th.Focus = t.Accent.Alpha(0.7)
	th.Inverse, th.InverseText = t.Text, t.Bg
	if t.Dark {
		th.Scrollbar = ui.RGBA(255, 255, 255, 0.25)
	} else {
		th.Scrollbar = ui.RGBA(0, 0, 0, 0.25)
	}
	th.Radius = 10
	th.Spacing = 4
	th.FontSize = 14
	if fontsOK.Load() {
		th.Font = t.Font
	}
	t.uiTheme = th
	return th
}

var current atomic.Pointer[Tokens]

func init() { current.Store(dark()) }

// Apply resolves the theme for this frame and installs it: call it first in
// the window's view. With ModeSystem the window's own appearance decides.
func Apply(c *ui.Context, m Mode) *Tokens {
	isDark := true
	switch m {
	case ModeLight:
		isDark = false
	case ModeSystem:
		isDark = c.Theme().Dark
	}
	t := current.Load()
	if t == nil || t.Dark != isDark || (t.uiTheme != nil && (t.uiTheme.Font != "") != fontsOK.Load()) {
		t = Resolve(isDark)
		current.Store(t)
	}
	c.SetTheme(t.UI())
	return t
}

// Current returns the tokens installed by the last Apply.
func Current() *Tokens { return current.Load() }
