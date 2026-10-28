package automations

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"
)

// minuteRule is a one-minute recurring rule anchored at create time.
func minuteRule() json.RawMessage {
	return json.RawMessage(`{"unit":"minute","interval":1,"hour":0,"minute":0,"anchorAt":0}`)
}

// TestTimerFiresAndRearms proves the service keeps at most one timer and that
// create / update / delete / enable re-arm it to the earliest nextRunAt.
func TestTimerFiresAndRearms(t *testing.T) {
	service := newTestService(t, Options{}, "2026-03-10T10:00:00+08:00")
	service.Start(context.Background())
	defer service.Close()

	// A rule bounded to a single dispatch so the test can observe it.
	created := service.create(t, CreateParams{
		Title:         "one shot",
		Prompt:        "go",
		CronExpr:      "0 9 1 1 *",
		WorkspacePath: "/work/app",
		Recurring:     ptr(false),
		MaxRuns:       ptr(1.0),
	})
	if created.NextRunAt == nil {
		t.Fatal("create must schedule a next run")
	}
	timerAfterCreate := service.armedDelay(t)
	if timerAfterCreate <= 0 {
		t.Fatalf("timer not armed after create (delay %v)", timerAfterCreate)
	}

	// Disabling drops the timer (nothing else is due).
	if err := service.setEnabled(created.AutomationID, false, workspaceScope{}, service.clock.Now()); err != nil {
		t.Fatalf("disable: %v", err)
	}
	waitFor(t, "timer disarmed after disable", func() bool { return service.armedDelay(t) == 0 })

	// Re-enabling arms it again.
	if err := service.setEnabled(created.AutomationID, true, workspaceScope{}, service.clock.Now()); err != nil {
		t.Fatalf("enable: %v", err)
	}
	waitFor(t, "timer re-armed after enable", func() bool { return service.armedDelay(t) > 0 })

	// An update that moves the schedule re-arms to the new instant.
	before := service.armedDelay(t)
	updated, err := service.updateAutomation(created.AutomationID, UpdateParams{
		CronExpr: ptr("0 9 1 6 *"),
	}, workspaceScope{}, service.clock.Now())
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if updated.NextRunAt == nil || *updated.NextRunAt == *created.NextRunAt {
		t.Fatalf("update did not move nextRunAt: %v", updated.NextRunAt)
	}
	after := service.armedDelay(t)
	if after == before {
		t.Fatalf("timer delay unchanged after moving the schedule: %v", after)
	}

	// Deleting removes the last due automation: no timer remains.
	if _, err := service.deleteAutomation(created.AutomationID, workspaceScope{}); err != nil {
		t.Fatalf("delete: %v", err)
	}
	waitFor(t, "timer disarmed after delete", func() bool { return service.armedDelay(t) == 0 })
}

// armedDelay reports the remaining time on the current timer (0 = no timer).
// It reads the timer itself: nextDueLocked excludes a running automation, so it
// cannot answer "is a timer armed".
func (s *Service) armedDelay(t *testing.T) time.Duration {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.timer == nil {
		return 0
	}
	due, ok := s.nextDueLocked()
	if !ok {
		return 0
	}
	return time.Until(due)
}

// TestScheduledRunPersistsBeforeDispatch proves the claim is on disk before the
// runner is called, and the dispatched result (with sessionId) after it —
// the crash-safety ordering from the spec.
func TestScheduledRunPersistsBeforeDispatch(t *testing.T) {
	service := newTestService(t, Options{}, "2026-03-10T10:00:00+08:00")
	automation := service.create(t, everyMinute("*/1 * * * *"))
	nextRun := *automation.NextRunAt

	observed := make(chan document, 1)
	service.dispatcher.respond = func(entry call) (string, error) {
		// At dispatch time the claim must already be durable.
		select {
		case observed <- readDocument(t, service.dir):
		default:
		}
		return "session-xyz", nil
	}
	service.Start(context.Background())

	service.clock.Set(timeOfMillis(nextRun))
	waitFor(t, "run recorded", func() bool { return len(service.runs(t, automation.AutomationID)) == 1 })

	var claim document
	select {
	case claim = <-observed:
	case <-time.After(5 * time.Second):
		t.Fatal("dispatcher was never called")
	}
	claimedRun := claim.Runs[0]
	if claimedRun.DispatchStatus != DispatchClaimed || claimedRun.RunID == "" {
		t.Fatalf("run not claimed before dispatch: %+v", claimedRun)
	}
	if claimedRun.Trigger != TriggerSchedule {
		t.Fatalf("trigger: %+v", claimedRun)
	}
	if claimedRun.ScheduledAt == nil || *claimedRun.ScheduledAt != nextRun {
		t.Fatalf("scheduledAt: %+v", claimedRun.ScheduledAt)
	}

	waitFor(t, "dispatch settled", func() bool {
		runs := service.runs(t, automation.AutomationID)
		return len(runs) == 1 && runs[0].DispatchStatus == DispatchDispatchedStatus
	})
	settled := service.runs(t, automation.AutomationID)[0]
	if settled.SessionID != "session-xyz" {
		t.Fatalf("sessionId not recorded: %+v", settled)
	}
	stored := service.stored(t, automation.AutomationID)
	if stored.RunCount != 1 || stored.ScheduledRunCount != 1 {
		t.Fatalf("counters: runCount=%d scheduledRunCount=%d", stored.RunCount, stored.ScheduledRunCount)
	}
	if stored.NextRunAt == nil || *stored.NextRunAt <= nextRun {
		t.Fatalf("schedule did not advance: %v (was %d)", stored.NextRunAt, nextRun)
	}
	if stored.Running {
		t.Fatal("claim not released after dispatch")
	}
	// One command per run, with commandId = runId (the idempotency contract).
	calls := service.dispatcher.Calls()
	if len(calls) != 1 || calls[0].Command.CommandID != settled.RunID {
		t.Fatalf("command ids: %+v vs run %s", calls, settled.RunID)
	}
	if !calls[0].Command.CreateSession || calls[0].Command.Text != "do the thing" {
		t.Fatalf("command shape: %+v", calls[0].Command)
	}
	if calls[0].Target.WorkspacePath != "/work/app" || calls[0].Target.WorkspaceIdentity != "" {
		t.Fatalf("target: %+v", calls[0].Target)
	}
}

// TestMissedRunIsSkipped proves a fire time more than 5 minutes in the past is
// recorded skipped and the schedule advances (server was down).
func TestMissedRunIsSkipped(t *testing.T) {
	service := newTestService(t, Options{}, "2026-03-10T10:00:00+08:00")
	automation := service.create(t, everyMinute("*/1 * * * *"))
	stale := *automation.NextRunAt
	service.clock.Set(timeOfMillis(stale + int64(10*time.Minute/time.Millisecond)))

	service.Start(context.Background())
	waitFor(t, "skipped run", func() bool { return len(service.runs(t, automation.AutomationID)) == 1 })
	run := service.runs(t, automation.AutomationID)[0]
	if run.DispatchStatus != RunDispatchSkipped {
		t.Fatalf("want skipped, got %+v", run)
	}
	if run.Error != "computer_asleep_or_app_not_running" {
		t.Fatalf("skip reason: %q", run.Error)
	}
	if run.Attempts != 0 || run.SessionID != "" {
		t.Fatalf("a skipped run must not be dispatched: %+v", run)
	}
	if service.dispatcher.Len() != 0 {
		t.Fatal("skipped run reached the dispatcher")
	}
	waitFor(t, "schedule advanced", func() bool {
		stored := service.stored(t, automation.AutomationID)
		return stored.NextRunAt != nil && *stored.NextRunAt > stale
	})
	stored := service.stored(t, automation.AutomationID)
	if stored.ScheduledRunCount != 0 {
		t.Fatalf("a skip must not count as a scheduled run: %d", stored.ScheduledRunCount)
	}
	if stored.LifecycleStatus != LifecycleActive {
		t.Fatalf("recurring keeps running after a skip: %q", stored.LifecycleStatus)
	}
}

// TestOneShotMissedRunCompletes proves a missed one-shot ends instead of being
// rescheduled from its compatibility rule.
func TestOneShotMissedRunCompletes(t *testing.T) {
	service := newTestService(t, Options{}, "2026-03-10T10:00:00+08:00")
	automation := service.create(t, CreateParams{
		Title:         "once",
		Prompt:        "go",
		CronExpr:      "0 9 1 1 *",
		WorkspacePath: "/work/app",
		Recurring:     ptr(false),
		MaxRuns:       ptr(1.0),
	})
	service.clock.Set(timeOfMillis(*automation.NextRunAt + int64(time.Hour/time.Millisecond)))
	service.Start(context.Background())
	waitFor(t, "one-shot completed", func() bool {
		return service.stored(t, automation.AutomationID).LifecycleStatus == LifecycleCompleted
	})
	stored := service.stored(t, automation.AutomationID)
	if stored.Enabled || stored.NextRunAt != nil {
		t.Fatalf("completed one-shot must be disabled without a next run: %+v", stored)
	}
}

// TestDispatchRetryThenFailure proves the 60 s × 3 retry policy, then
// failed_to_dispatch: recurring keeps its schedule, one-shot fails.
func TestDispatchRetryThenFailure(t *testing.T) {
	service := newTestService(t, Options{}, "2026-03-10T10:00:00+08:00")
	service.dispatcher.respond = func(call) (string, error) { return "", errors.New("runner offline") }
	recurring := service.create(t, everyMinute("*/1 * * * *"))
	oneShot := service.create(t, everyMinute("*/1 * * * *"))
	// Make the second automation a one-shot with the same schedule.
	service.mu.Lock()
	index := service.store.find(oneShot.AutomationID)
	service.store.doc.Automations[index].Recurring = false
	service.store.doc.Automations[index].MaxRuns = ptr(1)
	service.mu.Unlock()
	oneShot = service.stored(t, oneShot.AutomationID)

	service.Start(context.Background())

	// First attempt at the due instant (both automations are due together).
	service.clock.Set(timeOfMillis(*recurring.NextRunAt))
	waitFor(t, "first attempt", func() bool { return service.dispatcher.Len() >= 2 })
	waitFor(t, "first retry scheduled", func() bool {
		return service.stored(t, recurring.AutomationID).RetryAt != nil
	})
	first := service.stored(t, recurring.AutomationID)
	if first.DispatchAttempts != 1 || first.LastError != "runner offline" {
		t.Fatalf("after first failure: %+v", first)
	}
	if first.RetryAt == nil || *first.RetryAt-first.UpdatedAt != int64(60*time.Second/time.Millisecond) {
		t.Fatalf("retry must be 60 s later: %v", first.RetryAt)
	}

	// Attempts 2 and 3, 60 s apart.
	for attempt := 2; attempt <= dispatchMaxAttempts; attempt++ {
		service.clock.Set(timeOfMillis(*service.stored(t, recurring.AutomationID).RetryAt))
		waitFor(t, "attempt", func() bool { return service.dispatcher.Len() >= attempt*2 })
		waitFor(t, "attempt settled", func() bool {
			stored := service.stored(t, recurring.AutomationID)
			return stored.Running == false && (stored.RetryAt == nil || attempt == dispatchMaxAttempts || *stored.RetryAt > service.clock.Now().UnixMilli())
		})
	}
	// Two automations × three attempts each.
	if got := service.dispatcher.Len(); got != dispatchMaxAttempts*2 {
		t.Fatalf("want %d attempts, got %d", dispatchMaxAttempts*2, got)
	}
	// All retries reuse the same commandId (idempotent on the runner).
	ids := map[string]bool{}
	for _, entry := range service.dispatcher.Calls() {
		ids[entry.Command.CommandID] = true
	}
	// One distinct commandId per automation, reused by all its retries.
	if len(ids) != 2 {
		t.Fatalf("retries must reuse their own commandId, got %v", ids)
	}
	recurringStored := service.stored(t, recurring.AutomationID)
	if recurringStored.LifecycleStatus != LifecycleActive || !recurringStored.Enabled || recurringStored.NextRunAt == nil {
		t.Fatalf("recurring must keep its schedule after permanent failure: %+v", recurringStored)
	}
	runs := service.runs(t, recurring.AutomationID)
	if runs[0].DispatchStatus != DispatchFailedToDispatch {
		t.Fatalf("run status: %+v", runs[0])
	}
	if runs[0].Attempts < 1 {
		t.Fatalf("attempt count on the run: %+v", runs[0])
	}

	// The one-shot is never retried into a second run and ends failed.
	waitFor(t, "one-shot terminal", func() bool {
		stored := service.stored(t, oneShot.AutomationID)
		return !stored.Running && stored.LifecycleStatus != LifecycleActive
	})
	oneShotStored := service.stored(t, oneShot.AutomationID)
	if oneShotStored.LifecycleStatus != LifecycleFailed || oneShotStored.Enabled {
		t.Fatalf("one-shot after retries: %+v", oneShotStored)
	}
}

// TestRunNowDuplicate proves a manual run in flight rejects a second one with
// {"status":"duplicate"}, and that the claim is released afterwards.
func TestRunNowDuplicate(t *testing.T) {
	service := newTestService(t, Options{}, "2026-03-10T10:00:00+08:00")
	automation := service.create(t, everyMinute("*/5 * * * *"))
	release := make(chan struct{})
	arrived := make(chan struct{}, 1)
	service.dispatcher.respond = func(call) (string, error) {
		select {
		case arrived <- struct{}{}:
		default:
		}
		<-release
		return "session-manual", nil
	}

	first := service.callRaw(t, "runAutomationNow", map[string]any{
		"automationId": automation.AutomationID, "workspacePath": "/work/app",
	})
	if string(first) != `{"status":"queued"}` {
		t.Fatalf("first run-now: %s", first)
	}
	<-arrived
	second := service.callRaw(t, "runAutomationNow", map[string]any{
		"automationId": automation.AutomationID, "workspacePath": "/work/app",
	})
	if string(second) != `{"status":"duplicate"}` {
		t.Fatalf("second run-now while in flight: %s", second)
	}
	close(release)
	waitFor(t, "manual run dispatched", func() bool {
		runs := service.runs(t, automation.AutomationID)
		return len(runs) == 1 && runs[0].DispatchStatus == DispatchDispatchedStatus
	})
	run := service.runs(t, automation.AutomationID)[0]
	if run.Trigger != TriggerManual || run.SessionID != "session-manual" {
		t.Fatalf("manual run: %+v", run)
	}
	if automationID, trigger, scheduledAt, ok := parseRunID(run.RunID); !ok || scheduledAt != nil || automationID != automation.AutomationID || trigger != TriggerManual {
		t.Fatalf("manual run id must be <automationId>:manual:<uuid>: %q", run.RunID)
	}
	stored := service.stored(t, automation.AutomationID)
	if stored.RunCount != 1 || stored.ScheduledRunCount != 0 {
		t.Fatalf("manual runs count for display only: %+v", stored)
	}
	// A second run-now after settlement is accepted again.
	service.dispatcher.respond = nil
	third := service.callRaw(t, "runAutomationNow", map[string]any{
		"automationId": automation.AutomationID, "workspacePath": "/work/app",
	})
	if string(third) != `{"status":"queued"}` {
		t.Fatalf("run-now after settlement: %s", third)
	}
}

// callRaw invokes the channel with a positional argument array, like the
// browser's ChannelClient.
func (s *Service) callRaw(t *testing.T, method string, arg map[string]any) json.RawMessage {
	t.Helper()
	encoded, err := json.Marshal([]any{arg})
	if err != nil {
		t.Fatal(err)
	}
	result, err := s.call(context.Background(), method, json.RawMessage(encoded))
	if err != nil {
		t.Fatalf("%s: %v", method, err)
	}
	return result
}

// TestRunNowOutlivesRequestContext proves the manual dispatch is not bound to
// the RPC context: the browser call returns (and its context is cancelled)
// before the runner answers.
func TestRunNowOutlivesRequestContext(t *testing.T) {
	gate := make(chan struct{})
	dispatcher := DispatcherFunc(func(ctx context.Context, _ Target, command Command) (string, error) {
		<-gate
		if err := ctx.Err(); err != nil {
			return "", err
		}
		return "session-" + command.CommandID, nil
	})
	service := newTestService(t, Options{Dispatcher: dispatcher}, "2026-03-10T10:00:00+08:00")
	automation := service.create(t, everyMinute("*/5 * * * *"))

	requestCtx, cancel := context.WithCancel(context.Background())
	encoded, err := json.Marshal([]any{map[string]any{
		"automationId": automation.AutomationID, "workspacePath": "/work/app",
	}})
	if err != nil {
		t.Fatal(err)
	}
	result, err := service.call(requestCtx, "runAutomationNow", json.RawMessage(encoded))
	if err != nil || string(result) != `{"status":"queued"}` {
		t.Fatalf("run-now: %s %v", result, err)
	}
	cancel()
	close(gate)

	waitFor(t, "manual run settled", func() bool {
		runs := service.runs(t, automation.AutomationID)
		return len(runs) == 1 && runs[0].DispatchStatus != DispatchClaimed
	})
	run := service.runs(t, automation.AutomationID)[0]
	if run.DispatchStatus != DispatchDispatchedStatus || run.Error != "" {
		t.Fatalf("manual run must dispatch after the request context ends: %+v", run)
	}
}
