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
	e.Has("Nueva sesión", "ABRIR", "Agente", "Solo terminal", "Explorar…", "HOST", "estudio", "agentd 0.6.2 · SSH", "DÓNDE CORRE", "agentd", "zellij", "tmux", "SSH",
		"Recomendado: vista chat, aprobaciones con revisión, buzón entre agentes y estado fiable.",
		"PROYECTO", "webapp", "api", "agentd", "docs", "+ Carpeta…", "AGENTE", "Claude Code", "instalado · 2.4",
		"opencode", "Instalar…", "PRIMER MENSAJE (OPCIONAL)", "Se creará", "claude@webapp", "Lanzar")
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

func TestTerminalOnlyAgentd(t *testing.T) {
	e := apptest.New(t, "/new", 1400, 900)
	e.Click("Solo terminal")
	e.Has("Shell de login en la carpeta del proyecto, gestionado por agentd (buzón, vista y terminal).", "shell@webapp", "PROYECTO")
	if e.UI.HasText("AGENTE") || e.UI.HasText("Claude Code") || e.UI.HasText("PRIMER MENSAJE (OPCIONAL)") {
		t.Errorf("agent list shown for terminal only: %q", e.UI.Texts())
	}
	e.Click("api")
	e.Has("shell@api")
	e.Click("Lanzar")
	l := e.F.Launched()
	want := backend.LaunchSpec{HostID: "estudio", Where: backend.SourceAgentd, Project: "api", TerminalOnly: true}
	if len(l) != 1 || l[0] != want {
		t.Fatalf("launched %+v, want %+v", l, want)
	}
	if !e.Eventually(time.Second, func() bool {
		return e.A.Router.Location() == "/agents/s/estudio/agentd/shell@api?tab=terminal"
	}) {
		t.Errorf("location %q", e.A.Router.Location())
	}
}

func TestTerminalOnlyMuxAndSSHResolveAliases(t *testing.T) {
	e := apptest.New(t, "/new", 1400, 900)
	e.Click("Solo terminal")
	e.Click("zellij")
	e.Has("Terminal sin agente: abre el shell por defecto de zellij en la carpeta.", "shell@webapp")
	e.Click("Lanzar")
	l := e.F.Launched()
	if len(l) != 1 || l[0].Where != backend.SourceZellij || !l[0].TerminalOnly || l[0].Project != "~/src/webapp" || l[0].Harness != "" {
		t.Fatalf("launched %+v", l)
	}

	e = apptest.New(t, "/new", 1400, 900)
	e.Click("Solo terminal")
	e.Click("SSH")
	e.Has("ssh · terminal en webapp")
	e.Click("Lanzar")
	l = e.F.Launched()
	if len(l) != 1 || l[0].Where != backend.SourceShell || !l[0].TerminalOnly || l[0].Project != "~/src/webapp" {
		t.Fatalf("launched %+v", l)
	}
}

func TestProjectPrefilledFromTheRoute(t *testing.T) {
	e := apptest.New(t, "/new/estudio?project=%2Fhome%2Fjose%2Fsrc%2Fapi", 1400, 900)
	e.Has("/home/jose/src/api", "claude@api")
	e.Click("Lanzar")
	l := e.F.Launched()
	if len(l) != 1 || l[0].Project != "/home/jose/src/api" || l[0].Where != backend.SourceAgentd {
		t.Fatalf("launched %+v", l)
	}
}

func TestFolderBrowser(t *testing.T) {
	e := apptest.New(t, "/new", 1400, 900)
	e.Click("Explorar carpetas")
	// Starts at the project directory (alias webapp = ~/src/webapp).
	e.Has("Elegir carpeta", "Inicio (~)", "Subir", "Mostrar ocultas", "Usar esta carpeta", "/home/jose/src/webapp", "No hay carpetas aquí")
	e.Click("Ir a /home/jose/src")
	e.Has("api", "agentd", "docs", "infra", "webapp")
	// Leave the home: every ancestor is a button.
	e.Click("Ir a /")
	e.Has("etc", "home", "root", "srv")
	// A folder the user cannot enter is greyed and does nothing.
	_ = e.UI.Click("Carpeta sin permiso root")
	e.Settle()
	if e.UI.HasText("Sin permiso") {
		t.Errorf("a locked folder was opened")
	}
	e.Click("Inicio (~)")
	e.Has("Documentos", "private", "src")
	if e.UI.HasText(".config") {
		t.Errorf("hidden folder shown")
	}
	e.Click("Mostrar ocultas")
	e.Has(".config")
	e.Click("Abrir carpeta src")
	e.Click("Abrir carpeta api")
	e.Has("/home/jose/src/api")
	e.Click("Subir")
	e.Has("/home/jose/src", "webapp")
	e.Click("Abrir carpeta api")
	e.Click("Usar esta carpeta")
	e.Has("claude@api")
	e.Click("Lanzar")
	l := e.F.Launched()
	if len(l) != 1 || l[0].Project != "/home/jose/src/api" {
		t.Fatalf("launched %+v", l)
	}
	listed := e.F.Listed()
	if len(listed) == 0 || listed[0] != "estudio:~/src/webapp" {
		t.Errorf("listed %v", listed)
	}
}
