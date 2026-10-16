package agentscope

import (
	"context"
	"encoding/json"
)

// subscriptions.go holds the three topic families' subscribe / unsubscribe / resync
// rules. They are ONE implementation on purpose: the families differ only in the
// topic key, and the ownership bookkeeping that the TS spreads across three nearly
// identical method bodies is exactly the code that must not drift between them.

// Subscribe opens one subscription for this connection and registers its
// ownership. It is the one implementation for all three families: the kind only
// changes the topic key.
func (s *Scope) Subscribe(ctx context.Context, kind Kind, params json.RawMessage) (*SubscribeAck, error) {
	if err := s.handshake.assertReady(s.isDisposed()); err != nil {
		return nil, err
	}
	incoming, err := decodeParams(params)
	if err != nil {
		return nil, err
	}
	forwarded := s.call(incoming)
	args := incoming.subscribeArgs()
	topic := kind.Topic(args.SessionID, incoming.target().WorkspaceKey())
	pending := s.owned.beginPending(kind, incoming.target(), topic)

	ack, err := s.base.SubscribeTopic(ctx, kind, forwarded, args)
	if err != nil {
		s.owned.removePending(pending)
		pending.discard()
		return nil, err
	}
	if err := s.rememberAfterSubscribe(kind, ack.SubscriptionID, forwarded.Target, topic, forwarded.Trusted, pending); err != nil {
		return nil, err
	}
	return ack, nil
}

// rememberAfterSubscribe registers the owner and replays the staged frames. The
// ordering here is the load-bearing part:
//
//  1. runtime restart wins over a late ACK: if the generation moved, drop the local
//     owner and refuse — do NOT unsubscribe against the new runtime, which may have
//     reused the same subscription id.
//  2. a close that raced the ACK wins next: a late ACK must not re-register an owner
//     on a disposed facade, so it is reverse-unsubscribed immediately.
//  3. a staged overflow fails the subscribe loudly, for the same reason: a partial
//     batch would leave a permanent hole.
//  4. only then is the owner registered, the CURRENT flow state replayed (a
//     saturated transport must be told about the new route without waiting for the
//     next high-water edge), and the staged frames fired in arrival order.
func (s *Scope) rememberAfterSubscribe(kind Kind, subscriptionID string, target Target, topic string, trusted *TrustedConnection, pending *pendingOwnership) error {
	s.owned.removePending(pending)
	complete := &ownedEntry{
		kind:           kind,
		ownershipKey:   s.ownershipKeyFor(kind, target, topic, trusted),
		subscriptionID: subscriptionID,
		topic:          topic,
		connectionID:   trusted.ConnectionID,
		clientMode:     trusted.ClientMode,
		target:         target,
	}
	if pending.isInvalidated() || pending.generation() != s.owned.generation(target.WorkspaceKey()) {
		// 旧 runtime 的迟到 ACK 可能与新 runtime 复用同一 subId。这里只丢本地
		// owner，绝不能向新 runtime 反向 unsubscribe 同名 subscription。
		pending.discard()
		return &Fault{Code: FaultRuntimeRestarted}
	}
	if s.isDisposed() {
		// port close 与 subscribe ACK 可并发；迟到 ACK 不能在已关闭 facade 重新
		// 登记 owner，必须立即反向 unsubscribe。
		pending.discard()
		s.owned.removePending(pending)
		s.unsubscribeBase(context.Background(), kind, complete, "")
		return &Fault{Code: FaultClosed}
	}
	if pending.isOverflow() {
		pending.discard()
		s.owned.removePending(pending)
		s.unsubscribeBase(context.Background(), kind, complete, "")
		return &Fault{Code: FaultFrameStagingOverflow}
	}
	s.owned.remember(complete)
	if err := s.syncCurrentFlowForEntry(complete); err != nil {
		s.owned.forget(complete)
		s.unsubscribeBase(context.Background(), kind, complete, "")
		return err
	}
	// CLI notification 可能先于 RPC subscribe response 抵达 host。旧实现直到
	// await 返回才登记 owner，早帧会被永久丢弃；同 topic 双端 pending 时也不能猜
	// owner。ACK 后只释放 ACK subscriptionId 对应帧，保留原到达顺序。
	for _, frame := range pending.takeFrames() {
		if frame.SubscriptionID == subscriptionID && s.owned.ownsFrame(kind, target, frame) {
			s.fire(frame.Topic, frame)
		}
	}
	return nil
}

func (s *Scope) ownershipKeyFor(kind Kind, target Target, topic string, trusted *TrustedConnection) string {
	if kind == KindConversation {
		return joinKey(string(kind), target.WorkspaceKey(), topic, trusted.ConnectionID)
	}
	return joinKey(string(kind), topic, trusted.ConnectionID)
}

func (s *Scope) generation(workspace string) int64 {
	s.owned.mu.Lock()
	defer s.owned.mu.Unlock()
	return s.owned.runtimeGeneration[workspace]
}

// Unsubscribe tears one owned subscription down. The close of its flow route goes
// out BEFORE the unsubscribe (see unsubscribeOwnedEntry), because the reverse order
// makes the upstream forget the owner first and reject the close.
func (s *Scope) Unsubscribe(ctx context.Context, kind Kind, params json.RawMessage) error {
	incoming, err := decodeParams(params)
	if err != nil {
		return err
	}
	args := incoming.unsubscribeArgs()
	route := s.forwardedUnsubscribeRoute(incoming)
	entry := s.owned.findBySubscription(kind, incoming.target(), args.SubscriptionID, route)
	if entry == nil {
		// Nothing owned: a terminal UI cleanup that races a runtime restart lands
		// here, and it is a no-op rather than an error.
		return nil
	}
	return s.unsubscribeOwned(ctx, kind, entry, args.RuntimePolicy)
}

func (s *Scope) unsubscribeOwned(ctx context.Context, kind Kind, entry *ownedEntry, runtimePolicy string) error {
	s.owned.forget(entry)
	// trusted relay 以 owned route 校验 flow control。最后一个 subscription 必须先
	// closed 再 unsubscribe；反序会让上游先忘 owner，closed 被拒绝后留下 paused
	// connection。
	_ = s.flow.closeUnused(s.routeForEntry(entry), s.routeStillUsed(entry))
	s.unsubscribeBase(ctx, kind, entry, runtimePolicy)
	return nil
}

// unsubscribeBase sends the teardown with the entry's OWN trusted identity and the
// exact (topic, connectionId) route, never the caller's. A failed teardown is
// swallowed here (the caller is already unwinding), but it is never silently
// dropped: the ownership is already forgotten locally.
func (s *Scope) unsubscribeBase(ctx context.Context, kind Kind, entry *ownedEntry, runtimePolicy string) {
	route := UnsubscribeRoute{Topic: entry.topic, ConnectionID: entry.connectionID}
	call := Forwarded{
		Fields:  map[string]json.RawMessage{"subscriptionId": mustJSON(entry.subscriptionID)},
		Target:  entry.target,
		Trusted: &TrustedConnection{ConnectionID: entry.connectionID, ClientMode: entry.clientMode},
		Route:   &route,
	}
	_ = s.base.UnsubscribeTopic(ctx, kind, call,
		UnsubscribeArgs{SubscriptionID: entry.subscriptionID, RuntimePolicy: runtimePolicy}, route)
}

func (s *Scope) routeForEntry(entry *ownedEntry) flowRoute {
	trusted := TrustedConnection{ConnectionID: entry.connectionID, ClientMode: entry.clientMode}
	return flowRoute{
		key:     flowRouteKey{workspace: entry.target.WorkspaceKey(), connectionID: entry.connectionID},
		target:  entry.target,
		trusted: trusted,
	}
}

// routeStillUsed reports whether any other owned entry or attachment still uses a
// route, so only the LAST teardown closes it.
func (s *Scope) routeStillUsed(entry *ownedEntry) bool {
	key := flowRouteKey{workspace: entry.target.WorkspaceKey(), connectionID: entry.connectionID}
	return s.owned.ownsRoute(key.workspace, key.connectionID)
}

// Resync re-arbitrates an owned subscription from the caller's watermark. It is
// refused for a subscription this connection does not own (fault.subscription.notOwned).
func (s *Scope) Resync(ctx context.Context, kind Kind, params json.RawMessage) (json.RawMessage, error) {
	if err := s.handshake.assertReady(s.isDisposed()); err != nil {
		return nil, err
	}
	incoming, err := decodeParams(params)
	if err != nil {
		return nil, err
	}
	args := incoming.resyncArgs()
	route := s.forwardedUnsubscribeRoute(incoming)
	entry := s.owned.findBySubscription(kind, incoming.target(), args.SubscriptionID, route)
	if entry == nil {
		return nil, &Fault{Code: FaultNotOwned}
	}
	forwarded := s.call(incoming)
	forwarded.Route = route
	return s.base.ResyncTopic(ctx, kind, forwarded, args)
}
