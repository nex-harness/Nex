package windowcontroller

import (
	"encoding/json"
	"strings"

	"github.com/tylinux/nex/go/internal/adapters/taskindex"
)

// types.go is the controller's vocabulary: the two topics, the source scope and
// task address, the wire facts/rows/frames, and the mutation and list shapes.
// They are data; the rules that consume them live in projection.go (rows and
// deltas), topics.go (cursors and subscription) and runtime.go (refresh, route,
// list).

// The two controller topics (CONTROLLER_WORKSPACES_TOPIC /
// CONTROLLER_TASKS_INDEX_TOPIC in controller.ts). They are separate topics with
// separate cursors, not two payloads on one stream.
const (
	TopicWorkspaces = "controller/workspaces"
	TopicTasksIndex = "controller/tasks-index"
)

// SourceKind distinguishes a local workspace from a remote (SSH/WSL/Docker)
// workspace. A remote source is additionally namespaced by its remoteSessionId.
type SourceKind string

const (
	KindLocal  SourceKind = "local"
	KindRemote SourceKind = "remote"
)

// SourceScope identifies one task-index source. A remote scope MUST carry both
// RemoteSessionID and WorkspaceIdentity (controller.ts's
// windowHostTaskAddressSchema refinement); registerSource refuses a remote scope
// without an identity rather than emitting an address the schema rejects.
type SourceScope struct {
	Kind              SourceKind
	RemoteSessionID   string
	WorkspacePath     string
	WorkspaceIdentity string
}

// LocalScope builds a local scope.
func LocalScope(workspacePath, workspaceIdentity string) SourceScope {
	return SourceScope{Kind: KindLocal, WorkspacePath: workspacePath, WorkspaceIdentity: workspaceIdentity}
}

// RemoteScope builds a remote scope.
func RemoteScope(remoteSessionID, workspacePath, workspaceIdentity string) SourceScope {
	return SourceScope{
		Kind:              KindRemote,
		RemoteSessionID:   remoteSessionID,
		WorkspacePath:     workspacePath,
		WorkspaceIdentity: workspaceIdentity,
	}
}

// WorkspaceKey is the one identity rule (AGENTS.md):
// `workspaceIdentity?.trim() || workspacePath`. It is exported because the
// sessions-index observer keys its topic on it too.
func (s SourceScope) WorkspaceKey() string {
	if trimmed := strings.TrimSpace(s.WorkspaceIdentity); trimmed != "" {
		return trimmed
	}
	return s.WorkspacePath
}

// SourceKey ports the service-level sourceKey: the kind, then the remote session
// id or the literal "local", then the workspace key. The three are NUL-joined so
// a workspace path that itself contains the separator cannot collide with
// another source's key.
func SourceKey(s SourceScope) string {
	remote := "local"
	if s.Kind == KindRemote {
		remote = s.RemoteSessionID
	}
	return string(s.Kind) + "\x00" + remote + "\x00" + s.WorkspaceKey()
}

// AddressOf ports addressFor: a remote address carries the session id, every
// address carries the path and (when set) the identity.
func AddressOf(s SourceScope, taskID string) TaskAddress {
	addr := TaskAddress{WorkspacePath: s.WorkspacePath, TaskID: taskID}
	if s.Kind == KindRemote {
		addr.RemoteSessionID = s.RemoteSessionID
	}
	if s.WorkspaceIdentity != "" {
		addr.WorkspaceIdentity = s.WorkspaceIdentity
	}
	return addr
}

// AddressTaskKey ports taskKey(address): the remote-or-local marker, the
// workspace key, then the task id. This is the per-row map key, and it is what
// keeps a local task and a remote task of the same id apart.
func AddressTaskKey(a TaskAddress) string {
	remote := "local"
	if a.RemoteSessionID != "" {
		remote = a.RemoteSessionID
	}
	return remote + "\x00" + AddressWorkspaceKey(a) + "\x00" + a.TaskID
}

// AddressWorkspaceKey is the address's workspace identity key.
func AddressWorkspaceKey(a TaskAddress) string {
	if trimmed := strings.TrimSpace(a.WorkspaceIdentity); trimmed != "" {
		return trimmed
	}
	return a.WorkspacePath
}

// TaskAddress is WindowHostTaskAddress: remoteSessionId?, workspacePath,
// workspaceIdentity?, taskId. A remote address requires an identity.
type TaskAddress struct {
	RemoteSessionID   string `json:"remoteSessionId,omitempty"`
	WorkspacePath     string `json:"workspacePath"`
	WorkspaceIdentity string `json:"workspaceIdentity,omitempty"`
	TaskID            string `json:"taskId"`
}

// Valid reports whether the address satisfies windowHostTaskAddressSchema's
// constraints: non-blank path and id, and a remote address carrying an identity.
func (a TaskAddress) Valid() bool {
	if strings.TrimSpace(a.WorkspacePath) == "" || strings.TrimSpace(a.TaskID) == "" {
		return false
	}
	if a.RemoteSessionID != "" && strings.TrimSpace(a.WorkspaceIdentity) == "" {
		return false
	}
	return true
}

// Scope rebuilds the source scope an address belongs to. It is the inverse of
// AddressOf and is how the runtime matches an incoming mutation to its source.
func (a TaskAddress) Scope() SourceScope {
	if a.RemoteSessionID != "" {
		return RemoteScope(a.RemoteSessionID, a.WorkspacePath, a.WorkspaceIdentity)
	}
	return LocalScope(a.WorkspacePath, a.WorkspaceIdentity)
}

// Membership is the per-task bucket the projection keeps: the three booleans are
// read together by MatchesMembershipKind, never recomputed from a list query.
type Membership struct {
	Pinned   bool `json:"pinned"`
	Archived bool `json:"archived"`
	Active   bool `json:"active"`
}

// MembershipKind is TaskListMembershipKind: pinned | archived | timeline | active.
type MembershipKind string

const (
	MembershipPinned   MembershipKind = "pinned"
	MembershipArchived MembershipKind = "archived"
	MembershipTimeline MembershipKind = "timeline"
	MembershipActive   MembershipKind = "active"
)

// MatchesMembershipKind ports matchesTaskListMembershipKind: the single shared
// predicate between the query filter and the projection. `timeline` is the
// unpinned, unarchived bucket; `active` additionally includes pinned rows.
func MatchesMembershipKind(m Membership, kind MembershipKind) bool {
	switch kind {
	case MembershipPinned:
		return m.Pinned && !m.Archived
	case MembershipArchived:
		return m.Archived
	case MembershipTimeline:
		return !m.Pinned && !m.Archived
	default: // MembershipActive and any unknown value
		return !m.Archived
	}
}

// TaskActivity is the per-task live activity block (phase, lastActivityAt,
// hasBackgroundWork, plus optional pendingInteractions / workflowActivity). Only
// the fields the controller overlays read are modelled; the raw JSON keeps the
// optional sub-objects byte-identical to the source summary.
type TaskActivity struct {
	Phase             string          `json:"phase"`
	LastActivityAt    int64           `json:"lastActivityAt"`
	HasBackgroundWork bool            `json:"hasBackgroundWork"`
	PendingInteract   json.RawMessage `json:"pendingInteractions,omitempty"`
	WorkflowActivity  json.RawMessage `json:"workflowActivity,omitempty"`
}

// TaskRow is WindowHostControllerTaskRow: the projection's wire row. The address
// and the meta must agree on identity (a schema refinement), which buildRows
// guarantees by construction.
type TaskRow struct {
	Address            TaskAddress        `json:"address"`
	Meta               taskindex.TaskMeta `json:"meta"`
	Membership         Membership         `json:"membership"`
	SourceAvailability string             `json:"sourceAvailability"`
	LiveStatus         string             `json:"liveStatus"`
	Activity           *TaskActivity      `json:"activity,omitempty"`
	SearchSnippets     []string           `json:"searchSnippets,omitempty"`
}

// liveStatus values. Note that running and waiting are reachable ONLY through a
// live overlay — see defaultLiveStatus.
const (
	LiveIdle      = "idle"
	LiveRunning   = "running"
	LiveWaiting   = "waiting"
	LiveCompleted = "completed"
	LiveError     = "error"
)

// sourceAvailability values.
const (
	AvailabilityOnline  = "online"
	AvailabilityOffline = "offline"
)

// connectionState values. The set is CLOSED: it mirrors
// windowHostControllerWorkspaceFactSchema's enum, and the schema's refinement that
// an offline source must be disconnected or failed is enforced where the fact is
// built (workspaceFactOf), so the states a source can actually carry are the ones
// a consumer can decode.
const (
	StateConnecting   = "connecting"
	StateOnline       = "online"
	StateClosing      = "closing"
	StateFailed       = "failed"
	StateDisconnected = "disconnected"
	StateReconnecting = "reconnecting"
)

// WorkspaceFact is WindowHostControllerWorkspaceFact. An offline fact must be
// disconnected or failed (a schema refinement), which buildWorkspaceFact
// guarantees by pairing the two fields.
type WorkspaceFact struct {
	RemoteSessionID    string `json:"remoteSessionId,omitempty"`
	WorkspacePath      string `json:"workspacePath"`
	WorkspaceIdentity  string `json:"workspaceIdentity,omitempty"`
	SourceAvailability string `json:"sourceAvailability"`
	ConnectionState    string `json:"connectionState"`
}

// SessionOverlay is WindowHostControllerSessionOverlay: the live facts the Host
// keeps in memory for one source, derived from a sessions-index summary. It is
// ABSENT-tolerant — a source with no observer has no overlays, and the row then
// falls back to defaultLiveStatus.
type SessionOverlay struct {
	TaskID             string
	LiveStatus         string
	Title              string
	HasTitle           bool
	TitleSource        string
	UpdatedAt          *int64
	PendingInteraction json.RawMessage
	Activity           *TaskActivity
}

// TaskDelta op values.
const (
	OpTaskUpserted = "task.upserted"
	OpTaskRemoved  = "task.removed"
)

// WorkspaceDelta op values.
const (
	OpWorkspaceUpserted = "workspace.upserted"
	OpWorkspaceRemoved  = "workspace.removed"
)

// TaskDelta is one tasks-index delta: an upsert carries the row, a removal
// carries only the address.
type TaskDelta struct {
	Op      string       `json:"op"`
	Task    *TaskRow     `json:"task,omitempty"`
	Address *TaskAddress `json:"address,omitempty"`
}

// WorkspaceDelta is one workspaces delta.
type WorkspaceDelta struct {
	Op                string         `json:"op"`
	Workspace         *WorkspaceFact `json:"workspace,omitempty"`
	RemoteSessionID   string         `json:"remoteSessionId,omitempty"`
	WorkspacePath     string         `json:"workspacePath,omitempty"`
	WorkspaceIdentity string         `json:"workspaceIdentity,omitempty"`
}

// MutationKind is WindowHostControllerMutation's discriminant. "delete-archived"
// and "delete-archived-batch" are the only ones that do not require the target
// row to still be present in the projection (they are conditional deletes and
// may legitimately target a row another client already removed).
type MutationKind string

const (
	MutPin               MutationKind = "pin"
	MutArchive           MutationKind = "archive"
	MutDelete            MutationKind = "delete"
	MutDeleteArchived    MutationKind = "delete-archived"
	MutDeleteArchivedBat MutationKind = "delete-archived-batch"
	MutMarkRead          MutationKind = "mark-read"
	MutMarkUnread        MutationKind = "mark-unread"
	MutOpen              MutationKind = "open"
	MutResume            MutationKind = "resume"
)

// Mutation is one controller write request routed to its source.
type Mutation struct {
	Kind             MutationKind
	Pinned           bool     // pin
	Archived         bool     // archive
	TaskIDs          []string // delete-archived-batch
	ExpectedUnreadAt *int64   // mark-read (nil omits the CAS marker)
}

// ArchivedDeletionResult is NexArchivedTaskDeletionResult.
type ArchivedDeletionResult struct {
	DeletedTaskIDs []string `json:"deletedTaskIds"`
	SkippedTaskIDs []string `json:"skippedTaskIds"`
	FailedTaskIDs  []string `json:"failedTaskIds"`
}

// ListQuery is NexTaskListQuery's shape the controller answers: which workspace
// scopes to fold, which bucket, optional search, sort and limit.
type ListQuery struct {
	Kind            MembershipKind
	WorkspaceScopes []SourceScope
	Search          string
	SortBy          string
	Limit           *int
}

// ListItem is WindowHostControllerTaskListItem: a meta plus the source-relative
// fields the sidebar needs (remoteSessionId?, sourceAvailability, liveStatus,
// activity?).
type ListItem struct {
	taskindex.TaskMeta
	RemoteSessionID    string        `json:"remoteSessionId,omitempty"`
	SourceAvailability string        `json:"sourceAvailability"`
	LiveStatus         string        `json:"liveStatus"`
	Activity           *TaskActivity `json:"activity,omitempty"`
	SearchSnippets     []string      `json:"searchSnippets,omitempty"`
}

// ListResult is WindowHostControllerTaskListResult.
type ListResult struct {
	Items   []ListItem `json:"items"`
	Total   int        `json:"total"`
	HasMore bool       `json:"hasMore"`
}
