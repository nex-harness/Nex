// This file holds the tool-scheduling half of a model step: emitting the ledger
// and scheduled events, starting tools (streamed or deferred), appending their
// results, and building the next request's history.
package runtime

import (
	"context"
	"encoding/json"

	"github.com/tylinux/nex/go/internal/modelclient"
	"github.com/tylinux/nex/go/internal/modelreq"
)

// scheduleTool emits the tool ledger and tool_call_scheduled events for one
// tool call. It runs as soon as the stream produces the tool_call, before the
// stream's finish event, matching the recorded order.
func (s *session) scheduleTool(turnID, assistantMessageID string, call modelclient.ToolCall, index int) {
	input := call.Input
	if input == nil {
		input = map[string]any{}
	}
	s.appendLedger(turnID)
	s.appendEvent(sessionEvent{
		Type: evToolCallScheduled, TurnID: turnID, HasTurnID: true,
		Payload: scheduledPayload(call, assistantMessageID, index, orderedInput(call.Arguments, input)),
	})
}

// startStreamingTool accepts one read-only tool call that the model stream
// produced while it was still open.
//
// Node's streaming coordinator accepts the call from the stream's tool-call
// callback (`turn-model-step.ts` → `streamingToolCoordinator.accept`), which
// stores the execution promise *without awaiting it*, and drives the accepted
// call through `executeDuringStream`: persistPendingToolPart → tool_call_closed
// ledger → scheduleTools → tool_call_scheduled → tool_queued ledger →
// executeTools → batch_start → tool_started ledger → executeToolCall.
//
// Because accept() does not await, the call's own events interleave with the
// rest of the stream rather than blocking it, and the recorded order reflects
// that:
//
//   - schedule + the two ledgers happen synchronously inside accept(), so two
//     calls in one step come out as scheduled(c1) … scheduled(c2) with only the
//     stream's own events in between — *not* "schedule both, then start both".
//   - tool_call_started is the last await of the accepted chain, so it is
//     appended when the parser next yields: after the *next* call's tool_call
//     streaming event but before that call's input-text flush, and for the last
//     call after the stream's finish event. read-two-files records
//     scheduled(c1)=14, started(c1)=19, scheduled(c2)=21, started(c2)=25, and
//     S03 (one call) records scheduled=12, started=16, completed=17 — the
//     finish event sits between the last ledger and tool_call_started.
//
// startStreamingTool therefore does everything up to the handler and parks the
// started event; flushPendingStarts (called as the stream is consumed and once
// the stream closes) emits it at the right point.
// drainStreamedTools runs every accepted streaming call that has not started yet:
// the tool_call_scheduled event, the tool_queued and tool_started ledgers, and
// then the handler itself.
func (s *session) drainStreamedTools(ctx context.Context) {
	calls := s.pendingStreamed
	s.pendingStreamed = nil
	for _, c := range calls {
		// scheduleTool contributes the tool_call_closed ledger that precedes
		// tool_call_scheduled; the two ledgers below are tool_queued and the
		// batch_start's tool_started.
		s.scheduleTool(c.turnID, c.assistantMessageID, c.call, c.index)
		s.appendLedger(c.turnID)
		s.appendLedger(c.turnID)
		exec := s.runPreparedCall(ctx, c.turnID, c.call, c.assistantMessageID, true)
		exec.streamed = true
		// 流式调用在 handler 之前失败（schema 校验）时，call-runner 的
		// emitToolCallError 与成功路径的 tool_call_started 落在同一个 await 点，
		// 即 finish 事件之后、request completed 之前，不能拖到 step 结束。
		if exec.failure != nil && !exec.started && !exec.pendingStart && !exec.denied {
			exec.pendingStart = true
			exec.earlyFailure = true
		}
		if exec.pendingStart {
			s.streamedPendingStarts = append(s.streamedPendingStarts, pendingStart{turnID: c.turnID, exec: exec})
		}
		s.pendingTools = append(s.pendingTools, exec)
	}
}

// streamedCall is a streaming tool call the model produced that has not started
// yet: accept() reached its tool_call_closed ledger, the rest runs at the next yield.
type streamedCall struct {
	turnID             string
	assistantMessageID string
	call               modelclient.ToolCall
	index              int
}

// flushPendingStarts emits the tool_call_started events that Node's accepted
// execution chains append after the parser yields: the first pending call as the
// next tool_call streaming event is enqueued, the last one after the stream's
// finish event.
func (s *session) flushPendingStarts(keepLast bool) {
	if len(s.streamedPendingStarts) == 0 {
		return
	}
	last := len(s.streamedPendingStarts) - 1
	if keepLast && last > 0 {
		last--
	}
	for i := 0; i <= last; i++ {
		s.emitPendingStart(s.streamedPendingStarts[i])
	}
	s.streamedPendingStarts = append([]pendingStart(nil), s.streamedPendingStarts[last+1:]...)
}

// emitPendingStart publishes one parked tool_call_started and marks the matching
// execution as started.
func (s *session) emitPendingStart(p pendingStart) {
	if p.exec.earlyFailure {
		s.appendToolCallError(p.turnID, p.exec)
		s.appendLedger(p.turnID)
		for i := range s.pendingTools {
			if s.pendingTools[i].callID == p.exec.callID {
				s.pendingTools[i].errorEmitted = true
			}
		}
		return
	}
	s.appendEvent(sessionEvent{
		Type: evToolCallStarted, TurnID: p.turnID, HasTurnID: true,
		Payload: mustJSON(map[string]any{
			"toolCallId": p.exec.callID,
			"toolName":   p.exec.toolName,
			"startedAt":  s.nowMs(),
			"readOnly":   p.exec.readOnly,
		}),
	})
	for i := range s.pendingTools {
		if s.pendingTools[i].callID == p.exec.callID {
			s.pendingTools[i].started = true
		}
	}
}

// pendingStart is a tool execution whose tool_call_started still has to be
// emitted once the model stream has yielded at the right point.
type pendingStart struct {
	turnID string
	exec   toolExecution
}

// runPreparedCall is the shared body of the streaming and deferred runners: the
// pre-handler early exits (registry miss, initial input validation) that never
// emit tool_call_started, the permission gate, the handler, and the
// tool_call_started event that precedes it.
//
// deferStart parks the tool_call_started for a later flush (the streaming path);
// the deferred path emits it inline, as executeTools awaits the whole call.
func (s *session) runPreparedCall(ctx context.Context, turnID string, call modelclient.ToolCall, assistantMessageID string, deferStart bool) toolExecution {
	prepared := preparedInputJSON(call.Name, call.Arguments, call.Input)

	// Registry miss (call-runner.ts `if (!entry)`): the tool name never resolves,
	// so the call is rejected before any lifecycle event beyond scheduled.
	if !registeredTool(call.Name) {
		return s.registryMissExecution(call, toolNotFoundFailure(call.Name))
	}
	// Initial input-schema validation (call-runner.ts
	// validateInitialModelToolInput): also before ToolCallStarted.
	if failure := initialInputValidation(call); failure != nil {
		return s.registryMissExecution(call, *failure)
	}

	perm := s.requestPermission(ctx, turnID, call.ID, call.Name, call.Input, json.RawMessage(prepared))
	if perm.asked && !perm.allowed {
		// Denied: the tool never starts; its result is the denial text.
		return toolExecution{
			callID: call.ID, toolName: call.Name, input: call.Input,
			inputText: prepared, rawInput: orderedInput(call.Arguments, call.Input),
			modelContent: perm.reason, success: false, denied: true,
		}
	}

	inputMap, _ := call.Input.(map[string]any)
	exec := s.executeTool(call.ID, call.Name, inputMap)
	exec.inputText = prepared
	exec.rawInput = orderedInput(call.Arguments, call.Input)
	if exec.success {
		s.recordChangedFiles(exec)
	}
	if deferStart {
		exec.pendingStart = true
		return exec
	}
	s.emitToolCallStarted(turnID, exec)
	return exec
}

// emitToolCallStarted publishes the ToolCallStarted event, which Node emits after
// the permission gate and immediately before the handler runs.
func (s *session) emitToolCallStarted(turnID string, exec toolExecution) {
	s.appendEvent(sessionEvent{
		Type: evToolCallStarted, TurnID: turnID, HasTurnID: true,
		Payload: mustJSON(map[string]any{
			"toolCallId": exec.callID,
			"toolName":   exec.toolName,
			"startedAt":  s.nowMs(),
			"readOnly":   exec.readOnly,
		}),
	})
	exec.started = true
}

// appendToolCallError publishes the tool_call_error event for a failed call.
func (s *session) appendToolCallError(turnID string, exec toolExecution) {
	s.appendEvent(sessionEvent{
		Type: evToolCallError, TurnID: turnID, HasTurnID: true,
		Payload: mustJSON(map[string]any{
			"toolCallId": exec.callID,
			"error":      exec.failure.payload(),
		}),
	})
}

// appendToolResults appends the result, batch-complete, checkpoint, recovery
// anchor and ledger events for a completed step's tools, and moves them out of
// pending.
//
// Node's layout (executeToolCallsForModelStep): every streamed call ran its own
// single-call executeTools batch, so it emits its result and its own
// tool_batch_complete as it finishes; the calls that were not streamed share one
// batch, whose tool_batch_complete follows their results. Only after all of that
// does the per-result loop run: for each result, in call order, the optional
// checkpoint, the recovery anchor and the tool_result_committed ledger.
func (s *session) appendToolResults(turnID string, step *modelStep) {
	step.tools = s.pendingTools
	s.pendingTools = nil
	for _, exec := range step.tools {
		if exec.streamed {
			s.appendToolOutcome(turnID, exec)
			s.appendEvent(sessionEvent{Type: evToolBatchComplete, TurnID: turnID, HasTurnID: true, Payload: mustJSON(map[string]any{})})
		}
	}
	deferred := false
	for _, exec := range step.tools {
		if !exec.streamed {
			deferred = true
			s.appendToolOutcome(turnID, exec)
		}
	}
	if deferred {
		s.appendEvent(sessionEvent{Type: evToolBatchComplete, TurnID: turnID, HasTurnID: true, Payload: mustJSON(map[string]any{})})
	}
	for _, exec := range step.tools {
		if s.toolsWrote([]toolExecution{exec}) {
			s.appendEvent(sessionEvent{Type: evCheckpointCreated, TurnID: turnID, HasTurnID: true, Payload: mustJSON(map[string]any{})})
		}
		s.appendEvent(sessionEvent{Type: evStreamRecoveryAnchor, TurnID: turnID, HasTurnID: true, Payload: mustJSON(map[string]any{})})
		// 流中就已失败的调用把这条 ledger 提前到了 tool_call_error 之后（见
		// emitPendingStart），这里不再重复；序号对照 heldout/bad-input 的 Node 录制。
		if !exec.errorEmitted {
			s.appendLedger(turnID)
		}
	}
}

// appendToolOutcome publishes one call's tool_call_error or tool_call_result.
func (s *session) appendToolOutcome(turnID string, exec toolExecution) {
	if exec.denied {
		return
	}
	if exec.failure != nil {
		// A failed call publishes tool_call_error (call-runner.ts
		// emitToolCallError), never tool_call_result: the product row ends
		// up with an error and without output.
		if !exec.errorEmitted {
			s.appendToolCallError(turnID, exec)
		}
		return
	}
	perf := exec.perf
	if len(perf) == 0 {
		perf = json.RawMessage(`{"totalMs":0}`)
	}
	resultInner := map[string]any{
		"success": true,
		"content": exec.modelContent,
		"perf":    perf,
	}
	if len(exec.display) > 0 {
		resultInner["display"] = json.RawMessage(exec.display)
	}
	result := map[string]any{
		"toolCallId": exec.callID,
		"result":     resultInner,
		"duration":   0,
	}
	s.appendEvent(sessionEvent{
		Type: evToolCallResult, TurnID: turnID, HasTurnID: true, Payload: mustJSON(result),
	})
}

// appendLedger appends a transient streaming_tool_ledger_updated event.

func (s *session) appendLedger(turnID string) {
	s.appendEvent(sessionEvent{Type: evStreamingToolLedger, TurnID: turnID, HasTurnID: true, Payload: json.RawMessage(`{}`)})
}

// extendHistory appends the assistant tool-call message and one tool result
// message per executed tool, as the next request's history.

func (s *session) extendHistory(history []modelreq.Message, step *modelStep) []modelreq.Message {
	assistant := modelreq.Message{Role: "assistant", Content: "", ToolCalls: step.toolsToCallJSON()}
	history = append(history, assistant)
	for _, exec := range step.tools {
		history = append(history, modelreq.Message{
			Role: "tool", Content: exec.modelContent, ToolCallID: exec.callID, ToolName: exec.toolName,
		})
	}
	return history
}

// applyStreamEvent maps one decoded model event to a model_streaming event.

// streamableTool reports whether a tool may be scheduled and executed while the
// model stream is still open (read-only, concurrency-safe, no approval).
func streamableTool(name string) bool {
	c, ok := capabilityFor(name)
	if !ok {
		return false
	}
	entry, ok := registryEntry(name)
	if !ok {
		return false
	}
	return c.ReadOnly && entry.Metadata.ConcurrentSafe && !c.Destructive &&
		!c.NeedsApproval && c.SideEffectScope == "none"
}

// startDeferredTools schedules and starts the tools that were not streamable
// (side-effecting or approval-gated), after the model step completes.
//
// The per-call event layout is the one executeToolCallsForModelStep produces for
// a single-call schedule (recorded S04: scheduled=15, permission_requested=18,
// permission_resolved=19, tool_call_started=20, tool_call_result=21):
// one tool_call_closed ledger, the tool_call_scheduled event, then the
// batch_start's tool_started ledger, then the executor's own ToolCallStarted /
// ToolCallError.
func (s *session) startDeferredTools(ctx context.Context, turnID string) {
	calls := s.deferredCalls
	s.deferredCalls = nil
	for i, call := range calls {
		s.scheduleTool(turnID, s.lastAssistantMessageID, call, i)
		s.appendLedger(turnID)
		s.appendLedger(turnID)
		s.pendingTools = append(s.pendingTools, s.runPreparedCall(ctx, turnID, call, s.lastAssistantMessageID, false))
	}
}
