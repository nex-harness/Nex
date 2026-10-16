package proto

// wirelifecycle.go is the assembler's lifecycle surface: expiring timed-out
// assemblies, dropping a route on unsubscribe, aborting a route after a fault,
// reporting stats, and the next-expiry instant a consumer needs to arm one timer
// instead of polling.
//
// Discard and Abort differ in exactly one way and the difference is the point:
// Discard forgets the ordinal tombstone (the route is gone, so nothing can
// replay into it), while Abort keeps it (a replay after a fault must not revive
// the frame that faulted).

// Expire times out every assembly older than the timeout and returns their
// faults. It is called at the top of Accept and can also be driven on a timer by
// the consumer.
func (a *Assembler) Expire(nowMs int64) []Event {
	var events []Event
	for key, assembly := range a.assemblies {
		if nowMs-assembly.firstSeenAt < a.timeoutMs {
			continue
		}
		a.release(key, assembly)
		a.settle(key, assembly.logicalFrameID, assembly.logicalFrameOrdinal)
		events = append(events, a.faultOnAssembly(assembly, ReasonAssemblyTimedOut))
	}
	return events
}

// Discard drops a route's staged bytes AND its tombstone. Only a full
// unsubscription uses this: the tombstone exists to stop a replayed old frame
// from resurrecting a settled one, which is meaningless once the route is gone.
func (a *Assembler) Discard(topic, subscriptionID string) {
	key := routeKey(topic, subscriptionID)
	if assembly := a.assemblies[key]; assembly != nil {
		a.release(key, assembly)
	}
	delete(a.settledByRoute, key)
}

// Abort releases a route's active bytes but KEEPS the ordinal tombstone, so a
// replay after a fault cannot revive the frame that faulted.
func (a *Assembler) Abort(topic, subscriptionID string) {
	key := routeKey(topic, subscriptionID)
	assembly := a.assemblies[key]
	if assembly == nil {
		return
	}
	a.release(key, assembly)
	a.settle(key, assembly.logicalFrameID, assembly.logicalFrameOrdinal)
}

// Clear resets everything, including tombstones.
func (a *Assembler) Clear() {
	a.assemblies = map[string]*fragmentAssembly{}
	a.settledByRoute = map[string]settled{}
	a.stagedDecodedBytes = 0
}

// Stats is the consumer-visible accounting the vectors assert on.
type Stats struct {
	Assemblies         int
	StagedDecodedBytes int
}

// Stats reports the current assembly count and staged bytes.
func (a *Assembler) Stats() Stats {
	return Stats{Assemblies: len(a.assemblies), StagedDecodedBytes: a.stagedDecodedBytes}
}

// NextExpiryAt is the earliest time an in-progress assembly would time out, or
// nil when nothing is in progress. It lets the consumer arm one timer instead of
// polling.
func (a *Assembler) NextExpiryAt() *int64 {
	var next *int64
	for _, assembly := range a.assemblies {
		expiresAt := assembly.firstSeenAt + a.timeoutMs
		if next == nil || expiresAt < *next {
			v := expiresAt
			next = &v
		}
	}
	return next
}

func (a *Assembler) release(key string, assembly *fragmentAssembly) {
	if a.assemblies[key] != assembly {
		return
	}
	delete(a.assemblies, key)
	a.stagedDecodedBytes -= assembly.decodedBytes
}

func (a *Assembler) settle(key, logicalFrameID string, ordinal int64) {
	if prev, ok := a.settledByRoute[key]; ok && prev.logicalFrameOrdinal > ordinal {
		return
	}
	a.settledByRoute[key] = settled{logicalFrameID: logicalFrameID, logicalFrameOrdinal: ordinal}
}

// releaseAndSettle releases the active assembly only when it belongs to the same
// (id, ordinal) as the faulting frame, then records the tombstone.
func (a *Assembler) releaseAndSettle(key string, wire Candidate) {
	if active := a.assemblies[key]; active != nil &&
		active.logicalFrameOrdinal == wire.LogicalFrameOrdinal &&
		active.logicalFrameID == wire.LogicalFrameID {
		a.release(key, active)
	}
	a.settle(key, wire.LogicalFrameID, wire.LogicalFrameOrdinal)
}

// fault builds a typed fault from a wire candidate, reporting deliveryKind only
// when it is one of the three valid values.
func (a *Assembler) fault(wire Candidate, reason string) Event {
	f := Fault{
		ReasonCode:          reason,
		LogicalFrameID:      wire.LogicalFrameID,
		LogicalFrameOrdinal: wire.LogicalFrameOrdinal,
		Topic:               wire.Topic,
		SubscriptionID:      wire.SubscriptionID,
	}
	if kind, ok := deliveryKindValid(wire.DeliveryKind, wire.HasDeliveryKind); ok {
		f.HasDeliveryKind, f.DeliveryKind = true, kind
	}
	return Event{IsFault: true, Fault: f}
}

// faultOnAssembly builds a fault from a released in-progress assembly.
func (a *Assembler) faultOnAssembly(assembly *fragmentAssembly, reason string) Event {
	return Event{IsFault: true, Fault: Fault{
		HasDeliveryKind:     true,
		DeliveryKind:        assembly.deliveryKind,
		ReasonCode:          reason,
		LogicalFrameID:      assembly.logicalFrameID,
		LogicalFrameOrdinal: assembly.logicalFrameOrdinal,
		Topic:               assembly.topic,
		SubscriptionID:      assembly.subscriptionID,
	}}
}

// routeKey identifies one (topic, subscription) route. The NUL separator cannot
// occur in either half, so two distinct routes can never collide into one key.
func routeKey(topic, subscriptionID string) string { return topic + "\x00" + subscriptionID }
