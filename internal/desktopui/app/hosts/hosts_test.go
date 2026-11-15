package hosts_test

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/digiogithub/agentd/internal/desktopui/app"
	"github.com/digiogithub/agentd/internal/desktopui/backend/fake"

	"github.com/digiogithub/agentd/internal/desktopui/app/apptest"
	_ "github.com/digiogithub/agentd/internal/desktopui/app/hosts"
	_ "github.com/digiogithub/agentd/internal/desktopui/app/provision"
	"github.com/digiogithub/agentd/internal/desktopui/backend"
)

func TestHostCards(t *testing.T) {
	e := apptest.New(t, "/hosts", 1400, 900)
	e.Has("Hosts", "3 en línea · 1 sin agentd", "estudio", "jose@estudio.local · SSH", "4 sesiones", "agentd 0.6.2",
		"zellij", "tmux", "claude · codex · pando", "vps-hetzner", "agentd 0.6.1", "Actualización 0.6.2",
		"portatil", "jose@192.168.1.40 · SSH", "1 zellij", "zellij · main",
		"Sin agentd solo hay terminal. Instálalo por SSH para tener chat, aprobaciones y buzón entre agentes.",
		"Instalar agentd en portatil", "raspberry", "Sin conexión · visto hace 2 días", "Reintentar", "Añadir host")
}

func TestInstallBannerOpensProvision(t *testing.T) {
	e := apptest.New(t, "/hosts", 1400, 900)
	e.Click("Instalar agentd en portatil")
	if p := e.A.Router.Path(); p != "/hosts/provision/portatil" {
		t.Errorf("path %q", p)
	}
}

func TestRetryConnects(t *testing.T) {
	e := apptest.New(t, "/hosts", 1400, 900)
	e.Click("Reintentar")
	if !e.Eventually(time.Second, func() bool {
		h, _ := e.A.Live.Host("raspberry")
		return h.State == backend.HostOffline && e.UI.HasText("No se pudo conectar")
	}) {
		t.Errorf("retry did not report: %q", e.UI.Texts())
	}
}

// TestReconnectAsksKeyThenPassword: an unknown host key opens the
// fingerprint dialog, Aceptar trusts it, then the password dialog connects
// once and installs the app key.
func TestReconnectAsksKeyThenPassword(t *testing.T) {
	e := apptest.New(t, "/hosts", 1400, 900)
	h := backend.Host{ID: "caja", Name: "caja", Addr: "caja.local", User: "jose", Auth: backend.AuthPassword}
	if err := e.F.SaveHost(context.Background(), h); err != nil {
		t.Fatal(err)
	}
	if !e.Eventually(time.Second, func() bool { return e.UI.HasText("caja") }) {
		t.Fatalf("no card: %q", e.UI.Texts())
	}
	if err := e.UI.RightClick("caja"); err != nil {
		t.Fatal(err)
	}
	if err := e.UI.ChooseMenuItem("Reconectar"); err != nil {
		t.Fatalf("%v; menu %q", err, e.UI.Menu())
	}
	if !e.Eventually(time.Second, func() bool { return e.UI.HasText("Confirma la huella del host") }) {
		t.Fatalf("no fingerprint dialog: %q", e.UI.Texts())
	}
	e.Click("Aceptar")
	if !e.Eventually(time.Second, func() bool { return e.UI.HasText("Contraseña de caja") }) {
		t.Fatalf("no password dialog: %q", e.UI.Texts())
	}
	e.Fill("Contraseña", fake.FakePassword)
	e.Click("Conectar")
	if !e.Eventually(time.Second, func() bool {
		h, _ := e.A.Live.Host("caja")
		return h.State == backend.HostOnline && h.Host.Auth == backend.AuthGenerated
	}) {
		t.Fatalf("not connected: %q", e.UI.Texts())
	}
	if k := e.F.KeysInstalled(); len(k) != 1 || k[0] != "caja" {
		t.Errorf("keys installed %v", k)
	}
}

func TestHostKeyChangedText(t *testing.T) {
	st := backend.HostStatus{Err: "host key changed", ErrCode: backend.HostErrKeyChanged}
	if got := app.HostErrText(st); !strings.Contains(got, "ha cambiado") {
		t.Errorf("card text %q", got)
	}
	if got := app.ErrorText(fmt.Errorf("x: %w", backend.ErrHostKeyChanged)); got != app.HostKeyChangedText {
		t.Errorf("error text %q", got)
	}
}

func TestRemoveHostAsksFirst(t *testing.T) {
	e := apptest.New(t, "/hosts", 1400, 900)
	if err := e.UI.RightClick("raspberry"); err != nil {
		t.Fatal(err)
	}
	if err := e.UI.ChooseMenuItem("Quitar"); err != nil {
		t.Fatalf("%v; menu %q", err, e.UI.Menu())
	}
	e.Settle()
	e.Has("¿Quitar raspberry?", "Se borra de la app; el host no se toca.")
	e.Click("Quitar")
	if !e.Eventually(time.Second, func() bool { _, ok := e.A.Live.Host("raspberry"); return !ok }) {
		t.Errorf("host not removed")
	}
}
