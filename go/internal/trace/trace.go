// Package trace mints the identifiers the Go server uses to correlate one client connection
// across its own logs and across the error payloads it hands back to clients.
//
// It is the package phase 0 reserved (`go/internal/trace/  traceId helpers (stub)` in
// docs/specs/go-rewrite/phase-0-contracts.md, step 2.10 target). It holds no state and imports
// nothing but the uuid module already in go.mod: a traceId is an identifier, not a tracing
// implementation, so a reader that wants more (spans, exporters, sampling) replaces the helpers
// here rather than teaching every call site about it.
//
// Why a traceId at all, when the connectionId already exists: the connectionId answers "which
// socket", and it is a property of this process only — a line the Node upstream writes cannot
// carry it. `traceId` is the protocol's own passthrough key (it is in the TS `passthroughKeys`
// list in packages/rpc/src/channelServer.ts, so the client already copies it back onto the Error
// it reconstructs), which means one id can appear in a Go log line, in a PromiseError payload and
// in the client's own error object, and still be greppable as the same request.
package trace

import "github.com/google/uuid"

// NewTraceID returns a fresh UUID v4 in the canonical 8-4-4-4-12 form.
//
// The canonical form (with dashes) is what goes on the wire and into error payloads: it is
// unambiguous in a log line, sorts badly, and nobody sorts it. Node's own ids are strings, and
// nothing parses them, so the only requirement is that they never repeat.
func NewTraceID() string {
	return uuid.NewString()
}

// NewSpanID returns a fresh span identifier: 16 lowercase hex characters, no dashes.
//
// The narrower form is the W3C trace-context span-id shape, so an id minted here can be dropped
// into a `traceparent` header verbatim if a later step adopts that propagation. Nothing in Phase 2
// emits one; the helper exists so the width is decided once, here, rather than in the first
// caller that needs it.
func NewSpanID() string {
	raw := uuid.New()
	var out [32]byte
	hex := "0123456789abcdef"
	for i, b := range raw {
		out[i*2] = hex[b>>4]
		out[i*2+1] = hex[b&0x0f]
	}
	return string(out[:])
}
