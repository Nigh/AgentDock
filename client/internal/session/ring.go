package session

// ring is a fixed-capacity byte ring buffer holding the most recent
// terminal output, replayed to browsers on (re)attach.
type ring struct {
	buf  []byte
	off  int // write position
	full bool
}

func newRing(capacity int) *ring {
	return &ring{buf: make([]byte, capacity)}
}

func (r *ring) Write(p []byte) {
	if len(p) >= len(r.buf) {
		copy(r.buf, p[len(p)-len(r.buf):])
		r.off = 0
		r.full = true
		return
	}
	n := copy(r.buf[r.off:], p)
	if n < len(p) {
		copy(r.buf, p[n:])
		r.full = true
	}
	r.off = (r.off + len(p)) % len(r.buf)
	if r.off == 0 && n == len(p) {
		r.full = true
	}
}

// Bytes returns the buffered output oldest-first.
func (r *ring) Bytes() []byte {
	if !r.full {
		return append([]byte(nil), r.buf[:r.off]...)
	}
	out := make([]byte, 0, len(r.buf))
	out = append(out, r.buf[r.off:]...)
	return append(out, r.buf[:r.off]...)
}
