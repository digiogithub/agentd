package session

import (
	"bytes"
	"time"
)

// Frame-aligned chunking (AGED-US-0038). A child that wraps its repaints in
// synchronized updates (DEC 2026) is observed at chunk boundaries: a reader
// that lags (loaded machine, race detector) gets chunks cut at arbitrary
// points, usually inside a frame, so every Snapshot sees an open update and
// the transcript history never samples a complete frame. feed therefore holds
// back the trailing open frame (BSU without its ESU) and processes it together
// with the rest of the frame, so chunks end on frame boundaries whenever the
// child delivers complete frames. A frame that never completes is released
// after syncHold (or syncHoldMax bytes), exactly as before.
const (
	syncHold    = 100 * time.Millisecond
	syncHoldMax = 256 << 10
)

var (
	syncBegin = []byte("\x1b[?2026h")
	syncEnd   = []byte("\x1b[?2026l")
)

// openSyncTail returns the index where the trailing, unterminated synchronized
// update of b starts, or -1 when b does not end inside one.
func openSyncTail(b []byte) int {
	i := bytes.LastIndex(b, syncBegin)
	if i < 0 || bytes.LastIndex(b, syncEnd) > i {
		return -1
	}
	return i
}

// feed processes a PTY chunk, holding back an open trailing frame.
func (s *Session) feed(chunk []byte) {
	s.readMu.Lock()
	defer s.readMu.Unlock()
	if len(s.held) > 0 {
		chunk = append(s.held, chunk...)
		s.held = nil
	}
	if s.holdTimer != nil {
		s.holdTimer.Stop()
	}
	if cut := openSyncTail(chunk); cut >= 0 && len(chunk)-cut < syncHoldMax {
		s.held = chunk[cut:]
		chunk = chunk[:cut]
		s.holdTimer = time.AfterFunc(syncHold, s.flushHeld)
	}
	if len(chunk) > 0 {
		s.handleOutput(chunk)
	}
}

// flushHeld releases the held frame to the emulator, complete or not.
func (s *Session) flushHeld() {
	s.readMu.Lock()
	defer s.readMu.Unlock()
	if s.holdTimer != nil {
		s.holdTimer.Stop()
	}
	if len(s.held) > 0 {
		b := s.held
		s.held = nil
		s.handleOutput(b)
	}
}
