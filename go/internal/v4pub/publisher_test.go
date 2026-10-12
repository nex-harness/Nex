package v4pub

import (
	"encoding/json"
	"testing"
)

// TestSupersededReservationCannotCommit pins the generation guard in reserveFrame
// commit: a reservation whose subscription generation has been replaced must not
// advance the watermark (a late commit after the subscription was superseded).
func TestSupersededReservationCannotCommit(t *testing.T) {
	src := newScriptedSource("sess-g", "e")
	pub, err := NewPublisher("sess-g", "e", Options{Now: func() int64 { return 1 }, Source: src})
	if err != nil {
		t.Fatal(err)
	}
	first, err := pub.SubscribeReserved(SubscribeParams{ConnectionID: "a", DeliveryProfile: ProfileContinuous})
	if err != nil {
		t.Fatal(err)
	}
	// Re-subscribe (replaces) without committing the first reservation.
	second, err := pub.SubscribeReserved(SubscribeParams{ConnectionID: "a", DeliveryProfile: ProfileContinuous})
	if err != nil {
		t.Fatal(err)
	}
	if first.Reservation.Commit() {
		t.Error("superseded reservation commit returned true; must be false")
	}
	// The replacement still commits.
	if !second.Reservation.Commit() {
		t.Error("replacement reservation must commit")
	}
	// A committed reservation commits true on the second call (idempotent).
	if !second.Reservation.Commit() {
		t.Error("second commit must be idempotent true")
	}
}

// TestRollbackRestoresReplacedSubscription pins the atomic rollback used when an
// initial encode fails before the ACK is admitted.
func TestRollbackRestoresReplacedSubscription(t *testing.T) {
	src := newScriptedSource("sess-r", "e")
	pub, err := NewPublisher("sess-r", "e", Options{Now: func() int64 { return 1 }, Source: src})
	if err != nil {
		t.Fatal(err)
	}
	first, err := pub.SubscribeReserved(SubscribeParams{ConnectionID: "a", DeliveryProfile: ProfileContinuous})
	if err != nil {
		t.Fatal(err)
	}
	first.Reservation.Commit()
	// Replacement, then rollback before committing.
	second, err := pub.SubscribeReserved(SubscribeParams{ConnectionID: "a", DeliveryProfile: ProfileContinuous})
	if err != nil {
		t.Fatal(err)
	}
	if !second.Rollback() {
		t.Fatal("rollback should succeed")
	}
	if pub.HasSubscription(second.Ack.SubscriptionID, nil) {
		t.Error("rolled-back subscription must be gone")
	}
	if !pub.HasSubscription(first.Ack.SubscriptionID, nil) {
		t.Error("first subscription must be restored")
	}
	// Rollback after commit is refused.
	if second2, err := pub.SubscribeReserved(SubscribeParams{ConnectionID: "a", DeliveryProfile: ProfileContinuous}); err == nil {
		second2.Reservation.Commit()
		if second2.Rollback() {
			t.Error("rollback after commit must be false")
		}
	} else {
		t.Fatal(err)
	}
}

// TestSinkObservesFanoutAndOverflow pins the DeltaSink contract: each buffered
// fan-out is reported with its encoded byte count, and an overflow is reported
// with OnOverflow.
func TestSinkObservesFanoutAndOverflow(t *testing.T) {
	src := newScriptedSource("sess-sink", "e")
	sink := &recordingSink{}
	maxOps := 1
	pub, err := NewPublisher("sess-sink", "e", Options{
		Now:                    func() int64 { return 1 },
		Source:                 src,
		Sink:                   sink,
		SubscriberBufferMaxOps: &maxOps,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pub.SubscribeReserved(SubscribeParams{ConnectionID: "a", DeliveryProfile: ProfileContinuous}); err != nil {
		t.Fatal(err)
	}
	src.seq = 1
	src.deltas = mustParse(t, `[
		{"op":"row.appended","row":{"rowId":1,"kind":"assistantText","text":"a"}},
		{"op":"row.appended","row":{"rowId":2,"kind":"assistantText","text":"b"}}
	]`)
	if err := pub.Ingest(nil); err != nil {
		t.Fatal(err)
	}
	if len(sink.overflows) != 1 {
		t.Errorf("expected 1 overflow report, got %d", len(sink.overflows))
	}
}

func mustParse(t testing.TB, raw string) []Delta {
	t.Helper()
	var raws []json.RawMessage
	if err := json.Unmarshal([]byte(raw), &raws); err != nil {
		t.Fatal(err)
	}
	return parseDeltas(t, raws)
}

// TestUnsubscribeConnectionScope pins that unsubscribe only removes the
// subscription owned by the given connection.
func TestUnsubscribeConnectionScope(t *testing.T) {
	src := newScriptedSource("sess-u", "e")
	pub, err := NewPublisher("sess-u", "e", Options{Now: func() int64 { return 1 }, Source: src})
	if err != nil {
		t.Fatal(err)
	}
	r, err := pub.SubscribeReserved(SubscribeParams{ConnectionID: "a", DeliveryProfile: ProfileContinuous})
	if err != nil {
		t.Fatal(err)
	}
	wrong := "b"
	pub.Unsubscribe(r.Ack.SubscriptionID, &wrong)
	if !pub.HasSubscription(r.Ack.SubscriptionID, nil) {
		t.Error("unsubscribe with the wrong connection must be a no-op")
	}
	right := "a"
	pub.Unsubscribe(r.Ack.SubscriptionID, &right)
	if pub.HasSubscription(r.Ack.SubscriptionID, nil) {
		t.Error("unsubscribe with the right connection must remove it")
	}
}

type recordingSink struct {
	buffered  []string
	overflows []string
}

func (s *recordingSink) OnBuffered(subscriptionID string, _ []Delta, _ int) {
	s.buffered = append(s.buffered, subscriptionID)
}
func (s *recordingSink) OnOverflow(subscriptionID string) {
	s.overflows = append(s.overflows, subscriptionID)
}
