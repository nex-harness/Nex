package automations

import (
	"errors"
	"time"
)

func (s *Service) runNow(automationID string, scope workspaceScope, now time.Time) (*Automation, *Run, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil, nil, errClosed
	}
	if err := s.store.load(); err != nil {
		return nil, nil, err
	}
	s.notifyInvoke()
	index := s.store.find(automationID)
	if index < 0 || !scope.matches(&s.store.doc.Automations[index]) {
		return nil, nil, ErrNotFound
	}
	if s.dispatch == nil {
		// A missing dispatcher is a configuration error; fail before claiming.
		return nil, nil, errors.New("Automation immediate dispatcher is unavailable.")
	}
	existing := s.store.doc.Automations[index]
	s.reapStaleClaimLocked(index, now)
	existing = s.store.doc.Automations[index]
	if existing.Running {
		return nil, nil, ErrDuplicateRun
	}
	runID := automationID + ":manual:" + uuidString()
	scheduled := now.UnixMilli()
	run := Run{
		RunID:          runID,
		AutomationID:   automationID,
		WorkspaceKey:   existing.WorkspaceKey,
		ScheduledAt:    &scheduled,
		Trigger:        TriggerManual,
		DispatchStatus: DispatchClaimed,
		Attempts:       1,
		CreatedAt:      scheduled,
		UpdatedAt:      scheduled,
	}
	previous := existing
	existing.Running = true
	existing.ClaimedAt = &scheduled
	existing.UpdatedAt = scheduled
	s.store.doc.Automations[index] = existing
	s.store.upsertRun(run)
	if err := s.store.commit(); err != nil {
		s.store.doc.Automations[index] = previous
		if runIndex := s.store.findRun(runID); runIndex >= 0 {
			s.store.doc.Runs = append(s.store.doc.Runs[:runIndex], s.store.doc.Runs[runIndex+1:]...)
		}
		return nil, nil, err
	}
	automation := s.store.doc.Automations[index]
	created := run
	return &automation, &created, nil
}

// reapStaleClaimLocked releases a claim older than claimStaleMS (crashed
// holder), like claimDue/runNow's zombie recovery.
func (s *Service) reapStaleClaimLocked(index int, now time.Time) {
	automation := s.store.doc.Automations[index]
	if !automation.Running || automation.ClaimedAt == nil {
		return
	}
	if now.UnixMilli()-*automation.ClaimedAt <= claimStaleMS {
		return
	}
	automation.Running = false
	automation.ClaimedAt = nil
	s.store.doc.Automations[index] = automation
}

// listRuns returns the automation's runs newest first.
func (s *Service) listRuns(automationID string, scope workspaceScope) []Run {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.store.load(); err != nil {
		s.logError("loading runs", err)
		return []Run{}
	}
	if index := s.store.find(automationID); index < 0 || !scope.matches(&s.store.doc.Automations[index]) {
		return []Run{}
	}
	indices := s.store.runsOf(automationID)
	out := make([]Run, 0, len(indices))
	for position := len(indices) - 1; position >= 0; position-- {
		out = append(out, s.store.doc.Runs[indices[position]])
	}
	return out
}

// deleteRun removes one run, restricted to the scope's automations.
func (s *Service) deleteRun(runID string, scope workspaceScope) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return errClosed
	}
	if err := s.store.load(); err != nil {
		return err
	}
	index := s.store.findRun(runID)
	if index < 0 {
		return nil
	}
	automationID := s.store.doc.Runs[index].AutomationID
	automationIndex := s.store.find(automationID)
	if automationIndex >= 0 && !scope.matches(&s.store.doc.Automations[automationIndex]) {
		return nil
	}
	previous := s.store.doc.Runs
	s.store.doc.Runs = append(append([]Run(nil), previous[:index]...), previous[index+1:]...)
	if err := s.store.commit(); err != nil {
		s.store.doc.Runs = previous
		return err
	}
	s.notifyInvoke()
	return nil
}

// call is the channel entry point.
