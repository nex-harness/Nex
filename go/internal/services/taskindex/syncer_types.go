package taskindex

// syncer_types.go — the v4 sessions-index surface the syncer depends on, kept
// as interfaces so the syncer can be tested without an agent process. The
// implementation binds to internal/agentproc/proto (step 3.10.2) plus the
// connection scope (step 3.10.3).

import (
	"context"

	"encoding/json"
	"fmt"
	adapter "github.com/tylinux/nex/go/internal/adapters/taskindex"
)

// V4 method names (V4_METHODS in nex-protocol-v4/transport.ts).
const (
	MethodConversationSubscribe   = "v4/conversation/subscribe"
	MethodConversationResync      = "v4/conversation/resync"
	MethodConversationUnsubscribe = "v4/conversation/unsubscribe"
)

// Error codes the agent service raises; they decide the retry kind.
const (
	// CodeProviderNotReady: the user has not configured a model yet. This is a
	// normal waiting state, retried on a fixed 5 s cadence and never warned in
	// production (two topics × every workspace would flood the log before the
	// user does anything).
	CodeProviderNotReady = "NEX_AGENT_PROVIDER_NOT_READY"
	// CodeRuntimeUnavailable: the agent process is gone. The CLI's in-memory
	// subscriptions died with it, so there is nothing to unsubscribe.
	CodeRuntimeUnavailable = "NEX_AGENT_RUNTIME_UNAVAILABLE"
)

// SessionsIndexTopic ports sessionsIndexTopic.
func SessionsIndexTopic(workspaceID string) string { return "sessions-index/" + workspaceID }

// SessionSummary is the conflated per-session state the sidebar list needs.
type SessionSummary struct {
	SessionID         string `json:"sessionId"`
	WorkspaceID       string `json:"workspaceId"`
	ParentSessionID   string `json:"parentSessionId,omitempty"`
	Title             string `json:"title"`
	TitleSource       string `json:"titleSource,omitempty"`
	Phase             string `json:"phase"`
	SessionEnded      bool   `json:"sessionEnded"`
	HasBackgroundWork bool   `json:"hasBackgroundWork"`
	GoalStatus        string `json:"goalStatus,omitempty"`
	LastActivityAt    int64  `json:"lastActivityAt"`
	CreatedAt         int64  `json:"createdAt"`
}

// SessionDelta is one conflated change (op = session.upserted | session.removed).
type SessionDelta struct {
	Op        string          `json:"op"`
	Session   *SessionSummary `json:"session,omitempty"`
	SessionID string          `json:"sessionId,omitempty"`
}

// SessionsIndexSnapshot is the authoritative full state.
type SessionsIndexSnapshot struct {
	ProtocolVersion int              `json:"protocolVersion"`
	WorkspaceID     string           `json:"workspaceId"`
	LogEpoch        string           `json:"logEpoch"`
	Sessions        []SessionSummary `json:"sessions"`
}

// SessionsIndexFrame is one logical sessions-index frame.
type SessionsIndexFrame struct {
	Topic          string                 `json:"topic"`
	SubscriptionID string                 `json:"subscriptionId"`
	FromSeq        int64                  `json:"fromSeq"`
	ToSeq          int64                  `json:"toSeq"`
	SentAt         int64                  `json:"sentAt"`
	Kind           string                 `json:"-"`
	Snapshot       *SessionsIndexSnapshot `json:"-"`
	Deltas         []SessionDelta         `json:"-"`
}

// UnmarshalJSON splits the discriminated payload so callers do not have to.
func (f *SessionsIndexFrame) UnmarshalJSON(raw []byte) error {
	type alias struct {
		Topic          string `json:"topic"`
		SubscriptionID string `json:"subscriptionId"`
		FromSeq        int64  `json:"fromSeq"`
		ToSeq          int64  `json:"toSeq"`
		SentAt         int64  `json:"sentAt"`
		Payload        struct {
			Kind     string                 `json:"kind"`
			Snapshot *SessionsIndexSnapshot `json:"snapshot"`
			Deltas   []SessionDelta         `json:"deltas"`
		} `json:"payload"`
	}
	var a alias
	if err := json.Unmarshal(raw, &a); err != nil {
		return err
	}
	f.Topic = a.Topic
	f.SubscriptionID = a.SubscriptionID
	f.FromSeq = a.FromSeq
	f.ToSeq = a.ToSeq
	f.SentAt = a.SentAt
	f.Kind = a.Payload.Kind
	f.Snapshot = a.Payload.Snapshot
	f.Deltas = a.Payload.Deltas
	if f.Kind != "snapshot" && f.Kind != "deltas" {
		return fmt.Errorf("invalid sessions-index payload kind %q", f.Kind)
	}
	return nil
}

// SubscribeAck is subscribeAckSchema.
type SubscribeAck struct {
	SubscriptionID string `json:"subscriptionId"`
	Mode           string `json:"mode"`
	LogEpoch       string `json:"logEpoch"`
}

// SubscribeResult is v4SessionsIndexSubscribeResultSchema.
type SubscribeResult struct {
	Ack SubscribeAck `json:"ack"`
}

// ResyncResult is the resync answer; its ack carries the same three fields.
type ResyncResult struct {
	Ack SubscribeAck `json:"ack"`
}

// RuntimeIdentity identifies one attachable agent runtime for a workspace.
type RuntimeIdentity struct {
	Generation int64
	Identity   string
}

// RuntimeLifecycleEvent is the runtime availability signal.
type RuntimeLifecycleEvent struct {
	// State is "available" | "unavailable".
	State             string
	WorkspaceKey      string
	WorkspacePath     string
	WorkspaceIdentity string
	RuntimeIdentity   RuntimeIdentity
}

// SessionSnapshot is the authoritative session state the syncer re-reads to
// converge the row. It is a projection, not the whole protocol snapshot.
type SessionSnapshot struct {
	SessionID         string
	TraceID           string
	Title             string
	WorkspacePath     string
	WorkspaceIdentity string
	CreatedAt         int64
	UpdatedAt         int64
	Mode              string
	Model             string
	Status            string
	LastError         *TaskLastError
	SearchableText    string
	// SessionKind "subagent_child" rows are not mirrored as primary tasks.
	SessionKind           string
	HasUserVisibleContent bool
}

// Agent is the seam to the agent runtime. The production binding is the
// protocol client + connection scope; tests supply a fake.
type Agent interface {
	// SubscribeSessionsIndex starts a background-visibility subscription. The
	// runtimePolicy is "existing-only": the task index is a passive observer and
	// must never materialize or resume a session.
	SubscribeSessionsIndex(ctx context.Context, target WorkspaceTarget, visibility string, subscriberScope string) (SubscribeResult, error)
	// ResyncSessionsIndex recovers a gap on the same subscription. base nil means
	// "force a snapshot".
	ResyncSessionsIndex(ctx context.Context, target WorkspaceTarget, subscriptionID string, base *ResumeBase, forceSnapshot bool) (ResyncResult, error)
	// UnsubscribeSessionsIndex drops a route; "existing-only" again.
	UnsubscribeSessionsIndex(ctx context.Context, target WorkspaceTarget, subscriptionID string) error
	// ReadSession returns the authoritative snapshot without touching the
	// runtime's lifecycle: the task index may only read what already exists.
	ReadSession(ctx context.Context, target SessionTarget) (SessionSnapshot, error)
	// Frames delivers every sessions-index logical frame for a workspace.
	Frames(target WorkspaceTarget, handler func(SessionFrameEvent))
	// OnRuntimeLifecycle registers the availability listener. It returns a
	// function that removes the listener.
	OnRuntimeLifecycle(handler func(RuntimeLifecycleEvent)) func()
}

// SessionFrameEvent is a delivered frame plus its delivery kind.
type SessionFrameEvent struct {
	Frame SessionFrame
	// DeliveryKind is "initial" | "online" | "recovery".
	DeliveryKind string
	// FaultReasonCode is set when physical assembly failed; a frame is absent.
	FaultReasonCode string
}

// SessionFrame is the wire candidate: the same payload plus delivery metadata.
type SessionFrame struct {
	Topic          string          `json:"topic"`
	SubscriptionID string          `json:"subscriptionId"`
	FromSeq        int64           `json:"fromSeq"`
	ToSeq          int64           `json:"toSeq"`
	SentAt         int64           `json:"sentAt"`
	Payload        json.RawMessage `json:"payload"`
}

// Logical decodes the physical candidate into a logical frame.
func (f SessionFrame) Logical() (SessionsIndexFrame, error) {
	var out SessionsIndexFrame
	out.Topic = f.Topic
	out.SubscriptionID = f.SubscriptionID
	out.FromSeq = f.FromSeq
	out.ToSeq = f.ToSeq
	out.SentAt = f.SentAt
	if len(f.Payload) == 0 {
		return out, fmt.Errorf("sessions-index frame %s has no payload", f.Topic)
	}
	var envelope struct {
		Kind     string                 `json:"kind"`
		Snapshot *SessionsIndexSnapshot `json:"snapshot"`
		Deltas   []SessionDelta         `json:"deltas"`
	}
	if err := json.Unmarshal(f.Payload, &envelope); err != nil {
		return out, err
	}
	out.Kind = envelope.Kind
	out.Snapshot = envelope.Snapshot
	out.Deltas = envelope.Deltas
	return out, nil
}

// WorkspaceTarget identifies a workspace by identity key (AGENTS.md).
type WorkspaceTarget struct {
	WorkspacePath     string
	WorkspaceIdentity string
}

// Key is the identity key.
func (w WorkspaceTarget) Key() string {
	return adapter.WorkspaceKey(w.WorkspacePath, w.WorkspaceIdentity)
}

// SessionTarget narrows a workspace to one session.
type SessionTarget struct {
	WorkspacePath     string
	WorkspaceIdentity string
	SessionID         string
}

// Workspace returns the workspace half.
func (s SessionTarget) Workspace() WorkspaceTarget {
	return WorkspaceTarget{WorkspacePath: s.WorkspacePath, WorkspaceIdentity: s.WorkspaceIdentity}
}

// Ref is the repo write scope for the session.
func (s SessionTarget) Ref() TargetRef {
	return TargetRef{WorkspacePath: s.WorkspacePath, WorkspaceIdentity: s.WorkspaceIdentity, TaskID: s.SessionID}
}

// ResumeBase is the proven watermark a same-subscription recovery resumes from.
type ResumeBase struct {
	LogEpoch string `json:"logEpoch"`
	Seq      int64  `json:"seq"`
}

// Aliases for the adapter's value types. The syncer names them without a
// qualifier so the ingestion logic reads like the TypeScript it ports; the
// definitions stay in internal/adapters/taskindex, where the SQL lives.
type (
	// Repo is the task index repository (the tasks table owner).
	Repo = adapter.Repo
	// TaskMeta is the persisted task meta.
	TaskMeta = adapter.TaskMeta
	// TaskLastError is the persisted lastError shape.
	TaskLastError = adapter.TaskLastError
	// TargetRef is the write scope of one task.
	TargetRef = adapter.TargetRef
	// StatePatch is the app-side state patch.
	StatePatch = adapter.StatePatch
	// AgentPatch is the restricted agent-side patch.
	AgentPatch = adapter.AgentPatch
)

// NexAgentProvider is the only provider the sidebar lists.
const NexAgentProvider = adapter.NexAgentProvider

// Broadcast reasons. The reason is mandatory at every emit site: the old default
// (task_meta_changed) is the heaviest refresh semantic the UI has, and every
// unlabelled emitter silently fell into it — that was the "sidebar blinks on
// every keystroke" bug.
const (
	ReasonTaskCreated       = adapter.ReasonTaskCreated
	ReasonTaskStatusChanged = adapter.ReasonTaskStatusChanged
	ReasonTaskTitleChanged  = adapter.ReasonTaskTitleChanged
	ReasonTaskMetaChanged   = adapter.ReasonTaskMetaChanged
)
