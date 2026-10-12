package projection

import "encoding/json"

// This file ports the session/turn lifecycle handlers:
// onSessionCreated, onSessionTitleUpdated, onTurnStarted, onTurnComplete.

func (p *Projection) onSessionCreated(event Event, m map[string]json.RawMessage) []Delta {
	if n, ok := payloadInt(m, "contextWindow"); ok {
		p.contextMaxTokens = &n
	} else {
		p.contextMaxTokens = nil
	}
	// draft semantics: the session exists but has no row; phase stays draft.
	return nil
}

// onSessionTitleUpdated ports onSessionTitleUpdated. custom outranks generated
// (a user rename is never overwritten by an auto title).
func (p *Projection) onSessionTitleUpdated(event Event, m map[string]json.RawMessage) []Delta {
	title, _ := payloadString(m, "title")
	sourceRaw, _ := payloadString(m, "source")
	source := "generated"
	switch sourceRaw {
	case "custom":
		source = "custom"
	case "default":
		source = "default"
	}
	meta := p.snapshot.get("meta").(*obj)
	prevTitle := strOf(meta.get("title"))
	prevSource := strOf(meta.get("titleSource"))
	if prevSource == "custom" && source == "generated" {
		return nil
	}
	if prevTitle == title && prevSource == source {
		return nil
	}
	return []Delta{newStateUpdated(newObj().set("meta", newObj().
		set("title", title).
		set("titleSource", source)))}
}

// userIntent is the canonical fact normalizeTurnStarted produces (subset used by
// the projection).
type userIntent struct {
	transcriptMessageID string
	hasTranscriptID     bool
	productTurnID       string
	runtimeTurnID       string
	entityID            string
	visibility          string // "visible" | "modelOnly"
	origin              string
	input               string
	intentText          string
	executionKind       string
	turnHeaderOrigin    string
	sourceCommandID     string
	foregroundExecution string
	clientID            string
	originMeta          json.RawMessage
}

// normalizeTurnStarted ports normalizeTurnStarted (event-normalizer.ts).
func normalizeTurnStarted(event Event, m map[string]json.RawMessage, runtimeTurnID string) userIntent {
	messageID, hasMessageID := payloadString(m, "messageId")
	input, _ := payloadString(m, "input")
	productTurnID := runtimeTurnID
	if hasMessageID {
		productTurnID = messageID
	}
	entityID := "legacy:user:" + event.SessionID + ":" + runtimeTurnID + ":" + event.ID
	if hasMessageID {
		entityID = messageID
	}
	intent := m["intent"]
	intentMap := map[string]json.RawMessage{}
	if len(intent) > 0 {
		_ = json.Unmarshal(intent, &intentMap)
	}
	// intentText prefers intent.text, falling back to input.
	intentText := input
	if t, ok := payloadString(intentMap, "text"); ok {
		intentText = t
	}
	inputSource, _ := payloadString(m, "inputSource")
	visibility := "visible"
	if v, ok := payloadString(m, "inputVisibility"); ok && v == "model-only" {
		visibility = "modelOnly"
	}
	executionKind := "agent"
	if k, ok := payloadString(m, "executionKind"); ok {
		executionKind = k
	}
	sourceCommandID := ""
	if v, ok := payloadString(intentMap, "sourceCommandId"); ok {
		sourceCommandID = v
	} else if v, ok := payloadString(m, "inputId"); ok {
		sourceCommandID = v
	}
	clientID, _ := payloadString(intentMap, "clientId")
	foregroundExecution, _ := payloadString(m, "foregroundExecutionId")
	return userIntent{
		transcriptMessageID: messageID,
		hasTranscriptID:     hasMessageID,
		productTurnID:       productTurnID,
		runtimeTurnID:       runtimeTurnID,
		entityID:            entityID,
		visibility:          visibility,
		origin:              userInputOrigin(inputSource),
		input:               input,
		intentText:          intentText,
		executionKind:       executionKind,
		turnHeaderOrigin:    turnHeaderOrigin(inputSource),
		sourceCommandID:     sourceCommandID,
		foregroundExecution: foregroundExecution,
		clientID:            clientID,
		originMeta:          m["originMeta"],
	}
}

// onTurnStarted ports onTurnStarted.
func (p *Projection) onTurnStarted(event Event, m map[string]json.RawMessage) []Delta {
	runtimeTurnID := p.runtimeTurnIDOfEvent(event, m)
	fact := normalizeTurnStarted(event, m, runtimeTurnID)
	turnID := fact.productTurnID

	p.currentTurnID = runtimeTurnID
	p.hasCurrentTurn = true
	p.currentTurnStartedModelOnly = fact.visibility == "modelOnly"
	delete(p.productTurnIDByRuntimeTurnID, runtimeTurnID)
	if turnID != runtimeTurnID {
		p.productTurnIDByRuntimeTurnID[runtimeTurnID] = turnID
	}
	p.streamingTextRowID = nil
	p.streamingReasoningRowID = nil
	p.outputContinuationRowID = nil

	deltas := []Delta{}

	// modelChange marker: only when this turn's provider/model differs from the
	// previous turn's. The plain first turn (silentInitial) emits none.
	config := p.snapshot.get("config").(*obj)
	provider := strOf(config.get("provider"))
	model := strOf(config.get("model"))
	thought := strOf(config.get("thought"))
	hasModel := provider != "" && model != ""
	switch {
	case hasModel && p.lastTurnModelKind == "sourceLess":
		deltas = append(deltas, newRowAppended(p.rowBase(event, turnID,
			"model-initial:"+turnID+":"+provider+"/"+model).
			set("kind", "timelineMarker").
			set("lane", "lightBoundary").
			set("marker", newObj().
				set("type", "modelChange").
				set("toProvider", provider).
				set("toModel", model).
				set("toThought", thought))))
	case hasModel && p.lastTurnModelKind == "known" &&
		(p.lastTurnModelProvider != provider || p.lastTurnModelModel != model):
		deltas = append(deltas, newRowAppended(p.rowBase(event, turnID,
			"model-change:"+turnID+":"+p.lastTurnModelProvider+"/"+p.lastTurnModelModel+"->"+provider+"/"+model).
			set("kind", "timelineMarker").
			set("lane", "lightBoundary").
			set("marker", newObj().
				set("type", "modelChange").
				set("fromProvider", p.lastTurnModelProvider).
				set("fromModel", p.lastTurnModelModel).
				set("toProvider", provider).
				set("toModel", model).
				set("toThought", thought))))
	}
	if hasModel {
		p.lastTurnModelKind = "known"
		p.lastTurnModelProvider = provider
		p.lastTurnModelModel = model
		p.lastTurnModelThought = thought
	}

	headerBase := p.rowBase(event, turnID, turnID)
	header := shallowClone(headerBase).
		set("kind", "turnHeader").
		set("origin", fact.turnHeaderOrigin).
		set("executionKind", fact.executionKind).
		setIf(fact.sourceCommandID != "", "sourceCommandId", fact.sourceCommandID).
		setIf(len(fact.originMeta) > 0 && string(fact.originMeta) != "null", "originMeta", fact.originMeta).
		set("state", "running").
		set("startedAt", event.TimestampMs)
	p.turnHeaderRowIDByTurnID[turnID] = rowIDOf(header)
	deltas = append(deltas, newRowAppended(header))

	if fact.visibility == "visible" {
		rowBase := p.rowBase(event, turnID, fact.entityID)
		rootSourceCommandID := fact.sourceCommandID
		userRow := shallowClone(rowBase).
			set("kind", "userInput").
			set("text", fact.input).
			set("origin", fact.origin).
			setIf(fact.sourceCommandID != "", "sourceCommandId", fact.sourceCommandID).
			setIf(rootSourceCommandID != "", "rootSourceCommandId", rootSourceCommandID).
			setIf(fact.clientID != "", "clientId", fact.clientID)
		p.registerCanonicalUserRowTarget(userRow, fact)
		deltas = append(deltas, newRowAppended(userRow))
	}

	if fact.executionKind == "agent" {
		workKind := "primaryTurn"
		if fact.origin == "goalContinuation" {
			workKind = "goalContinuation"
		}
		work := newObj().set("kind", workKind).
			setIf(fact.foregroundExecution != "", "foregroundExecutionId", fact.foregroundExecution).
			set("startedAt", event.TimestampMs)
		deltas = append(deltas, newStateUpdated(p.controlPatch(newObj().
			set("phase", "running").
			set("sessionEnded", false).
			set("canStop", true).
			set("stopState", "stoppable").
			set("stopTargetKind", "assistant").
			set("activeWorks", []any{work}).
			set("lastError", nil).
			set("apiRetry", nil), nil, nil, false, false)))
	}
	return deltas
}

func userInputOrigin(source string) string {
	switch source {
	case "background_task":
		return "backgroundResult"
	case "goal-continuation":
		return "goalContinuation"
	case "subagent", "subagent_message":
		return "mailbox"
	case "fork", "plugin_reference", "rewind", "todo_reminder":
		return "synthetic"
	case "workflow_launch":
		return "workflowLaunch"
	default:
		return "realUser"
	}
}

func turnHeaderOrigin(source string) string {
	switch source {
	case "background_task":
		return "backgroundResult"
	case "goal-continuation":
		return "goalContinuation"
	case "rewind":
		return "editRerun"
	case "workflow_launch":
		return "workflowLaunch"
	default:
		return "userInput"
	}
}
