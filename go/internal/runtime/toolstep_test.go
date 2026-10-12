package runtime

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf16"

	"github.com/tylinux/nex/go/internal/adapters/fsx"
	"github.com/tylinux/nex/go/internal/eventstore"
	"github.com/tylinux/nex/go/internal/modelclient"
	"github.com/tylinux/nex/go/internal/modelreq"
	"github.com/tylinux/nex/go/internal/protocol"
	"github.com/tylinux/nex/go/internal/tools"
)

// The tool-failure lifecycle rules this file pins, all read off the real Node
// agent (see docs/specs/go-rewrite/tool-failures.md):
//
//  1. A failed call publishes tool_call_error, never tool_call_result
//     (call-runner.ts catch block -> emitToolCallError).
//  2. The error payload is createErrorResult's {type, message, code?}; `code` is
//     String(handlerFailure.errorCode) for a ToolHandlerFailure and the
//     upper-cased CoreError type otherwise; `message` is whitespace-compacted.
//  3. A registry miss and an initial input-schema failure both exit before
//     tool_call_started, so the lifecycle is scheduled -> failed only.
//  4. A handler failure still emits tool_call_started first.
//  5. Two read-only calls in one model step interleave as
//     scheduled(c1), started(c1), scheduled(c2), started(c2).

// ── mock model bodies ──────────────────────────────────────────────────

// sseChunk wraps one payload in a mock-model SSE frame.
func sseChunk(payload string) string { return "data: " + payload + "\n\n" }

// sseFinishChunk is the usage-bearing finish frame plus the DONE sentinel.
func sseFinishChunk() string {
	return sseChunk(`{"id":"m","object":"chat.completion.chunk","created":1,"model":"mock","choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":100,"completion_tokens":20,"total_tokens":120}}`) +
		"data: [DONE]\n\n"
}

// sseToolCallChunk renders one tool-call delta.
func sseToolCallChunk(index int, id, name, args string, withRole bool) string {
	call, _ := json.Marshal(map[string]any{
		"index": index, "id": id, "type": "function",
		"function": map[string]string{"name": name, "arguments": args},
	})
	delta := `{"tool_calls":[` + string(call) + `]}`
	if withRole {
		delta = `{"role":"assistant",` + delta[1:]
	}
	return sseChunk(`{"id":"m","object":"chat.completion.chunk","created":1,"model":"mock","choices":[{"index":0,"delta":` + delta + `,"finish_reason":null}]}`)
}

// sseToolCall renders a stream whose assistant message carries one tool call.
func sseToolCall(name, args string) string {
	return sseToolCallChunk(0, "c1", name, args, true) + sseFinishChunk()
}

// sseTwoToolCalls renders a stream carrying two read-only tool calls in the
// same model step, the shape the read-two-files oracle produces.
func sseTwoToolCalls(name, argsA, argsB string) string {
	return sseToolCallChunk(0, "c1", name, argsA, true) +
		sseToolCallChunk(1, "c2", name, argsB, false) + sseFinishChunk()
}

// ── driving one model step ─────────────────────────────────────────────

// toolFact is one tool.lifecycle telemetry fact the step produced.
type toolFact struct {
	seq          int64
	phase        string
	toolCallID   string
	toolName     string
	errorCode    string
	errorMessage string
}

// stepRun is the outcome of one model step: the tool.lifecycle facts in
// emission order plus the executions appendToolResults committed.
type stepRun struct {
	facts   []toolFact
	execs   []toolExecution
	history []modelreq.Message
}

// runOneModelStep drives one model step (stream -> tools -> results) against ws
// and returns what the step produced. seed, when given, runs once the session
// exists but before the step, so a test can prime the read state.
func runOneModelStep(t *testing.T, sse string, ws string, seed func(*session)) stepRun {
	t.Helper()
	rt, ob, _ := newTestRuntime(t, &fakeTransport{bodies: []string{sse}})
	rt.deps.FS = fsPort{fsx.NewPort()}
	// newTestRuntime leaves the model client without retry options, which makes
	// the attempt loop decline to run at all; give it the production defaults.
	rt.deps.ModelConfig.Retry = modelclient.DefaultRetryOptions()
	rt.ctx = context.Background()
	s, err := rt.newSession("sess_1", "epoch1", Workspace{Path: ws, Key: ws})
	if err != nil {
		t.Fatal(err)
	}
	s.firstText = "go"
	s.firstSub = closedChan()
	s.subscribed = true
	s.modelSelection = json.RawMessage(`{"providerId":"mock","modelId":"mock","options":{"reasoningLevel":"none"}}`)
	s.setMemoryEnabled(false)
	if seed != nil {
		seed(s)
	}

	// Side-effecting tools (Write/Edit) ask for approval; approve every request
	// the same way the recording host does, from another goroutine, because
	// requestPermission blocks until the reply arrives.
	stop := approveAll(t, rt, ob)
	defer stop()

	step := s.runModelStep(context.Background(), "turn_1", "cmd-1", nil)
	if step == nil {
		t.Fatalf("model step returned nil")
	}
	s.startDeferredTools(context.Background(), "turn_1")
	s.appendToolResults("turn_1", step)
	return stepRun{
		facts:   toolFactsFrom(t, ob.snapshot()),
		execs:   step.tools,
		history: s.extendHistory(nil, step),
	}
}

// readSeed primes the session's read state with a successful whole-file Read,
// which is what Edit's read-before-write guard requires. The scenarios do the
// same across two model steps: read, then edit.
func readSeed(path string) func(*session) {
	return func(s *session) {
		if _, err := tools.Read(map[string]any{"file_path": path}, &tools.Context{
			FS: s.rt.deps.FS.ToolFS(), ReadState: s.readState,
			WorkingDirectory: s.ws.Path, WorkspaceRoot: s.ws.Path,
			SessionID: s.id, Ctx: s.rt.ctx,
		}); err != nil {
			panic("seed Read: " + err.Error())
		}
	}
}

// fsPort adapts the real filesystem adapter to the runtime's FS port.
type fsPort struct{ inner *fsx.Port }

func (p fsPort) ToolFS() tools.FS { return p.inner }

// approveAll answers every interaction/requestPermission the runtime sends with
// an allow decision, mirroring the recording host's default choice. It returns a
// function that stops the responder.
func approveAll(t *testing.T, rt *Runtime, ob *fakeOutbox) func() {
	t.Helper()
	done := make(chan struct{})
	answered := map[string]bool{}
	var mu sync.Mutex
	go func() {
		for {
			for _, f := range ob.snapshot() {
				if f.Kind != protocol.KindRequest || f.Method != "interaction/requestPermission" {
					continue
				}
				mu.Lock()
				seen := answered[f.ID.String()]
				answered[f.ID.String()] = true
				mu.Unlock()
				if seen {
					continue
				}
				rt.HandleResponse(protocol.Message{
					Kind: protocol.KindResponse, ID: f.ID,
					Result: json.RawMessage(`{"decision":"allow","reason":"Approved for this test"}`),
				})
			}
			select {
			case <-done:
				return
			case <-time.After(time.Millisecond):
			}
		}
	}()
	var once sync.Once
	return func() { once.Do(func() { close(done) }) }
}

func closedChan() chan struct{} {
	ch := make(chan struct{})
	close(ch)
	return ch
}

// toolFactsFrom extracts the tool.lifecycle facts from the emitted frames.
func toolFactsFrom(t *testing.T, frames []protocol.Message) []toolFact {
	t.Helper()
	var out []toolFact
	for _, f := range frames {
		if f.Method != "v4/telemetry/event" || !f.HasParams {
			continue
		}
		var params struct {
			Kind         string `json:"kind"`
			EventSeq     int64  `json:"eventSeq"`
			Phase        string `json:"phase"`
			ToolCallID   string `json:"toolCallId"`
			ToolName     string `json:"toolName"`
			ErrorCode    string `json:"errorCode"`
			ErrorMessage string `json:"errorMessage"`
		}
		if err := json.Unmarshal(f.Params, &params); err != nil {
			t.Fatalf("telemetry params: %v", err)
		}
		if params.Kind != "tool.lifecycle" {
			continue
		}
		out = append(out, toolFact{
			seq: params.EventSeq, phase: params.Phase, toolCallID: params.ToolCallID,
			toolName: params.ToolName, errorCode: params.ErrorCode,
			errorMessage: params.ErrorMessage,
		})
	}
	return out
}

// phases renders the facts as "phase(callId)" so an ordering assertion reads
// like the recorded trace.
func phases(facts []toolFact) []string {
	out := make([]string, 0, len(facts))
	for _, f := range facts {
		out = append(out, f.phase+"("+f.toolCallID+")")
	}
	return out
}

func joined(parts []string) string { return strings.Join(parts, ",") }

// failurePayload reads back the error object the tool_call_error event carried,
// straight off the execution's createErrorResult projection.
func failurePayload(t *testing.T, exec toolExecution) map[string]any {
	t.Helper()
	if exec.failure == nil {
		t.Fatalf("tool %s succeeded; want a failure", exec.toolName)
	}
	var out map[string]any
	if err := json.Unmarshal(exec.failure.payload(), &out); err != nil {
		t.Fatalf("error payload: %v", err)
	}
	return out
}

// writeFixture writes a workspace file and returns its absolute path.
func writeFixture(t *testing.T, dir, name, body string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// ── 1 + 2: the failed lifecycle and its payload ────────────────────────

func TestHandlerFailureEmitsToolCallErrorNotResult(t *testing.T) {
	ws := t.TempDir()
	writeFixture(t, ws, "f.txt", "abc\n")
	sse := sseToolCall("Edit", `{"file_path":"`+ws+`/f.txt","old_string":"zzz","new_string":"y"}`)
	run := runOneModelStep(t, sse, ws, readSeed(writeFixture(t, ws, "f.txt", "abc\n")))
	facts := run.facts

	want := "scheduled(c1),started(c1),failed(c1)"
	if got := joined(phases(facts)); got != want {
		t.Fatalf("tool lifecycle = %v, want %v", got, want)
	}
	// tool_call_result must never be emitted for a failure.
	for _, f := range facts {
		if f.phase == "completed" {
			t.Errorf("a failed call published tool_call_result (%v)", joined(phases(facts)))
		}
	}

	execs := run.execs
	payload := failurePayload(t, execs[0])
	if payload["type"] != "tool_execution_failed" {
		t.Errorf("error.type = %v, want tool_execution_failed", payload["type"])
	}
	// Edit's ToolHandlerFailure carries numeric code 8 (old_string not found).
	if payload["code"] != "8" {
		t.Errorf("error.code = %v, want \"8\" (String(handlerFailure.errorCode))", payload["code"])
	}
	// The payload message is whitespace-compacted by projectExecutionErrorPayload.
	if payload["message"] != "String to replace not found in file. String: zzz" {
		t.Errorf("error.message = %v", payload["message"])
	}
	// The model sees the raw multi-line text in <tool_use_error>, not the
	// compacted message the row and telemetry carry.
	wantModel := "<tool_use_error>String to replace not found in file.\nString: zzz</tool_use_error>"
	if execs[0].modelContent != wantModel {
		t.Errorf("model-visible tool result\n got: %q\nwant: %q", execs[0].modelContent, wantModel)
	}
}

func TestAmbiguousEditCarriesCodeNine(t *testing.T) {
	ws := t.TempDir()
	writeFixture(t, ws, "f.txt", "foo foo\n")
	sse := sseToolCall("Edit", `{"file_path":"`+ws+`/f.txt","old_string":"foo","new_string":"bar"}`)
	run := runOneModelStep(t, sse, ws, readSeed(writeFixture(t, ws, "f.txt", "foo foo\n")))
	facts := run.facts

	if got := joined(phases(facts)); got != "scheduled(c1),started(c1),failed(c1)" {
		t.Fatalf("tool lifecycle = %v", got)
	}
	execs := run.execs
	payload := failurePayload(t, execs[0])
	if payload["code"] != "9" {
		t.Errorf("error.code = %v, want \"9\" (ambiguous replace)", payload["code"])
	}
	if !strings.HasPrefix(payload["message"].(string), "Found 2 matches of the string to replace") {
		t.Errorf("error.message = %v", payload["message"])
	}
	if got := facts[len(facts)-1].errorCode; got != "9" {
		t.Errorf("tool.lifecycle errorCode = %q, want \"9\"", got)
	}
}

// ── 3: failures before tool_call_started ───────────────────────────────

func TestUnknownToolFailsBeforeStarted(t *testing.T) {
	run := runOneModelStep(t, sseToolCall("NoSuchTool", `{"a":1}`), t.TempDir(), nil)
	facts := run.facts

	if got := joined(phases(facts)); got != "scheduled(c1),failed(c1)" {
		t.Fatalf("tool lifecycle = %v, want scheduled,failed (no started)", got)
	}
	execs := run.execs
	payload := failurePayload(t, execs[0])
	if payload["type"] != "tool_not_found" {
		t.Errorf("error.type = %v, want tool_not_found", payload["type"])
	}
	// createCoreError upper-cases the type; the telemetry fact reads code ?? type.
	if payload["code"] != "TOOL_NOT_FOUND" {
		t.Errorf("error.code = %v, want TOOL_NOT_FOUND", payload["code"])
	}
	if payload["message"] != "Tool not found: NoSuchTool" {
		t.Errorf("error.message = %v", payload["message"])
	}
	// A registry miss is not a ToolHandlerFailure, so no <tool_use_error> wrapper.
	if strings.Contains(payload["message"].(string), "<tool_use_error>") {
		t.Errorf("registry-miss message must not be wrapped")
	}
	if execs[0].started {
		t.Error("a registry miss must not publish tool_call_started")
	}
	failed := facts[len(facts)-1]
	if failed.errorCode != "TOOL_NOT_FOUND" || failed.errorMessage != "Tool not found: NoSuchTool" {
		t.Errorf("tool.lifecycle error = %q/%q", failed.errorCode, failed.errorMessage)
	}
}

func TestBadInputFailsBeforeStarted(t *testing.T) {
	run := runOneModelStep(t, sseToolCall("Read", `{"file_path":123}`), t.TempDir(), nil)
	facts := run.facts

	if got := joined(phases(facts)); got != "scheduled(c1),failed(c1)" {
		t.Fatalf("tool lifecycle = %v, want scheduled,failed (no started)", got)
	}
	execs := run.execs
	payload := failurePayload(t, execs[0])
	// The CoreError message is fixed; the concrete issue only reaches the model.
	if payload["message"] != "Tool input failed inputSchema validation" {
		t.Errorf("error.message = %v", payload["message"])
	}
	if payload["code"] != "TOOL_EXECUTION_FAILED" {
		t.Errorf("error.code = %v, want TOOL_EXECUTION_FAILED", payload["code"])
	}
	if got := facts[len(facts)-1].errorMessage; got != "Tool input failed inputSchema validation" {
		t.Errorf("tool.lifecycle errorMessage = %q", got)
	}
	if execs[0].started {
		t.Error("an input-schema failure must not publish tool_call_started")
	}

	// The model instead receives the projected InputValidationError text.
	wantContent := "<tool_use_error>InputValidationError: Read failed due to the following issue:\n" +
		"The parameter `file_path` type is expected as `string` but provided as `number`</tool_use_error>"
	if execs[0].modelContent != wantContent {
		t.Errorf("model-visible tool result\n got: %q\nwant: %q", execs[0].modelContent, wantContent)
	}
	if run.history[1].Content != wantContent {
		t.Errorf("history tool message = %q", run.history[1].Content)
	}
}

// ── 4: a handler failure still starts ──────────────────────────────────

func TestWriteWithoutPriorReadStartsThenFails(t *testing.T) {
	ws := t.TempDir()
	writeFixture(t, ws, "exists.txt", "old\n")
	sse := sseToolCall("Write", `{"file_path":"`+ws+`/exists.txt","content":"new\n"}`)
	run := runOneModelStep(t, sse, ws, nil)
	facts := run.facts

	if got := joined(phases(facts)); got != "scheduled(c1),started(c1),failed(c1)" {
		t.Fatalf("tool lifecycle = %v", got)
	}
	execs := run.execs
	payload := failurePayload(t, execs[0])
	// Write raises a thrown CoreError, so there is no numeric handler code.
	if payload["code"] != "TOOL_EXECUTION_FAILED" {
		t.Errorf("error.code = %v, want TOOL_EXECUTION_FAILED", payload["code"])
	}
	if payload["message"] != "File has not been read yet. Read it first before writing to it." {
		t.Errorf("error.message = %v", payload["message"])
	}
	// The started event is parked until the stream yields; the lifecycle
	// assertion above already proves it was published.
	if execs[0].failure == nil {
		t.Error("a not-read Write must record a failure")
	}
	if got := facts[len(facts)-1].errorCode; got != "TOOL_EXECUTION_FAILED" {
		t.Errorf("tool.lifecycle errorCode = %q", got)
	}
}

// ── 5: two streaming calls interleave ──────────────────────────────────

func TestTwoStreamingCallsScheduleAndStartInterleaved(t *testing.T) {
	ws := t.TempDir()
	a := writeFixture(t, ws, "a.txt", "A\n")
	b := writeFixture(t, ws, "b.txt", "B\n")
	sse := sseTwoToolCalls("Read", `{"file_path":"`+a+`"}`, `{"file_path":"`+b+`"}`)

	run := runOneModelStep(t, sse, ws, nil)
	facts := run.facts

	want := "scheduled(c1),started(c1),scheduled(c2),started(c2),completed(c1),completed(c2)"
	if got := joined(phases(facts)); got != want {
		t.Fatalf("tool lifecycle = %v\nwant %v", got, want)
	}
	// The rule is about the *start*, not merely the schedule: c2 is scheduled
	// only after c1 has started, which is what executeDuringStream does when
	// accept() drives one call through its whole lifecycle from inside the
	// stream's tool-call callback.
	if facts[1].phase != "started" || facts[2].phase != "scheduled" {
		t.Errorf("second call was not scheduled after the first one started: %v", joined(phases(facts)))
	}
	// Sequence numbers must keep increasing across the interleave.
	for i := 1; i < len(facts); i++ {
		if facts[i].seq <= facts[i-1].seq {
			t.Errorf("eventSeq not increasing at %d: %d after %d", i, facts[i].seq, facts[i-1].seq)
		}
	}
}

// ── message sanitisation ───────────────────────────────────────────────

func TestSanitizeErrorTextCollapsesAndCaps(t *testing.T) {
	if got := sanitizeErrorText("a\n\n  b\tc  "); got != "a b c" {
		t.Errorf("collapse = %q, want %q", got, "a b c")
	}
	if got := sanitizeErrorText("   "); got != "" {
		t.Errorf("blank = %q, want empty", got)
	}
	got := sanitizeErrorText(strings.Repeat("x", 600))
	if len(got) != 500 || !strings.HasSuffix(got, "...") || !strings.HasPrefix(got, strings.Repeat("x", 497)) {
		t.Errorf("cap = %d chars, tail %q", len(got), tail(got, 5))
	}
	// The cap counts UTF-16 code units (String.prototype.slice semantics), so
	// the result is 500 units followed by "...".
	astral := sanitizeErrorText(strings.Repeat("\U0001D11E", 400))
	if !strings.HasSuffix(astral, "...") {
		t.Errorf("astral cap lost its suffix: %q", tail(astral, 8))
	}
	if units := len(utf16.Encode([]rune(astral))); units != 500 {
		t.Errorf("astral cap = %d UTF-16 units, want 500", units)
	}
}

func tail(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[len(s)-n:]
}

// ── the model-visible text per failure stage ───────────────────────────

func TestFailureModelTextPerStage(t *testing.T) {
	if got := handlerFailure(&tools.Failure{ErrorCode: 8, Message: "a\nb"}).modelText(); got != "<tool_use_error>a\nb</tool_use_error>" {
		t.Errorf("handler model text = %q", got)
	}
	if got := coreToolFailure(&tools.Failure{Message: "a\nb"}).modelText(); got != "a b" {
		t.Errorf("core model text = %q (projected message)", got)
	}
	if got := toolNotFoundFailure("X").modelText(); got != "Tool not found: X" {
		t.Errorf("not-found model text = %q", got)
	}
	if got := inputValidationFailure("<tool_use_error>x</tool_use_error>").modelText(); got != "<tool_use_error>x</tool_use_error>" {
		t.Errorf("validation model text = %q", got)
	}
}

var _ = modelclient.ToolCall{}

// ── 6: where tool_call_started lands relative to the stream ─────────────

// TestStreamingToolStartedLandsAfterTheFinishEvent pins the recorded S03 order:
// stream events …, tool_call_closed ledger, tool_call_scheduled, tool_queued
// ledger, tool_started ledger, model_streaming finish, tool_call_started,
// model_network_status completed.
//
// accept() is not awaited in Node, so the accepted call's own events interleave
// with the rest of the stream; the last of them (emitToolCallStarted) is appended
// only once the parser yields at the finish event.
func TestStreamingToolStartedLandsAfterTheFinishEvent(t *testing.T) {
	ws := t.TempDir()
	path := writeFixture(t, ws, "a.txt", "A\n")
	rt, _, _ := newTestRuntime(t, &fakeTransport{bodies: []string{
		sseToolCall("Read", `{"file_path":"`+path+`"}`),
	}})
	rt.deps.FS = fsPort{fsx.NewPort()}
	rt.deps.ModelConfig.Retry = modelclient.DefaultRetryOptions()
	rt.ctx = context.Background()
	s, err := rt.newSession("sess_1", "epoch1", Workspace{Path: ws, Key: ws})
	if err != nil {
		t.Fatal(err)
	}
	s.firstText = "go"
	s.firstSub = closedChan()
	s.subscribed = true
	s.setMemoryEnabled(false)

	step := s.runModelStep(context.Background(), "turn_1", "cmd-1", nil)
	if step == nil {
		t.Fatal("model step returned nil")
	}

	// The raw session-event types, in order.
	want := []string{
		evModelRequest, evModelNetworkStatus,
		// start + tool_input_start + tool_input_delta + tool_input_end + tool_call
		evModelStreaming, evModelStreaming, evModelStreaming, evModelStreaming, evModelStreaming,
		evStreamingToolLedger, evToolCallScheduled,
		evStreamingToolLedger, evStreamingToolLedger,
		evModelStreaming, // finish
		evToolCallStarted,
		evModelNetworkStatus,
	}
	got := rawEventTypes(t, s)
	if joined(got) != joined(want) {
		t.Fatalf("event types\n got: %v\nwant: %v", got, want)
	}
	// The finish event must be the one between the last ledger and the started
	// event; assert on its kind so a future reordering is caught here.
	finish := got[len(want)-3]
	if finish != evModelStreaming {
		t.Errorf("event before tool_call_started = %q, want the stream's finish event", finish)
	}
}

// rawEventTypes lists the appended event types in raw-sequence order.
func rawEventTypes(t *testing.T, s *session) []string {
	t.Helper()
	store, ok := s.events.(*eventstore.InMemory)
	if !ok {
		t.Fatalf("session store is not in-memory")
	}
	events := store.Events(s.id)
	out := make([]string, 0, len(events))
	for _, e := range events {
		out = append(out, e.Type)
	}
	return out
}

// ── 7: no checkpoint for a failed or denied write ───────────────────────

// TestCheckpointOnlyForSuccessfulWrites pins emitFileMutationCheckpoint's guard
// (runtime/methods/tools.ts:170): it returns early unless result.success, so a
// failed Write/Edit must not emit checkpoint_created.
func TestCheckpointOnlyForSuccessfulWrites(t *testing.T) {
	s := &session{}
	ok := toolExecution{toolName: "Write", success: true}
	failed := toolExecution{toolName: "Edit", success: false, failure: &toolFailure{}}
	denied := toolExecution{toolName: "Write", success: false, denied: true}
	read := toolExecution{toolName: "Read", success: true}

	if !s.toolsWrote([]toolExecution{ok}) {
		t.Error("a successful Write must count as a write")
	}
	if s.toolsWrote([]toolExecution{failed}) {
		t.Error("a failed Edit must not emit a checkpoint (nothing changed)")
	}
	if s.toolsWrote([]toolExecution{denied}) {
		t.Error("a denied Write must not emit a checkpoint")
	}
	if s.toolsWrote([]toolExecution{read}) {
		t.Error("a Read is not a workspace write")
	}
	if s.toolsWrote([]toolExecution{failed, read}) {
		t.Error("only the successful writes count")
	}
}

// TestRecordChangedFilesCountsEachFileOnce pins recordTurnFileChange's map
// semantics (runtime/helpers/turn-file-changes.ts): keyed by path, first write
// wins, so an Edit after a Write does not double-count the file.
func TestRecordChangedFilesCountsEachFileOnce(t *testing.T) {
	s := &session{}
	s.recordChangedFiles(toolExecution{
		toolName: "Write", success: true, filePath: "/w/a.txt",
		wroteCreate: true, createdLines: 3,
	})
	summary := s.fileChangesSummary()
	if summary["additions"] != 3 || summary["files"] != 1 {
		t.Fatalf("create summary = %v", summary)
	}

	// A later Edit to the same file keeps the first write's counts.
	s.recordChangedFiles(toolExecution{
		toolName: "Edit", success: true, filePath: "/w/a.txt",
		display: json.RawMessage(`{"additions":10,"deletions":10}`),
	})
	summary = s.fileChangesSummary()
	if summary["additions"] != 3 || summary["files"] != 1 {
		t.Fatalf("Edit must not re-count the file: %v", summary)
	}

	// A different file adds its own entry.
	s.recordChangedFiles(toolExecution{
		toolName: "Edit", success: true, filePath: "/w/b.txt",
		display: json.RawMessage(`{"additions":2,"deletions":1}`),
	})
	summary = s.fileChangesSummary()
	if summary["additions"] != 5 || summary["deletions"] != 1 || summary["files"] != 2 {
		t.Fatalf("second file summary = %v", summary)
	}

	// A failed write records nothing.
	fresh := &session{}
	fresh.recordChangedFiles(toolExecution{
		toolName: "Edit", success: false, failure: &toolFailure{}, filePath: "/w/c.txt",
		display: json.RawMessage(`{"additions":9,"deletions":9}`),
	})
	if got := fresh.fileChangesSummary(); got != nil {
		t.Fatalf("a failed write must not record file changes: %v", got)
	}
}
