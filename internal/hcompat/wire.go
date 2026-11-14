// Package hcompat speaks the herdr-core ("hc") client protocol, generation 1,
// on top of the agentd daemon, so that unmodified Herdr clients (Herdr
// Desktop, Whip...) can list and attach to agentd sessions.
//
// It is a translator only: every hc call becomes one or more daemon ops over
// the agentd socket (internal/client); permissions, identity and terminal
// semantics stay in the daemon. Protocol reference: herdr-core
// schema/README.md and schema/released.openrpc.json (Apache-2.0); analysis in
// docs/architecture/herdr-compatibility-analysis.md.
package hcompat

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
)

// Generation is the hc protocol generation this package speaks.
const Generation = 1

// CompatVersion is the herdr-core release whose released schema this package
// follows. It is what hello and --version report: Herdr Desktop rejects
// cores older than 0.1.11 and parses the version by splitting on '.' and '-'
// (a "+build" suffix would read as 0.1.0), so it stays a plain x.y.z.
const CompatVersion = "0.1.21"

// Frame kinds of framed mode: [channel u32 LE][kind u8][len u32 LE][payload].
const (
	KindData           byte = 0 // channel 0: one JSON-RPC message; other channels: stream bytes
	KindCredit         byte = 1 // u32 LE: bytes the receiver consumed
	KindClose          byte = 2 // the channel is over (JSON reason from the core, empty from a client)
	KindResize         byte = 3 // u16 LE cols, u16 LE rows
	KindFocus          byte = 4 // 1 gained focus, 0 lost it
	KindFacts          byte = 5 // JSON display facts
	KindAutomaticInput byte = 6 // terminal-generated replies, not human activity
)

const (
	// HeaderSize is the size of a frame header.
	HeaderSize = 9
	// MaxPayload is the largest accepted frame payload (16 MiB).
	MaxPayload = 16 << 20
	// Window is the credit a stream starts with.
	Window = 256 * 1024
	// chunk is the largest stream DATA frame this package sends.
	chunk = 16 * 1024
)

// ErrFrameTooLarge is returned for a frame whose payload exceeds MaxPayload.
var ErrFrameTooLarge = errors.New("hcompat: frame too large")

// Frame is one decoded frame.
type Frame struct {
	Channel uint32
	Kind    byte
	Payload []byte
}

// AppendFrame appends the encoding of one frame to dst.
func AppendFrame(dst []byte, channel uint32, kind byte, payload []byte) []byte {
	var h [HeaderSize]byte
	binary.LittleEndian.PutUint32(h[0:4], channel)
	h[4] = kind
	binary.LittleEndian.PutUint32(h[5:9], uint32(len(payload)))
	dst = append(dst, h[:]...)
	return append(dst, payload...)
}

// ReadFrame reads one frame. A clean EOF before a header returns io.EOF.
func ReadFrame(r io.Reader) (Frame, error) {
	var h [HeaderSize]byte
	if _, err := io.ReadFull(r, h[:]); err != nil {
		return Frame{}, err
	}
	n := binary.LittleEndian.Uint32(h[5:9])
	if n > MaxPayload {
		return Frame{}, fmt.Errorf("%w: %d bytes", ErrFrameTooLarge, n)
	}
	f := Frame{Channel: binary.LittleEndian.Uint32(h[0:4]), Kind: h[4], Payload: make([]byte, n)}
	if _, err := io.ReadFull(r, f.Payload); err != nil {
		if errors.Is(err, io.EOF) {
			err = io.ErrUnexpectedEOF
		}
		return Frame{}, err
	}
	return f, nil
}

// CreditPayload encodes a CREDIT payload.
func CreditPayload(n uint32) []byte { return binary.LittleEndian.AppendUint32(nil, n) }

// ResizePayload encodes a RESIZE payload.
func ResizePayload(cols, rows uint16) []byte {
	b := binary.LittleEndian.AppendUint16(nil, cols)
	return binary.LittleEndian.AppendUint16(b, rows)
}

// parseResize decodes a RESIZE payload; zero dimensions are invalid.
func parseResize(p []byte) (cols, rows uint16, ok bool) {
	if len(p) != 4 {
		return 0, 0, false
	}
	cols, rows = binary.LittleEndian.Uint16(p[0:2]), binary.LittleEndian.Uint16(p[2:4])
	return cols, rows, cols > 0 && rows > 0
}
