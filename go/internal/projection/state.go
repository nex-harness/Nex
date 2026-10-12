package projection

import "encoding/json"

// This file ports projection-state.ts: the initial snapshot factory and the
// pure availability/inputRouting derivations, plus the revision-bump rule.

// availabilityContext mirrors the TS AvailabilityContext. compacting and
// goalVerifying are not phases (the phase enum is closed); they derive from
// activeWorks and the goal status.
type availabilityContext struct {
	phase         string
	goalStatus    string // "" means null
	compacting    bool
	goalVerifying bool
	queueLength   int
	autoDrain     bool
}

func allowed() *obj { return newObj().set("allowed", true) }

func denied(reasonCode string) *obj {
	return newObj().set("allowed", false).set("reasonCode", reasonCode)
}

// computeAvailability ports computeAvailability (projection-state.ts), aligned
// with packages/formal-proof/src/model.ts. reasonCode = product-protocol guard id.
func computeAvailability(c availabilityContext) *obj {
	running := c.phase == "running" || c.phase == "prewarming"

	var compact *obj
	switch {
	case c.compacting:
		compact = denied("compactOperationLock")
	case c.phase == "draft":
		compact = denied("idleCannotCompact")
	default:
		compact = allowed()
	}

	fork := allowed()
	switch {
	case c.compacting:
		fork = denied("compactOperationLock")
	case c.phase == "draft":
		fork = denied("forkTargetNotStable")
	}

	var sendQueuedNow *obj
	switch {
	case c.compacting:
		sendQueuedNow = denied("compactOperationLock")
	case running:
		sendQueuedNow = allowed()
	default:
		sendQueuedNow = denied("sendQueuedNowRequiresRunning")
	}

	var pauseGoal *obj
	switch c.goalStatus {
	case "active", "verifying", "notSatisfied":
		pauseGoal = allowed()
	case "":
		pauseGoal = denied("noGoalToPause")
	default:
		pauseGoal = denied("goalNotActive")
	}

	var resumeGoal *obj
	switch c.goalStatus {
	case "paused":
		resumeGoal = allowed()
	case "":
		resumeGoal = denied("noGoalToResume")
	default:
		resumeGoal = denied("goalNotPaused")
	}

	return newObj().
		set("fork", fork).
		set("compact", compact).
		set("switchModelConfig", allowed()).
		set("setFollowupMode", allowed()).
		set("queueEdit", allowed()).
		set("sendQueuedNow", sendQueuedNow).
		set("pauseGoal", pauseGoal).
		set("resumeGoal", resumeGoal)
}

// computeInputRouting ports computeInputRouting (projection-state.ts).
func computeInputRouting(c availabilityContext, followupMode string) *obj {
	if c.compacting {
		return newObj().set("mode", "enqueue").set("reasonCode", "compactingAcceptsFutureInput")
	}
	if c.goalVerifying {
		return newObj().set("mode", "enqueue").set("reasonCode", "goalVerifierAcceptsFutureInput")
	}
	if c.phase == "running" || c.phase == "prewarming" {
		mode := "enqueue"
		if followupMode == "guide" {
			mode = "guide"
		}
		return newObj().set("mode", mode)
	}
	completed := c.phase == "completedSuccess" || c.phase == "completedInterrupted"
	if completed && c.queueLength > 0 && !c.autoDrain {
		return newObj().set("mode", "choice").set("reasonCode", "heldQueueInputRequiresChoice")
	}
	return newObj().set("mode", "startNow")
}

// revisionBearingKeys ports REVISION_BEARING_PATCH_KEYS: rows always bump, and a
// state.updated bumps only when one of these keys is present. workflowRuns is
// deliberately excluded (self-revisioning, high-frequency).
var revisionBearingKeys = []string{
	"control", "availability", "inputRouting", "meta", "config",
	"queue", "pendingInteractions", "backgroundWorks", "subagents", "goal", "plan",
}

// createInitialConversationSnapshot ports createInitialConversationSnapshot.
func createInitialConversationSnapshot(sessionID, logEpoch string) *obj {
	ctx := availabilityContext{phase: "draft", goalStatus: "", autoDrain: true}
	return newObj().
		set("protocolVersion", 1).
		set("sessionId", sessionID).
		set("logEpoch", logEpoch).
		set("seq", 0).
		set("revision", 0).
		set("control", newObj().
			set("phase", "draft").
			set("sessionEnded", false).
			set("canStop", false).
			set("stopState", "idle").
			set("stopTargetKind", "unknown").
			set("activeWorks", []any{}).
			set("lastError", nil).
			set("apiRetry", nil)).
		set("availability", computeAvailability(ctx)).
		set("inputRouting", computeInputRouting(ctx, "queue")).
		set("meta", newObj().set("title", "").set("titleSource", "default")).
		set("config", newObj().
			set("provider", "").
			set("model", "").
			set("thought", "").
			set("thoughtLevels", []any{}).
			set("followupMode", "queue").
			set("mode", "build")).
		set("modelTransition", nil).
		set("usage", newObj().
			set("contextWindow", nil).
			set("cumulative", newObj().
				set("inputTokens", 0).
				set("outputTokens", 0).
				set("cacheReadTokens", 0).
				set("cacheWriteTokens", 0))).
		set("queue", newObj().set("items", []any{}).set("autoDrain", true)).
		set("pendingInteractions", []any{}).
		set("pendingCommands", []any{}).
		set("backgroundWorks", []any{}).
		set("subagents", newObj().
			set("revision", 0).
			set("childSessionIds", []any{}).
			set("running", []any{}).
			set("endedTotal", 0)).
		set("goal", nil).
		set("plan", nil).
		set("workspaceHookAdmission", nil).
		set("rows", newObj().
			set("window", []any{}).
			set("totalCount", 0).
			set("firstRowId", nil))
}

// controlObj returns the snapshot's control sub-object.
func (p *Projection) controlObj() *obj { return p.snapshot.get("control").(*obj) }

// phase reads control.phase.
func (p *Projection) phase() string {
	v, _ := p.controlObj().get("phase").(string)
	return v
}

func (p *Projection) isRunning() bool {
	phase := p.phase()
	return phase == "running" || phase == "prewarming"
}

// goalStatus reads snapshot.goal.status, or "" when goal is null.
func (p *Projection) goalStatus() string {
	goal, ok := p.snapshot.get("goal").(*obj)
	if !ok || goal == nil {
		return ""
	}
	status, _ := goal.get("status").(string)
	return status
}

// deriveContext ports deriveContext: it reads the current snapshot (no overrides)
// to feed availability/inputRouting.
func (p *Projection) deriveContext() availabilityContext {
	return p.contextWith(nil, nil)
}

// contextWith ports deriveContext({goal, queue}) with optional overrides. A nil
// override means "use the snapshot value".
func (p *Projection) contextWith(goalOverride *obj, queueOverride *obj, goalOverrideSet ...bool) availabilityContext {
	context := availabilityContext{phase: p.phase()}

	goal := p.snapshot.get("goal")
	if len(goalOverrideSet) > 0 && goalOverrideSet[0] {
		goal = goalOverride
	}
	if g, ok := goal.(*obj); ok && g != nil {
		context.goalStatus, _ = g.get("status").(string)
	}

	queue := p.snapshot.get("queue")
	if queueOverride != nil {
		queue = queueOverride
	}
	if q, ok := queue.(*obj); ok && q != nil {
		if items, ok := q.get("items").([]any); ok {
			context.queueLength = len(items)
		}
		context.autoDrain, _ = q.get("autoDrain").(bool)
	}

	context.compacting = p.activeWorksHasKind("compact")
	context.goalVerifying = p.activeWorksHasKind("goalVerifier")
	return context
}

func (p *Projection) activeWorksHasKind(kind string) bool {
	works, _ := p.controlObj().get("activeWorks").([]any)
	for _, w := range works {
		work, ok := w.(*obj)
		if !ok {
			continue
		}
		if k, _ := work.get("kind").(string); k == kind {
			return true
		}
	}
	return false
}

// followupMode reads snapshot.config.followupMode.
func (p *Projection) followupMode() string {
	config, _ := p.snapshot.get("config").(*obj)
	if config == nil {
		return "queue"
	}
	mode, _ := config.get("followupMode").(string)
	return mode
}

// deltaBumpsRevision ports deltaBumpsRevision (projection-state.ts).
func deltaBumpsRevision(op string, patch *obj) bool {
	switch op {
	case "row.appended", "row.upserted", "row.removed":
		return true
	case "row.delta":
		return false
	case "state.updated":
		for _, key := range revisionBearingKeys {
			if patch.has(key) {
				return true
			}
		}
		return false
	default:
		// workflowRun.updated / workflowRun.removed are exempt, like a whole-key
		// workflowRuns patch.
		return false
	}
}

// rawMessage passes a decoded JSON value through untouched.
func rawMessage(v any) json.RawMessage {
	data, err := json.Marshal(v)
	if err != nil {
		return json.RawMessage("null")
	}
	return data
}
