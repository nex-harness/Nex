// Package sessionstore defines the port for session persistence. It is pure Go:
// no database/sql, no file or network I/O. The SQLite implementation lives in
// go/internal/adapters/store.
//
// The row shapes and JSON encodings here mirror the Node agent exactly
// (apps/nex-cli/packages/adapters/src/storage/session-store). Key order matters:
// Node writes with JSON.stringify, which preserves insertion order, and the Go
// code must produce byte-identical column values. That is checked by
// go/testdata/vectors/store-scenarios.json, generated from the real Node
// repositories by scripts/go-rewrite/gen-store-vectors.mts.
package sessionstore

// Clock supplies the wall-clock milliseconds that the Node repositories read
// from Date.now(). Injecting it makes the writes deterministic in tests.
type Clock interface {
	Now() int64
}

// CreateSessionInput mirrors the fields createSession writes to the session row.
type CreateSessionInput struct {
	ID           string
	ProjectID    string
	WorkspaceID  *string
	ParentID     *string
	TraceID      *string
	TaskType     string // defaults to "interactive"
	Slug         string
	Directory    string
	Path         *string
	Title        string
	TitleSource  string // defaults to "first_input"
	TitleMessage *string
	Version      string
	Permission   []byte // already-encoded JSON, or nil
	TimeCreated  *int64
	TimeUpdated  *int64
}

// SessionEntry is a row of the session_entry table. Data is the raw JSON the
// caller wants stored after the adapter's own wrapping (model_selection is
// wrapped as {"modelSelection": ...}). TouchSession mirrors the port's option.
type SessionEntry struct {
	ID           string
	SessionID    string
	Type         string
	TimeCreated  int64
	TimeUpdated  int64
	Data         []byte
	TouchSession bool
}

// SessionInput is the admission record for the session_input ledger.
type SessionInput struct {
	ID        string
	SessionID string
	Kind      string
	Delivery  string // startNow | guide | queue
	Payload   []byte
	TimeNow   *int64
}

// SessionInputUpdate is one entry of updateSessionInputs (queue edit/reorder).
type SessionInputUpdate struct {
	ID            string
	Text          *string
	QueuePosition *int
	// Intent, when non-nil, is the full TurnInputIntentMetadata object that
	// replaces payload.intent and drives the delivery/steer rewrite.
	Intent []byte
}

// ModelUsage is a row of model_usage.
type ModelUsage struct {
	ID                       string
	LogicalRequestID         string
	AttemptIndex             int
	SessionID                string
	TurnID                   *string
	TraceID                  *string
	SpanID                   *string
	AssistantMessageID       *string
	ParentUserMessageID      *string
	QuerySource              string
	ProviderID               string
	ModelID                  string
	Variant                  *string
	Agent                    *string
	Mode                     *string
	TaskType                 *string
	Status                   string
	StartedAt                int64
	FirstTokenAt             *int64
	CompletedAt              *int64
	DurationMs               *int64
	TimeToFirstTokenMs       *int64
	FinishReason             *string
	ToolCallCount            int
	InputTokens              int
	OutputTokens             int
	ReasoningTokens          int
	CacheCreationInputTokens int
	CacheReadInputTokens     int
	ProviderTotalTokens      *int64
	ComputedTotalTokens      *int64
	RetryCount               int
	Retryable                bool
	CancelledByUser          bool
	ContextExceeded          bool
	ErrorType                *string
	ErrorCode                *string
	ErrorMessage             *string
	RawUsage                 []byte
	ProviderMetadata         []byte
}

// TurnUsage is a row of turn_usage (keyed by session_id + turn_id).
type TurnUsage struct {
	SessionID                string
	TurnID                   string
	TraceID                  *string
	UserMessageID            *string
	Status                   string
	StartedAt                int64
	FirstModelStartAt        *int64
	FirstTokenAt             *int64
	CompletedAt              *int64
	DurationMs               *int64
	TimeToFirstTokenMs       *int64
	ModelRequestCount        int
	ModelRetryCount          int
	ToolCallCount            int
	ToolErrorCount           int
	InputTokens              int
	OutputTokens             int
	ReasoningTokens          int
	CacheCreationInputTokens int
	CacheReadInputTokens     int
	ComputedTotalTokens      int
	Retryable                bool
	CancelledByUser          bool
	ContextExceeded          bool
	ErrorType                *string
	ErrorCode                *string
}

// ToolUsage is a row of tool_usage.
type ToolUsage struct {
	ID                  string
	SessionID           string
	TurnID              *string
	TraceID             *string
	ToolCallID          string
	ToolName            string
	SideEffectScope     *string
	ReadOnly            *bool
	Destructive         *bool
	ApprovalStatus      *string
	Status              string
	StartedAt           int64
	FirstOutputAt       *int64
	CompletedAt         *int64
	DurationMs          *int64
	TimeToFirstOutputMs *int64
	ExitCode            *int64
	OutputBytes         int
	StdoutBytes         int
	StderrBytes         int
	Truncated           bool
	RetryCount          int
	Retryable           bool
	CancelledByUser     bool
	ErrorType           *string
	ErrorCode           *string
	ErrorMessage        *string
}

// InputHistory records a recalled prompt. Attachments is raw JSON or nil.
type InputHistory struct {
	ProjectID   string
	SessionID   *string
	Text        string
	Attachments []byte
	Kind        string
	TimeCreated *int64
}

// MessageWithParts is the read side of the transcript for resume.
type MessageWithParts struct {
	MessageID   string
	SessionID   string
	Sequence    *int64
	TimeCreated int64
	TimeUpdated int64
	Data        []byte
	Parts       []StoredPart
}

// StoredPart is one part row plus its owning ids.
type StoredPart struct {
	ID          string
	MessageID   string
	SessionID   string
	Sequence    *int64
	TimeCreated int64
	TimeUpdated int64
	Data        []byte
}

// SessionRow is the decoded session row (read side), field names as columns.
type SessionRow struct {
	ID               string
	ProjectID        string
	WorkspaceID      *string
	ParentID         *string
	TraceID          *string
	TaskType         string
	Slug             string
	Directory        string
	Path             *string
	Title            string
	TitleSource      string
	TitleMessageID   *string
	Version          string
	ShareURL         *string
	SummaryAdditions *int64
	SummaryDeletions *int64
	SummaryFiles     *int64
	SummaryDiffs     []byte
	Revert           []byte
	Permission       []byte
	TimeCreated      int64
	TimeUpdated      int64
	TimeTitleUpdated *int64
	TimeCompacting   *int64
	TimeArchived     *int64
}

// Store is the session persistence port used by the turn loop.
type Store interface {
	// Session + entries.
	CreateSession(input CreateSessionInput) (SessionRow, error)
	UpdateTitle(id string, title string, titleSource string, timeUpdated int64) (SessionRow, error)
	GetSession(id string) (*SessionRow, error)
	UpsertSessionEntry(entry SessionEntry) error

	// Input ledger.
	AdmitInput(input SessionInput) error
	UpdateInputs(sessionID string, updates []SessionInputUpdate) error
	PromoteInput(id, sessionID string, message Message, parts []Part) error
	MarkInputPromoted(id, sessionID, promotedMessageID string) error
	SettleInput(id, sessionID, status, reason string) error

	// Transcript.
	SaveMessage(message Message) error
	SavePart(part Part) error
	ListMessages(sessionID string) ([]MessageWithParts, error)

	// Usage.
	RecordModelUsage(usage ModelUsage) error
	UpsertTurnUsage(usage TurnUsage) error
	UpsertToolUsage(usage ToolUsage) error

	// Input history.
	RecordInputHistory(input InputHistory) (string, error)

	// Todos.
	UpdateTodos(sessionID string, todos []Todo) error
}

// Todo is a single todo item row.
type Todo struct {
	Content  string
	Status   string
	Priority string
}
