package taskadapter

import (
	"sync"
)

// owner.go — the LOCAL ownership half: which input run owns a task right now, and the
// owner-command check that guards every queue mutation.
//
// This is the leaf of the cross-host decision. taskRealtimeBus refuses a stale
// cross-host command before it leaves the bus; this refuses a stale local command
// that arrives on this host (a relay that forwards a command it could not itself
// refuse, or a direct desktop call carrying an ownerRunId). Both raise the same code,
// so the UI's error branch is the same either way.

// ownerState is the per-task active-run bookkeeping: activePromptInputIds plus the
// task targets this host has loaded.
type ownerState struct {
	mu sync.Mutex
	// activeInputIDs is activePromptInputIds: taskKey → the inputId (traceId) of the
	// run currently owning that task. Its ABSENCE is load-bearing — it is what makes
	// NO_ACTIVE_TASK_OWNER distinguishable from a matching run.
	activeInputIDs map[string]string
	// targets is taskTargets: taskId → the target this host knows the task by.
	targets map[string]Target
}

func newOwnerState() *ownerState {
	return &ownerState{activeInputIDs: map[string]string{}, targets: map[string]Target{}}
}

// ClaimRun ports `activePromptInputIds.set(taskKey, inputId)`.
func (o *ownerState) ClaimRun(target Target, inputID string) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.activeInputIDs[target.TaskKey()] = inputID
}

// ReleaseRun ports `activePromptInputIds.delete(taskKey)`.
func (o *ownerState) ReleaseRun(target Target) {
	o.mu.Lock()
	defer o.mu.Unlock()
	delete(o.activeInputIDs, target.TaskKey())
}

// ActiveInput returns the task's current active input run.
func (o *ownerState) ActiveInput(target Target) (string, bool) {
	o.mu.Lock()
	defer o.mu.Unlock()
	inputID, ok := o.activeInputIDs[target.TaskKey()]
	return inputID, ok
}

// HasActiveRun reports whether a run currently owns the task.
func (o *ownerState) HasActiveRun(target Target) bool {
	_, ok := o.ActiveInput(target)
	return ok
}

// RememberTarget ports rememberTaskTarget: it records the target and keeps the STICKY
// cronAutomationId when the incoming params omit it.
//
// The stickiness is not cosmetic. cronAutomationId is the session's durable
// attribution mark shown in the UI and used by task grouping; `resumeTask` and
// `onDynamicTaskEvent` do not carry it, so a plain overwrite would silently strip a
// cron task's identity the first time it was resumed. Only an explicit set or clear
// (through the indexed-task path) may change it.
func (o *ownerState) RememberTarget(target Target) {
	o.mu.Lock()
	defer o.mu.Unlock()
	existing := o.targets[target.TaskID]
	if target.CronAutomationID == "" {
		target.CronAutomationID = existing.CronAutomationID
	}
	o.targets[target.TaskID] = target
}

// Target ports getTaskTarget. An unknown task is a refusal with its own code, not an
// empty target: the caller must not fall through and drive a session this host has
// never loaded.
func (o *ownerState) Target(taskID string) (Target, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	target, ok := o.targets[taskID]
	if !ok {
		return Target{}, faultTargetNotLoaded(taskID)
	}
	return target, nil
}

// AssertCurrentOwnerRun ports assertCurrentOwnerRun (guards A1–A3).
//
// Three cases, in order, and all three are load-bearing:
//
//   - no ownerRunId: the desktop-continuous leg. It is not owner-checked at all (A1);
//     refusing it would break the desktop's own UI, which has no run id to present.
//   - no active run: the run already reached its terminal. Writing the command into
//     the host queue anyway would send a stale input on a shared desktop host (A2).
//   - a different active run: the command belongs to an older turn. It must not
//     mutate the CURRENT turn's queue (A3).
func (o *ownerState) AssertCurrentOwnerRun(target Target, ownerRunID string) error {
	if ownerRunID == "" {
		return nil
	}
	key := target.TaskKey()
	o.mu.Lock()
	activeRunID, ok := o.activeInputIDs[key]
	o.mu.Unlock()
	if !ok {
		return &OwnerError{Code: CodeNoActiveTaskOwner, Message: "No active task owner."}
	}
	if activeRunID != ownerRunID {
		return &OwnerError{Code: CodeStaleTaskOwnerCommand, Message: "Stale task owner command."}
	}
	return nil
}
