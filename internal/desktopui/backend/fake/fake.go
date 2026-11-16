// Package fake is an in-memory backend.Backend with the data of the validated
// mockups (hosts estudio, vps-hetzner, portatil, raspberry; sessions
// claude@webapp waiting on a permission, codex@api asking a question with
// three options, pando@agentd working, zellij main on portatil, claude@docs
// done...). It simulates agentd behaviour closely enough for UI work and
// headless tests: act choose/prompt change the view and bump its revision
// (a stale revision is a conflict), the terminal is a PTY that shows a
// canned screen and echoes what is typed, provisioning streams events.
//
// It never runs a program. Run agentd-desktop with AGENTD_DESKTOP_FAKE=1 to
// use it.
package fake

import (
	"context"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/digiogithub/agentd/internal/desktopui/backend"
	"github.com/digiogithub/agentd/internal/proto"
)

// Epoch is the daemon epoch of every fake host.
const Epoch = "fake-epoch-1"

// PublicKeyLine is the app key the fake reports.
const PublicKeyLine = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIF4k3pQ9x2t0mXq8w7yJt8n1rVbS0aGfL2cJp6uYwH9e agentd-desktop"

// ActCall is one recorded Act.
type ActCall struct {
	Key  string
	Args proto.ActArgs
}

// Fake is the in-memory backend. Delay scales the simulated latencies
// (agent replies, install steps); 0 makes them immediate.
type Fake struct {
	Delay time.Duration

	mu            sync.Mutex
	hosts         []*hostRec
	sessions      []*sessRec
	messages      []proto.Message
	nextMsg       int64
	acts          []ActCall
	sent          []proto.SendArgs
	launched      []backend.LaunchSpec
	killed        []string
	terminated    []string
	listed        []string
	installs      []backend.ProvisionOptions
	typed         []string
	resizes       [][2]int
	keysInstalled []string

	hostSubs []chan []backend.HostStatus
	sessSubs []chan []backend.Session
}

type hostRec struct {
	st       backend.HostStatus
	projects []proto.AliasInfo
	seen     string // "visto hace 2 días" for offline hosts
	password bool   // ConnectWithPassword is connecting
}

type sessRec struct {
	s     backend.Session
	view  *proto.View // nil for zellij/tmux/shell
	subs  []chan *proto.View
	reply string // what the agent answers next
}

var _ backend.Backend = (*Fake)(nil)

// New returns a fake with the mockup data.
func New() *Fake {
	f := &Fake{Delay: 600 * time.Millisecond, nextMsg: 154}
	f.seed(time.Now())
	return f
}

func tools(pairs ...string) map[string]backend.Tool {
	m := map[string]backend.Tool{}
	for _, n := range []string{"agentd", "zellij", "tmux", "node", "npm", "claude", "codex", "pando", "opencode", "gemini"} {
		m[n] = backend.Tool{Name: n}
	}
	for i := 0; i+1 < len(pairs); i += 2 {
		m[pairs[i]] = backend.Tool{Name: pairs[i], Installed: true, Version: pairs[i+1], Path: "/usr/bin/" + pairs[i]}
	}
	return m
}

func aliases(names ...string) []proto.AliasInfo {
	var out []proto.AliasInfo
	for _, n := range names {
		out = append(out, proto.AliasInfo{Name: n, Dir: "~/src/" + n, Source: "file"})
	}
	return out
}

func (f *Fake) seed(now time.Time) {
	estudio := &backend.Capabilities{OS: "Linux", Arch: "x86_64", Distro: "Debian 13", SystemdUser: true, Home: "/home/jose",
		AgentdSocket: "/run/user/1000/agentd/agentd.sock", LocalBinOnPath: true,
		Tools: tools("agentd", "0.6.2", "zellij", "0.43", "tmux", "3.5", "node", "24", "npm", "11", "claude", "2.4", "codex", "0.62", "pando", "1.9")}
	vps := &backend.Capabilities{OS: "Linux", Arch: "x86_64", Distro: "Ubuntu 24.04", SystemdUser: true, Home: "/home/dev",
		AgentdSocket: "/run/user/1000/agentd/agentd.sock", LocalBinOnPath: true,
		Tools: tools("agentd", "0.6.1", "node", "22", "npm", "10", "codex", "0.62")}
	portatil := &backend.Capabilities{OS: "Linux", Arch: "x86_64", Distro: "Pop!_OS 24.04", SystemdUser: true, Home: "/home/jose",
		LocalBinOnPath: true, Tools: tools("zellij", "0.43", "node", "24", "npm", "11", "claude", "2.4")}
	f.hosts = []*hostRec{
		{st: backend.HostStatus{Host: backend.Host{ID: "estudio", Name: "estudio", Addr: "estudio.local", Port: 22, User: "jose",
			Auth: backend.AuthGenerated, HostKey: "SHA256:k3Yp0Q8r2c9TnVwq4yE1u7hB6dZsLmA0fJx5GiRtC2o"},
			State: backend.HostOnline, Capabilities: estudio, Sessions: 4, AgentdVer: "0.6.2"},
			projects: aliases("webapp", "api", "agentd", "docs")},
		{st: backend.HostStatus{Host: backend.Host{ID: "vps-hetzner", Name: "vps-hetzner", Addr: "vps", Port: 22, User: "dev",
			Auth: backend.AuthAgent, HostKey: "SHA256:Vq9n2LrT0xYc8pKm4sHd7eWj1bZa6uGf3oNi5EtR0Qw", SSHAlias: "vps"},
			State: backend.HostOnline, Capabilities: vps, Sessions: 2, AgentdVer: "0.6.1"},
			projects: aliases("api", "infra")},
		{st: backend.HostStatus{Host: backend.Host{ID: "portatil", Name: "portatil", Addr: "192.168.1.40", Port: 22, User: "jose",
			Auth: backend.AuthGenerated, HostKey: "SHA256:Hn4cQ1zX8yLp0mVt7rKs2dWe9uBf6aJo3iTg5NqY1Ec"},
			State: backend.HostOnline, Capabilities: portatil, Sessions: 1},
			projects: aliases("webapp", "agentd")},
		{st: backend.HostStatus{Host: backend.Host{ID: "raspberry", Name: "raspberry", Addr: "raspberrypi.local", Port: 22, User: "pi",
			Auth: backend.AuthGenerated, HostKey: "SHA256:Rb8pL2wQ0nXc5tYm1kVd9sHe4uGa7jZo6iFr3NcT8Uw"},
			State: backend.HostOffline, Err: "Sin conexión"}, seen: "visto hace 2 días"},
	}
	ago := func(d time.Duration) time.Time { return now.Add(-d) }
	info := func(name, cwd string, turn uint64, status string, cols, rows uint16, attached int) *proto.SessionInfo {
		return &proto.SessionInfo{Name: name, State: "running", Cwd: cwd, Profile: strings.Split(name, "@")[0], Status: status,
			Turn: turn, Cols: cols, Rows: rows, Attached: attached, StartedAt: ago(time.Hour)}
	}
	webappView := &proto.View{Name: "claude@webapp", Epoch: Epoch, Revision: 31, Status: "waiting", StatusReason: "dialog",
		Attention: proto.ViewAttention{Set: true, Reason: "waiting", Since: ago(time.Minute)}, Cwd: "~/src/webapp",
		Lines: []proto.ViewLine{
			{Text: "❯ Pasa los tests del daemon con -race y arregla lo que falle.", Role: "transcript"},
			{Text: "● Voy a ejecutar la batería de tests con el detector de carreras.", Role: "transcript"},
			{Text: "Bash command", Role: "dialog"},
			{Text: "  go test ./internal/... -race", Role: "dialog"},
		},
		Dialog: &proto.ViewDialog{Question: "Bash · go test ./internal/... -race"},
		Options: []proto.ViewOption{
			{ID: "w1", Label: "Yes", Index: 1, Kind: "numbered", Selected: true, Confidence: 0.95},
			{ID: "w2", Label: "Yes, and don't ask again for go test commands", Index: 2, Kind: "numbered", Confidence: 0.95},
			{ID: "w3", Label: "No, and tell Claude what to do differently", Index: 3, Kind: "numbered", Confidence: 0.95},
		},
		Hints: []proto.ViewHint{{Key: "esc", Action: "cancel"}},
		Input: proto.ViewInput{State: "empty"}}
	apiView := &proto.View{Name: "codex@api", Epoch: Epoch, Revision: 57, Status: "waiting", StatusReason: "dialog",
		Attention: proto.ViewAttention{Set: true, Reason: "waiting", Since: ago(3 * time.Minute)}, Cwd: "~/src/api",
		Lines: []proto.ViewLine{
			{Text: "› Añade expiración a los tokens de sesión y migra la base de datos.", Role: "transcript"},
			{Text: "He añadido expires_at al modelo y los tests pasan. Antes de tocar la base de datos necesito una decisión.", Role: "transcript"},
			{Text: "✓ go test ./...  4,1 s", Role: "transcript"},
			{Text: "  ok  api/internal/auth   1.204s", Role: "transcript"},
			{Text: "  ok  api/internal/store  2.871s", Role: "transcript"},
			{Text: "• Edited auth/token.go (+42 −7)", Role: "transcript"},
		},
		Dialog: &proto.ViewDialog{Question: "¿Migro la tabla sessions ahora o dejo la migración para el despliegue?"},
		Options: []proto.ViewOption{
			{ID: "a1", Label: "Migrar ahora con una migración reversible", Index: 1, Kind: "numbered", Confidence: 0.9},
			{ID: "a2", Label: "Dejarlo para el despliegue y documentarlo", Index: 2, Kind: "numbered", Selected: true, Confidence: 0.9},
			{ID: "a3", Label: "Parar y revisar el esquema conmigo", Index: 3, Kind: "numbered", Confidence: 0.9},
		},
		Input: proto.ViewInput{State: "empty"}}
	pandoView := &proto.View{Name: "pando@agentd", Epoch: Epoch, Revision: 12, Status: "working", Cwd: "~/src/agentd",
		Lines: []proto.ViewLine{
			{Text: "> Revisa cómo el listener hc gestiona el core.lock", Role: "transcript"},
			{Text: "Leyendo internal/hcompat/server.go…", Role: "status"},
		}, Input: proto.ViewInput{State: "empty"}}
	docsView := &proto.View{Name: "claude@docs", Epoch: Epoch, Revision: 8, Status: "idle",
		Attention: proto.ViewAttention{Set: true, Reason: "turn_done", Since: ago(20 * time.Minute)}, Cwd: "~/src/docs",
		Lines: []proto.ViewLine{
			{Text: "❯ Actualiza el README con la sección de colas.", Role: "transcript"},
			{Text: "● Resumen de las colas (más detalle en https://example.com/docs/colas).", Role: "transcript"},
			{Text: "  ┌──────────┬───────────────────────┐", Role: "transcript"},
			{Text: "  │ Cola     │ Uso                   │", Role: "transcript"},
			{Text: "  ├──────────┼───────────────────────┤", Role: "transcript"},
			{Text: "  │ builds   │ trabajos de compilar  │", Role: "transcript"},
			{Text: "  │          │ y empaquetar          │", Role: "transcript"},
			{Text: "  ├──────────┼───────────────────────┤", Role: "transcript"},
			{Text: "  │ reviews  │ revisiones pendientes │", Role: "transcript"},
			{Text: "  └──────────┴───────────────────────┘", Role: "transcript"},
			{Text: "● README actualizado con la sección de colas de trabajo y un ejemplo de agentd claim.", Role: "transcript"},
		}, Input: proto.ViewInput{State: "suggestion", Ghost: "Try \"commit the changes\""}}
	codexWebView := &proto.View{Name: "codex@webapp", Epoch: Epoch, Revision: 4, Status: "idle", Cwd: "~/src/webapp",
		Lines: []proto.ViewLine{{Text: "Listo: el formulario de login valida el correo.", Role: "transcript"}}, Input: proto.ViewInput{State: "empty"}}
	exit := 0
	infraView := &proto.View{Name: "claude@infra", Epoch: Epoch, Revision: 3, Status: "exited", ExitCode: &exit, Cwd: "~/src/infra",
		Lines: []proto.ViewLine{{Text: "● Terraform plan sin cambios.", Role: "transcript"}}, Input: proto.ViewInput{State: "empty"}}

	agentdSess := func(host, name, status, preview string, attention bool, updated time.Time, v *proto.View, turn uint64) *sessRec {
		h, p, _ := strings.Cut(name, "@")
		inf := info(name, v.Cwd, turn, status, 160, 50, 2)
		inf.Attention, inf.Preview, inf.Revision = attention, preview, v.Revision
		if status == "exited" {
			inf.State, inf.ExitCode = "exited", v.ExitCode
		}
		return &sessRec{s: backend.Session{HostID: host, Source: backend.SourceAgentd, Name: name, Harness: h, Project: p,
			Status: status, Attention: attention, Preview: preview, Updated: updated, Info: inf}, view: v}
	}
	f.sessions = []*sessRec{
		agentdSess("estudio", "claude@webapp", "waiting", "Bash · go test ./internal/... -race", true, ago(time.Minute), webappView, 22),
		agentdSess("vps-hetzner", "codex@api", "waiting", "¿Migro la tabla sessions ahora o dejo la migración para el despliegue?", true, ago(3*time.Minute), apiView, 14),
		agentdSess("estudio", "pando@agentd", "working", "Leyendo internal/hcompat/server.go…", false, ago(2*time.Minute), pandoView, 5),
		{s: backend.Session{HostID: "portatil", Source: backend.SourceZellij, Name: "main", Harness: "claude", Project: "webapp",
			Status: "working", Detail: "3 panes · claude en pane 2", Updated: ago(10 * time.Second)}},
		agentdSess("estudio", "claude@docs", "idle", "README actualizado con la sección…", true, ago(20*time.Minute), docsView, 3),
		agentdSess("estudio", "codex@webapp", "idle", "Listo: el formulario de login valida el correo.", false, ago(time.Hour), codexWebView, 2),
		agentdSess("vps-hetzner", "claude@infra", "exited", "Terraform plan sin cambios.", false, ago(3*time.Hour), infraView, 1),
	}
	f.sessions[0].reply = "● Bash(go test ./internal/... -race)\n  ⎿ ok internal/session 3.912s\n  ok internal/daemon 8.104s"
	f.sessions[1].reply = "De acuerdo. Dejo la migración para el despliegue y lo documento en MIGRATIONS.md."

	id := func(n int64) *int64 { return &n }
	claimed := ago(20 * time.Minute)
	f.messages = []proto.Message{
		{ID: 144, Sender: "claude@pips", Recipient: "claude@agentd", Subject: "Mejorar agentd a partir de clientes de herdr de terceros…",
			Body: "Mejorar agentd a partir de clientes de herdr de terceros: revisa Whip y Alma.", State: "queued", CreatedAt: ago(5 * time.Minute)},
		{ID: 142, Sender: "claude@webapp", Recipient: "codex@api", Subject: "", Body: "¿Está lista la API de login con expiración?",
			State: "replied", CreatedAt: ago(14 * time.Minute)},
		{ID: 143, Sender: "codex@api", Recipient: "claude@webapp", Body: "Sí, endpoint /v1/session con expires_at. Tests en verde.",
			ReplyTo: id(142), State: "read", CreatedAt: ago(12 * time.Minute)},
		{ID: 139, Sender: "human:jose", Recipient: "pando@agentd", Body: "Revisa el plan de colas antes de seguir.", State: "read", CreatedAt: ago(time.Hour)},
		{ID: 151, Sender: "codex@api", Recipient: "queue:review", Queue: "review", Subject: "Revisar PR de expiración de tokens", State: "unclaimed", CreatedAt: ago(8 * time.Minute)},
		{ID: 152, Sender: "codex@api", Recipient: "queue:review", Queue: "review", Subject: "Revisar migración sessions", State: "unclaimed", CreatedAt: ago(6 * time.Minute)},
		{ID: 153, Sender: "claude@docs", Recipient: "queue:review", Queue: "review", Subject: "Revisar README", State: "unclaimed", CreatedAt: ago(4 * time.Minute)},
		{ID: 148, Sender: "codex@api", Recipient: "claude@webapp", Queue: "tests", Subject: "Ejecutar tests de integración", State: "delivered",
			CreatedAt: ago(30 * time.Minute), ClaimedAt: &claimed},
	}
}

// ---- recorded calls (tests) ----

// Acts returns the recorded Act calls.
func (f *Fake) Acts() []ActCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.acts)
}

// Sent returns the recorded Send calls.
func (f *Fake) Sent() []proto.SendArgs {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.sent)
}

// Launched returns the recorded Launch specs.
func (f *Fake) Launched() []backend.LaunchSpec {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.launched)
}

// Terminated returns the keys of the sessions Terminate removed.
func (f *Fake) Terminated() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.terminated)
}

// Listed returns the ListDir calls as "host:path" (hidden adds "+h").
func (f *Fake) Listed() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.listed)
}

// Installs returns the recorded Install options.
func (f *Fake) Installs() []backend.ProvisionOptions {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.installs)
}

// Typed returns what was written to fake terminals.
func (f *Fake) Typed() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return strings.Join(f.typed, "")
}

// SessionByName returns a copy of a session (tests).
func (f *Fake) SessionByName(name string) (backend.Session, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, r := range f.sessions {
		if r.s.Name == name {
			return r.s, true
		}
	}
	return backend.Session{}, false
}

// View returns a copy of the current view of a session (tests).
func (f *Fake) View(name string) *proto.View {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, r := range f.sessions {
		if r.s.Name == name && r.view != nil {
			return cloneView(r.view)
		}
	}
	return nil
}

// ---- watchers ----

func (f *Fake) hostSnapshot() []backend.HostStatus {
	out := make([]backend.HostStatus, 0, len(f.hosts))
	for _, h := range f.hosts {
		st := h.st
		if h.seen != "" && st.State == backend.HostOffline {
			st.Err = "Sin conexión · " + h.seen
		}
		out = append(out, st)
	}
	return out
}

func (f *Fake) sessSnapshot() []backend.Session {
	out := make([]backend.Session, 0, len(f.sessions))
	for _, r := range f.sessions {
		s := r.s
		if s.Info != nil {
			inf := *s.Info
			s.Info = &inf
		}
		out = append(out, s)
	}
	return out
}

// publish sends fresh snapshots to every watcher (f.mu held).
func (f *Fake) publishLocked() {
	hs, ss := f.hostSnapshot(), f.sessSnapshot()
	for _, ch := range f.hostSubs {
		replace(ch, hs)
	}
	for _, ch := range f.sessSubs {
		replace(ch, ss)
	}
}

// replace puts v in a 1-buffered channel, dropping an unread older value.
func replace[T any](ch chan T, v T) {
	for {
		select {
		case ch <- v:
			return
		default:
			select {
			case <-ch:
			default:
			}
		}
	}
}

func watch[T any](ctx context.Context, f *Fake, list *[]chan T, first T) <-chan T {
	in := make(chan T, 1)
	in <- first
	*list = append(*list, in)
	out := make(chan T)
	go func() {
		defer close(out)
		defer func() {
			f.mu.Lock()
			*list = slices.DeleteFunc(*list, func(c chan T) bool { return c == in })
			f.mu.Unlock()
		}()
		for {
			select {
			case <-ctx.Done():
				return
			case v := <-in:
				select {
				case out <- v:
				case <-ctx.Done():
					return
				}
			}
		}
	}()
	return out
}

// WatchHosts implements backend.Backend.
func (f *Fake) WatchHosts(ctx context.Context) <-chan []backend.HostStatus {
	f.mu.Lock()
	defer f.mu.Unlock()
	return watch(ctx, f, &f.hostSubs, f.hostSnapshot())
}

// WatchSessions implements backend.Backend.
func (f *Fake) WatchSessions(ctx context.Context) <-chan []backend.Session {
	f.mu.Lock()
	defer f.mu.Unlock()
	return watch(ctx, f, &f.sessSubs, f.sessSnapshot())
}

func (f *Fake) findLocked(s backend.Session) *sessRec {
	for _, r := range f.sessions {
		if r.s.Key() == s.Key() {
			return r
		}
	}
	return nil
}

func (f *Fake) hostLocked(id string) *hostRec {
	for _, h := range f.hosts {
		if h.st.Host.ID == id {
			return h
		}
	}
	return nil
}

func cloneView(v *proto.View) *proto.View {
	c := *v
	c.Lines = slices.Clone(v.Lines)
	c.Options = slices.Clone(v.Options)
	c.Hints = slices.Clone(v.Hints)
	if v.Dialog != nil {
		d := *v.Dialog
		c.Dialog = &d
	}
	return &c
}

// WatchView implements backend.Backend.
func (f *Fake) WatchView(ctx context.Context, s backend.Session) (<-chan *proto.View, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	r := f.findLocked(s)
	if r == nil {
		return nil, proto.Errorf(proto.CodeNotFound, "no such session %q", s.Name)
	}
	if r.view == nil {
		return nil, proto.Errorf(proto.CodeInvalid, "%s sessions have no view", s.Source)
	}
	in := make(chan *proto.View, 1)
	in <- cloneView(r.view)
	r.subs = append(r.subs, in)
	out := make(chan *proto.View)
	go func() {
		defer close(out)
		defer func() {
			f.mu.Lock()
			r.subs = slices.DeleteFunc(r.subs, func(c chan *proto.View) bool { return c == in })
			f.mu.Unlock()
		}()
		for {
			select {
			case <-ctx.Done():
				return
			case v := <-in:
				select {
				case out <- v:
				case <-ctx.Done():
					return
				}
			}
		}
	}()
	return out, nil
}

// changeLocked bumps the revision, syncs the session entry and publishes.
func (f *Fake) changeLocked(r *sessRec) {
	v := r.view
	v.Revision++
	r.s.Status = v.Status
	r.s.Attention = v.Attention.Set
	r.s.Updated = time.Now()
	for i := len(v.Lines) - 1; i >= 0; i-- {
		if v.Lines[i].Role != "dialog" && strings.TrimSpace(v.Lines[i].Text) != "" {
			r.s.Preview = strings.TrimSpace(v.Lines[i].Text)
			break
		}
	}
	if v.Dialog != nil {
		r.s.Preview = v.Dialog.Question
	}
	if r.s.Info != nil {
		r.s.Info.Status, r.s.Info.Attention, r.s.Info.Revision, r.s.Info.Preview = v.Status, v.Attention.Set, v.Revision, r.s.Preview
	}
	for _, ch := range r.subs {
		replace(ch, cloneView(v))
	}
	f.publishLocked()
}

func (f *Fake) later(d time.Duration, fn func()) {
	if f.Delay == 0 {
		fn()
		return
	}
	time.AfterFunc(d, fn)
}

// finishTurn makes the agent answer and go idle after a while.
func (f *Fake) finishTurn(key string, reply string) {
	f.later(f.Delay*3, func() {
		f.mu.Lock()
		defer f.mu.Unlock()
		for _, r := range f.sessions {
			if r.s.Key() != key || r.view == nil || r.view.Status != "working" {
				continue
			}
			for _, l := range strings.Split(reply, "\n") {
				r.view.Lines = append(r.view.Lines, proto.ViewLine{Text: l, Role: "transcript"})
			}
			r.view.Status = "idle"
			r.view.Attention = proto.ViewAttention{Set: true, Reason: "turn_done", Since: time.Now()}
			if r.s.Info != nil {
				r.s.Info.Turn++
			}
			f.changeLocked(r)
		}
	})
}

// Act implements backend.Backend.
func (f *Fake) Act(ctx context.Context, s backend.Session, args proto.ActArgs) (*proto.ActResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.acts = append(f.acts, ActCall{Key: s.Key(), Args: args})
	r := f.findLocked(s)
	if r == nil {
		return nil, proto.Errorf(proto.CodeNotFound, "no such session %q", s.Name)
	}
	if r.view == nil {
		return nil, proto.Errorf(proto.CodeInvalid, "act needs an agentd session")
	}
	v := r.view
	if args.Revision != 0 && (args.Epoch != Epoch || args.Revision != v.Revision) {
		return nil, &proto.CodedError{Code: proto.CodeConflict, Msg: "the screen changed", Data: proto.ActConflict{Revision: v.Revision, View: *cloneView(v)}}
	}
	if v.Status == "exited" {
		return nil, proto.Errorf(proto.CodeExited, "session %s exited", s.Name)
	}
	res := func(result string) *proto.ActResult {
		return &proto.ActResult{Revision: v.Revision, Status: v.Status, Result: result}
	}
	switch args.Action {
	case "choose":
		if args.Revision == 0 || args.Epoch == "" {
			return nil, proto.Errorf(proto.CodeInvalid, "choose needs revision and epoch")
		}
		var opt *proto.ViewOption
		for i := range v.Options {
			if v.Options[i].ID == args.OptionID {
				opt = &v.Options[i]
			}
		}
		if opt == nil {
			return nil, &proto.CodedError{Code: proto.CodeConflict, Msg: "no such option", Data: proto.ActConflict{Revision: v.Revision, View: *cloneView(v)}}
		}
		v.Lines = slices.DeleteFunc(v.Lines, func(l proto.ViewLine) bool { return l.Role == "dialog" })
		v.Lines = append(v.Lines, proto.ViewLine{Text: "→ " + opt.Label, Role: "status"})
		v.Dialog, v.Options, v.Hints = nil, nil, nil
		v.Status, v.Attention = "working", proto.ViewAttention{}
		f.changeLocked(r)
		reply := r.reply
		if reply == "" || strings.HasPrefix(strings.ToLower(opt.Label), "no") || strings.HasPrefix(opt.Label, "Parar") {
			reply = "● Entendido: " + opt.Label
		}
		go f.finishTurn(r.s.Key(), reply)
		return res("chosen"), nil
	case "prompt":
		if strings.TrimSpace(args.Text) == "" {
			return nil, proto.Errorf(proto.CodeInvalid, "empty prompt")
		}
		if v.Status == "working" {
			return nil, proto.Errorf(proto.CodeNotReady, "the agent is working")
		}
		v.Lines = append(v.Lines, proto.ViewLine{Text: "❯ " + args.Text, Role: "transcript"})
		v.Dialog, v.Options = nil, nil
		v.Status, v.Attention = "working", proto.ViewAttention{}
		f.changeLocked(r)
		go f.finishTurn(r.s.Key(), "● Hecho: "+args.Text)
		out := res("submitted")
		out.Submitted = true
		return out, nil
	case "keys", "interrupt", "cancel":
		if args.Action != "keys" && v.Status == "working" {
			v.Lines = append(v.Lines, proto.ViewLine{Text: "Interrupted by user", Role: "status"})
			v.Status = "idle"
			f.changeLocked(r)
			return res("stopped"), nil
		}
		return res("sent"), nil
	}
	return nil, proto.Errorf(proto.CodeInvalid, "unknown action %q", args.Action)
}

// ViewAck implements backend.Backend.
func (f *Fake) ViewAck(ctx context.Context, s backend.Session) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	r := f.findLocked(s)
	if r == nil || r.view == nil {
		return nil
	}
	if r.view.Attention.Set && r.view.Status != "waiting" {
		r.view.Attention = proto.ViewAttention{}
		f.changeLocked(r)
	}
	return nil
}

// Launch implements backend.Backend.
func (f *Fake) Launch(ctx context.Context, spec backend.LaunchSpec) (backend.Session, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.launched = append(f.launched, spec)
	h := f.hostLocked(spec.HostID)
	if h == nil || h.st.State != backend.HostOnline {
		return backend.Session{}, errors.New("host offline")
	}
	project := spec.Project
	if i := strings.LastIndex(project, "/"); i >= 0 {
		project = project[i+1:]
	}
	harness := spec.Harness
	if harness == "" {
		harness = "claude"
	}
	if spec.TerminalOnly {
		harness = "shell"
	}
	s := backend.Session{HostID: spec.HostID, Source: spec.Where, Harness: harness, Project: project, Status: "starting", Updated: time.Now()}
	r := &sessRec{}
	switch spec.Where {
	case backend.SourceAgentd:
		s.Name = harness + "@" + project
		cwd := "~/src/" + project
		if strings.HasPrefix(spec.Project, "/") || strings.HasPrefix(spec.Project, "~") {
			cwd = spec.Project
		}
		s.Info = &proto.SessionInfo{Name: s.Name, State: "running", Cwd: cwd, Cols: 160, Rows: 50, Status: "starting", StartedAt: time.Now()}
		r.view = &proto.View{Name: s.Name, Epoch: Epoch, Revision: 1, Status: "starting", Cwd: cwd, Input: proto.ViewInput{State: "empty"}}
	case backend.SourceShell:
		s.Name = "ssh-" + project
		s.Status = "working"
	default:
		s.Name = harness + "-" + project
		s.Status, s.Detail = "working", harness+" en "+project
	}
	for _, o := range f.sessions {
		if o.s.Key() == s.Key() {
			return backend.Session{}, proto.Errorf(proto.CodeExists, "session %s exists", s.Name)
		}
	}
	r.s = s
	f.sessions = append(f.sessions, r)
	h.st.Sessions++
	f.publishLocked()
	if r.view != nil {
		key, prompt := s.Key(), spec.FirstPrompt
		go f.later(f.Delay, func() {
			f.mu.Lock()
			defer f.mu.Unlock()
			for _, o := range f.sessions {
				if o.s.Key() != key {
					continue
				}
				if prompt != "" {
					o.view.Lines = append(o.view.Lines, proto.ViewLine{Text: "❯ " + prompt, Role: "transcript"})
					o.view.Status = "working"
					f.changeLocked(o)
					go f.finishTurn(key, "● Empiezo: "+prompt)
				} else {
					o.view.Status = "idle"
					f.changeLocked(o)
				}
			}
		})
	}
	return s, nil
}

// Kill implements backend.Backend.
func (f *Fake) Kill(ctx context.Context, s backend.Session) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.killed = append(f.killed, s.Key())
	r := f.findLocked(s)
	if r == nil {
		return proto.Errorf(proto.CodeNotFound, "no such session")
	}
	r.s.Status = "exited"
	if r.view != nil {
		code := 130
		r.view.Status, r.view.ExitCode = "exited", &code
		f.changeLocked(r)
		return nil
	}
	f.publishLocked()
	return nil
}

// Terminate implements backend.Backend: the session disappears from the list.
func (f *Fake) Terminate(ctx context.Context, s backend.Session) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.terminated = append(f.terminated, s.Key())
	for i, r := range f.sessions {
		if r.s.Key() != s.Key() {
			continue
		}
		f.sessions = slices.Delete(f.sessions, i, i+1)
		if h := f.hostLocked(s.HostID); h != nil && h.st.Sessions > 0 {
			h.st.Sessions--
		}
		f.publishLocked()
		return nil
	}
	return proto.Errorf(proto.CodeNotFound, "no such session")
}

// ---- hosts ----

// SaveHost implements backend.Backend.
func (f *Fake) SaveHost(ctx context.Context, h backend.Host) error {
	if h.ID == "" {
		return errors.New("host id required")
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if r := f.hostLocked(h.ID); r != nil {
		r.st.Host = h
	} else {
		f.hosts = append(f.hosts, &hostRec{st: backend.HostStatus{Host: h, State: backend.HostOffline}, projects: aliases("webapp")})
	}
	f.publishLocked()
	return nil
}

// RemoveHost implements backend.Backend.
func (f *Fake) RemoveHost(ctx context.Context, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.hosts = slices.DeleteFunc(f.hosts, func(h *hostRec) bool { return h.st.Host.ID == id })
	f.sessions = slices.DeleteFunc(f.sessions, func(r *sessRec) bool { return r.s.HostID == id })
	f.publishLocked()
	return nil
}

// fingerprint is a stable fake SHA256 fingerprint for a host address.
func fingerprint(addr string) string {
	const alphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/"
	var b strings.Builder
	h := uint32(2166136261)
	for i := 0; i < 43; i++ {
		for _, c := range addr {
			h = (h ^ uint32(c)) * 16777619
		}
		h = h*16777619 + uint32(i)
		b.WriteByte(alphabet[h%64])
	}
	return "SHA256:" + b.String()
}

// Connect implements backend.Backend. A host never connected before gets a
// *backend.HostKeyError first; once its key is trusted (TrustHostKey, or
// saved with that HostKey) a password host gets backend.ErrPasswordRequired
// (ConnectWithPassword), and then it comes online with portatil's
// capabilities (no agentd).
func (f *Fake) Connect(ctx context.Context, id string) error {
	f.mu.Lock()
	h := f.hostLocked(id)
	if h == nil {
		f.mu.Unlock()
		return proto.Errorf(proto.CodeNotFound, "no such host %q", id)
	}
	if id == "raspberry" {
		h.st.State = backend.HostConnecting
		f.publishLocked()
		f.mu.Unlock()
		f.later(f.Delay*2, func() {
			f.mu.Lock()
			defer f.mu.Unlock()
			h.st.State = backend.HostOffline
			f.publishLocked()
		})
		return errors.New("dial tcp raspberrypi.local:22: no route to host")
	}
	fp := fingerprint(h.st.Host.Addr)
	if h.st.Host.HostKey == "" {
		f.mu.Unlock()
		return &backend.HostKeyError{HostID: id, Fingerprint: fp, KeyType: "ssh-ed25519"}
	}
	if strings.HasSuffix(h.st.Host.Addr, ".changed") { // simulates a reinstalled (or spoofed) host
		f.mu.Unlock()
		return fmt.Errorf("%w: got %s, expected %s", backend.ErrHostKeyChanged, fp, h.st.Host.HostKey)
	}
	if h.st.Host.Auth == backend.AuthPassword && !h.password {
		f.mu.Unlock()
		return backend.ErrPasswordRequired
	}
	h.password = false // one-time
	if h.st.State != backend.HostOnline {
		h.st.State = backend.HostOnline
		h.st.Err = ""
		if h.st.Capabilities == nil {
			h.st.Capabilities = &backend.Capabilities{OS: "Linux", Arch: "x86_64", Distro: "Debian 13", SystemdUser: true,
				Home: "/home/" + h.st.Host.User, LocalBinOnPath: true, Tools: tools("tmux", "3.4", "node", "22", "npm", "10")}
		}
		f.publishLocked()
	}
	f.mu.Unlock()
	return nil
}

// FakePassword is the only password the fake accepts.
const FakePassword = "secret"

// TrustHostKey implements backend.Backend.
func (f *Fake) TrustHostKey(ctx context.Context, id, fingerprint string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	h := f.hostLocked(id)
	if h == nil {
		return proto.Errorf(proto.CodeNotFound, "no such host %q", id)
	}
	if !strings.HasPrefix(fingerprint, "SHA256:") {
		return fmt.Errorf("not a SHA256 fingerprint: %q", fingerprint)
	}
	if h.st.Host.HostKey != "" && h.st.Host.HostKey != fingerprint {
		return fmt.Errorf("%w: %s already has another key", backend.ErrHostKeyChanged, id)
	}
	h.st.Host.HostKey = fingerprint
	f.publishLocked()
	return nil
}

// ConnectWithPassword implements backend.Backend: FakePassword logs in once;
// installKey switches the host to AuthGenerated and records it in
// KeysInstalled.
func (f *Fake) ConnectWithPassword(ctx context.Context, id, password string, installKey bool) error {
	if password == "" {
		return backend.ErrPasswordRequired
	}
	f.mu.Lock()
	h := f.hostLocked(id)
	if h == nil {
		f.mu.Unlock()
		return proto.Errorf(proto.CodeNotFound, "no such host %q", id)
	}
	if password != FakePassword {
		f.mu.Unlock()
		return errors.New("ssh: handshake failed: ssh: unable to authenticate, attempted methods [none password], no supported methods remain")
	}
	h.password = true
	f.mu.Unlock()
	if err := f.Connect(ctx, id); err != nil {
		return err
	}
	if installKey {
		f.mu.Lock()
		h.st.Host.Auth = backend.AuthGenerated
		f.keysInstalled = append(f.keysInstalled, id)
		f.publishLocked()
		f.mu.Unlock()
	}
	return nil
}

// KeysInstalled lists the hosts ConnectWithPassword installed the app key on.
func (f *Fake) KeysInstalled() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.keysInstalled)
}

// PublicKey implements backend.Backend.
func (f *Fake) PublicKey(ctx context.Context) (string, error) { return PublicKeyLine, nil }

// ImportSSHConfig implements backend.Backend.
func (f *Fake) ImportSSHConfig(ctx context.Context) ([]backend.Host, error) {
	return []backend.Host{
		{ID: "nas", Name: "nas", Addr: "nas.local", Port: 22, User: "admin", Auth: backend.AuthAgent, SSHAlias: "nas"},
		{ID: "gpu-box", Name: "gpu-box", Addr: "gpu-box.tail1234.ts.net", Port: 22, User: "jose", Auth: backend.AuthKeyFile,
			KeyFile: "~/.ssh/id_ed25519", SSHAlias: "gpu"},
	}, nil
}

// Projects implements backend.Backend.
func (f *Fake) Projects(ctx context.Context, hostID string) ([]proto.AliasInfo, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	h := f.hostLocked(hostID)
	if h == nil {
		return nil, proto.Errorf(proto.CodeNotFound, "no such host")
	}
	return slices.Clone(h.projects), nil
}

// ---- mailbox ----

// Inbox implements backend.Backend: every message of the host (the mailbox
// screen shows them all, as a human peek).
func (f *Fake) Inbox(ctx context.Context, hostID string) ([]proto.Message, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if h := f.hostLocked(hostID); h == nil || h.st.Capabilities == nil || !h.st.Capabilities.Has("agentd") {
		return nil, errors.New("no agentd on host")
	}
	return slices.Clone(f.messages), nil
}

// Queues implements backend.Backend.
func (f *Fake) Queues(ctx context.Context, hostID string) ([]proto.QueueCount, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	counts := map[string]int{}
	var names []string
	for _, m := range f.messages {
		if m.Queue == "" {
			continue
		}
		if _, ok := counts[m.Queue]; !ok {
			names = append(names, m.Queue)
			counts[m.Queue] = 0
		}
		if m.State == "unclaimed" {
			counts[m.Queue]++
		}
	}
	slices.Sort(names)
	var out []proto.QueueCount
	for _, n := range names {
		out = append(out, proto.QueueCount{Name: n, Unclaimed: counts[n]})
	}
	return out, nil
}

// Send implements backend.Backend.
func (f *Fake) Send(ctx context.Context, hostID string, args proto.SendArgs) (*proto.Message, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sent = append(f.sent, args)
	if strings.TrimSpace(args.To) == "" || strings.TrimSpace(args.Body) == "" {
		return nil, proto.Errorf(proto.CodeInvalid, "to and body are required")
	}
	m := proto.Message{ID: f.nextMsg, Sender: "human:jose", Recipient: args.To, Subject: args.Subject, Body: args.Body,
		ReplyTo: args.ReplyTo, State: "queued", CreatedAt: time.Now()}
	if q, ok := strings.CutPrefix(args.To, "queue:"); ok {
		m.Queue, m.State = q, "unclaimed"
	}
	f.nextMsg++
	f.messages = append(f.messages, m)
	return &m, nil
}

// ---- provisioning ----

// Detect implements backend.Backend.
func (f *Fake) Detect(ctx context.Context, hostID string) (*backend.Capabilities, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	h := f.hostLocked(hostID)
	if h == nil || h.st.State != backend.HostOnline || h.st.Capabilities == nil {
		return nil, errors.New("host offline")
	}
	c := *h.st.Capabilities
	c.Tools = map[string]backend.Tool{}
	for k, v := range h.st.Capabilities.Tools {
		c.Tools[k] = v
	}
	return &c, nil
}

var cliPackages = map[string]string{
	"claude":   "npm i -g @anthropic-ai/claude-code",
	"codex":    "npm i -g @openai/codex",
	"opencode": "npm i -g opencode-ai",
	"gemini":   "npm i -g @google/gemini-cli",
	"pando":    "curl -fsSL https://github.com/digiogithub/pando/releases/latest/download/install.sh | sh",
}

// Plan implements backend.Backend.
func (f *Fake) Plan(caps *backend.Capabilities, opts backend.ProvisionOptions) []backend.PlannedStep {
	arch := "amd64"
	if caps != nil && (caps.Arch == "aarch64" || caps.Arch == "arm64") {
		arch = "arm64"
	}
	var out []backend.PlannedStep
	if opts.Agentd {
		out = append(out,
			backend.PlannedStep{Step: "download", Title: "Descargar agentd 0.6.2", Command: "curl -fsSLO …/agentd_0.6.2_linux_" + arch + ".tar.gz"},
			backend.PlannedStep{Step: "verify", Title: "Comprobar la firma", Command: "sha256sum -c checksums.txt"},
			backend.PlannedStep{Step: "install", Title: "Instalar en ~/.local/bin", Command: "tar -xzf agentd_0.6.2_linux_" + arch + ".tar.gz -C ~/.local/bin agentd"},
			backend.PlannedStep{Step: "service", Title: "Servicio systemd de usuario", Command: "systemctl --user enable --now agentd"},
		)
		if opts.HCListener {
			out = append(out, backend.PlannedStep{Step: "hc_listener", Title: "Listener herdr-core", Command: "agentd hc install-shim && printf '[herdr]\\nlisten = true\\n' >> ~/.config/agentd/config.toml"})
		}
		if opts.MCP {
			out = append(out, backend.PlannedStep{Step: "mcp", Title: "MCP de agentd en los CLIs", Command: "claude mcp add agentd -- agentd mcp"})
		}
	}
	for _, c := range opts.CLIs {
		out = append(out, backend.PlannedStep{Step: "cli:" + c, Title: "Instalar " + c, Command: cliPackages[c]})
	}
	return out
}

// Install implements backend.Backend: each planned step reports a start
// and a done event; with FailStep set to a step name that step fails.
func (f *Fake) Install(ctx context.Context, hostID string, opts backend.ProvisionOptions) (<-chan backend.ProvisionEvent, error) {
	f.mu.Lock()
	f.installs = append(f.installs, opts)
	h := f.hostLocked(hostID)
	var caps *backend.Capabilities
	if h != nil {
		caps = h.st.Capabilities
	}
	f.mu.Unlock()
	if caps == nil {
		return nil, errors.New("host offline")
	}
	steps := f.Plan(caps, opts)
	ch := make(chan backend.ProvisionEvent)
	go func() {
		defer close(ch)
		send := func(e backend.ProvisionEvent) bool {
			select {
			case ch <- e:
				return true
			case <-ctx.Done():
				return false
			}
		}
		for _, s := range steps {
			if !send(backend.ProvisionEvent{Step: s.Step, Message: s.Title}) {
				return
			}
			if f.Delay > 0 {
				select {
				case <-time.After(f.Delay):
				case <-ctx.Done():
					return
				}
			}
			if !send(backend.ProvisionEvent{Step: s.Step, Message: s.Title, Done: true}) {
				return
			}
		}
		f.mu.Lock()
		if opts.Agentd {
			caps.Tools["agentd"] = backend.Tool{Name: "agentd", Installed: true, Version: "0.6.2", Path: "~/.local/bin/agentd"}
			caps.AgentdSocket = "/run/user/1000/agentd/agentd.sock"
			h.st.AgentdVer = "0.6.2"
		}
		for _, c := range opts.CLIs {
			caps.Tools[c] = backend.Tool{Name: c, Installed: true, Version: "1.0", Path: "/usr/bin/" + c}
		}
		f.publishLocked()
		f.mu.Unlock()
	}()
	return ch, nil
}

// ---- terminal ----

// Screen is the canned screen a fake agent terminal starts with (the
// Terminal mockup).
const Screen = "\x1b[2J\x1b[H" +
	"\x1b[32m●\x1b[0m Bash(go test ./internal/... -race)\r\n" +
	"  ⎿ ok  internal/session  3.912s\r\n" +
	"    ok  internal/daemon   8.104s\r\n" +
	"    ok  internal/hcompat  5.633s\r\n\r\n" +
	"\x1b[32m●\x1b[0m Update(internal/daemon/env.go)\r\n" +
	"  ⎿ Updated with 9 additions\r\n" +
	"\x1b[32m    + env = append(env, \"HC_TERMINAL_ID=\"+name)\x1b[0m\r\n" +
	"\x1b[32m    + env = append(env, \"HC_SESSION=\"+hc.Session)\x1b[0m\r\n\r\n" +
	"\x1b[33m✳ Revisando la entrada del hijo… (38s · esc para interrumpir)\x1b[0m\r\n" +
	"────────────────────────────────────────────\r\n" +
	"❯ \x1b[2mTry \"run the hc listener tests\"\x1b[0m\r\n" +
	"────────────────────────────────────────────\r\n" +
	"  \x1b[2m? for shortcuts\x1b[0m\r\n"

// Terminal implements backend.Backend: a PTY showing Screen that echoes
// what is typed (observe mode drops writes, as agentd does).
func (f *Fake) Terminal(ctx context.Context, s backend.Session, cols, rows int, observe bool) (backend.PTY, error) {
	f.mu.Lock()
	r := f.findLocked(s)
	f.mu.Unlock()
	if r == nil {
		return nil, proto.Errorf(proto.CodeNotFound, "no such session %q", s.Name)
	}
	p := newPTY(f, observe)
	screen := Screen
	if s.Source == backend.SourceShell {
		screen = "\x1b[2J\x1b[H" + s.HostID + ":~$ "
	}
	go p.out(screen)
	return p, nil
}

type pty struct {
	f       *Fake
	observe bool
	pr      *io.PipeReader
	pw      *io.PipeWriter
	mu      sync.Mutex
	closed  bool
	done    chan struct{}
	cols    int
	rows    int
}

func newPTY(f *Fake, observe bool) *pty {
	pr, pw := io.Pipe()
	return &pty{f: f, observe: observe, pr: pr, pw: pw, done: make(chan struct{})}
}

func (p *pty) out(s string) { _, _ = p.pw.Write([]byte(s)) }

func (p *pty) Read(b []byte) (int, error) { return p.pr.Read(b) }

func (p *pty) Write(b []byte) (int, error) {
	p.mu.Lock()
	closed := p.closed
	p.mu.Unlock()
	if closed {
		return 0, io.ErrClosedPipe
	}
	if p.observe {
		return len(b), nil
	}
	p.f.mu.Lock()
	p.f.typed = append(p.f.typed, string(b))
	p.f.mu.Unlock()
	echo := strings.ReplaceAll(string(b), "\r", "\r\n")
	go p.out(echo)
	return len(b), nil
}

func (p *pty) Resize(cols, rows int) error {
	p.mu.Lock()
	p.cols, p.rows = cols, rows
	p.mu.Unlock()
	p.f.mu.Lock()
	p.f.resizes = append(p.f.resizes, [2]int{cols, rows})
	p.f.mu.Unlock()
	return nil
}

// Resizes returns the sizes asked of the fake PTYs, oldest first.
func (f *Fake) Resizes() [][2]int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([][2]int(nil), f.resizes...)
}

func (p *pty) Close() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return nil
	}
	p.closed = true
	close(p.done)
	_ = p.pw.Close()
	return p.pr.Close()
}

func (p *pty) Wait() (int, error) {
	<-p.done
	return 0, nil
}

// fakeDirs is the in-memory tree of ListDir (every host shares it).
var fakeDirs = []string{
	"/etc", "/srv", "/root", "/tmp",
	"/home/jose/src/webapp", "/home/jose/src/api", "/home/jose/src/agentd", "/home/jose/src/docs", "/home/jose/src/infra",
	"/home/jose/Documentos", "/home/jose/.config", "/home/jose/private",
	"/home/dev/src/api", "/home/dev/src/infra", "/home/shared",
}

// fakeLocked are the directories the user cannot enter.
var fakeLocked = map[string]bool{"/root": true, "/home/jose/private": true}

// ListDir implements backend.Backend with a fixed in-memory tree: /root and
// ~/private are not enterable, "/" is above the home.
func (f *Fake) ListDir(ctx context.Context, hostID, path string, hidden bool) (backend.DirListing, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	h := f.hostLocked(hostID)
	if h == nil || h.st.State != backend.HostOnline {
		return backend.DirListing{}, errors.New("host offline")
	}
	home := "/home/jose"
	if h.st.Capabilities != nil && h.st.Capabilities.Home != "" {
		home = h.st.Capabilities.Home
	}
	rec := hostID + ":" + path
	if hidden {
		rec += "+h"
	}
	f.listed = append(f.listed, rec)
	p := path
	switch {
	case p == "":
		p = home
	case p == "~":
		p = home
	case strings.HasPrefix(p, "~/"):
		p = home + p[1:]
	}
	p = strings.TrimRight(p, "/")
	if p == "" {
		p = "/"
	}
	known := p == "/"
	prefix := p + "/"
	if p == "/" {
		prefix = "/"
	}
	set := map[string]bool{}
	for _, d := range fakeDirs {
		if d == p || strings.HasPrefix(d, prefix) && p != "/" || p == "/" {
			known = true
		}
		if rest, ok := strings.CutPrefix(d, prefix); ok && rest != "" {
			set[strings.SplitN(rest, "/", 2)[0]] = true
		}
	}
	if p == "/" {
		set["home"] = true
	}
	if !known {
		return backend.DirListing{}, backend.ErrDirNotFound
	}
	if fakeLocked[p] {
		return backend.DirListing{}, backend.ErrDirDenied
	}
	l := backend.DirListing{Path: p, Home: home}
	for n := range set {
		if strings.HasPrefix(n, ".") && !hidden {
			continue
		}
		child := prefix + n
		if p == "/" {
			child = "/" + n
		}
		l.Entries = append(l.Entries, backend.DirEntry{Name: n, Enterable: !fakeLocked[child]})
	}
	slices.SortFunc(l.Entries, func(a, b backend.DirEntry) int {
		return strings.Compare(strings.ToLower(a.Name), strings.ToLower(b.Name))
	})
	return l, nil
}
