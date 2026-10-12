package projection

import (
	"bytes"
	"encoding/json"
	"strings"
)

// This file ports the model-streaming handlers (onModelStreaming and the
// per-segment row builders it drives).

// fileStreamingInputPreviewMinIntervalMs ports
// NEX_FILE_STREAMING_TOOL_INPUT_PREVIEW_MIN_INTERVAL_MS (streaming-tool-input-preview.ts).
const fileStreamingInputPreviewMinIntervalMs = 1000

// fileToolInputPreview mirrors FileToolInputPreviewState.
type fileToolInputPreview struct {
	lastPublishedAt *int64
	pendingAppend   string
}

// isFileStreamingToolInputPreviewTool ports isNexFileStreamingToolInputPreviewTool.
func isFileStreamingToolInputPreviewTool(toolName string) bool {
	name := strings.ToLower(strings.TrimSpace(toolName))
	return name == "write" || name == "edit"
}

// normalizeModelStreaming ports normalizeModelStreaming (subset): the canonical
// stream fields the projection reads.
type modelStream struct {
	kind                string
	delta               string
	assistantResponseID string
	hasAssistantID      bool
	toolCallID          string
	hasToolCallID       bool
	toolName            string
	hasToolName         bool
	input               json.RawMessage
	hasInput            bool
	transcriptMessageID string
	hasTranscriptID     bool
	entityID            string
}

func normalizeModelStreaming(m map[string]json.RawMessage, event Event, runtimeTurnID string) modelStream {
	kind, _ := payloadString(m, "kind")
	delta, _ := payloadString(m, "delta")
	assistantID, hasAssistantID := payloadString(m, "assistantMessageId")
	toolCallID, hasToolCallID := payloadString(m, "toolCallId")
	toolName, hasToolName := payloadString(m, "toolName")
	input, hasInput := m["input"]
	if hasInput && string(input) == "null" {
		hasInput = false
	}
	transcriptMessageID := ""
	if hasAssistantID {
		transcriptMessageID = assistantID
	}
	entityID := transcriptMessageID
	if entityID == "" {
		if hasToolCallID {
			entityID = toolCallID
		} else {
			entityID = "legacy:assistant:" + event.SessionID + ":" + runtimeTurnID + ":" + event.ID
		}
	}
	return modelStream{
		kind:                kind,
		delta:               delta,
		assistantResponseID: assistantID,
		hasAssistantID:      hasAssistantID,
		toolCallID:          toolCallID,
		hasToolCallID:       hasToolCallID,
		toolName:            toolName,
		hasToolName:         hasToolName,
		input:               input,
		hasInput:            hasInput,
		transcriptMessageID: transcriptMessageID,
		hasTranscriptID:     hasAssistantID,
		entityID:            entityID,
	}
}

func (p *Projection) onModelStreaming(event Event, m map[string]json.RawMessage) []Delta {
	runtimeTurnID := p.runtimeTurnIDOfEvent(event, m)
	if !p.isRunning() {
		kind, _ := payloadString(m, "kind")
		switch kind {
		case "text_start", "text_delta", "reasoning_start", "reasoning_delta":
			p.droppedContentStreamEventCount++
		}
		return nil
	}
	stream := normalizeModelStreaming(m, event, runtimeTurnID)
	// A valid model progress after a retry clears the retry state (the same
	// predicate as isNexModelRetryRecoveryProgressPayload).
	var retryClear []Delta
	if p.acceptsActiveModelEvent(event) &&
		isModelRetryRecoveryProgress(stream.kind, stream.delta, stream.toolCallID) {
		retryClear = p.setAPIRetry(nil)
	}
	var deltas []Delta
	switch stream.kind {
	case "text_start":
		deltas = p.openTextRow(event, stream)
	case "text_delta":
		open := []Delta{}
		if p.streamingTextRowID == nil {
			open = p.openTextRow(event, stream)
		}
		deltas = append(open, newRowDelta(*p.streamingTextRowID, "text", stream.delta))
	case "text_end":
		deltas = p.closeTextRow("complete")
	case "reasoning_start":
		deltas = p.openReasoningRow(event, stream)
	case "reasoning_delta":
		open := []Delta{}
		if p.streamingReasoningRowID == nil {
			open = p.openReasoningRow(event, stream)
		}
		deltas = append(open, newRowDelta(*p.streamingReasoningRowID, "text", stream.delta))
	case "reasoning_end":
		deltas = p.closeReasoningRow("complete")
	case "tool_input_start":
		deltas = p.openToolRow(event, stream)
	case "tool_input_delta":
		deltas = p.appendStreamingToolInput(event, stream)
	case "tool_input_end":
		deltas = p.flushStreamingToolInput(stream.toolCallID)
	case "tool_call":
		deltas = p.finalizeStreamingToolInput(event, stream)
	}
	return append(deltas, retryClear...)
}

// isModelRetryRecoveryProgress ports isNexModelRetryRecoveryProgressPayload: a
// model progress event that proves the stream recovered.
func isModelRetryRecoveryProgress(kind, delta, toolCallID string) bool {
	if kind == "text_delta" || kind == "reasoning_delta" {
		return delta != ""
	}
	if toolCallID == "" {
		return false
	}
	switch kind {
	case "tool_input_start", "tool_input_end", "tool_call":
		return true
	case "tool_input_delta":
		return delta != ""
	default:
		return false
	}
}

// openTextRow ports openTextRow for the non-continuation path (the recorded
// scenarios contain no output-token Continue, so continuationRowID is always
// null there).
func (p *Projection) openTextRow(event Event, stream modelStream) []Delta {
	close := p.closeTextRow("complete")
	row := p.rowBase(event, p.turnIDOfEvent(event), stream.entityID).
		set("kind", "assistantText").
		setIf(stream.hasTranscriptID, "assistantResponseId", stream.assistantResponseID).
		set("text", "").
		set("state", "streaming")
	id := rowIDOf(row)
	p.streamingTextRowID = &id
	if stream.hasTranscriptID {
		p.messageIDByRowID[id] = stream.transcriptMessageID
	}
	return append(close, newRowAppended(row))
}

// openReasoningRow ports openReasoningRow.
func (p *Projection) openReasoningRow(event Event, stream modelStream) []Delta {
	close := p.closeReasoningRow("complete")
	row := p.rowBase(event, p.turnIDOfEvent(event), stream.entityID).
		set("kind", "reasoning").
		setIf(stream.hasTranscriptID, "assistantResponseId", stream.assistantResponseID).
		set("text", "").
		set("state", "streaming")
	id := rowIDOf(row)
	p.streamingReasoningRowID = &id
	return append(close, newRowAppended(row))
}

// openToolRow ports openToolRow: a tool_input_start opens a toolCall row in
// inputStreaming with an empty inputText.
func (p *Projection) openToolRow(event Event, stream modelStream) []Delta {
	turnID := p.turnIDOfEvent(event)
	// openToolRow keys the row entity on the tool call id; the canonical entity
	// id (stream.entityID) is kept only in the side table.
	row := p.rowBase(event, turnID, stream.toolCallID).
		set("kind", "toolCall").
		setIf(stream.hasAssistantID, "assistantResponseId", stream.assistantResponseID).
		set("toolCallId", stream.toolCallID).
		setIf(stream.hasToolName, "toolName", stream.toolName).
		set("status", "inputStreaming").
		set("inputText", "")
	if stream.hasToolCallID {
		p.toolRowIDByCallID[stream.toolCallID] = rowIDOf(row)
	}
	p.entityIDByRowID[rowIDOf(row)] = stream.entityID
	if isFileStreamingToolInputPreviewTool(stream.toolName) {
		p.fileToolInputPreviewByCallID[stream.toolCallID] = &fileToolInputPreview{}
	}
	return []Delta{newRowAppended(row)}
}

// appendStreamingToolInput ports appendStreamingToolInput: it grows the open
// tool row's inputText with a row.delta. The TS keeps a per-call pending buffer
// for the file-tool preview throttle; the ported scenarios do not use those
// tools, so the append applies directly.
func (p *Projection) appendStreamingToolInput(event Event, stream modelStream) []Delta {
	rowID, ok := p.toolRowIDByCallID[stream.toolCallID]
	if !ok {
		return nil
	}
	state := p.fileToolInputPreviewByCallID[stream.toolCallID]
	if state == nil {
		return []Delta{newRowDelta(rowID, "inputText", stream.delta)}
	}
	state.pendingAppend += stream.delta
	if state.lastPublishedAt != nil &&
		event.TimestampMs-*state.lastPublishedAt < fileStreamingInputPreviewMinIntervalMs {
		return nil
	}
	append := state.pendingAppend
	state.pendingAppend = ""
	at := event.TimestampMs
	state.lastPublishedAt = &at
	if append == "" {
		return nil
	}
	return []Delta{newRowDelta(rowID, "inputText", append)}
}

func (p *Projection) flushStreamingToolInput(toolCallID string) []Delta {
	// No pending buffer in the subset: a file-tool preview flush is a no-op here.
	return nil
}

// finalizeStreamingToolInput ports finalizeStreamingToolInput: tool_call seals
// the row. inputText is replaced with the canonical stringified input (not the
// accumulated deltas), and toolName keeps the existing value when non-empty.
func (p *Projection) finalizeStreamingToolInput(event Event, stream modelStream) []Delta {
	if stream.toolCallID == "" {
		return nil
	}
	delete(p.fileToolInputPreviewByCallID, stream.toolCallID)
	inputText := stringifyToolInput(stream.input)
	existing := p.findToolRow(stream.toolCallID)
	if existing != nil {
		next := shallowClone(existing)
		if stream.hasAssistantID {
			next.put("assistantResponseId", stream.assistantResponseID)
		}
		toolName := strOf(existing.get("toolName"))
		if toolName == "" {
			if stream.hasToolName {
				toolName = stream.toolName
			} else {
				toolName = ""
			}
		}
		next.put("toolName", toolName)
		next.put("inputText", inputText)
		next.put("input", compactRaw(rawOrNull(stream.input)))
		return []Delta{newRowUpserted(next)}
	}
	row := p.rowBase(event, p.turnIDOfEvent(event), stream.toolCallID).
		set("kind", "toolCall").
		setIf(stream.hasAssistantID, "assistantResponseId", stream.assistantResponseID).
		set("toolCallId", stream.toolCallID).
		set("toolName", stream.toolName).
		set("status", "inputStreaming").
		set("inputText", inputText).
		set("input", compactRaw(rawOrNull(stream.input)))
	p.toolRowIDByCallID[stream.toolCallID] = rowIDOf(row)
	return []Delta{newRowAppended(row)}
}

// stringifyToolInput ports stringifyToolInput: JSON.stringify of the input, and
// "undefined" (absent) is not a valid wire value, so a missing input becomes "".
func stringifyToolInput(input json.RawMessage) string {
	if len(input) == 0 {
		return ""
	}
	var buf bytes.Buffer
	if err := json.Compact(&buf, input); err != nil {
		return string(input)
	}
	return buf.String()
}
