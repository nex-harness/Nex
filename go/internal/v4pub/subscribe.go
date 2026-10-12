package v4pub

import (
	"fmt"
	"sort"
)

// Subscribe arbitrates base.logEpoch match + base.seq within the retention
// window -> resume, otherwise snapshot. Re-subscribing from the same
// connectionId replaces the old subscription and clears its flush buffer.
func (p *Publisher) Subscribe(params SubscribeParams) (*SubscribeResult, error) {
	result, err := p.subscribeReserved(params)
	if err != nil {
		return nil, err
	}
	if result.Reservation != nil {
		result.Reservation.Commit()
	}
	return result, nil
}

// SubscribeReserved is the production gateway entry: the initial frame may only
// commit once the whole physical batch has been accepted.
func (p *Publisher) SubscribeReserved(params SubscribeParams) (*SubscribeResult, error) {
	return p.subscribeReserved(params)
}

func (p *Publisher) subscribeReserved(params SubscribeParams) (*SubscribeResult, error) {
	previousID, hadPrevious := p.subscriptionIDByConnection[params.ConnectionID]
	var previous *subscription
	if hadPrevious {
		previous = p.subscriptions[previousID]
		delete(p.subscriptions, previousID)
	}

	profile := DeliveryProfiles[params.DeliveryProfile]
	sub := &subscription{
		subscriptionID:          fmt.Sprintf("sub-%s-%d", p.logEpoch, p.nextSubscriptionSerial),
		connectionID:            params.ConnectionID,
		profile:                 profile,
		workflowRunDeltas:       params.WorkflowRunDeltas,
		nextLogicalFrameOrdinal: 1,
	}
	p.nextSubscriptionSerial++
	p.subscriptions[sub.subscriptionID] = sub
	p.subscriptionIdByConnectionSet(params.ConnectionID, sub.subscriptionID)

	rollback := func() bool {
		if sub.inFlight == nil {
			return false
		}
		if p.subscriptions[sub.subscriptionID] != sub {
			return false
		}
		if p.subscriptionIDByConnection[params.ConnectionID] != sub.subscriptionID {
			return false
		}
		delete(p.subscriptions, sub.subscriptionID)
		if hadPrevious && previous != nil {
			p.subscriptions[previousID] = previous
			p.subscriptionIDByConnection[params.ConnectionID] = previousID
		} else {
			delete(p.subscriptionIDByConnection, params.ConnectionID)
		}
		return true
	}

	resumable := params.Base != nil &&
		params.Base.LogEpoch == p.logEpoch &&
		params.Base.Seq >= p.floorSeq &&
		params.Base.Seq <= p.currentSeq()

	if !resumable {
		reservation, err := p.reserveFrame(sub, Frame{
			Topic:          p.topic,
			SubscriptionID: sub.subscriptionID,
			SentAt:         p.now(),
			FromSeq:        0,
			ToSeq:          p.currentSeq(),
			Payload:        FramePayload{Kind: "snapshot", Snapshot: p.wireSnapshotForSubscription(sub)},
		}, false, DeliveryInitial)
		if err != nil {
			return nil, err
		}
		return &SubscribeResult{
			Ack:          p.ackFor(sub, ModeSnapshot),
			Reservation:  reservation,
			InitialFrame: &reservation.Frame,
			rollback:     rollback,
		}, nil
	}

	replay := CoalesceConversationDeltas(
		p.encodeDeltasForSubscription(p.logDeltasAfter(params.Base.Seq), sub),
		p.bounds,
	)
	if params.Base.Seq == p.currentSeq() {
		sub.sentSeq = params.Base.Seq
		return &SubscribeResult{
			Ack:      p.ackFor(sub, ModeResume),
			rollback: func() bool { return false },
		}, nil
	}
	sub.sentSeq = params.Base.Seq
	reservation, err := p.reserveFrame(sub, Frame{
		Topic:          p.topic,
		SubscriptionID: sub.subscriptionID,
		SentAt:         p.now(),
		FromSeq:        params.Base.Seq,
		ToSeq:          p.currentSeq(),
		Payload:        FramePayload{Kind: "deltas", Deltas: replay},
	}, false, DeliveryInitial)
	if err != nil {
		return nil, err
	}
	return &SubscribeResult{
		Ack:          p.ackFor(sub, ModeResume),
		Reservation:  reservation,
		InitialFrame: &reservation.Frame,
		rollback:     rollback,
	}, nil
}

func (p *Publisher) subscriptionIdByConnectionSet(connectionID, subscriptionID string) {
	p.subscriptionIDByConnection[connectionID] = subscriptionID
}

func (p *Publisher) logDeltasAfter(seq int64) []Delta {
	var out []Delta
	for _, entry := range p.log {
		if entry.Seq > seq {
			out = append(out, entry.Deltas...)
		}
	}
	return out
}

// Unsubscribe removes a subscription, optionally scoped to a connection.
func (p *Publisher) Unsubscribe(subscriptionID string, connectionID *string) {
	sub := p.subscriptions[subscriptionID]
	if sub == nil {
		return
	}
	if connectionID != nil && sub.connectionID != *connectionID {
		return
	}
	delete(p.subscriptions, subscriptionID)
	if p.subscriptionIDByConnection[sub.connectionID] == subscriptionID {
		delete(p.subscriptionIDByConnection, sub.connectionID)
	}
}

// HasSubscription reports whether the subscription exists, optionally scoped to
// a connection.
func (p *Publisher) HasSubscription(subscriptionID string, connectionID *string) bool {
	sub := p.subscriptions[subscriptionID]
	if sub == nil {
		return false
	}
	return connectionID == nil || sub.connectionID == *connectionID
}

// HasSubscribers reports whether any subscriber remains (resident reclamation).
func (p *Publisher) HasSubscribers() bool { return len(p.subscriptions) > 0 }

// ConnectionIDForSubscription returns the owning connection, or ("", false).
func (p *Publisher) ConnectionIDForSubscription(subscriptionID string) (string, bool) {
	sub := p.subscriptions[subscriptionID]
	if sub == nil {
		return "", false
	}
	return sub.connectionID, true
}

func (p *Publisher) ackFor(sub *subscription, mode AckMode) SubscribeAck {
	return SubscribeAck{SubscriptionID: sub.subscriptionID, Mode: mode, LogEpoch: p.logEpoch}
}

// reserveFrame builds a reservation for one frame and installs it as the
// subscription's in-flight reservation. commit advances sentSeq when the
// generation is still valid.
func (p *Publisher) reserveFrame(sub *subscription, frame Frame, snapshotRecovery bool, deliveryKind DeliveryKind) (*Reservation, error) {
	frameID := fmt.Sprintf("%s-lf-%d", sub.subscriptionID, p.nextLogicalFrameSerial)
	p.nextLogicalFrameSerial++
	ordinal := sub.nextLogicalFrameOrdinal
	sub.nextLogicalFrameOrdinal++

	reservation := &Reservation{
		DeliveryKind:        deliveryKind,
		LogicalFrameID:      frameID,
		LogicalFrameOrdinal: ordinal,
		Frame:               frame,
	}
	committed := false
	reservation.commit = func() bool {
		if committed {
			return true
		}
		if p.subscriptions[sub.subscriptionID] != sub || sub.inFlight != reservation {
			return false
		}
		sub.sentSeq = frame.ToSeq
		sub.inFlight = nil
		if snapshotRecovery {
			sub.resyncRequired = p.currentSeq() > frame.ToSeq
		}
		committed = true
		return true
	}
	sub.inFlight = reservation
	return reservation, nil
}

// Flush drains one subscriber's flush buffer into a frame (the host drives this
// by flushWindowMs). It returns nil when there is nothing new. The frame
// interval (sentSeq, currentSeq] covers seq gaps filtered out mid-way, so the
// client's `frame.fromSeq === store.seq` contiguity check is unaffected by
// profile filtering.
func (p *Publisher) Flush(subscriptionID string) *Frame {
	reservation := p.ReserveFlush(subscriptionID)
	if reservation == nil || !reservation.Commit() {
		return nil
	}
	frame := reservation.Frame
	return &frame
}

// ReserveFlush is the reservation-returning variant used by the gateway.
func (p *Publisher) ReserveFlush(subscriptionID string) *Reservation {
	sub := p.subscriptions[subscriptionID]
	if sub == nil {
		return nil
	}
	if sub.inFlight != nil {
		return sub.inFlight
	}
	if sub.resyncRequired {
		sub.buffer = nil
		sub.bufferBytes = 0
		reservation, _ := p.reserveFrame(sub, Frame{
			Topic:          p.topic,
			SubscriptionID: sub.subscriptionID,
			SentAt:         p.now(),
			FromSeq:        0,
			ToSeq:          p.currentSeq(),
			Payload:        FramePayload{Kind: "snapshot", Snapshot: p.wireSnapshotForSubscription(sub)},
		}, true, DeliveryOnline)
		return reservation
	}
	if len(sub.buffer) == 0 && sub.sentSeq == p.currentSeq() {
		return nil
	}
	deltas := sub.buffer
	frame := Frame{
		Topic:          p.topic,
		SubscriptionID: sub.subscriptionID,
		SentAt:         p.now(),
		FromSeq:        sub.sentSeq,
		ToSeq:          p.currentSeq(),
		Payload:        FramePayload{Kind: "deltas", Deltas: deltas},
	}
	sub.buffer = nil
	sub.bufferBytes = 0
	reservation, _ := p.reserveFrame(sub, frame, false, DeliveryOnline)
	return reservation
}

// wireSnapshotForSubscription is the snapshot frame payload for one subscriber.
func (p *Publisher) wireSnapshotForSubscription(sub *subscription) []byte {
	return p.wireSnapshotRaw()
}

// SubscriptionIDs returns the ids of the currently active subscriptions. The
// gateway uses it to drive per-subscription flushes on its timer.
func (p *Publisher) SubscriptionIDs() []string {
	ids := make([]string, 0, len(p.subscriptions))
	for id := range p.subscriptions {
		ids = append(ids, id)
	}
	// Deterministic order so frame emission is stable in tests.
	sort.Strings(ids)
	return ids
}
