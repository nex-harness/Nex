package runtime

import (
	"encoding/json"
)

// event is a SessionEvent under construction. The runtime builds the payload as
// plain JSON (order is irrelevant: the projection and the telemetry emitters
// read named fields) and the event store assigns the raw sequence number.
type sessionEvent struct {
	Type      string
	TurnID    string
	HasTurnID bool
	Payload   json.RawMessage
}

// sessionEventType values the runtime appends. Mirrors SessionEventType in
// contracts/src/events/session.events.ts.
const (
	evSessionCreated        = "session_created"
	evSessionTitleUpdated   = "session_title_updated"
	evTurnStarted           = "turn_started"
	evSessionInputPromoted  = "session_input_promoted"
	evModelRequest          = "model_request"
	evModelNetworkStatus    = "model_network_status"
	evModelStreaming        = "model_streaming"
	evModelComplete         = "model_complete"
	evTurnComplete          = "turn_complete"
	evToolCallScheduled     = "tool_call_scheduled"
	evToolCallStarted       = "tool_call_started"
	evToolCallResult        = "tool_call_result"
	evToolCallError         = "tool_call_error"
	evPermissionRequested   = "permission_requested"
	evPermissionResolved    = "permission_resolved"
	evPermissionDenied      = "permission_denied"
	evToolBatchComplete     = "tool_batch_complete"
	evStreamRecoveryAnchor  = "stream_recovery_anchor_created"
	evStreamingToolLedger   = "streaming_tool_ledger_updated"
	evCheckpointCreated     = "checkpoint_created"
	evModelSelected         = "model_selected"
	evSessionInputSettled   = "session_input_settled"
	evUserInputAutoResolved = "user_input_auto_resolution_updated"
)

func mustJSON(v any) json.RawMessage {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return b
}
