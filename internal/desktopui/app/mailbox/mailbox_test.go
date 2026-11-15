package mailbox_test

import (
	"testing"
	"time"

	"github.com/digiogithub/agentd/internal/desktopui/app/apptest"
	_ "github.com/digiogithub/agentd/internal/desktopui/app/mailbox"
)

func TestMessagesAndQueues(t *testing.T) {
	e := apptest.New(t, "/mailbox", 1400, 900)
	e.Eventually(time.Second, func() bool { return e.UI.HasText("Mensajes · 3") })
	e.Has("Buzón", "Mensajes entre agentes · estudio", "Mensajes · 3", "Colas · 2", "claude@pips", "claude@agentd", "En cola",
		"Mejorar agentd a partir de clientes de herdr de terceros: revisa Whip y Alma.",
		"#144 · se entregará cuando claude@agentd quede libre", "Respondido", "¿Está lista la API de login con expiración?",
		"Sí, endpoint /v1/session con expires_at. Tests en verde.", "tú", "Leído", "Revisa el plan de colas antes de seguir.")
	e.Click("Colas · 2")
	e.Has("queue:review", "3 sin reclamar", "#151 · Revisar PR de expiración de tokens · de codex@api",
		"#153 · Revisar README · de claude@docs", "queue:tests", "0 sin reclamar")
	if !e.UI.HasText("#148 reclamado por claude@webapp hace 20 min") {
		t.Errorf("claimed row missing: %q", e.UI.Texts())
	}
}

func TestSendToQueue(t *testing.T) {
	e := apptest.New(t, "/mailbox", 1400, 900)
	e.Eventually(time.Second, func() bool { return e.UI.HasText("Colas · 2") })
	e.Click("Enviar a agente o cola")
	e.Click("queue:review")
	e.Fill("Mensaje", "Revisa el PR 12")
	e.Click("Enviar")
	sent := e.F.Sent()
	if len(sent) != 1 || sent[0].To != "queue:review" || sent[0].Body != "Revisa el PR 12" {
		t.Fatalf("sent %+v", sent)
	}
	if !e.Eventually(time.Second, func() bool { return e.UI.HasText("Mensaje enviado") }) {
		t.Errorf("mailbox not reloaded: %q", e.UI.Texts())
	}
}
