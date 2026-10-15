// reclaim.go owns everything that takes an agent process *away*: the per-workspace
// dispose, the manager-wide dispose (with and without a wait), the idle reclaim,
// and the single idempotent reaper they all share.
//
// The reaper is deliberately one function. A protocol close, a request timeout,
// an idle timer, a workspace restart and app quit can all fire for the same
// process; if each had its own teardown, the second one would either race the
// first or leave MCP descendants behind. Here they all call reap, which:
//  1. records the FIRST cleanup reason as the termination intent (so a later
//     idempotent reclaim can never rewrite a crash into an "expected" exit);
//  2. closes stdin for a graceful stop and waits, then kills the whole tree;
//  3. releases host ownership only after the tree is confirmed reaped.
//
// Ownership is per *instance*, not per workspace key: retiring a resident does
// not drop the manager's handle on it, which is what stops a retired process
// (and its detached MCP descendants) from becoming orphans.
package agentproc

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"time"
)

// DisposeWorkspace retires and reaps the agent for one workspace, bumping its
// epoch so a concurrent start fails rather than racing the new configuration
// (the TS restartWorkspaceProcess path).
func (m *Manager) DisposeWorkspace(ctx context.Context, ws WorkspaceRef) error {
	key := ws.Key()

	m.mu.Lock()
	m.epoch[key]++ // invalidate any in-flight start's generation check
	current, ok := m.byKey[key]
	delete(m.byKey, key)
	m.mu.Unlock()

	if !ok {
		return nil
	}
	m.clearIdleTimer(current)
	m.emitUnavailable(current)
	return m.reap(ctx, current, ReasonWorkspaceDispose, ReasonWorkspaceDisposeRet, "workspace dispose")
}

// DisposeAll retires every resident and marks the manager closed.
func (m *Manager) DisposeAll() {
	m.mu.Lock()
	m.disposed = true
	managedList := make([]*managed, 0, len(m.owned))
	for entry := range m.owned {
		managedList = append(managedList, entry)
	}
	m.byKey = map[string]*managed{}
	m.mu.Unlock()

	for _, entry := range managedList {
		m.clearIdleTimer(entry)
		m.emitUnavailable(entry)
		go func(entry *managed) {
			_ = m.reap(context.Background(), entry, ReasonManagerDispose, ReasonManagerDisposeRetry, "manager dispose")
		}(entry)
	}
}

// DisposeAllAndWait retires every resident and waits for each tree to be reaped.
// It is the host/app-quit path: the caller must be able to prove no agent or MCP
// descendant survived.
func (m *Manager) DisposeAllAndWait(ctx context.Context) {
	m.mu.Lock()
	m.disposed = true
	managedList := make([]*managed, 0, len(m.owned))
	for entry := range m.owned {
		managedList = append(managedList, entry)
	}
	m.byKey = map[string]*managed{}
	m.mu.Unlock()

	for _, entry := range managedList {
		m.clearIdleTimer(entry)
		m.emitUnavailable(entry)
		_ = m.reap(ctx, entry, ReasonManagerDispose, ReasonManagerDisposeRetry, "manager dispose")
	}
}

// reap is the single idempotent reaper. It records the first cleanup reason as the
// termination intent, closes stdin for a graceful stop, waits, kills the tree and
// releases host ownership only after the tree is confirmed gone.
//
// The retry variant matters on Windows and under load: the first observation can
// be a lagging process-table snapshot, so a residue is retried once before it is
// reported as a failure.
func (m *Manager) reap(ctx context.Context, entry *managed, reason, retryReason CleanupReason, scope string) error {
	entry.cleanupMu.Lock()
	switch {
	case entry.reapDone == nil:
		// First caller: it owns the teardown and closes reapDone when finished.
		entry.reapDone = make(chan struct{})
		entry.reapStarted = true
		entry.cleanupMu.Unlock()
	case !entry.reapStarted:
		// Cleanup already finished; hand back its result.
		err := entry.cleanupErr
		entry.cleanupMu.Unlock()
		return err
	default:
		// Another caller is already reaping this exact instance; join it instead of
		// starting a second teardown of one process.
		wait := entry.reapDone
		entry.cleanupMu.Unlock()
		select {
		case <-wait:
			return entry.cleanupErr
		case <-ctx.Done():
			return ctx.Err()
		}
	}

	m.recordTermination(entry, reason)
	m.clearIdleTimer(entry)

	err := entry.child.terminateAndReap(ctx, !entry.child.IsAlive())
	if err != nil {
		m.log.Warn("", "nex agent process cleanup retrying during "+scope,
			"workspaceKey", entry.identity.WorkspaceKey, "pid", entry.child.PID(),
			"cleanupStage", string(reason), "err", err)
		// One retry, reusing the same instance; a real residue keeps throwing.
		err = entry.child.terminateAndReap(ctx, true)
	}

	entry.cleanupMu.Lock()
	entry.cleanupErr = err
	entry.reapStarted = false
	close(entry.reapDone)
	entry.cleanupMu.Unlock()

	m.releaseOwnership(entry)
	if err != nil {
		m.log.Error("", "nex agent process cleanup failed",
			"workspaceKey", entry.identity.WorkspaceKey, "pid", entry.child.PID(),
			"reason", string(retryReason), "err", err)
		return err
	}
	m.log.Info("", "nex agent process cleanup completed",
		"workspaceKey", entry.identity.WorkspaceKey, "pid", entry.child.PID(),
		"reason", string(reason))
	return nil
}

// releaseOwnership drops a reaped instance from the owned set. Ownership is only
// released after the tree is confirmed gone, so a retired process is never
// orphaned.
func (m *Manager) releaseOwnership(entry *managed) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.owned, entry)
	// If this instance is still the resident for its key, evict it so the next
	// GetClient respawns instead of handing back a dead child.
	if current, ok := m.byKey[entry.identity.WorkspaceKey]; ok && current == entry {
		delete(m.byKey, entry.identity.WorkspaceKey)
	}
}

// recordTermination records the FIRST cleanup reason as the root cause and maps
// it to a termination kind. A later idempotent reclaim must not rewrite it.
func (m *Manager) recordTermination(entry *managed, reason CleanupReason) {
	entry.stateMu.Lock()
	defer entry.stateMu.Unlock()
	if entry.firstCleanup != "" {
		return
	}
	entry.firstCleanup = reason
	if reason == ReasonProtocolClose {
		// A protocol close may be the RESULT of a crash, not an intended stop; it
		// must not establish "expected" termination intent on its own.
		return
	}
	entry.termKind = terminationExpected
	if reason == ReasonRequestTimeout {
		entry.termKind = terminationWatchdogRecycle
	}
}

// pumpStderr consumes the child's stderr lines: a well-formed diagnostic is
// reported as a structured exception and NOT added to the tail; every other line
// is redacted, appended to the bounded tail, and debug-logged.
func (inst *managed) pumpStderr(m *Manager) {
	for line := range inst.child.stderrLines {
		if diagnostic, ok := ParseDiagnostic(line); ok {
			// Report against the identity captured at creation, never the current
			// workspace — a late diagnostic from a retired process must not be
			// attributed to the workspace's new instance.
			m.reportException(inst, diagnostic)
			continue
		}
		inst.stderrTail.Append(line)
		m.log.Debug("", line)
	}
}

// observeExit reaps the child, drains stderr and reports the exit with the correct
// termination kind. It also drives ownership release if no explicit reap claimed
// the process (an unexpected crash).
func (inst *managed) observeExit(m *Manager) {
	// child.observeExit is the single reader of the exec adapter's Wait: it reaps
	// the process, publishes the terminal state and closes done. Waiting on done
	// without it would wait for a signal nobody closes.
	inst.child.observeExit()
	info := inst.child.WaitExit()
	// The classification is captured HERE, at the instant of the exit: a cleanup
	// that lands afterwards (app quit finding an already-dead process) must not
	// be able to relabel a crash as an expected exit.
	termKind, termReason := inst.markExited()
	m.clearIdleTimer(inst)

	// The protocol has already failed, but the exit can precede stderr EOF; give
	// the collector a bounded window so the tail is complete before it is read.
	drainStderr(inst.child.stderrLines, ExitStderrDrain)
	snapshot := inst.stderrTail.Snapshot()

	exitCtx := map[string]any{
		"workspaceKey":      inst.identity.WorkspaceKey,
		"pid":               inst.child.PID(),
		"runtimeIdentity":   inst.identity.Identity,
		"code":              info.Code,
		"signal":            info.Signal,
		"terminationKind":   termKind,
		"terminationReason": termReason,
	}
	// Always log the exit so a crash has a trace even when the crash was expected
	// after the fact.
	m.log.Info("", "nex agent process exited", exitCtx)
	if termKind == terminationUnexpected {
		// An agent crash prints only to stderr and exits non-zero; without the tail
		// the production log shows just "code=1" and cannot say why. The lifecycle
		// marker keeps an external collector from counting this as a second,
		// unexplained exception.
		m.log.Error("", "nex agent process exited unexpectedly "+LifecycleLogMarker,
			"workspaceKey", inst.identity.WorkspaceKey, "pid", inst.child.PID(),
			"stderrLineCount", snapshot.LineCount, "stderrTail", snapshot.Tail)
	}

	m.reportExit(inst, info, termKind, termReason, snapshot)

	// If nothing explicitly reaped this process (an unexpected crash), release
	// ownership here; a reclaim that is already running owns the teardown.
	m.releaseOwnershipIfNotReaping(inst)
}

// releaseOwnershipIfNotReaping releases a crashed process's ownership when no
// reap claimed it, so the manager does not hold a dead entry forever. If a reap is
// in progress it will release once the tree is reaped.
func (m *Manager) releaseOwnershipIfNotReaping(inst *managed) {
	inst.cleanupMu.Lock()
	reaping := inst.reapStarted
	inst.cleanupMu.Unlock()
	if reaping {
		return
	}
	m.releaseOwnership(inst)
}

// rescheduleIdle arms the idle reclaim for a manager that has one configured.
// Touching the client counts as work, so the timer is re-armed on every access.
func (m *Manager) rescheduleIdle(entry *managed, key string) {
	if m.opts.IdleTimeoutMs <= 0 {
		return
	}
	m.clearIdleTimer(entry)
	entry.stateMu.Lock()
	entry.idleTimer = time.AfterFunc(time.Duration(m.opts.IdleTimeoutMs)*time.Millisecond, func() {
		m.onIdleTimeout(entry, key)
	})
	entry.stateMu.Unlock()
}

// onIdleTimeout reclaims the tree when a manager with an idle timeout has had no
// work for the configured window. It is a no-op if the process has exited or is
// no longer the resident instance.
func (m *Manager) onIdleTimeout(entry *managed, key string) {
	m.mu.Lock()
	current, isResident := m.byKey[key]
	m.mu.Unlock()
	if entry.hasExited() || !isResident || current != entry {
		return
	}
	m.log.Info("", "nex agent process idle timeout; reclaiming",
		"workspaceKey", key, "pid", entry.child.PID(), "idleTimeoutMs", m.opts.IdleTimeoutMs)
	m.mu.Lock()
	if current, ok := m.byKey[key]; ok && current == entry {
		delete(m.byKey, key)
	}
	m.mu.Unlock()
	m.emitUnavailable(entry)
	_ = m.reap(context.Background(), entry, ReasonIdleTimeout, ReasonIdleTimeoutRetry, "idle timeout")
}

// clearIdleTimer stops any pending idle reclaim for an instance. It is called
// from the exit observer and from every reclaim path, so the timer field is read
// and written under stateMu.
func (m *Manager) clearIdleTimer(entry *managed) {
	entry.stateMu.Lock()
	timer := entry.idleTimer
	entry.idleTimer = nil
	entry.stateMu.Unlock()
	if timer != nil {
		timer.Stop()
	}
}

// drainStderr waits up to timeout for the stderr line channel to go quiet (no more
// lines). It is the bounded drain the TS AgentStderrCollector performs; the
// channel is not closed by the exec adapter, so drain is a "no line for timeout"
// wait rather than a close-wait.
func drainStderr(lines <-chan string, timeout time.Duration) {
	if lines == nil {
		return
	}
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	for {
		select {
		case _, ok := <-lines:
			// keep draining; reset nothing — the loop simply consumes until quiet.
			// A closed channel means the writer is gone: stop immediately rather
			// than spin on zero values until the timer fires.
			if !ok {
				return
			}
		case <-timer.C:
			return
		}
	}
}

func envPatchOf(env map[string]string) EnvPatch {
	if len(env) == 0 {
		return nil
	}
	patch := make(EnvPatch, 0, len(env))
	for key, value := range env {
		patch = append(patch, key+"="+value)
	}
	return patch
}

func laneSuffix(lane string) string {
	if lane == "" {
		return ""
	}
	return ":" + lane
}

// randomHex is the opaque runtime instance id suffix (randomUUID in the TS). It is
// not a security boundary — it only has to make two runtimes distinguishable.
func randomHex() string {
	var buf [16]byte
	if _, err := rand.Read(buf[:]); err != nil {
		// A failure here cannot compromise isolation (the epoch already separates
		// generations); fall back to a fixed marker rather than fail a spawn.
		return "0000000000000000"
	}
	return hex.EncodeToString(buf[:])
}
