package exec

import "sync"

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
	defer c.mu.Unlock()
	c.total += int64(len(p))
	c.appendTailLocked(p)
	remaining := c.maxBytes - int64(len(c.head))
	if remaining <= 0 {
		c.dropped += int64(len(p))
		return len(p), nil
	}
	take := int64(len(p))
	if take > remaining {
		take = remaining
	}
	c.head = append(c.head, p[:take]...)
	c.dropped += int64(len(p)) - take
	return len(p), nil
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
