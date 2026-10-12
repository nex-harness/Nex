package projection

import (
	"encoding/json"
	"sort"
)

// Delta is one ConversationDelta op. Deltas are emitted as *obj so the JSON key
// order matches the TypeScript (`op` first, then the payload field).
type Delta = *obj

// SessionEventType values handled by the projection (session.events.ts). Only the
// types the ported scenarios exercise are named constants; Apply dispatches on
// the raw event type string so unhandled types fall through like the TS default.
const (
	TypeSessionCreated       = "session_created"
	TypeSessionTitleUpdated  = "session_title_updated"
	TypeTurnStarted          = "turn_started"
	TypeModelStreaming       = "model_streaming"
	TypeModelComplete        = "model_complete"
	TypeTurnComplete         = "turn_complete"
	TypeToolCallScheduled    = "tool_call_scheduled"
	TypeToolCallStarted      = "tool_call_started"
	TypeToolCallProgress     = "tool_call_progress"
	TypeToolCallResult       = "tool_call_result"
	TypeToolCallError        = "tool_call_error"
	TypePermissionRequested  = "permission_requested"
	TypePermissionResolved   = "permission_resolved"
	TypePermissionDenied     = "permission_denied"
	TypeModelNetworkStatus   = "model_network_status"
	TypeSessionResumed       = "session_resumed"
	TypeSessionForked        = "session_forked"
	TypeRewindTriggered      = "rewind_triggered"
	TypeStreamRecoveryStart  = "stream_recovery_started"
	TypeStreamRecoveryRetry  = "stream_recovery_retry_started"
	TypeStreamRecoveryTail   = "stream_recovery_tail_discarded"
	TypeUserInputAutoResol   = "user_input_auto_resolution_updated"
	TypeCompactStarted       = "compact_started"
	TypeCompactCompleted     = "compact_completed"
	TypeCompactFailed        = "compact_failed"
	TypeWorkspaceHookAdmit   = "workspace_hook_admission_updated"
	TypeAssistantFeedbackUpd = "assistant_feedback_updated"
)

// Projection is the Go port of ProductProjection. It owns the authority snapshot
// and every side index the reducers read (rowId lookup, tool-call map, streaming
// row ids, turn mapping). The snapshot is an ordered object so serialization is
// byte-identical to the TypeScript.
type Projection struct {
	snapshot *obj

	rowIndexByID map[int64]int
	nextRowID    int64

	streamingTextRowID      *int64
	streamingReasoningRowID *int64
	outputContinuationRowID *int64

	toolRowIDByCallID       map[string]int64
	openForegroundToolCalls map[string]struct{}
	// fileToolInputPreviewByCallID throttles file-tool input previews (Write /
	// Edit); the projection port models the pending append and the last publish.
	fileToolInputPreviewByCallID map[string]*fileToolInputPreview

	messageIDByRowID map[int64]string
	entityIDByRowID  map[int64]string

	// editableEntityIDs is the set of entity ids that carry a canonical command
	// target (editTargetByEntityId in the TS). Only rows in this set can grant the
	// edit/retry action.
	editableEntityIDs map[string]struct{}

	turnHeaderRowIDByTurnID map[string]int64

	// productTurnIdByRuntimeTurnId maps a runtime turn to its product turn. A
	// queue drain splits a runtime turn into a new product turn.
	productTurnIDByRuntimeTurnID map[string]string
	// currentTurnID is the runtime turn of the in-flight turn.
	currentTurnID  string
	hasCurrentTurn bool

	// currentTurnStartedModelOnly tracks whether the current runtime turn was
	// opened by a model-only TurnStarted (no product turn to attach hooks to).
	currentTurnStartedModelOnly bool

	// contextWindowState mirrors the reducer's internal context-window facts.
	contextMaxTokens      *int64
	contextUsedTokens     int64
	contextTouchedByEvent bool

	// lastTurnModel carries the previous turn's resolved provider/model for the
	// modelChange marker. kind is "silentInitial" | "sourceLess" | "known".
	lastTurnModelKind     string
	lastTurnModelProvider string
	lastTurnModelModel    string
	lastTurnModelThought  string

	droppedContentStreamEventCount int
}

// New creates a projection for a session with the given log epoch.
func New(sessionID, logEpoch string) *Projection {
	return &Projection{
		snapshot:                     createInitialConversationSnapshot(sessionID, logEpoch),
		rowIndexByID:                 map[int64]int{},
		nextRowID:                    1,
		toolRowIDByCallID:            map[string]int64{},
		openForegroundToolCalls:      map[string]struct{}{},
		fileToolInputPreviewByCallID: map[string]*fileToolInputPreview{},
		messageIDByRowID:             map[int64]string{},
		entityIDByRowID:              map[int64]string{},
		editableEntityIDs:            map[string]struct{}{},
		turnHeaderRowIDByTurnID:      map[string]int64{},
		productTurnIDByRuntimeTurnID: map[string]string{},
		lastTurnModelKind:            "silentInitial",
	}
}

// Snapshot marshals the current snapshot to JSON (byte-identical to the TS).
func (p *Projection) Snapshot() (json.RawMessage, error) {
	return p.snapshot.MarshalJSON()
}

// SeedConfig values fill in the snapshot.config seed (ports
// ProductProjection.seedConfig for a fresh session, where no config event has
// touched the fields). It produces no delta and does not bump the revision.
type SeedConfig struct {
	Provider       string
	Model          string
	Thought        string
	ThoughtLevels  []string
	Mode           string
	ModelSelection json.RawMessage
	HasSelection   bool
	PlanEnabled    *bool
}

// SeedConfig injects the session config seed into the snapshot's config object.
func (p *Projection) SeedConfig(seed SeedConfig) {
	config, ok := p.snapshot.get("config").(*obj)
	if !ok {
		return
	}
	if seed.Provider != "" {
		config.put("provider", seed.Provider)
	}
	if seed.Model != "" {
		config.put("model", seed.Model)
	}
	if seed.Thought != "" {
		config.put("thought", seed.Thought)
	}
	if len(seed.ThoughtLevels) > 0 {
		levels := make([]any, len(seed.ThoughtLevels))
		for i, l := range seed.ThoughtLevels {
			levels[i] = l
		}
		config.put("thoughtLevels", levels)
	}
	if seed.Mode != "" {
		config.put("mode", seed.Mode)
	}
	if seed.HasSelection {
		config.put("modelSelection", json.RawMessage(seed.ModelSelection))
	}
	if seed.PlanEnabled != nil {
		config.put("planEnabled", *seed.PlanEnabled)
	}
}

// DroppedContentStreamEventCount reports the assistant-conservation counter.
func (p *Projection) DroppedContentStreamEventCount() int {
	return p.droppedContentStreamEventCount
}

// Apply consumes one event and returns the deltas it produced. The returned
// slice is empty when the event is a no-op for the projection.
func (p *Projection) Apply(event Event) ([]Delta, error) {
	m, err := event.payloadMap()
	if err != nil {
		return nil, err
	}
	reduced := p.reduce(event, m)
	withActions := append(reduced, p.materializeCommandRowActions(reduced)...)
	final := p.attachRevision(withActions)
	p.advance(final, event.SequenceNumber)
	return final, nil
}

// applyNoRevision is used by the event handlers that internally reduce derived
// facts; it mirrors applyEventInternal but keeps the same batch/revision rule.
func (p *Projection) reduce(event Event, m map[string]json.RawMessage) []Delta {
	switch event.Type {
	case TypeSessionCreated:
		return p.onSessionCreated(event, m)
	case TypeSessionTitleUpdated:
		return p.onSessionTitleUpdated(event, m)
	case TypeTurnStarted:
		return p.onTurnStarted(event, m)
	case TypeModelStreaming:
		return p.onModelStreaming(event, m)
	case TypeModelNetworkStatus:
		return p.onModelNetworkStatus(event, m)
	case TypeModelComplete:
		return p.onModelComplete(event, m)
	case TypeToolCallScheduled:
		return p.onToolCallScheduled(event, m)
	case TypeToolCallStarted, TypeToolCallProgress:
		return p.onToolCallActivity(event, m)
	case TypeToolCallResult:
		return p.onToolCallResult(event, m)
	case TypeToolCallError:
		return p.onToolCallError(event, m)
	case TypePermissionRequested:
		return p.onPermissionRequested(event, m)
	case TypePermissionResolved:
		return p.onPermissionResolved(event, m)
	case TypePermissionDenied:
		return p.onPermissionDenied(event, m)
	case TypeTurnComplete:
		return p.onTurnComplete(event, m)
	default:
		return nil
	}
}

// advance applies the batch to the snapshot immutably and updates side indexes,
// then records the event's sequence number — exactly what applyEventInternal does
// after computing finalDeltas.
func (p *Projection) advance(deltas []Delta, sequenceNumber int64) {
	previousLen := len(p.rowsWindow())
	p.snapshot = applyConversationDeltas(p.snapshot, deltas)
	p.snapshot.put("seq", sequenceNumber)
	reindex(p, previousLen, deltas)
	p.updateToolIndexesAfterDeltas(deltas)
}

// attachRevision ports attachRevision: one structural/A-zone delta bumps revision
// by one, merged into a trailing state.updated (or appended as its own).
func (p *Projection) attachRevision(deltas []Delta) []Delta {
	any := false
	for _, d := range deltas {
		if deltaBumpsRevision(opOf(d), patchOf(d)) {
			any = true
			break
		}
	}
	if !any {
		return deltas
	}
	revision := int64Of(p.snapshot.get("revision")) + 1
	last := deltas[len(deltas)-1]
	if opOf(last) == "state.updated" {
		patch := patchOf(last)
		patch.put("revision", revision)
		return deltas
	}
	return append(deltas, newStateUpdated(newObj().set("revision", revision)))
}

// reindex mirrors updateRowIndexAfterImmutableApply.
func reindex(p *Projection, previousLen int, deltas []Delta) {
	for _, d := range deltas {
		if opOf(d) == "row.removed" {
			p.rowIndexByID = map[int64]int{}
			for i, r := range p.rowsWindow() {
				if row, ok := r.(*obj); ok {
					p.rowIndexByID[rowIDOf(row)] = i
				}
			}
			return
		}
	}
	next := previousLen
	for _, d := range deltas {
		if opOf(d) != "row.appended" {
			continue
		}
		p.rowIndexByID[rowIDOf(rowOf(d))] = next
		next++
	}
}

// updateToolIndexesAfterDeltas mirrors the TS index maintenance for open
// foreground tool rows.
func (p *Projection) updateToolIndexesAfterDeltas(deltas []Delta) {
	for _, d := range deltas {
		switch opOf(d) {
		case "row.appended", "row.upserted":
			row := rowOf(d)
			if kindOf(row) != "toolCall" {
				continue
			}
			id, _ := row.get("toolCallId").(string)
			if isOpenForegroundToolRow(row) {
				p.openForegroundToolCalls[id] = struct{}{}
			} else {
				delete(p.openForegroundToolCalls, id)
			}
		case "row.removed":
			from := int64Of(d.get("fromRowId"))
			for id, rowID := range p.toolRowIDByCallID {
				if rowID < from {
					continue
				}
				delete(p.toolRowIDByCallID, id)
				delete(p.openForegroundToolCalls, id)
			}
		}
	}
}

// materializeCommandRowActions ports materializeCommandRowActions (a derived
// action materialization). The full port is in actions.go.
func (p *Projection) rowsWindow() []any {
	rows, _ := p.snapshot.get("rows").(*obj)
	window, _ := rows.get("window").([]any)
	return window
}

// sortedToolIDs returns open tool call ids sorted by row id, mirroring the TS
// closeOpenToolRows ordering.
func (p *Projection) openToolRowsSorted() []*obj {
	rows := make([]*obj, 0, len(p.openForegroundToolCalls))
	for id := range p.openForegroundToolCalls {
		row := p.findToolRow(id)
		if row == nil || !isOpenForegroundToolRow(row) {
			continue
		}
		rows = append(rows, row)
	}
	sort.Slice(rows, func(i, j int) bool { return rowIDOf(rows[i]) < rowIDOf(rows[j]) })
	return rows
}

// delta constructors keep the op key first and the payload second.
func newRowAppended(row *obj) Delta  { return newObj().set("op", "row.appended").set("row", row) }
func newRowUpserted(row *obj) Delta  { return newObj().set("op", "row.upserted").set("row", row) }
func newRowRemoved(from int64) Delta { return newObj().set("op", "row.removed").set("fromRowId", from) }

func newRowDelta(rowID int64, path, append string) Delta {
	return newObj().set("op", "row.delta").set("rowId", rowID).set("path", path).set("append", append)
}

func newStateUpdated(patch *obj) Delta {
	return newObj().set("op", "state.updated").set("patch", patch)
}

func opOf(d Delta) string {
	v, _ := d.get("op").(string)
	return v
}

func patchOf(d Delta) *obj {
	v, _ := d.get("patch").(*obj)
	return v
}

func rowOf(d Delta) *obj {
	v, _ := d.get("row").(*obj)
	return v
}
