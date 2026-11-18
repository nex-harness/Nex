package pty

import "sync"

// RingBuffer retains the most recent capacity bytes of terminal output for tmux-like reconnect replay.
type RingBuffer struct {
	mu   sync.Mutex
	buf  []byte
	cap  int
	head int
	full bool
}

// NewRingBuffer creates a thread-safe circular byte buffer of capacity bytes (default 128KB).
func NewRingBuffer(capacity int) *RingBuffer {
	if capacity <= 0 {
		capacity = 128 * 1024
	}
	return &RingBuffer{
		buf: make([]byte, capacity),
		cap: capacity,
	}
}

// Write appends p into the circular buffer, overwriting oldest bytes when full.
func (r *RingBuffer) Write(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	origLen := len(p)
	if origLen == 0 {
		return 0, nil
	}
	if origLen >= r.cap {
		copy(r.buf, p[origLen-r.cap:])
		r.head = 0
		r.full = true
		return origLen, nil
	}

	first := r.cap - r.head
	if origLen <= first {
		copy(r.buf[r.head:], p)
		r.head += origLen
		if r.head == r.cap {
			r.head = 0
			r.full = true
		}
	} else {
		copy(r.buf[r.head:], p[:first])
		rem := origLen - first
		copy(r.buf[0:], p[first:])
		r.head = rem
		r.full = true
	}
	return origLen, nil
}

// Snapshot returns a contiguous copy of all buffered bytes in chronological order.
func (r *RingBuffer) Snapshot() []byte {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.full {
		out := make([]byte, r.head)
		copy(out, r.buf[:r.head])
		return out
	}
	out := make([]byte, r.cap)
	n := copy(out, r.buf[r.head:])
	copy(out[n:], r.buf[:r.head])
	return out
}
