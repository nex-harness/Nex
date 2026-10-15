// lifecycle.go is the side-band observation of one agent process: the spawn,
// ready, exception, error and exit records the ProcessLifecycleReporter receives,
// plus the lifecycle/restarted event buffers a caller can drain.
//
// It is a separate file because it is pure reporting: none of it changes process
// state, and every entry point here is best effort. A reporter that panics or a
// consumer that stops draining must never be able to break a spawn or a reclaim —
// the TS wraps the reporter call for exactly that reason.
package agentproc

import (
	"fmt"
	"time"
)

// reportSpawn forwards the spawn record.
func (m *Manager) reportSpawn(inst *managed, preflight Preflight) {
	if m.opts.ProcessLifecycleReporter == nil {
		return
	}
	m.safeReport(func(r ProcessLifecycleReporter) {
		r.OnSpawn(SpawnEvent{
			PID: inst.child.PID(), Provider: Provider, Lane: m.opts.Lane,
			WorkspacePath: inst.workspace.WorkspacePath,
			Command:       preflight.Command, Args: preflight.Args,
			StartedAt:         inst.startedAt,
			RuntimeGeneration: inst.identity.Generation, RuntimeInstanceID: inst.instanceID,
		})
	})
}

// MarkReady records that the agent passed its provider/model gate and forwards the
// ready record once per runtime. It is called by the agent service (step 3.10.2)
// after the first successful provider gate; until then the process is spawned but
// not "ready".
func (m *Manager) MarkReady(ws WorkspaceRef) {
	key := ws.Key()
	m.mu.Lock()
	entry, ok := m.byKey[key]
	m.mu.Unlock()
	if !ok {
		return
	}
	// readyAt is guarded by stateMu: MarkReady races the exit observer, and two
	// concurrent provider gates must produce exactly one ready record.
	entry.stateMu.Lock()
	if entry.exited || !entry.readyAt.IsZero() {
		entry.stateMu.Unlock()
		return
	}
	entry.readyAt = time.Now()
	ready := entry.readyAt
	entry.stateMu.Unlock()
	m.reportReady(entry, ready)
}

// reportReady forwards the ready record (startup duration included), once.
func (m *Manager) reportReady(entry *managed, readyAt time.Time) {
	if m.opts.ProcessLifecycleReporter == nil {
		return
	}
	entry.stateMu.Lock()
	if entry.readyReported {
		entry.stateMu.Unlock()
		return
	}
	entry.readyReported = true
	entry.stateMu.Unlock()
	m.safeReport(func(r ProcessLifecycleReporter) {
		r.OnReady(ReadyEvent{
			PID: entry.child.PID(), Provider: Provider, Lane: m.opts.Lane,
			WorkspacePath:     entry.workspace.WorkspacePath,
			ReadyAt:           readyAt,
			StartupDuration:   readyAt.Sub(entry.startedAt),
			RuntimeGeneration: entry.identity.Generation, RuntimeInstanceID: entry.instanceID,
		})
	})
}

// reportException forwards a parsed stderr diagnostic. The redaction happens here
// (RedactForReport) before the record leaves this process, so a secret in a
// diagnostic never reaches the reporter or a log.
func (m *Manager) reportException(entry *managed, diagnostic Diagnostic) {
	if m.opts.ProcessLifecycleReporter == nil {
		return
	}
	redacted := RedactForReport(diagnostic)
	m.safeReport(func(r ProcessLifecycleReporter) {
		r.OnException(ExceptionEvent{
			PID: entry.child.PID(), Provider: Provider, Lane: m.opts.Lane,
			WorkspacePath:     entry.workspace.WorkspacePath,
			RuntimeGeneration: entry.identity.Generation, RuntimeInstanceID: entry.instanceID,
			Diagnostic: redacted,
		})
	})
}

// reportError forwards a spawn failure.
func (m *Manager) reportError(ws WorkspaceRef, generation int, key string, preflight Preflight, err error) {
	if m.opts.ProcessLifecycleReporter == nil {
		return
	}
	m.safeReport(func(r ProcessLifecycleReporter) {
		r.OnError(ErrorEvent{
			PID: 0, Provider: Provider, Lane: m.opts.Lane,
			WorkspacePath: ws.WorkspacePath,
			Command:       preflight.Command, Args: preflight.Args,
			ErrorName: errorName(err), ErrorMessage: err.Error(),
			RuntimeGeneration: generation, RuntimeInstanceID: "",
			OccurredAt: time.Now(),
		})
	})
	m.log.Error("", "nex agent process error "+LifecycleLogMarker,
		"workspaceKey", key, "errorMessage", err.Error())
}

// reportExit forwards the exit record, including the termination kind and the
// stderr tail (only for an unexpected exit, where the tail is the explanation).
func (m *Manager) reportExit(entry *managed, info ExitInfo, termKind, termReason string, snapshot StderrSnapshot) {
	if m.opts.ProcessLifecycleReporter == nil {
		return
	}
	entry.stateMu.Lock()
	runtimeReady := !entry.readyAt.IsZero()
	entry.stateMu.Unlock()
	endedAt := time.Now()
	event := ExitEvent{
		PID: entry.child.PID(), Provider: Provider, Lane: m.opts.Lane,
		WorkspacePath: entry.workspace.WorkspacePath,
		ExitCode:      info.Code, Signal: info.Signal, EndedAt: endedAt,
		TerminationKind:   termKind,
		RuntimeReady:      runtimeReady,
		RuntimeGeneration: entry.identity.Generation, RuntimeInstanceID: entry.instanceID,
		Uptime:          endedAt.Sub(entry.startedAt),
		StderrLineCount: snapshot.LineCount,
	}
	if termReason != "" {
		event.TerminationReason = termReason
	}
	if termKind == terminationUnexpected && len(snapshot.Tail) > 0 {
		event.StderrTail = snapshot.Tail
	}
	m.safeReport(func(r ProcessLifecycleReporter) { r.OnExit(event) })
}

// safeReport runs a reporter callback and swallows its failure. Process-lifecycle
// reporting is a side-band observer; a temporary failure there must never block an
// agent spawn or a reclaim.
func (m *Manager) safeReport(call func(r ProcessLifecycleReporter)) {
	defer func() {
		if recovered := recover(); recovered != nil {
			m.log.Warn("", "nex agent process lifecycle reporter failed", "panic", fmt.Sprint(recovered))
		}
	}()
	call(m.opts.ProcessLifecycleReporter)
}

// emitRestarted buffers a runtime-restarted event. The protocol client (3.10.2)
// drains these to tell v4 subscribers to re-subscribe.
func (m *Manager) emitRestarted(ev RestartedEvent) {
	m.restartMu.Lock()
	defer m.restartMu.Unlock()
	m.restarted = append(m.restarted, ev)
}

// emitLifecycle buffers an available/unavailable lifecycle event.
func (m *Manager) emitLifecycle(ev LifecycleEvent) {
	m.lifeMu.Lock()
	defer m.lifeMu.Unlock()
	m.life = append(m.life, ev)
}

// emitUnavailable buffers the unavailable transition for an instance, but only if
// it is still the current resident for its key — a retired instance must not
// publish "unavailable" over the new one that replaced it.
func (m *Manager) emitUnavailable(entry *managed) {
	key := entry.identity.WorkspaceKey
	m.mu.Lock()
	current, isResident := m.byKey[key]
	if !isResident || current != entry {
		// Either already evicted (by this reap) or replaced. The unavailable event
		// was emitted by whoever evicted it, or will be by the new spawn. Skip to
		// avoid a stale "unavailable" that hides the live instance.
		m.mu.Unlock()
		return
	}
	m.mu.Unlock()
	m.emitLifecycle(LifecycleEvent{
		WorkspacePath: entry.workspace.WorkspacePath, WorkspaceIdentity: entry.workspace.WorkspaceIdentity,
		WorkspaceKey: key, RuntimeIdentity: entry.identity, State: "unavailable",
	})
}

// DrainRestarted returns and clears the buffered runtime-restarted events.
func (m *Manager) DrainRestarted() []RestartedEvent {
	m.restartMu.Lock()
	defer m.restartMu.Unlock()
	out := m.restarted
	m.restarted = nil
	return out
}

// DrainLifecycle returns and clears the buffered lifecycle events.
func (m *Manager) DrainLifecycle() []LifecycleEvent {
	m.lifeMu.Lock()
	defer m.lifeMu.Unlock()
	out := m.life
	m.life = nil
	return out
}

// ManagedCount reports how many processes this manager still owns (spawned and not
// yet reaped). It is the leak signal a test asserts.
func (m *Manager) ManagedCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.owned)
}

// errorName extracts a short, stable name for an error (the TS uses
// error.name). Go errors do not carry a name, so the type is the closest match.
func errorName(err error) string {
	if err == nil {
		return "Error"
	}
	return fmt.Sprintf("%T", err)
}
