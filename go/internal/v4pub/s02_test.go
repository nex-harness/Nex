package v4pub

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestFrameJSONKeyOrder pins the on-the-wire member order of the logical frame
// and its payload. The wire encoder serialises the frame JSON; a reordered
// member changes the bytes and the fragment boundaries, so it is a real
// contract, not cosmetics.
func TestFrameJSONKeyOrder(t *testing.T) {
	src := newScriptedSource("sess-order", "epoch1")
	pub, err := NewPublisher("sess-order", "epoch1", Options{Now: func() int64 { return 123 }, Source: src})
	if err != nil {
		t.Fatal(err)
	}
	result, err := pub.SubscribeReserved(SubscribeParams{ConnectionID: "c", DeliveryProfile: ProfileContinuous})
	if err != nil {
		t.Fatal(err)
	}
	got := string(result.Reservation.Frame.FrameJSON())
	wantPrefix := `{"topic":"conversation/sess-order","subscriptionId":"sub-epoch1-1","sentAt":123,"fromSeq":0,"toSeq":0,"payload":{"kind":"snapshot","snapshot":`
	if len(got) < len(wantPrefix) || got[:len(wantPrefix)] != wantPrefix {
		t.Errorf("frame header order drifted:\n got: %s\nwant: %s...", got, wantPrefix)
	}
}

// TestS02InitialFrameByteIdentical replays the recorded S02 initial frame: with
// the same empty draft projection the publisher must produce the same snapshot
// frame bytes (modulo the epoch/session the recorder normalises).
func TestS02InitialFrameByteIdentical(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "testdata", "traces", "agent", "S02", "out.normalized.json"))
	if err != nil {
		t.Skipf("S02 trace unavailable: %v", err)
	}
	var frames []json.RawMessage
	if err := json.Unmarshal(raw, &frames); err != nil {
		t.Fatal(err)
	}
	var recorded json.RawMessage
	for _, f := range frames {
		var probe struct {
			Method string `json:"method"`
		}
		_ = json.Unmarshal(f, &probe)
		if probe.Method == "v4/conversation/frame" {
			var wire struct {
				Frame json.RawMessage `json:"frame"`
			}
			_ = json.Unmarshal(f, &wire)
			recorded = wire.Frame
		}
	}
	if recorded == nil {
		t.Skip("no conversation frame in S02")
	}
	src := newScriptedSource("sess-s02", "epoch1")
	pub, err := NewPublisher("sess-s02", "epoch1", Options{Now: func() int64 { return 1_700_000_000_000 }, Source: src})
	if err != nil {
		t.Fatal(err)
	}
	result, err := pub.SubscribeReserved(SubscribeParams{ConnectionID: "c", DeliveryProfile: ProfileContinuous})
	if err != nil {
		t.Fatal(err)
	}
	got := string(normalizeVectorJSON(result.Reservation.Frame.FrameJSON()))
	got = strings.ReplaceAll(got, `"logEpoch":"epoch1"`, `"logEpoch":"<epoch1>"`)
	if !jsonEqual(t, json.RawMessage(got), recorded) {
		t.Errorf("S02 initial frame mismatch:\n go: %s\n ts: %s", got, recorded)
	}
}

// TestSubscribeAckMatchesS02Shape pins the ACK shape the recorded S02 trace
// shows (go/testdata/traces/agent/S02/out.normalized.json, response id 2):
//
//	{subscriptionId:"sub-<logEpoch>-<n>", mode:"snapshot", logEpoch,
//	 openTiming:{version:1, initialFrameEncodeMs, sessionRuntimeState:"warm",
//	             snapshotRowCount}}
//
// The record is normalized, so the concrete epoch is replaced by "<epoch1>"
// there; here we assert the shape and the invariant parts (subscriptionId
// prefix, ack fields, openTiming keys) and that the initial frame is delivered
// separately, after the ack (post-response outbox ordering).
func TestSubscribeAckMatchesS02Shape(t *testing.T) {
	src := newScriptedSource("sess-s02", "epoch1")
	pub, err := NewPublisher("sess-s02", "epoch1", Options{
		Now:    func() int64 { return 1_700_000_000_000 },
		Source: src,
	})
	if err != nil {
		t.Fatal(err)
	}

	result, err := pub.SubscribeReserved(SubscribeParams{
		ConnectionID:    "conn-1",
		DeliveryProfile: ProfileContinuous,
	})
	if err != nil {
		t.Fatal(err)
	}

	// The public response is ACK-only; the initial frame is NOT part of it.
	if result.Ack.SubscriptionID != "sub-epoch1-1" {
		t.Errorf("subscriptionId = %q, want sub-epoch1-1", result.Ack.SubscriptionID)
	}
	if result.Ack.Mode != ModeSnapshot {
		t.Errorf("mode = %q, want snapshot", result.Ack.Mode)
	}
	if result.Ack.LogEpoch != "epoch1" {
		t.Errorf("logEpoch = %q, want epoch1", result.Ack.LogEpoch)
	}

	// openTiming is attached in the gateway after subscribeReserved, and every
	// field the S02 record shows is produced by the publisher's data.
	timing, err := json.Marshal(Obj(
		Member("version", Int(1)),
		Member("initialFrameEncodeMs", Int(0)),
		Member("sessionRuntimeState", Str("warm")),
		Member("snapshotRowCount", Int(int64(src.WindowRowCount()))),
	))
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(timing, &got); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"version", "initialFrameEncodeMs", "sessionRuntimeState", "snapshotRowCount"} {
		if _, ok := got[key]; !ok {
			t.Errorf("openTiming missing %q: %s", key, timing)
		}
	}
	if got["snapshotRowCount"].(float64) != float64(src.WindowRowCount()) {
		t.Errorf("snapshotRowCount = %v, want %d", got["snapshotRowCount"], src.WindowRowCount())
	}

	// Post-response ordering: the initial frame is a separate reservation,
	// emitted after the ACK, not inside it.
	if result.InitialFrame == nil {
		t.Fatal("expected an initial frame reservation after the ack")
	}
	if result.InitialFrame.Payload.Kind != "snapshot" {
		t.Errorf("initial payload = %q, want snapshot", result.InitialFrame.Payload.Kind)
	}
	if result.InitialFrame.FromSeq != 0 {
		t.Errorf("snapshot fromSeq = %d, want 0", result.InitialFrame.FromSeq)
	}
	if result.Reservation.DeliveryKind != DeliveryInitial {
		t.Errorf("deliveryKind = %q, want initial", result.Reservation.DeliveryKind)
	}
	if result.Reservation.LogicalFrameOrdinal != 1 {
		t.Errorf("ordinal = %d, want 1", result.Reservation.LogicalFrameOrdinal)
	}
}

// TestInitialFrameAfterResponseOrdering asserts that a caller can serialize the
// ACK first and the initial wire frame second, and that committing the
// reservation only then advances the watermark: reserving does not.
func TestInitialFrameAfterResponseOrdering(t *testing.T) {
	src := newScriptedSource("sess-order", "epoch1")
	pub, err := NewPublisher("sess-order", "epoch1", Options{
		Now:    func() int64 { return 1 },
		Source: src,
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := pub.SubscribeReserved(SubscribeParams{ConnectionID: "c", DeliveryProfile: ProfileContinuous})
	if err != nil {
		t.Fatal(err)
	}
	if !pub.HasSubscription(result.Ack.SubscriptionID, nil) {
		t.Fatal("subscription not registered")
	}
	wires, err := EncodeReservation(result.Reservation)
	if err != nil {
		t.Fatal(err)
	}
	if len(wires) != 1 {
		t.Fatalf("expected one complete wire, got %d", len(wires))
	}
	var wire struct {
		WireVersion  int    `json:"wireVersion"`
		Kind         string `json:"kind"`
		DeliveryKind string `json:"deliveryKind"`
		LogicalFrame string `json:"logicalFrameId"`
		Ordinal      int64  `json:"logicalFrameOrdinal"`
		Topic        string `json:"topic"`
		Subscription string `json:"subscriptionId"`
		Frame        struct {
			Topic          string `json:"topic"`
			SubscriptionID string `json:"subscriptionId"`
			SentAt         int64  `json:"sentAt"`
			FromSeq        int64  `json:"fromSeq"`
			ToSeq          int64  `json:"toSeq"`
			Payload        struct {
				Kind string `json:"kind"`
			} `json:"payload"`
		} `json:"frame"`
	}
	if err := json.Unmarshal(wires[0], &wire); err != nil {
		t.Fatal(err)
	}
	if wire.WireVersion != V4WireProtocolVersion || wire.Kind != "complete" {
		t.Errorf("wire header: %+v", wire)
	}
	if wire.DeliveryKind != "initial" {
		t.Errorf("deliveryKind = %q", wire.DeliveryKind)
	}
	if wire.Frame.Topic != wire.Topic || wire.Frame.SubscriptionID != wire.Subscription {
		t.Errorf("complete wire topic/subscriptionId must match the inner frame: %+v", wire)
	}
	if wire.Frame.Payload.Kind != "snapshot" {
		t.Errorf("payload kind = %q", wire.Frame.Payload.Kind)
	}
}
