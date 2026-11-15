// Package apptest runs the agentd-desktop UI headless on the fake backend
// for the area tests: a context with a queue dispatcher, the root view and
// a MyGo tester, plus helpers that let background work land.
package apptest

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/digiogithub/agentd/internal/desktopui/app"
	"github.com/digiogithub/agentd/internal/desktopui/backend/fake"
	"github.com/egoist/mygo/ui"
)

// Env is one headless app.
type Env struct {
	T    *testing.T
	A    *app.Context
	F    *fake.Fake
	Q    *app.QueueDispatcher
	Root *app.Root
	UI   *ui.Tester
}

// New starts the app at path in a w×h window on a fresh fake (no delays).
func New(t *testing.T, path string, w, h int) *Env {
	t.Helper()
	f := fake.New()
	f.Delay = 0
	ctx, cancel := context.WithCancel(context.Background())
	a := app.NewContext(ctx, f, nil)
	q := &app.QueueDispatcher{}
	a.SetDispatcher(q)
	if path != "" {
		a.Router.Replace(path)
	}
	root := app.NewRoot(a)
	e := &Env{T: t, A: a, F: f, Q: q, Root: root}
	e.UI = ui.NewTester(root.View, w, h)
	t.Cleanup(func() {
		root.Close()
		cancel()
	})
	e.Settle()
	return e
}

// Settle lets goroutines deliver their updates and redraws until nothing
// is left to apply (at most 2 s).
func (e *Env) Settle() {
	idle := 0
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && idle < 4 {
		if e.Q.Drain() == 0 {
			idle++
		} else {
			idle = 0
		}
		e.UI.Frame()
		time.Sleep(5 * time.Millisecond)
	}
}

// Eventually settles until cond holds (at most d).
func (e *Env) Eventually(d time.Duration, cond func() bool) bool {
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		e.Q.Drain()
		e.UI.Frame()
		if cond() {
			return true
		}
		time.Sleep(10 * time.Millisecond)
	}
	return cond()
}

// Click clicks the element with text or label s and settles.
func (e *Env) Click(s string) {
	e.T.Helper()
	if err := e.UI.Click(s); err != nil {
		e.T.Fatalf("click %q: %v; texts %q", s, err, e.UI.Texts())
	}
	e.Settle()
}

// Fill types text into the input below the field label (kit.Field puts
// the label right above its input, which carries the same name).
func (e *Env) Fill(label, text string) {
	e.T.Helper()
	r, ok := e.UI.Find(label)
	if !ok {
		e.T.Fatalf("field %q not found; texts %q", label, e.UI.Texts())
	}
	e.UI.ClickAt(r.X+20, r.Y+r.H+6+22)
	e.Settle()
	e.UI.Type(text)
	e.Settle()
}

// Has fails the test when a text is missing.
func (e *Env) Has(texts ...string) {
	e.T.Helper()
	for _, s := range texts {
		if !e.UI.HasText(s) {
			e.T.Errorf("text %q not shown; texts %q", s, e.UI.Texts())
		}
	}
}

// UseGhosttyVT points MYGO_GHOSTTY_VT at the library under
// internal/desktopui/natives/<goos>-<goarch> when it was fetched, and
// reports whether one is available.
func UseGhosttyVT(t *testing.T) bool {
	if os.Getenv("MYGO_GHOSTTY_VT") != "" {
		return true
	}
	_, file, _, _ := runtime.Caller(0)
	dir := filepath.Join(filepath.Dir(file), "..", "..", "natives", runtime.GOOS+"-"+runtime.GOARCH)
	for _, n := range []string{"libghostty-vt.so", "libghostty-vt.dylib", "ghostty-vt.dll"} {
		if p := filepath.Join(dir, n); fileExists(p) {
			t.Setenv("MYGO_GHOSTTY_VT", p)
			return true
		}
	}
	return false
}

func fileExists(p string) bool { _, err := os.Stat(p); return err == nil }
