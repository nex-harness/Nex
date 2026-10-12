package v4pub

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

type publisherVectors struct {
	Limits struct {
		SubscriberBufferMaxOps   int `json:"subscriberBufferMaxOps"`
		SubscriberBufferMaxBytes int `json:"subscriberBufferMaxBytes"`
		EventRetentionPerSession int `json:"eventRetentionPerSession"`
		SnapshotTailWindowRows   int `json:"snapshotTailWindowRows"`
	} `json:"limits"`
	DeliveryProfiles struct {
		Continuous struct {
			FlushWindowMs int `json:"flushWindowMs"`
		} `json:"continuous"`
		Replayable struct {
			FlushWindowMs int `json:"flushWindowMs"`
		} `json:"replayable"`
	} `json:"deliveryProfiles"`
	CoalesceCases []struct {
		Name   string            `json:"name"`
		Input  []json.RawMessage `json:"input"`
		Output []json.RawMessage `json:"output"`
	} `json:"coalesceCases"`
	FilterCases []struct {
		Profile string            `json:"profile"`
		Input   []json.RawMessage `json:"input"`
		Output  []json.RawMessage `json:"output"`
	} `json:"filterCases"`
	LegacyEncodeCases []struct {
		Name         string            `json:"name"`
		Input        []json.RawMessage `json:"input"`
		WorkflowRuns json.RawMessage   `json:"workflowRuns"`
		Output       []json.RawMessage `json:"output"`
	} `json:"legacyEncodeCases"`
	ClampCases []struct {
		Name   string          `json:"name"`
		Input  json.RawMessage `json:"input"`
		Output json.RawMessage `json:"output"`
	} `json:"clampCases"`
	Scenarios []vectorScenario `json:"scenarios"`
}

type vectorScenario struct {
	Name       string           `json:"name"`
	AckCount   int              `json:"ackCount"`
	Acks       []vectorAck      `json:"acks"`
	Frames     []vectorFrameRec `json:"frames"`
	FlushNulls []bool           `json:"flushNulls"`
}

type vectorAck struct {
	Ack struct {
		SubscriptionID string `json:"subscriptionId"`
		Mode           string `json:"mode"`
		LogEpoch       string `json:"logEpoch"`
	} `json:"ack"`
}

type vectorFrameRec struct {
	DeliveryKind        string          `json:"deliveryKind"`
	LogicalFrameID      string          `json:"logicalFrameId"`
	LogicalFrameOrdinal int64           `json:"logicalFrameOrdinal"`
	Frame               json.RawMessage `json:"frame"`
}

func loadPublisherVectors(t testing.TB) *publisherVectors {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "testdata", "vectors", "publisher.json"))
	if err != nil {
		t.Fatal(err)
	}
	var v publisherVectors
	if err := json.Unmarshal(raw, &v); err != nil {
		t.Fatal(err)
	}
	return &v
}

func parseDeltas(t testing.TB, raws []json.RawMessage) []Delta {
	t.Helper()
	out := make([]Delta, len(raws))
	for i, r := range raws {
		d, err := ParseDelta(r)
		if err != nil {
			t.Fatalf("parse delta %d: %v", i, err)
		}
		out[i] = d
	}
	return out
}

func deltaRaws(deltas []Delta) []json.RawMessage {
	out := make([]json.RawMessage, len(deltas))
	for i, d := range deltas {
		out[i] = d.Raw
	}
	return out
}

// jsonEqual compares two JSON documents structurally (key order independent).
func jsonEqual(t testing.TB, a, b json.RawMessage) bool {
	t.Helper()
	var av, bv any
	if err := json.Unmarshal(a, &av); err != nil {
		t.Fatalf("unmarshal a: %v\n%s", err, a)
	}
	if err := json.Unmarshal(b, &bv); err != nil {
		t.Fatalf("unmarshal b: %v\n%s", err, b)
	}
	return reflect.DeepEqual(av, bv)
}

func TestLimitsMatchTypeScript(t *testing.T) {
	v := loadPublisherVectors(t)
	if v.Limits.SubscriberBufferMaxOps != SubscriberBufferMaxOps {
		t.Errorf("subscriberBufferMaxOps: ts=%d go=%d", v.Limits.SubscriberBufferMaxOps, SubscriberBufferMaxOps)
	}
	if v.Limits.SubscriberBufferMaxBytes != SubscriberBufferMaxBytes {
		t.Errorf("subscriberBufferMaxBytes: ts=%d go=%d", v.Limits.SubscriberBufferMaxBytes, SubscriberBufferMaxBytes)
	}
	if v.Limits.EventRetentionPerSession != EventRetentionPerSession {
		t.Errorf("eventRetentionPerSession: ts=%d go=%d", v.Limits.EventRetentionPerSession, EventRetentionPerSession)
	}
	if v.Limits.SnapshotTailWindowRows != SnapshotTailWindowRows {
		t.Errorf("snapshotTailWindowRows: ts=%d go=%d", v.Limits.SnapshotTailWindowRows, SnapshotTailWindowRows)
	}
	if v.DeliveryProfiles.Continuous.FlushWindowMs != DeliveryProfiles[ProfileContinuous].FlushWindowMs {
		t.Errorf("continuous flushWindowMs: ts=%d go=%d", v.DeliveryProfiles.Continuous.FlushWindowMs, DeliveryProfiles[ProfileContinuous].FlushWindowMs)
	}
	if v.DeliveryProfiles.Replayable.FlushWindowMs != DeliveryProfiles[ProfileReplayable].FlushWindowMs {
		t.Errorf("replayable flushWindowMs: ts=%d go=%d", v.DeliveryProfiles.Replayable.FlushWindowMs, DeliveryProfiles[ProfileReplayable].FlushWindowMs)
	}
}

func TestCoalesceAgreesWithTypeScript(t *testing.T) {
	v := loadPublisherVectors(t)
	for _, c := range v.CoalesceCases {
		in := parseDeltas(t, c.Input)
		got := CoalesceConversationDeltas(in, DefaultWorkflowRunWireBounds())
		if len(got) != len(c.Output) {
			t.Errorf("%s: %d deltas, ts produced %d", c.Name, len(got), len(c.Output))
			continue
		}
		for i := range got {
			if !jsonEqual(t, got[i].Raw, c.Output[i]) {
				t.Errorf("%s[%d]:\n go: %s\n ts: %s", c.Name, i, got[i].Raw, c.Output[i])
			}
		}
	}
}

func TestFilterAgreesWithTypeScript(t *testing.T) {
	v := loadPublisherVectors(t)
	for _, c := range v.FilterCases {
		in := parseDeltas(t, c.Input)
		got := FilterConversationDeltasForProfile(in, DeliveryProfiles[DeliveryProfileName(c.Profile)])
		if len(got) != len(c.Output) {
			t.Errorf("%s: %d deltas, ts produced %d", c.Profile, len(got), len(c.Output))
			continue
		}
		for i := range got {
			if !jsonEqual(t, got[i].Raw, c.Output[i]) {
				t.Errorf("%s[%d]:\n go: %s\n ts: %s", c.Profile, i, got[i].Raw, c.Output[i])
			}
		}
	}
}

func TestLegacyEncodeAgreesWithTypeScript(t *testing.T) {
	v := loadPublisherVectors(t)
	for _, c := range v.LegacyEncodeCases {
		in := parseDeltas(t, c.Input)
		got := encodeDeltasForLegacy(in, c.WorkflowRuns)
		if len(got) != len(c.Output) {
			t.Errorf("%s: %d deltas, ts produced %d", c.Name, len(got), len(c.Output))
			continue
		}
		for i := range got {
			if !jsonEqual(t, got[i].Raw, c.Output[i]) {
				t.Errorf("%s[%d]:\n go: %s\n ts: %s", c.Name, i, got[i].Raw, c.Output[i])
			}
		}
	}
}

func TestClampWorkflowRunsAgreesWithTypeScript(t *testing.T) {
	v := loadPublisherVectors(t)
	for _, c := range v.ClampCases {
		got := clampWorkflowRunsForLegacy(c.Input)
		if !jsonEqual(t, got, c.Output) {
			t.Errorf("%s:\n go: %s\n ts: %s", c.Name, got, c.Output)
		}
	}
}
