package kit

import (
	"embed"
	"sync"

	"github.com/egoist/mygo/ui"
)

// icons holds a subset of Lucide (ISC, see icons/LICENSE). Add an icon by
// copying its SVG from lucide into ./icons; names are lucide's file names.
//
//go:embed icons/*.svg
var iconFS embed.FS

// Icon names used by the shell and the views.
const (
	IconAgents      = "bot"
	IconHosts       = "server"
	IconMailbox     = "inbox"
	IconSettings    = "settings"
	IconTerminal    = "square-terminal"
	IconPlus        = "plus"
	IconSend        = "send"
	IconArrowUp     = "arrow-up"
	IconBack        = "arrow-left"
	IconClose       = "x"
	IconCheck       = "check"
	IconCopy        = "copy"
	IconTrash       = "trash"
	IconSearch      = "search"
	IconRefresh     = "refresh-cw"
	IconSpinner     = "loader-circle"
	IconError       = "circle-alert"
	IconWarning     = "triangle-alert"
	IconInfo        = "info"
	IconSuccess     = "circle-check"
	IconStop        = "circle-stop"
	IconMore        = "ellipsis"
	IconChevronDown = "chevron-down"
	IconChevronUp   = "chevron-up"
	IconChevronR    = "chevron-right"
	IconChevronL    = "chevron-left"
	IconClock       = "clock"
	IconMail        = "mail"
	IconMic         = "mic"
	IconImage       = "image-plus"
	IconQR          = "qr-code"
	IconPaste       = "clipboard-paste"
	IconImport      = "import"
	IconShield      = "shield"
	IconBell        = "bell"
	IconBellOff     = "bell-off"
	IconKey         = "key-round"
	IconOffline     = "wifi-off"
	IconKeyboard    = "keyboard"
	IconEye         = "eye"
	IconLaptop      = "laptop"
	IconDownload    = "download"
	IconFolderPlus  = "folder-plus"
	IconFolder      = "folder"
	IconLock        = "lock"
	IconFile        = "file-text"
	IconSun         = "sun"
	IconMoon        = "moon"
	IconMonitor     = "monitor"
	IconCircle      = "circle"
	IconPanel       = "list-checks"

	// Custom window controls (minimise, maximise, restore).
	IconMinus    = "minus"
	IconType     = "type"
	IconMaximize = "square"
	IconRestore  = "copy"
)

var (
	iconMu    sync.Mutex
	iconCache = map[string]*ui.SVG{}
)

// SVG returns the embedded icon name, parsed once. An unknown name returns
// the "circle-alert" icon so a typo shows instead of crashing.
func SVG(name string) *ui.SVG {
	iconMu.Lock()
	defer iconMu.Unlock()
	return svgLocked(name)
}

func svgLocked(name string) *ui.SVG {
	if s, ok := iconCache[name]; ok {
		return s
	}
	data, err := iconFS.ReadFile("icons/" + name + ".svg")
	if err != nil {
		if name == IconError {
			return nil
		}
		return svgLocked(IconError)
	}
	s, err := ui.ParseSVG(data)
	if err != nil {
		return nil
	}
	iconCache[name] = s
	return s
}

// HasIcon reports whether an icon is embedded.
func HasIcon(name string) bool {
	_, err := iconFS.ReadFile("icons/" + name + ".svg")
	return err == nil
}

// Icon shows an embedded icon in the current text colour.
func Icon(c *ui.Context, name string) ui.Element { return ui.Icon(c, SVG(name)) }
