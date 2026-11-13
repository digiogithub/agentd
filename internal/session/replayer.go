package session

import (
	"io"
	"sync"

	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/vt"

	"github.com/digiogithub/agentd/internal/screen"
)

// Replayer is a standalone terminal (no PTY, no child) fed with raw PTY
// output bytes. It applies the same input filter (stringFilter), the same
// emulator callbacks and the same cell conversion as a live Session, so a
// recorded byte stream replays to the Snapshot a live session would have
// produced. Intended for fixtures and tests (internal/testutil/screenfix).
//
// Replay is deterministic: no clock is read, so Taken, LastBell, LastNotify.At
// and the activity timestamps stay zero; State is StateRunning. Feed and
// Snapshot may be called from different goroutines; Close must be called once
// when done.
type Replayer struct {
	mu       sync.RWMutex // plays the role of Session.outMu
	emu      *vt.SafeEmulator
	filt     stringFilter
	buf      []byte
	gen      uint64
	title    string
	cwd      string
	modes    map[int]bool
	bells    uint64
	notifies uint64
	notify   screen.Notify
}

// NewReplayer creates an empty terminal of cols x rows cells.
func NewReplayer(cols, rows int) *Replayer {
	r := &Replayer{emu: vt.NewSafeEmulator(cols, rows), modes: map[int]bool{}}
	// Terminal replies (CPR, DA...) are discarded; nobody reads them in a replay.
	go func() { _, _ = io.Copy(io.Discard, r.emu) }()
	r.emu.SetCallbacks(vt.Callbacks{
		Title:            func(t string) { r.title = t },
		Bell:             func() { r.bells++ },
		WorkingDirectory: func(p string) { r.cwd = screen.Sanitize(osc7Path(p), screen.MaxCwdLen) },
		EnableMode:       func(m ansi.Mode) { r.mode(m, true) },
		DisableMode:      func(m ansi.Mode) { r.mode(m, false) },
	})
	registerNotify(r.emu.Emulator, func(title, body string) {
		r.notifies++
		r.notify = screen.Notify{
			Title: screen.Sanitize(title, screen.MaxTitleLen), Body: screen.Sanitize(body, screen.MaxTitleLen),
		}
	})
	return r
}

func (r *Replayer) mode(m ansi.Mode, on bool) {
	if d, ok := m.(ansi.DECMode); ok {
		r.modes[int(d)] = on
	}
}

// Feed writes one chunk of PTY output; each non-empty chunk is one OutputSeq
// step, like one read of a live session.
func (r *Replayer) Feed(p []byte) {
	if len(p) == 0 {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.buf = r.filt.filter(r.buf[:0], p)
	_, _ = r.emu.Write(r.buf)
	r.gen++
}

// Snapshot copies the current terminal state, the way Session.Snapshot does
// (same helpers); see Replayer for the fields that stay zero.
func (r *Replayer) Snapshot(opts screen.SnapOpts) *screen.Snapshot {
	r.mu.RLock()
	snap := &screen.Snapshot{
		OutputSeq:      r.gen,
		Title:          r.title,
		Cwd:            r.cwd,
		AppCursor:      r.modes[1],
		BracketedPaste: r.modes[2004],
		Bells:          r.bells,
		Notifies:       r.notifies,
		LastNotify:     r.notify,
		State:          screen.StateRunning,
	}
	raw := captureScreen(r.emu.Emulator, opts, snap)
	r.mu.RUnlock()
	raw.convert(snap)
	snap.Title = screen.Sanitize(snap.Title, screen.MaxTitleLen)
	return snap
}

// Close stops the reply drain. It closes the input pipe directly because
// vt.Emulator.Close writes a flag the drain goroutine reads (data race under
// -race).
func (r *Replayer) Close() {
	if pw, ok := r.emu.InputPipe().(*io.PipeWriter); ok {
		_ = pw.Close()
	}
}
