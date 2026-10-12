package emitters

import (
	"encoding/json"

	"github.com/tylinux/nex/go/internal/projection"
	"github.com/tylinux/nex/go/internal/v4pub"
)

// eventTimestampMs mirrors the TS eventTimestamp: a finite, non-negative ms.
func eventTimestampMs(ev projection.Event) int64 {
	if ev.TimestampMs < 0 {
		return 0
	}
	return ev.TimestampMs
}

// base builds the shared base fields in the exact order the zod fact schema
// declares them (zod reorders keys to schema order, so the wire order is the
// schema's, not the object literal's): memoryEnabled?, version, eventId,
// eventSeq, occurredAt, sessionId, sourceCommandId?, turnId?. The fact's `kind`
// and remaining fields are appended by each handler after this.
func (t *Telemetry) base(ev projection.Event, sourceCommandID string, hasSourceCommand bool, turnID string, hasTurn bool) *objBuilder {
	o := newObj()
	if t.meta.MemoryEnabled != nil {
		o.set("memoryEnabled", v4pub.Bool(*t.meta.MemoryEnabled))
	}
	o.set("version", v4pub.Int(1))
	o.set("eventId", v4pub.Str(ev.ID))
	seq := ev.SequenceNumber
	if seq < 0 {
		seq = 0
	}
	o.set("eventSeq", v4pub.Int(seq))
	o.set("occurredAt", v4pub.Int(eventTimestampMs(ev)))
	o.set("sessionId", v4pub.Str(t.sessionID))
	o.setStr(hasSourceCommand, "sourceCommandId", sourceCommandID)
	if hasTurn {
		o.set("turnId", v4pub.Str(turnID))
	}
	return o
}

// normalize ports ConversationTelemetryFactNormalizer.normalize. It returns nil
// when the event produces no fact (unknown type, ignored stream chunk, sidecar
// model complete, ...). The returned bytes are the ordered params object.
func (t *Telemetry) normalize(ev projection.Event) ([]byte, error) {
	turnID := ""
	hasTurn := ev.HasTurnID
	if hasTurn {
		turnID = ev.TurnID
	}
	turnKey := ""
	if hasTurn {
		turnKey = t.sessionID + "\x00" + turnID
	}
	sourceCommandID, hasSourceCommand := t.sourceCommand(turnKey)
	m := payloadMap(ev)

	var o *objBuilder
	switch ev.Type {
	case projection.TypeTurnStarted:
		o = t.factTurnStarted(ev, m, turnID, hasTurn, turnKey)
	case projection.TypeModelNetworkStatus:
		o = t.factNetworkStatus(ev, m, turnID, hasTurn, sourceCommandID, hasSourceCommand)
	case projection.TypeModelStreaming:
		o = t.factStreamChunk(ev, m, turnID, hasTurn, sourceCommandID, hasSourceCommand)
	case projection.TypeToolCallScheduled:
		o = t.factToolScheduled(ev, m, turnID, hasTurn, turnKey, sourceCommandID, hasSourceCommand)
	case projection.TypeToolCallStarted, projection.TypeToolCallProgress, projection.TypeToolCallResult, projection.TypeToolCallError:
		o = t.factToolTerminal(ev, m, turnID, hasTurn, turnKey, sourceCommandID, hasSourceCommand)
	case projection.TypePermissionRequested, projection.TypePermissionResolved, projection.TypePermissionDenied:
		o = t.factPermission(ev, m, turnID, hasTurn, sourceCommandID, hasSourceCommand)
	case projection.TypeModelComplete:
		o = t.factUsageDelta(ev, m, turnID, hasTurn, sourceCommandID, hasSourceCommand)
	case projection.TypeTurnComplete:
		o = t.factTurnComplete(ev, m, turnID, hasTurn, turnKey, sourceCommandID, hasSourceCommand)
	default:
		return nil, nil
	}
	if o == nil {
		return nil, nil
	}
	return o.raw(), nil
}

func (t *Telemetry) sourceCommand(turnKey string) (string, bool) {
	if turnKey == "" {
		return "", false
	}
	if v, ok := t.sourceCommandByTurn.get(turnKey); ok {
		return v.(string), true
	}
	return "", false
}

// ── turn.started ───────────────────────────────────────────────────────

func (t *Telemetry) factTurnStarted(ev projection.Event, m map[string]json.RawMessage, turnID string, hasTurn bool, turnKey string) *objBuilder {
	inputID, hasInput := nonEmpty(m, "inputId")
	if turnKey != "" && hasInput {
		t.sourceCommandByTurn.set(turnKey, inputID)
	}
	// sourceCommandId occupies the schema's base slot, before turnId.
	o := t.base(ev, inputID, hasInput, turnID, hasTurn)
	o.set("kind", v4pub.Str("turn.started"))
	o.setStr(mustNonEmpty(m, "executionKind"), "executionKind", mustString(m, "executionKind"))
	o.setStr(mustNonEmpty(m, "inputSource"), "inputSource", mustString(m, "inputSource"))
	if bs := mustString(m, "backgroundSource"); bs == "bash" || bs == "subagent" || bs == "workflow" {
		o.set("backgroundSource", v4pub.Str(bs))
	}
	automationID, hasAutomation := nonEmpty(m, "automationId")
	automationAdmission(inputID, automationID, hasAutomation).apply(o)
	offPeakTaskID, hasOffPeak := nonEmpty(m, "offPeakTaskId")
	o.setStr(hasOffPeak, "offPeakTaskId", offPeakTaskID)
	o.setStr(hasOffPeak, "offPeakRunType", mustString(m, "offPeakRunType"))
	return o
}

// parseAutomationRunID ports parseAutomationRunId (packages/shared/src/automation-types.ts).
// runId = "<automationId>:manual:<uuid>" or "<automationId>:<epochMs>".
type automationRunID struct {
	automationID string
	trigger      string
	scheduledAt  int64
}

func parseAutomationRunID(runID string) (automationRunID, bool) {
	sep := indexByte(runID, ':')
	if sep <= 0 {
		return automationRunID{}, false
	}
	automationID := runID[:sep]
	rest := runID[sep+1:]
	if hasPrefix(rest, "manual:") {
		if len(rest) > len("manual:") {
			return automationRunID{automationID: automationID, trigger: "manual"}, true
		}
		return automationRunID{}, false
	}
	n, ok := safeInteger(rest)
	if !ok || n <= 0 {
		return automationRunID{}, false
	}
	return automationRunID{automationID: automationID, trigger: "schedule", scheduledAt: n}, true
}

// automationAdmission mirrors automationAdmission(): it only attributes the run
// when inputId parses as this automation's run id.
func automationAdmission(inputID, automationID string, hasAutomation bool) admissionFields {
	if inputID == "" || !hasAutomation || automationID == "" {
		return admissionFields{}
	}
	parsed, ok := parseAutomationRunID(inputID)
	if !ok || parsed.automationID != automationID {
		return admissionFields{hasAny: true, automationID: automationID}
	}
	f := admissionFields{hasAny: true, automationID: automationID, taskTrigger: parsed.trigger}
	if parsed.trigger == "schedule" {
		f.scheduledAt = parsed.scheduledAt
		f.hasScheduled = true
	}
	return f
}

type admissionFields struct {
	automationID string
	taskTrigger  string
	scheduledAt  int64
	hasScheduled bool
	hasAny       bool
}

func (f admissionFields) apply(o *objBuilder) {
	if !f.hasAny {
		return
	}
	o.set("automationId", v4pub.Str(f.automationID))
	if f.taskTrigger != "" {
		o.set("taskTrigger", v4pub.Str(f.taskTrigger))
	}
	if f.hasScheduled {
		o.set("scheduledAt", v4pub.Int(f.scheduledAt))
	}
}
