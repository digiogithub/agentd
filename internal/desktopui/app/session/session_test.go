package session_test

import (
	"encoding/json"
	"errors"
	"github.com/digiogithub/agentd/internal/desktopui/chatfmt"
	"strings"
	"testing"
	"time"

	"github.com/digiogithub/agentd/internal/desktopui/app"
	"github.com/digiogithub/agentd/internal/desktopui/app/agents"
	"github.com/digiogithub/agentd/internal/desktopui/app/apptest"
	"github.com/digiogithub/agentd/internal/desktopui/app/session"
	"github.com/digiogithub/agentd/internal/desktopui/backend/fake"
	"github.com/digiogithub/agentd/internal/proto"
	"github.com/egoist/mygo/ui"
)

func TestChatShowsTranscriptAndQuestion(t *testing.T) {
	e := apptest.New(t, "/agents/s/vps-hetzner/agentd/codex@api", 1400, 900)
	e.Has("codex@api", "vps-hetzner · ~/src/api · turno 14", "Esperando",
		"Añade expiración a los tokens de sesión y migra la base de datos.",
		"go test ./...", "4,1 s", "Edited auth/token.go", "+42 −7",
		"PREGUNTA", "¿Migro la tabla sessions ahora o dejo la migración para el despliegue?",
		"Migrar ahora con una migración reversible", "Dejarlo para el despliegue y documentarlo",
		"Parar y revisar el esquema conmigo", "Responder", "Escribir otra", "Mensaje para el agente", "Adjuntar imagen", "Dictar")
}

func TestChooseSendsSelectedOptionWithRevision(t *testing.T) {
	e := apptest.New(t, "/agents/s/vps-hetzner/agentd/codex@api", 1400, 900)
	e.Click("Migrar ahora con una migración reversible")
	e.Click("Responder")
	acts := e.F.Acts()
	if len(acts) != 1 {
		t.Fatalf("acts %+v", acts)
	}
	a := acts[0].Args
	if a.Action != "choose" || a.OptionID != "a1" || a.Revision != 57 || a.Epoch != fake.Epoch || a.Label != "Migrar ahora con una migración reversible" {
		t.Errorf("act %+v", a)
	}
	if !e.Eventually(time.Second, func() bool {
		return e.UI.HasText("● Entendido: Migrar ahora con una migración reversible") ||
			e.UI.HasText("De acuerdo. Dejo la migración para el despliegue y lo documento en MIGRATIONS.md.")
	}) {
		t.Errorf("answer not reflected: %q", e.UI.Texts())
	}
}

func TestComposerSendsPrompt(t *testing.T) {
	e := apptest.New(t, "/agents/s/estudio/agentd/claude@docs", 1400, 900)
	e.Click("Mensaje para el agente")
	e.UI.Type("Añade un índice")
	e.UI.Key(0, ui.KeyEnter)
	e.Settle()
	var got *proto.ActArgs
	for _, a := range e.F.Acts() {
		if a.Args.Action == "prompt" {
			got = &a.Args
		}
	}
	if got == nil || got.Text != "Añade un índice" || got.Name != "claude@docs" || got.Epoch != fake.Epoch || got.Revision == 0 {
		t.Fatalf("prompt act %+v (all %+v)", got, e.F.Acts())
	}
	if !e.Eventually(time.Second, func() bool { return e.UI.HasText("● Hecho: Añade un índice") }) {
		t.Errorf("reply not shown: %q", e.UI.Texts())
	}
}

// A message goes in while the agent works (act prompt with force).
func TestComposerSendsWhileWorking(t *testing.T) {
	e := apptest.New(t, "/agents/s/estudio/agentd/pando@agentd", 1400, 900)
	e.Click("Mensaje para el agente")
	e.UI.Type("Y añade tests")
	e.UI.Key(0, ui.KeyEnter)
	e.Settle()
	var got *proto.ActArgs
	for _, a := range e.F.Acts() {
		if a.Args.Action == "prompt" {
			got = &a.Args
		}
	}
	if got == nil || !got.Force || got.Text != "Y añade tests" {
		t.Fatalf("prompt act %+v (all %+v)", got, e.F.Acts())
	}
}

// Scrolling up stops the follow and offers a way back to the bottom.
func TestChatScrollUpStopsFollowing(t *testing.T) {
	e := apptest.New(t, "/agents/s/vps-hetzner/agentd/codex@api", 1000, 260)
	e.Settle()
	if e.UI.HasText("Ir al final") {
		t.Fatalf("button shown while pinned: %q", e.UI.Texts())
	}
	e.UI.Scroll(600, 150, 0, -400)
	e.Settle()
	if !e.UI.HasText("Ir al final") {
		t.Fatalf("no button after scrolling up: %q", e.UI.Texts())
	}
	e.Click("Ir al final")
	e.Settle()
	if e.UI.HasText("Ir al final") {
		t.Errorf("button still shown at the bottom")
	}
}

func TestQueuedMailHint(t *testing.T) {
	// claude@agentd has no session in the fake; queue one for claude@docs.
	e := apptest.New(t, "/agents", 1400, 900)
	if _, err := e.F.Send(t.Context(), "estudio", proto.SendArgs{To: "claude@docs", Body: "hola"}); err != nil {
		t.Fatal(err)
	}
	e.A.Navigate("/agents/s/estudio/agentd/claude@docs")
	if !e.Eventually(2*time.Second, func() bool {
		return e.UI.HasText("1 mensaje en cola: se entrega cuando el agente quede libre")
	}) {
		t.Fatalf("queue hint missing: %q", e.UI.Texts())
	}
	e.Click("Ver mensajes en cola")
	if p := e.A.Router.Path(); p != "/mailbox/estudio" {
		t.Errorf("path %q", p)
	}
}

func TestItemsParsing(t *testing.T) {
	items := session.Items([]proto.ViewLine{
		{Text: "❯ hazlo", Role: "transcript"},
		{Text: "● Voy a ello.", Role: "transcript"},
		{Text: "● Bash(go test ./...)", Role: "transcript"},
		{Text: "  ⎿ ok  pkg 1.2s", Role: "transcript"},
		{Text: "✓ go vet ./...  2,5 s", Role: "transcript"},
		{Text: "Bash command", Role: "dialog"},
		{Text: "esperando", Role: "status"},
	}, true)
	want := []session.ItemKind{session.ItemUser, session.ItemAgent, session.ItemTool, session.ItemTool, session.ItemSystem}
	if len(items) != len(want) {
		t.Fatalf("items %+v", items)
	}
	for i, k := range want {
		if items[i].Kind != k {
			t.Errorf("item %d kind %d, want %d (%+v)", i, items[i].Kind, k, items[i])
		}
	}
	if items[2].Title != "Bash(go test ./...)" || !strings.Contains(items[2].Text, "ok  pkg 1.2s") {
		t.Errorf("tool %+v", items[2])
	}
	if items[3].Meta != "2,5 s" || items[3].OK == nil || !*items[3].OK {
		t.Errorf("tool meta %+v", items[3])
	}
}

// Agent prose comes from the lines' chat markup, with blank lines kept as
// paragraph breaks; tool output stays plain text.
func TestItemsUseMarkup(t *testing.T) {
	items := session.Items([]proto.ViewLine{
		{Text: "● No por ahora. Chunking", Md: "● **No por ahora.** Chunking", Role: "transcript"},
		{Text: "", Role: "transcript"},
		{Text: "  Antes de RAG", Md: "  ### Antes de RAG", Role: "transcript"},
		{Text: "  - usa grant_call_id", Md: "  - usa `grant_call_id`", Role: "transcript"},
		{Text: "● Bash(ls *.go)", Role: "transcript"},
		{Text: "  ⎿ a.go", Role: "transcript"},
	}, true)
	if len(items) != 2 || items[0].Kind != session.ItemAgent {
		t.Fatalf("items %+v", items)
	}
	if want := "**No por ahora.** Chunking\n\n### Antes de RAG\n- usa `grant_call_id`"; items[0].Text != want {
		t.Errorf("agent markup %q, want %q", items[0].Text, want)
	}
	if items[1].Title != "Bash(ls *.go)" || items[1].Text != "a.go" {
		t.Errorf("tool %+v", items[1])
	}
}

// The chat's text menu changes the saved chat font settings.
func TestChatFontMenu(t *testing.T) {
	e := apptest.New(t, "/agents/s/estudio/agentd/claude@docs", 1400, 900)
	e.Click("Texto del chat")
	if err := e.UI.ChooseMenuItem("Texto más grande"); err != nil {
		t.Fatalf("%v (menu %q)", err, e.UI.Menu())
	}
	e.Click("Texto del chat")
	if err := e.UI.ChooseMenuItem("Monoespaciada"); err != nil {
		t.Fatal(err)
	}
	if s := e.A.Prefs.Get(); s.ChatSize() != 15.5 || s.ChatFont != app.ChatFontMono {
		t.Errorf("settings %+v", s)
	}
	e.Click("Texto del chat")
	if err := e.UI.ChooseMenuItem("Lectura (Literata)"); err != nil {
		t.Fatal(err)
	}
	e.Click("Texto del chat")
	if err := e.UI.ChooseMenuItem("Interlineado", "Amplio"); err != nil {
		t.Fatalf("%v (menu %q)", err, e.UI.Menu())
	}
	if s := e.A.Prefs.Get(); s.ChatFont != app.ChatFontReading || s.ChatLineHeight != app.ChatLineWide {
		t.Errorf("settings %+v", s)
	}
	e.Settle()
	e.Click("Texto del chat")
	if err := e.UI.ChooseMenuItem("Legible (Atkinson Hyperlegible)"); err != nil {
		t.Fatal(err)
	}
	e.Click("Texto del chat")
	if err := e.UI.ChooseMenuItem("Interlineado", "Compacto"); err != nil {
		t.Fatal(err)
	}
	if s := e.A.Prefs.Get(); s.ChatFont != app.ChatFontLegible || s.ChatLineHeight != app.ChatLineCompact {
		t.Errorf("settings %+v", s)
	}
}

func TestTerminalTabShowsPTYAndSendsKeys(t *testing.T) {
	if !apptest.UseGhosttyVT(t) {
		t.Skip("libghostty-vt not fetched (make desktop-natives-<goos>-<goarch>)")
	}
	e := apptest.New(t, "/agents/s/estudio/agentd/claude@webapp?tab=terminal", 1400, 900)
	v := agentsView(t, e)
	if msg := v.TerminalError(); msg != "" {
		t.Skipf("terminal unavailable: %s", msg)
	}
	e.Has("Interrumpir", "Control", "Control: tus teclas llegan al agente y el PTY se ajusta a esta ventana.", "160×50 · 2 clientes")
	if !e.Eventually(3*time.Second, func() bool {
		return v.Terminal() != nil && strings.Contains(v.Terminal().Text(), "Revisando la entrada del hijo")
	}) {
		t.Fatalf("screen %q", v.Terminal().Text())
	}
	// Control mode: the PTY takes the view's size, not the session's 160x50.
	if !e.Eventually(2*time.Second, func() bool {
		r := e.F.Resizes()
		return len(r) > 0 && r[len(r)-1][0] > 20 && r[len(r)-1] != [2]int{160, 50}
	}) {
		t.Errorf("PTY not resized to the view: %v", e.F.Resizes())
	}
	v.Terminal().Send([]byte("ls\r"))
	if !e.Eventually(2*time.Second, func() bool { return strings.Contains(e.F.Typed(), "ls\r") }) {
		t.Errorf("typed %q", e.F.Typed())
	}
	e.Click("Escape")
	if !e.Eventually(time.Second, func() bool {
		for _, a := range e.F.Acts() {
			if a.Args.Action == "keys" && len(a.Args.Keys) == 1 && a.Args.Keys[0] == "esc" {
				return true
			}
		}
		return false
	}) {
		t.Errorf("esc not sent: %+v", e.F.Acts())
	}
	e.Click("Interrumpir")
	found := false
	for _, a := range e.F.Acts() {
		found = found || a.Args.Action == "interrupt"
	}
	if !found {
		t.Errorf("interrupt not sent")
	}
	// Observar: read only, the key bar is disabled.
	e.Click("Cambiar modo de control")
	e.Has("Observar: solo lectura, nadie ve tus pulsaciones.")
}

func agentsView(t *testing.T, e *apptest.Env) *session.View {
	t.Helper()
	v, _ := e.Root.AreaView("agents").(*agents.View)
	if v == nil || v.Detail() == nil {
		t.Fatalf("no session page open: %q", e.UI.Texts())
	}
	return v.Detail()
}

func TestChatRendersTableAsCellsAndLink(t *testing.T) {
	var opened []string
	old := chatfmt.OpenURL
	chatfmt.OpenURL = func(u string) error { opened = append(opened, u); return nil }
	defer func() { chatfmt.OpenURL = old }()
	e := apptest.New(t, "/agents/s/estudio/agentd/claude@docs", 1400, 900)
	// Each cell is its own text; the box-drawing characters are gone.
	e.Has("Cola", "Uso", "builds", "trabajos de compilar y empaquetar", "reviews", "revisiones pendientes")
	for _, s := range e.UI.Texts() {
		if strings.ContainsAny(s, "┌│├└") {
			t.Errorf("box drawing left in %q", s)
		}
	}
	if err := e.UI.Click("https://example.com/docs/colas"); err != nil {
		t.Fatal(err)
	}
	e.Settle()
	if len(opened) != 1 || opened[0] != "https://example.com/docs/colas" {
		t.Errorf("opened %v", opened)
	}
}

func TestSendErrorText(t *testing.T) {
	cases := map[string]string{
		proto.CodeBusy:         "Hay texto pendiente",
		proto.CodeNotReady:     "está trabajando",
		proto.CodeVerifyFailed: "no mostró el texto pegado",
		proto.CodeConflict:     "pantalla del agente cambió",
		proto.CodeTimeout:      "no responde",
	}
	for code, want := range cases {
		title, _ := session.SendErrorText(proto.Errorf(code, "x"))
		if !strings.Contains(title, want) {
			t.Errorf("%s -> %q", code, title)
		}
	}
	for reason, want := range map[string]string{
		proto.BusyInput: "ya tiene texto en su entrada",
		proto.BusyDraft: "borrador sin enviar",
		proto.BusyHuman: "Acabas de teclear",
		proto.BusyLock:  "recibiendo otra entrada",
	} {
		err := &proto.RemoteError{Msg: "busy", Code: proto.CodeBusy, Data: json.RawMessage(`{"reason":"` + reason + `"}`)}
		if title, _ := session.SendErrorText(err); !strings.Contains(title, want) {
			t.Errorf("busy/%s -> %q", reason, title)
		}
	}
	if title, d := session.SendErrorText(errors.New("boom")); title != "No se pudo enviar" || d == "" {
		t.Errorf("default %q %q", title, d)
	}
}

// A tool call is recognised by its bold name, also while it is still being
// written and when it spans several lines; the lines before the output mark
// continue the call, the rest are its output, and none leaks into the prose.
func TestItemsGroupMultiLineToolCalls(t *testing.T) {
	items := session.Items([]proto.ViewLine{
		{Text: "● Miro el estado.", Role: "transcript"},
		{Text: "", Role: "transcript"},
		{Text: "● Bash(agentd task set <<'EOF'", Md: "● **Bash**(agentd task set \\<\\<'EOF'", Role: "transcript"},
		{Text: "  [", Md: "  \\[", Role: "transcript"},
		{Text: "", Role: "transcript"},
		{Text: "   {\"id\":\"a\"}", Role: "transcript"},
		{Text: "  EOF)", Role: "transcript"},
		{Text: "  ⎿  revision 2", Role: "transcript"},
		{Text: "     second line", Role: "transcript"},
		{Text: "", Role: "transcript"},
		{Text: "● Read", Md: "● **Read**", Role: "transcript"},
		{Text: "", Role: "transcript"},
		{Text: "● agentd - agentd_inbox (MCP)(all: true)", Md: "● **agentd - agentd\\_inbox (MCP)**(all: true)", Role: "transcript"},
		{Text: "● Listo", Role: "transcript"},
		{Text: "✻ Brewed for 8s · done 15:16", Role: "transcript"},
	}, true)
	kinds := []session.ItemKind{session.ItemAgent, session.ItemTool, session.ItemTool, session.ItemTool, session.ItemAgent, session.ItemSystem}
	if len(items) != len(kinds) {
		t.Fatalf("items %+v", items)
	}
	for i, k := range kinds {
		if items[i].Kind != k {
			t.Errorf("item %d kind %v, want %v (%+v)", i, items[i].Kind, k, items[i])
		}
	}
	bash := items[1]
	if bash.Title != "Bash(agentd task set <<'EOF'" || bash.Call != "Bash(agentd task set <<'EOF'\n[\n {\"id\":\"a\"}\nEOF)" || bash.Text != "revision 2\nsecond line" {
		t.Errorf("bash %+v", bash)
	}
	if items[2].Title != "Read" || items[4].Text != "Listo" {
		t.Errorf("read %+v, prose %+v", items[2], items[4])
	}
}

// Runs of code lines become one fenced block (MyGo's Markdown draws it with a
// copy button), with the code's own indentation; a prompt of several lines
// stays one user entry.
func TestItemsFenceCodeAndJoinPrompt(t *testing.T) {
	items := session.Items([]proto.ViewLine{
		{Text: "❯ primera línea", Role: "transcript"},
		{Text: "  segunda línea", Role: "transcript"},
		{Text: "● Instálalo así:", Role: "transcript"},
		{Text: "", Role: "transcript"},
		{Text: "  func main() {", Code: true, Role: "transcript"},
		{Text: "", Role: "transcript"},
		{Text: "      run(\"a_b\")", Code: true, Role: "transcript"},
		{Text: "  }", Role: "transcript"},
		{Text: "", Role: "transcript"},
		{Text: "  Y reinicia `agentd`.", Md: "  Y reinicia \\`agentd\\`.", Role: "transcript"},
		{Text: "● sudo dpkg -i a.deb", Code: true, Role: "transcript"},
	}, true)
	if len(items) != 3 || items[0].Text != "primera línea\nsegunda línea" {
		t.Fatalf("items %+v", items)
	}
	if want := "Instálalo así:\n\n````\nfunc main() {\n\n    run(\"a_b\")\n}\n````\n\nY reinicia \\`agentd\\`."; items[1].Text != want {
		t.Errorf("agent %q, want %q", items[1].Text, want)
	}
	if want := "````\nsudo dpkg -i a.deb\n````"; items[2].Text != want {
		t.Errorf("code only %q, want %q", items[2].Text, want)
	}
}
