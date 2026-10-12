package projection

import "encoding/json"

// This file ports the tool-call and permission-interaction handlers.

// onToolCallScheduled ports onToolCallScheduled (non-mirrored, running path).
func (p *Projection) onToolCallScheduled(event Event, m map[string]json.RawMessage) []Delta {
	if !p.isRunning() {
		return nil
	}
	toolCallID, _ := payloadString(m, "toolCallId")
	toolName, _ := payloadString(m, "toolName")
	input := m["input"]
	inputText := stringifyToolInput(input)
	assistantMessageID, hasAssistant := payloadString(m, "assistantMessageId")
	existing := p.findToolRow(toolCallID)
	if existing != nil {
		next := shallowClone(existing)
		if hasAssistant {
			next.put("assistantResponseId", assistantMessageID)
		}
		next.put("inputText", inputText)
		next.put("input", rawOrNull(input))
		return []Delta{newRowUpserted(next)}
	}
	row := p.rowBase(event, p.turnIDOfEvent(event), toolCallID).
		set("kind", "toolCall").
		setIf(hasAssistant, "assistantResponseId", assistantMessageID).
		set("toolCallId", toolCallID).
		set("toolName", toolName).
		set("status", "inputStreaming").
		set("inputText", inputText).
		set("input", compactRaw(rawOrNull(input)))
	p.toolRowIDByCallID[toolCallID] = rowIDOf(row)
	return []Delta{newRowAppended(row)}
}

// onToolCallActivity ports onToolCallActivity / projectToolActivity.
func (p *Projection) onToolCallActivity(event Event, m map[string]json.RawMessage) []Delta {
	if !p.isRunning() {
		return nil
	}
	toolCallID, _ := payloadString(m, "toolCallId")
	row := p.findToolRow(toolCallID)
	if row == nil {
		return nil
	}
	if event.Type == TypeToolCallStarted {
		next := shallowClone(row).
			put("status", "running").
			put("startedAt", event.TimestampMs)
		return []Delta{newRowUpserted(next)}
	}
	// ToolCallProgress only updates a running, non-backgrounded Bash row; not
	// modelled further (no outputPreview in the ported scenarios).
	return nil
}

// onToolCallResult ports onToolCallResult.
func (p *Projection) onToolCallResult(event Event, m map[string]json.RawMessage) []Delta {
	if !p.isRunning() {
		return nil
	}
	toolCallID, _ := payloadString(m, "toolCallId")
	row := p.findToolRow(toolCallID)
	if row == nil {
		return nil
	}
	var result struct {
		Success bool            `json:"success"`
		Content string          `json:"content"`
		Display json.RawMessage `json:"display"`
	}
	if r, ok := m["result"]; ok {
		_ = json.Unmarshal(r, &result)
	}
	next := shallowClone(row).
		put("status", map[bool]string{true: "success", false: "error"}[result.Success]).
		put("output", buildToolOutput(result.Content, result.Display, toolCallID)).
		put("endedAt", event.TimestampMs)
	if !result.Success {
		next.put("error", newObj().
			set("code", "fault.runtime.toolFailed").
			set("message", "Tool execution failed."))
	}
	return []Delta{newRowUpserted(next)}
}

// onToolCallError ports onToolCallError (product-projection.ts:3032).
//
// A failed call publishes tool_call_error and *no* tool_call_result, so this is
// the only delta that settles a failed row: status error (or cancelled when the
// error is a cancellation), `error:{code,message}` taken from the payload's
// error.type/error.message, and NO `output` key — the row never received one.
func (p *Projection) onToolCallError(event Event, m map[string]json.RawMessage) []Delta {
	if !p.isRunning() {
		return nil
	}
	toolCallID, _ := payloadString(m, "toolCallId")
	row := p.findToolRow(toolCallID)
	if row == nil {
		return nil
	}
	errorType, errorCode, hasError := payloadError(m)
	// Stop emits tool_cancelled before the cancelled turn; a row already written
	// as error would not be corrected by the turn reducer, so cancellation keeps
	// its own status and drops the error.
	cancelled := errorType == coreErrorToolCancelled || errorCode == "TOOL_CANCELLED"
	next := shallowClone(row).put("status", map[bool]string{true: "cancelled", false: "error"}[cancelled])
	if cancelled {
		next.del("error")
	} else if hasError {
		next.put("error", newObj().
			set("code", errorType).
			set("message", payloadMessage(m)))
	}
	next.put("endedAt", event.TimestampMs)
	return []Delta{newRowUpserted(next)}
}

// coreErrorToolCancelled mirrors CoreErrorType.ToolCancelled.
const coreErrorToolCancelled = "tool_cancelled"

// payloadError reads payload.error's `type` and `code`. The `type` is what the
// row shows as error.code; `code` only selects the cancelled status.
func payloadError(m map[string]json.RawMessage) (errorType, errorCode string, ok bool) {
	raw, has := m["error"]
	if !has || len(raw) == 0 || string(raw) == "null" {
		return "", "", false
	}
	var errObj map[string]json.RawMessage
	if err := json.Unmarshal(raw, &errObj); err != nil {
		return "", "", false
	}
	errorType, _ = payloadString(errObj, "type")
	errorCode, _ = payloadString(errObj, "code")
	return errorType, errorCode, errorType != "" || errorCode != ""
}

// payloadMessage reads payload.error.message.
func payloadMessage(m map[string]json.RawMessage) string {
	raw, has := m["error"]
	if !has {
		return ""
	}
	var errObj map[string]json.RawMessage
	if err := json.Unmarshal(raw, &errObj); err != nil {
		return ""
	}
	msg, _ := payloadString(errObj, "message")
	return msg
}

// onPermissionRequested ports onPermissionRequested (permission kind path).
func (p *Projection) onPermissionRequested(event Event, m map[string]json.RawMessage) []Delta {
	toolCallID, _ := payloadString(m, "toolCallId")
	requestID, hasRequestID := payloadString(m, "requestId")
	interactionID := "perm-" + toolCallID
	if hasRequestID {
		interactionID = requestID
	}
	toolName, _ := payloadString(m, "toolName")
	reason, _ := payloadString(m, "reason")
	input, _ := m["input"]
	display, hasDisplay := m["display"]
	if hasDisplay && string(display) == "null" {
		hasDisplay = false
	}

	interaction := newObj().
		set("interactionId", interactionID).
		set("kind", "permission").
		set("anchorRowId", p.anchorRowID(toolCallID)).
		set("createdAt", event.TimestampMs)

	fullAccessSupported, _ := payloadBool(m, "fullAccessSupported")
	_, hasOrigin := m["origin"]
	_, hasOptionsPolicy := m["optionsPolicy"]
	payload := newObj().
		set("kind", "permission").
		set("toolCallId", toolCallID).
		set("toolName", toolName).
		set("summary", reason).
		set("detail", compactRaw(rawOrNull(input))).
		set("freeText", true)
	// fullAccessOption is only offered when the tool advertises v4 full-access
	// support and the request has no origin/optionsPolicy override.
	if fullAccessSupported && !hasOrigin && !hasOptionsPolicy {
		payload.set("fullAccessOption", newObj().
			set("optionId", "fullAccess").
			set("label", "Full access").
			set("kind", "custom").
			set("response", newObj().
				set("decision", "deny").
				set("reason", "Full access requires V4 approval")))
	}
	if hasDisplay {
		payload.set("display", compactRaw(display))
	}
	payload.set("options", p.protocolPermissionOptions(toolName, input))
	interaction.set("payload", payload)

	deltas := []Delta{}
	if row := p.findToolRow(toolCallID); row != nil {
		deltas = append(deltas, newRowUpserted(shallowClone(row).
			put("status", "pendingApproval").
			put("approvalInteractionId", interactionID)))
	}
	pending, _ := p.snapshot.get("pendingInteractions").([]any)
	deltas = append(deltas, newStateUpdated(newObj().
		set("pendingInteractions", append(append([]any{}, pending...), interaction))))
	return deltas
}

func (p *Projection) anchorRowID(toolCallID string) any {
	if rowID, ok := p.toolRowIDByCallID[toolCallID]; ok {
		return rowID
	}
	return nil
}

// onPermissionResolved ports onPermissionResolved: deny → cancelled, else running.
func (p *Projection) onPermissionResolved(event Event, m map[string]json.RawMessage) []Delta {
	toolCallID, _ := payloadString(m, "toolCallId")
	decision, _ := payloadString(m, "decision")
	status := "running"
	if decision == "deny" {
		status = "cancelled"
	}
	return p.settlePermission(toolCallID, status)
}

// onPermissionDenied ports onPermissionDenied.
func (p *Projection) onPermissionDenied(event Event, m map[string]json.RawMessage) []Delta {
	toolCallID, _ := payloadString(m, "toolCallId")
	return p.settlePermission(toolCallID, "cancelled")
}

// settlePermission ports settlePermission.
func (p *Projection) settlePermission(toolCallID, status string) []Delta {
	deltas := []Delta{}
	if row := p.findToolRow(toolCallID); row != nil {
		next := shallowClone(row).put("status", status)
		next.del("approvalInteractionId")
		deltas = append(deltas, newRowUpserted(next))
	}
	pending, _ := p.snapshot.get("pendingInteractions").([]any)
	kept := make([]any, 0, len(pending))
	for _, item := range pending {
		interaction, ok := item.(*obj)
		if ok {
			payload, _ := interaction.get("payload").(*obj)
			if payload != nil && strOf(payload.get("toolCallId")) == toolCallID {
				continue
			}
		}
		kept = append(kept, item)
	}
	if len(kept) != len(pending) {
		deltas = append(deltas, newStateUpdated(newObj().set("pendingInteractions", kept)))
	}
	return deltas
}

// protocolPermissionOptions ports buildProtocolPermissionOptions
// (permission-options.ts) for the default policy.
func (p *Projection) protocolPermissionOptions(toolName string, input json.RawMessage) []any {
	permissionUpdates := newObj().
		set("behavior", "allow").
		set("rules", []any{ruleFor(toolName, input)}).
		set("type", "addRules")
	return []any{
		newObj().
			set("optionId", "allowOnce").
			set("label", "Allow once").
			set("kind", "allowOnce").
			set("response", newObj().set("decision", "allow").set("reason", "Approved once")),
		newObj().
			set("optionId", "allowAlways").
			set("label", "Always allow in this project").
			set("kind", "allowAlways").
			set("response", newObj().
				set("decision", "allow").
				set("permissionUpdates", []any{permissionUpdates}).
				set("reason", "Approved for this project")),
		newObj().
			set("optionId", "deny").
			set("label", "Deny").
			set("kind", "deny").
			set("response", newObj().
				set("decision", "deny").
				set("reason", permissionDeniedByUserContent)),
	}
}

func ruleFor(toolName string, input json.RawMessage) *obj {
	rule := newObj().set("toolName", toolName)
	if content := ruleContentFromInput(input); content != "" {
		rule.set("ruleContent", content)
	}
	return rule
}

const permissionDeniedByUserContent = "The user doesn't want to proceed with this tool use. The tool use was rejected (eg. if it was a file edit, the new_string was NOT written to the file). STOP what you are doing and wait for the user to tell you how to proceed."

// ruleContentFromInput ports ruleContentFromPermissionInput: the first matching
// project rule input key (command/url/file_path/path/pattern).
func ruleContentFromInput(input json.RawMessage) string {
	if len(input) == 0 || string(input) == "null" {
		return ""
	}
	var s string
	if err := json.Unmarshal(input, &s); err == nil {
		return s
	}
	var record map[string]json.RawMessage
	if err := json.Unmarshal(input, &record); err != nil {
		return ""
	}
	for _, key := range []string{"command", "url", "file_path", "path", "pattern"} {
		if v, ok := payloadString(record, key); ok && v != "" {
			return v
		}
	}
	return ""
}

// toolOutputFinalHeadBytes/TailBytes are PROTOCOL_V4_LIMITS.toolOutputFinalHeadBytes
// / TailBytes (32 KiB each).
const (
	toolOutputFinalHeadBytes = 32 * 1024
	toolOutputFinalTailBytes = 32 * 1024
)

// buildToolOutput ports buildToolOutput (projection-rows.ts): the row output
// carries the model text, an optional display (every kind except
// node_repl_images) and a truncation ref when the text exceeds the head+tail
// budget.
func buildToolOutput(text string, display json.RawMessage, toolCallID string) *obj {
	out := newObj().set("text", text)
	hasDisplay := len(display) > 0 && string(display) != "null"
	if hasDisplay {
		var kind struct {
			Kind string `json:"kind"`
		}
		_ = json.Unmarshal(display, &kind)
		if kind.Kind == "node_repl_images" {
			hasDisplay = false
		}
	}
	total := len([]byte(text))
	if total <= toolOutputFinalHeadBytes+toolOutputFinalTailBytes {
		if hasDisplay {
			out.set("display", compactRaw(display))
		}
		return out
	}
	b := []byte(text)
	head := string(b[:toolOutputFinalHeadBytes])
	tail := string(b[len(b)-toolOutputFinalTailBytes:])
	out.set("text", head+"\n…\n"+tail)
	if hasDisplay {
		out.set("display", compactRaw(display))
	}
	out.set("truncated", newObj().
		set("totalBytes", int64(total)).
		set("ref", "tool-output/"+toolCallID))
	return out
}
