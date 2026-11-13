package session

import (
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/vt"

	"github.com/digiogithub/agentd/internal/screen"
)

// OutputGen returns the output generation counter: the number of PTY output
// chunks fed to the emulator so far. It increments once per processed chunk
// (after the emulator consumed it) and never decreases, so an unchanged value
// means an unchanged screen. Lock-free.
func (s *Session) OutputGen() uint64 { return s.outputGen.Load() }

// Changed returns a channel that is closed the next time output is processed
// (or the session finishes). After the session exits no further output is
// processed, so the channel never fires again: loops must also select on
// Done(). Typical use: take the channel, read OutputGen or
// Snapshot, then wait on the channel; a change between the two closes the
// channel that was already taken, so no wakeup is lost.
//
//	for {
//		ch := s.Changed()
//		snap := s.Snapshot(opts)
//		... interpret snap ...
//		select { case <-ch: case <-s.Done(): return }
//	}
func (s *Session) Changed() <-chan struct{} {
	s.chMu.Lock()
	defer s.chMu.Unlock()
	return s.changed
}

// signalChanged wakes every waiter on Changed and arms a fresh channel.
func (s *Session) signalChanged() {
	s.chMu.Lock()
	close(s.changed)
	s.changed = make(chan struct{})
	s.chMu.Unlock()
}

// onBell is the emulator Bell callback (runs inside emu.Write, under outMu).
func (s *Session) onBell() {
	s.bells++
	s.lastBell = time.Now()
}

// onWorkingDirectory is the emulator OSC 7 callback (under outMu).
func (s *Session) onWorkingDirectory(p string) {
	s.oscCwd = screen.Sanitize(osc7Path(p), screen.MaxCwdLen)
}

// osc7Path extracts the path from an OSC 7 payload ("file://host/path" or a
// bare path).
func osc7Path(v string) string {
	if strings.HasPrefix(v, "file://") {
		if u, err := url.Parse(v); err == nil && u.Path != "" {
			return u.Path
		}
	}
	return v
}

// conEmuSub matches an OSC 9 body that starts with a numeric sub-command
// (ConEmu / Windows Terminal: "4;1;50", "9;/cwd", "12"); real notifications
// are free text.
var conEmuSub = regexp.MustCompile(`^\d+(;|$)`)

// registerNotifyHandlers hooks OSC 9 and OSC 777 (x/vt only logs them as
// unhandled). Handlers receive the whole payload including the command number
// and run under outMu.
func (s *Session) registerNotifyHandlers() { registerNotify(s.emu.Emulator, s.recordNotify) }

// registerNotify installs the OSC 9 / OSC 777 handlers on e, calling rec for
// every real notification (shared by Session and Replayer).
func registerNotify(e *vt.Emulator, rec func(title, body string)) {
	e.RegisterOscHandler(9, func(data []byte) bool {
		body := strings.TrimPrefix(string(data), "9;")
		if conEmuSub.MatchString(body) {
			return true // ConEmu/Windows Terminal sub-command (9;4 progress, 9;9;cwd, 9;12 ...), not a notification
		}
		rec("", body)
		return true
	})
	e.RegisterOscHandler(777, func(data []byte) bool {
		parts := strings.SplitN(string(data), ";", 4)
		if len(parts) >= 2 && parts[1] == "notify" {
			var title, body string
			if len(parts) > 2 {
				title = parts[2]
			}
			if len(parts) > 3 {
				body = parts[3]
			}
			rec(title, body)
		}
		return true
	})
}

func (s *Session) recordNotify(title, body string) {
	s.notifies++
	s.lastNotify = screen.Notify{
		At: time.Now(), Title: screen.Sanitize(title, screen.MaxTitleLen), Body: screen.Sanitize(body, screen.MaxTitleLen),
	}
}

// Notifications returns the bell and notification counters and last payloads
// (the same values Snapshot reports).
func (s *Session) Notifications() (bells uint64, lastBell time.Time, notifies uint64, last screen.Notify) {
	s.outMu.RLock()
	defer s.outMu.RUnlock()
	return s.bells, s.lastBell, s.notifies, s.lastNotify
}

// snapshotCache keeps the emulator-derived part of the last Snapshot, keyed by
// (OutputSeq, resolved ScrollbackTail). Polling without new output (the
// common case of wait/act loops) reuses it. The cached Snapshot is immutable
// and shared read-only by every caller: Screen, Scrollback and their cells
// must never be modified (screen.Snapshot documents this). A resize, which
// changes the screen without output, bumps gen via reset; put ignores results
// computed before the bump.
type snapshotCache struct {
	mu   sync.Mutex
	gen  uint64
	ok   bool
	seq  uint64
	tail int
	snap *screen.Snapshot
}

func (c *snapshotCache) get(seq uint64, tail int) (*screen.Snapshot, uint64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.ok && c.seq == seq && c.tail == tail {
		return c.snap, c.gen
	}
	return nil, c.gen
}

func (c *snapshotCache) put(gen, seq uint64, tail int, snap *screen.Snapshot) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.gen == gen {
		c.ok, c.seq, c.tail, c.snap = true, seq, tail, snap
	}
}

func (c *snapshotCache) reset() {
	c.mu.Lock()
	c.gen++
	c.ok, c.snap = false, nil
	c.mu.Unlock()
}

// Snapshot copies the interpretable state of the session at one instant,
// under outMu's read lock: screen, cursor, scrollback tail, title, modes and
// the output counter all belong to the same emulator state. Activity
// timestamps are lock-free atomics read right after.
//
// Only the newest opts.ScrollbackTail scrollback rows are copied under the
// lock (0, the zero value, copies none: use it for cheap polls and request a
// tail only when the transcript is needed). The emulator-derived part is cached
// per (OutputSeq, tail): calls without new output share the same Screen and
// Scrollback slices, which are immutable by contract (callers must not modify
// them); the returned *Snapshot itself is a fresh copy with current activity
// fields (Taken, State, LastOutput, LastHuman, DraftSince, ExitCode).
func (s *Session) Snapshot(opts screen.SnapOpts) *screen.Snapshot {
	e := s.emu.Emulator
	tail := opts.ScrollbackTail
	if tail < 0 {
		tail = screen.DefaultScrollbackTail
	}
	opts.ScrollbackTail = tail

	s.outMu.RLock()
	seq := s.outputGen.Load()
	base, gen := s.snapCache.get(seq, tail)
	var raw rawScreen
	fresh := base == nil
	if fresh {
		base = &screen.Snapshot{
			OutputSeq:      seq,
			Title:          s.title,
			Cwd:            s.oscCwd,
			AppCursor:      s.decModes[1],
			BracketedPaste: s.decModes[2004],
			SyncSince:      s.syncSince,
			SyncFrames:     s.syncFrames,
			Bells:          s.bells,
			LastBell:       s.lastBell,
			Notifies:       s.notifies,
			LastNotify:     s.lastNotify,
		}
		raw = captureScreen(e, opts, base)
	}
	s.outMu.RUnlock()

	if fresh {
		raw.convert(base)
		base.Title = screen.Sanitize(base.Title, screen.MaxTitleLen)
		base.Cwd = screen.Sanitize(base.Cwd, screen.MaxCwdLen)
		s.snapCache.put(gen, seq, tail, base)
	}
	snap := new(screen.Snapshot)
	*snap = *base // shares the immutable Screen / Scrollback
	snap.Taken = time.Now()
	snap.SyncOpen = !snap.SyncSince.IsZero() && snap.Taken.Sub(snap.SyncSince) < SyncTimeout

	snap.State = s.State()
	if code, ok := s.ExitCode(); ok {
		snap.ExitCode = &code
	}
	snap.StartedAt = s.startedAt
	snap.LastOutput = s.LastOutput()
	snap.LastHuman = s.LastInput()
	if n := s.draftAt.Load(); n != 0 {
		snap.DraftSince = unixNano(n)
	}
	if snap.Cwd == "" {
		if p, err := os.Readlink("/proc/" + strconv.Itoa(s.pid) + "/cwd"); err == nil && snap.State == StateRunning {
			snap.Cwd = p
		} else {
			snap.Cwd = s.spec.Cwd
		}
	}
	return snap
}

// rawScreen holds verbatim cell copies of an emulator, taken under the
// owner's lock and converted to screen types afterwards.
type rawScreen struct {
	screen, scrollback [][]uv.Cell
}

// captureScreen fills the cheap emulator-derived fields of snap (size, cursor,
// alt screen, scrollback length) and copies the cells verbatim. The caller
// must hold the lock that serializes emulator writes. It is the one code path
// shared by Session.Snapshot and Replayer.Snapshot.
func captureScreen(e *vt.Emulator, opts screen.SnapOpts, snap *screen.Snapshot) rawScreen {
	tail := opts.ScrollbackTail
	if tail < 0 {
		tail = screen.DefaultScrollbackTail
	}
	snap.Cols, snap.Rows = e.Width(), e.Height()
	snap.CursorHidden = e.CursorHidden()
	snap.AltScreen = e.IsAltScreen()
	cur := e.CursorPosition()
	snap.Cursor = screen.Pos{X: cur.X, Y: cur.Y}
	var r rawScreen
	r.screen = make([][]uv.Cell, snap.Rows)
	for y := range r.screen {
		r.screen[y] = rawRow(snap.Cols, func(x int) *uv.Cell { return e.CellAt(x, y) })
	}
	if sb := e.Scrollback(); sb != nil {
		lines := sb.Lines()
		snap.ScrollbackLen = len(lines)
		if tail > 0 {
			if len(lines) > tail {
				lines = lines[len(lines)-tail:]
			}
			r.scrollback = make([][]uv.Cell, len(lines))
			for i, l := range lines {
				l := l
				r.scrollback[i] = rawRow(len(l), func(x int) *uv.Cell { return &l[x] })
			}
		}
	}
	return r
}

// convert does the colour/string conversion (no lock needed) and sets
// snap.Screen and snap.Scrollback.
func (r rawScreen) convert(snap *screen.Snapshot) {
	snap.Screen = make([]screen.Row, len(r.screen))
	for y, row := range r.screen {
		snap.Screen[y] = screen.Row{Cells: convertRow(row)}
	}
	if r.scrollback != nil {
		snap.Scrollback = make([]screen.Row, len(r.scrollback))
		for i, row := range r.scrollback {
			snap.Scrollback[i] = screen.Row{Cells: convertRow(row)}
		}
	}
}

// copyRow converts n cells read through at (see rawRow, convertRow).
func copyRow(n int, at func(x int) *uv.Cell) []screen.Cell { return convertRow(rawRow(n, at)) }

// rawRow copies n cells verbatim (no string or colour conversion, so it is
// cheap enough to run under outMu); an unwritten (nil) cell becomes a blank
// width-1 cell.
func rawRow(n int, at func(x int) *uv.Cell) []uv.Cell {
	out := make([]uv.Cell, n)
	for x := range out {
		if c := at(x); c != nil {
			out[x] = *c
		} else {
			out[x].Width = 1
		}
	}
	return out
}

// convertRow converts copied cells, one per column: a wide character's
// continuation cell stays as a Width 0 cell with empty Text. Blank unstyled
// cells get Text "".
func convertRow(cells []uv.Cell) []screen.Cell {
	out := make([]screen.Cell, len(cells))
	for x := range cells {
		c := &cells[x]
		text := c.Content
		if text == " " {
			text = ""
		}
		w := uint8(c.Width)
		if c.Width < 0 {
			w = 0
		} else if w > 2 {
			w = 2
		}
		if w == 0 {
			text = ""
		}
		out[x] = screen.Cell{
			Text: text, Width: w, Attrs: attrBits(c.Style),
			Fg: colorString(c.Style.Fg), Bg: colorString(c.Style.Bg),
		}
	}
	return out
}

func attrBits(st uv.Style) screen.Attr {
	var a screen.Attr
	set := func(on bool, b screen.Attr) {
		if on {
			a |= b
		}
	}
	set(st.Attrs&uv.AttrBold != 0, screen.Bold)
	set(st.Attrs&uv.AttrFaint != 0, screen.Faint)
	set(st.Attrs&uv.AttrItalic != 0, screen.Italic)
	set(st.Underline != 0, screen.Underline)
	set(st.Attrs&(uv.AttrBlink|uv.AttrRapidBlink) != 0, screen.Blink)
	set(st.Attrs&uv.AttrReverse != 0, screen.Reverse)
	set(st.Attrs&uv.AttrConceal != 0, screen.Conceal)
	set(st.Attrs&uv.AttrStrikethrough != 0, screen.Strike)
	return a
}
