package addhost_test

import (
	"testing"
	"time"

	"github.com/digiogithub/agentd/internal/desktopui/app/addhost"
	"github.com/digiogithub/agentd/internal/desktopui/app/apptest"
	_ "github.com/digiogithub/agentd/internal/desktopui/app/provision"
	"github.com/digiogithub/agentd/internal/desktopui/backend"
	"github.com/digiogithub/agentd/internal/desktopui/backend/fake"
)

func TestFormCopy(t *testing.T) {
	e := apptest.New(t, "/hosts/add", 1400, 1000)
	e.Has("Añadir host", "Relay (invitación)", "Pega el enlace de agentd relay invite", "Importar", "~/.ssh/config o Tailscale", "O A MANO",
		"Nombre", "Host", "Puerto", "Usuario", "Autenticación", "Clave generada en este equipo", "Agente SSH",
		"Clave existente", "Contraseña", "Copiar clave pública", fake.PublicKeyLine,
		"Al conectar verás la huella del host para confirmarla. Luego la app detecta agentd, zellij, tmux y los CLIs de agentes.",
		"Conectar y detectar")
}

func TestCopyPublicKey(t *testing.T) {
	e := apptest.New(t, "/hosts/add", 1400, 1000)
	e.Click("Copiar clave pública")
	if got := e.UI.Clipboard(); got != fake.PublicKeyLine {
		t.Errorf("clipboard %q", got)
	}
}

func TestConnectConfirmsFingerprint(t *testing.T) {
	e := apptest.New(t, "/hosts/add", 1400, 1000)
	e.Fill("Nombre", "Mi NAS")
	e.Fill("Host", "nas.local")
	e.Fill("Usuario", "jose")
	e.Click("Agente SSH, Usa las claves cargadas en tu agente SSH")
	e.Click("Conectar y detectar")
	if !e.Eventually(time.Second, func() bool { return e.UI.HasText("Confirma la huella del host") }) {
		t.Fatalf("no fingerprint dialog: %q", e.UI.Texts())
	}
	e.Has("ssh-ed25519", "Aceptar")
	e.Click("Aceptar")
	if !e.Eventually(time.Second, func() bool { return e.A.Router.Path() == "/hosts/provision/mi-nas" }) {
		t.Fatalf("path %q", e.A.Router.Path())
	}
	h, ok := e.A.Live.Host("mi-nas")
	if !ok || h.Host.Auth != backend.AuthAgent || h.Host.HostKey == "" || h.Host.Addr != "nas.local" || h.State != backend.HostOnline {
		t.Errorf("host %+v", h)
	}
}

func TestPasswordInstallsKey(t *testing.T) {
	e := apptest.New(t, "/hosts/add", 1400, 1000)
	e.Fill("Nombre", "Caja")
	e.Fill("Host", "caja.local")
	e.Fill("Usuario", "jose")
	e.Click("Contraseña, Solo para instalar la clave; no se guarda")
	e.Fill("Contraseña del host", fake.FakePassword)
	e.Click("Conectar y detectar")
	if !e.Eventually(time.Second, func() bool { return e.UI.HasText("Confirma la huella del host") }) {
		t.Fatalf("no fingerprint dialog: %q", e.UI.Texts())
	}
	e.Click("Aceptar")
	if !e.Eventually(time.Second, func() bool { return e.A.Router.Path() == "/hosts/provision/caja" }) {
		t.Fatalf("path %q; texts %q", e.A.Router.Path(), e.UI.Texts())
	}
	if k := e.F.KeysInstalled(); len(k) != 1 || k[0] != "caja" {
		t.Errorf("keys installed %v", k)
	}
	if h, ok := e.A.Live.Host("caja"); !ok || h.Host.Auth != backend.AuthGenerated || h.State != backend.HostOnline {
		t.Errorf("host %+v", h)
	}
}

func TestMissingFieldsToast(t *testing.T) {
	e := apptest.New(t, "/hosts/add", 1400, 1000)
	e.Click("Conectar y detectar")
	if p := e.A.Toasts.Pending(); len(p) == 0 && !e.UI.HasText("Rellena host, puerto y usuario") {
		t.Errorf("no toast")
	}
}

func TestImportSSHConfig(t *testing.T) {
	e := apptest.New(t, "/hosts/add", 1400, 1000)
	e.Click("Importar, ~/.ssh/config o Tailscale")
	if !e.Eventually(time.Second, func() bool { return e.UI.HasText("gpu-box") }) {
		t.Fatalf("import list %q", e.UI.Texts())
	}
	e.Has("Importar hosts", "nas", "admin@nas.local")
}

func TestSlug(t *testing.T) {
	if s := addhost.Slug(" Mi NAS (casa) "); s != "mi-nas-casa" {
		t.Errorf("slug %q", s)
	}
}
