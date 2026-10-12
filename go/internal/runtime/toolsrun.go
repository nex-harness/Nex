package runtime

import (
	"encoding/json"
	"strings"

	"github.com/tylinux/nex/go/internal/modelclient"
	"github.com/tylinux/nex/go/internal/tools"
)

// toolExecution is the outcome of running one model tool call.
type toolExecution struct {
	callID    string
	toolName  string
	input     any
	inputText string
	started   bool
	denied    bool
	// pendingStart marks a streaming call whose tool_call_started still has to
	// be emitted after the model stream closes.
	pendingStart bool
	// earlyFailure marks a streamed call that failed before its handler started;
	// its parked event is tool_call_error and errorEmitted stops a second one.
	earlyFailure bool
	// streamed marks a call that ran while the model stream was open; it owns a
	// single-call batch in Node and so its own tool_batch_complete.
	streamed     bool
	errorEmitted bool
	// modelContent is the text the next request carries as the tool result.
	modelContent string
	success      bool
	// failure is the createErrorResult payload for a failed call (nil on
	// success). A failure publishes tool_call_error, never tool_call_result.
	failure *toolFailure
	// readOnly mirrors the tool metadata (tool_call_started.readOnly).
	readOnly bool
	// perf is the result.perf payload (tool.lifecycle performance block).
	perf json.RawMessage
	// display is the result.display payload (file_diff for Write/Edit).
	display json.RawMessage
	// rawInput is the model's raw argument JSON (key order preserved, no schema
	// defaults); the request history and the breakdown use it.
	rawInput string
	// filePath / wroteCreate / createdLines feed the fileChanges summary.
	filePath     string
	wroteCreate  bool
	createdLines int
}

// executeTool runs one tool call. Only Read, Write and Edit are ported; a call
// to anything else is a registry miss and never reaches the handler. Permission
// handling for side-effecting tools is done by the caller before this runs.
func (s *session) executeTool(callID, name string, input map[string]any) toolExecution {
	ctx := &tools.Context{
		ToolCallID:       callID,
		FS:               s.rt.deps.FS.ToolFS(),
		ReadState:        s.readState,
		WorkingDirectory: s.ws.Path,
		WorkspaceRoot:    s.ws.Path,
		SessionID:        s.id,
		Ctx:              s.rt.ctx,
	}
	switch name {
	case "Read":
		out, err := tools.Read(input, ctx)
		if err != nil {
			return s.toolFailure(callID, name, input, coreToolFailure(failureOf(err)))
		}
		return toolExecution{callID: callID, toolName: name, input: input,
			modelContent: tools.FormatReadModelContent(out), success: true, readOnly: true}
	case "Write":
		out, err := tools.Write(input, ctx)
		if err != nil {
			return s.toolFailure(callID, name, input, coreToolFailure(failureOf(err)))
		}
		return toolExecution{callID: callID, toolName: name, input: input,
			modelContent: tools.FormatWriteModelContent(out), success: true,
			perf:         s.filesystemPerf(out.FilePath, nil),
			display:      fileDiffDisplay(out.FilePath, out.StructuredPatch),
			filePath:     out.FilePath,
			wroteCreate:  out.Type == "create",
			createdLines: countContentLines(out.Content)}
	case "Edit":
		out, err := tools.Edit(input, ctx)
		if err != nil {
			// Edit reports a predictable business failure by *returning* a
			// ToolHandlerFailure; the runner turns it into a CoreError before
			// the shared failure path runs, which is what puts the numeric code
			// on the payload and wraps the model text in <tool_use_error>.
			return s.toolFailure(callID, name, input, handlerFailure(failureOf(err)))
		}
		hunks := len(out.StructuredPatch)
		return toolExecution{callID: callID, toolName: name, input: input,
			modelContent: tools.FormatEditModelContent(out), success: true,
			perf:     s.filesystemPerf(out.FilePath, &hunks),
			display:  fileDiffDisplay(out.FilePath, out.StructuredPatch),
			filePath: out.FilePath}
	default:
		return s.toolFailure(callID, name, input, toolNotFoundFailure(name))
	}
}

// failureOf unwraps a *tools.Failure. A non-Failure error is reported as an
// opaque ToolExecutionFailed, matching the runner's fallback for a thrown Error.
func failureOf(err error) *tools.Failure {
	if f, ok := err.(*tools.Failure); ok {
		return f
	}
	return &tools.Failure{Message: err.Error()}
}

// toolFailure builds the failed toolExecution. The model sees the failure text
// as the tool result; success stays false so the caller emits tool_call_error.
func (s *session) toolFailure(callID, name string, input map[string]any, f toolFailure) toolExecution {
	return toolExecution{callID: callID, toolName: name, input: input,
		modelContent: f.modelText(), success: false, failure: &f}
}

// registryMissExecution builds the failed toolExecution for a call whose tool is
// not in the registry (call-runner.ts's `if (!entry)` branch).
func (s *session) registryMissExecution(call modelclient.ToolCall, f toolFailure) toolExecution {
	return toolExecution{
		callID: call.ID, toolName: call.Name, input: call.Input,
		inputText:    preparedInputJSON(call.Name, call.Arguments, call.Input),
		rawInput:     orderedInput(call.Arguments, call.Input),
		modelContent: f.modelText(), success: false, failure: &f,
	}
}

// fileChange is one changed file's contribution to the turn's file summary.
type fileChange struct {
	additions int
	deletions int
}

// recordChangedFiles records the net change a workspace write made to a file.
//
// recordTurnFileChange (runtime/helpers/turn-file-changes.ts) keys the map by
// path and keeps the *first* write's fallback counts — a created file counts its
// lines once and a later Edit to the same file does not add a second entry — so
// the summary is the workspace diff rather than the sum of tool patches.
func (s *session) recordChangedFiles(exec toolExecution) {
	if exec.toolName != "Write" && exec.toolName != "Edit" {
		return
	}
	// A failed or denied call changed nothing on disk.
	if !exec.success || exec.denied {
		return
	}
	if s.changedFilesByPath == nil {
		s.changedFilesByPath = map[string]fileChange{}
	}
	path := exec.filePath
	if path == "" {
		return
	}
	if _, seen := s.changedFilesByPath[path]; seen {
		return
	}
	if exec.toolName == "Write" && exec.wroteCreate {
		s.changedFilesByPath[path] = fileChange{additions: exec.createdLines}
		return
	}
	adds, dels := exec.patchCounts()
	s.changedFilesByPath[path] = fileChange{additions: adds, deletions: dels}
}

// patchCounts returns the additions/deletions this execution produced.
func (e toolExecution) patchCounts() (int, int) {
	if len(e.display) == 0 {
		return 0, 0
	}
	var d struct {
		Additions int `json:"additions"`
		Deletions int `json:"deletions"`
	}
	_ = json.Unmarshal(e.display, &d)
	return d.Additions, d.Deletions
}

// countPatchLines mirrors countPatchLines (diff.ts).
func countPatchLines(hunks []tools.DiffHunk) (int, int) {
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
	return additions, deletions
}

// canonicalInputString is the JSON.stringify of the canonical tool input, as the
// tool_call_scheduled payload carries it.
func canonicalInputString(input any) string {
	b, err := json.Marshal(input)
	if err != nil {
		return "{}"
	}
	return string(b)
}

// orderedInput returns the input's canonical JSON in the model's key order. The
// raw argument string preserves insertion order, which json.Marshal of a Go map
// would sort away; the projection and the wire compare it byte-for-byte.
func orderedInput(arguments string, input any) string {
	trimmed := strings.TrimSpace(arguments)
	if trimmed != "" && json.Valid([]byte(trimmed)) {
		return trimmed
	}
	return canonicalInputString(input)
}

// filesystemPerf builds the ToolExecutionTelemetry perf block for a Write/Edit
// result: a filesystem detail (and, for Edit, a patch detail).
func (s *session) filesystemPerf(path string, hunks *int) json.RawMessage {
	size := int64(0)
	if st, err := s.rt.deps.FS.ToolFS().Stat(s.rt.ctx, path); err == nil {
		size = st.SizeBytes
	}
	fsDetail := map[string]any{
		"readMs": 0, "writeMs": 0, "fileCount": 1,
		"totalBytes": size, "maxFileBytes": size, "workspaceKind": "local",
	}
	detail := map[string]any{"kind": "filesystem", "filesystem": fsDetail}
	if hunks != nil {
		detail = map[string]any{
			"kind":       "patch",
			"filesystem": fsDetail,
			"patch":      map[string]any{"matchMs": 0, "hunkCount": *hunks, "matchAttempts": 1},
		}
	}
	return mustJSON(map[string]any{
		"totalMs": 0, "permissionWaitMs": 0, "detail": detail,
	})
}

// countContentLines counts the lines a created file has (a trailing newline does
// not add an empty line).
func countContentLines(content string) int {
	if content == "" {
		return 0
	}
	n := strings.Count(content, "\n")
	if !strings.HasSuffix(content, "\n") {
		n++
	}
	return n
}
