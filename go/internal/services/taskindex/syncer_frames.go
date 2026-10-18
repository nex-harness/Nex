package taskindex

// syncer_frames.go — the frame-application half of the syncer: ownership gate,
// watermark checks, gap detection and the summary diff that decides which write
// a frame produces. See syncer_subscribe.go for the subscription lifecycle.

import (
	"context"

	adapter "github.com/tylinux/nex/go/internal/adapters/taskindex"
	"sort"
)

// onFrame ports handleSessionsIndexWire plus applySessionsIndexFrame.
//
// The ownership gate comes FIRST: a frame for another subscription must never
// consume the decoded staging buffer, and a frame arriving before the subscribe
// ACK cannot be interpreted as belonging to the new subscription.
func (s *Syncer) onFrame(ctx context.Context, state *ingestState, event SessionFrameEvent) {
	s.mu.Lock()
	if s.disposed {
		s.mu.Unlock()
		return
	}
	live := s.ingests[state.target.Key()] == state
	s.mu.Unlock()
	if !live {
		return
	}
	if event.FaultReasonCode != "" {
		s.handleGap(ctx, state, deliveryKindOf(event.DeliveryKind))
		return
	}
	frame, err := event.Frame.Logical()
	if err != nil {
		s.handleGap(ctx, state, deliveryKindOf(event.DeliveryKind))
		return
	}
	if frame.Topic != SessionsIndexTopic(state.target.Key()) {
		return
	}
	state.mu.Lock()
	subscriptionID := state.subscriptionID
	state.mu.Unlock()
	// A fresh task-index subscribe never carries a base. Without an owned
	// subscriptionId the frame belongs to no one yet: only a recovery frame
	// (which the agent sent for our route) is acted on.
	if subscriptionID == "" || frame.SubscriptionID != subscriptionID {
		return
	}
	s.applyFrame(ctx, state, frame, event.DeliveryKind)
}

func deliveryKindOf(kind string) string {
	if kind == DeliveryInitial || kind == DeliveryRecovery {
		return kind
	}
	return DeliveryOnline
}

// applyFrame ports applySessionsIndexFrame.
func (s *Syncer) applyFrame(ctx context.Context, state *ingestState, frame SessionsIndexFrame, deliveryKind string) {
	state.mu.Lock()
	if deliveryKind == DeliveryOnline && state.recovery != nil && frame.Kind == "deltas" {
		if state.recovery.frameApplied && frame.ToSeq > state.seq {
			state.recovery.postRecoveryGapPending = true
		}
		state.mu.Unlock()
		return
	}
	if frame.Kind == "snapshot" {
		// An online snapshot that an owned newer state already covers is a
		// duplicate; replaying it would diff against itself.
		if deliveryKind == DeliveryOnline && state.hasAppliedBase && frame.ToSeq <= state.seq {
			state.mu.Unlock()
			return
		}
		if frame.Snapshot != nil {
			state.logEpoch = frame.Snapshot.LogEpoch
		}
		state.seq = frame.ToSeq
		state.hasAppliedBase = true
		next := make(map[string]SessionSummary, len(frame.Snapshot.Sessions))
		for _, summary := range frame.Snapshot.Sessions {
			next[summary.SessionID] = summary
		}
		if !state.seeded {
			// 首帧 = 静默基线：不回放历史终态、不发列表广播；仅原子补齐缺失行，
			// 防止远端/新安装的空 sqlite 因纯 V4 路径永远没有存量。
			state.summaries = next
			state.seeded = true
			state.mu.Unlock()
			go s.seedMissingRows(ctx, state, frame.Snapshot.Sessions)
			s.completeRecoveryFrame(state, deliveryKind)
			return
		}
		// 断档降级 snapshot：对基线 diff 后处理，等价于补投丢失的 deltas（conflated 语义）。
		previous := state.summaries
		state.summaries = map[string]SessionSummary{}
		ordered := make([]SessionSummary, 0, len(next))
		for _, summary := range next {
			ordered = append(ordered, summary)
		}
		sort.Slice(ordered, func(i, j int) bool { return ordered[i].SessionID < ordered[j].SessionID })
		state.mu.Unlock()
		for _, summary := range ordered {
			prev, had := previous[summary.SessionID]
			var prevPtr *SessionSummary
			if had {
				prevPtr = &prev
			}
			s.processSummary(ctx, state, prevPtr, summary)
		}
		s.completeRecoveryFrame(state, deliveryKind)
		return
	}
	if !state.hasAppliedBase {
		state.mu.Unlock()
		// fresh task-index subscribe 从不携带 base；initial 整批丢失后任何 delta 都不能
		// 建立 cold baseline。只有 owned snapshot 可证明完整状态。
		s.handleGap(ctx, state, deliveryKind)
		return
	}
	if frame.ToSeq <= state.seq {
		// 任一完整校验的 recovery 若已被更晚权威 snapshot/online 覆盖，都可确认
		// delivery 成功；普通重复帧仍静默丢弃。
		state.mu.Unlock()
		s.completeRecoveryFrame(state, deliveryKind)
		return
	}
	if frame.FromSeq != state.seq {
		state.mu.Unlock()
		s.handleGap(ctx, state, deliveryKind)
		return
	}
	deltas := frame.Deltas
	prevSummaries := make(map[string]SessionSummary, len(deltas))
	for _, delta := range deltas {
		if delta.Op == "session.upserted" && delta.Session != nil {
			prevSummaries[delta.Session.SessionID] = state.summaries[delta.Session.SessionID]
		}
	}
	state.seq = frame.ToSeq
	state.hasAppliedBase = true
	state.mu.Unlock()
	for _, delta := range deltas {
		switch delta.Op {
		case "session.upserted":
			if delta.Session == nil {
				continue
			}
			prev, had := prevSummaries[delta.Session.SessionID]
			var prevPtr *SessionSummary
			if had {
				prevPtr = &prev
			}
			s.processSummary(ctx, state, prevPtr, *delta.Session)
		case "session.removed":
			// session.removed：会话删除的 sqlite 收口走 task 删除操作
			// （adapter deleteTask / v4 deleteSession 命令的 host 侧收尾），
			// 这里只维护基线。
			state.mu.Lock()
			delete(state.summaries, delta.SessionID)
			state.mu.Unlock()
		}
	}
	s.completeRecoveryFrame(state, deliveryKind)
}

// processSummary ports processSummary: one summary diffed against the baseline.
func (s *Syncer) processSummary(ctx context.Context, state *ingestState, previous *SessionSummary, next SessionSummary) {
	state.mu.Lock()
	state.summaries[next.SessionID] = next
	state.mu.Unlock()
	// draft 裁决：纯内存态、不落盘，也绝不进 task index sqlite。
	if next.Phase == PhaseDraft {
		return
	}
	target := SessionTarget{
		WorkspacePath:     state.target.WorkspacePath,
		WorkspaceIdentity: state.target.WorkspaceIdentity,
		SessionID:         next.SessionID,
	}
	becameVisible := previous == nil || previous.Phase == PhaseDraft
	// 终态迁移 = 基线里真实观察到非终态 → 终态。无基线的会话（冷恢复 hydration、
	// 断档降级后新出现的历史会话）不回放终态。
	becameTerminal := previous != nil && !isTerminalPhase(previous.Phase) && isTerminalPhase(next.Phase)
	if becameTerminal {
		s.applyTerminalTransition(ctx, state, target, next, becameVisible)
		return
	}
	if becameVisible {
		// v4 预热 session 从 draft 提升，或 online delta 首次出现新 session 时，
		// 不经过 createSession。此处是最早且不依赖标题时序的新任务边界；立即回源写入
		// task 行与 grouped root 最小 sort_order，避免缺序节点落到末尾。
		go s.resyncRow(ctx, state, target, "session.became-visible", true, "")
		return
	}
	title := adapter.TrimSpace(next.Title)
	if title != "" && (previous == nil || previous.Title != next.Title) {
		s.applyTitleChange(ctx, state, target, title)
	}
}

// applyTerminalTransition ports applyTerminalTransition: sqlite status
// convergence + broadcast + full-snapshot re-read for the body index.
func (s *Syncer) applyTerminalTransition(ctx context.Context, state *ingestState, target SessionTarget, summary SessionSummary, moveGroupedTaskToTop bool) {
	failed := summary.Phase == PhaseError
	unreadSignal := terminalUnreadSignal(summary.Phase, summary.GoalStatus)
	// 顺序保持旧协议语义：先 turn 终态（收口当前 input），再 prompt ready（放行下一条）。
	s.emitTerminalAndReady(target, summary)
	updatedAt := s.now()
	patch := AgentPatch{UpdatedAt: &updatedAt}
	if failed {
		patch.Status = adapter.StrPtr("error")
	} else {
		patch.Status = adapter.StrPtr("completed")
		// error 的 lastError 详情不在 sessions-index 摘要里，留给随后的回源 snapshot
		// 写权威值；completed 沿旧语义清空。
		patch.ClearLastError = true
	}
	// patch 不带 searchableText：indexed 正文必须保留，不能被摘要写路径清空。
	meta, err := s.repo.ApplyAgentPatch(ctx, target.Ref(), patch)
	if err != nil {
		return
	}
	if meta != nil {
		// 之前只更新 sqlite 不广播，UI 监听 workspace_task_list_changed 收不到通知，
		// 导致 spinner 不消失、updatedAt 排序不刷新。终态收敛必须用
		// task_status_changed；落缺省 task_meta_changed 会让每次 turn 完成都全局
		// bump membership 版本号，表现为"任务结束左侧列表闪一下"。
		s.fireWorkspaceEvent(target.Workspace(), target.SessionID, meta,
			ReasonTaskStatusChanged, unreadSignal)
	}
	// 终态读取完整 snapshot：v4 命令路径不经过 op 驱动的 snapshot 同步，行缺失/正文
	// 搜索/lastError 全靠这里收敛；ReadSession 只读现有 runtime，不重新恢复 session。
	// patch 已广播时不能让随后的 snapshot 回源再次制造完成提醒；行缺失时则把同一
	// signal 交给回源结果，保证提醒既不丢也不重复。
	s.resyncRow(ctx, state, target, "phase-terminal", moveGroupedTaskToTop, unreadSignal)
}

// applyTitleChange ports applyTitleChange.
func (s *Syncer) applyTitleChange(ctx context.Context, state *ingestState, target SessionTarget, title string) {
	updatedAt := s.now()
	meta, err := s.repo.ApplyAgentPatch(ctx, target.Ref(), AgentPatch{Title: &title, UpdatedAt: &updatedAt})
	if err != nil {
		return
	}
	if meta != nil {
		// 标题变更（首条消息/自动标题生成）与归属无关且每个任务必发，用专属 reason，
		// 避免每次首发/收口标题落盘都触发全局 membership 重拉。
		s.fireWorkspaceEvent(target.Workspace(), target.SessionID, meta, ReasonTaskTitleChanged, "")
		return
	}
	// draft session 不预写占位行；首个标题先于任何 snapshot upsert 到达时按完整
	// snapshot 回源，避免依赖空 session 占位行。回源时必须同时写 grouped 顶层最小
	// sort_order，否则缺序节点会被客户端补到列表末尾。
	s.resyncRow(ctx, state, target, "meta.titleUpdated", true, "")
}

// resyncRow ports resyncTaskIndexRowFromAgent + syncSnapshotAndBroadcast.
func (s *Syncer) resyncRow(ctx context.Context, state *ingestState, target SessionTarget, reason string, moveGroupedTaskToTop bool, unreadSignal string) {
	snapshot, err := s.agent.ReadSession(ctx, target)
	if err != nil {
		// task-index 是被动观察者，读不到 runtime 就保持现状；下一帧会再试。
		return
	}
	// 旧 child 标签页的显式 resume 仍会同步快照；只读详情不能重新写成主任务。
	if snapshot.SessionKind == "subagent_child" {
		return
	}
	meta, unread := s.metaFromSnapshot(state.target, snapshot)
	if meta.TaskID == "" {
		return
	}
	var searchableText *string
	if snapshot.SearchableText != "" {
		text := snapshot.SearchableText
		searchableText = &text
	}
	var persisted TaskMeta
	var initializedGroupedOrder bool
	if moveGroupedTaskToTop {
		// desktop-continuous 首发若先提交 task row、再补 grouped sort_order，
		// sessions-index 会在两次写之间把缺序 task 暴露给 Renderer，产生先到底部
		// 再回顶部的跳动。
		persisted, initializedGroupedOrder, err = s.repo.SyncTaskMetaAtGroupedTop(ctx, meta, searchableText)
	} else {
		persisted, err = s.repo.SyncTaskMeta(ctx, meta, searchableText)
	}
	if err != nil {
		return
	}
	// createSession 刚回来时 snapshot 既没有 title 也没有 user message，默认 title
	// 会落成 "New session" 占位符。这种"还没有任何用户内容"的快照不应该广播给 UI，
	// 否则侧边栏会先闪一下 "New session"。sqlite 行仍然要写，让后续标题更新能找到对应行。
	if !snapshot.HasUserVisibleContent {
		if initializedGroupedOrder {
			// 即使暂时没有可广播的 task meta，也必须通知 renderer 重拉 structure；
			// 否则 sessions-index 已显示 task、structure 仍缺序，当前进程会把它补到末尾。
			s.fireWorkspaceEvent(WorkspaceTarget{
				WorkspacePath:     persisted.WorkspacePath,
				WorkspaceIdentity: adapter.Deref(persisted.WorkspaceIdentity),
			}, persisted.TaskID, nil, ReasonTaskCreated, "")
		}
		return
	}
	reason2 := ReasonTaskStatusChanged
	if initializedGroupedOrder {
		// 首次初始化必须用 task_created 通知运行中的 grouped structure 缓存失效；
		// 重复 snapshot 没有新增顺序，仍保留调用方的 status 语义。
		reason2 = ReasonTaskCreated
	}
	s.fireWorkspaceEvent(WorkspaceTarget{
		WorkspacePath:     persisted.WorkspacePath,
		WorkspaceIdentity: adapter.Deref(persisted.WorkspaceIdentity),
	}, persisted.TaskID, &persisted, reason2, unread)
	_ = unread
}

// seedMissingRows ports seedMissingRowsFromInitialSnapshot: the silent baseline
// fills only missing rows, in bounded batches, so a workspace with a long history
// cannot spawn a write per session at once.
func (s *Syncer) seedMissingRows(ctx context.Context, state *ingestState, summaries []SessionSummary) {
	const batchSize = 64
	candidates := make([]SessionSummary, 0, len(summaries))
	for _, summary := range summaries {
		if summary.Phase == PhaseDraft {
			continue
		}
		candidates = append(candidates, summary)
	}
	if len(candidates) == 0 {
		return
	}
	for offset := 0; offset < len(candidates); offset += batchSize {
		end := min(offset+batchSize, len(candidates))
		for _, summary := range candidates[offset:end] {
			if _, err := s.repo.SeedTaskMetaIfMissing(ctx, baselineMetaFromSummary(state.target, summary)); err != nil {
				// 失败只汇总，不逐会话打印，避免日志风暴。
				return
			}
		}
	}
}
