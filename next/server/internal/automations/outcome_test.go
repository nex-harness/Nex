package automations

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// TestRecordOutcomeTransitions proves a run's outcome follows the session's
// task-row status and never downgrades a terminal outcome back to running.
func TestRecordOutcomeTransitions(t *testing.T) {
	service := newTestService(t, Options{}, "2026-03-10T10:00:00+08:00")
	automation := service.create(t, everyMinute("*/1 * * * *"))
	service.dispatcher.respond = func(call) (string, error) { return "session-1", nil }
	service.Start(context.Background())
	service.clock.Set(timeOfMillis(*automation.NextRunAt))
	waitFor(t, "dispatched", func() bool {
		runs := service.runs(t, automation.AutomationID)
		return len(runs) == 1 && runs[0].DispatchStatus == DispatchDispatchedStatus
	})

	if !service.RecordOutcome(Outcome{SessionID: "session-1", Status: "running"}) {
		t.Fatal("running outcome not recorded")
	}
	if !service.RecordOutcome(Outcome{SessionID: "session-1", Status: "error", Error: "boom"}) {
		t.Fatal("terminal outcome not recorded")
	}
	run := service.runs(t, automation.AutomationID)[0]
	if run.Outcome != OutcomeFailed || run.Error != "boom" {
		t.Fatalf("outcome: %+v", run)
	}
	// A late "running" refresh must not resurrect a terminal run.
	service.RecordOutcome(Outcome{SessionID: "session-1", Status: "running"})
	if got := service.runs(t, automation.AutomationID)[0].Outcome; got != OutcomeFailed {
		t.Fatalf("terminal outcome downgraded to %q", got)
	}
	// The outcome is persisted.
	if readDocument(t, service.dir).Runs[0].Outcome != OutcomeFailed {
		t.Fatal("outcome not persisted")
	}
	// Unknown sessions and unknown statuses are ignored.
	if service.RecordOutcome(Outcome{SessionID: "nope", Status: "succeeded"}) {
		t.Fatal("unknown session must be ignored")
	}
	if service.RecordOutcome(Outcome{SessionID: "session-1", Status: "weird"}) {
		t.Fatal("unknown status must be ignored")
	}
	if service.RecordOutcome(Outcome{SessionID: "session-1", Status: "cancelled"}) != true {
		t.Fatal("cancelled should map to stopped")
	}
	if got := service.runs(t, automation.AutomationID)[0].Outcome; got != OutcomeStopped {
		t.Fatalf("want stopped, got %q", got)
	}
}

// TestMaxRunsAccounting proves a finite plan ends after its scheduled runs,
// and that manual runs never consume the plan.
func TestMaxRunsAccounting(t *testing.T) {
	service := newTestService(t, Options{}, "2026-03-10T10:00:00+08:00")
	automation := service.create(t, CreateParams{
		Title: "twice", Prompt: "go", CronExpr: "*/1 * * * *", WorkspacePath: "/work/app",
		Recurring: ptr(false), MaxRuns: ptr(2.0),
	})

	// A manual run must not count towards maxRuns. Drive runNow + dispatch
	// synchronously before Start so the scheduler is not racing the window.
	service.dispatcher.respond = func(call) (string, error) { return "session-manual", nil }
	claimed, run, err := service.runNow(automation.AutomationID, workspaceScope{}, service.clock.Now())
	if err != nil {
		t.Fatalf("runNow: %v", err)
	}
	service.dispatchManual(context.Background(), claimed, run)
	stored := service.stored(t, automation.AutomationID)
	if stored.RunCount != 1 || stored.ScheduledRunCount != 0 || stored.LifecycleStatus != LifecycleActive {
		t.Fatalf("manual run accounting: %+v", stored)
	}
	if stored.Running {
		t.Fatal("dispatchManual must release its own claim")
	}

	// The first scheduled window is already due; open the service and let the
	// two schedule runs happen, moving the clock to each window in turn.
	service.Start(context.Background())
	for runIndex := 1; runIndex <= 2; runIndex++ {
		waitFor(t, "due instant", func() bool {
			stored := service.stored(t, automation.AutomationID)
			return stored.NextRunAt != nil && !stored.Running &&
				stored.ScheduledRunCount < runIndex
		})
		target := *service.stored(t, automation.AutomationID).NextRunAt
		service.clock.Set(timeOfMillis(target))
		// Move the wall clock first, then arm: the timer delay is computed from
		// the injected clock at arm time, and re-arming after a clock jump is
		// exactly what a mutation does.
		service.mu.Lock()
		service.armLocked()
		service.mu.Unlock()
		waitFor(t, "scheduled run", func() bool {
			stored := service.stored(t, automation.AutomationID)
			return stored.ScheduledRunCount >= runIndex && !stored.Running
		})
	}
	stored = service.stored(t, automation.AutomationID)
	if stored.LifecycleStatus != LifecycleCompleted || stored.Enabled || stored.NextRunAt != nil {
		t.Fatalf("finite plan should complete after maxRuns: %+v", stored)
	}
	if stored.ScheduledRunCount != 2 || stored.RunCount != 3 {
		t.Fatalf("counters: %+v", stored)
	}
	if got := len(service.runs(t, automation.AutomationID)); got != 3 {
		t.Fatalf("want 3 runs (1 manual + 2 scheduled), got %d", got)
	}
}

// TestEndAtAccounting proves a plan whose next run is past endAt completes
// without dispatching, and that moving endAt revives a completed plan.
func TestEndAtAccounting(t *testing.T) {
	service := newTestService(t, Options{}, "2026-03-10T10:00:00+08:00")
	endAt := float64(time.Date(2026, 3, 10, 11, 0, 0, 0, fixedZone(t)).UnixMilli())
	automation := service.create(t, CreateParams{
		Title: "until 11", Prompt: "go", CronExpr: "*/10 * * * *", WorkspacePath: "/work/app",
		Recurring: ptr(false), EndAt: &endAt,
	})
	if automation.LifecycleStatus != LifecycleActive {
		t.Fatalf("should start active: %+v", automation)
	}
	// Move past the deadline: the tick finishes the plan without dispatching.
	service.clock.Set(timeOfMillis(int64(endAt) + 1000))
	service.Start(context.Background())
	waitFor(t, "completed at endAt", func() bool {
		return service.stored(t, automation.AutomationID).LifecycleStatus == LifecycleCompleted
	})
	if service.dispatcher.Len() != 0 {
		t.Fatalf("a run past endAt must not dispatch: %+v", service.dispatcher.Calls())
	}
	// Extending the deadline revives the plan (AutomationService.update).
	later := float64(time.Date(2026, 3, 10, 12, 0, 0, 0, fixedZone(t)).UnixMilli())
	updated, err := service.updateAutomation(automation.AutomationID, UpdateParams{
		EndAt: json.RawMessage([]byte(itoa(int64(later)))),
	}, workspaceScope{}, service.clock.Now())
	if err != nil {
		t.Fatalf("update endAt: %v", err)
	}
	if updated.LifecycleStatus != LifecycleActive || !updated.Enabled || updated.NextRunAt == nil {
		t.Fatalf("extending endAt should revive: %+v", updated)
	}
}

func itoa(value int64) string {
	encoded, _ := json.Marshal(value)
	return string(encoded)
}

// TestOnChangeFired proves the hook runs after every persisted mutation, so
// the browser notification cannot miss a change or fire for a rejected one.
func TestOnChangeFired(t *testing.T) {
	service := newTestService(t, Options{}, "2026-03-10T10:00:00+08:00")
	var count int
	// Register before Start: mutations notify only once the timer loop owns
	// the service (the wiring order in Options/Start).
	service.OnChange(func() { count++ })
	service.Start(context.Background())

	created := service.create(t, everyMinute("*/5 * * * *"))
	if count != 1 {
		t.Fatalf("create should notify once, got %d", count)
	}
	if err := service.setEnabled(created.AutomationID, false, workspaceScope{}, service.clock.Now()); err != nil {
		t.Fatal(err)
	}
	if count != 2 {
		t.Fatalf("setEnabled should notify, got %d", count)
	}
	// A rejected mutation must not notify.
	if err := service.setEnabled("missing", true, workspaceScope{}, service.clock.Now()); err == nil {
		t.Fatal("want ErrNotFound")
	}
	if count != 2 {
		t.Fatalf("rejected mutation notified: %d", count)
	}
	if _, err := service.deleteAutomation(created.AutomationID, workspaceScope{}); err != nil {
		t.Fatal(err)
	}
	if count != 3 {
		t.Fatalf("delete should notify, got %d", count)
	}
}

// TestCallDecodesPositionalArgs proves the channel unwraps the browser's
// `[arg]` array, treats {"$undef":true} as undefined, and returns null/values
// with the wire shapes the UI expects.
func TestCallDecodesPositionalArgs(t *testing.T) {
	service := newTestService(t, Options{}, "2026-03-10T10:00:00+08:00")

	raw := func(value string) json.RawMessage {
		result, err := service.call(context.Background(), "createAutomation", json.RawMessage(value))
		if err != nil {
			t.Fatalf("createAutomation(%s): %v", value, err)
		}
		return result
	}
	created := raw(`[{"title":"t","prompt":"p","cronExpr":"*/5 * * * *","workspacePath":"/w","scheduleRule":{"$undef":true},"maxRuns":{"$undef":true}}]`)
	var automation Automation
	if err := json.Unmarshal(created, &automation); err != nil {
		t.Fatalf("decoding create result: %v", err)
	}
	if automation.ScheduleRule == nil || automation.ScheduleRule.Unit != UnitMinute {
		t.Fatalf("an undefined scheduleRule must fall back to the inferred rule: %+v", automation.ScheduleRule)
	}
	if automation.MaxRuns != nil {
		t.Fatalf("an undefined maxRuns must stay unset: %+v", automation.MaxRuns)
	}

	// listAllAutomations takes no argument at all.
	if _, err := service.call(context.Background(), "listAllAutomations", json.RawMessage(`[{"$undef":true}]`)); err != nil {
		t.Fatalf("listAllAutomations with undefined arg: %v", err)
	}
	list, err := service.call(context.Background(), "listAllAutomations", nil)
	if err != nil || !strings.Contains(string(list), automation.AutomationID) {
		t.Fatalf("listAllAutomations: %s %v", list, err)
	}

	// null and undefined results are "null" on the wire.
	for _, method := range []string{"setAutomationEnabled", "restartAutomation"} {
		result, err := service.call(context.Background(), method, json.RawMessage(
			`[{"automationId":"`+automation.AutomationID+`","enabled":true,"workspacePath":"/w"}]`))
		if err != nil {
			t.Fatalf("%s: %v", method, err)
		}
		if string(result) != "null" {
			t.Fatalf("%s should answer null, got %s", method, result)
		}
	}
	if _, err := service.call(context.Background(), "unknownMethod", nil); err == nil ||
		!strings.HasPrefix(err.Error(), "Method not found") {
		t.Fatalf("unknown method error: %v", err)
	}
}

// TestCloseReleasesClaims proves Close persists released claims so the next
// process does not wait for the stale-claim timeout.
func TestCloseReleasesClaims(t *testing.T) {
	dir := t.TempDir()
	service := newTestService(t, Options{Dir: dir}, "2026-03-10T10:00:00+08:00")
	automation := service.create(t, everyMinute("*/1 * * * *"))
	service.mu.Lock()
	index := service.store.find(automation.AutomationID)
	claimed := service.clock.Now().UnixMilli()
	service.store.doc.Automations[index].Running = true
	service.store.doc.Automations[index].ClaimedAt = &claimed
	service.store.doc.Automations[index].DispatchStatus = DispatchClaimed
	if err := service.store.commit(); err != nil {
		t.Fatal(err)
	}
	service.mu.Unlock()

	service.Close()
	stored := readDocument(t, dir).Automations[0]
	if stored.Running || stored.DispatchStatus != DispatchIdle {
		t.Fatalf("Close must release claims: %+v", stored)
	}
	// Mutations after Close are refused instead of racing the closed service.
	if _, err := service.call(context.Background(), "createAutomation",
		json.RawMessage(`[{"title":"t","prompt":"p","cronExpr":"* * * * *","workspacePath":"/w"}]`)); err == nil {
		t.Fatal("create after Close must fail")
	}
}
