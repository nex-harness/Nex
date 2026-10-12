package tools

import "context"

// Context is what a tool handler needs from the executor: the filesystem port,
// the session read-state, the workspace roots and the cancellation signal.
type Context struct {
	ToolCallID       string
	FS               FS
	ReadState        *ReadState
	WorkingDirectory string
	WorkspaceRoot    string
	SessionID        string
	Ctx              context.Context
	// RecordReadStateMetadata is called with the entry after a successful read
	// (used by the runtime to persist it); nil is allowed.
	RecordReadStateMetadata func(metadata ReadStateEntry, toolName string)
}

// Failure mirrors ToolHandlerFailure: a predictable business failure whose
// message is what the model sees.
type Failure struct {
	ErrorCode int
	Message   string
}

func (f *Failure) Error() string { return f.Message }

// TempPath marks a path the caller must treat as workspace-relative in tests: the
// tools receive an already-resolved absolute path in Context-resolved input.

// ToIntPtr returns a pointer to v, for the optional offset/limit inputs.
func ToIntPtr(v int) *int { return &v }
