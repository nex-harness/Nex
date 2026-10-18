// The task index repository: the mirror of agent sessions into tasks-index.sqlite
// that the sidebar, the global session search and the grouped view read from.
//
// Port of packages/services/src/session/taskIndexRepo.ts. The rules that matter:
//
//   - meta_json is the single source of truth for the meta; the scalar columns
//     are the query/isolation projection (identity key, status, pinned,
//     archived, deleted, unread, cron/off-peak identity).
//   - The identity key is always `workspaceIdentity?.trim() || workspacePath`
//     (AGENTS.md), and the row primary key — not the workspace_identity column —
//     decides which remote the row projects onto.
//   - Writes for one (workspaceKey, taskId) are serialized; only the unread
//     dedicated write path may touch unread_at, every other write preserves the
//     current CAS marker.
//   - meta_json is written in the nexTaskMetaSchema key order (which is what
//     zod's parse produces on the Node side), so both engines emit the same
//     column text for the same meta.
package taskindex

import (
	"database/sql"
	"encoding/json"

	"strings"
)

// Provider values. NexAgentProvider is the only provider the sidebar lists;
// migrationSource is a separate column and never filters the list.
const NexAgentProvider = "glm"

// System group ids (CRON_DEFAULT_GROUP_ID / OFF_PEAK_DEFAULT_GROUP_ID).
const (
	CronDefaultGroupID    = "nex-default-group-cron"
	OffPeakDefaultGroupID = "nex-default-group-off-peak"
)

const (
	// Search text limits: the index must not grow without bound and blow up
	// startup and list queries.
	searchTextMaxChars            = 200_000
	searchSnippetPrefixRadius     = 20
	searchSnippetSuffixRadius     = 72
	searchSnippetMaxChars         = 140
	searchSnippetLimit            = 4
	groupedTaskOrderStep          = 1000
	defaultTaskGroupColor         = "gray"
	groupedWorkspaceBootstrapOnce = "__nex_internal_grouped_workspace_bootstrap_once__"
)

var workspaceBootstrapTaskGroupColors = []string{"red", "orange", "yellow", "green", "blue", "purple"}

// TaskMeta mirrors NexTaskMeta. Optional fields are pointers so "absent" and
// "present but empty" stay apart — meta_json has to round trip with the same
// key set the TypeScript writes.
type TaskMeta struct {
	TaskID            string         `json:"taskId"`
	TraceID           string         `json:"traceId"`
	Title             string         `json:"title"`
	TitleOverridden   bool           `json:"titleOverridden,omitempty"`
	WorkspacePath     string         `json:"workspacePath"`
	WorkspaceIdentity *string        `json:"workspaceIdentity,omitempty"`
	CreatedAt         int64          `json:"createdAt"`
	UpdatedAt         int64          `json:"updatedAt"`
	Mode              string         `json:"mode"`
	Model             *string        `json:"model,omitempty"`
	ThoughtLevel      *string        `json:"thoughtLevel,omitempty"`
	Provider          *string        `json:"provider,omitempty"`
	MigrationSource   *string        `json:"migrationSource,omitempty"`
	ForkedFromTaskID  *string        `json:"forkedFromTaskId,omitempty"`
	CronAutomationID  *string        `json:"cronAutomationId,omitempty"`
	OffPeakTaskID     *string        `json:"offPeakTaskId,omitempty"`
	UnreadAt          *int64         `json:"unreadAt,omitempty"`
	Status            *string        `json:"status,omitempty"`
	LastError         *TaskLastError `json:"lastError,omitempty"`
	Target            *Goal          `json:"target,omitempty"`
	// hasTarget separates "this snapshot carried no target key" (must not
	// overwrite the index) from an explicit null (DB has no goal: clear it).
	hasTarget bool
}

// TaskLastError mirrors the persisted lastError shape.
type TaskLastError struct {
	Code        string  `json:"code,omitempty"`
	Detail      string  `json:"detail,omitempty"`
	Message     string  `json:"message"`
	TraceID     string  `json:"traceId,omitempty"`
	TaskID      string  `json:"taskId,omitempty"`
	Attribution *string `json:"attribution,omitempty"`
}

// Goal mirrors NexTaskGoal, persisted inside meta_json. The field order and the
// nullable/default semantics match nexTaskGoalSchema, so both engines emit the
// same object for the same goal (zod fills summaryTitle with null and requires
// tokenBudget / tokensUsed / timeUsedSeconds to be present).
type Goal struct {
	SessionID       string  `json:"sessionID"`
	TargetID        string  `json:"targetID"`
	Objective       string  `json:"objective"`
	SummaryTitle    *string `json:"summaryTitle"`
	Status          string  `json:"status"`
	TokenBudget     *int64  `json:"tokenBudget"`
	TokensUsed      int64   `json:"tokensUsed"`
	TimeUsedSeconds int64   `json:"timeUsedSeconds"`
	Time            struct {
		Created int64 `json:"created"`
		Updated int64 `json:"updated"`
	} `json:"time"`
}

// Valid reports whether the goal satisfies nexTaskGoalSchema's non-optional
// constraints; a goal that fails is dropped from the projection instead of
// poisoning meta_json for every later read.
func (g Goal) Valid() bool {
	return g.SessionID != "" && g.TargetID != "" && g.Objective != "" && g.Status != "" &&
		g.TokensUsed >= 0 && g.TimeUsedSeconds >= 0 &&
		g.Time.Created >= 0 && g.Time.Updated >= 0
}

// HasTarget reports whether the snapshot explicitly carried a target key.
func (m TaskMeta) HasTarget() bool { return m.hasTarget }

// SetTarget records the explicit target key. An explicit JSON null means "the DB
// has no goal, clear it"; a goal object that does not satisfy nexTaskGoalSchema
// is dropped (zod would reject the whole meta, losing every other field).
func (m *TaskMeta) SetTarget(raw json.RawMessage) {
	m.hasTarget = true
	if len(raw) == 0 || string(raw) == "null" {
		m.Target = nil
		return
	}
	var goal Goal
	if err := json.Unmarshal(raw, &goal); err != nil {
		m.Target = nil
		return
	}
	if !goal.Valid() {
		m.Target = nil
		return
	}
	m.Target = &goal
}

// TargetRef is the minimum scope any write needs: the identity key plus the id.
type TargetRef struct {
	WorkspacePath     string
	WorkspaceIdentity string
	TaskID            string
}

// TaskRow is the tasks table projection, column for column.
type TaskRow struct {
	WorkspaceKey      string
	WorkspacePath     string
	WorkspaceIdentity sql.NullString
	TaskID            string
	Title             string
	TaskStatus        sql.NullString
	Provider          sql.NullString
	Mode              string
	Model             sql.NullString
	MigrationSource   sql.NullString
	ForkedFromTaskID  sql.NullString
	CronAutomationID  sql.NullString
	OffPeakTaskID     sql.NullString
	CreatedAt         int64
	UpdatedAt         int64
	UnreadAt          sql.NullInt64
	LastUnreadAt      int64
	Pinned            int64
	Archived          int64
	Deleted           int64
	TitleOverridden   int64
	SearchableText    string
	MetaJSON          string
}

const taskColumns = `workspace_key, workspace_path, workspace_identity, task_id, title, task_status,
      provider, mode, model, migration_source, forked_from_task_id, cron_automation_id,
      off_peak_task_id, created_at, updated_at, unread_at, last_unread_at, pinned, archived,
      deleted, title_overridden, searchable_text, meta_json`

// isTerminalTaskStatus ports isTerminalTaskStatus.
func isTerminalTaskStatus(status string) bool { return status == "completed" || status == "error" }

// shouldPreserveNewerTerminalStatus ports shouldPreserveNewerTerminalStatus:
// turn.completed writes a newer terminal status first; a snapshot that arrives
// afterwards still carrying `running` must not rewind the row, or switching back
// to a finished task would show it as "working" again.
func shouldPreserveNewerTerminalStatus(existing *TaskMeta, incoming *TaskMeta) bool {
	if existing == nil || !isTerminalTaskStatus(deref(existing.Status)) {
		return false
	}
	if incoming.Status != nil && *incoming.Status != "running" {
		return false
	}
	return existing.UpdatedAt > incoming.UpdatedAt
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func nullable(v *string) any {
	if v == nil {
		return nil
	}
	return *v
}

func nullableInt(v *int64) any {
	if v == nil {
		return nil
	}
	return *v
}

// identityOrNil trims an identity and returns nil when it is absent, so a
// whitespace-only identity never leaks into the row.
func identityOrNil(identity string) *string {
	trimmed := strings.TrimSpace(identity)
	if trimmed == "" {
		return nil
	}
	return &trimmed
}

// trimOrEmpty mirrors JS `?.trim()` on a possibly-absent value.
func trimOrEmpty(v sql.NullString) string {
	if !v.Valid {
		return ""
	}
	return strings.TrimSpace(v.String)
}

// strPtr ports the `?? undefined` idiom used all over the TypeScript: an absent
// optional field is nil, never a pointer to the zero value.
func strPtr(s string) *string { return StrPtr(s) }

// trimSpace is the JS String.prototype.trim used on titles and model ids.
func trimSpace(s string) string { return strings.TrimSpace(s) }

// StrPtr is the exported form of strPtr, for the syncer.
func StrPtr(s string) *string { return &s }

// IdentityOrNil is the exported form of identityOrNil, for the syncer.
func IdentityOrNil(identity string) *string { return identityOrNil(identity) }

// TrimSpace is the exported form of trimSpace, for the syncer.
func TrimSpace(s string) string { return strings.TrimSpace(s) }

// Broadcast reasons for workspace_task_list_changed. The reason is mandatory at
// every emit site: the old implicit default (task_meta_changed) is the heaviest
// refresh the UI has — it globally bumps the membership version and reloads the
// grouping — and every unlabelled emitter silently fell into it. That was the
// "sidebar blinks on every keystroke" bug.
const (
	ReasonTaskCreated       = "task_created"
	ReasonTaskStatusChanged = "task_status_changed"
	ReasonTaskTitleChanged  = "task_title_changed"
	ReasonTaskMetaChanged   = "task_meta_changed"
)

// WorkspaceKey is the AGENTS.md identity rule exported for the syncer:
// `workspaceIdentity?.trim() || workspacePath`.
func WorkspaceKey(workspacePath, workspaceIdentity string) string {
	return workspaceKey(workspacePath, workspaceIdentity)
}

// Deref is the exported form of deref, for the syncer.
func Deref(v *string) string { return deref(v) }
