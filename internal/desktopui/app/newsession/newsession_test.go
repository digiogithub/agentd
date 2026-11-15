package newsession_test

import (
	"testing"
	"time"

	_ "github.com/digiogithub/agentd/internal/desktopui/app/agents"
	"github.com/digiogithub/agentd/internal/desktopui/app/apptest"
	_ "github.com/digiogithub/agentd/internal/desktopui/app/newsession"
	_ "github.com/digiogithub/agentd/internal/desktopui/app/provision"
	"github.com/digiogithub/agentd/internal/desktopui/backend"
)

func TestFormCopyAndDefaults(t *testing.T) {
	e := apptest.New(t, "/new", 1400, 900)
	e.Has("Nueva sesión", "HOST", "estudio", "agentd 0.6.2 · SSH", "DÓNDE CORRE", "agentd", "zellij", "tmux", "SSH",
		"Recomendado: vista chat, aprobaciones con revisión, buzón entre agentes y estado fiable.",
		"PROYECTO", "webapp", "api", "agentd", "docs", "+ Carpeta…", "AGENTE", "Claude Code", "instalado · 2.4",
		"opencode", "Instalar…", "Shell", "bash", "PRIMER MENSAJE (OPCIONAL)", "Se creará", "claude@webapp", "Lanzar")
}

func TestLaunchAgentdSession(t *testing.T) {
	e := apptest.New(t, "/new", 1400, 900)
	e.Click("api")
	e.Click("Codex, instalado · 0.62")
	e.Has("codex@api")
	e.Click("Primer mensaje")
	e.UI.Type("Revisa los tests")
	e.Settle()
	e.Click("Lanzar")
	l := e.F.Launched()
	if len(l) != 1 {
		t.Fatalf("launched %+v", l)
	}
	want := backend.LaunchSpec{HostID: "estudio", Where: backend.SourceAgentd, Project: "api", Harness: "codex", FirstPrompt: "Revisa los tests"}
	if l[0] != want {
		t.Errorf("spec %+v, want %+v", l[0], want)
	}
	if !e.Eventually(time.Second, func() bool { return e.A.Router.Path() == "/agents/s/estudio/agentd/codex@api" }) {
		t.Errorf("path %q", e.A.Router.Path())
	}
}

func TestShellTargetAndFolder(t *testing.T) {
	e := apptest.New(t, "/new", 1400, 900)
	e.Click("SSH")
	e.Has("Shell SSH sin multiplexor: se pierde al desconectar.")
	e.Click("Añadir carpeta")
	e.Has("Carpeta o alias")
	e.UI.Type("~/src/nuevo")
	e.Click("Añadir")
	e.Has("~/src/nuevo", "ssh · claude en ~/src/nuevo")
	e.Click("Lanzar")
	l := e.F.Launched()
	if len(l) != 1 || l[0].Where != backend.SourceShell || l[0].Project != "~/src/nuevo" {
		t.Fatalf("launched %+v", l)
	}
}

func TestMissingAgentOpensProvision(t *testing.T) {
	e := apptest.New(t, "/new", 1400, 900)
	e.Click("Gemini CLI, Instalar…")
	if p := e.A.Router.Location(); p != "/hosts/provision/estudio?cli=gemini" {
		t.Errorf("location %q", p)
	}
}

func TestHostWithoutAgentdDisablesAgentdTarget(t *testing.T) {
	e := apptest.New(t, "/new/portatil", 1400, 900)
	e.Has("portatil", "SSH · sin agentd",
		"Pane nuevo en la sesión zellij del host. Terminal completa; el chat se deduce de la pantalla.")
}
