package emitters

import (
	"encoding/json"

	"github.com/tylinux/nex/go/internal/projection"
	"github.com/tylinux/nex/go/internal/v4pub"
)

// ── tool.lifecycle ─────────────────────────────────────────────────────

func (t *Telemetry) factToolScheduled(ev projection.Event, m map[string]json.RawMessage, turnID string, hasTurn bool, turnKey string, sourceCommandID string, hasSourceCommand bool) *objBuilder {
	toolCallID := mustString(m, "toolCallId")
	toolName := mustString(m, "toolName")
	scope := turnKey
	if scope == "" {
		scope = t.sessionID
	}
	t.toolNameByCall.set(scope+"\x00"+toolCallID, toolName)

	o := t.base(ev, sourceCommandID, hasSourceCommand, turnID, hasTurn)
	o.set("kind", v4pub.Str("tool.lifecycle"))
	o.set("phase", v4pub.Str("scheduled"))
	o.set("toolCallId", v4pub.Str(toolCallID))
	o.set("toolName", v4pub.Str(toolName))
	mirroredSubagentFields(o, m, nil)
	return o
}

func (t *Telemetry) factToolTerminal(ev projection.Event, m map[string]json.RawMessage, turnID string, hasTurn bool, turnKey string, sourceCommandID string, hasSourceCommand bool) *objBuilder {
	toolCallID := mustString(m, "toolCallId")
	scope := turnKey
	if scope == "" {
		scope = t.sessionID
	}
	key := scope + "\x00" + toolCallID
	explicitName, hasExplicit := nonEmpty(m, "toolName")
	toolName := explicitName
	hasName := hasExplicit
	if !hasName {
		if v, ok := t.toolNameByCall.get(key); ok {
			toolName = v.(string)
			hasName = true
		}
	}
	isResult := ev.Type == projection.TypeToolCallResult
	isError := ev.Type == projection.TypeToolCallError
	display := recordOf(record(m, "result"), "display")
	performance := toolPerformance(resultPerf(m, isResult))

	var resultSuccess bool
	var resultEncoded bool
	var resultContent json.RawMessage
	var resultErr map[string]json.RawMessage
	var duration float64
	var hasDuration bool
	if isResult {
		result := record(m, "result")
		if b, ok := boolField(result, "success"); ok {
			resultSuccess, resultEncoded = b, true
		}
		if raw, ok := result["content"]; ok {
			resultContent = raw
		}
		resultErr = record(result, "error")
		if n, ok := num(m, "duration"); ok {
			duration, hasDuration = n, true
		}
	}

	var phase string
	switch {
	case ev.Type == projection.TypeToolCallStarted:
		phase = "started"
	case ev.Type == projection.TypeToolCallProgress:
		phase = "progress"
	case ev.Type == projection.TypeToolCallResult:
		if resultEncoded && !resultSuccess {
			phase = "failed"
		} else {
			phase = "completed"
		}
	default:
		phase = "failed"
	}

	var errorPayload map[string]json.RawMessage
	var skillMetadata map[string]json.RawMessage
	if isError {
		errorPayload = record(m, "error")
		skillMetadata = record(m, "skillMetadata")
	} else if isResult {
		skillMetadata = record(m, "skillMetadata")
	}

	automationID := ""
	if phase == "completed" && toolName == "CronCreate" {
		automationID = cronCreateAutomationID(resultContent)
	}
	if phase == "completed" || phase == "failed" {
		t.toolNameByCall.delete(key)
	}

	o := t.base(ev, sourceCommandID, hasSourceCommand, turnID, hasTurn)
	o.set("kind", v4pub.Str("tool.lifecycle"))
	o.set("phase", v4pub.Str(phase))
	o.set("toolCallId", v4pub.Str(toolCallID))
	o.setStr(hasName, "toolName", toolName)
	o.setIf(hasDuration, "durationMs", v4pub.Float(duration))
	if isError && hasErrorObject(errorPayload) {
		o.set("errorCode", v4pub.Str(errorObjectCode(errorPayload)))
		o.set("errorMessage", v4pub.Str(mustString(errorPayload, "message")))
	}
	if isResult && hasErrorObject(resultErr) {
		o.set("errorCode", v4pub.Str(errorObjectCode(resultErr)))
		o.set("errorMessage", v4pub.Str(mustString(resultErr, "message")))
	}
	mirroredSubagentFields(o, m, display)
	skillTelemetryFields(o, toolName, firstMap(skillMetadata, errorSkillMetadata(isError, m)))
	o.setIf(performance != nil, "performance", v4pub.RawMessageOf(performance))
	o.setStr(automationID != "", "automationId", automationID)
	return o
}

// ── permission.lifecycle ───────────────────────────────────────────────

func (t *Telemetry) factPermission(ev projection.Event, m map[string]json.RawMessage, turnID string, hasTurn bool, sourceCommandID string, hasSourceCommand bool) *objBuilder {
	var requested, resolved, denied map[string]json.RawMessage
	phase := ""
	switch ev.Type {
	case projection.TypePermissionRequested:
		requested = m
		phase = "requested"
	case projection.TypePermissionResolved:
		resolved = m
		phase = "resolved"
	case projection.TypePermissionDenied:
		denied = m
		phase = "denied"
	}
	o := t.base(ev, sourceCommandID, hasSourceCommand, turnID, hasTurn)
	o.set("kind", v4pub.Str("permission.lifecycle"))
	o.set("phase", v4pub.Str(phase))
	reqID, hasReq := permissionRequestID(requested, resolved)
	o.setStr(hasReq, "requestId", reqID)
	o.set("toolCallId", v4pub.Str(mustString(m, "toolCallId")))
	toolName := ""
	if requested != nil {
		if v, ok := nonEmpty(requested, "toolName"); ok {
			toolName = v
		}
	} else if denied != nil {
		if v, ok := nonEmpty(denied, "toolName"); ok {
			toolName = v
		}
	}
	o.setStr(toolName != "", "toolName", toolName)
	if cs, ok := nonEmpty(m, "childSessionId"); ok {
		o.set("childSessionId", v4pub.Str(cs))
	}
	if b, ok := boolField(m, "background"); ok && b {
		o.set("background", v4pub.Bool(true))
	}
	if resolved != nil {
		o.set("decision", v4pub.Str(mustString(resolved, "decision")))
	}
	return o
}
