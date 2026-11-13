// Package screen defines Snapshot, an immutable, self-contained copy of
// everything the interpretation layers (reflow, regions, status, options) need
// from one session at one instant: the visible cells, the cursor, the
// scrollback tail, the terminal modes, the activity timestamps and the
// notification counters. internal/session fills it atomically under its output
// lock; this package depends only on the standard library and holds no
// reference to the live emulator, so a Snapshot can be read from any goroutine.
//
// Design: docs/design/api/semantic-model.md §2.
package screen

import (
	"strings"
	"time"
)

// Attr is a bit set of cell text attributes.
type Attr uint16

// Cell attributes. RapidBlink is folded into Blink.
const (
	Bold Attr = 1 << iota
	Faint
	Italic
	Underline
	Blink
	Reverse
	Conceal
	Strike
)

var attrNames = []struct {
	a    Attr
	name string
}{
	{Bold, "bold"}, {Faint, "faint"}, {Italic, "italic"}, {Underline, "underline"},
	{Blink, "blink"}, {Reverse, "reverse"}, {Conceal, "conceal"}, {Strike, "strike"},
}

// Has reports whether every bit of b is set in a.
func (a Attr) Has(b Attr) bool { return a&b == b }

// Names returns the attribute names in a stable order.
func (a Attr) Names() []string {
	var out []string
	for _, n := range attrNames {
		if a&n.a != 0 {
			out = append(out, n.name)
		}
	}
	return out
}

// Cell is one screen cell.
type Cell struct {
	Text string // grapheme; "" for an empty cell (blank or unwritten)
	// Width is 1, 2 for a wide character's first cell, or 0 for its
	// continuation cell (empty Text), so a cell's index in Row.Cells is its
	// column.
	Width uint8
	Attrs Attr
	// Fg and Bg: "" default, "N" palette index, "#rrggbb" (same encoding as
	// session.CellInfo).
	Fg, Bg string
}

// Blank reports whether the cell shows nothing: no text and no background or
// reverse-video styling. A wide character's continuation cell (Width 0) is
// not blank when the cell it continues is styled; callers that need that
// look at the previous cell.
func (c Cell) Blank() bool {
	return c.Text == "" && c.Bg == "" && !c.Attrs.Has(Reverse)
}

// Continuation reports whether the cell has Width 0, i.e. it is the second
// column of the wide character before it.
func (c Cell) Continuation() bool { return c.Width == 0 }

// Row is one terminal row.
type Row struct {
	// Cells has at most Cols entries and cell index == column: the
	// continuation cell after a wide (Width 2) character is kept with Width 0.
	// Scrollback rows are already trimmed by the emulator (fewer than Cols).
	Cells []Cell
	// Wrapped: the emulator's autowrap continued this row onto the next. Always
	// false with x/vt v0.1.0, which keeps no wrap flag (design §3.1).
	Wrapped bool
}

// Text returns the row's text: empty cells become spaces (continuation cells
// of wide characters contribute nothing), trailing spaces are trimmed.
func (r Row) Text() string {
	var b strings.Builder
	for i, c := range r.Cells {
		if c.Width == 0 && c.Text == "" && i > 0 && r.Cells[i-1].Width == 2 {
			continue
		}
		if c.Text == "" {
			b.WriteByte(' ')
		} else {
			b.WriteString(c.Text)
		}
	}
	return strings.TrimRight(b.String(), " ")
}

// Pos is a 0-based screen coordinate.
type Pos struct{ X, Y int }

// Notify is the last desktop-notification style message (OSC 9, OSC 777;notify).
type Notify struct {
	At          time.Time
	Title, Body string
}

// Session states carried in Snapshot.State.
const (
	StateStarting = "starting"
	StateRunning  = "running"
	StateExited   = "exited"
)

// Caps applied to the strings a child controls (title, cwd, notifications).
const (
	MaxTitleLen = 256  // runes, title and notification title/body
	MaxCwdLen   = 4096 // runes
)

// Sanitize strips C0/C1 control characters (and DEL) from s and truncates it
// to max runes. Used on text the child process controls.
func Sanitize(s string, max int) string {
	clean := true
	for _, r := range s {
		if r < 0x20 || (r >= 0x7f && r <= 0x9f) {
			clean = false
			break
		}
	}
	if clean && len(s) <= max {
		return s
	}
	var b strings.Builder
	n := 0
	for _, r := range s {
		if r < 0x20 || (r >= 0x7f && r <= 0x9f) {
			continue
		}
		if n++; n > max {
			break
		}
		b.WriteRune(r)
	}
	return b.String()
}

// DefaultScrollbackTail is the scrollback tail length used when SnapOpts asks
// for the default.
const DefaultScrollbackTail = 500

// SnapOpts tunes what Snapshot copies.
type SnapOpts struct {
	// ScrollbackTail is the maximum number of main-screen scrollback rows
	// copied (the newest ones). 0 = none (the zero value: cheap polls);
	// negative = DefaultScrollbackTail. Only these rows are copied under the
	// session's output lock.
	ScrollbackTail int
}

// Snapshot is a consistent copy of one session. Callers may keep and read it
// freely from any goroutine, but must treat it as immutable: Screen and
// Scrollback (rows and cells) may be shared between several Snapshots of the
// same session taken without new output in between (session.Snapshot caches per
// OutputSeq), so modifying them corrupts the other readers.
type Snapshot struct {
	Taken time.Time
	// OutputSeq counts the PTY output chunks fed to the emulator when the
	// snapshot was taken (Session.OutputGen).
	OutputSeq    uint64
	Cols, Rows   int
	Cursor       Pos // 0-based, screen coordinates
	CursorHidden bool
	AltScreen    bool
	Screen       []Row // len == Rows
	// Scrollback is the tail of the main-screen scrollback, oldest first.
	Scrollback []Row
	// ScrollbackLen is the total scrollback line count at capture.
	ScrollbackLen int
	Title         string // OSC 0/2, control characters stripped, at most MaxTitleLen runes
	// Cwd is the directory announced by OSC 7 if seen, else the child's
	// /proc/<pid>/cwd (Linux), else the spawn directory. Control characters
	// are stripped and it is capped at MaxCwdLen runes.
	Cwd            string
	AppCursor      bool // DECCKM: arrows are sent as SS3 (ESC O A)
	BracketedPaste bool
	// Synchronized output (DEC 2026): SyncOpen = a BSU is open (and younger
	// than the session's SyncTimeout), i.e. Screen is a half-drawn frame;
	// SyncSince is when it opened; SyncFrames counts completed updates (0 =
	// the child does not use 2026). Zero in a Replayer snapshot.
	SyncOpen   bool
	SyncSince  time.Time
	SyncFrames uint64

	// Activity (same sources as the delivery idle rule).
	State                                        string
	ExitCode                                     *int
	StartedAt, LastOutput, LastHuman, DraftSince time.Time

	// Notifications since the session started.
	Bells      uint64
	LastBell   time.Time
	Notifies   uint64 // OSC 9 free-text notifications (numeric ConEmu/WT sub-commands such as 9;4 are excluded) and OSC 777;notify
	LastNotify Notify
}

// Lines returns the plain text of the visible screen, one entry per row.
func (s *Snapshot) Lines() []string {
	out := make([]string, len(s.Screen))
	for i, r := range s.Screen {
		out[i] = r.Text()
	}
	return out
}

// Text returns the visible screen as plain text joined by "\n".
func (s *Snapshot) Text() string { return strings.Join(s.Lines(), "\n") }
