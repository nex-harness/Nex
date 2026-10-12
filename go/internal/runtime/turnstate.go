package runtime

// The turn state machine: the pure part of the turn loop. No I/O, no clock, no
// goroutines. The runtime (turn.go, toolstep.go, toolsrun.go) drives a turn by
// feeding events to Transition and executing the returned effects; the runtime
// owns every side effect.
//
// Source: apps/nex-cli/packages/core/src/agent/turn-state.ts (the phase list
// and canTransitionTo) and turn-machine.ts (which phase each TurnMachineImpl
// method moves to). Both tables below were written down from those two files
// before the code, as step 1.5 of docs/specs/go-rewrite/phase-1-go-agent.md
// requires.
//
// ── Phases ────────────────────────────────────────────────────────────────
// TurnPhase.{Idle,ProcessingInput,AwaitingModelResponse,Streaming,
// SchedulingTools,ExecutingTools,AggregatingResults,AwaitingPermission,
// Completing,Error}: the same ten phases in the same order.
//
// ── Valid transitions (canTransitionTo, turn-state.ts:230) ────────────────
//
//	from                   to
//	---------------------  ---------------------------------------------------
//	Idle                   ProcessingInput
//	ProcessingInput        AwaitingModelResponse, Completing
//	AwaitingModelResponse  Streaming, Completing, Error
//	Streaming              SchedulingTools, AggregatingResults, Completing, Error
//	SchedulingTools        ExecutingTools, AwaitingPermission, Error
//	ExecutingTools         AggregatingResults, AwaitingPermission, Error
//	AggregatingResults     AwaitingModelResponse, SchedulingTools, Completing, Error
//	AwaitingPermission     ExecutingTools, Error
//	Completing             Idle
//	Error                  Idle
//
// 24 edges. Every pair not in this table is illegal and Transition returns an
// explicit error naming it; it never panics and never silently keeps going.
//
// ── Events, one per TurnMachineImpl method ────────────────────────────────
//
//	event                   method                target phase        table
//	----------------------  --------------------  ------------------  ------------------
//	TurnStart               start()               ProcessingInput     edge
//	TurnStartModelRequest   startModelRequest()   AwaitingModelResponse  edge
//	TurnStreamDelta         addStreamingContent() Streaming             edge
//	TurnScheduleTools       scheduleTools()       SchedulingTools      edge
//	TurnStartToolExecution  startToolExecution()  ExecutingTools       edge
//	TurnPermissionRequired  requestPermission()   AwaitingPermission   edge
//	TurnAggregateResults    aggregateResults()    AggregatingResults   edge
//	TurnComplete            complete()            Completing           edge
//	TurnFail                fail()                Error                all non-terminal, Idle included
//	TurnToolCompleted       completeTool()        (no change)          self-loop, ExecutingTools
//	TurnPermissionResolved  resolvePermission()   (no change)          self-loop, AwaitingPermission
//	TurnInputQueued         queuePendingInput()   (no change)          self-loop, any state
//	TurnReset               (none in TS)          Idle                 terminal only
//
// The first nine rows are the methods that call the TS transition() helper,
// so their legality is exactly canTransitionTo and nothing else. The last four
// are the methods that write TurnState fields without moving the phase: they
// are self-loops, which the phase table cannot express because it has no
// self-edges, and turnTransition handles them before consulting it.
//
// ── Deliberate differences from turn-machine.ts ───────────────────────────
//
//   - fail() writes Error without consulting the table, so it works from any
//     phase, Idle included (the request can fail before the turn starts). Here
//     TurnFail is legal from every non-terminal phase and illegal from
//     Completing/Error: a turn that already has an outcome must not get a second
//     one, and hiding that behind a silent overwrite is how a late cancel turns
//     a finished turn into an error.
//   - after resolvePermission() the TS caller invokes startToolExecution()
//     again; that second step is TurnStartToolExecution here, which the phase
//     table allows out of AwaitingPermission. The deny case is therefore the
//     same path as the allow case: resolve, re-enter execution, record the
//     denial as the tool result.
//   - queuePendingInput/drainPendingInputs mutate pendingInputs only, so one
//     event (TurnInputQueued) covers both: accepting input changes no phase.
//
// ── Terminal phases ──────────────────────────────────────────────────────
// isTerminalPhase is Completing or Error. Both are absorbing with the one
// exception the ported table already carries, TurnReset back to Idle.
// FuzzTurnTurn asserts that: from a terminal phase nothing but a self-loop and
// TurnReset is accepted.

import (
	"errors"
	"fmt"
)

// TurnState is the phase of one turn. An explicit int type, not a string, so
// the switch in turnTransition is exhaustive and a zero value is a documented
// state rather than an empty string.
type TurnState int

// The ten phases, in the order turn-state.ts declares them.
const (
	TurnIdle TurnState = iota
	TurnProcessingInput
	TurnAwaitingModelResponse
	TurnStreaming
	TurnSchedulingTools
	TurnExecutingTools
	TurnAggregatingResults
	TurnAwaitingPermission
	TurnCompleting
	TurnError
)

var turnStateNames = [...]string{
	TurnIdle:                  "idle",
	TurnProcessingInput:       "processing_input",
	TurnAwaitingModelResponse: "awaiting_model_response",
	TurnStreaming:             "streaming",
	TurnSchedulingTools:       "scheduling_tools",
	TurnExecutingTools:        "executing_tools",
	TurnAggregatingResults:    "aggregating_results",
	TurnAwaitingPermission:    "awaiting_permission",
	TurnCompleting:            "completing",
	TurnError:                 "error",
}

// String is the phase name as the TypeScript object spells it.
func (s TurnState) String() string {
	if s < 0 || int(s) >= len(turnStateNames) {
		return fmt.Sprintf("TurnState(%d)", int(s))
	}
	return turnStateNames[s]
}

// IsValid reports whether s is one of the ten declared phases, so a value built
// by hand is refused rather than silently treated as a phase.
func (s TurnState) IsValid() bool { return s >= 0 && int(s) < len(turnStateNames) }

// IsTerminal is isTerminalPhase: Completing and Error. Only TurnReset leaves.
func (s TurnState) IsTerminal() bool { return s == TurnCompleting || s == TurnError }

// IsWaiting is isWaitingPhase: the phases where the turn blocks on something
// outside the agent (the model, the host's permission answer, a tool).
func (s TurnState) IsWaiting() bool {
	return s == TurnAwaitingModelResponse || s == TurnAwaitingPermission || s == TurnExecutingTools
}

// AllTurnStates lists every phase, for tests that must cover the whole space.
func AllTurnStates() []TurnState {
	return []TurnState{
		TurnIdle, TurnProcessingInput, TurnAwaitingModelResponse, TurnStreaming,
		TurnSchedulingTools, TurnExecutingTools, TurnAggregatingResults,
		TurnAwaitingPermission, TurnCompleting, TurnError,
	}
}

// TurnEvent is an intent the runtime asks the machine to apply. Each maps to one
// target phase; the mapping is in the table at the top of this file.
type TurnEvent int

const (
	// turnEventZero is unassigned, so a TurnEvent built without a constant is an
	// explicit error rather than a silent TurnStart.
	turnEventZero TurnEvent = iota
	TurnStart
	TurnStartModelRequest
	TurnStreamDelta
	TurnScheduleTools
	TurnStartToolExecution
	TurnPermissionRequired
	TurnToolCompleted
	TurnAggregateResults
	TurnComplete
	TurnFail
	TurnPermissionResolved
	TurnInputQueued
	TurnReset
)

var turnEventNames = [...]string{
	TurnStart:              "start",
	TurnStartModelRequest:  "start_model_request",
	TurnStreamDelta:        "stream_delta",
	TurnScheduleTools:      "schedule_tools",
	TurnStartToolExecution: "start_tool_execution",
	TurnPermissionRequired: "permission_required",
	TurnToolCompleted:      "tool_completed",
	TurnAggregateResults:   "aggregate_results",
	TurnComplete:           "complete",
	TurnFail:               "fail",
	TurnPermissionResolved: "permission_resolved",
	TurnInputQueued:        "input_queued",
	TurnReset:              "reset",
}

func (e TurnEvent) String() string {
	if e < TurnStart || e > TurnReset || turnEventNames[e] == "" {
		return fmt.Sprintf("TurnEvent(%d)", int(e))
	}
	return turnEventNames[e]
}

// AllTurnEvents lists every event, for tests that must cover the whole space.
func AllTurnEvents() []TurnEvent {
	return []TurnEvent{
		TurnStart, TurnStartModelRequest, TurnStreamDelta, TurnScheduleTools,
		TurnStartToolExecution, TurnPermissionRequired, TurnToolCompleted,
		TurnAggregateResults, TurnComplete, TurnFail, TurnPermissionResolved,
		TurnInputQueued, TurnReset,
	}
}

// EffectKind is a side effect the runtime must perform after a transition. The
// machine only names them; turn.go, toolstep.go and toolsrun.go perform them.
type EffectKind int

const (
	EffectNone EffectKind = iota
	EffectAdmitInput
	EffectEmitTurnStarted
	EffectStartModelRequest
	EffectAppendStreamDelta
	EffectScheduleToolCalls
	EffectExecuteTools
	EffectRequestPermission
	EffectResolvePermission
	EffectRecordToolResult
	EffectAggregateToolResults
	EffectCompleteTurn
	EffectFailTurn
	EffectQueueInput
	EffectResetTurn
)

var effectKindNames = map[EffectKind]string{
	EffectNone:                 "none",
	EffectAdmitInput:           "admit_input",
	EffectEmitTurnStarted:      "emit_turn_started",
	EffectStartModelRequest:    "start_model_request",
	EffectAppendStreamDelta:    "append_stream_delta",
	EffectScheduleToolCalls:    "schedule_tool_calls",
	EffectExecuteTools:         "execute_tools",
	EffectRequestPermission:    "request_permission",
	EffectResolvePermission:    "resolve_permission",
	EffectRecordToolResult:     "record_tool_result",
	EffectAggregateToolResults: "aggregate_tool_results",
	EffectCompleteTurn:         "complete_turn",
	EffectFailTurn:             "fail_turn",
	EffectQueueInput:           "queue_input",
	EffectResetTurn:            "reset_turn",
}

func (k EffectKind) String() string {
	if n, ok := effectKindNames[k]; ok {
		return n
	}
	return fmt.Sprintf("EffectKind(%d)", int(k))
}

// Effect is one side effect the runtime must run. Detail carries the event's
// argument (a model id, a tool call id, an error kind); the machine never
// interprets it.
type Effect struct {
	Kind   EffectKind
	Detail string
}

// ErrIllegalTransition is returned by Transition for a (state, event) pair that
// is not in the tables above. It is always wrapped with the offending pair, so
// a log line names the caller that got the ordering wrong.
var ErrIllegalTransition = errors.New("illegal turn transition")

// Transition is the pure state machine: it returns the new phase and the effects
// the runtime must perform, or an error naming the illegal pair. It touches no
// global, starts no goroutine and is safe to call concurrently.
//
// The returned state equals the input state when the pair is illegal, so a
// caller that ignores the error still holds a usable phase rather than a
// corrupted one.
func Transition(state TurnState, event TurnEvent) (TurnState, []Effect, error) {
	if !state.IsValid() {
		return state, nil, fmt.Errorf("%w: unknown state %d", ErrIllegalTransition, int(state))
	}
	if event < TurnStart || event > TurnReset {
		return state, nil, fmt.Errorf("%w: unknown event %d in state %s", ErrIllegalTransition, int(event), state)
	}
	target, effects, ok := turnTransition(state, event)
	if !ok {
		return state, nil, fmt.Errorf("%w: %s on %s", ErrIllegalTransition, event, state)
	}
	return target, effects, nil
}

// turnEdge is the set of target phases reachable from one phase.
type turnEdge uint16

func turnEdgeBit(s TurnState) turnEdge { return 1 << uint(s) }

// turnEdges is canTransitionTo's table as a bit set per source phase. It is the
// single copy of the relation in this file: turnTransition consults it, so the
// event table and the ported phase table cannot drift apart.
var turnEdges = [...]turnEdge{
	TurnIdle:                  turnEdgeBit(TurnProcessingInput),
	TurnProcessingInput:       turnEdgeBit(TurnAwaitingModelResponse) | turnEdgeBit(TurnCompleting),
	TurnAwaitingModelResponse: turnEdgeBit(TurnStreaming) | turnEdgeBit(TurnCompleting) | turnEdgeBit(TurnError),
	TurnStreaming: turnEdgeBit(TurnSchedulingTools) | turnEdgeBit(TurnAggregatingResults) |
		turnEdgeBit(TurnCompleting) | turnEdgeBit(TurnError),
	TurnSchedulingTools: turnEdgeBit(TurnExecutingTools) | turnEdgeBit(TurnAwaitingPermission) |
		turnEdgeBit(TurnError),
	TurnExecutingTools: turnEdgeBit(TurnAggregatingResults) | turnEdgeBit(TurnAwaitingPermission) |
		turnEdgeBit(TurnError),
	TurnAggregatingResults: turnEdgeBit(TurnAwaitingModelResponse) | turnEdgeBit(TurnSchedulingTools) |
		turnEdgeBit(TurnCompleting) | turnEdgeBit(TurnError),
	TurnAwaitingPermission: turnEdgeBit(TurnExecutingTools) | turnEdgeBit(TurnError),
	TurnCompleting:         turnEdgeBit(TurnIdle),
	TurnError:              turnEdgeBit(TurnIdle),
}

// CanTransitionTo is canTransitionTo from turn-state.ts: whether the phase table
// allows from -> to. Self-transitions are false (the table has no self-edges);
// Transition handles its self-loop events separately.
func CanTransitionTo(from, to TurnState) bool {
	if !from.IsValid() || !to.IsValid() {
		return false
	}
	return turnEdges[from]&turnEdgeBit(to) != 0
}

// turnTransition resolves one legal (state, event) pair. It reports false for an
// unlisted pair and the caller turns that into ErrIllegalTransition.
//
// Three kinds of case:
//   - self-loops, checked here because the phase table has no self-edges;
//   - TurnFail, legal from every non-terminal phase because TS fail() does not
//     consult the table at all;
//   - everything else, which must target a phase the phase table allows
//     (guarded() consults turnEdges).
func turnTransition(state TurnState, event TurnEvent) (TurnState, []Effect, bool) {
	switch event {
	case TurnToolCompleted:
		// completeTool() records a result without moving the phase; only an
		// executing turn has tool results to record.
		return state, []Effect{{EffectRecordToolResult, ""}}, state == TurnExecutingTools
	case TurnPermissionResolved:
		// resolvePermission() likewise; the caller then sends
		// TurnStartToolExecution, which the phase table allows out of
		// AwaitingPermission (the deny case included).
		return state, []Effect{{EffectResolvePermission, ""}}, state == TurnAwaitingPermission
	case TurnInputQueued:
		// queuePendingInput() appends to pendingInputs from any phase.
		return state, []Effect{{EffectQueueInput, ""}}, true
	case TurnFail:
		// fail() bypasses the phase table in TS: it writes Error from whatever
		// phase the turn is in, Idle included, so the request can fail before the
		// turn even starts. A terminal phase keeps the outcome it already has
		// rather than being overwritten: that is the difference that stops a late
		// cancel from turning a finished turn into an error.
		if state.IsTerminal() {
			return state, nil, false
		}
		return TurnError, []Effect{{EffectFailTurn, ""}}, true
	case TurnReset:
		if !state.IsTerminal() {
			return state, nil, false
		}
		return TurnIdle, []Effect{{EffectResetTurn, ""}}, true
	case TurnStart:
		return guarded(state, TurnProcessingInput, []Effect{{EffectEmitTurnStarted, ""}, {EffectAdmitInput, ""}})
	case TurnStartModelRequest:
		return guarded(state, TurnAwaitingModelResponse, []Effect{{EffectStartModelRequest, ""}})
	case TurnStreamDelta:
		return guarded(state, TurnStreaming, []Effect{{EffectAppendStreamDelta, ""}})
	case TurnScheduleTools:
		return guarded(state, TurnSchedulingTools, []Effect{{EffectScheduleToolCalls, ""}})
	case TurnStartToolExecution:
		return guarded(state, TurnExecutingTools, []Effect{{EffectExecuteTools, ""}})
	case TurnPermissionRequired:
		return guarded(state, TurnAwaitingPermission, []Effect{{EffectRequestPermission, ""}})
	case TurnAggregateResults:
		return guarded(state, TurnAggregatingResults, []Effect{{EffectAggregateToolResults, ""}})
	case TurnComplete:
		return guarded(state, TurnCompleting, []Effect{{EffectCompleteTurn, ""}})
	}
	return state, nil, false
}

// guarded is the common case: the event is legal exactly when the phase table
// has an edge from state to target.
func guarded(state, target TurnState, effects []Effect) (TurnState, []Effect, bool) {
	return target, effects, CanTransitionTo(state, target)
}
