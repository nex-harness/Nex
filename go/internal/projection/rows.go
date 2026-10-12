package projection

import "encoding/json"

// Accessors used across the reducer. Values in an obj are Go values: int64 for
// JSON numbers, string, bool, nil, []any, *obj, or json.RawMessage.

func rowIDOf(row *obj) int64       { return int64Of(row.get("rowId")) }
func kindOf(row *obj) string       { s, _ := row.get("kind").(string); return s }
func stateOf(row *obj) string      { s, _ := row.get("state").(string); return s }
func statusOf(row *obj) string     { s, _ := row.get("status").(string); return s }
func turnIDOf(row *obj) string     { s, _ := row.get("turnId").(string); return s }
func toolCallIDOf(row *obj) string { s, _ := row.get("toolCallId").(string); return s }

func int64Of(v any) int64 {
	switch n := v.(type) {
	case int64:
		return n
	case int:
		return int64(n)
	case float64:
		return int64(n)
	case json.Number:
		i, _ := n.Int64()
		return i
	default:
		return 0
	}
}

func intOf(v any) int { return int(int64Of(v)) }

func boolOf(v any) bool { b, _ := v.(bool); return b }

func strOf(v any) string { s, _ := v.(string); return s }

// isOpenForegroundToolRow ports isOpenForegroundToolRow.
func isOpenForegroundToolRow(row *obj) bool {
	if boolOf(row.get("backgrounded")) {
		return false
	}
	switch statusOf(row) {
	case "inputStreaming", "pendingApproval", "running":
		return true
	default:
		return false
	}
}

// rowBase ports rowBase: it allocates the next rowId and records the entity
// mapping. The TypeScript default entityId is String(event.id).
func (p *Projection) rowBase(event Event, turnID, entityID string) *obj {
	rowID := p.nextRowID
	p.nextRowID++
	p.entityIDByRowID[rowID] = entityID
	return newObj().
		set("rowId", rowID).
		set("turnId", turnID).
		set("entityId", entityID).
		set("productTurnId", turnID).
		set("visibility", "visible").
		set("createdAt", event.TimestampMs).
		set("createdAtSeq", event.SequenceNumber)
}

// turnIDOfEvent ports turnIdOf: the runtime turn id, remapped to the current
// product turn after a queue drain.
func (p *Projection) turnIDOfEvent(event Event) string {
	runtimeTurnID := "turn-unknown"
	switch {
	case event.HasTurnID:
		runtimeTurnID = event.TurnID
	case p.hasCurrentTurn:
		runtimeTurnID = p.currentTurnID
	}
	if product, ok := p.productTurnIDByRuntimeTurnID[runtimeTurnID]; ok {
		return product
	}
	return runtimeTurnID
}

// runtimeTurnIDOfEvent ports runtimeTurnIdOf (event-normalizer.ts): turnId if
// present, else `turn-<turnNumber>` for TurnStarted, else "turn-unknown".
func (p *Projection) runtimeTurnIDOfEvent(event Event, m map[string]json.RawMessage) string {
	if event.HasTurnID {
		return event.TurnID
	}
	if event.Type == TypeTurnStarted {
		if n, ok := payloadInt(m, "turnNumber"); ok {
			return "turn-" + itoa(n)
		}
	}
	return "turn-unknown"
}

// findRow ports findRow via the rowId index.
func (p *Projection) findRow(rowID int64) *obj {
	index, ok := p.rowIndexByID[rowID]
	if !ok {
		return nil
	}
	window := p.rowsWindow()
	if index < 0 || index >= len(window) {
		return nil
	}
	row, _ := window[index].(*obj)
	return row
}

// findToolRow ports findToolRow.
func (p *Projection) findToolRow(toolCallID string) *obj {
	rowID, ok := p.toolRowIDByCallID[toolCallID]
	if !ok {
		return nil
	}
	row := p.findRow(rowID)
	if row == nil || kindOf(row) != "toolCall" {
		return nil
	}
	return row
}

// turnHeaderForEvent ports turnHeaderForEvent.
func (p *Projection) turnHeaderForEvent(event Event) *obj {
	rowID, ok := p.turnHeaderRowIDByTurnID[p.turnIDOfEvent(event)]
	if !ok {
		return nil
	}
	row := p.findRow(rowID)
	if row == nil || kindOf(row) != "turnHeader" {
		return nil
	}
	return row
}

// rowIndex ports findIndex over the window by rowId.
func (p *Projection) rowIndexOf(rowID int64) int {
	index, ok := p.rowIndexByID[rowID]
	if !ok {
		return -1
	}
	return index
}
