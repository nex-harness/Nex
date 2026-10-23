package automations

import (
	"time"
)

// restart revives a failed automation (the only resumable terminal state):
// active, counters cleared, nextRunAt recomputed from now.
func (s *Service) restart(automationID string, scope workspaceScope, now time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return errClosed
	}
	if err := s.store.load(); err != nil {
		return err
	}
	index := s.store.find(automationID)
	if index < 0 || !scope.matches(&s.store.doc.Automations[index]) {
		return ErrNotFound
	}
	existing := s.store.doc.Automations[index]
	if existing.LifecycleStatus != LifecycleFailed {
		// completed is a naturally exhausted plan; old callers could not revive it.
		return nil
	}
	loc := normalizeLocation(existing.Timezone, s.location)
	next := existing
	next.LifecycleStatus = LifecycleActive
	next.Enabled = true
	next.RunCount = 0
	next.ScheduledRunCount = 0
	next.DispatchAttempts = 0
	next.RetryAt = nil
	next.DispatchStatus = DispatchIdle
	next.Running = false
	next.ClaimedAt = nil
	next.LastError = ""
	if value, ok := nextRunAt(&next, now, loc); ok {
		millis := value.UnixMilli()
		next.NextRunAt = &millis
	} else {
		next.NextRunAt = nil
	}
	next.UpdatedAt = now.UnixMilli()
	s.store.doc.Automations[index] = next
	if err := s.store.commit(); err != nil {
		s.store.doc.Automations[index] = existing
		return err
	}
	s.notifyInvoke()
	return nil
}

// runNow inserts a claimed manual run, occupying the single-flight claim. It
// returns ErrDuplicateRun when a run (manual or schedule) is in flight, and
// the created run otherwise. The caller dispatches after the response.

// nextLifecycleCanRun reports whether a next run may be scheduled in the given
// lifecycle state (active or paused keeps the schedule; paused only stops
// dispatch while enabled=false).
func nextLifecycleCanRun(status string) bool {
	return status == LifecycleActive || status == LifecyclePaused
}
