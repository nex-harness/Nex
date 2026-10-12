package runtime

import (
	"encoding/json"

	"github.com/tylinux/nex/go/internal/modelclient"

	"github.com/tylinux/nex/go/internal/emitters"
	"github.com/tylinux/nex/go/internal/eventstore"
	"github.com/tylinux/nex/go/internal/projection"
	"github.com/tylinux/nex/go/internal/protocol"
	"github.com/tylinux/nex/go/internal/tools"
	"github.com/tylinux/nex/go/internal/v4pub"
)

// session is the per-session authority. It is driven by the runtime actor: all
// mutation happens on the session's goroutine, so no locking is needed (the
// publisher, projection, emitters and event store are all single-threaded state
// machines).
type session struct {
	rt  *Runtime
	id  string
	ws  Workspace
	cmd commandRef

	events  eventstore.Store
	project *projection.Projection
	pub     *v4pub.Publisher
	tele    *emitters.Telemetry

	// transportSeq is the publisher/projection number. The v4 gateway ingests a
	// synthesized session_created (raw 0) first, so it starts at 1 and every raw
	// event advances it by one (transport = raw + 1 for the recorded scenarios).
	transportSeq int64

	// model config cached from the create command.
	modelSelection json.RawMessage
	memoryEnabled  bool
	firstText      string

	// turn bookkeeping.
	turnNumber             int
	lastAssistantMessageID string
	readState              *tools.ReadState
	// pendingTools accumulates the tools started during the current model step
	// (their results are appended after model_complete).
	pendingTools  []toolExecution
	lastToolWrite bool
	// scheduledCalls are the tool calls scheduled during the current step.
	scheduledCalls []modelclient.ToolCall
	// deferredCalls are side-effecting/approval-gated calls scheduled after the step.
	deferredCalls []modelclient.ToolCall
	// streamedPendingStarts are the streaming calls whose tool_call_started lands
	// after the model stream's finish event (Node's accept() does not await).
	streamedPendingStarts []pendingStart
	// pendingStreamed are the accepted streaming calls that have not started yet.
	pendingStreamed []streamedCall
	// file changes accumulated across the turn's Write/Edit tools, keyed by path
	// so a file counts once (the workspace diff, not the sum of tool patches).
	changedFilesByPath map[string]fileChange
	// cache accumulation across the turn's model requests.
	cacheRequests   int
	cacheTotalInput int64
	cacheTotalRead  int64
	cacheTotalWrite int64

	// firstSub is closed when the session's first conversation subscription is
	// established. The createSession-scheduled turn waits for it so the initial
	// snapshot is taken before turn_started (recorded snapshot seq 2, empty rows).
	firstSub   chan struct{}
	subscribed bool
}

// commandRef is the identity carried by a v4 command.
type commandRef struct {
	ID       string
	ClientID string
}

func (s *session) nowMs() int64 { return s.rt.deps.Clock.NowMs() }
func (s *session) ids() IDs     { return s.rt.deps.IDs }
func (s *session) write(m protocol.Message) {
	_ = s.rt.deps.Outbox.WriteMessage(m)
}

// setMemoryEnabled records the memory flag from the materialization prefs reply.
// The telemetry metadata carries it on every fact (memoryEnabled is emitted only
// when defined), and the createSession result echoes it.
func (s *session) setMemoryEnabled(enabled bool) {
	s.memoryEnabled = enabled
	s.tele = emitters.New(s.id, emitters.Metadata{
		ModelName:     s.rt.deps.Provider.ModelName,
		ModelProvider: s.rt.deps.Provider.ProviderID,
		MemoryEnabled: &enabled,
	})
}

// appendEvent persists a session event: it assigns the raw sequence number,
// emits the telemetry/operation frames (raw number), feeds the publisher
// (transport number) and returns the raw-numbered projection event.
func (s *session) appendEvent(ev sessionEvent) projection.Event {
	stored := s.events.Append(eventstore.Event{
		SessionID: s.id,
		TurnID:    ev.TurnID,
		Type:      ev.Type,
	})
	event := projection.Event{
		ID:             s.ids().EventID(),
		SessionID:      s.id,
		Type:           ev.Type,
		TimestampMs:    s.nowMs(),
		TraceID:        s.rt.rootTrace,
		SequenceNumber: int64(stored.SequenceNumber),
		Payload:        ev.Payload,
	}
	if ev.HasTurnID {
		event.TurnID = ev.TurnID
		event.HasTurnID = true
	}
	s.emitTelemetry(event)
	s.ingestTransport(event)
	return event
}

// emitTelemetry writes the computer-use operation event (if any) and the v4
// telemetry fact for one event, in that order. Telemetry uses the raw number.
func (s *session) emitTelemetry(event projection.Event) {
	frames, err := s.tele.OnSessionEvent(event)
	if err != nil {
		return
	}
	for _, f := range frames {
		s.write(f)
	}
}

// ingestTransport feeds the publisher a copy of the event whose sequence number
// is the transport number (raw + 1). The +1 models the v4 gateway's
// session_created seed and matches every recorded scenario
// (finalToSeq == maxRaw + 1).
func (s *session) ingestTransport(event projection.Event) {
	s.transportSeq = event.SequenceNumber + 1
	raw, err := json.Marshal(struct {
		ID             string          `json:"id"`
		SessionID      string          `json:"sessionId"`
		TurnID         *string         `json:"turnId,omitempty"`
		Type           string          `json:"type"`
		TimestampMs    int64           `json:"timestampMs"`
		TraceID        string          `json:"traceId"`
		SequenceNumber int64           `json:"sequenceNumber"`
		Payload        json.RawMessage `json:"payload"`
	}{
		ID: event.ID, SessionID: event.SessionID, TurnID: optionalString(event.TurnID, event.HasTurnID),
		Type: event.Type, TimestampMs: event.TimestampMs, TraceID: event.TraceID,
		SequenceNumber: s.transportSeq, Payload: event.Payload,
	})
	if err != nil {
		return
	}
	_ = s.pub.Ingest(raw)
}

// seedPublisher ingests the synthesized session_created event that the v4
// gateway feeds first. It consumes transport seq 1 and produces no deltas. It
// also injects the session config seed (no delta, no revision bump).
func (s *session) seedPublisher() {
	plan := false
	s.project.SeedConfig(projection.SeedConfig{
		Provider:       s.rt.deps.Provider.ProviderID,
		Model:          s.rt.deps.Provider.ModelID,
		Thought:        s.rt.deps.Provider.DefaultReasoning,
		ThoughtLevels:  s.rt.deps.Provider.ReasoningLevels,
		Mode:           "build",
		ModelSelection: s.modelSelection,
		HasSelection:   len(s.modelSelection) > 0,
		PlanEnabled:    &plan,
	})
	seed := mustJSON(map[string]any{
		"id":             s.ids().EventID(),
		"sessionId":      s.id,
		"type":           evSessionCreated,
		"timestampMs":    s.nowMs(),
		"traceId":        s.rt.rootTrace,
		"sequenceNumber": 1,
		"payload":        map[string]any{"mode": "default"},
	})
	s.transportSeq = 1
	_ = s.pub.Ingest(seed)
}

func optionalString(v string, has bool) *string {
	if !has {
		return nil
	}
	return &v
}
