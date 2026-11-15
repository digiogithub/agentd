package provision_test

import (
	"testing"
	"time"

	"github.com/digiogithub/agentd/internal/desktopui/app/apptest"
	"github.com/digiogithub/agentd/internal/desktopui/app/provision"
)

func TestDetectedAndPlan(t *testing.T) {
	e := apptest.New(t, "/hosts/provision/portatil", 1400, 1000)
	e.Has("Preparar portatil", "Linux x86_64 · Pop!_OS 24.04 · systemd de usuario", "DETECTADO", "zellij 0.43", "node 24",
		"claude 2.4", "sin agentd", "sin tmux", "agentd", "Binario en ~/.local/bin, servicio systemd de usuario, firma comprobada",
		"Activar el listener herdr-core (Herdr, Alma, Whip)", "Registrar el MCP de agentd en los CLIs instalados",
		"AGENTES DE TERMINAL", "Claude Code", "2.4 · instalado", "Actualizar", "Codex", "npm @openai/codex",
		"Ver los comandos que se ejecutarán por SSH", "Instalar 1 componente")
	e.Click("Instalar Codex")
	e.Has("Se instalará", "Instalar 2 componentes")
	e.Click("Ver los comandos que se ejecutarán por SSH")
	e.Has("curl -fsSLO …/agentd_0.6.2_linux_amd64.tar.gz\nsha256sum -c checksums.txt\ntar -xzf agentd_0.6.2_linux_amd64.tar.gz -C ~/.local/bin agentd\nsystemctl --user enable --now agentd\nagentd hc install-shim && printf '[herdr]\\nlisten = true\\n' >> ~/.config/agentd/config.toml\nclaude mcp add agentd -- agentd mcp\nnpm i -g @openai/codex")
}

func TestInstallStreamsEvents(t *testing.T) {
	e := apptest.New(t, "/hosts/provision/portatil", 1400, 1000)
	e.Click("Instalar Codex")
	e.Click("Instalar 2 componentes")
	if !e.Eventually(2*time.Second, func() bool { return e.UI.HasText("Listo") }) {
		t.Fatalf("install did not finish: %q", e.UI.Texts())
	}
	ins := e.F.Installs()
	if len(ins) != 1 || !ins[0].Agentd || !ins[0].HCListener || !ins[0].MCP || len(ins[0].CLIs) != 1 || ins[0].CLIs[0] != "codex" {
		t.Errorf("install options %+v", ins)
	}
	v, _ := e.Root.AreaView("provision").(*provision.View)
	rows := v.Progress()
	if len(rows) != 7 {
		t.Fatalf("progress %+v", rows)
	}
	for _, r := range rows {
		if !r.Done || r.Err != "" {
			t.Errorf("row %+v", r)
		}
	}
	e.Has("RESULTADO", "Descargar agentd 0.6.2", "Instalar codex")
	// After the install the host reports agentd.
	if !e.Eventually(time.Second, func() bool { return e.UI.HasText("agentd 0.6.2 instalado") }) {
		t.Errorf("caps not refreshed: %q", e.UI.Texts())
	}
}

func TestPreselectFromRoute(t *testing.T) {
	e := apptest.New(t, "/hosts/provision/estudio?cli=gemini", 1400, 1000)
	e.Has("Se instalará", "Instalar 1 componente")
}
