package session

// ring is a fixed-capacity byte ring buffer keeping the most recent bytes
// written to it. It is not safe for concurrent use; Session guards it.
type ring struct {
	buf  []byte
	pos  int  // next write position
	full bool // buffer has wrapped at least once
}

func newRing(size int) *ring {
	return &ring{buf: make([]byte, size)}
}

func (r *ring) Write(p []byte) {
	n := len(r.buf)
	if n == 0 {
		return
	}
	if len(p) >= n {
		copy(r.buf, p[len(p)-n:])
		r.pos = 0
		r.full = true
		return
	}
	c := copy(r.buf[r.pos:], p)
	if c < len(p) {
		copy(r.buf, p[c:])
		r.full = true
	}
	r.pos = (r.pos + len(p)) % n
	if r.pos == 0 && len(p) > 0 {
		r.full = true
	}
}

// Bytes returns a copy of the buffered bytes, oldest first.
func (r *ring) Bytes() []byte {
	if !r.full {
		return append([]byte(nil), r.buf[:r.pos]...)
	}
	out := make([]byte, 0, len(r.buf))
	out = append(out, r.buf[r.pos:]...)
	return append(out, r.buf[:r.pos]...)
}
