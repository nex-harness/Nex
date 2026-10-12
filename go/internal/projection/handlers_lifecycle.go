package projection

import "encoding/json"

// controlPatch ports controlPatch(control, goal?, queue?). Overrides of nil mean
// "keep the snapshot value"; a non-nil empty marker is not possible for goal
// because goal may legitimately be replaced by null — callers pass an explicit
// flag through controlPatchGoal.

// controlPatch builds a StatePatch. goalSet/queueSet tell whether the caller
// overrode that key (so a null goal replacement is distinguishable from "keep").
func (p *Projection) controlPatch(overrides *obj, goal *obj, queue *obj, goalSet bool, queueSet bool) *obj {
	current := p.controlObj()
	next := shallowClone(current)
	for i, key := range overrides.keys {
		next.put(key, overrides.vals[i])
	}
	nextGoal := p.snapshot.get("goal")
	if goalSet {
		nextGoal = goal
	}
	nextQueue := p.snapshot.get("queue")
	if queueSet {
		nextQueue = queue
	}

	activeWorks, _ := next.get("activeWorks").([]any)
	context := availabilityContext{
		phase:         strOf(next.get("phase")),
		goalVerifying: worksHaveKind(activeWorks, "goalVerifier"),
		compacting:    worksHaveKind(activeWorks, "compact"),
	}
	if g, ok := nextGoal.(*obj); ok && g != nil {
		context.goalStatus = strOf(g.get("status"))
	}
	if q, ok := nextQueue.(*obj); ok && q != nil {
		if items, ok := q.get("items").([]any); ok {
			context.queueLength = len(items)
		}
		context.autoDrain = boolOf(q.get("autoDrain"))
	}

	patch := newObj().set("control", next)
	if goalSet {
		patch.set("goal", goal)
	}
	if queueSet {
		patch.set("queue", queue)
	}
	patch.set("availability", computeAvailability(context))
	patch.set("inputRouting", computeInputRouting(context, p.followupMode()))
	return patch
}

func worksHaveKind(works []any, kind string) bool {
	for _, w := range works {
		if work, ok := w.(*obj); ok && strOf(work.get("kind")) == kind {
			return true
		}
	}
	return false
}

// registerCanonicalUserRowTarget records the user row's canonical identity side
// tables for command targets (messageIdByRowId / entityIdByRowId). Only rows with
// a persisted transcript message id become canonical command targets.
func (p *Projection) registerCanonicalUserRowTarget(row *obj, fact userIntent) {
	if !fact.hasTranscriptID {
		return
	}
	p.messageIDByRowID[rowIDOf(row)] = fact.transcriptMessageID
	p.editableEntityIDs[fact.entityID] = struct{}{}
}

// closeTextRow ports closeTextRow.
func (p *Projection) closeTextRow(state string) []Delta {
	if p.streamingTextRowID == nil {
		return nil
	}
	row := p.findRow(*p.streamingTextRowID)
	rowID := *p.streamingTextRowID
	p.streamingTextRowID = nil
	if row == nil || kindOf(row) != "assistantText" {
		_ = rowID
		return nil
	}
	return []Delta{newRowUpserted(shallowClone(row).put("state", state))}
}

// closeReasoningRow ports closeReasoningRow.
func (p *Projection) closeReasoningRow(state string) []Delta {
	if p.streamingReasoningRowID == nil {
		return nil
	}
	row := p.findRow(*p.streamingReasoningRowID)
	p.streamingReasoningRowID = nil
	if row == nil || kindOf(row) != "reasoning" {
		return nil
	}
	return []Delta{newRowUpserted(shallowClone(row).put("state", state))}
}

func (p *Projection) closeStreamingRows(state string) []Delta {
	return append(p.closeTextRow(state), p.closeReasoningRow(state)...)
}

// closeOpenToolRows ports closeOpenToolRows for the turn-terminal path.
func (p *Projection) closeOpenToolRows(event Event, status string) []Delta {
	rows := p.openToolRowsSorted()
	if len(rows) == 0 {
		return nil
	}
	deltas := []Delta{}
	closed := map[string]struct{}{}
	for _, row := range rows {
		id := toolCallIDOf(row)
		next := shallowClone(row).
			put("status", status).
			put("inputText", strOf(row.get("inputText"))).
			put("endedAt", event.TimestampMs)
		next.del("approvalInteractionId")
		if status == "error" {
			next.put("error", newObj().
				set("code", "fault.runtime.toolLifecycleIncomplete").
				set("message", "Tool call ended without a terminal event."))
		}
		closed[id] = struct{}{}
		deltas = append(deltas, newRowUpserted(next))
	}
	// Drop pending interactions that anchor to a closed tool call.
	pending, _ := p.snapshot.get("pendingInteractions").([]any)
	kept := make([]any, 0, len(pending))
	for _, item := range pending {
		interaction, ok := item.(*obj)
		if ok {
			payload, _ := interaction.get("payload").(*obj)
			if payload != nil {
				toolCallID := strOf(payload.get("toolCallId"))
				if _, isClosed := closed[toolCallID]; isClosed {
					continue
				}
			}
		}
		kept = append(kept, item)
	}
	if len(kept) != len(pending) {
		deltas = append(deltas, newStateUpdated(newObj().set("pendingInteractions", kept)))
	}
	return deltas
}

// upsertTurnHeader ports upsertTurnHeader.
func (p *Projection) upsertTurnHeader(event Event, state string, activeMs *int64, historyRoundCount *int64) []Delta {
	row := p.turnHeaderForEvent(event)
	if row == nil {
		return nil
	}
	next := shallowClone(row).put("state", state).put("endedAt", event.TimestampMs)
	if activeMs != nil {
		next.put("activeMs", *activeMs)
	}
	if historyRoundCount != nil {
		next.put("historyRoundCount", *historyRoundCount)
	}
	return []Delta{newRowUpserted(next)}
}

// onTurnComplete ports onTurnComplete (non-controlOnly path).
func (p *Projection) onTurnComplete(event Event, m map[string]json.RawMessage) []Delta {
	p.outputContinuationRowID = nil
	resultType, _ := payloadString(m, "resultType")
	headerState := mapTurnResultToHeaderState(resultType)
	header := p.turnHeaderForEvent(event)
	if header != nil && strOf(header.get("executionKind")) == "controlOnly" {
		deltas := p.upsertTurnHeader(event, headerState, nil, intPtr(m, "historyRoundCount"))
		deltas = append(deltas, p.leaveDraftAfterControlOnlyTurn(resultType)...)
		p.currentTurnID = ""
		p.hasCurrentTurn = false
		p.currentTurnStartedModelOnly = false
		return deltas
	}

	phase := "error"
	switch resultType {
	case "success":
		phase = "completedSuccess"
	case "cancelled":
		phase = "completedInterrupted"
	}
	streamClose := "interrupted"
	if resultType == "success" {
		streamClose = "complete"
	}
	toolStatus := "error"
	if resultType == "cancelled" {
		toolStatus = "cancelled"
	}

	deltas := []Delta{}
	deltas = append(deltas, p.closeStreamingRows(streamClose)...)
	deltas = append(deltas, p.closeOpenToolRows(event, toolStatus)...)
	deltas = append(deltas, p.upsertTurnHeader(event, headerState, p.activeMsForCompletion(event, m), intPtr(m, "historyRoundCount"))...)
	if resultType == "success" {
		deltas = append(deltas, p.markStableForkAssistant(event)...)
	}
	deltas = append(deltas, newStateUpdated(p.controlPatch(newObj().
		set("phase", phase).
		set("sessionEnded", phase != "error").
		set("canStop", false).
		set("stopState", "idle").
		set("stopTargetKind", "unknown").
		set("activeWorks", []any{}).
		set("apiRetry", nil), nil, nil, false, false)))
	p.currentTurnID = ""
	p.hasCurrentTurn = false
	p.currentTurnStartedModelOnly = false
	return deltas
}

// leaveDraftAfterControlOnlyTurn ports leaveDraftAfterControlOnlyTurn.
func (p *Projection) leaveDraftAfterControlOnlyTurn(resultType string) []Delta {
	if p.phase() != "draft" {
		return nil
	}
	phase := "error"
	if resultType == "success" {
		phase = "completedSuccess"
	} else if resultType == "cancelled" {
		phase = "completedInterrupted"
	}
	return []Delta{newStateUpdated(p.controlPatch(newObj().
		set("phase", phase).
		set("sessionEnded", phase != "error").
		set("canStop", false).
		set("stopState", "idle").
		set("stopTargetKind", "unknown").
		set("activeWorks", []any{}), nil, nil, false, false))}
}

// activeMsForCompletion ports activeMsForCompletion: the runtime duration if
// present, else the elapsed time since the turn started.
func (p *Projection) activeMsForCompletion(event Event, m map[string]json.RawMessage) *int64 {
	if n, ok := payloadInt(m, "duration"); ok {
		return &n
	}
	_ = event
	return nil
}

// markStableForkAssistant ports markStableForkAssistant.
func (p *Projection) markStableForkAssistant(event Event) []Delta {
	turnID := p.turnIDOfEvent(event)
	window := p.rowsWindow()
	start := 0
	if headerID, ok := p.turnHeaderRowIDByTurnID[turnID]; ok {
		if index := p.rowIndexOf(headerID); index >= 0 {
			start = index + 1
		}
	}
	var row *obj
	for i := len(window) - 1; i >= start; i-- {
		candidate, ok := window[i].(*obj)
		if !ok || kindOf(candidate) != "assistantText" || turnIDOf(candidate) != turnID {
			continue
		}
		row = candidate
		break
	}
	if row == nil {
		return nil
	}
	if _, ok := p.messageIDByRowID[rowIDOf(row)]; !ok {
		return nil
	}
	next := shallowClone(row).put("state", "complete")
	next.put("actions", mergeActions(row.get("actions"), newObj().set("canFork", true)))
	return []Delta{newRowUpserted(next)}
}

func intPtr(m map[string]json.RawMessage, key string) *int64 {
	if n, ok := payloadInt(m, key); ok {
		return &n
	}
	return nil
}

// mapTurnResultToHeaderState ports mapTurnResultToHeaderState.
func mapTurnResultToHeaderState(resultType string) string {
	switch resultType {
	case "success":
		return "completedSuccess"
	case "cancelled":
		return "completedInterrupted"
	default:
		return "failed"
	}
}
