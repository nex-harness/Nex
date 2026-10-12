package runtime

import (
	"errors"
	"testing"
)

// The exhaustive (state, event) table. The legal pairs are written out here;
// TestTransitionRejectsEveryUnlistedPair checks by enumerating the whole
// 10 x 13 space that nothing else is accepted, so this list is a description,
// not the source of truth.
var turnTransitionCases = []struct {
	name    string
	state   TurnState
	event   TurnEvent
	want    TurnState
	effects []EffectKind
}{
	// Idle: the turn starts, or the request fails before it does. fail() does
	// not consult the phase table in TS, so Idle is included there too.
	{"idle/start", TurnIdle, TurnStart, TurnProcessingInput,
		[]EffectKind{EffectEmitTurnStarted, EffectAdmitInput}},
	{"idle/fail", TurnIdle, TurnFail, TurnError,
		[]EffectKind{EffectFailTurn}},
	{"idle/input_queued", TurnIdle, TurnInputQueued, TurnIdle,
		[]EffectKind{EffectQueueInput}},

	// ProcessingInput: build the request, finish without a model call, or fail.
	{"processing/start_model_request", TurnProcessingInput, TurnStartModelRequest, TurnAwaitingModelResponse,
		[]EffectKind{EffectStartModelRequest}},
	{"processing/complete", TurnProcessingInput, TurnComplete, TurnCompleting,
		[]EffectKind{EffectCompleteTurn}},
	{"processing/fail", TurnProcessingInput, TurnFail, TurnError,
		[]EffectKind{EffectFailTurn}},
	{"processing/input_queued", TurnProcessingInput, TurnInputQueued, TurnProcessingInput,
		[]EffectKind{EffectQueueInput}},

	// AwaitingModelResponse: the stream opens, or the request fails.
	{"awaiting_model/stream_delta", TurnAwaitingModelResponse, TurnStreamDelta, TurnStreaming,
		[]EffectKind{EffectAppendStreamDelta}},
	{"awaiting_model/complete", TurnAwaitingModelResponse, TurnComplete, TurnCompleting,
		[]EffectKind{EffectCompleteTurn}},
	{"awaiting_model/fail", TurnAwaitingModelResponse, TurnFail, TurnError,
		[]EffectKind{EffectFailTurn}},
	{"awaiting_model/input_queued", TurnAwaitingModelResponse, TurnInputQueued, TurnAwaitingModelResponse,
		[]EffectKind{EffectQueueInput}},

	// Streaming: with tool calls the turn schedules them; without them it
	// aggregates straight away (both edges are in canTransitionTo); either way it
	// finishes or fails.
	{"streaming/schedule_tools", TurnStreaming, TurnScheduleTools, TurnSchedulingTools,
		[]EffectKind{EffectScheduleToolCalls}},
	{"streaming/aggregate_results", TurnStreaming, TurnAggregateResults, TurnAggregatingResults,
		[]EffectKind{EffectAggregateToolResults}},
	{"streaming/complete", TurnStreaming, TurnComplete, TurnCompleting,
		[]EffectKind{EffectCompleteTurn}},
	{"streaming/fail", TurnStreaming, TurnFail, TurnError,
		[]EffectKind{EffectFailTurn}},
	{"streaming/input_queued", TurnStreaming, TurnInputQueued, TurnStreaming,
		[]EffectKind{EffectQueueInput}},

	// SchedulingTools: run the batch, or stop on a permission request.
	{"scheduling/start_tool_execution", TurnSchedulingTools, TurnStartToolExecution, TurnExecutingTools,
		[]EffectKind{EffectExecuteTools}},
	{"scheduling/permission_required", TurnSchedulingTools, TurnPermissionRequired, TurnAwaitingPermission,
		[]EffectKind{EffectRequestPermission}},
	{"scheduling/fail", TurnSchedulingTools, TurnFail, TurnError,
		[]EffectKind{EffectFailTurn}},
	{"scheduling/input_queued", TurnSchedulingTools, TurnInputQueued, TurnSchedulingTools,
		[]EffectKind{EffectQueueInput}},

	// ExecutingTools: results arrive as a self-loop until the batch is done.
	{"executing/tool_completed", TurnExecutingTools, TurnToolCompleted, TurnExecutingTools,
		[]EffectKind{EffectRecordToolResult}},
	{"executing/aggregate_results", TurnExecutingTools, TurnAggregateResults, TurnAggregatingResults,
		[]EffectKind{EffectAggregateToolResults}},
	{"executing/permission_required", TurnExecutingTools, TurnPermissionRequired, TurnAwaitingPermission,
		[]EffectKind{EffectRequestPermission}},
	{"executing/fail", TurnExecutingTools, TurnFail, TurnError,
		[]EffectKind{EffectFailTurn}},
	{"executing/input_queued", TurnExecutingTools, TurnInputQueued, TurnExecutingTools,
		[]EffectKind{EffectQueueInput}},

	// AggregatingResults: another model round, more tools, finish, or fail.
	{"aggregating/start_model_request", TurnAggregatingResults, TurnStartModelRequest, TurnAwaitingModelResponse,
		[]EffectKind{EffectStartModelRequest}},
	{"aggregating/schedule_tools", TurnAggregatingResults, TurnScheduleTools, TurnSchedulingTools,
		[]EffectKind{EffectScheduleToolCalls}},
	{"aggregating/complete", TurnAggregatingResults, TurnComplete, TurnCompleting,
		[]EffectKind{EffectCompleteTurn}},
	{"aggregating/fail", TurnAggregatingResults, TurnFail, TurnError,
		[]EffectKind{EffectFailTurn}},
	{"aggregating/input_queued", TurnAggregatingResults, TurnInputQueued, TurnAggregatingResults,
		[]EffectKind{EffectQueueInput}},

	// AwaitingPermission: answering it does not move the phase (the caller then
	// sends TurnStartToolExecution, which the phase table allows out of
	// AwaitingPermission); unanswered it fails.
	{"awaiting_permission/permission_resolved", TurnAwaitingPermission, TurnPermissionResolved, TurnAwaitingPermission,
		[]EffectKind{EffectResolvePermission}},
	{"awaiting_permission/start_tool_execution", TurnAwaitingPermission, TurnStartToolExecution, TurnExecutingTools,
		[]EffectKind{EffectExecuteTools}},
	{"awaiting_permission/fail", TurnAwaitingPermission, TurnFail, TurnError,
		[]EffectKind{EffectFailTurn}},
	{"awaiting_permission/input_queued", TurnAwaitingPermission, TurnInputQueued, TurnAwaitingPermission,
		[]EffectKind{EffectQueueInput}},

	// Terminal phases: reset only.
	{"completing/reset", TurnCompleting, TurnReset, TurnIdle,
		[]EffectKind{EffectResetTurn}},
	{"completing/input_queued", TurnCompleting, TurnInputQueued, TurnCompleting,
		[]EffectKind{EffectQueueInput}},
	{"error/reset", TurnError, TurnReset, TurnIdle,
		[]EffectKind{EffectResetTurn}},
	{"error/input_queued", TurnError, TurnInputQueued, TurnError,
		[]EffectKind{EffectQueueInput}},
}

func TestTransitionLegalPairs(t *testing.T) {
	for _, tc := range turnTransitionCases {
		t.Run(tc.name, func(t *testing.T) {
			got, effects, err := Transition(tc.state, tc.event)
			if err != nil {
				t.Fatalf("Transition(%s, %s) = error %v, want %s", tc.state, tc.event, err, tc.want)
			}
			if got != tc.want {
				t.Fatalf("Transition(%s, %s) = %s, want %s", tc.state, tc.event, got, tc.want)
			}
			kinds := make([]EffectKind, len(effects))
			for i, e := range effects {
				kinds[i] = e.Kind
			}
			if len(kinds) != len(tc.effects) {
				t.Fatalf("effects = %v, want %v", kinds, tc.effects)
			}
			for i := range kinds {
				if kinds[i] != tc.effects[i] {
					t.Fatalf("effects = %v, want %v", kinds, tc.effects)
				}
			}
		})
	}
}

// TestTransitionRejectsEveryUnlistedPair enumerates all 10 states x 13 events and
// asserts the list above is exactly the accepted set: an unlisted pair must
// return ErrIllegalTransition, leave the state untouched and return no effects,
// never panic and never be silently accepted.
func TestTransitionRejectsEveryUnlistedPair(t *testing.T) {
	listed := map[[2]int]bool{}
	for _, tc := range turnTransitionCases {
		key := [2]int{int(tc.state), int(tc.event)}
		if listed[key] {
			t.Fatalf("pair (%s, %s) listed twice", tc.state, tc.event)
		}
		listed[key] = true
	}

	states, events := AllTurnStates(), AllTurnEvents()
	if len(states) != 10 {
		t.Fatalf("AllTurnStates has %d entries, want 10", len(states))
	}
	if len(events) != 13 {
		t.Fatalf("AllTurnEvents has %d entries, want 13", len(events))
	}
	accepted := 0
	for _, s := range states {
		for _, e := range events {
			got, effects, err := Transition(s, e)
			if listed[[2]int{int(s), int(e)}] {
				if err != nil {
					t.Errorf("Transition(%s, %s) = %v, want it accepted", s, e, err)
					continue
				}
				accepted++
				if len(effects) == 0 {
					t.Errorf("Transition(%s, %s) returned no effects", s, e)
				}
				continue
			}
			if err == nil {
				t.Errorf("Transition(%s, %s) = %s, want ErrIllegalTransition", s, e, got)
				continue
			}
			if !errors.Is(err, ErrIllegalTransition) {
				t.Errorf("Transition(%s, %s) error = %v, want ErrIllegalTransition", s, e, err)
			}
			if got != s {
				t.Errorf("Transition(%s, %s) returned %s on error, want the state unchanged (%s)", s, e, got, s)
			}
			if effects != nil {
				t.Errorf("Transition(%s, %s) returned effects %v on error, want none", s, e, effects)
			}
		}
	}
	if accepted != len(turnTransitionCases) {
		t.Errorf("accepted %d pairs, the table lists %d", accepted, len(turnTransitionCases))
	}
}

// TestTransitionMatchesPhaseTable cross-checks the event table against the ported
// canTransitionTo relation: every legal pair that moves the phase must land on a
// phase the phase table allows, and a self-loop must only ever be one of the
// three events that write state without moving the phase in turn-machine.ts.
//
// TurnFail is exempt: TS fail() writes Error without consulting canTransitionTo,
// so the ported table deliberately allows Error out of states it lists no edge
// for (Idle, ProcessingInput, Streaming). TestFailFromEveryNonTerminalPhase
// pins that exception, and nothing else may add one.
func TestTransitionMatchesPhaseTable(t *testing.T) {
	selfLoops := map[TurnEvent]bool{
		TurnToolCompleted: true, TurnPermissionResolved: true, TurnInputQueued: true,
	}
	for _, s := range AllTurnStates() {
		for _, e := range AllTurnEvents() {
			next, _, err := Transition(s, e)
			if err != nil {
				continue
			}
			switch {
			case e == TurnFail:
				if next != TurnError {
					t.Errorf("Transition(%s, fail) = %s, want error", s, next)
				}
			case next != s && !CanTransitionTo(s, next):
				t.Errorf("Transition(%s, %s) = %s, which canTransitionTo forbids", s, e, next)
			case next == s && !selfLoops[e]:
				t.Errorf("Transition(%s, %s) is a self-loop, but only completeTool/resolvePermission/queuePendingInput may be", s, e)
			}
		}
	}
}

// TestFailFromEveryNonTerminalPhase pins the one place where the Go machine is
// stricter than canTransitionTo in one direction and looser in the other: a
// failure may be reported from any non-terminal phase (the request can fail at
// any point, including before the turn starts), but never from a phase that
// already holds an outcome.
func TestFailFromEveryNonTerminalPhase(t *testing.T) {
	for _, s := range AllTurnStates() {
		next, effects, err := Transition(s, TurnFail)
		if s.IsTerminal() {
			if err == nil {
				t.Errorf("Transition(%s, fail) = %s, want ErrIllegalTransition", s, next)
			}
			continue
		}
		if err != nil {
			t.Errorf("Transition(%s, fail) = %v, want it accepted", s, err)
			continue
		}
		if next != TurnError {
			t.Errorf("Transition(%s, fail) = %s, want error", s, next)
		}
		if len(effects) != 1 || effects[0].Kind != EffectFailTurn {
			t.Errorf("Transition(%s, fail) effects = %v, want one fail_turn", s, effects)
		}
	}
}

// The recorded Node runs pin which events each phase really sees. These are the
// phases the running turn loop occupies at each observable step (S02/S03/S07/S13
// in go/testdata/traces), so a transition no run performs is a table error
// rather than a spare row. Each fragment starts in the phase its trace is in:
// S07 and S13 pick up mid-turn.
func TestRecordedRunsUseOnlyLegalEvents(t *testing.T) {
	runs := map[string]struct {
		from  TurnState
		steps []struct {
			state TurnState
			event TurnEvent
		}
	}{
		// S02: a plain text answer, no tools.
		"s02 text answer": {from: TurnIdle, steps: []struct {
			state TurnState
			event TurnEvent
		}{
			{TurnIdle, TurnStart},
			{TurnProcessingInput, TurnStartModelRequest},
			{TurnAwaitingModelResponse, TurnStreamDelta},
			{TurnStreaming, TurnComplete},
			{TurnCompleting, TurnReset},
		}},
		// S03: the model asks for a read, the tool runs, a second round answers.
		"s03 one tool round": {from: TurnIdle, steps: []struct {
			state TurnState
			event TurnEvent
		}{
			{TurnIdle, TurnStart},
			{TurnProcessingInput, TurnStartModelRequest},
			{TurnAwaitingModelResponse, TurnStreamDelta},
			{TurnStreaming, TurnScheduleTools},
			{TurnSchedulingTools, TurnStartToolExecution},
			{TurnExecutingTools, TurnToolCompleted},
			{TurnExecutingTools, TurnAggregateResults},
			{TurnAggregatingResults, TurnStartModelRequest},
			{TurnAwaitingModelResponse, TurnStreamDelta},
			{TurnStreaming, TurnComplete},
			{TurnCompleting, TurnReset},
		}},
		// S07: a side-effecting tool asks the host first; the answer (allow or
		// deny) is applied without moving the phase, then execution resumes and
		// the turn aggregates its results.
		"s07 permission then execute": {from: TurnSchedulingTools, steps: []struct {
			state TurnState
			event TurnEvent
		}{
			{TurnSchedulingTools, TurnPermissionRequired},
			{TurnAwaitingPermission, TurnPermissionResolved},
			{TurnAwaitingPermission, TurnStartToolExecution},
			{TurnExecutingTools, TurnToolCompleted},
			{TurnExecutingTools, TurnAggregateResults},
			{TurnAggregatingResults, TurnComplete},
			{TurnCompleting, TurnReset},
		}},
		// S13: the model request fails after retries; the turn ends in Error.
		"s13 retry then fail": {from: TurnProcessingInput, steps: []struct {
			state TurnState
			event TurnEvent
		}{
			{TurnProcessingInput, TurnStartModelRequest},
			{TurnAwaitingModelResponse, TurnFail},
			{TurnError, TurnReset},
		}},
	}
	for name, run := range runs {
		t.Run(name, func(t *testing.T) {
			state := run.from
			for i, step := range run.steps {
				if state != step.state {
					t.Fatalf("step %d: the run is in %s, its trace says %s", i, state, step.state)
				}
				next, effects, err := Transition(state, step.event)
				if err != nil {
					t.Fatalf("step %d: Transition(%s, %s) = %v", i, state, step.event, err)
				}
				if len(effects) == 0 {
					t.Fatalf("step %d: %s produced no effects", i, step.event)
				}
				state = next
			}
			if run.from == TurnIdle && state != TurnIdle {
				t.Fatalf("run ended in %s, want a reset back to idle", state)
			}
			if state != TurnIdle && !state.IsTerminal() {
				t.Fatalf("fragment ended in %s, which is not terminal", state)
			}
		})
	}
}

// A denied permission still lets the turn finish: the denial is a tool result,
// not a turn error, so the runtime must be able to complete after it.
func TestDeniedPermissionStillCompletes(t *testing.T) {
	state := TurnIdle
	for _, e := range []TurnEvent{
		TurnStart, TurnStartModelRequest, TurnStreamDelta, TurnScheduleTools,
		TurnPermissionRequired, TurnPermissionResolved, TurnStartToolExecution,
		TurnToolCompleted, TurnAggregateResults, TurnStartModelRequest,
		TurnStreamDelta, TurnComplete, TurnReset,
	} {
		next, _, err := Transition(state, e)
		if err != nil {
			t.Fatalf("Transition(%s, %s) = %v", state, e, err)
		}
		state = next
	}
	if state != TurnIdle {
		t.Fatalf("state = %s, want idle", state)
	}
}

// A turn that already failed must not be failed again and one that completed
// must not be completed twice: the outcome is written exactly once. Without this
// a cancel arriving after a finish would silently overwrite the result.
func TestTerminalStateRejectsSecondOutcome(t *testing.T) {
	for _, terminal := range []TurnState{TurnCompleting, TurnError} {
		for _, e := range []TurnEvent{TurnComplete, TurnFail} {
			if got, _, err := Transition(terminal, e); !errors.Is(err, ErrIllegalTransition) {
				t.Errorf("Transition(%s, %s) = %s, %v; want ErrIllegalTransition", terminal, e, got, err)
			}
		}
		if _, _, err := Transition(terminal, TurnReset); err != nil {
			t.Errorf("Transition(%s, reset) = %v, want it accepted", terminal, err)
		}
	}
}

// Out-of-range values (an unassigned TurnEvent, a state built by hand) are
// refused with the same explicit error, never a panic.
func TestOutOfRangeValuesAreRefused(t *testing.T) {
	for _, bad := range []TurnState{-1, TurnState(10), TurnState(1000)} {
		if _, _, err := Transition(bad, TurnStart); !errors.Is(err, ErrIllegalTransition) {
			t.Errorf("Transition(%d, start) = %v, want ErrIllegalTransition", int(bad), err)
		}
		if bad.IsValid() {
			t.Errorf("TurnState(%d).IsValid() = true", int(bad))
		}
	}
	for _, bad := range []TurnEvent{turnEventZero, TurnEvent(-1), TurnEvent(999)} {
		if _, _, err := Transition(TurnIdle, bad); !errors.Is(err, ErrIllegalTransition) {
			t.Errorf("Transition(idle, %d) = %v, want ErrIllegalTransition", int(bad), err)
		}
	}
}

// CanTransitionTo is canTransitionTo: false for every pair the ported table does
// not list, including self-edges, which the turn table never uses.
func TestCanTransitionToMatchesPhaseTable(t *testing.T) {
	allowed := map[[2]TurnState]bool{
		{TurnIdle, TurnProcessingInput}:                     true,
		{TurnProcessingInput, TurnAwaitingModelResponse}:    true,
		{TurnProcessingInput, TurnCompleting}:               true,
		{TurnAwaitingModelResponse, TurnStreaming}:          true,
		{TurnAwaitingModelResponse, TurnCompleting}:         true,
		{TurnAwaitingModelResponse, TurnError}:              true,
		{TurnStreaming, TurnSchedulingTools}:                true,
		{TurnStreaming, TurnAggregatingResults}:             true,
		{TurnStreaming, TurnCompleting}:                     true,
		{TurnStreaming, TurnError}:                          true,
		{TurnSchedulingTools, TurnExecutingTools}:           true,
		{TurnSchedulingTools, TurnAwaitingPermission}:       true,
		{TurnSchedulingTools, TurnError}:                    true,
		{TurnExecutingTools, TurnAggregatingResults}:        true,
		{TurnExecutingTools, TurnAwaitingPermission}:        true,
		{TurnExecutingTools, TurnError}:                     true,
		{TurnAggregatingResults, TurnAwaitingModelResponse}: true,
		{TurnAggregatingResults, TurnSchedulingTools}:       true,
		{TurnAggregatingResults, TurnCompleting}:            true,
		{TurnAggregatingResults, TurnError}:                 true,
		{TurnAwaitingPermission, TurnExecutingTools}:        true,
		{TurnAwaitingPermission, TurnError}:                 true,
		{TurnCompleting, TurnIdle}:                          true,
		{TurnError, TurnIdle}:                               true,
	}
	n := 0
	for _, from := range AllTurnStates() {
		for _, to := range AllTurnStates() {
			want := allowed[[2]TurnState{from, to}]
			if got := CanTransitionTo(from, to); got != want {
				t.Errorf("CanTransitionTo(%s, %s) = %v, want %v", from, to, got, want)
			}
			if want {
				n++
			}
		}
	}
	if n != 24 {
		t.Errorf("phase table has %d edges, want 24 (the entries of canTransitionTo)", n)
	}
}

// Every named phase, event and effect must render; the names are what logs and
// the recorded traces quote, so a rename here is a behaviour change.
func TestNamesAndPredicates(t *testing.T) {
	wantStates := map[TurnState]string{
		TurnIdle: "idle", TurnProcessingInput: "processing_input",
		TurnAwaitingModelResponse: "awaiting_model_response", TurnStreaming: "streaming",
		TurnSchedulingTools: "scheduling_tools", TurnExecutingTools: "executing_tools",
		TurnAggregatingResults: "aggregating_results", TurnAwaitingPermission: "awaiting_permission",
		TurnCompleting: "completing", TurnError: "error",
	}
	for s, w := range wantStates {
		if s.String() != w {
			t.Errorf("TurnState(%d).String() = %q, want %q", int(s), s.String(), w)
		}
		if !s.IsValid() {
			t.Errorf("%s.IsValid() = false", s)
		}
	}
	if len(wantStates) != len(AllTurnStates()) {
		t.Errorf("the name table has %d entries, AllTurnStates has %d", len(wantStates), len(AllTurnStates()))
	}
	for _, e := range AllTurnEvents() {
		if e.String() == "" || len(e.String()) > 32 {
			t.Errorf("TurnEvent(%d).String() = %q", int(e), e.String())
		}
	}
	for _, k := range []EffectKind{
		EffectAdmitInput, EffectEmitTurnStarted, EffectStartModelRequest,
		EffectAppendStreamDelta, EffectScheduleToolCalls, EffectExecuteTools,
		EffectRequestPermission, EffectResolvePermission, EffectRecordToolResult,
		EffectAggregateToolResults, EffectCompleteTurn, EffectFailTurn,
		EffectQueueInput, EffectResetTurn,
	} {
		if k.String() == "" || k == EffectNone {
			t.Errorf("EffectKind(%d).String() = %q", int(k), k.String())
		}
	}
	if EffectNone.String() != "none" {
		t.Errorf("EffectNone.String() = %q", EffectNone.String())
	}
	// isTerminalPhase / isWaitingPhase from turn-state.ts.
	for _, s := range AllTurnStates() {
		wantTerminal := s == TurnCompleting || s == TurnError
		if s.IsTerminal() != wantTerminal {
			t.Errorf("%s.IsTerminal() = %v, want %v", s, s.IsTerminal(), wantTerminal)
		}
		wantWaiting := s == TurnAwaitingModelResponse || s == TurnAwaitingPermission || s == TurnExecutingTools
		if s.IsWaiting() != wantWaiting {
			t.Errorf("%s.IsWaiting() = %v, want %v", s, s.IsWaiting(), wantWaiting)
		}
	}
}
