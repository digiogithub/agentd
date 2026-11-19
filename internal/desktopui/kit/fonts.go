package kit

import (
	"embed"
	"sync"
	"sync/atomic"

	"github.com/egoist/mygo/ui"
)

// Font families of the mockups. All are SIL Open Font License 1.1 (see
// fonts/OFL-*.txt): Inter (rsms/inter) for the UI, JetBrains Mono for code
// and the terminal, and two chat reading fonts (Ajustes > Vista chat):
// Atkinson Hyperlegible Next (Braille Institute) and Literata (TypeTogether
// for Google). They are embedded so every platform draws the same.
const (
	FontUI      = "Inter"
	FontMono    = "JetBrains Mono"
	FontLegible = "Atkinson Hyperlegible Next"
	FontReading = "Literata"
)

//go:embed fonts/*.ttf fonts/*.txt
var fontFS embed.FS

// fontFiles are the embedded faces: file, family and the family's OFL text.
var fontFiles = []struct{ name, family, license string }{
	{"Inter-Regular.ttf", FontUI, "OFL-Inter.txt"}, {"Inter-Medium.ttf", FontUI, "OFL-Inter.txt"},
	{"Inter-SemiBold.ttf", FontUI, "OFL-Inter.txt"}, {"Inter-Bold.ttf", FontUI, "OFL-Inter.txt"},
	{"JetBrainsMono-Regular.ttf", FontMono, "OFL-JetBrainsMono.txt"},
	{"JetBrainsMono-Medium.ttf", FontMono, "OFL-JetBrainsMono.txt"},
	{"AtkinsonHyperlegibleNext-Regular.ttf", FontLegible, "OFL-AtkinsonHyperlegibleNext.txt"},
	{"AtkinsonHyperlegibleNext-Bold.ttf", FontLegible, "OFL-AtkinsonHyperlegibleNext.txt"},
	{"Literata-Regular.ttf", FontReading, "OFL-Literata.txt"},
	{"Literata-Bold.ttf", FontReading, "OFL-Literata.txt"},
}

var (
	fontsOnce sync.Once
	fontsOK   atomic.Bool
	fontsErr  error
)

// RegisterFonts registers the embedded fonts with MyGo once. Until it runs
// (tests, or when it fails) text uses the system's fonts.
func RegisterFonts() error {
	fontsOnce.Do(func() {
		for _, f := range fontFiles {
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
