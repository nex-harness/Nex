package projection

// materializeCommandRowActions ports materializeCommandRowActions: it derives the
// per-row action flags (canEdit / canRetry / canFork) from the projected snapshot
// and emits row.upserted for rows whose actions changed. It runs once per event
// after the event's own deltas.
func (p *Projection) materializeCommandRowActions(reduced []Delta) []Delta {
	prospective := applyConversationDeltas(p.snapshot, reduced)
	rows, _ := prospective.get("rows").(*obj)
	window, _ := rows.get("window").([]any)

	rowByID := map[int64]*obj{}
	latestAssistantByTurn := map[string]int64{}
	for _, r := range window {
		row, ok := r.(*obj)
		if !ok {
			continue
		}
		rowByID[rowIDOf(row)] = row
		if kindOf(row) == "assistantText" {
			turn := turnIDOf(row)
			if current, ok := latestAssistantByTurn[turn]; !ok || rowIDOf(row) > current {
				latestAssistantByTurn[turn] = rowIDOf(row)
			}
		}
	}

	control, _ := prospective.get("control").(*obj)
	activeWorks, _ := control.get("activeWorks").([]any)
	compactActive := worksHaveKind(activeWorks, "compact")
	completionBlockingActive := len(activeWorks) > 0

	var latestEditable *obj
	var latestAssistant *obj
	for i := len(window) - 1; i >= 0; i-- {
		row, _ := window[i].(*obj)
		if row == nil {
			continue
		}
		if latestEditable == nil && !compactActive && kindOf(row) == "userInput" && strOf(row.get("origin")) == "realUser" {
			latestEditable = row
		}
		if latestAssistant == nil && kindOf(row) == "assistantText" {
			latestAssistant = row
		}
		if latestEditable != nil && latestAssistant != nil {
			break
		}
	}

	var latestRetryable *obj
	if !completionBlockingActive && latestAssistant != nil &&
		stateOf(latestAssistant) == "complete" {
		if _, ok := p.messageIDByRowID[rowIDOf(latestAssistant)]; ok {
			headerID, hasHeader := p.turnHeaderRowIDByTurnID[turnIDOf(latestAssistant)]
			header := rowByID[headerID]
			if hasHeader && header != nil && kindOf(header) == "turnHeader" && stateOf(header) != "running" {
				var canonicalUserEntityID string
				for _, r := range window {
					row, _ := r.(*obj)
					if row != nil && turnIDOf(row) == turnIDOf(latestAssistant) &&
						kindOf(row) == "userInput" && strOf(row.get("origin")) == "realUser" {
						canonicalUserEntityID = p.entityIDByRowID[rowIDOf(row)]
						break
					}
				}
				if canonicalUserEntityID != "" && p.editTargetExists(canonicalUserEntityID) {
					latestRetryable = latestAssistant
				}
			}
		}
	}

	var latestEditableRowID *int64
	if latestEditable != nil {
		entityID := p.entityIDByRowID[rowIDOf(latestEditable)]
		if _, ok := p.messageIDByRowID[rowIDOf(latestEditable)]; ok && p.editTargetExists(entityID) {
			id := rowIDOf(latestEditable)
			latestEditableRowID = &id
		}
	}

	deltas := []Delta{}
	for _, r := range window {
		row, ok := r.(*obj)
		if !ok {
			continue
		}
		switch kindOf(row) {
		case "turnHeader", "userInput", "assistantText":
		default:
			continue
		}
		nextActions := shallowClone(actionsOrEmpty(row.get("actions")))
		switch kindOf(row) {
		case "turnHeader":
			canRewindFiles := !completionBlockingActive &&
				len(pendingInteractionsOf(prospective)) == 0 &&
				stateOf(row) != "running" &&
				fileChangesStateIsActive(row)
			if canRewindFiles {
				nextActions.put("canRewindFiles", true)
			} else {
				nextActions.del("canRewindFiles")
			}
		case "userInput":
			if latestEditableRowID != nil && rowIDOf(row) == *latestEditableRowID {
				nextActions.put("canEdit", true)
				nextActions.put("editDisposition", "rewind")
			} else {
				nextActions.del("canEdit")
				nextActions.del("editDisposition")
			}
		case "assistantText":
			if latestRetryable != nil && rowIDOf(row) == rowIDOf(latestRetryable) {
				nextActions.put("canRetry", true)
			} else {
				nextActions.del("canRetry")
			}
			headerID, hasHeader := p.turnHeaderRowIDByTurnID[turnIDOf(row)]
			header := rowByID[headerID]
			canFork := !compactActive && stateOf(row) == "complete" &&
				hasHeader && header != nil && kindOf(header) == "turnHeader" &&
				stateOf(header) == "completedSuccess" &&
				latestAssistantByTurn[turnIDOf(row)] == rowIDOf(row)
			if _, ok := p.messageIDByRowID[rowIDOf(row)]; !ok {
				canFork = false
			}
			if canFork {
				nextActions.put("canFork", true)
			} else {
				nextActions.del("canFork")
			}
		}
		actions := (*obj)(nil)
		if len(nextActions.keys) > 0 {
			actions = nextActions
		}
		if actionsEqual(actions, row.get("actions")) {
			continue
		}
		next := shallowClone(row)
		if actions == nil {
			next.del("actions")
		} else {
			next.put("actions", actions)
		}
		deltas = append(deltas, newRowUpserted(next))
	}
	return deltas
}

func pendingInteractionsOf(snapshot *obj) []any {
	items, _ := snapshot.get("pendingInteractions").([]any)
	return items
}

func fileChangesStateIsActive(row *obj) bool {
	fc, ok := row.get("fileChanges").(*obj)
	if !ok || fc == nil {
		return false
	}
	return strOf(fc.get("state")) == "active"
}

func (p *Projection) editTargetExists(entityID string) bool {
	_, ok := p.editableEntityIDs[entityID]
	return ok
}

func actionsOrEmpty(v any) *obj {
	if o, ok := v.(*obj); ok && o != nil {
		return o
	}
	return newObj()
}

func actionsEqual(a *obj, b any) bool {
	bObj, ok := b.(*obj)
	if !ok || bObj == nil {
		return a == nil
	}
	if a == nil || len(a.keys) != len(bObj.keys) {
		return false
	}
	for i, key := range a.keys {
		if bObj.keys[i] != key {
			return false
		}
		av, _ := marshalValue(a.vals[i])
		bv, _ := marshalValue(bObj.vals[i])
		if string(av) != string(bv) {
			return false
		}
	}
	return true
}

// mergeActions returns a new action object with base's keys, then extra's keys
// applied (put updates in place, keeping base order).
func mergeActions(base any, extra *obj) *obj {
	result := shallowClone(actionsOrEmpty(base))
	for i, key := range extra.keys {
		result.put(key, extra.vals[i])
	}
	return result
}
