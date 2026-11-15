package kit

import (
	"embed"
	"sync"
	"sync/atomic"

	"github.com/egoist/mygo/ui"
)

// Font families of the mockups. Both are SIL Open Font License 1.1 (see
// fonts/OFL-*.txt): Inter (rsms/inter) for the UI, JetBrains Mono for code
// and the terminal. They are embedded so every platform draws the same.
const (
	FontUI   = "Inter"
	FontMono = "JetBrains Mono"
)

//go:embed fonts/*.ttf
var fontFS embed.FS

var (
	fontsOnce sync.Once
	fontsOK   atomic.Bool
	fontsErr  error
)

// RegisterFonts registers the embedded fonts with MyGo once. Until it runs
// (tests, or when it fails) text uses the system's fonts.
func RegisterFonts() error {
	fontsOnce.Do(func() {
		files := []struct{ name, family string }{
			{"Inter-Regular.ttf", FontUI}, {"Inter-Medium.ttf", FontUI},
			{"Inter-SemiBold.ttf", FontUI}, {"Inter-Bold.ttf", FontUI},
			{"JetBrainsMono-Regular.ttf", FontMono}, {"JetBrainsMono-Medium.ttf", FontMono},
		}
		for _, f := range files {
			data, err := fontFS.ReadFile("fonts/" + f.name)
			if err == nil {
				err = ui.RegisterFont(data, f.family)
			}
			if err != nil {
				fontsErr = err
				return
			}
		}
		fontsOK.Store(true)
	})
	return fontsErr
}

// MonoFamily is the family to give Font() for code: the embedded JetBrains
// Mono when registered, else the system's monospaced font.
func MonoFamily() string {
	if fontsOK.Load() {
		return FontMono + ", monospace"
	}
	return "monospace"
}
