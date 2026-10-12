package runtime

import (
	"bytes"
	"context"
	"encoding/json"
	"strconv"
	"strings"
	"time"

	"github.com/tylinux/nex/go/internal/protocol"
	"github.com/tylinux/nex/go/internal/v4pub"
)

func itoa(n int) string { return strconv.Itoa(n) }

// topicSession extracts the session id from "conversation/<sessionId>".
func topicSession(topic string) string {
	return strings.TrimPrefix(topic, "conversation/")
}

// write emits one frame through the outbox.
func (r *Runtime) write(msg protocol.Message) { _ = r.deps.Outbox.WriteMessage(msg) }

// windowRowCount is the current snapshot row window length (the ACK openTiming).
func (s *session) windowRowCount() int {
	raw := newProjectionSource(s.project).SnapshotRaw()
	var snap struct {
		Rows struct {
			Window []any `json:"window"`
		} `json:"rows"`
	}
	if err := json.Unmarshal(raw, &snap); err != nil {
		return 0
	}
	return len(snap.Rows.Window)
}

// postResponse registers a callback the frame reader runs after it has written
// the current request's response (the request-scoped post-response outbox).
func (r *Runtime) postResponse(fn func()) {
	r.mu.Lock()
	r.postResponseQueue = append(r.postResponseQueue, fn)
	r.mu.Unlock()
}

// RunPostResponse drains and runs the queued post-response callbacks. The reader
// calls it after writing each response.
func (r *Runtime) RunPostResponse() {
	r.mu.Lock()
	q := r.postResponseQueue
	r.postResponseQueue = nil
	r.mu.Unlock()
	for _, fn := range q {
		fn()
	}
}

// StartFlushTimer runs the desktop flush cadence: every flushWindowMs it flushes
// every subscription's buffered deltas. It is the timer the host normally drives
// (`flushWindowMs` in the delivery profile); the runtime owns it so a turn's
// deltas are framed the way the recorded traces show.
func (r *Runtime) StartFlushTimer(ctx context.Context, flushWindowMs int) {
	if flushWindowMs <= 0 {
		return
	}
	ch, stop := r.deps.Clock.NewTimer(time.Duration(flushWindowMs) * time.Millisecond)
	go func() {
		defer stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ch:
				r.flushAll()
				ch, stop = r.deps.Clock.NewTimer(time.Duration(flushWindowMs) * time.Millisecond)
			}
		}
	}()
}

// flushAll flushes every session's subscribers under runMu.
func (r *Runtime) flushAll() {
	r.runMu.Lock()
	defer r.runMu.Unlock()
	r.mu.Lock()
	sessions := make([]*session, 0, len(r.sessions))
	for _, s := range r.sessions {
		sessions = append(sessions, s)
	}
	r.mu.Unlock()
	for _, s := range sessions {
		r.flushSubscribers(s)
	}
}

// FlushWindowMs is the desktop flush cadence (30 ms).
func (r *Runtime) FlushWindowMs() int {
	return v4pub.DeliveryProfiles[v4pub.ProfileContinuous].FlushWindowMs
}

// jsStringify marshals v the way JSON.stringify does: no HTML escaping of
// < > & and U+2028/U+2029 left raw. The context-usage char counts are measured
// on this text, so an escaped "<" (6 chars instead of 1) would inflate them.
func jsStringify(v any) json.RawMessage {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		panic(err)
	}
	out := bytes.TrimSuffix(buf.Bytes(), []byte("\n"))
	out = bytes.ReplaceAll(out, []byte(`\u2028`), []byte("\u2028"))
	out = bytes.ReplaceAll(out, []byte(`\u2029`), []byte("\u2029"))
	return json.RawMessage(out)
}
