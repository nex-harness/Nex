package v4pub

// ResyncReserved re-arbitrates an active subscription from the client's base
// without changing its subscriptionId or profile. A client's base is the only
// recovery starting point: the publisher must not guess from sentSeq where the
// client got to. A new recovery admission supersedes the old reservation; a late
// commit returns false because the in-flight identity no longer matches.
func (p *Publisher) ResyncReserved(subscriptionID string, request ResyncRequest) *SubscribeResult {
	sub := p.subscriptions[subscriptionID]
	if sub == nil {
		return nil
	}

	previous := struct {
		buffer         []Delta
		bufferBytes    int
		resyncRequired bool
		sentSeq        int64
		inFlight       *Reservation
	}{sub.buffer, sub.bufferBytes, sub.resyncRequired, sub.sentSeq, sub.inFlight}

	// The old resync committed the current reservation and then sent a snapshot
	// based on the server's sentSeq, mis-recording frames the client never got.
	// Same-sub recovery must supersede directly.
	sub.inFlight = nil
	sub.buffer = nil
	sub.bufferBytes = 0
	sub.resyncRequired = false

	base := request.Base
	resumable := !request.ForceSnapshot &&
		base != nil &&
		base.LogEpoch == p.logEpoch &&
		base.Seq >= p.floorSeq &&
		base.Seq <= p.currentSeq()

	if !resumable {
		sub.sentSeq = 0
		reservation, _ := p.reserveFrame(sub, Frame{
			Topic:          p.topic,
			SubscriptionID: sub.subscriptionID,
			SentAt:         p.now(),
			FromSeq:        0,
			ToSeq:          p.currentSeq(),
			Payload:        FramePayload{Kind: "snapshot", Snapshot: p.wireSnapshotForSubscription(sub)},
		}, false, DeliveryRecovery)
		return &SubscribeResult{
			Ack:          p.ackFor(sub, ModeSnapshot),
			Reservation:  reservation,
			InitialFrame: &reservation.Frame,
			rollback:     p.resyncRollback(sub, reservation, previous.buffer, previous.bufferBytes, previous.resyncRequired, previous.sentSeq, previous.inFlight),
		}
	}

	sub.sentSeq = base.Seq
	replay := CoalesceConversationDeltas(
		p.encodeDeltasForSubscription(p.logDeltasAfter(base.Seq), sub),
		p.bounds,
	)
	reservation, _ := p.reserveFrame(sub, Frame{
		Topic:          p.topic,
		SubscriptionID: sub.subscriptionID,
		SentAt:         p.now(),
		FromSeq:        base.Seq,
		ToSeq:          p.currentSeq(),
		Payload:        FramePayload{Kind: "deltas", Deltas: replay},
	}, false, DeliveryRecovery)
	return &SubscribeResult{
		Ack:          p.ackFor(sub, ModeResume),
		Reservation:  reservation,
		InitialFrame: &reservation.Frame,
		rollback:     p.resyncRollback(sub, reservation, previous.buffer, previous.bufferBytes, previous.resyncRequired, previous.sentSeq, previous.inFlight),
	}
}

func (p *Publisher) resyncRollback(
	sub *subscription,
	reservation *Reservation,
	prevBuffer []Delta,
	prevBufferBytes int,
	prevResyncRequired bool,
	prevSentSeq int64,
	prevInFlight *Reservation,
) func() bool {
	rolledBack := false
	return func() bool {
		if rolledBack {
			return true
		}
		if p.subscriptions[sub.subscriptionID] != sub || sub.inFlight != reservation {
			return false
		}
		recoveryBuffer := sub.buffer
		recoveryResyncRequired := sub.resyncRequired
		merged, encodedBytes, overflow := appendSubscriberBuffer(prevBuffer, recoveryBuffer, p.maxOps, p.maxBytes)
		if overflow || prevResyncRequired || recoveryResyncRequired {
			sub.buffer = nil
			sub.bufferBytes = 0
			sub.resyncRequired = true
		} else {
			sub.buffer = merged
			sub.bufferBytes = encodedBytes
			sub.resyncRequired = false
		}
		sub.sentSeq = prevSentSeq
		sub.inFlight = prevInFlight
		rolledBack = true
		return true
	}
}

// Resync is the overflow-degradation convenience form: clear the buffer and
// re-send a snapshot frame to realign.
func (p *Publisher) Resync(subscriptionID string) *Frame {
	result := p.ResyncReserved(subscriptionID, ResyncRequest{Base: nil, ForceSnapshot: true})
	if result == nil || result.Reservation == nil || !result.Reservation.Commit() {
		return nil
	}
	frame := result.Reservation.Frame
	return &frame
}
