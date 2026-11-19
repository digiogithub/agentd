package settings_test

import (
	"testing"
	"time"

	"github.com/digiogithub/agentd/internal/desktopui/app"
	"github.com/digiogithub/agentd/internal/desktopui/app/apptest"
	_ "github.com/digiogithub/agentd/internal/desktopui/app/settings"
	"github.com/digiogithub/agentd/internal/desktopui/backend/fake"
	"github.com/digiogithub/agentd/internal/desktopui/kit"
)

func TestTogglesAndThemePersist(t *testing.T) {
	e := apptest.New(t, "/settings", 1400, 1400)
	e.Has("Ajustes", "AVISARME CUANDO", "Un agente espera respuesta", "Permisos, preguntas y planes", "Termina un turno",
		"Agrupado cada 30 s por sesión", "Una sesión se cierra", "Incluye el código de salida", "Responder desde el aviso",
		"SILENCIADAS", "Ninguna sesión silenciada", "SEGURIDAD", "Claves SSH", "Dispositivos y tokens", "APARIENCIA",
		"Oscuro", "Claro", "Sistema")
	if e.UI.HasText("Pedir huella para aprobar") {
		t.Error("biometrics shown on desktop")
	}
	e.Click("Una sesión se cierra")
	if !e.A.Prefs.Get().NotifyExit {
		t.Error("NotifyExit not saved")
	}
	e.Click("Claro")
	if e.A.Prefs.Get().Theme != kit.ModeLight || kit.Current().Dark {
		t.Errorf("theme %q dark %v", e.A.Prefs.Get().Theme, kit.Current().Dark)
	}
	e.Click("Oscuro")
}

func TestMutesAndKey(t *testing.T) {
	e := apptest.New(t, "/settings", 1400, 900)
	_ = e.A.Prefs.Update(func(s *app.Settings) {
		y, m, d := time.Now().Date()
		s.Muted = []app.Mute{{Session: "estudio/agentd/claude@docs", Label: "claude@docs", Until: time.Date(y, m, d+1, 12, 0, 0, 0, time.Local)}}
	})
	e.Settle()
	e.Has("claude@docs", "hasta mañana")
	e.Click("Quitar silencio de claude@docs")
	if len(e.A.Prefs.Get().Muted) != 0 {
		t.Error("mute not removed")
	}
	e.Click("Claves SSH, 1 ed25519")
	if !e.Eventually(time.Second, func() bool { return e.UI.HasText(fake.PublicKeyLine) }) {
		t.Fatalf("key dialog %q", e.UI.Texts())
	}
	e.Click("Copiar clave pública")
	if e.UI.Clipboard() != fake.PublicKeyLine {
		t.Errorf("clipboard %q", e.UI.Clipboard())
	}
}

func TestChatFontSettings(t *testing.T) {
	e := apptest.New(t, "/settings", 1400, 1400)
	e.Has("VISTA CHAT", "Tamaño del texto", "14.5 pt", "Tipo de letra", "Sans", "Serif", "Monoespaciada")
	e.Click("Texto más grande")
	if got := e.A.Prefs.Get().ChatSize(); got != 15.5 {
		t.Errorf("size %v", got)
	}
	e.Click("Serif")
	if got := e.A.Prefs.Get().ChatFont; got != app.ChatFontSerif {
		t.Errorf("font %q", got)
	}
	e.Has("15.5 pt")

	e.Has("Legible (Atkinson Hyperlegible)", "Lectura (Literata)", "Interlineado", "Compacto", "Normal", "Amplio")
	if got := e.A.Prefs.Get().ChatLineHeightOrDefault(); got != app.ChatLineNormal {
		t.Errorf("default line height %q", got)
	}
	e.Click("Lectura (Literata)")
	e.Click("Amplio")
	if s := e.A.Prefs.Get(); s.ChatFont != app.ChatFontReading || s.ChatLineHeight != app.ChatLineWide {
		t.Errorf("settings %+v", s)
	}
	e.Click("Legible (Atkinson Hyperlegible)")
	e.Click("Compacto")
	if s := e.A.Prefs.Get(); s.ChatFont != app.ChatFontLegible || s.ChatLineHeight != app.ChatLineCompact {
		t.Errorf("settings %+v", s)
	}
}
