package agent

import (
	"context"
	"encoding/json"

	"github.com/nex-harness/nex/pkg/ai"
	"github.com/nex-harness/nex/pkg/harness/tools"
	"github.com/nex-harness/nex/pkg/store"
)

// EventType enumerates fine-grained lifecycle events emitted by the Agent Loop.
type EventType string

const (
	EventAgentStart          EventType = "agent_start"
	EventAgentEnd            EventType = "agent_end"
	EventTurnStart           EventType = "turn_start"
	EventTurnEnd             EventType = "turn_end"
	EventMessageStart        EventType = "message_start"
	EventMessageUpdate       EventType = "message_update"
	EventMessageEnd          EventType = "message_end"
	EventToolExecutionStart  EventType = "tool_execution_start"
	EventToolExecutionUpdate EventType = "tool_execution_update"
	EventToolExecutionEnd    EventType = "tool_execution_end"
	EventApprovalRequested   EventType = "approval_requested"
	EventApprovalResolved    EventType = "approval_resolved"
	EventUIPromptRequested   EventType = "ui_prompt_requested"
	EventUIPromptResolved    EventType = "ui_prompt_resolved"
	EventUIStatusUpdated     EventType = "ui_status_updated"
	EventSubagentProgress    EventType = "subagent_progress"
	EventCompaction          EventType = "compaction"
)

// Event represents a structured event emitted during an agent run.
type Event struct {
	Type          EventType            `json:"type"`
	TurnIdx       int                  `json:"turn_idx,omitempty"`
	DeltaKind     string               `json:"delta_kind,omitempty"` // "text" | "thinking" | "toolcall"
	Delta         string               `json:"delta,omitempty"`
	Message       *ai.Message          `json:"message,omitempty"`
	ToolCallID    string               `json:"tool_call_id,omitempty"`
	ToolName      string               `json:"tool_name,omitempty"`
	ToolArgs      json.RawMessage      `json:"tool_args,omitempty"`
	ToolResult    *tools.Result        `json:"tool_result,omitempty"`
	ApprovalID    string               `json:"approval_id,omitempty"`
	Approved      *bool                `json:"approved,omitempty"`
	Reason            string               `json:"reason,omitempty"`
	CompactedMessages []ai.Message         `json:"compacted_messages,omitempty"`
	Telemetry         *store.TurnTelemetry `json:"telemetry,omitempty"`
	UIStatusKey   string               `json:"ui_status_key,omitempty"`
	UIStatusBadge string               `json:"ui_status_badge,omitempty"`
	Error         string               `json:"error,omitempty"`
}

// EventSink consumes ordered Agent Loop events (for persistence, Outbox queueing, and live UI streaming).
type EventSink func(ev Event)

// PermissionMode defines how non-read-only tool calls are authorized.
type PermissionMode string

const (
	PermissionFullAccess  PermissionMode = "full_access"
	PermissionInteractive PermissionMode = "interactive"
	PermissionAutonomous  PermissionMode = "autonomous"
	PermissionReadOnly    PermissionMode = "read_only"
	PermissionPlan        PermissionMode = "plan"
)

// ApprovalRequest describes a sensitive tool execution awaiting authorization.
type ApprovalRequest struct {
	ApprovalID string          `json:"approval_id"`
	SessionID  string          `json:"session_id"`
	ToolCallID string          `json:"tool_call_id"`
	ToolName   string          `json:"tool_name"`
	Arguments  json.RawMessage `json:"arguments"`
}

// Approver resolves interactive tool approval prompts (e.g., via Server -> Web/Mobile UI).
type Approver interface {
	RequestApproval(ctx context.Context, req ApprovalRequest) (approved bool, reason string, err error)
}

// ToolCallDecision allows plugins/hooks to mutate arguments or block a tool invocation before execution.
type ToolCallDecision struct {
	Block       bool
	Reason      string
	UpdatedArgs json.RawMessage
}

// ToolResultMutation allows plugins/hooks to append diagnostics (e.g., auto-gofmt errors) after tool execution.
type ToolResultMutation struct {
	ReplaceOutput *string
	AppendText    string
}

// Hooks provides lifecycle interception points for the dual-layer Extension/Plugin engine (pkg/extension).
type Hooks interface {
	OnContextBuild(ctx context.Context, messages []ai.Message) ([]ai.Message, error)
	OnToolCall(ctx context.Context, toolName string, args json.RawMessage) (ToolCallDecision, error)
	OnToolResult(ctx context.Context, toolName string, args json.RawMessage, res tools.Result) (ToolResultMutation, error)
}

// SystemPromptHook is an optional extension hook interface allowing plugins to inspect or
// append dynamic sections to SystemSections before each LLM call (aligned with pi-agent).
type SystemPromptHook interface {
	OnSystemPrompt(ctx context.Context, sections []ai.SystemSection) ([]ai.SystemSection, error)
}
