// Package emitters ports the non-conversation protocol frames the Node agent
// emits around a turn, and the context-usage numbers those frames carry.
//
// Owners of the TypeScript behaviour:
//
//   - v4/telemetry/event    bootstrap/src/nex-protocol-v4/conversation-telemetry-facts.ts
//   - computer-use/operation-event  bootstrap/src/nex-protocol/computer-use-operation-event.ts
//   - process/mcpTelemetry  bootstrap/src/nex-protocol/server.ts (session_startup)
//   - state.updated         bootstrap/src/nex-protocol/{server,server-operations,session-mapper,model-mapper}.ts
//   - session/requestRuntimePreferences  bootstrap/src/nex-protocol/server-operations.ts
//   - contextUsageBreakdown core/src/runtime/methods/context-usage.ts
//
// The frames are notifications / requests on the stdio protocol; their params
// are ordered JSON so the bytes are identical to the TypeScript
// JSON.stringify of the same object literal. This package builds every object
// through the ordered-JSON helpers in internal/v4pub (JSONValue/Obj/Member),
// which already implement JSON.stringify-compatible string escaping and number
// formatting for the rest of the wire.
//
// Telemetry is stateful: the normalizer remembers the admission inputId per
// turn, the first stream chunk per (turn, channel, partId, parentToolCallId),
// the tool name per call, the last model seen per session and the queue of
// completed model requests used to attach identity to usage.delta. One
// Telemetry instance is owned by one session; use clearTurn/clearSession to
// match the TypeScript lifecycle.
package emitters

import (
	"encoding/json"

	"github.com/tylinux/nex/go/internal/projection"
	"github.com/tylinux/nex/go/internal/protocol"
)

// Method names used on the wire.
const (
	MethodTelemetry      = "v4/telemetry/event"
	MethodOperationEvent = "computer-use/operation-event"
	MethodMCPTelemetry   = "process/mcpTelemetry"
	MethodStateUpdated   = "state.updated"
	MethodRequestPrefs   = "session/requestRuntimePreferences"
)

// Metadata mirrors the runtimeMetadata argument the TypeScript normalizer takes.
// MemoryEnabled is only emitted when explicitly set (pointer, like the TS
// `runtimeMetadata?.memoryEnabled !== undefined` guard).
type Metadata struct {
	ModelName     string
	ModelProvider string
	MemoryEnabled *bool
}

// Telemetry converts projection events into the v4 telemetry frames a session
// emits. It owns the same per-session side tables as
// ConversationTelemetryFactNormalizer.
type Telemetry struct {
	sessionID string
	meta      Metadata

	firstStreamChunks      *boundedKeySet
	sourceCommandByTurn    *boundedValueMap
	toolNameByCall         *boundedValueMap
	modelBySession         *boundedValueMap
	completedModelRequests *boundedValueMap
}

// New creates a Telemetry emitter for one session.
func New(sessionID string, meta Metadata) *Telemetry {
	return &Telemetry{
		sessionID:              sessionID,
		meta:                   meta,
		firstStreamChunks:      newBoundedKeySet(),
		sourceCommandByTurn:    newBoundedValueMap(),
		toolNameByCall:         newBoundedValueMap(),
		modelBySession:         newBoundedValueMap(),
		completedModelRequests: newBoundedValueMap(),
	}
}

// OnSessionEvent returns the frames the event produces in the order the Node
// agent emits them: the computer-use operation event first, then the telemetry
// fact (verified against the recorded traces, e.g. S02 index 32/33).
func (t *Telemetry) OnSessionEvent(ev projection.Event) ([]protocol.Message, error) {
	var out []protocol.Message
	if op := mapOperationEvent(ev); op != nil {
		out = append(out, notification(MethodOperationEvent, op))
	}
	fact, err := t.normalize(ev)
	if err != nil {
		return nil, err
	}
	if fact != nil {
		out = append(out, notification(MethodTelemetry, fact))
	}
	return out, nil
}

// OnSessionEventTelemetry returns only the v4/telemetry/event frame, if any.
func (t *Telemetry) OnSessionEventTelemetry(ev projection.Event) (protocol.Message, bool, error) {
	fact, err := t.normalize(ev)
	if err != nil {
		return protocol.Message{}, false, err
	}
	if fact == nil {
		return protocol.Message{}, false, nil
	}
	return notification(MethodTelemetry, fact), true, nil
}

// ClearSession drops every per-session table, mirroring clearSession.
func (t *Telemetry) ClearSession() {
	prefix := t.sessionID + "\x00"
	t.sourceCommandByTurn.deletePrefix(prefix)
	t.firstStreamChunks.deletePrefix(prefix)
	t.toolNameByCall.deletePrefix(prefix)
	t.modelBySession.delete(t.sessionID)
	t.completedModelRequests.deletePrefix(prefix)
}

// notification builds a protocol notification whose params are the given
// ordered object.
func notification(method string, params json.RawMessage) protocol.Message {
	return protocol.Message{
		Kind:      protocol.KindNotification,
		Method:    method,
		Params:    params,
		HasParams: true,
	}
}
