package automations

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestStoreRoundTrip proves a definition survives a restart byte-for-byte in
// the fields that matter, and that the file is schema 1.
func TestStoreRoundTrip(t *testing.T) {
	dir := t.TempDir()
	service := newTestService(t, Options{Dir: dir}, "2026-03-10T02:34:00Z")
	created := service.create(t, CreateParams{
		Title:             "Nightly report",
		Prompt:            "summarise the repo",
		CronExpr:          "0 9 * * *",
		WorkspacePath:     "/work/app",
		WorkspaceIdentity: "runner:alpha:/work/app",
		Mode:              ptr("build"),
		ModelSelection:    json.RawMessage(`{"providerId":"anthropic","modelId":"claude"}`),
	})
	service.Close()

	reopened, err := New(Options{Dir: dir, Now: time.Now})
	if err != nil {
		t.Fatalf("reopening: %v", err)
	}
	defer reopened.Close()
	stored := reopened.listAutomations(workspaceScope{})
	if len(stored) != 1 {
		t.Fatalf("want 1 automation after reopen, got %d", len(stored))
	}
	got := stored[0]
	if got.AutomationID != created.AutomationID || got.Title != "Nightly report" ||
		got.Prompt != "summarise the repo" || got.CronExpr != "0 9 * * *" {
		t.Fatalf("round trip lost fields: %+v", got)
	}
	if got.WorkspaceKey != "runner:alpha:/work/app" || got.WorkspaceIdentity != "runner:alpha:/work/app" {
		t.Fatalf("workspace identity lost: %+v", got)
	}
	if got.ModelSelection == nil || got.ModelSelection.ModelID != "claude" {
		t.Fatalf("model selection lost: %+v", got.ModelSelection)
	}
	if got.Mode == nil || *got.Mode != "build" {
		t.Fatalf("mode lost: %+v", got.Mode)
	}
	if got.Timezone != "Asia/Shanghai" {
		t.Fatalf("timezone captured at create should be the server zone, got %q", got.Timezone)
	}
	if got.LifecycleStatus != LifecycleActive || !got.Enabled || got.NextRunAt == nil {
		t.Fatalf("lifecycle after create: %+v", got)
	}

	document := readDocument(t, dir)
	if document.Schema != StoreSchema {
		t.Fatalf("schema: want %d, got %d", StoreSchema, document.Schema)
	}
}

// TestStoreAtomicWrite proves a write replaces the file atomically: no temp
// files remain and a concurrent reader never sees a partial document.
func TestStoreAtomicWrite(t *testing.T) {
	dir := t.TempDir()
	service := newTestService(t, Options{Dir: dir}, "2026-03-10T02:34:00Z")
	created := service.create(t, everyMinute("*/5 * * * *"))
	service.Close()

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("reading dir: %v", err)
	}
	for _, entry := range entries {
		if entry.Name() != storeFileName {
			t.Fatalf("unexpected file left behind: %s", entry.Name())
		}
	}
	info, err := os.Stat(filepath.Join(dir, storeFileName))
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("store mode: want 0600, got %v", info.Mode().Perm())
	}
	if readDocument(t, dir).Automations[0].AutomationID != created.AutomationID {
		t.Fatal("document missing the created automation")
	}
}

// TestStoreRejectsUnknownSchema proves a newer document is not silently
// rewritten (that would drop fields this build does not know).
func TestStoreRejectsUnknownSchema(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, storeFileName)
	if err := os.WriteFile(path, []byte(`{"schema":2,"automations":[]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := New(Options{Dir: dir}); err == nil {
		t.Fatal("want an error for schema 2")
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("the unknown-schema file must not be removed: %v", err)
	}
	raw, _ := os.ReadFile(path)
	if !strings.Contains(string(raw), `"schema":2`) {
		t.Fatal("the unknown-schema file was rewritten")
	}
}

// TestCreateLimit proves AUTOMATION_CREATE_LIMIT (20) and its exact message.
func TestCreateLimit(t *testing.T) {
	service := newTestService(t, Options{}, "2026-03-10T02:34:00Z")
	for index := 0; index < CreateLimit; index++ {
		service.create(t, everyMinute("*/5 * * * *"))
	}
	_, err := service.createAutomation(everyMinute("*/5 * * * *"), service.clock.Now())
	if err == nil {
		t.Fatal("want the create limit error for automation 21")
	}
	want := "[AUTOMATION_CREATE_LIMIT_REACHED] At most 20 automations may be retained. Delete an existing automation before creating another."
	if err.Error() != want {
		t.Fatalf("limit message:\n want %q\n  got %q", want, err.Error())
	}
	// Deleting one frees a slot.
	existing := service.listAutomations(workspaceScope{})
	if _, err := service.deleteAutomation(existing[0].AutomationID, workspaceScope{}); err != nil {
		t.Fatalf("delete: %v", err)
	}
	service.create(t, everyMinute("*/5 * * * *"))
}

// TestRunHistoryPruned proves the 200-runs-per-automation cap drops the
// oldest entries first.
func TestRunHistoryPruned(t *testing.T) {
	service := newTestService(t, Options{}, "2026-03-10T02:34:00Z")
	automation := service.create(t, everyMinute("*/5 * * * *"))
	service.mu.Lock()
	for index := 0; index < maxRunsPerAutomation+25; index++ {
		at := int64(index)
		service.store.upsertRun(Run{
			RunID:        fmt.Sprintf("%s:%d", automation.AutomationID, index+1),
			AutomationID: automation.AutomationID,
			ScheduledAt:  &at,
			Trigger:      TriggerSchedule,
			CreatedAt:    int64(index),
		})
	}
	service.mu.Unlock()
	if err := writeNow(service); err != nil {
		t.Fatalf("persisting: %v", err)
	}
	runs := readDocument(t, service.dir).Runs
	if len(runs) != maxRunsPerAutomation {
		t.Fatalf("want %d runs retained, got %d", maxRunsPerAutomation, len(runs))
	}
	// The oldest (createdAt 0..24) are gone, the newest survive.
	kept := map[int64]bool{}
	for _, run := range runs {
		kept[run.CreatedAt] = true
	}
	if kept[0] || kept[24] {
		t.Fatal("the oldest runs were not pruned")
	}
	if !kept[maxRunsPerAutomation+24] {
		t.Fatal("the newest run was pruned")
	}
	// listRuns returns newest first.
	listed := service.listRuns(automation.AutomationID, workspaceScope{})
	if len(listed) != maxRunsPerAutomation || listed[0].CreatedAt != int64(maxRunsPerAutomation+24) {
		t.Fatalf("listRuns order/count: %d first=%d", len(listed), listed[0].CreatedAt)
	}
}

// writeNow persists the in-memory document (test-only helper).
func writeNow(service *testService) error {
	service.mu.Lock()
	defer service.mu.Unlock()
	return service.store.persist()
}

// TestValidationMessages pins the old texts the UI maps to toasts
// (packages/services/src/session/automationService.ts).
func TestValidationMessages(t *testing.T) {
	service := newTestService(t, Options{}, "2026-03-10T02:34:00Z")
	cases := []struct {
		name   string
		params CreateParams
		want   string
	}{
		{"title", CreateParams{Prompt: "p", CronExpr: "* * * * *", WorkspacePath: "/w"}, "title is required"},
		{"workspace", CreateParams{Title: "t", Prompt: "p", CronExpr: "* * * * *"}, "workspacePath is required"},
		{"prompt", CreateParams{Title: "t", CronExpr: "* * * * *", WorkspacePath: "/w"}, "prompt is required"},
		{"cron", CreateParams{Title: "t", Prompt: "p", CronExpr: "not a cron", WorkspacePath: "/w"}, "非法的 cron 表达式：not a cron"},
		{"cron fields", CreateParams{Title: "t", Prompt: "p", CronExpr: "0 9 * *", WorkspacePath: "/w"}, "非法的 cron 表达式：0 9 * *"},
		{"interval carrier", CreateParams{Title: "t", Prompt: "p", CronExpr: "* * * * *", WorkspacePath: "/w", IntervalUnit: "minute"}, "非法的自定义重复入参：intervalUnit 与 interval 必须同时提交或同时省略"},
		{"interval range", CreateParams{Title: "t", Prompt: "p", CronExpr: "* * * * *", WorkspacePath: "/w", IntervalUnit: "minute", Interval: ptr(0.0)}, "非法的自定义重复入参：interval 必须是 1-200 的整数"},
		{"relative delay recurring", CreateParams{Title: "t", Prompt: "p", CronExpr: "* * * * *", WorkspacePath: "/w", RelativeDelayMinutes: ptr(5.0)}, "非法的相对时间定时任务：相对延迟任务必须设置 recurring=false"},
		{"relative delay range", CreateParams{Title: "t", Prompt: "p", CronExpr: "* * * * *", WorkspacePath: "/w", RelativeDelayMinutes: ptr(0.0), Recurring: ptr(false)}, "非法的相对时间定时任务：delayMinutes 必须是 1-525600 的整数"},
		{"schedule rule unit", CreateParams{Title: "t", Prompt: "p", CronExpr: "* * * * *", WorkspacePath: "/w", ScheduleRule: json.RawMessage(`{"unit":"hourly2","interval":1}`)}, "非法的定时任务调度规则：unit 不受支持"},
		{"schedule rule weekly", CreateParams{Title: "t", Prompt: "p", CronExpr: "* * * * *", WorkspacePath: "/w", ScheduleRule: json.RawMessage(`{"unit":"weekly","interval":1,"hour":9,"minute":0,"anchorAt":0}`)}, "非法的定时任务调度规则：weekly 规则必须包含有效的 weekdays"},
	}
	for _, fixture := range cases {
		t.Run(fixture.name, func(t *testing.T) {
			_, err := service.createAutomation(fixture.params, service.clock.Now())
			if err == nil || err.Error() != fixture.want {
				t.Fatalf("want %q, got %v", fixture.want, err)
			}
		})
	}
	if len(service.listAutomations(workspaceScope{})) != 0 {
		t.Fatal("rejected creates must not be persisted")
	}
}

// TestUpdateValidation proves update keeps the old maxRuns contract.
func TestUpdateValidation(t *testing.T) {
	service := newTestService(t, Options{}, "2026-03-10T02:34:00Z")
	created := service.create(t, everyMinute("*/5 * * * *"))
	_, err := service.updateAutomation(created.AutomationID, UpdateParams{
		MaxRuns:   json.RawMessage(`null`),
		Recurring: ptr(false),
		EndAt:     json.RawMessage(`{"$undef":true}`),
	}, workspaceScope{}, service.clock.Now())
	if err == nil || err.Error() != "清空 maxRuns 时必须在同一次更新中设置 recurring=true" {
		t.Fatalf("unexpected error: %v", err)
	}
	// Recurring + maxRuns is rejected.
	_, err = service.updateAutomation(created.AutomationID, UpdateParams{
		MaxRuns:   json.RawMessage(`3`),
		Recurring: ptr(true),
	}, workspaceScope{}, service.clock.Now())
	if err == nil || err.Error() != "无限循环任务不能设置有限次数 maxRuns" {
		t.Fatalf("unexpected error: %v", err)
	}
	if !errors.Is(service.setEnabled("missing", true, workspaceScope{}, time.Now()), ErrNotFound) {
		t.Fatal("unknown id must be ErrNotFound")
	}
}
