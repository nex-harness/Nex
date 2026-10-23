package automations

import (
	"strings"
)

// Outcome is fed by the window-controller when a run's task row turns
// terminal. status is the row status: succeeded / failed / stopped (or
// running). The session id is the one recorded for the run.
type Outcome struct {
	SessionID string
	Status    string
	Error     string
}

// RecordOutcome maps a terminal task-row status onto the run that created the
// session. Unknown sessions and unknown statuses are ignored. A terminal
// outcome is never downgraded back to running (markRunOutcome semantics).
func (s *Service) RecordOutcome(outcome Outcome) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || outcome.SessionID == "" {
		return false
	}
	status := mapOutcomeStatus(outcome.Status)
	if status == "" {
		return false
	}
	// Prefer the newest DISPATCHED run for this session: a later manual run
	// that is still claimed (no session yet) must not shadow it.
	latest, latestDispatched := -1, -1
	for index := range s.store.doc.Runs {
		run := &s.store.doc.Runs[index]
		if run.SessionID != outcome.SessionID {
			continue
		}
		if latest < 0 || run.UpdatedAt >= s.store.doc.Runs[latest].UpdatedAt {
			latest = index
		}
		if run.DispatchStatus == DispatchDispatchedStatus &&
			(latestDispatched < 0 || run.UpdatedAt >= s.store.doc.Runs[latestDispatched].UpdatedAt) {
			latestDispatched = index
		}
	}
	if latestDispatched >= 0 {
		latest = latestDispatched
	}
	if latest < 0 {
		return false
	}
	run := &s.store.doc.Runs[latest]
	if run.Outcome == OutcomeSucceeded || run.Outcome == OutcomeFailed || run.Outcome == OutcomeStopped {
		if status == OutcomeRunning {
			return false
		}
	}
	run.Outcome = status
	if outcome.Error != "" && status != OutcomeRunning {
		run.Error = outcome.Error
	}
	run.UpdatedAt = s.now().UnixMilli()
	if err := s.store.commit(); err != nil {
		s.logError("persisting outcome", err)
	}
	return true
}

// mapOutcomeStatus maps runner task-row statuses to run outcomes. The row
// status vocabulary is the nex-task one (succeeded/error/cancelled/stopped);
// "error" is the runner's failure status.
func mapOutcomeStatus(status string) string {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case OutcomeRunning, "queued", "pending", "starting":
		return OutcomeRunning
	case OutcomeSucceeded, "success", "completed", "done":
		return OutcomeSucceeded
	case OutcomeFailed, "error", "errored":
		return OutcomeFailed
	case OutcomeStopped, "cancelled", "canceled", "aborted", "interrupted":
		return OutcomeStopped
	}
	return ""
}

func maxInt(left, right int) int {
	if left > right {
		return left
	}
	return right
}

// logError is a nil-safe logger helper.
