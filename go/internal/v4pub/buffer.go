package v4pub

import "encoding/json"

// appendConversationSubscriberBuffer (port): merge incoming into current and
// coalesce, then enforce the op / UTF-8-byte limits. A slow subscriber cannot
// keep piling up deltas into an unbounded frame.
//
// It returns the new buffer and its encoded byte count, or overflow when either
// limit is exceeded.
func appendSubscriberBuffer(current, incoming []Delta, maxOps, maxBytes int) ([]Delta, int, bool) {
	combined := make([]Delta, 0, len(current)+len(incoming))
	combined = append(combined, current...)
	combined = append(combined, incoming...)
	deltas := CoalesceConversationDeltas(combined, DefaultWorkflowRunWireBounds())
	if len(deltas) > maxOps {
		return nil, 0, true
	}
	encodedBytes := encodedDeltasPayloadBytes(deltas)
	if encodedBytes > maxBytes {
		return nil, 0, true
	}
	return deltas, encodedBytes, false
}

// encodedDeltasPayloadBytes is utf8JsonByteLength({kind:"deltas", deltas}) over
// the coalesced deltas, rendered the way JSON.stringify would.
func encodedDeltasPayloadBytes(deltas []Delta) int {
	items := make([]JSONValue, len(deltas))
	for i, d := range deltas {
		items[i] = RawMessageOf(d.Raw)
	}
	raw := Obj(Member("kind", Str("deltas")), Member("deltas", Arr(items...))).appendJSON(nil)
	return len(raw)
}

// wireSnapshotRaw trims the full projection snapshot to the wire window (last
// snapshotTailWindowRows rows) and recomputes totalCount/firstRowId as the
// source does, so the frame's snapshot matches getWireSnapshotForProfile.
func (p *Publisher) wireSnapshotRaw() json.RawMessage {
	full := p.source.SnapshotRaw()
	if len(full) == 0 {
		return full
	}
	obj, err := decodeRawObject(full)
	if err != nil {
		return full
	}
	rowsRaw, ok := obj.values["rows"]
	if !ok || len(rowsRaw) == 0 {
		return full
	}
	rowsObj, err := decodeRawObject(rowsRaw)
	if err != nil {
		return full
	}
	windowRaw, ok := rowsObj.values["window"]
	if !ok || len(windowRaw) == 0 {
		return full
	}
	var window []json.RawMessage
	if err := json.Unmarshal(windowRaw, &window); err != nil {
		return full
	}
	visible := window
	if len(visible) > SnapshotTailWindowRows {
		visible = visible[len(visible)-SnapshotTailWindowRows:]
	}
	newWindow := RawArray(visible)
	totalCount := len(visible)
	var firstRowID JSONValue = Null()
	if len(visible) > 0 {
		firstRowID = rowIDOf(visible[0])
	}
	newRows := replaceRowWindow(rowsObj, newWindow, totalCount, firstRowID)
	newObj := replaceMember(obj, "rows", newRows)
	return newObj.appendJSON(nil)
}

func rowIDOf(rowRaw json.RawMessage) JSONValue {
	var row struct {
		RowID *int64 `json:"rowId"`
	}
	if err := json.Unmarshal(rowRaw, &row); err == nil && row.RowID != nil {
		return Int(*row.RowID)
	}
	return Null()
}

func replaceRowWindow(rowsObj *orderedRawObject, window JSONValue, totalCount int, firstRowID JSONValue) JSONValue {
	members := make([]orderedMember, 0, len(rowsObj.keys))
	for _, k := range rowsObj.keys {
		switch k {
		case "window":
			members = append(members, Member("window", window))
		case "totalCount":
			members = append(members, Member("totalCount", Int(int64(totalCount))))
		case "firstRowId":
			members = append(members, Member("firstRowId", firstRowID))
		default:
			members = append(members, Member(k, RawMessageOf(rowsObj.values[k])))
		}
	}
	return Obj(members...)
}

func replaceMember(obj *orderedRawObject, key string, value JSONValue) JSONValue {
	members := make([]orderedMember, 0, len(obj.keys))
	for _, k := range obj.keys {
		if k == key {
			members = append(members, Member(key, value))
			continue
		}
		members = append(members, Member(k, RawMessageOf(obj.values[k])))
	}
	return Obj(members...)
}
