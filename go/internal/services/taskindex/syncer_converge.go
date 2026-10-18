package taskindex

// syncer.go — the Syncer itself: wiring, the workspace event fan-out, the
// snapshot convergence entry points and disposal.

import (
	"context"

	"errors"
	"fmt"
	adapter "github.com/tylinux/nex/go/internal/adapters/taskindex"
)

// event reasons and emitter wiring live above; the following block holds the
// terminal/ready emission and the snapshot convergence entry points.

func (s *Syncer) emitTerminalAndReady(target SessionTarget, summary SessionSummary) {
	failed := summary.Phase == PhaseError
	s.mu.Lock()
	terminal := append([]func(TerminalEvent){}, s.terminalListeners...)
	ready := append([]func(ReadyEvent){}, s.readyListeners...)
	s.mu.Unlock()
	kind := "turn.completed"
	reason := "prompt_completed"
	if failed {
		kind = "turn.failed"
		reason = "prompt_failed"
	}
	for _, fn := range terminal {
		fn(TerminalEvent{Target: target, Kind: kind})
	}
	for _, fn := range ready {
		fn(ReadyEvent{Target: target, Reason: reason})
	}
}

// SyncTaskModel ports syncTaskModel: a model switch writes the index without a
// full snapshot, and without broadcasting a historical snapshot.
func (s *Syncer) SyncTaskModel(ctx context.Context, target SessionTarget, model string) (*TaskMeta, error) {
	trimmed := adapter.TrimSpace(model)
	if trimmed == "" {
		return nil, nil
	}
	meta, err := s.repo.UpdateTaskState(ctx, target.Ref(), StatePatch{Model: &trimmed})
	if err != nil {
		return nil, err
	}
	return &meta, nil
}

// SyncSnapshotAndBroadcast ports syncSnapshotAndBroadcast for the desktop
// continuous path: the session service calls this after create/resume/setModel.
func (s *Syncer) SyncSnapshotAndBroadcast(ctx context.Context, snapshot SessionSnapshot, options BroadcastOptions) (TaskMeta, error) {
	state := &ingestState{target: WorkspaceTarget{
		WorkspacePath:     snapshot.WorkspacePath,
		WorkspaceIdentity: snapshot.WorkspaceIdentity,
	}}
	persisted, err := s.syncOne(ctx, state, snapshot, options)
	return persisted, err
}

// BroadcastOptions is the required-reason options bag of syncSnapshotAndBroadcast.
type BroadcastOptions struct {
	MoveGroupedTaskToTop bool
	UnreadSignal         string
	// Reason is mandatory at every call site (see the reason constants).
	Reason string
}

func (s *Syncer) syncOne(ctx context.Context, state *ingestState, snapshot SessionSnapshot, options BroadcastOptions) (TaskMeta, error) {
	if snapshot.SessionKind == "subagent_child" {
		return TaskMeta{}, nil
	}
	meta, unread := s.metaFromSnapshot(state.target, snapshot)
	if meta.TaskID == "" {
		return TaskMeta{}, fmt.Errorf("snapshot has no usable session: %s", snapshot.SessionID)
	}
	var searchableText *string
	if snapshot.SearchableText != "" {
		text := snapshot.SearchableText
		searchableText = &text
	}
	var persisted TaskMeta
	var initializedGroupedOrder bool
	var err error
	if options.MoveGroupedTaskToTop {
		persisted, initializedGroupedOrder, err = s.repo.SyncTaskMetaAtGroupedTop(ctx, meta, searchableText)
	} else {
		persisted, err = s.repo.SyncTaskMeta(ctx, meta, searchableText)
	}
	if err != nil {
		return TaskMeta{}, err
	}
	if !snapshot.HasUserVisibleContent {
		if initializedGroupedOrder {
			s.fireWorkspaceEvent(state.target, persisted.TaskID, nil, ReasonTaskCreated, "")
		}
		return persisted, nil
	}
	reason := options.Reason
	if initializedGroupedOrder {
		reason = ReasonTaskCreated
	}
	unreadSignal := options.UnreadSignal
	if unreadSignal == "" {
		unreadSignal = unread
	}
	s.fireWorkspaceEvent(state.target, persisted.TaskID, &persisted, reason, unreadSignal)
	return persisted, nil
}

// metaFromSnapshot builds the meta from a session snapshot (buildMetaFromSnapshot).
func (s *Syncer) metaFromSnapshot(fallback WorkspaceTarget, snapshot SessionSnapshot) (TaskMeta, string) {
	workspacePath := snapshot.WorkspacePath
	if workspacePath == "" {
		workspacePath = fallback.WorkspacePath
	}
	identity := snapshot.WorkspaceIdentity
	if identity == "" {
		identity = fallback.WorkspaceIdentity
	}
	traceID := snapshot.TraceID
	if traceID == "" {
		traceID = "nex-" + snapshot.SessionID
	}
	meta := TaskMeta{
		TaskID:            snapshot.SessionID,
		TraceID:           traceID,
		Title:             snapshot.Title,
		WorkspacePath:     workspacePath,
		WorkspaceIdentity: adapter.IdentityOrNil(identity),
		CreatedAt:         snapshot.CreatedAt,
		UpdatedAt:         snapshot.UpdatedAt,
		Mode:              snapshot.Mode,
		Provider:          adapter.StrPtr(NexAgentProvider),
	}
	if snapshot.Model != "" {
		meta.Model = adapter.StrPtr(snapshot.Model)
	}
	if snapshot.Status != "" {
		meta.Status = adapter.StrPtr(snapshot.Status)
	}
	meta.LastError = snapshot.LastError
	unread := ""
	if snapshot.Status == "completed" || snapshot.Status == "error" {
		unread = "background_terminal"
	}
	return meta, unread
}

// onRuntimeLifecycle ports the runtimeLifecycleDisposable handler.
func (s *Syncer) onRuntimeLifecycle(event RuntimeLifecycleEvent) {
	s.mu.Lock()
	if s.disposed {
		s.mu.Unlock()
		return
	}
	if event.State == "unavailable" {
		delete(s.runtimeGenerations, event.WorkspaceKey)
		state := s.ingests[event.WorkspaceKey]
		if state != nil {
			state.mu.Lock()
			state.runtimeGeneration = -1
			// runtime 已退出后调用 unsubscribe/retry 会重新进入 getClient。
			// unavailable 只清本地 ownership；CLI 内订阅已随进程销毁，不再发送清理 RPC。
			state.subscriptionID = ""
			state.subscriptionGeneration++
			state.recovery = nil
			state.mu.Unlock()
		}
		s.mu.Unlock()
		return
	}
	s.runtimeGenerations[event.WorkspaceKey] = event.RuntimeIdentity.Generation
	existing := s.ingests[event.WorkspaceKey]
	if existing == nil {
		s.mu.Unlock()
		go s.EnsureWorkspaceSubscription(context.Background(), WorkspaceTarget{
			WorkspacePath:     event.WorkspacePath,
			WorkspaceIdentity: event.WorkspaceIdentity,
		})
		return
	}
	existing.mu.Lock()
	changed := existing.runtimeGeneration != event.RuntimeIdentity.Generation
	existing.runtimeGeneration = event.RuntimeIdentity.Generation
	existing.mu.Unlock()
	s.mu.Unlock()
	if !changed {
		return
	}
	// （CLI 重连重订）：agent 进程换代后，CLI 内存里的订阅全部丢失且不会有 gap 帧到达
	// （订阅静默失活），占位早退也不会重订。这里按 workspaceKey 重发 subscribe；帧监听
	// 挂在 workspace 级持久 emitter 上，只需刷新 subscriptionId 闸门。
	go s.subscribeIndexTopic(context.Background(), existing, "runtime-restart", false)
}

// DisposeAll ports disposeAll: release every subscription and listener. The
// injected repo is not closed — its owner is the caller.
func (s *Syncer) DisposeAll() {
	s.mu.Lock()
	if s.disposed {
		s.mu.Unlock()
		return
	}
	s.disposed = true
	states := make([]*ingestState, 0, len(s.ingests))
	for _, state := range s.ingests {
		states = append(states, state)
	}
	s.ingests = map[string]*ingestState{}
	emitters := s.emitters
	s.emitters = map[string]*workspaceEmitter{}
	s.terminalListeners = nil
	s.readyListeners = nil
	unsubscribeRuntime := s.unsubscribeRuntime
	s.unsubscribeRuntime = nil
	s.runtimeGenerations = map[string]int64{}
	s.mu.Unlock()

	for _, state := range states {
		state.mu.Lock()
		subscriptionID := state.subscriptionID
		state.subscriptionID = ""
		state.subscriptionGeneration++
		state.recovery = nil
		state.mu.Unlock()
		if subscriptionID != "" {
			go func(target WorkspaceTarget, id string) {
				_ = s.agent.UnsubscribeSessionsIndex(context.Background(), target, id)
			}(state.target, subscriptionID)
		}
	}
	for _, emitter := range emitters {
		emitter.mu.Lock()
		emitter.closed = true
		emitter.listeners = nil
		emitter.mu.Unlock()
	}
	if unsubscribeRuntime != nil {
		unsubscribeRuntime()
	}
}

// errorCode extracts the agent service error code, used to pick the retry kind.
func errorCode(err error) string {
	var coded interface{ ErrorCode() string }
	if errors.As(err, &coded) {
		return coded.ErrorCode()
	}
	return ""
}

// IsSeeded reports whether a workspace has consumed its silent baseline. It is
// the observability hook tests use to know the first snapshot landed; production
// code branches on the internal field.
func (s *Syncer) IsSeeded(workspace WorkspaceTarget) bool {
	s.mu.Lock()
	state := s.ingests[workspace.Key()]
	s.mu.Unlock()
	if state == nil {
		return false
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	return state.seeded
}
