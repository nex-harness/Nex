package v4pub

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// scriptedSource is the Go twin of the generator's ScriptedProjection: it hands
// back the deltas the script pre-loaded and keeps a projection snapshot with the
// same key order the TypeScript object spread produces.
type scriptedSource struct {
	seq      int64
	revision int64
	deltas   []Delta
	snapshot JSONValue
}

func newScriptedSource(sessionID, logEpoch string) *scriptedSource {
	return &scriptedSource{snapshot: initialSnapshotValue(sessionID, logEpoch)}
}

func (s *scriptedSource) Seq() int64                       { return s.seq }
func (s *scriptedSource) WindowRowCount() int              { return windowRowCount(s.snapshot) }
func (s *scriptedSource) WorkflowRunsRaw() json.RawMessage { return nil }
func (s *scriptedSource) SnapshotRaw() json.RawMessage     { return s.snapshot.appendJSON(nil) }

func (s *scriptedSource) ApplyIngest(_ json.RawMessage) ([]Delta, error) {
	out := s.deltas
	s.applyDeltas(out)
	return out, nil
}

// applyDeltas mirrors the generator's ScriptedProjection.applyDeltas.
func (s *scriptedSource) applyDeltas(deltas []Delta) {
	for _, d := range deltas {
		switch d.Op {
		case OpStateUpdated:
			if obj, err := decodeRawObject(statePatchRaw(d.Raw)); err == nil {
				s.snapshot = mergeIntoSnapshot(s.snapshot, obj)
			}
		case OpRowAppended:
			s.appendRow(rowOf(d))
		case OpRowUpserted:
			s.upsertRow(rowOf(d))
		case OpRowRemoved:
			s.removeRowsFrom(d.FromRow)
		case OpRowDelta:
			s.appendRowText(d)
		}
	}
}

func initialSnapshotValue(sessionID, logEpoch string) JSONValue {
	return Obj(
		Member("protocolVersion", Int(1)),
		Member("sessionId", Str(sessionID)),
		Member("logEpoch", Str(logEpoch)),
		Member("seq", Int(0)),
		Member("revision", Int(0)),
		Member("control", controlValue()),
		Member("availability", availabilityValue()),
		Member("inputRouting", Obj(Member("mode", Str("startNow")))),
		Member("meta", Obj(Member("title", Str("t")), Member("titleSource", Str("generated")))),
		Member("config", configValue()),
		Member("modelTransition", Null()),
		Member("usage", usageValue()),
		Member("queue", Obj(Member("items", Arr()), Member("autoDrain", Bool(true)))),
		Member("pendingInteractions", Arr()),
		Member("pendingCommands", Arr()),
		Member("backgroundWorks", Arr()),
		Member("subagents", Obj(Member("revision", Int(0)), Member("childSessionIds", Arr()), Member("running", Arr()), Member("endedTotal", Int(0)))),
		Member("goal", Null()),
		Member("plan", Null()),
		Member("workspaceHookAdmission", Null()),
		Member("rows", Obj(Member("window", Arr()), Member("totalCount", Int(0)), Member("firstRowId", Null()))),
	)
}

func controlValue() JSONValue {
	return Obj(
		Member("phase", Str("draft")), Member("sessionEnded", Bool(false)),
		Member("canStop", Bool(false)), Member("stopState", Str("idle")),
		Member("stopTargetKind", Str("unknown")), Member("activeWorks", Arr()),
		Member("lastError", Null()), Member("apiRetry", Null()),
	)
}

func availabilityValue() JSONValue {
	return Obj(
		Member("fork", Obj(Member("allowed", Bool(false)), Member("reasonCode", Str("forkTargetNotStable")))),
		Member("compact", Obj(Member("allowed", Bool(false)), Member("reasonCode", Str("idleCannotCompact")))),
		Member("switchModelConfig", Obj(Member("allowed", Bool(true)))),
		Member("setFollowupMode", Obj(Member("allowed", Bool(true)))),
		Member("queueEdit", Obj(Member("allowed", Bool(true)))),
		Member("sendQueuedNow", Obj(Member("allowed", Bool(false)), Member("reasonCode", Str("sendQueuedNowRequiresRunning")))),
		Member("pauseGoal", Obj(Member("allowed", Bool(false)), Member("reasonCode", Str("noGoalToPause")))),
		Member("resumeGoal", Obj(Member("allowed", Bool(false)), Member("reasonCode", Str("noGoalToResume")))),
	)
}

func configValue() JSONValue {
	return Obj(
		Member("provider", Str("mock")), Member("model", Str("mock-model")),
		Member("thought", Str("none")), Member("thoughtLevels", Arr(Str("none"))),
		Member("followupMode", Str("queue")), Member("mode", Str("build")),
		Member("modelSelection", Obj(Member("providerId", Str("mock")), Member("modelId", Str("mock-model")), Member("options", Obj(Member("reasoningLevel", Str("none")))))),
		Member("planEnabled", Bool(false)),
	)
}

func usageValue() JSONValue {
	return Obj(
		Member("contextWindow", Null()),
		Member("cumulative", Obj(Member("inputTokens", Int(0)), Member("outputTokens", Int(0)), Member("cacheReadTokens", Int(0)), Member("cacheWriteTokens", Int(0)))),
	)
}

func windowRowCount(v JSONValue) int {
	for _, m := range v.obj {
		if m.Key == "rows" {
			for _, rm := range m.Val.obj {
				if rm.Key == "window" {
					return len(rm.Val.arr)
				}
			}
		}
	}
	return 0
}

func rowOf(d Delta) JSONValue {
	var envelope struct {
		Row json.RawMessage `json:"row"`
	}
	_ = json.Unmarshal(d.Raw, &envelope)
	return RawMessageOf(envelope.Row)
}

func (s *scriptedSource) appendRow(row JSONValue) {
	s.snapshot = updateRows(s.snapshot, func(window []JSONValue, totalCount int, firstRow JSONValue) ([]JSONValue, int, JSONValue) {
		window = append(window, row)
		if firstRow.kind == jvNull {
			firstRow = Int(fieldInt(row, "rowId"))
		}
		return window, len(window), firstRow
	})
}

func (s *scriptedSource) upsertRow(row JSONValue) {
	rid := fieldInt(row, "rowId")
	s.snapshot = updateRows(s.snapshot, func(window []JSONValue, totalCount int, firstRow JSONValue) ([]JSONValue, int, JSONValue) {
		for i, r := range window {
			if fieldInt(r, "rowId") == rid {
				window[i] = row
			}
		}
		return window, totalCount, firstRow
	})
}

func (s *scriptedSource) removeRowsFrom(fromRow int64) {
	s.snapshot = updateRows(s.snapshot, func(window []JSONValue, totalCount int, firstRow JSONValue) ([]JSONValue, int, JSONValue) {
		out := make([]JSONValue, 0, len(window))
		for _, r := range window {
			if fieldInt(r, "rowId") < fromRow {
				out = append(out, r)
			}
		}
		var first JSONValue = Null()
		if len(out) > 0 {
			first = Int(fieldInt(out[0], "rowId"))
		}
		return out, len(out), first
	})
}

func (s *scriptedSource) appendRowText(d Delta) {
	s.snapshot = updateRows(s.snapshot, func(window []JSONValue, totalCount int, firstRow JSONValue) ([]JSONValue, int, JSONValue) {
		for i, r := range window {
			if fieldInt(r, "rowId") == d.RowID {
				window[i] = appendText(r, d)
			}
		}
		return window, totalCount, firstRow
	})
}

func appendText(row JSONValue, d Delta) JSONValue {
	if d.Path == "output.text" {
		members := make([]orderedMember, 0, len(row.obj))
		for _, m := range row.obj {
			if m.Key == "output" {
				members = append(members, Member("output", appendFieldText(m.Val, "text", d.Append, true)))
				continue
			}
			members = append(members, m)
		}
		return Obj(members...)
	}
	field := map[string]string{"text": "text", "inputText": "inputText", "summaryText": "summaryText"}[d.Path]
	if field == "" {
		return row
	}
	return appendFieldText(row, field, d.Append, false)
}

func appendFieldText(v JSONValue, field, suffix string, required bool) JSONValue {
	members := make([]orderedMember, 0, len(v.obj)+1)
	found := false
	for _, m := range v.obj {
		if m.Key == field {
			members = append(members, Member(field, Str(m.Val.str+suffix)))
			found = true
			continue
		}
		members = append(members, m)
	}
	if !found && !required {
		members = append(members, Member(field, Str(suffix)))
	}
	return Obj(members...)
}

func updateRows(v JSONValue, fn func(window []JSONValue, totalCount int, firstRow JSONValue) ([]JSONValue, int, JSONValue)) JSONValue {
	members := make([]orderedMember, 0, len(v.obj))
	for _, m := range v.obj {
		if m.Key != "rows" {
			members = append(members, m)
			continue
		}
		var window []JSONValue
		totalCount := 0
		var firstRow JSONValue = Null()
		for _, rm := range m.Val.obj {
			switch rm.Key {
			case "window":
				window = append([]JSONValue{}, rm.Val.arr...)
			case "totalCount":
				totalCount = int(rm.Val.int)
			case "firstRowId":
				firstRow = rm.Val
			}
		}
		newWindow, newTotal, newFirst := fn(window, totalCount, firstRow)
		rowsMembers := make([]orderedMember, 0, len(m.Val.obj))
		for _, rm := range m.Val.obj {
			switch rm.Key {
			case "window":
				rowsMembers = append(rowsMembers, Member("window", Arr(newWindow...)))
			case "totalCount":
				rowsMembers = append(rowsMembers, Member("totalCount", Int(int64(newTotal))))
			case "firstRowId":
				rowsMembers = append(rowsMembers, Member("firstRowId", newFirst))
			default:
				rowsMembers = append(rowsMembers, rm)
			}
		}
		members = append(members, Member("rows", Obj(rowsMembers...)))
	}
	return Obj(members...)
}

func mergeIntoSnapshot(v JSONValue, patch *orderedRawObject) JSONValue {
	members := append([]orderedMember{}, v.obj...)
	for _, k := range patch.keys {
		val := RawMessageOf(patch.values[k])
		replaced := false
		for i := range members {
			if members[i].Key == k {
				members[i] = Member(k, val)
				replaced = true
				break
			}
		}
		if !replaced {
			members = append(members, Member(k, val))
		}
	}
	return Obj(members...)
}

func fieldInt(v JSONValue, key string) int64 {
	for _, m := range v.obj {
		if m.Key == key {
			return m.Val.int
		}
	}
	return 0
}

// --- script model -----------------------------------------------------------

type scenarioScript struct {
	Name                     string           `json:"name"`
	LogEpoch                 string           `json:"logEpoch"`
	SessionID                string           `json:"sessionId"`
	WorkflowRunDeltas        bool             `json:"workflowRunDeltas"`
	Retention                *int             `json:"retention"`
	SubscriberBufferMaxOps   *int             `json:"subscriberBufferMaxOps"`
	SubscriberBufferMaxBytes *int             `json:"subscriberBufferMaxBytes"`
	Steps                    []scenarioStep   `json:"steps"`
	Actions                  []scenarioAction `json:"actions"`
}

type scenarioStep struct {
	Seq      int64             `json:"seq"`
	Revision *int64            `json:"revision"`
	Deltas   []json.RawMessage `json:"deltas"`
}

type scenarioAction struct {
	Op              string `json:"op"`
	ConnectionID    string `json:"connectionId"`
	DeliveryProfile string `json:"deliveryProfile"`
	Sub             int    `json:"sub"`
	Step            int    `json:"step"`
	ForceSnapshot   bool   `json:"forceSnapshot"`
	Base            *struct {
		LogEpoch string `json:"logEpoch"`
		Seq      int64  `json:"seq"`
	} `json:"base"`
}

func loadScenarioScript(t testing.TB, name string) scenarioScript {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "testdata", "vectors", "publisher-scripts", name+".json"))
	if err != nil {
		t.Fatalf("missing script for %s: %v", name, err)
	}
	var s scenarioScript
	if err := json.Unmarshal(raw, &s); err != nil {
		t.Fatal(err)
	}
	return s
}

func TestScenariosAgreeWithTypeScript(t *testing.T) {
	v := loadPublisherVectors(t)
	for _, sc := range v.Scenarios {
		t.Run(sc.Name, func(t *testing.T) {
			runScenarioVector(t, sc)
		})
	}
}

func runScenarioVector(t testing.TB, sc vectorScenario) {
	t.Helper()
	script := loadScenarioScript(t, sc.Name)
	src := newScriptedSource(script.SessionID, script.LogEpoch)

	now := int64(1_700_000_000_000)
	pub, err := NewPublisher(script.SessionID, script.LogEpoch, Options{
		Now:                      func() int64 { return now },
		Source:                   src,
		Retention:                script.Retention,
		SubscriberBufferMaxOps:   script.SubscriberBufferMaxOps,
		SubscriberBufferMaxBytes: script.SubscriberBufferMaxBytes,
	})
	if err != nil {
		t.Fatal(err)
	}

	var ackIdx, frameIdx, nullIdx int
	subIDs := map[int]string{}

	for _, action := range script.Actions {
		switch action.Op {
		case "ingest":
			step := script.Steps[action.Step]
			src.seq = step.Seq
			if step.Revision != nil {
				src.revision = *step.Revision
				src.snapshot = withSeqRevision(src.snapshot, step.Seq, *step.Revision)
			}
			src.deltas = parseDeltas(t, step.Deltas)
			if err := pub.Ingest(nil); err != nil {
				t.Fatalf("ingest: %v", err)
			}
		case "subscribe", "resubscribe":
			params := SubscribeParams{
				ConnectionID:      action.ConnectionID,
				DeliveryProfile:   DeliveryProfileName(action.DeliveryProfile),
				WorkflowRunDeltas: script.WorkflowRunDeltas,
			}
			if action.Base != nil {
				params.Base = &Base{LogEpoch: action.Base.LogEpoch, Seq: action.Base.Seq}
			}
			result, err := pub.SubscribeReserved(params)
			if err != nil {
				t.Fatalf("subscribe: %v", err)
			}
			wantAck := sc.Acks[ackIdx]
			if string(result.Ack.Mode) != wantAck.Ack.Mode {
				t.Errorf("ack %d mode: go=%s ts=%s", ackIdx, result.Ack.Mode, wantAck.Ack.Mode)
			}
			if result.Ack.LogEpoch != wantAck.Ack.LogEpoch {
				t.Errorf("ack %d logEpoch: go=%s ts=%s", ackIdx, result.Ack.LogEpoch, wantAck.Ack.LogEpoch)
			}
			if formatSubID(result.Ack.SubscriptionID) != wantAck.Ack.SubscriptionID {
				t.Errorf("ack %d subId: go=%s ts=%s", ackIdx, formatSubID(result.Ack.SubscriptionID), wantAck.Ack.SubscriptionID)
			}
			subIDs[action.Sub] = result.Ack.SubscriptionID
			ackIdx++
			if result.Reservation != nil {
				checkFrame(t, sc.Frames[frameIdx], result.Reservation)
				frameIdx++
				result.Reservation.Commit()
			}
		case "flush":
			subID := subIDs[action.Sub]
			reservation := pub.ReserveFlush(subID)
			wantNull := sc.FlushNulls[nullIdx]
			nullIdx++
			if (reservation == nil) != wantNull {
				t.Errorf("flush: nil=%v want nil=%v", reservation == nil, wantNull)
				continue
			}
			if reservation != nil {
				checkFrame(t, sc.Frames[frameIdx], reservation)
				frameIdx++
				reservation.Commit()
			}
		case "resync":
			subID := subIDs[action.Sub]
			req := ResyncRequest{ForceSnapshot: action.ForceSnapshot}
			if action.Base != nil {
				req.Base = &Base{LogEpoch: action.Base.LogEpoch, Seq: action.Base.Seq}
			}
			result := pub.ResyncReserved(subID, req)
			if result == nil {
				t.Fatal("resync returned nil")
			}
			subIDs[action.Sub] = result.Ack.SubscriptionID
			ackIdx++
			if result.Reservation != nil {
				checkFrame(t, sc.Frames[frameIdx], result.Reservation)
				frameIdx++
				result.Reservation.Commit()
			}
		case "unsubscribe":
			var conn *string
			if action.ConnectionID != "" {
				c := action.ConnectionID
				conn = &c
			}
			pub.Unsubscribe(subIDs[action.Sub], conn)
			delete(subIDs, action.Sub)
		}
	}
	if frameIdx != len(sc.Frames) {
		t.Errorf("emitted %d frames, vector expects %d", frameIdx, len(sc.Frames))
	}
	if ackIdx != len(sc.Acks) {
		t.Errorf("emitted %d acks, vector expects %d", ackIdx, len(sc.Acks))
	}
}

func withSeqRevision(v JSONValue, seq, revision int64) JSONValue {
	members := make([]orderedMember, 0, len(v.obj))
	for _, m := range v.obj {
		switch m.Key {
		case "seq":
			members = append(members, Member("seq", Int(seq)))
		case "revision":
			members = append(members, Member("revision", Int(revision)))
		default:
			members = append(members, m)
		}
	}
	return Obj(members...)
}

func checkFrame(t testing.TB, want vectorFrameRec, got *Reservation) {
	t.Helper()
	if string(got.DeliveryKind) != want.DeliveryKind {
		t.Errorf("deliveryKind: go=%s ts=%s", got.DeliveryKind, want.DeliveryKind)
	}
	if got.LogicalFrameID != want.LogicalFrameID {
		t.Errorf("logicalFrameId: go=%s ts=%s", got.LogicalFrameID, want.LogicalFrameID)
	}
	if got.LogicalFrameOrdinal != want.LogicalFrameOrdinal {
		t.Errorf("logicalFrameOrdinal: go=%d ts=%d", got.LogicalFrameOrdinal, want.LogicalFrameOrdinal)
	}
	gotJSON := normalizeVectorJSON(got.Frame.FrameJSON())
	if !jsonEqual(t, gotJSON, want.Frame) {
		t.Errorf("frame mismatch:\n go: %s\n ts: %s", gotJSON, want.Frame)
	}
}

// normalizeVectorJSON applies the generator's normalization: the session id,
// topic and subscriptionId placeholder.
func normalizeVectorJSON(raw []byte) json.RawMessage {
	s := string(raw)
	s = strings.ReplaceAll(s, `"sessionId":"sess-vector"`, `"sessionId":"<session>"`)
	s = strings.ReplaceAll(s, `"topic":"conversation/sess-vector"`, `"topic":"conversation/<session>"`)
	s = strings.ReplaceAll(s, `"subscriptionId":"sub-epochepoch0-`, `"subscriptionId":"sub-<epoch>-`)
	return json.RawMessage(s)
}

func formatSubID(id string) string {
	const prefix = "sub-epochepoch0-"
	if strings.HasPrefix(id, prefix) {
		return "sub-<epoch>-" + strings.TrimPrefix(id, prefix)
	}
	return id
}
