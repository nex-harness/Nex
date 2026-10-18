package taskadapter

import (
	"errors"
	"strings"
)

// types.go — the vocabulary of the adapter: the workspace/task target, the host
// command, the owner's error codes and the lease/owner-command shapes. The rules
// that consume them live in owner.go, queue.go, lease.go and route.go.

// Target is TaskTarget: the workspace plus task a call names. Per AGENTS.md the
// identity key is always `identity?.trim() || path` — never the path alone, and
// never the remote session id.
type Target struct {
	TaskID            string
	WorkspacePath     string
	WorkspaceIdentity string
	CronAutomationID  string
	RemoteSessionID   string
}

// WorkspaceKey ports workspaceKey(): identity when present, path otherwise.
func (t Target) WorkspaceKey() string { return WorkspaceKey(t.WorkspacePath, t.WorkspaceIdentity) }

// WorkspaceKey is the one keying rule.
func WorkspaceKey(workspacePath, workspaceIdentity string) string {
	if trimmed := strings.TrimSpace(workspaceIdentity); trimmed != "" {
		return trimmed
	}
	return workspacePath
}

// TaskKey ports taskKey(): `${workspaceKey}\u0000${taskId}`. The NUL separator is
// what keeps a workspace whose path ends in a task id from colliding with another
// workspace's task.
func (t Target) TaskKey() string { return TaskKey(t.WorkspacePath, t.WorkspaceIdentity, t.TaskID) }

// TaskKey is the composite key every per-task table is keyed by.
func TaskKey(workspacePath, workspaceIdentity, taskID string) string {
	return WorkspaceKey(workspacePath, workspaceIdentity) + "\x00" + taskID
}

// Command status values. A command is accepted → running → (removed on terminal) or
// accepted → failed. `running` is what makes it visible to the phone's pending list
// AFTER the send ACK, which is why it is not cleared on ACK (D15).
const (
	CommandAccepted = "accepted"
	CommandRunning  = "running"
	CommandFailed   = "failed"
)

// Command is NexTaskRuntimeCommand: one queued input.
type Command struct {
	CommandID     string `json:"commandId"`
	TaskID        string `json:"taskId"`
	TraceID       string `json:"traceId"`
	QueryID       string `json:"queryId,omitempty"`
	WorkspacePath string `json:"workspacePath"`
	// WorkspaceIdentity is optional in the wire schema; AGENTS.md still requires it
	// to be carried through the remote leg, never inferred from the path.
	WorkspaceIdentity string `json:"workspaceIdentity,omitempty"`
	WorkspaceKey      string `json:"workspaceKey"`
	Status            string `json:"status"`
	CreatedAt         int64  `json:"createdAt"`
	UpdatedAt         int64  `json:"updatedAt"`
	ClientID          string `json:"clientId,omitempty"`
	ClientLabel       string `json:"clientLabel,omitempty"`
	Type              string `json:"type"`
	Content           string `json:"content"`
	Error             string `json:"error,omitempty"`
	AutomationID      string `json:"automationId,omitempty"`
}

// CommandTypeSendPrompt is the only command type in the wire schema.
const CommandTypeSendPrompt = "send_prompt"

// The owner-command error codes. These are the strings the UI branches on
// (ChatErrorBanner, i18n `nex.error.*`), so they are literals and not a wrapped
// sentence around a sentence.
const (
	CodeNoActiveTaskOwner     = "NO_ACTIVE_TASK_OWNER"
	CodeStaleTaskOwnerCommand = "STALE_TASK_OWNER_COMMAND"
	CodeOwnerCommandFailed    = "OWNER_COMMAND_FAILED"
	CodeSessionTargetNotFound = "NEX_SESSION_TARGET_NOT_FOUND"
)

// OwnerError is createTaskOwnerCommandError: an Error carrying a code. The TS
// assigns `code` onto an Error and the UI reads `error.code`; the Go equivalent is a
// typed error whose Code is extracted with OwnerErrorCode.
type OwnerError struct {
	Code    string
	Message string
}

func (e *OwnerError) Error() string {
	if e.Message == "" {
		return e.Code
	}
	return e.Message
}

// OwnerErrorCode reports the code an error carries, or "" when it carries none.
func OwnerErrorCode(err error) string {
	var owner *OwnerError
	if errors.As(err, &owner) {
		return owner.Code
	}
	return ""
}

// FaultNotLoaded is getTaskTarget's refusal: this host has no target for the task,
// which normally means a different window owns it. It is a refusal, not a lookup
// miss, so it carries its own code rather than reading as "no rows".
func faultTargetNotLoaded(taskID string) *OwnerError {
	return &OwnerError{Code: CodeSessionTargetNotFound, Message: "Nex session target is not loaded: " + taskID}
}
