package taskindex

// syncer_subscribe.go — the subscription lifecycle: subscribe, gap detection,
// same-sub resync and retry.
//
// Why so much machinery for one topic: the subscribe ACK is only ADMISSION. The
// initial frame may still be incomplete, and a fresh task-index subscribe never
// carries a base. Until a logical frame is atomically applied there is no
// provable resume point, so a gap must escalate to a snapshot rather than
// silently stitching deltas over a hole.

import (
	"context"
	"time"
)

// subscribeIndexTopic ports subscribeIndexTopic.
func (s *Syncer) subscribeIndexTopic(ctx context.Context, state *ingestState, reason string, unsubscribeActive bool) {
	if !s.live(state) {
		return
	}
	state.mu.Lock()
	state.retryAttempt = 0
	state.lastWarnAt = 0
	previousSubscriptionID := state.subscriptionID
	state.subscriptionID = ""
	state.subscriptionGeneration++
	generation := state.subscriptionGeneration
	state.recovery = nil
	state.mu.Unlock()

	if unsubscribeActive && previousSubscriptionID != "" {
		if err := s.agent.UnsubscribeSessionsIndex(ctx, state.target, previousSubscriptionID); err != nil {
			// 清理失败只记录：新一轮订阅已经建立，旧 route 由 agent 侧换代回收。
			s.logSubscribeFailure(state, reason, err)
		}
		if !s.live(state) || generation != state.currentGeneration() {
			return
		}
	}
	result, err := s.agent.SubscribeSessionsIndex(ctx, state.target, "background", "task-index")
	if err != nil {
		s.onSubscribeError(state, reason, err, generation)
		return
	}
	s.activateSubscribe(ctx, state, generation, result)
}

// activateSubscribe ports activateIndexSubscribe.
func (s *Syncer) activateSubscribe(ctx context.Context, state *ingestState, generation int64, result SubscribeResult) {
	state.mu.Lock()
	stale := !s.live(state) || state.subscriptionGeneration != generation
	if stale {
		// runtime 换代可能复用 subscriptionId；迟到旧 ACK 不能反向退订已由新代
		// 接管的同 id route。
		state.mu.Unlock()
		if state.currentSubscriptionID() != result.Ack.SubscriptionID {
			go func() {
				_ = s.agent.UnsubscribeSessionsIndex(context.Background(), state.target, result.Ack.SubscriptionID)
			}()
		}
		return
	}
	state.subscriptionID = result.Ack.SubscriptionID
	state.logEpoch = result.Ack.LogEpoch
	state.seq = 0
	// subscribe ACK 的 epoch/seq0 只是 admission metadata；initial frame 可能尚未
	// 齐片或校验失败。只有 logical snapshot/delta 原子 apply 后才有 base。
	state.hasAppliedBase = false
	state.mu.Unlock()
}

// onSubscribeError ports the subscribe catch block: a gone runtime just parks the
// workspace; a provider that is not configured yet is a normal wait; anything
// else backs off per topic.
func (s *Syncer) onSubscribeError(state *ingestState, reason string, err error, generation int64) {
	if !s.live(state) || state.currentGeneration() != generation {
		return
	}
	code := errorCode(err)
	if code == CodeRuntimeUnavailable {
		state.mu.Lock()
		state.runtimeGeneration = -1
		state.mu.Unlock()
		return
	}
	kind := retryTransient
	if code == CodeProviderNotReady {
		kind = retryProviderNotReady
	}
	s.logSubscribeFailure(state, reason, err)
	s.scheduleRetry(state, kind)
}

// scheduleRetry ports scheduleIndexRetry: one timer per topic, exponential to a
// 1 s cap; the provider-not-ready wait is a flat 5 s because the user is
// configuring a model, not recovering from a fault.
func (s *Syncer) scheduleRetry(state *ingestState, kind retryKind) {
	if !s.live(state) {
		return
	}
	state.mu.Lock()
	if state.retryTimer != nil {
		state.mu.Unlock()
		return
	}
	attempt := state.retryAttempt
	state.retryAttempt++
	state.mu.Unlock()

	delay := time.Duration(min(1000, 25<<min(attempt, 5))) * time.Millisecond
	if kind == retryProviderNotReady {
		delay = providerNotReadyRetryMs * time.Millisecond
	}
	timer := &retryTimer{kind: kind}
	state.mu.Lock()
	state.retryTimer = timer
	state.mu.Unlock()
	s.schedule(delay, func() {
		state.mu.Lock()
		if state.retryTimer != timer {
			state.mu.Unlock()
			return
		}
		state.retryTimer = nil
		state.mu.Unlock()
		if !s.live(state) {
			return
		}
		go s.subscribeIndexTopic(context.Background(), state, "retry", false)
	})
}

// logTopicSubscribeFailure ports logTopicSubscribeFailure: a not-ready provider
// is debug-only and a persistently failing topic warns at most once a minute.
func (s *Syncer) logSubscribeFailure(state *ingestState, reason string, err error) {
	if errorCode(err) == CodeProviderNotReady {
		return
	}
	state.mu.Lock()
	now := s.now()
	shouldWarn := state.lastWarnAt == 0 || now-state.lastWarnAt >= topicSubscribeWarnIntervalMs
	if shouldWarn {
		state.lastWarnAt = now
	}
	state.mu.Unlock()
}

// handleGap ports handleIndexGap.
func (s *Syncer) handleGap(ctx context.Context, state *ingestState, deliveryKind string) {
	if deliveryKind == DeliveryRecovery {
		state.mu.Lock()
		recovery := state.recovery
		state.mu.Unlock()
		if recovery == nil {
			return
		}
		if recovery.forceSnapshot {
			// 强制 snapshot 仍断档说明当前 same-sub 流已不可证，不能继续猜测拼接，
			// 改为该 topic 的新代 snapshot 订阅。
			state.mu.Lock()
			state.recovery = nil
			state.mu.Unlock()
			go s.subscribeIndexTopic(ctx, state, "force-recovery-gap", true)
			return
		}
		go s.requestRecovery(ctx, state, true)
		return
	}
	go s.requestRecovery(ctx, state, false)
}

// requestRecovery ports requestIndexRecovery.
func (s *Syncer) requestRecovery(ctx context.Context, state *ingestState, forceSnapshot bool) {
	state.mu.Lock()
	subscriptionID := state.subscriptionID
	if subscriptionID == "" {
		state.mu.Unlock()
		return
	}
	active := state.recovery
	if active != nil {
		if forceSnapshot && !active.forceSnapshot {
			active.upgradeToSnapshot = true
		}
		state.mu.Unlock()
		return
	}
	// 只有在确实持有该时刻一致状态时才允许带 base；否则必须强制 snapshot。
	effectiveForce := forceSnapshot || !state.hasAppliedBase || state.logEpoch == ""
	recovery := &recoveryState{
		generation:    1,
		forceSnapshot: effectiveForce,
	}
	state.recovery = recovery
	var base *ResumeBase
	if !effectiveForce {
		base = &ResumeBase{LogEpoch: state.logEpoch, Seq: state.seq}
	}
	state.mu.Unlock()
	if !s.live(state) {
		return
	}
	result, err := s.agent.ResyncSessionsIndex(ctx, state.target, subscriptionID, base, effectiveForce)
	if err != nil {
		state.mu.Lock()
		if state.recovery == recovery {
			state.recovery = nil
		}
		state.mu.Unlock()
		go s.subscribeIndexTopic(ctx, state, "resync-failed", true)
		return
	}
	state.mu.Lock()
	if state.recovery != recovery {
		state.mu.Unlock()
		return
	}
	if state.subscriptionID != subscriptionID || result.Ack.SubscriptionID != subscriptionID {
		// An ACK for a route we no longer own means the runtime rotated under us.
		state.recovery = nil
		state.mu.Unlock()
		go s.subscribeIndexTopic(ctx, state, "resync-ack-mismatch", true)
		return
	}
	recovery.ackReceived = true
	if result.Ack.Mode == "snapshot" {
		recovery.forceSnapshot = true
	}
	state.mu.Unlock()
}

// completeRecoveryFrame ports completeIndexRecoveryFrame.
func (s *Syncer) completeRecoveryFrame(state *ingestState, deliveryKind string) {
	if deliveryKind != DeliveryRecovery {
		return
	}
	state.mu.Lock()
	recovery := state.recovery
	if recovery == nil {
		state.mu.Unlock()
		return
	}
	if !recovery.frameApplied {
		recovery.frameApplied = true
	}
	upgrade := recovery.upgradeToSnapshot
	followup := recovery.postRecoveryGapPending
	// ACK 只证明 admission；首个 logical frame 原子 apply 后才允许把 epoch/seq
	// 当 resume base，所以没有 ACK 时不清理 recovery。
	if !recovery.ackReceived {
		state.mu.Unlock()
		return
	}
	state.recovery = nil
	state.mu.Unlock()
	if upgrade {
		go s.subscribeIndexTopic(context.Background(), state, "snapshot-recovery-gap", true)
		return
	}
	if followup {
		go s.requestRecovery(context.Background(), state, false)
	}
}

func (s *Syncer) live(state *ingestState) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return isLiveState(s.ingests, s.disposed, state)
}

func (st *ingestState) currentGeneration() int64 {
	st.mu.Lock()
	defer st.mu.Unlock()
	return st.subscriptionGeneration
}

func (st *ingestState) currentSubscriptionID() string {
	st.mu.Lock()
	defer st.mu.Unlock()
	return st.subscriptionID
}
