package automations

import (
	"encoding/json"
	"errors"
	"strings"
	"time"
)

// ErrNotFound is returned by id-scoped operations. The channel maps it to a
// PromiseError so the UI toast shows the message.
var ErrNotFound = errors.New("automation not found")

// ErrDuplicateRun is runAutomationNow's single-flight rejection; the channel
// turns it into {"status":"duplicate"}.
var ErrDuplicateRun = errors.New("manual run already in flight")

// errClosed is returned by mutations after Close: the document and the timer
// are released, so a write would race the closed service.
var errClosed = errors.New("automations: service is closed")

// createAutomation persists a new definition.
func (s *Service) createAutomation(params CreateParams, now time.Time) (*Automation, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil, errClosed
	}
	if err := s.store.load(); err != nil {
		return nil, err
	}
	if len(s.store.doc.Automations) >= CreateLimit {
		return nil, createLimitError()
	}
	loc := s.location
	resolved, err := resolveCreate(params, now, loc)
	if err != nil {
		return nil, err
	}
	identity := strings.TrimSpace(params.WorkspaceIdentity)
	created := Automation{
		AutomationID:      "automation-" + uuidString(),
		Title:             resolved.title,
		CronExpr:          resolved.cronExpr,
		Prompt:            resolved.prompt,
		ModelSelection:    resolved.modelSelection,
		Mode:              resolved.mode,
		WorkspaceKey:      workspaceKeyOf(params.WorkspacePath, identity),
		WorkspacePath:     params.WorkspacePath,
		WorkspaceIdentity: identity,
		TargetTaskID:      params.TargetTaskID,
		LocationKind:      "local",
		Timezone:          loc.String(),
		Recurring:         resolved.recurring,
		MaxRuns:           resolved.maxRuns,
		EndAt:             resolved.endAt,
		ScheduleRule:      resolved.scheduleRule,
		Enabled:           resolved.lifecycle == LifecycleActive,
		LifecycleStatus:   resolved.lifecycle,
		NextRunAt:         resolved.nextRunAt,
		DispatchStatus:    DispatchIdle,
		CreatedAt:         now.UnixMilli(),
		UpdatedAt:         now.UnixMilli(),
	}
	s.store.doc.Automations = append(s.store.doc.Automations, created)
	if err := s.store.commit(); err != nil {
		s.store.doc.Automations = s.store.doc.Automations[:len(s.store.doc.Automations)-1]
		return nil, err
	}
	s.notifyInvoke()
	index := s.store.find(created.AutomationID)
	result := s.store.doc.Automations[index]
	return &result, nil
}

// listAutomations returns every automation (createdAt desc) or one scope's.
func (s *Service) listAutomations(scope workspaceScope) []Automation {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.store.load(); err != nil {
		s.logError("loading automations", err)
		return []Automation{}
	}
	out := make([]Automation, 0, len(s.store.doc.Automations))
	for index := range s.store.doc.Automations {
		candidate := s.store.doc.Automations[index]
		if scope.matches(&candidate) {
			out = append(out, candidate)
		}
	}
	// Newest first, like `ORDER BY created_at DESC`.
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j].CreatedAt > out[j-1].CreatedAt; j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out
}

// getAutomation returns a copy inside the scope.
func (s *Service) getAutomation(automationID string, scope workspaceScope) (*Automation, bool) {
	index := s.store.find(automationID)
	if index < 0 || !scope.matches(&s.store.doc.Automations[index]) {
		return nil, false
	}
	candidate := s.store.doc.Automations[index]
	return &candidate, true
}

// updateAutomation ports AutomationService.update: cron edits re-anchor the
// schedule, explicit rules keep their anchor when only other fields change,
// carrier updates re-anchor, and recurring/maxRuns changes recompute the
// lifecycle from the scheduled-run count only.
func (s *Service) updateAutomation(automationID string, params UpdateParams, scope workspaceScope, now time.Time) (*Automation, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil, errClosed
	}
	if err := s.store.load(); err != nil {
		return nil, err
	}
	index := s.store.find(automationID)
	if index < 0 || !scope.matches(&s.store.doc.Automations[index]) {
		return nil, ErrNotFound
	}
	loc := normalizeLocation(s.store.doc.Automations[index].Timezone, s.location)
	existing := s.store.doc.Automations[index]

	directRule, err := decodeRule(params.ScheduleRule)
	if err != nil {
		return nil, err
	}
	if directRule != nil {
		if err := validScheduleRule(directRule); err != nil {
			return nil, err
		}
	}
	hasDirectRule := !isUndefined(params.ScheduleRule) && !isNull(params.ScheduleRule)
	maxRunsProvided := !isUndefined(params.MaxRuns)
	var maxRunsValue *float64
	if maxRunsProvided && !isNull(params.MaxRuns) {
		var value float64
		if err := json.Unmarshal(params.MaxRuns, &value); err != nil {
			return nil, errors.New("invalid maxRuns")
		}
		maxRunsValue = &value
	}
	if err := validateIntervalCarrier(params.IntervalUnit, params.Interval, hasDirectRule, nil, params.Recurring, maxRunsValue); err != nil {
		return nil, err
	}
	hasCarrier := params.IntervalUnit != "" && params.Interval != nil
	if !hasCarrier && maxRunsProvided && isNull(params.MaxRuns) && (params.Recurring == nil || !*params.Recurring) {
		return nil, invalidMaxRunsUpdate("")
	}
	nextRecurring := existing.Recurring
	if hasCarrier {
		nextRecurring = true
	} else if params.Recurring != nil {
		nextRecurring = *params.Recurring
	}
	if nextRecurring && maxRunsValue != nil {
		return nil, invalidMaxRunsUpdate("无限循环任务不能设置有限次数 maxRuns")
	}

	next := existing
	if params.Title != nil {
		if title := strings.TrimSpace(*params.Title); title != "" {
			next.Title = title
		}
	}
	if params.Prompt != nil {
		next.Prompt = *params.Prompt
	}
	if !isUndefined(params.ModelSelection) {
		selection, err := decodeSelection(params.ModelSelection)
		if err != nil {
			return nil, err
		}
		next.ModelSelection = selection
	}
	if !isUndefined(params.Mode) {
		if isNull(params.Mode) {
			next.Mode = nil
		} else {
			var mode string
			if err := json.Unmarshal(params.Mode, &mode); err != nil {
				return nil, errors.New("invalid mode")
			}
			next.Mode = &mode
		}
	}
	if params.ScheduleEditedByUser != nil {
		next.ScheduleEdited = *params.ScheduleEditedByUser
	}
	// maxRuns: recurring tasks drop the cap; finite tasks keep/clear it.
	if nextRecurring {
		next.MaxRuns = nil
	} else if maxRunsProvided {
		if isNull(params.MaxRuns) {
			next.MaxRuns = nil
		} else if maxRunsValue != nil {
			value := int(*maxRunsValue)
			next.MaxRuns = &value
		}
	}

	cronChanged := false
	if params.CronExpr != nil {
		if _, err := parseCronExpr(*params.CronExpr); err != nil {
			return nil, invalidCronExpr(*params.CronExpr)
		}
		cronChanged = *params.CronExpr != existing.CronExpr
		next.CronExpr = *params.CronExpr
	}

	// Schedule rule resolution (automationService.update steps).
	var effectiveRule *ScheduleRule
	var ruleChanged bool
	switch {
	case hasCarrier:
		effectiveRule, err = buildIntervalScheduleRule(params.IntervalUnit, *params.Interval, next.CronExpr, now, loc)
		if err != nil {
			return nil, err
		}
		ruleChanged = true
	case hasDirectRule:
		anchored := *directRule
		if existing.ScheduleRule != nil && scheduleRuleDefinition(existing.ScheduleRule) == scheduleRuleDefinition(directRule) {
			anchored.AnchorAt = existing.ScheduleRule.AnchorAt
		} else {
			anchored.AnchorAt = float64(now.UnixMilli())
		}
		effectiveRule = &anchored
		ruleChanged = true
	case !isUndefined(params.ScheduleRule) && isNull(params.ScheduleRule):
		effectiveRule = nil
		ruleChanged = true
	case cronChanged:
		effectiveRule = inferMinuteIntervalScheduleRule(next.CronExpr, now, loc)
		ruleChanged = true
	default:
		effectiveRule = existing.ScheduleRule
	}
	next.ScheduleRule = effectiveRule
	next.Recurring = nextRecurring

	endAtProvided := !isUndefined(params.EndAt)
	if endAtProvided {
		if isNull(params.EndAt) {
			next.EndAt = nil
		} else {
			var value float64
			if err := json.Unmarshal(params.EndAt, &value); err != nil {
				return nil, errors.New("invalid endAt")
			}
			millis := int64(value)
			next.EndAt = &millis
		}
	}

	nextRunAtSet := false
	if ruleChanged || cronChanged || endAtProvided {
		if value, ok := nextRunAt(&next, now, loc); ok {
			millis := value.UnixMilli()
			next.NextRunAt = &millis
		} else {
			next.NextRunAt = nil
		}
		nextRunAtSet = true
		next.DispatchAttempts = 0
		next.RetryAt = nil
		next.DispatchStatus = DispatchIdle
	}

	// endAt boundary: a next run beyond the deadline finishes the plan.
	mayRun := nextLifecycleCanRun(next.LifecycleStatus)
	if next.EndAt != nil && mayRun && (next.NextRunAt == nil || *next.NextRunAt > *next.EndAt) {
		next.NextRunAt = nil
		next.LifecycleStatus = LifecycleCompleted
		next.Enabled = false
	} else if endAtProvided && (existing.LifecycleStatus == LifecycleCompleted || existing.LifecycleStatus == LifecycleFailed) {
		next.LifecycleStatus = LifecycleActive
		next.Enabled = true
	}

	recurringTouched := hasCarrier || params.Recurring != nil || maxRunsProvided
	if recurringTouched {
		scheduled := existing.ScheduledRunCount
		limit := 1
		if next.MaxRuns != nil {
			limit = *next.MaxRuns
		}
		reachedMax := !next.Recurring && scheduled >= limit
		if reachedMax {
			next.LifecycleStatus = LifecycleCompleted
			next.Enabled = false
		} else if existing.LifecycleStatus == LifecycleCompleted || existing.LifecycleStatus == LifecycleFailed {
			next.LifecycleStatus = LifecycleActive
			next.Enabled = true
			if !nextRunAtSet {
				if value, ok := nextRunAt(&next, now, loc); ok {
					millis := value.UnixMilli()
					next.NextRunAt = &millis
				} else {
					next.NextRunAt = nil
				}
			}
		}
	}

	next.UpdatedAt = now.UnixMilli()
	s.store.doc.Automations[index] = next
	if err := s.store.commit(); err != nil {
		s.store.doc.Automations[index] = existing
		return nil, err
	}
	s.notifyInvoke()
	result := s.store.doc.Automations[index]
	return &result, nil
}

func (s *Service) deleteAutomation(automationID string, scope workspaceScope) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return false, errClosed
	}
	if err := s.store.load(); err != nil {
		return false, err
	}
	index := s.store.find(automationID)
	if index < 0 || !scope.matches(&s.store.doc.Automations[index]) {
		return false, nil
	}
	previousAutomations := append([]Automation(nil), s.store.doc.Automations...)
	previousRuns := append([]Run(nil), s.store.doc.Runs...)
	s.store.doc.Automations = append(s.store.doc.Automations[:index], s.store.doc.Automations[index+1:]...)
	s.store.removeRunsOf(automationID)
	if err := s.store.commit(); err != nil {
		s.store.doc.Automations = previousAutomations
		s.store.doc.Runs = previousRuns
		return false, err
	}
	s.notifyInvoke()
	return true, nil
}

// setEnabled pauses (enabled=false, lifecycle=paused) or resumes (active),
// keeping nextRunAt and runCount.
func (s *Service) setEnabled(automationID string, enabled bool, scope workspaceScope, now time.Time) error {
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
	next := existing
	next.Enabled = enabled
	if enabled {
		next.LifecycleStatus = LifecycleActive
	} else {
		next.LifecycleStatus = LifecyclePaused
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
