package automations

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

type pendingDispatch struct {
	automation   Automation
	runID        string
	scheduledAt  *int64
	trigger      string
	attempt      int
	runStartedAt time.Time
}

// processDue claims due automations and dispatches them outside the lock.
func (s *Service) processDue() {
	now := s.now()
	var pending []pendingDispatch
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	isFirst := func(automation *Automation) bool {
		return automation.DispatchStatus != DispatchFailedToDispatch || automation.DispatchAttempts == 0
	}
	mutated := false
	for index := range s.store.doc.Automations {
		automation := &s.store.doc.Automations[index]
		if !automation.Enabled || automation.LifecycleStatus != LifecycleActive {
			continue
		}
		if automation.EndAt != nil && now.UnixMilli() > *automation.EndAt {
			// Deadline is a plan boundary; finish before claiming.
			automation.LifecycleStatus = LifecycleCompleted
			automation.Enabled = false
			automation.NextRunAt = nil
			automation.RetryAt = nil
			automation.UpdatedAt = now.UnixMilli()
			mutated = true
			continue
		}
		if automation.Running {
			if automation.ClaimedAt != nil && now.UnixMilli()-*automation.ClaimedAt > claimStaleMS {
				automation.Running = false
				automation.ClaimedAt = nil
			} else {
				continue
			}
		}
		due, scheduledAt := false, int64(0)
		if automation.RetryAt != nil && now.UnixMilli() >= *automation.RetryAt {
			due = true
			if automation.NextRunAt != nil {
				scheduledAt = *automation.NextRunAt
			} else {
				scheduledAt = *automation.RetryAt
			}
		} else if automation.RetryAt == nil && automation.NextRunAt != nil && now.UnixMilli() >= *automation.NextRunAt {
			due = true
			scheduledAt = *automation.NextRunAt
		}
		if !due {
			continue
		}
		if isFirst(automation) && now.UnixMilli()-scheduledAt > misfireGrace.Milliseconds() {
			s.skipMissedLocked(index, now, scheduledAt)
			mutated = true
			continue
		}
		automation.Running = true
		claimed := now.UnixMilli()
		automation.ClaimedAt = &claimed
		automation.DispatchStatus = DispatchClaimed
		automation.UpdatedAt = claimed
		// Every claim is an attempt: the first dispatch is attempt 1, and the
		// 60 s retries count 2 and 3 before failed_to_dispatch.
		automation.DispatchAttempts++
		runScheduled := scheduledAt
		pending = append(pending, pendingDispatch{
			automation:   *automation,
			runID:        fmt.Sprintf("%s:%d", automation.AutomationID, scheduledAt),
			scheduledAt:  &runScheduled,
			trigger:      TriggerSchedule,
			attempt:      automation.DispatchAttempts,
			runStartedAt: now,
		})
	}
	for _, item := range pending {
		s.recordClaimedLocked(item, now)
	}
	// Persist whenever the tick changed anything — a skip-only tick must be
	// durable before the run history is observed, otherwise a crash re-fires
	// the very window it just recorded as missed.
	if mutated || len(pending) > 0 {
		if err := s.store.commit(); err != nil {
			s.logError("persisting claims", err)
		}
	}
	s.mu.Unlock()
	if len(pending) == 0 {
		return
	}
	s.notify()
	s.logInfo("claimed due automations", "count", len(pending))
	for _, item := range pending {
		s.dispatchRun(item)
	}
	s.mu.Lock()
	_ = s.store.commit()
	s.mu.Unlock()
	s.notify()
}

// recordClaimedLocked upserts the run row for a claim (same runId on retry).
func (s *Service) recordClaimedLocked(item pendingDispatch, now time.Time) {
	runID := item.runID
	existing := s.store.findRun(runID)
	if existing >= 0 {
		run := s.store.doc.Runs[existing]
		run.DispatchStatus = DispatchClaimed
		run.Outcome = ""
		run.Error = ""
		run.UpdatedAt = now.UnixMilli()
		if run.ModelSelection == nil {
			run.ModelSelection = item.automation.ModelSelection
		}
		s.store.doc.Runs[existing] = run
		return
	}
	s.store.upsertRun(Run{
		RunID:          runID,
		AutomationID:   item.automation.AutomationID,
		WorkspaceKey:   item.automation.WorkspaceKey,
		ScheduledAt:    item.scheduledAt,
		Trigger:        TriggerSchedule,
		ModelSelection: item.automation.ModelSelection,
		DispatchStatus: DispatchClaimed,
		Attempts:       item.attempt,
		CreatedAt:      now.UnixMilli(),
		UpdatedAt:      now.UnixMilli(),
	})
}

// skipMissedLocked records a skipped run and advances the schedule. A
// one-shot ends instead of being rescheduled from its compatibility rule.
func (s *Service) skipMissedLocked(index int, now time.Time, scheduledAt int64) {
	automation := &s.store.doc.Automations[index]
	runID := fmt.Sprintf("%s:%d", automation.AutomationID, scheduledAt)
	s.store.upsertRun(Run{
		RunID:          runID,
		AutomationID:   automation.AutomationID,
		WorkspaceKey:   automation.WorkspaceKey,
		ScheduledAt:    &scheduledAt,
		Trigger:        TriggerSchedule,
		DispatchStatus: RunDispatchSkipped,
		// Same reason string the old scheduler recorded
		// (packages/desktop/src/scheduler/index.ts:127).
		Error:     "computer_asleep_or_app_not_running",
		CreatedAt: now.UnixMilli(),
		UpdatedAt: now.UnixMilli(),
	})
	if isOneShot(automation) {
		automation.LifecycleStatus = LifecycleCompleted
		automation.Enabled = false
		automation.NextRunAt = nil
		automation.RetryAt = nil
	} else {
		loc := normalizeLocation(automation.Timezone, s.location)
		if value, ok := nextRunAt(automation, now, loc); ok {
			millis := value.UnixMilli()
			automation.NextRunAt = &millis
		} else {
			automation.NextRunAt = nil
		}
	}
	automation.Running = false
	automation.ClaimedAt = nil
	automation.DispatchStatus = DispatchIdle
	automation.DispatchAttempts = 0
	automation.RetryAt = nil
	automation.UpdatedAt = now.UnixMilli()
	s.logInfo("skipped missed window", "automation", automation.AutomationID, "scheduledAt", scheduledAt)
}

// dispatchRun performs one runner call and settles the result. Failures are
// retried with a 60 s delay up to three attempts.
func (s *Service) dispatchRun(item pendingDispatch) {
	ctx := s.lifetimeContext()
	command := Command{
		CommandID:     item.runID,
		CreateSession: item.automation.TargetTaskID == "",
		SessionID:     item.automation.TargetTaskID,
		Text:          item.automation.Prompt,
		Mode:          item.automation.Mode,
		// automation 会话无人值守：runner 侧据此立即 deny 审批并打 cron 徽章。
		AutomationID: item.automation.AutomationID,
	}
	if item.automation.ModelSelection != nil {
		encoded, err := json.Marshal(item.automation.ModelSelection)
		if err == nil {
			command.ModelSelection = encoded
		}
	}
	target := Target{
		WorkspacePath:     item.automation.WorkspacePath,
		WorkspaceIdentity: item.automation.WorkspaceIdentity,
	}
	sessionID, err := s.dispatchCommand(ctx, target, command)
	if err == nil {
		s.settleDispatched(item, sessionID, s.now())
		return
	}
	s.settleDispatchFailed(item, err, s.now())
}

// dispatchCommand calls the dispatcher with a bounded context.
func (s *Service) dispatchCommand(ctx context.Context, target Target, command Command) (string, error) {
	if s.dispatch == nil {
		return "", errors.New("runner offline: no dispatcher configured")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	callCtx, cancel := context.WithTimeout(ctx, dispatchTimeout)
	defer cancel()
	return s.dispatch.Dispatch(callCtx, target, command)
}

// dispatchManual dispatches a manual run claimed by runAutomationNow. A manual
// dispatch failure is recorded on the run and releases the claim; it never
// advances the schedule or retries (the user can retry by hand).
func (s *Service) dispatchManual(ctx context.Context, automation *Automation, run *Run) {
	command := Command{
		CommandID:     run.RunID,
		CreateSession: automation.TargetTaskID == "",
		SessionID:     automation.TargetTaskID,
		Text:          automation.Prompt,
		Mode:          automation.Mode,
		// 同 dispatchRun：manual run 同样无人值守。
		AutomationID: automation.AutomationID,
	}
	if run.ModelSelection != nil {
		if encoded, err := json.Marshal(run.ModelSelection); err == nil {
			command.ModelSelection = encoded
		}
	} else if automation.ModelSelection != nil {
		if encoded, err := json.Marshal(automation.ModelSelection); err == nil {
			command.ModelSelection = encoded
		}
	}
	target := Target{WorkspacePath: automation.WorkspacePath, WorkspaceIdentity: automation.WorkspaceIdentity}
	sessionID, err := s.dispatchCommand(ctx, target, command)
	now := s.now()
	s.mu.Lock()
	index := s.store.findRun(run.RunID)
	if err != nil {
		if index >= 0 {
			failed := s.store.doc.Runs[index]
			failed.DispatchStatus = DispatchFailedToDispatch
			failed.Error = err.Error()
			failed.UpdatedAt = now.UnixMilli()
			s.store.doc.Runs[index] = failed
		}
		if automationIndex := s.store.find(automation.AutomationID); automationIndex >= 0 {
			current := &s.store.doc.Automations[automationIndex]
			// Release only OUR claim: a scheduler window may have re-claimed
			// the automation while this manual dispatch was in flight.
			if current.ClaimedAt != nil && *current.ClaimedAt == manualClaimedAt(run) {
				current.Running = false
				current.ClaimedAt = nil
			}
			current.LastError = err.Error()
			current.UpdatedAt = now.UnixMilli()
		}
		s.logWarn("manual dispatch failed", "runId", run.RunID, "error", err.Error())
	} else {
		if index >= 0 {
			dispatched := s.store.doc.Runs[index]
			dispatched.DispatchStatus = DispatchDispatchedStatus
			dispatched.SessionID = sessionID
			dispatched.Error = ""
			dispatched.UpdatedAt = now.UnixMilli()
			s.store.doc.Runs[index] = dispatched
		}
		if automationIndex := s.store.find(automation.AutomationID); automationIndex >= 0 {
			current := &s.store.doc.Automations[automationIndex]
			// Release only OUR claim: the manual run is in flight on the
			// runner, not in this service, and a scheduler window claimed in
			// the meantime must keep its claim.
			if current.ClaimedAt != nil && *current.ClaimedAt == manualClaimedAt(run) {
				current.Running = false
				current.ClaimedAt = nil
			}
			current.RunCount++
			last := now.UnixMilli()
			current.LastRunAt = &last
			current.UpdatedAt = now.UnixMilli()
		}
	}
	if err := s.store.commit(); err != nil {
		s.logError("persisting manual dispatch", err)
	}
	s.mu.Unlock()
	// notify() takes the same mutex, so it runs after the unlock.
	s.notify()
}

// manualClaimedAt is the claim stamp runNow recorded for a manual run: the
// run's scheduledAt (runNow sets both from the same clock reading).
func manualClaimedAt(run *Run) int64 {
	if run.ScheduledAt != nil {
		return *run.ScheduledAt
	}
	return 0
}
