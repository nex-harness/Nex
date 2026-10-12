package eventstore

// Event type string values, mirrored from SessionEventType in
// apps/nex-cli/packages/contracts/src/events/session.events.ts (lines 83-176).
// Only the constants the store's behaviour depends on are named; the full
// vocabulary lives in the protocol/contracts port. These string values are the
// exact wire identifiers used by the vectors.
const (
	TypeSessionCreated   EventType = "session_created"
	TypeSessionResumed   EventType = "session_resumed"
	TypeSessionForked    EventType = "session_forked"
	TypeSessionCompacted EventType = "session_compacted"

	TypeSessionTitleUpdated EventType = "session_title_updated"

	TypeTurnInputReceived EventType = "turn_input_received"
	TypeTurnStarted       EventType = "turn_started"
	TypeTurnComplete      EventType = "turn_complete"
	TypeTurnError         EventType = "turn_error"

	TypeSessionInputPromoted EventType = "session_input_promoted"

	TypeUserMessage      EventType = "user_message"
	TypeAssistantMessage EventType = "assistant_message"

	TypeModelRequest  EventType = "model_request"
	TypeModelComplete EventType = "model_complete"
	TypeModelError    EventType = "model_error"

	// Transient types (evictable after their turn is superseded).
	TypeModelStreaming             EventType = "model_streaming"
	TypeModelNetworkStatus         EventType = "model_network_status"
	TypeStreamingToolLedgerUpdated EventType = "streaming_tool_ledger_updated"
	TypeToolCallProgress           EventType = "tool_call_progress"

	TypeToolCallScheduled EventType = "tool_call_scheduled"
	TypeToolCallStarted   EventType = "tool_call_started"
	TypeToolCallResult    EventType = "tool_call_result"
	TypeToolCallError     EventType = "tool_call_error"
	TypeToolBatchComplete EventType = "tool_batch_complete"

	TypePermissionRequested EventType = "permission_requested"
	TypePermissionResolved  EventType = "permission_resolved"
	TypePermissionDenied    EventType = "permission_denied"

	TypeCheckpointCreated           EventType = "checkpoint_created"
	TypeStreamRecoveryAnchorCreated EventType = "stream_recovery_anchor_created"
)
