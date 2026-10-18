package taskindex

// syncer_state.go — per-workspace ingest state and the frame rules that turn
// sessions-index traffic into index writes.
//
// The owner chain per workspace (AGENTS.md: no duplicate state, one writer):
//
//   agent runtime ──sessions-index topic──▶ Syncer ──▶ Repo (tasks table)
//        │                                      │
//        └── available/unavailable lifecycle    └──▶ workspace emitter (UI)
//
// Ordering rules that must not be simplified away:
//   - The first frame is a SILENT baseline: it only fills missing rows. It must
//     not replay historical terminal transitions nor broadcast, or a cold start
//     would fire a completion notification for every session ever created.
//   - A session with no baseline (cold hydration, or a session that first appears
//     after a gap downgrade) never replays a terminal transition. A live session
//     necessarily enters the baseline as running/prewarming first.
//   - updatedAt is monotonic: the row never moves backwards.

import (
	"sync"
	"time"

	adapter "github.com/tylinux/nex/go/internal/adapters/taskindex"
)

// Phase values (SessionPhase).
const (
	PhaseRunning              = "running"
	PhasePrewarming           = "prewarming"
	PhaseCompletedSuccess     = "completedSuccess"
	PhaseCompletedInterrupted = "completedInterrupted"
	PhaseError                = "error"
	PhaseDraft                = "draft"
)

// Delivery kinds.
const (
	DeliveryInitial  = "initial"
	DeliveryOnline   = "online"
	DeliveryRecovery = "recovery"
)

// isTerminalPhase is the terminal phase set (isTerminalPhase).
func isTerminalPhase(phase string) bool {
	return phase == PhaseCompletedSuccess || phase == PhaseCompletedInterrupted || phase == PhaseError
}

// taskStatusFromSummaryPhase ports taskStatusFromSummaryPhase.
func taskStatusFromSummaryPhase(phase string) string {
	switch phase {
	case PhaseRunning, PhasePrewarming:
		return "running"
	case PhaseCompletedSuccess, PhaseCompletedInterrupted:
		return "completed"
	case PhaseError:
		return "error"
	}
	return ""
}

// terminalUnreadSignal ports resolveTerminalUnreadSignal: only a settled terminal
// phase (or a verified goal) is worth a background notification.
func terminalUnreadSignal(phase, goalStatus string) string {
	if phase == PhaseError {
		return "background_terminal"
	}
	if (phase == PhaseCompletedSuccess || phase == PhaseCompletedInterrupted) &&
		(goalStatus == "" || goalStatus == "verified") {
		return "background_terminal"
	}
	return ""
}

// ingestState is WorkspaceIngestState for one workspace. Every field is owned by
// the syncer's single goroutine per workspace; mu only guards cross-goroutine
// entry (RPC callbacks, dispose).
type ingestState struct {
	target WorkspaceTarget

	// runtimeGeneration is the attached runtime; -1 means dormant.
	runtimeGeneration int64

	subscriptionID         string
	subscriptionGeneration int64

	logEpoch string
	seq      int64
	// hasAppliedBase: the ACK's epoch/seq is admission metadata only. A base
	// exists only after a logical frame was atomically applied — otherwise a
	// lost initial batch could build a cold baseline out of gaps.
	hasAppliedBase bool

	summaries map[string]SessionSummary
	seeded    bool

	recovery *recoveryState

	retryTimer   *retryTimer
	retryAttempt int

	lastWarnAt int64

	schedule func(delay time.Duration, fire func())

	mu sync.Mutex
}

// recoveryState is TopicRecoveryState for the same-sub resync in flight.
type recoveryState struct {
	generation int64
	// forceSnapshot: the current same-sub stream is not provable, ask for a
	// snapshot instead of stitching deltas.
	forceSnapshot bool
	ackReceived   bool
	frameApplied  bool
	// upgradeToSnapshot: a recovery was requested without a snapshot while one
	// was in flight; upgrade when the flight settles.
	upgradeToSnapshot bool
	// postRecoveryGapPending: an online frame arrived after the recovery frame —
	// another gap follows this recovery.
	postRecoveryGapPending bool
}

type retryTimer struct {
	// channel is closed to fire the retry; nil means "no retry scheduled".
	channel chan struct{}
	kind    retryKind
}

type retryKind string

const (
	retryTransient        retryKind = "transient"
	retryProviderNotReady retryKind = "provider-not-ready"
)

const (
	providerNotReadyRetryMs = 5_000
	// topicSubscribeWarnIntervalMs rate-limits the production warn for a
	// persistently failing topic.
	topicSubscribeWarnIntervalMs = 60_000
)

// isLiveState ports isLiveState: a state that has been replaced by a newer
// generation (or disposed) must stop acting.
func isLiveState(ingests map[string]*ingestState, disposed bool, s *ingestState) bool {
	if disposed {
		return false
	}
	return ingests[s.target.Key()] == s
}

// baselineMetaFromSummary ports buildBaselineMetaFromSummary.
func baselineMetaFromSummary(target WorkspaceTarget, summary SessionSummary) TaskMeta {
	status := taskStatusFromSummaryPhase(summary.Phase)
	meta := TaskMeta{
		TaskID:  summary.SessionID,
		TraceID: "nex-" + summary.SessionID,
		// The summary title is the CLI's authoritative session title.
		Title:             summary.Title,
		TitleOverridden:   summary.TitleSource == "custom",
		WorkspacePath:     target.WorkspacePath,
		WorkspaceIdentity: adapter.IdentityOrNil(target.WorkspaceIdentity),
		CreatedAt:         summary.CreatedAt,
		UpdatedAt:         summary.LastActivityAt,
		Mode:              "build",
		Provider:          adapter.StrPtr(NexAgentProvider),
	}
	if summary.ParentSessionID != "" {
		meta.ForkedFromTaskID = adapter.StrPtr(summary.ParentSessionID)
	}
	if status != "" {
		meta.Status = adapter.StrPtr(status)
	}
	return meta
}
