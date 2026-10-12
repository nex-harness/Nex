package emitters

import (
	"encoding/json"

	"github.com/tylinux/nex/go/internal/projection"
	"github.com/tylinux/nex/go/internal/v4pub"
)

// mapOperationEvent ports mapComputerUseOperationEvent. Returns nil when the
// event produces no operation frame.
func mapOperationEvent(ev projection.Event) json.RawMessage {
	turnID := ""
	hasTurn := ev.HasTurnID
	if hasTurn {
		turnID = ev.TurnID
	}
	m := payloadMap(ev)

	base := func() *objBuilder {
		o := newObj()
		o.set("eventId", v4pub.Str(ev.ID))
		o.set("sequenceNumber", v4pub.Int(ev.SequenceNumber))
		o.set("sessionId", v4pub.Str(ev.SessionID))
		o.set("timestamp", v4pub.Int(eventTimestampMs(ev)))
		return o
	}

	switch ev.Type {
	case projection.TypeTurnStarted:
		if !hasTurn {
			return nil
		}
		return base().set("kind", v4pub.Str("turn-started")).set("turnId", v4pub.Str(turnID)).raw()
	case projection.TypeTurnComplete:
		if !hasTurn {
			return nil
		}
		return base().set("kind", v4pub.Str("turn-completed")).set("turnId", v4pub.Str(turnID)).raw()
	case projection.TypeToolCallScheduled:
		toolCallID, hasID := nonEmpty(m, "toolCallId")
		toolName, hasName := nonEmpty(m, "toolName")
		if !hasTurn || !hasID || !hasName {
			return nil
		}
		o := base().set("kind", v4pub.Str("tool-scheduled")).set("turnId", v4pub.Str(turnID)).
			set("toolCallId", v4pub.Str(toolCallID)).set("toolName", v4pub.Str(toolName))
		if toolName == "mcp__node_repl__js" && usesComputerUse(m) {
			o.set("computerUse", v4pub.Bool(true))
		}
		return o.raw()
	case projection.TypeToolCallStarted:
		toolCallID, hasID := nonEmpty(m, "toolCallId")
		if !hasID {
			return nil
		}
		o := base().set("kind", v4pub.Str("tool-started"))
		if hasTurn {
			o.set("turnId", v4pub.Str(turnID))
		}
		o.set("toolCallId", v4pub.Str(toolCallID))
		if v, ok := nonEmpty(m, "toolName"); ok {
			o.set("toolName", v4pub.Str(v))
		}
		return o.raw()
	case "session_ended":
		return base().set("kind", v4pub.Str("session-closed")).raw()
	default:
		return nil
	}
}

// usesComputerUse ports usesComputerUse: whether payload.input.code contains the
// mandatory setupComputerUseRuntime bootstrap string.
func usesComputerUse(payload map[string]json.RawMessage) bool {
	input := record(payload, "input")
	code, ok := nonEmpty(input, "code")
	if !ok {
		return false
	}
	return containsSubstring(code, "setupComputerUseRuntime")
}
