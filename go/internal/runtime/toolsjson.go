package runtime

import (
	"encoding/json"
	"strings"

	"github.com/tylinux/nex/go/internal/modelclient"
	"github.com/tylinux/nex/go/internal/tools"
)

// toolsToCallJSON renders the assistant message's tool_calls array for the next
// request, in the provider shape.
func (st *modelStep) toolsToCallJSON() json.RawMessage {
	calls := make([]map[string]any, 0, len(st.tools))
	for _, exec := range st.tools {
		name := exec.toolName
		args := exec.rawInput
		if args == "" {
			args = exec.inputText
		}
		if args == "" {
			args = "{}"
		}
		calls = append(calls, map[string]any{
			"id":       exec.callID,
			"type":     "function",
			"function": map[string]any{"name": name, "arguments": args},
		})
	}
	b, _ := json.Marshal(calls)
	return b
}

// toolsWrote reports whether the step's tools include a *successful* workspace
// write (Write/Edit), which emits checkpoint_created. A failed or denied call
// changed nothing, so it produces no checkpoint — emitFileMutationCheckpoint
// returns early on `!options.result.success` (runtime/methods/tools.ts).
func (s *session) toolsWrote(tools []toolExecution) bool {
	for _, exec := range tools {
		if exec.denied || !exec.success {
			continue
		}
		if exec.toolName == "Write" || exec.toolName == "Edit" {
			return true
		}
	}
	return false
}

// toolInputFlushChars mirrors TOOL_INPUT_STREAM_DELTA_FALLBACK_FLUSH_CHARS.
const toolInputFlushChars = 4096

// toolInputBuffers holds the pending tool-argument deltas for one model step,
// keyed by tool call id. The runtime buffers them the way core does
// (appendToolInputDelta): a delta is only emitted on a line break or after the
// fallback threshold, and always at tool_input_end.
func (st *modelStep) appendToolInputDelta(s *session, turnID, assistantMessageID, id, delta string) {
	if delta == "" {
		return
	}
	if st.toolInputBuffers == nil {
		st.toolInputBuffers = map[string]string{}
	}
	next := st.toolInputBuffers[id] + delta
	st.toolInputBuffers[id] = next
	if hasToolInputLineBreak(next) || len(next) >= toolInputFlushChars {
		st.flushToolInputDelta(s, turnID, assistantMessageID, id)
	}
}

func (st *modelStep) flushToolInputDelta(s *session, turnID, assistantMessageID, id string) {
	delta := st.toolInputBuffers[id]
	if delta == "" {
		return
	}
	delete(st.toolInputBuffers, id)
	s.appendStreamEvent(turnID, assistantMessageID, "tool_input_delta", delta)
}

func hasToolInputLineBreak(v string) bool {
	return strings.Contains(v, "\n") || strings.Contains(v, "\r") ||
		strings.Contains(v, `\n`) || strings.Contains(v, `\r`)
}

// scheduledPayload builds the tool_call_scheduled payload with the input in the
// model's key order (a Go map would sort keys and break the row's inputText).
type scheduledPayloadWire struct {
	ToolCallID         string            `json:"toolCallId"`
	AssistantMessageID string            `json:"assistantMessageId"`
	ToolName           string            `json:"toolName"`
	Input              json.RawMessage   `json:"input"`
	ParallelGroupIndex int               `json:"parallelGroupIndex"`
	CanRunParallel     bool              `json:"canRunParallel"`
	Schedule           scheduledSchedule `json:"schedule"`
}

type scheduledSchedule struct {
	ParallelGroups [][]string `json:"parallelGroups"`
	ExecutionOrder []string   `json:"executionOrder"`
}

func scheduledPayload(call modelclient.ToolCall, assistantMessageID string, index int, inputJSON string) json.RawMessage {
	in := json.RawMessage(inputJSON)
	if len(in) == 0 || !json.Valid(in) {
		in = json.RawMessage("{}")
	}
	b, _ := json.Marshal(scheduledPayloadWire{
		ToolCallID: call.ID, AssistantMessageID: assistantMessageID, ToolName: call.Name,
		Input: in, ParallelGroupIndex: index, CanRunParallel: false,
		Schedule: scheduledSchedule{ParallelGroups: [][]string{{call.ID}}, ExecutionOrder: []string{call.ID}},
	})
	return b
}

// orderedRaw returns the model's raw argument JSON (key order preserved) as a
// RawMessage, or the marshalled input when the raw text is unavailable.
func orderedRaw(arguments string, input any) json.RawMessage {
	trimmed := strings.TrimSpace(arguments)
	if trimmed != "" && json.Valid([]byte(trimmed)) {
		return json.RawMessage(trimmed)
	}
	return json.RawMessage(canonicalInputString(input))
}

// preparedInputJSON returns the tool input as Node's runtime schema leaves it
// (unknown keys stripped, replace_all defaulted for Edit) in the model's key
// order. A Go map would sort keys, so the ordered raw text is rewritten in
// place; replace_all is appended when the schema adds it.
func preparedInputJSON(toolName, rawArguments string, input any) string {
	trimmed := strings.TrimSpace(rawArguments)
	if trimmed == "" || !json.Valid([]byte(trimmed)) || !strings.HasPrefix(trimmed, "{") {
		return canonicalInputString(input)
	}
	dec := json.NewDecoder(strings.NewReader(trimmed))
	dec.UseNumber()
	var fields []orderedField
	if err := dec.Decode(&orderedObject{fields: &fields}); err != nil {
		return canonicalInputString(input)
	}
	allowed := allowedKeys(toolName)
	out := make([]orderedField, 0, len(fields)+1)
	seenReplace := false
	for _, f := range fields {
		if allowed != nil && !allowed[f.key] {
			continue
		}
		if f.key == "replace_all" {
			seenReplace = true
		}
		out = append(out, f)
	}
	if toolName == "Edit" && !seenReplace {
		out = append(out, orderedField{key: "replace_all", value: json.RawMessage("false")})
	}
	return renderOrderedObject(out)
}

func allowedKeys(toolName string) map[string]bool {
	switch toolName {
	case "Read":
		return map[string]bool{"file_path": true, "offset": true, "limit": true}
	case "Write":
		return map[string]bool{"file_path": true, "content": true}
	case "Edit":
		return map[string]bool{"file_path": true, "old_string": true, "new_string": true, "replace_all": true}
	}
	return nil
}

type orderedField struct {
	key   string
	value json.RawMessage
}

type orderedObject struct {
	fields *[]orderedField
}

func (o *orderedObject) UnmarshalJSON(data []byte) error {
	dec := json.NewDecoder(strings.NewReader(string(data)))
	dec.UseNumber()
	tok, err := dec.Token()
	if err != nil {
		return err
	}
	if d, ok := tok.(json.Delim); !ok || d != '{' {
		return nil
	}
	for dec.More() {
		keyTok, err := dec.Token()
		if err != nil {
			return err
		}
		key, _ := keyTok.(string)
		var raw json.RawMessage
		if err := dec.Decode(&raw); err != nil {
			return err
		}
		*o.fields = append(*o.fields, orderedField{key: key, value: raw})
	}
	_, err = dec.Token()
	return err
}

func renderOrderedObject(fields []orderedField) string {
	var b strings.Builder
	b.WriteByte('{')
	for i, f := range fields {
		if i > 0 {
			b.WriteByte(',')
		}
		k, _ := json.Marshal(f.key)
		b.Write(k)
		b.WriteByte(':')
		b.Write(f.value)
	}
	b.WriteByte('}')
	return b.String()
}

// fileDiffDisplay builds the tool result's file_diff display from a Write/Edit
// structured patch, mirroring createToolResultDisplay (result-display.ts).
func fileDiffDisplay(filePath string, hunks []tools.DiffHunk) json.RawMessage {
	if filePath == "" || len(hunks) == 0 {
		return nil
	}
	additions, deletions := 0, 0
	for _, h := range hunks {
		for _, line := range h.Lines {
			if strings.HasPrefix(line, "+") {
				additions++
			}
			if strings.HasPrefix(line, "-") {
				deletions++
			}
		}
	}
	bounded, truncated := boundDiffHunks(hunks)
	return mustJSON(map[string]any{
		"kind": "file_diff", "filePath": filePath,
		"additions": additions, "deletions": deletions,
		"structuredPatch": bounded, "truncated": truncated,
	})
}

const (
	maxDisplayDiffHunks = 8
	maxDisplayDiffLines = 160
)

func boundDiffHunks(hunks []tools.DiffHunk) ([]tools.DiffHunk, bool) {
	bounded := make([]tools.DiffHunk, 0, len(hunks))
	remaining := maxDisplayDiffLines
	truncated := len(hunks) > maxDisplayDiffHunks
	for i, h := range hunks {
		if i >= maxDisplayDiffHunks {
			break
		}
		if remaining <= 0 {
			truncated = true
			break
		}
		lines := h.Lines
		if len(lines) > remaining {
			lines = lines[:remaining]
		}
		nh := h
		nh.Lines = lines
		bounded = append(bounded, nh)
		remaining -= len(lines)
		if len(lines) < len(h.Lines) {
			truncated = true
			break
		}
	}
	return bounded, truncated
}
