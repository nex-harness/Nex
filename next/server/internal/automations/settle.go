package automations

import (
	"time"
)

// settleDispatched records a successful dispatch: run dispatched+sessionId,
// counters advanced, schedule moved to the next occurrence (persisted before
// anybody observes it).
func (s *Service) settleDispatched(item pendingDispatch, sessionID string, now time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if index := s.store.findRun(item.runID); index >= 0 {
		run := s.store.doc.Runs[index]
		run.DispatchStatus = DispatchDispatchedStatus
		if sessionID != "" {
			run.SessionID = sessionID
		}
		run.Error = ""
		run.Attempts = maxInt(run.Attempts, item.attempt)
		run.UpdatedAt = now.UnixMilli()
		s.store.doc.Runs[index] = run
	}
	index := s.store.find(item.automation.AutomationID)
	if index >= 0 {
		automation := &s.store.doc.Automations[index]
		if automation.Running && automation.ClaimedAt != nil && *automation.ClaimedAt == item.runStartedAt.UnixMilli() {
			automation.Running = false
			automation.ClaimedAt = nil
		}
		automation.RunCount++
		automation.ScheduledRunCount++
		last := now.UnixMilli()
		automation.LastRunAt = &last
		automation.DispatchStatus = DispatchDispatchedStatus
		automation.DispatchAttempts = 0
		automation.RetryAt = nil
		automation.LastError = ""
		loc := normalizeLocation(automation.Timezone, s.location)
		next, hasNext := nextRunAt(automation, now, loc)
		limit := 1
		if automation.MaxRuns != nil {
			limit = *automation.MaxRuns
		}
		reachedMax := !automation.Recurring && automation.ScheduledRunCount >= limit
		reachedEnd := automation.EndAt != nil && (!hasNext || next.UnixMilli() > *automation.EndAt)
		if reachedMax || reachedEnd {
			automation.LifecycleStatus = LifecycleCompleted
			automation.Enabled = false
			automation.NextRunAt = nil
		} else if hasNext {
			millis := next.UnixMilli()
			automation.NextRunAt = &millis
		} else {
			automation.NextRunAt = nil
			if !automation.Recurring {
				automation.LifecycleStatus = LifecycleCompleted
				automation.Enabled = false
			}
		}
		automation.UpdatedAt = now.UnixMilli()
	}
	if err := s.store.commit(); err != nil {
		s.logError("persisting dispatch result", err)
	}
	s.logInfo("dispatched run", "runId", item.runID, "sessionId", sessionID)
}

// settleDispatchFailed applies the retry policy: attempts 1..2 back off 60 s;
// the third attempt gives up. A recurring automation keeps its schedule and
// records lastError; a one-shot becomes failed and disabled (Restart revives).
func (s *Service) settleDispatchFailed(item pendingDispatch, dispatchErr error, now time.Time) {
	message := dispatchErr.Error()
	s.mu.Lock()
	defer s.mu.Unlock()
	if index := s.store.findRun(item.runID); index >= 0 {
		run := s.store.doc.Runs[index]
		run.DispatchStatus = DispatchFailedToDispatch
		run.Error = message
		run.UpdatedAt = now.UnixMilli()
		s.store.doc.Runs[index] = run
	}
	index := s.store.find(item.automation.AutomationID)
	if index < 0 {
		return
	}
	automation := &s.store.doc.Automations[index]
	if automation.Running && automation.ClaimedAt != nil && *automation.ClaimedAt == item.runStartedAt.UnixMilli() {
		automation.Running = false
		automation.ClaimedAt = nil
	}
	automation.LastError = message
	automation.UpdatedAt = now.UnixMilli()
	if item.attempt < dispatchMaxAttempts {
		automation.DispatchStatus = DispatchFailedToDispatch
		automation.DispatchAttempts = item.attempt
		retry := now.Add(dispatchRetryDelay).UnixMilli()
		automation.RetryAt = &retry
		s.logWarn("dispatch failed; retrying", "runId", item.runID, "attempt", item.attempt, "error", message)
	} else {
		// Keep the attempt count: it is what bounds the retry chain (a reset
		// here made "first dispatch" detection true again and the 60 s retry
		// loop never ended).
		automation.DispatchStatus = DispatchFailedToDispatch
		automation.RetryAt = nil
		loc := normalizeLocation(automation.Timezone, s.location)
		if automation.Recurring {
			// Give up this window, keep the schedule.
			if value, ok := nextRunAt(automation, now, loc); ok {
				millis := value.UnixMilli()
				automation.NextRunAt = &millis
			} else {
				automation.NextRunAt = nil
			}
			automation.DispatchStatus = DispatchIdle
		} else {
			automation.LifecycleStatus = LifecycleFailed
			automation.Enabled = false
		}
		s.logWarn("dispatch failed permanently", "runId", item.runID, "error", message)
	}
	if err := s.store.commit(); err != nil {
		s.logError("persisting dispatch failure", err)
	}
}
