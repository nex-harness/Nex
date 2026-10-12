package exec

import (
	"bytes"
	"sync"
)

// OutputCollector is the port of OutputCollector in
// apps/nex-cli/packages/adapters/src/exec/output-collector.ts, minus the parts
// Phase 1 does not use (artifact files, the aggregate persisted budget and the
// backpressure hook that a Go io.Writer cannot have, because Write must consume
// the whole chunk or the child blocks on a full pipe).
//
// It keeps three things:
//   - an inline head of at most maxBytes;
//   - a tail of at most maxTailBytes (the last maxTailBytes bytes, for progress
//     previews);
//   - the total number of bytes the child produced, counted even when they are
//     dropped, so `truncated` is honest about how much output existed.
//
// It implements io.Writer, so exec.Cmd can write straight into it.
type OutputCollector struct {
	mu sync.Mutex

	maxBytes int64
	maxTail  int64

	head    []byte
	tail    []byte
	total   int64
	dropped int64

	// onLine, when set, receives every complete line the child wrote to this stream.
	// It is the streaming seam for the one thing a retained buffer cannot serve: a
	// supervisor that must learn the child's listen port from the line it prints on
	// startup, before it has seen the rest of the output. Lines are delivered
	// outside the lock and in order; a line longer than the internal buffer is
	// delivered in the fragments it arrived in and ends with an empty line.
	onLine func(string)
	// pending is the tail of the longest line seen so far, awaiting its newline.
	pending []byte
}

// SetLineSink installs the streaming tap. It must be called before the child starts
// writing to this collector; afterwards it races with Write.
func (c *OutputCollector) SetLineSink(sink chan<- string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if sink == nil {
		c.onLine = nil
		return
	}
	c.onLine = func(line string) {
		select {
		case sink <- line:
		default: // a slow consumer loses lines; it must not stall the child's pipe
		}
	}
}

// Flush delivers whatever partial line is buffered. Write only reports a line once
// its newline arrives, so a caller that waits for the process to exit must call this
// to see the final unterminated line (a banner without a trailing newline).
func (c *OutputCollector) Flush() {
	c.mu.Lock()
	pending := string(c.pending)
	c.pending = nil
	onLine := c.onLine
	c.mu.Unlock()
	if pending != "" && onLine != nil {
		onLine(pending)
	}
}

// tapLineLocked splits the incoming bytes into complete lines, updating the pending
// tail. It MUST be called with c.mu held and returns the completed lines; the caller
// fires onLine for each after releasing the lock, because onLine belongs to the caller
// and may do anything (including calling back into this collector).
//
// The tap sees every byte, including the ones dropped past the head cap, so it
// accumulates from p and not from the retained head.
func (c *OutputCollector) tapLineLocked(p []byte) []string {
	if c.onLine == nil {
		return nil
	}
	var lines []string
	buffer := p
	for {
		idx := bytes.IndexByte(buffer, '\n')
		if idx < 0 {
			// The buffer is capped by TRUNCATING, not by skipping the write: a child
			// that emits one enormous unterminated line must neither grow this process
			// without limit nor lose the newline that would later close it.
			c.pending = appendBounded(c.pending, buffer, maxPendingLine)
			return lines
		}
		pending := c.pending
		c.pending = nil
		pending = append(pending, buffer[:idx]...)
		if len(pending) > 0 && pending[len(pending)-1] == '\r' {
			pending = pending[:len(pending)-1]
		}
		lines = append(lines, string(pending))
		buffer = buffer[idx+1:]
	}
}

// maxPendingLine bounds the unterminated-line buffer, so a child that never emits a
// newline cannot grow it without limit.
const maxPendingLine = 64 << 10

// appendBounded appends src to dst, keeping at most limit bytes. When dst is already
// at the limit the INCOMING bytes are dropped rather than the earlier ones: a line
// that has already arrived is more useful than one that has not, and the newline that
// completes it is what matters.
func appendBounded(dst, src []byte, limit int) []byte {
	if len(dst) >= limit {
		return dst
	}
	room := limit - len(dst)
	if len(src) < room {
		return append(dst, src...)
	}
	return append(dst, src[:room]...)
}

// NewOutputCollector returns a collector with an inline cap of maxBytes and a
// tail cap of maxTailBytes. A negative maxBytes disables the head (everything
// is counted and dropped); a negative maxTail disables the tail.
func NewOutputCollector(maxBytes, maxTailBytes int64) *OutputCollector {
	return &OutputCollector{maxBytes: maxBytes, maxTail: maxTailBytes}
}

// Write implements io.Writer. It never fails and never retains more than the
// configured caps, which is what keeps a 10 MiB (or 10 GiB) child from growing
// the agent's heap: the bytes past the cap are counted and dropped.
func (c *OutputCollector) Write(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	c.mu.Lock()
	lines := c.tapLineLocked(p)
	c.total += int64(len(p))
	c.appendTailLocked(p)
	remaining := c.maxBytes - int64(len(c.head))
	if remaining <= 0 {
		c.dropped += int64(len(p))
		c.mu.Unlock()
		fireLines(c.onLine, lines)
		return len(p), nil
	}
	take := int64(len(p))
	if take > remaining {
		take = remaining
	}
	c.head = append(c.head, p[:take]...)
	c.dropped += int64(len(p)) - take
	c.mu.Unlock()
	fireLines(c.onLine, lines)
	return len(p), nil
}

// fireLines delivers the completed lines outside the collector lock.
func fireLines(onLine func(string), lines []string) {
	if onLine == nil {
		return
	}
	for _, line := range lines {
		onLine(line)
	}
}

// appendTailLocked keeps the last maxTail bytes seen. The head-then-tail order
// matters when maxTail exceeds maxBytes: the tail is the newer data.
func (c *OutputCollector) appendTailLocked(p []byte) {
	if c.maxTail <= 0 || len(p) == 0 {
		return
	}
	if int64(len(p)) >= c.maxTail {
		c.tail = append(c.tail[:0], p[int64(len(p))-c.maxTail:]...)
		return
	}
	if int64(len(c.tail)+len(p)) <= c.maxTail {
		c.tail = append(c.tail, p...)
		return
	}
	overflow := int64(len(c.tail)) + int64(len(p)) - c.maxTail
	copy(c.tail, c.tail[overflow:])
	c.tail = c.tail[:int64(len(c.tail))-overflow]
	c.tail = append(c.tail, p...)
}

// Output is the snapshot a collector reports.
type Output struct {
	// Text is the inline head.
	Text string
	// Bytes is everything the child wrote on this stream, dropped bytes included.
	Bytes int64
	// Truncated is true when Bytes exceeds the inline cap.
	Truncated bool
	// Tail is the last MaxTailBytes bytes, empty when the tail is disabled.
	Tail string
}

// Bytes is the number of bytes seen so far, including bytes dropped after the
// cap.
func (c *OutputCollector) Bytes() int64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.total
}

// Result snapshots the collector. Safe to call while the child is still
// writing.
func (c *OutputCollector) Result() Output {
	c.mu.Lock()
	defer c.mu.Unlock()
	return Output{
		Text:      string(c.head),
		Bytes:     c.total,
		Truncated: c.total > int64(len(c.head)),
		Tail:      string(c.tail),
	}
}
