package proto

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"sync"
)

// Frame types of an attach stream.
const (
	FrameData   byte = 'D' // terminal bytes (both directions)
	FrameResize byte = 'R' // client → daemon: uint16 cols, uint16 rows (big-endian)
	FrameDetach byte = 'X' // either side: detach / close
	FrameExit   byte = 'E' // daemon → client: session exited, payload = exit code as text
	// FrameAuto (client → daemon) carries terminal-generated replies (query
	// answers): written to the child unchanged, never human input, never
	// taking size ownership.
	FrameAuto byte = 'A'
	// FrameFocus (client → daemon): 1 byte, 1 = the client's window gained
	// focus, 0 = lost it.
	FrameFocus byte = 'F'
)

// FrameHeaderSize is the size of a frame header: type + big-endian length.
const FrameHeaderSize = 5

// MaxFrameSize is the largest accepted frame payload.
const MaxFrameSize = 1 << 20

// Frame errors.
var (
	ErrFrameTooLarge   = errors.New("proto: frame too large")
	ErrUnknownFrame    = errors.New("proto: unknown frame type")
	ErrInvalidResize   = errors.New("proto: invalid resize frame")
	ErrInvalidExitCode = errors.New("proto: invalid exit frame")
)

// Frame is one decoded frame.
type Frame struct {
	Type    byte
	Payload []byte
}

func knownFrame(t byte) bool {
	switch t {
	case FrameData, FrameResize, FrameDetach, FrameExit, FrameAuto, FrameFocus:
		return true
	}
	return false
}

// WriteFrame writes one frame. Payloads larger than MaxFrameSize are rejected
// (use FrameWriter.Data to split data).
func WriteFrame(w io.Writer, typ byte, payload []byte) error {
	if !knownFrame(typ) {
		return fmt.Errorf("%w: %q", ErrUnknownFrame, typ)
	}
	if len(payload) > MaxFrameSize {
		return ErrFrameTooLarge
	}
	buf := make([]byte, FrameHeaderSize+len(payload))
	buf[0] = typ
	binary.BigEndian.PutUint32(buf[1:5], uint32(len(payload)))
	copy(buf[FrameHeaderSize:], payload)
	_, err := w.Write(buf)
	return err
}

// ReadFrame reads one frame. It rejects unknown types and payloads larger than
// MaxFrameSize before allocating, and validates R and E payloads. A clean EOF
// before the header returns io.EOF; a truncated frame io.ErrUnexpectedEOF.
func ReadFrame(r io.Reader) (Frame, error) {
	var hdr [FrameHeaderSize]byte
	if _, err := io.ReadFull(r, hdr[:]); err != nil {
		return Frame{}, err
	}
	f := Frame{Type: hdr[0]}
	if !knownFrame(f.Type) {
		return Frame{}, fmt.Errorf("%w: 0x%02x", ErrUnknownFrame, f.Type)
	}
	n := binary.BigEndian.Uint32(hdr[1:5])
	if n > MaxFrameSize {
		return Frame{}, ErrFrameTooLarge
	}
	f.Payload = make([]byte, n)
	if _, err := io.ReadFull(r, f.Payload); err != nil {
		if errors.Is(err, io.EOF) {
			err = io.ErrUnexpectedEOF
		}
		return Frame{}, err
	}
	switch f.Type {
	case FrameResize:
		if _, _, err := ParseResize(f.Payload); err != nil {
			return Frame{}, err
		}
	case FrameExit:
		if _, err := ParseExit(f.Payload); err != nil {
			return Frame{}, err
		}
	}
	return f, nil
}

// ResizePayload encodes an R payload.
func ResizePayload(cols, rows uint16) []byte {
	b := make([]byte, 4)
	binary.BigEndian.PutUint16(b[0:2], cols)
	binary.BigEndian.PutUint16(b[2:4], rows)
	return b
}

// ParseResize decodes an R payload. Zero dimensions are invalid.
func ParseResize(p []byte) (cols, rows uint16, err error) {
	if len(p) != 4 {
		return 0, 0, ErrInvalidResize
	}
	cols = binary.BigEndian.Uint16(p[0:2])
	rows = binary.BigEndian.Uint16(p[2:4])
	if cols == 0 || rows == 0 {
		return 0, 0, ErrInvalidResize
	}
	return cols, rows, nil
}

// ExitPayload encodes an E payload.
func ExitPayload(code int) []byte { return []byte(strconv.Itoa(code)) }

// ParseExit decodes an E payload.
func ParseExit(p []byte) (int, error) {
	if len(p) == 0 || len(p) > 12 {
		return 0, ErrInvalidExitCode
	}
	code, err := strconv.Atoi(strings.TrimSpace(string(p)))
	if err != nil {
		return 0, ErrInvalidExitCode
	}
	return code, nil
}

// FrameWriter serializes frame writes from several goroutines.
type FrameWriter struct {
	mu sync.Mutex
	w  io.Writer
}

// NewFrameWriter wraps w.
func NewFrameWriter(w io.Writer) *FrameWriter { return &FrameWriter{w: w} }

// Write writes one frame.
func (fw *FrameWriter) Write(typ byte, payload []byte) error {
	fw.mu.Lock()
	defer fw.mu.Unlock()
	return WriteFrame(fw.w, typ, payload)
}

// Data writes p as one or more D frames of at most MaxFrameSize bytes.
func (fw *FrameWriter) Data(p []byte) error {
	fw.mu.Lock()
	defer fw.mu.Unlock()
	for len(p) > 0 {
		n := min(len(p), MaxFrameSize)
		if err := WriteFrame(fw.w, FrameData, p[:n]); err != nil {
			return err
		}
		p = p[n:]
	}
	return nil
}

// Resize writes an R frame.
func (fw *FrameWriter) Resize(cols, rows uint16) error {
	return fw.Write(FrameResize, ResizePayload(cols, rows))
}

// Detach writes an X frame.
func (fw *FrameWriter) Detach() error { return fw.Write(FrameDetach, nil) }

// Exit writes an E frame.
func (fw *FrameWriter) Exit(code int) error { return fw.Write(FrameExit, ExitPayload(code)) }
