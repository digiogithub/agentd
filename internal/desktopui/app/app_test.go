package app

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/digiogithub/agentd/internal/desktopui/backend"
	"github.com/digiogithub/agentd/internal/desktopui/backend/fake"
	"github.com/digiogithub/agentd/internal/desktopui/kit"
	"github.com/digiogithub/agentd/internal/proto"
)

func TestBatchCoalescesPushes(t *testing.T) {
	q := &QueueDispatcher{}
	var got [][]int
	b := NewBatch(q.Update, func(items []int) { got = append(got, items) })
	var wg sync.WaitGroup
	for i := range 100 {
		wg.Add(1)
		go func() { defer wg.Done(); b.Push(i) }()
	}
	wg.Wait()
	q.Drain()
	if len(got) != 1 || len(got[0]) != 100 {
		t.Errorf("batches %d", len(got))
	}
}

func TestPermissionDetection(t *testing.T) {
	f := fake.New()
	perm := f.View("claude@webapp")
	if !IsPermission(perm) || AllowOption(perm).ID != "w1" || DenyOption(perm).ID != "w3" {
		t.Errorf("claude@webapp permission: %v %+v %+v", IsPermission(perm), AllowOption(perm), DenyOption(perm))
	}
	q := f.View("codex@api")
	if IsPermission(q) || !Pending(q) {
		t.Errorf("codex@api is a question with options")
	}
	if IsPermission(f.View("pando@agentd")) || Pending(f.View("pando@agentd")) {
		t.Error("working session pending")
	}
	yn := &proto.View{Dialog: &proto.ViewDialog{Question: "Continue?"}, Options: []proto.ViewOption{{ID: "y", Label: "y", Kind: "yesno"}}}
	if !IsPermission(yn) {
		t.Error("yes/no menu is a permission")
	}
}

func TestGroupOf(t *testing.T) {
	cases := map[string]Group{"waiting": GroupNeedsAnswer, "working": GroupWorking, "starting": GroupWorking, "idle": GroupDone, "exited": GroupDone}
	for st, g := range cases {
		if got := GroupOf(backend.Session{Status: st}, nil); got != g {
			t.Errorf("%s: %d, want %d", st, got, g)
		}
	}
}

func TestFormat(t *testing.T) {
	now := time.Date(2026, 11, 15, 12, 0, 0, 0, time.UTC)
	for d, want := range map[time.Duration]string{10 * time.Second: "ahora", 3 * time.Minute: "hace 3 min", 2 * time.Hour: "hace 2 h",
		26 * time.Hour: "hace 1 día", 50 * time.Hour: "hace 2 días"} {
		if got := RelativeTime(now.Add(-d), now); got != want {
			t.Errorf("%v: %q, want %q", d, got, want)
		}
	}
	if Plural(1, "sesión", "sesiones") != "1 sesión" || Plural(7, "sesión", "sesiones") != "7 sesiones" {
		t.Error("plural")
	}
	s := backend.Session{HostID: "estudio", Source: backend.SourceAgentd, Name: "codex@api", Harness: "codex"}
	if AvatarLetter(s) != "X" || SessionTitle(s) != "codex@api" {
		t.Error("avatar/title")
	}
	if SessionTitle(backend.Session{Source: backend.SourceZellij, Name: "main"}) != "zellij · main" {
		t.Error("mux title")
	}
	p := SessionPath(s)
	if p != "/agents/s/estudio/agentd/codex@api" {
		t.Errorf("path %q", p)
	}
	if SessionKeyFromRoute("estudio", "agentd", "codex@api") != s.Key() {
		t.Error("key from route")
	}
}

func TestConflictViewAndHostKey(t *testing.T) {
	f := fake.New()
	s, _ := f.SessionByName("codex@api")
	_, err := f.Act(context.Background(), s, proto.ActArgs{Name: s.Name, Action: "choose", OptionID: "a1", Revision: 1, Epoch: fake.Epoch})
	if proto.CodeOf(err) != proto.CodeConflict {
		t.Fatalf("err %v", err)
	}
	if v := ConflictView(err); v == nil || v.Revision != 57 {
		t.Errorf("conflict view %+v", v)
	}
	if ErrorText(err) != "El menú cambió: revisa la pregunta actualizada" {
		t.Errorf("text %q", ErrorText(err))
	}
	if HostKeyFingerprint(&backend.HostKeyError{Fingerprint: "SHA256:x"}) != "SHA256:x" || HostKeyFingerprint(errors.New("x")) != "" {
		t.Error("host key fingerprint")
	}
}

func TestPrefsPersist(t *testing.T) {
	p := filepath.Join(t.TempDir(), "d", "settings.json")
	a := NewPrefs(p)
	if !a.Get().NotifyWaiting || a.Get().Theme != kit.ModeDark {
		t.Errorf("defaults %+v", a.Get())
	}
	if err := a.Update(func(s *Settings) { s.NotifyExit, s.Theme = true, kit.ModeLight }); err != nil {
		t.Fatal(err)
	}
	b := NewPrefs(p)
	if !b.Get().NotifyExit || b.Get().Theme != kit.ModeLight {
		t.Errorf("reloaded %+v", b.Get())
	}
}

type fakeShell struct {
	focused bool
	sent    []string
}

func (f *fakeShell) Notify(id, title, body string, onClick func()) {
	f.sent = append(f.sent, title+"|"+body)
}
func (f *fakeShell) Focused() bool { return f.focused }

func TestNotifierTransitions(t *testing.T) {
	a := NewContext(context.Background(), nil, nil)
	sh := &fakeShell{}
	a.SetShell(sh)
	n := StartNotifier(a)
	now := time.Now()
	n.Now = func() time.Time { return now }
	s := backend.Session{HostID: "h", Source: backend.SourceAgentd, Name: "claude@x", Status: "working"}
	emit := func(st string) {
		prev := a.Live.Sessions
		next := s
		next.Status = st
		a.Live.Sessions = []backend.Session{next}
		if prev == nil {
			prev = []backend.Session{}
		}
		for _, fn := range a.Live.OnSessions {
			fn(prev, a.Live.Sessions)
		}
	}
	a.Live.Sessions = []backend.Session{s}
	emit("waiting")
	emit("working")
	emit("idle")
	emit("working")
	emit("idle") // within 30 s: grouped
	emit("exited")
	if len(sh.sent) != 2 || sh.sent[0] != "claude@x|Espera tu respuesta" || sh.sent[1] != "claude@x|Turno terminado" {
		t.Errorf("sent %q", sh.sent)
	}
	// Muted sessions and a focused window get nothing.
	_ = a.Prefs.Update(func(st *Settings) { st.Muted = []Mute{{Session: s.Key(), Until: now.Add(time.Hour)}} })
	emit("waiting")
	sh.focused = true
	_ = a.Prefs.Update(func(st *Settings) { st.Muted = nil })
	emit("working")
	emit("waiting")
	if len(sh.sent) != 2 {
		t.Errorf("sent %q", sh.sent)
	}
}
