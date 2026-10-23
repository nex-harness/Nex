// Package automations owns scheduled-agent definitions, their schedules and
// their run history for the nex-next server.
//
// Why the server owns them (docs/next/automations-views-search.md §A):
// runners can be offline and the page lists every automation globally, so the
// schedule must outlive any single runner. The JSON file `<Dir>/automations.json`
// (schema 1, atomic tmp+rename rewrite) is the single source of truth; one
// timer is armed to the earliest nextRunAt and re-armed after every mutation
// (there is no polling loop). Runs are dispatched to the runner that serves
// the automation's workspace; see Dispatcher.
//
// The wire shapes mirror packages/shared/src/automation-types.ts one-to-one
// (camelCase). Optional fields use omitempty/pointer so a value the UI never
// receives stays "undefined" on the wire, exactly like the old host.
package automations

import (
	"encoding/json"
	"time"
)

// StoreSchema is the persisted document version. Only this version is read;
// anything else is reported as an error instead of being silently rewritten.
const StoreSchema = 1

// CreateLimit is AUTOMATION_CREATE_LIMIT: the total number of retained
// automation definitions across every lifecycle state.
const CreateLimit = 20

// CreateLimitErrorCode is AUTOMATION_CREATE_LIMIT_ERROR_CODE. The UI detects
// the limit by this substring in the error message, so it must stay verbatim.
const CreateLimitErrorCode = "AUTOMATION_CREATE_LIMIT_REACHED"

// maxRunsPerAutomation is the per-automation history cap (oldest pruned).
const maxRunsPerAutomation = 200

// Lifecycle status values (NexAutomationLifecycleStatus).
const (
	LifecycleActive    = "active"
	LifecycleCompleted = "completed"
	LifecycleFailed    = "failed"
	LifecyclePaused    = "paused"
)

// Dispatch status values (NexAutomationDispatchStatus).
const (
	DispatchIdle             = "idle"
	DispatchClaimed          = "claimed"
	DispatchDispatchedStatus = "dispatched"
	DispatchFailedToDispatch = "failed_to_dispatch"
)

// Run dispatch status values (NexAutomationRunDispatchStatus). It shares the
// three above plus "skipped".
const RunDispatchSkipped = "skipped"

// Run outcomes (NexAutomationRunOutcome).
const (
	OutcomeRunning   = "running"
	OutcomeSucceeded = "succeeded"
	OutcomeFailed    = "failed"
	OutcomeStopped   = "stopped"
)

// Triggers (NexAutomationTrigger).
const (
	TriggerSchedule = "schedule"
	TriggerManual   = "manual"
)

// ScheduleRule units (NexAutomationScheduleRule["unit"]).
const (
	UnitMinute  = "minute"
	UnitHourly  = "hourly"
	UnitDaily   = "daily"
	UnitWeekly  = "weekly"
	UnitMonthly = "monthly"
	UnitYearly  = "yearly"
)

// ScheduleRule is NexAutomationScheduleRule. It is authoritative when present;
// cronExpr is then display-only. Calendar math runs in the automation's
// timezone.
type ScheduleRule struct {
	Unit        string  `json:"unit"`
	Interval    float64 `json:"interval"`
	Hour        float64 `json:"hour"`
	Minute      float64 `json:"minute"`
	AnchorAt    float64 `json:"anchorAt"`
	Weekdays    []int   `json:"weekdays,omitempty"`
	MonthDays   []int   `json:"monthDays,omitempty"`
	Months      []int   `json:"months,omitempty"`
	MonthlyMode string  `json:"monthlyMode,omitempty"` // "date" | "weekday"
}

// ModelSelection is packages/shared ModelSelection. Options are kept opaque
// (round-tripped byte for byte) because the server never interprets them.
type ModelSelection struct {
	ProviderID string          `json:"providerId"`
	ModelID    string          `json:"modelId"`
	Options    json.RawMessage `json:"options,omitempty"`
}

// Automation is NexAutomation. It is the persisted definition plus the
// scheduler state, exactly the fields the browser reads.
type Automation struct {
	AutomationID string `json:"automationId"`
	Title        string `json:"title"`
	// CronExpr is a 5-field cron in Timezone. Display-only when ScheduleRule
	// is set.
	CronExpr string `json:"cronExpr"`
	Prompt   string `json:"prompt"`
	// ModelSelection nil means "follow the workspace preference".
	ModelSelection *ModelSelection `json:"modelSelection,omitempty"`
	// Mode nil means "workspace default permission mode".
	Mode              *string `json:"mode,omitempty"`
	WorkspaceKey      string  `json:"workspaceKey"`
	WorkspacePath     string  `json:"workspacePath"`
	WorkspaceIdentity string  `json:"workspaceIdentity,omitempty"`
	// TargetTaskID binds the automation to an existing session (sendText
	// instead of createSession).
	TargetTaskID string `json:"targetTaskId,omitempty"`
	// LocationKind stays "local"; remote is reserved.
	LocationKind string `json:"locationKind"`
	// Timezone is the IANA zone captured at create time (the server's local
	// zone). scheduleRule and cron math run in it.
	Timezone string `json:"timezone"`
	// Recurring true = unbounded; false = finite (maxRuns, default 1).
	Recurring      bool          `json:"recurring"`
	MaxRuns        *int          `json:"maxRuns,omitempty"`
	EndAt          *int64        `json:"endAt,omitempty"`
	ScheduleRule   *ScheduleRule `json:"scheduleRule,omitempty"`
	ScheduleEdited bool          `json:"scheduleEditedByUser,omitempty"`
	// RunCount counts dispatched runs of every trigger (display).
	RunCount int  `json:"runCount"`
	Enabled  bool `json:"enabled"`
	// LifecycleStatus is one of Lifecycle*.
	LifecycleStatus string `json:"lifecycleStatus"`
	NextRunAt       *int64 `json:"nextRunAt,omitempty"`
	LastRunAt       *int64 `json:"lastRunAt,omitempty"`
	// DispatchStatus is one of Dispatch*.
	DispatchStatus   string `json:"dispatchStatus"`
	DispatchAttempts int    `json:"dispatchAttempts"`
	RetryAt          *int64 `json:"retryAt,omitempty"`
	LastError        string `json:"lastError,omitempty"`
	CreatedAt        int64  `json:"createdAt"`
	UpdatedAt        int64  `json:"updatedAt"`

	// ScheduledRunCount counts schedule-triggered dispatches only; maxRuns and
	// endAt accounting use it so manual runs never end a finite plan early.
	ScheduledRunCount int `json:"scheduledRunCount"`
	// Running is the single-flight claim (a schedule or manual dispatch is in
	// flight for this automation). ClaimedAt lets a crashed claim expire.
	Running   bool   `json:"running"`
	ClaimedAt *int64 `json:"claimedAt,omitempty"`
}

// Run is NexAutomationRun.
type Run struct {
	// RunID is "<automationId>:<scheduledAt>" for scheduled runs and
	// "<automationId>:manual:<uuid>" for manual ones (parseAutomationRunId).
	RunID          string          `json:"runId"`
	AutomationID   string          `json:"automationId"`
	WorkspaceKey   string          `json:"workspaceKey"`
	ScheduledAt    *int64          `json:"scheduledAt,omitempty"`
	Trigger        string          `json:"trigger"`
	ModelSelection *ModelSelection `json:"modelSelection,omitempty"`
	// DispatchStatus is one of Dispatch* or RunDispatchSkipped.
	DispatchStatus string `json:"dispatchStatus"`
	Outcome        string `json:"outcome,omitempty"`
	SessionID      string `json:"sessionId,omitempty"`
	Error          string `json:"error,omitempty"`
	Attempts       int    `json:"attempts"`
	CreatedAt      int64  `json:"createdAt"`
	UpdatedAt      int64  `json:"updatedAt"`
}

// document is the persisted file.
type document struct {
	Schema      int          `json:"schema"`
	Automations []Automation `json:"automations"`
	Runs        []Run        `json:"runs"`
}

// runID parses the "<automationId>:<scheduledAt>" / "<automationId>:manual:<uuid>"
// contract from packages/shared/src/automation-types.ts parseAutomationRunId.
func parseRunID(runID string) (automationID string, trigger string, scheduledAt *int64, ok bool) {
	separator := indexByte(runID, ':')
	if separator <= 0 {
		return "", "", nil, false
	}
	automationID = runID[:separator]
	rest := runID[separator+1:]
	const manualPrefix = "manual:"
	if len(rest) >= len(manualPrefix) && rest[:len(manualPrefix)] == manualPrefix {
		if len(rest) == len(manualPrefix) {
			return "", "", nil, false
		}
		return automationID, TriggerManual, nil, true
	}
	value, ok := parseInt64(rest)
	if !ok || value <= 0 {
		return "", "", nil, false
	}
	return automationID, TriggerSchedule, &value, true
}

func indexByte(s string, b byte) int {
	for i := 0; i < len(s); i++ {
		if s[i] == b {
			return i
		}
	}
	return -1
}

func parseInt64(s string) (int64, bool) {
	if s == "" || len(s) > 19 {
		return 0, false
	}
	var value int64
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c < '0' || c > '9' {
			return 0, false
		}
		value = value*10 + int64(c-'0')
	}
	return value, true
}

func millis(t time.Time) int64 { return t.UnixMilli() }

func timeOfMillis(ms int64) time.Time { return time.UnixMilli(ms) }
