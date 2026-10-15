// manager.go ports NexAgentProcessManager (step 3.10.1): the per-workspace
// residency of one agent process, the runtime epoch that survives a crash, the
// bounded idle reclaim, the termination-intent bookkeeping and the lifecycle
// reporting.
//
// The invariants this file exists to keep, each with the TS rule it ports:
//
//   - Residency is keyed by paths.WorkspaceKey (identity first, path fallback).
//     A second GetClient for the same key reuses the running process; two
//     workspaces never share one.
//   - The runtime epoch (generation + opaque instance id) increments on every
//     spawn. A subscriber that reconnects after a crash compares epochs and
//     knows the old frames are gone; nothing else carries that fact.
//   - The FIRST cleanup reason is the root cause and is recorded once. A later
//     idempotent reclaim (app quit after a crash) must never rewrite a crash
//     into an "expected" exit — the monitor relies on the difference.
//   - Idle reclaim only fires for a manager configured with an idle timeout, and
//     only while no request is in flight and no storage migration is waiting.
//   - Ownership is released only after the process tree is reaped. A protocol
//     close does not mean the OS process is gone, and detached MCP descendants
//     outlive the root, so the exit path and the cleanup path share one
//     idempotent reaper.
package agentproc

import (
	"context"
	"fmt"
	"github.com/tylinux/nex/go/internal/adapters/logx"
	"sync"
	"time"
)

// managed is one resident agent process plus its epoch and lifecycle state.
//
// stateMu guards the fields both the exit observer and a reclaimer touch: the
// observed exit, the first cleanup reason (the root cause) and the termination
// kind. They are written from two goroutines — the exit observer reads them once
// the child dies, and a reclaim writes them while it tears the process down — so
// without this lock the exit report could read a half-written root cause and
// report a crash as an expected exit, or the reverse.
type managed struct {
	child         *child
	workspace     WorkspaceRef
	identity      RuntimeIdentity
	instanceID    string
	startedAt     time.Time
	readyAt       time.Time
	readyReported bool

	stateMu      sync.Mutex
	exited       bool
	firstCleanup CleanupReason
	termKind     string

	// stderrTail is the bounded redacted tail fed by the stderr line pump. The pump
	// is its only writer and the exit observer its only reader, and the observer
	// reads it after the drain window, never concurrently.
	stderrTail StderrTail
	// idleTimer reclaims the tree after Options.IdleTimeoutMs of no work.
	idleTimer *time.Timer
	// cleanupMu guards the idempotent reaper's state; reapDone is closed when the
	// one reaper finishes so concurrent requests wait on it instead of spinning.
	cleanupMu   sync.Mutex
	reapStarted bool
	reapDone    chan struct{}
	cleanupErr  error
}

// markExited records the observed exit and reports the termination classification
// the exit event must carry. The classification is read at that instant: a
// termination intent recorded LATER (an app quit that finds an already-dead
// process) must not rewrite what the exit actually was.
func (inst *managed) markExited() (termKind, termReason string) {
	inst.stateMu.Lock()
	defer inst.stateMu.Unlock()
	inst.exited = true
	termKind = terminationUnexpected
	if inst.termKind != "" {
		termKind = inst.termKind
	}
	return termKind, string(inst.firstCleanup)
}

// hasExited reports whether the process was observed to exit.
func (inst *managed) hasExited() bool {
	inst.stateMu.Lock()
	defer inst.stateMu.Unlock()
	return inst.exited
}

// Manager owns one agent process per workspace.
type Manager struct {
	opts     Options
	resolver CommandResolver
	log      logx.ServiceLogger

	mu sync.Mutex
	// byKey is the residency map; the value is the CURRENT instance for the key.
	byKey map[string]*managed
	// owned is every process this manager ever spawned and has not finished
	// reaping — including ones retired from byKey. Host ownership lives here so a
	// retired process is never orphaned.
	owned map[*managed]bool
	// epoch counts spawns per workspace and survives disposal.
	epoch map[string]int
	// starting dedups concurrent first spawns for one key.
	starting map[string]chan struct{}
	// disposed latches once Dispose/DisposeAll has run.
	disposed bool

	// events
	restartMu sync.Mutex
	restarted []RestartedEvent
	lifeMu    sync.Mutex
	life      []LifecycleEvent
}

// NewManager constructs a Manager. No process is started here; the first
// GetClient for a workspace spawns it.
func NewManager(opts Options) *Manager {
	resolver := opts.CommandResolver
	if resolver == nil {
		resolver = ResolveDefaultCommand
	}
	logger := opts.Log
	if logger == nil {
		logger = logx.NewServiceLogger("nex-agent", nil)
	}
	return &Manager{
		opts:     opts,
		resolver: resolver,
		log:      logger,
		byKey:    map[string]*managed{},
		owned:    map[*managed]bool{},
		epoch:    map[string]int{},
		starting: map[string]chan struct{}{},
	}
}

// Client is the public handle to one resident agent process. It is the seam the
// protocol client (step 3.10.2) builds on: frames arrive on StdoutLines, requests
// go out on Send, and the process is stopped with Dispose.
//
// The handle is bound to one *process generation*. After a restart the manager
// returns a different Client, and the two must not be mixed — that is what the
// epoch in RuntimeIdentity is for.
// use. Concurrent callers for the same workspace converge on one spawn.
func (m *Manager) GetClient(ctx context.Context, ws WorkspaceRef) (*Client, error) {
	key := ws.Key()
	for {
		m.mu.Lock()
		if m.disposed {
			m.mu.Unlock()
			return nil, ErrDisposed
		}
		if current, ok := m.byKey[key]; ok && current.child.IsAlive() {
			client := newClient(current.child, current.identity, current.workspace)
			m.mu.Unlock()
			// Touching the client counts as work: it re-arms the idle timer so a busy
			// workspace is never reclaimed underneath a caller.
			m.clearIdleTimer(current)
			m.rescheduleIdle(current, key)
			return client, nil
		}
		if gate, ok := m.starting[key]; ok {
			// A start is already in flight for this key (host warmup and the first
			// UI read arrive together). Wait for it and re-check residency: two
			// spawns for one workspace is exactly the bug this convergence prevents.
			m.mu.Unlock()
			select {
			case <-gate:
				continue
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}
		gate := make(chan struct{})
		m.starting[key] = gate
		m.mu.Unlock()

		client, err := m.spawn(ctx, ws, key)

		m.mu.Lock()
		// A concurrent retire (dispose / idle) may have installed or removed an
		// instance while this spawn ran; whatever is resident now is the answer, so
		// a late spawn cannot overwrite the live process with its own.
		if current, ok := m.byKey[key]; ok && current.child.IsAlive() {
			client = newClient(current.child, current.identity, current.workspace)
			err = nil
		}
		delete(m.starting, key)
		m.mu.Unlock()
		close(gate)
		return client, err
	}
}

// GetExistingClient returns the resident client without an implicit spawn, for
// passive observers (the TS getExistingClient). It returns nil when nothing is
// resident or the process has exited.
func (m *Manager) GetExistingClient(ws WorkspaceRef) *Client {
	m.mu.Lock()
	defer m.mu.Unlock()
	current, ok := m.byKey[ws.Key()]
	if !ok || !current.child.IsAlive() {
		return nil
	}
	return newClient(current.child, current.identity, current.workspace)
}

// GetRuntimeIdentity returns the current epoch for a workspace without spawning.
// It errors when nothing is resident — a passive query must never implicitly
// start an agent (the TS bug this ports).
func (m *Manager) GetRuntimeIdentity(ws WorkspaceRef) (RuntimeIdentity, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	current, ok := m.byKey[ws.Key()]
	if !ok || !current.child.IsAlive() {
		return RuntimeIdentity{}, ErrRuntimeUnavailable
	}
	return current.identity, nil
}

// CanStart reports whether an agent command resolves for this workspace, without
// spawning. It is the passive preflight the TS canStart performs.
func (m *Manager) CanStart(ws WorkspaceRef) (bool, string) {
	key := ws.Key()
	command, err := m.resolver(CommandResolverContext{
		WorkspacePath: ws.WorkspacePath, WorkspaceIdentity: ws.WorkspaceIdentity, WorkspaceKey: key,
	})
	if err != nil {
		return false, err.Error()
	}
	if command == nil {
		return false, "GLM_BINARY_PATH is not configured"
	}
	return true, ""
}

// spawn resolves the command, builds the environment, launches the child and
// registers it. It runs the stderr pump and the exit observer for the new child.
func (m *Manager) spawn(ctx context.Context, ws WorkspaceRef, key string) (*Client, error) {
	command, err := m.resolver(CommandResolverContext{
		WorkspacePath: ws.WorkspacePath, WorkspaceIdentity: ws.WorkspaceIdentity, WorkspaceKey: key,
	})
	if err != nil {
		return nil, err
	}
	if command == nil {
		return nil, ErrCommandNotConfigured
	}

	preflight := BuildPreflight(*command, ws.WorkspacePath, m.opts.SpawnFallbackCwd)
	m.log.Info("", "nex agent command resolved",
		"workspaceKey", key, "command", fmtCommand(*command), "preflight", preflight)

	// Settings-page environment (proxy/CA/origin) is resolved per spawn so a
	// change takes effect on the next start.
	settingsPatch := EnvPatch{}
	if m.opts.ResolveSpawnEnv != nil {
		settingsPatch = m.opts.ResolveSpawnEnv(ws)
	}
	env := BuildSpawnEnv(SpawnEnvOptions{
		HostEnv:           HostEnv(),
		Settings:          settingsPatch,
		Command:           envPatchOf(command.Env),
		WorkspaceIdentity: ws.WorkspaceIdentity,
	})

	m.mu.Lock()
	if m.disposed {
		m.mu.Unlock()
		return nil, ErrDisposed
	}
	// Bump the epoch for this spawn. It increases even across a restart, which is
	// what tells a reconnecting subscriber that the process changed.
	m.epoch[key]++
	generation := m.epoch[key]
	m.mu.Unlock()

	entry, err := startChild(ctx, append([]string{command.Command}, preflight.Args...), preflight.Cwd, env)
	if err != nil {
		m.reportError(ws, generation, key, preflight, err)
		return nil, fmt.Errorf("agentproc: start agent for %s: %w", key, err)
	}

	pid := entry.PID()
	lane := m.opts.Lane
	identity := RuntimeIdentity{
		Generation:   generation,
		Identity:     fmt.Sprintf("%s:%d:%d", key, generation, pid) + laneSuffix(lane),
		ProcessID:    pid,
		WorkspaceKey: key,
		Lane:         lane,
	}
	inst := &managed{
		child:      entry,
		workspace:  ws,
		identity:   identity,
		instanceID: "agent-" + randomHex(),
		startedAt:  time.Now(),
	}

	m.mu.Lock()
	// A live resident installed between the gate above and here wins: this spawn
	// must not replace it (that would hand two callers different processes for one
	// workspace). The new child is reaped immediately and the resident is returned.
	if current, ok := m.byKey[key]; ok && current.child.IsAlive() {
		winner := current
		m.mu.Unlock()
		go func() { _ = entry.terminateAndReap(context.Background(), false) }()
		return newClient(winner.child, winner.identity, winner.workspace), nil
	}
	// A concurrent retire (dispose/idle) may have removed the previous resident;
	// replace it but keep ownership of both until each is reaped.
	m.byKey[key] = inst
	m.owned[inst] = true
	m.mu.Unlock()

	// stderr pump: parse diagnostics live, append the rest to the bounded tail.
	go inst.pumpStderr(m)
	// exit observer: reaps once and drives the exit reporting + ownership release.
	go inst.observeExit(m)

	// A generation > 1 means the agent process was rebuilt (timeout reclaim or a
	// crash followed by a respawn). Subscribers must re-subscribe.
	if generation > 1 {
		m.emitRestarted(RestartedEvent{WorkspaceKey: key, RuntimeIdentity: identity})
	}
	m.emitLifecycle(LifecycleEvent{
		WorkspacePath: ws.WorkspacePath, WorkspaceIdentity: ws.WorkspaceIdentity,
		WorkspaceKey: key, RuntimeIdentity: identity, State: "available",
	})
	m.reportSpawn(inst, preflight)
	m.log.Info("", "nex agent process started",
		"workspaceKey", key, "pid", pid, "cwd", preflight.Cwd, "generation", generation)

	m.rescheduleIdle(inst, key)
	return newClient(entry, identity, ws), nil
}
