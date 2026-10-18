package windowcontroller

import (
	"context"
	"errors"
)

// source.go is the seam the runtime resolves through: one task-index reader and
// the live-session observer, per source.
//
// The TS names it resolveSource and returns a bag of closures over the
// Node-side services. Here the same thing is expressed as two narrow interfaces
// so the controller does not depend on the whole task service or the whole agent
// service, and so tests can supply either half independently — an offline source
// with no reader, or an online source with no observer, are both real states.

// SourceReader is the task-index half: what the controller needs from the task
// service. It is deliberately the three LIST reads plus the mutations the
// controller routes, because those are exactly the members the TS projection and
// executeMutation call; nothing else in INexTaskService reaches the sidebar.
//
// Identity is never inferred: every method takes the FULL scope (path + identity
// + remoteSessionId), so a source whose identity is absent cannot accidentally
// read another workspace's rows (AGENTS.md's "no keying by path alone").
type SourceReader interface {
	// ListTaskIndex reads the source's three membership buckets (active, pinned,
	// archived) in one call. The TS issues three parallel listTasks /
	// listPinnedTasks / listArchivedTasks; one method keeps the three buckets
	// atomic on this side so a row cannot be observed in two buckets at once.
	ListTaskIndex(ctx context.Context, scope SourceScope) ([]TaskMembership, error)
	// SearchTasks runs the indexed search for one source, unbounded.
	SearchTasks(ctx context.Context, query ListQuery, scope SourceScope) (SearchResult, error)

	// SetPinned pins or unpins one task.
	SetPinned(ctx context.Context, addr TaskAddress, pinned bool) error
	// SetArchived archives or unarchives one task.
	SetArchived(ctx context.Context, addr TaskAddress, archived bool) error
	// DeleteTask removes one task.
	DeleteTask(ctx context.Context, addr TaskAddress) error
	// DeleteArchivedTask conditionally removes one archived task; it reports
	// whether the row was actually deleted (false = it was already gone).
	DeleteArchivedTask(ctx context.Context, addr TaskAddress) (bool, error)
	// DeleteArchivedTasks conditionally removes a batch and reports per-task
	// outcome, which is the shape the sidebar's archived sweep consumes.
	DeleteArchivedTasks(ctx context.Context, addr TaskAddress, taskIDs []string) (ArchivedDeletionResult, error)
	// SetUnread marks one task read/unread; expectedUnreadAt is the CAS marker
	// (nil omits it and the write is unconditional).
	SetUnread(ctx context.Context, addr TaskAddress, unread bool, expectedUnreadAt *int64) error
}

// SessionObserver is the live half. When a source provides one, the runtime keeps
// a sessions-index observer and feeds SessionOverlays into the projection; when it
// does not, the projection has no live facts for that source and every row falls
// back to defaultLiveStatus (which never claims running).
//
// It is the Go seam for the TS windowHostSessionsIndexObserver, which is built on
// the taskindex syncer's existing-only sessions-index subscription (step 3.10.4).
// The observer itself is not re-implemented here: internal/services/taskindex
// already owns the subscription, the frame assembly and the runtime-generation
// handling, and duplicating them would create a second owner for one subscription.
type SessionObserver interface {
	// Observe starts (or joins) the passive sessions-index observation for the
	// scope and calls onChange with the current conflated summaries. It returns a
	// stop func that releases the subscription. It must be EXISTING-ONLY: reading
	// the sidebar must never start an agent or materialize a session.
	Observe(ctx context.Context, scope SourceScope, onChange func(overlays []SessionOverlay)) (stop func(), err error)
}

// TaskMembership is one task-index row plus the bucket it was read from.
type TaskMembership struct {
	Meta       *taskMeta
	Membership Membership
}

// SearchResult is one source's search answer.
type SearchResult struct {
	Items   []SearchItem
	Total   int
	HasMore bool
}

// SearchItem is one search hit: the meta plus the matched excerpts.
type SearchItem struct {
	Meta           *taskMeta
	SearchSnippets []string
}

// ResolvedSource is what resolveSource returned: a scope plus the two halves.
// A reader may be nil (the source is registered for availability but cannot be
// read) and an observer may be nil (no live facts).
type ResolvedSource struct {
	Scope        SourceScope
	Reader       SourceReader
	Observer     SessionObserver
	Availability string
}

// SourceResolver maps a requested scope to the source that owns it, or nil when
// no source does.
//
// Returning nil is the fail-closed half: the server variant returns nil for a
// remote identity, because a remote workspace must never be answered from the
// LOCAL task index. A nil result makes the controller skip that scope entirely
// (it is not an error and it is not an empty list — the caller's other scopes
// still resolve).
type SourceResolver func(scope SourceScope) *ResolvedSource

// OnSourceError is the runtime's error reporter. It is called with the scope and
// the operation ("refresh" | "search") and never aborts the surrounding call:
// one source failing must not clear the trusted projection of another (the TS
// wraps every per-source read in a try/catch that reports and continues).
type OnSourceError func(scope SourceScope, operation string, err error)

// ErrSourceUnavailable is the refusal a write gets when the target source is not
// online. It is deliberately distinct from "no such source": the UI offers
// "retry" for one and simply omits the row for the other.
var ErrSourceUnavailable = errors.New("controller source is not available")

// ErrNoSource is the refusal when no source matches the target address at all.
var ErrNoSource = errors.New("no controller source matches the task address")
