package modelclient

import (
	"crypto/rand"
	"encoding/hex"
	"io"
	"time"
)

// requestIDForAttempt derives a per-attempt request id. The Node code generates a
// fresh uuid for every attempt (createAttemptStatusContext); a caller may set
// RequestID to override, in which case attempts share it.
func requestIDForAttempt(req Request, attempt int) string {
	if id, ok := req.Headers["x-request-id"]; ok && id != "" {
		return id
	}
	return randomID()
}

func randomID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "00000000-0000-4000-8000-000000000000"
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	dst := make([]byte, 36)
	hex.Encode(dst[0:8], b[0:4])
	dst[8] = '-'
	hex.Encode(dst[9:13], b[4:6])
	dst[13] = '-'
	hex.Encode(dst[14:18], b[6:8])
	dst[18] = '-'
	hex.Encode(dst[19:23], b[8:10])
	dst[23] = '-'
	hex.Encode(dst[24:36], b[10:16])
	return string(dst)
}

// transportReaderError wraps a read failure, mapping an os/network error code.
type transportReaderError struct {
	code  string
	cause error
}

func (e *transportReaderError) Error() string {
	if e.cause != nil {
		return e.cause.Error()
	}
	return "transport read failed"
}
func (e *transportReaderError) Unwrap() error { return e.cause }

func errCode(err error) string {
	if e, ok := err.(interface{ Code() string }); ok {
		return e.Code()
	}
	// net errors satisfy net.Error; the adapter sets a typed error when it can.
	return ""
}

// normalizeTransportError turns an arbitrary transport error into one the
// classifier understands. The adapter already produces *AttrError values, so this
// mostly passes them through; a bare io error becomes an unknown failure.
func normalizeTransportError(err error) error {
	if err == nil {
		return nil
	}
	if _, ok := err.(*AttrError); ok {
		return err
	}
	if _, ok := err.(*StreamIdleTimeoutError); ok {
		return err
	}
	if _, ok := err.(*transportReaderError); ok {
		return err
	}
	return NewAttrError(ErrorAttrs{Message: err.Error(), Cause: err})
}

// readWithIdle reads one chunk, failing with a StreamIdleTimeoutError if no byte
// arrives within timeoutMs. timeoutMs <= 0 disables the timeout.
func readWithIdle(stream ResponseStream, buf []byte, clock Clock, timeoutMs int) (int, error) {
	if stream == nil {
		return 0, io.EOF
	}
	if timeoutMs <= 0 {
		return stream.Read(buf)
	}
	type result struct {
		n   int
		err error
	}
	ch := make(chan result, 1)
	go func() {
		n, err := stream.Read(buf)
		ch <- result{n, err}
	}()
	select {
	case r := <-ch:
		return r.n, r.err
	case <-clock.After(time.Duration(timeoutMs) * time.Millisecond):
		return 0, &StreamIdleTimeoutError{IdleMs: int64(timeoutMs), TimeoutMs: int64(timeoutMs)}
	}
}

// isRetrySafePreludeEvent mirrors isRetrySafePreludeStreamEvent: start/end block
// markers and tool-input lifecycle events (including empty text/reasoning deltas)
// are safe to drop and replay. A non-empty text or reasoning delta commits.
func isRetrySafePreludeEvent(ev Event) bool {
	switch ev.Kind {
	case EventStart, EventTextStart, EventTextEnd, EventReasoningStart, EventReasoningEnd,
		EventToolInputStart, EventToolInputDelta, EventToolInputEnd:
		return true
	case EventTextDelta, EventReasoningDelta:
		return ev.Text == ""
	}
	return false
}

// observeOutcome records usage/finish from the passing events.
func observeOutcome(outcome *streamOutcome, ev Event) {
	switch ev.Kind {
	case EventFinish:
		outcome.finishReason = ev.FinishReason
		outcome.usage = ev.Usage
	}
}

func durationMs(startedAt time.Time, clock Clock) *int64 {
	d := clock.Now().Sub(startedAt).Milliseconds()
	return &d
}

// closeResponse closes a transport response's stream.
func closeResponse(resp *Response) {
	if resp != nil && resp.Stream != nil {
		_ = resp.Stream.Close()
	}
}
