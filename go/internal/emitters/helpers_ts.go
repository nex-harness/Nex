package emitters

import (
	"encoding/json"

	"github.com/tylinux/nex/go/internal/v4pub"
)

// recordOf returns a nested object value from a parent map, {} when absent.
func recordOf(m map[string]json.RawMessage, key string) map[string]json.RawMessage {
	return record(m, key)
}

// hasErrorObject reports whether errObj is a truthy error payload. record()
// yields {} for absent/null, and the TS `result?.result.error ? ...` is falsy
// for an empty object only when the key was absent; a present object is truthy.
// We treat "present and not an empty JSON object" as present by checking the
// result map was non-nil and the key existed.
func hasErrorObject(errObj map[string]json.RawMessage) bool {
	return len(errObj) > 0
}

// resultPerf reads result.perf (only for ToolCallResult).
func resultPerf(m map[string]json.RawMessage, isResult bool) map[string]json.RawMessage {
	if !isResult {
		return nil
	}
	raw, ok := record(m, "result")["perf"]
	if !ok || isJSONNull(raw) {
		return nil
	}
	var out map[string]json.RawMessage
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil
	}
	return out
}

// errorSkillMetadata returns payload.skillMetadata for error events.
func errorSkillMetadata(isError bool, m map[string]json.RawMessage) map[string]json.RawMessage {
	if !isError {
		return nil
	}
	return record(m, "skillMetadata")
}

func firstMap(a, b map[string]json.RawMessage) map[string]json.RawMessage {
	if a != nil {
		return a
	}
	return b
}

// errorObjectCode ports `error.code ?? error.type`.
func errorObjectCode(errObj map[string]json.RawMessage) string {
	if v, ok := nonEmpty(errObj, "code"); ok {
		return v
	}
	return mustString(errObj, "type")
}

// skillTelemetryFactFields ports skillTelemetryFactFields: only for Skill.
func skillTelemetryFields(o *objBuilder, toolName string, metadata map[string]json.RawMessage) {
	if toolName != "Skill" || metadata == nil {
		return
	}
	if v, ok := nonEmpty(metadata, "qualifiedName"); ok {
		o.set("skillQualifiedName", v4pub.Str(v))
	}
	if v, ok := nonEmpty(metadata, "pluginId"); ok {
		o.set("skillPluginId", v4pub.Str(v))
	}
	if v, ok := nonEmpty(metadata, "source"); ok {
		o.set("skillSource", v4pub.Str(v))
	}
}

// mirroredSubagentFields ports mirroredSubagentToolFields: parent/child ids and
// agent attribution are read from the payload first, then the display record.
func mirroredSubagentFields(o *objBuilder, m map[string]json.RawMessage, display map[string]json.RawMessage) {
	if display == nil {
		display = map[string]json.RawMessage{}
	}
	setFrom := func(key string) {
		if v, ok := firstNonEmpty(m, display, key); ok {
			o.set(key, v4pub.Str(v))
		}
	}
	setFrom("parentToolCallId")
	setFrom("childToolCallId")
	setFrom("agentId")
	setFrom("agentType")
	setFrom("childSessionId")
	if b, ok := boolField(m, "background"); ok && b {
		o.set("background", v4pub.Bool(true))
	}
}

func firstNonEmpty(a, b map[string]json.RawMessage, key string) (string, bool) {
	if v, ok := nonEmpty(a, key); ok {
		return v, true
	}
	if v, ok := nonEmpty(b, key); ok {
		return v, true
	}
	return "", false
}

// cronCreateAutomationId ports cronCreateAutomationId(content): parse content as
// JSON (if a string) and read automation.automationId.
func cronCreateAutomationID(content json.RawMessage) string {
	if len(content) == 0 {
		return ""
	}
	var value json.RawMessage = content
	// If the value is a JSON string, parse its contents as JSON.
	var s string
	if err := json.Unmarshal(content, &s); err == nil {
		value = json.RawMessage(s)
	}
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(value, &obj); err != nil {
		return ""
	}
	automation := record(obj, "automation")
	if v, ok := nonEmpty(automation, "automationId"); ok {
		return v
	}
	return ""
}

// permissionRequestID ports `optionalString(requested?.requestId ?? resolved?.requestId)`.
func permissionRequestID(requested, resolved map[string]json.RawMessage) (string, bool) {
	if v, ok := nonEmpty(requested, "requestId"); ok {
		return v, true
	}
	if v, ok := nonEmpty(resolved, "requestId"); ok {
		return v, true
	}
	return "", false
}
