// types.go holds the vocabulary of the manager: the reasons a process is taken
// away, the epoch that names one runtime, the lifecycle events a caller observes,
// the reporter interface and the constructor options.
//
// They are data rather than behavior, so they live apart from the code that
// spawns (manager.go), reports (lifecycle.go) and tears down (reclaim.go). The
// names and values are the telemetry contract and are ported verbatim from the
// TypeScript unions; a rename here is a wire change, not a refactor.
package agentproc

import (
	"time"

	"github.com/tylinux/nex/go/internal/adapters/logx"
	"github.com/tylinux/nex/go/internal/services/paths"
)

// CleanupReason ports AgentProcessCleanupReason. The names are the log/telemetry
// contract; the "-retry" variant marks the second, best-effort attempt.
type CleanupReason string

// The cleanup reasons, ported verbatim from the TS union.
const (
	ReasonIdleTimeout         CleanupReason = "idle-timeout"
	ReasonIdleTimeoutRetry    CleanupReason = "idle-timeout-retry"
	ReasonManagerDispose      CleanupReason = "manager-dispose"
	ReasonManagerDisposeRetry CleanupReason = "manager-dispose-retry"
	ReasonProtocolClose       CleanupReason = "protocol-close"
	ReasonRequestTimeout      CleanupReason = "request-timeout"
	ReasonWorkspaceDispose    CleanupReason = "workspace-dispose"
	ReasonWorkspaceDisposeRet CleanupReason = "workspace-dispose-retry"
)

// Termination kinds reported to the lifecycle reporter.
const (
	terminationExpected        = "expected"
	terminationWatchdogRecycle = "watchdog_recycle"
	terminationUnexpected      = "unexpected"
)

// RuntimeIdentity ports NexAgentRuntimeIdentity: the epoch of one process.
type RuntimeIdentity struct {
	// Generation is the 1-based per-workspace spawn counter. It increases on every
	// spawn, so a client can tell a reconnect (same generation, new frames) from
	// a restart (higher generation).
	Generation int
	// Identity is an opaque, workspace-scoped string: "<key>:<generation>:<pid>"
	// plus the lane when the manager runs a non-chat lane. It is used for log
	// correlation, not as a protocol identity.
	Identity string
	// ProcessID is the child's pid when the spawn produced one.
	ProcessID int
	// WorkspaceKey is the residency key this runtime belongs to.
	WorkspaceKey string
	// Lane is the process-lane label; the chat lane is empty.
	Lane string
}

// LifecycleEvent ports NexAgentRuntimeLifecycleEvent: available once the child has
// really spawned, unavailable when the current client closes.
type LifecycleEvent struct {
	WorkspacePath     string
	WorkspaceIdentity string
	WorkspaceKey      string
	RuntimeIdentity   RuntimeIdentity
	// State is "available" or "unavailable".
	State string
}

// RestartedEvent ports NexAgentRuntimeRestartedEvent: fired on a spawn whose
// generation is greater than 1, so v4 subscribers know to re-subscribe.
type RestartedEvent struct {
	WorkspaceKey    string
	RuntimeIdentity RuntimeIdentity
}

// ProcessLifecycleReporter ports RuntimeProcessLifecycleReporter: the side-band
// spawn/ready/exception/exit observer. It is best effort — a reporter failure is
// logged and never blocks a spawn or a reclaim.
type ProcessLifecycleReporter interface {
	OnSpawn(ev SpawnEvent)
	OnReady(ev ReadyEvent)
	OnException(ev ExceptionEvent)
	OnError(ev ErrorEvent)
	OnExit(ev ExitEvent)
}

// Spawn ports the reporter's onSpawn payload.
type SpawnEvent struct {
	PID               int
	Provider          string
	Lane              string
	WorkspacePath     string
	Command           string
	Args              []string
	StartedAt         time.Time
	RuntimeGeneration int
	RuntimeInstanceID string
}

// Ready ports onReady.
type ReadyEvent struct {
	PID               int
	Provider          string
	Lane              string
	WorkspacePath     string
	ReadyAt           time.Time
	StartupDuration   time.Duration
	RuntimeGeneration int
	RuntimeInstanceID string
}

// Exception ports onException: a parsed `[nex-process-exception]` stderr line.
type ExceptionEvent struct {
	PID               int
	Provider          string
	Lane              string
	WorkspacePath     string
	RuntimeGeneration int
	RuntimeInstanceID string
	Diagnostic        Diagnostic
}

// Error ports onError: a spawn failure (no process, or a process that errored).
type ErrorEvent struct {
	PID               int
	Provider          string
	Lane              string
	WorkspacePath     string
	Command           string
	Args              []string
	ErrorName         string
	ErrorCode         string
	ErrorMessage      string
	RuntimeGeneration int
	RuntimeInstanceID string
	OccurredAt        time.Time
}

// Exit ports onExit.
type ExitEvent struct {
	PID               int
	Provider          string
	Lane              string
	WorkspacePath     string
	ExitCode          int
	Signal            string
	EndedAt           time.Time
	TerminationKind   string
	RuntimeReady      bool
	TerminationReason string
	RuntimeGeneration int
	RuntimeInstanceID string
	Uptime            time.Duration
	StderrLineCount   int
	StderrTail        []string
}

// Options configures a Manager.
type Options struct {
	// CommandResolver overrides the default agent command. Nil uses
	// ResolveDefaultCommand.
	CommandResolver CommandResolver
	// Lane labels a non-chat process lane (e.g. "mcp-status") so log lines and
	// lifecycle records can be told apart. Empty is the chat lane.
	Lane string
	// IdleTimeoutMs enables idle reclaim for this manager: with no request in
	// flight for this long, the whole tree is reclaimed and the next GetClient
	// transparently respawns. Zero disables reclaim (chat/plugin lanes never
	// reclaim).
	IdleTimeoutMs int
	// SpawnFallbackCwd is used only when the default cwd equals the target
	// workspace and that directory is unusable. It never changes the identity key.
	SpawnFallbackCwd string
	// ResolveSpawnEnv returns per-spawn extra environment (settings proxy etc.),
	// merged after the host environment and before the workspace variables.
	ResolveSpawnEnv func(WorkspaceRef) EnvPatch
	// ProcessLifecycleReporter observes spawn/ready/exception/exit. Nil disables.
	ProcessLifecycleReporter ProcessLifecycleReporter
	// Log receives the service log lines. Nil uses the "nex-agent" scope.
	Log logx.ServiceLogger
}

// WorkspaceRef is the workspace identity triple every agent-facing call carries.
type WorkspaceRef struct {
	WorkspacePath     string
	WorkspaceIdentity string
	// RemoteSessionID is carried through remote links; it does not affect the
	// residency key but must travel with the request.
	RemoteSessionID string
}

// Key is the AGENTS.md identity rule, delegated to the shared helper so this
// package cannot drift from the rest of the services.
func (w WorkspaceRef) Key() string { return paths.WorkspaceKey(w.WorkspacePath, w.WorkspaceIdentity) }
