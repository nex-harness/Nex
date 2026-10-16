package agentscope

// runtime.go ports the two runtime-lifecycle observers of nexAgentConnectionScope.ts
// (onAgentRuntimeLifecycle, and the legacy onAgentRuntimeRestarted fallback).
//
// Both exist because a v4 subscription lives in the AGENT PROCESS's memory: when the
// process is recycled, every subscription it held is gone, and the facade that still
// believes it owns one is holding a stale handle. Two rules from the TS comments are
// the whole point of this file:
//
//   - `available` is NOT an invalidation boundary. On a cold start the subscribe
//     registers its pending ownership FIRST, the same startup then publishes
//     available(gen 1), and only afterwards does the ACK arrive. Advancing the
//     generation on available would classify that legitimate ACK as a restart and
//     fail every first subscribe. A real generation change is always preceded by
//     `unavailable`, and it is that branch that does the cleanup.
//   - `unavailable` is, because a facade that only watched restarts would keep the
//     dead owners when the runtime exited WITHOUT coming back — and the next UI
//     cleanup would then rebuild unsubscribe params for a subscription that no
//     longer exists, which walks the start-if-needed path and spawns an agent just
//     to tear down nothing.

// RuntimeLifecycleEvent is one runtime-lifecycle notification from the base service.
type RuntimeLifecycleEvent struct {
	// WorkspaceKey is the identity key of the workspace whose runtime changed.
	WorkspaceKey string
	// Generation is the runtime's generation number; a value the facade compares
	// against the one its pending ownership was created under.
	Generation int64
	// State is "available" or "unavailable". Only unavailable invalidates.
	State string
}

// Lifecycle states.
const (
	RuntimeAvailable   = "available"
	RuntimeUnavailable = "unavailable"
)

// RuntimeWatcher is the base service's lifecycle subscription surface. The
// richer one is optional: a base service that only reports restarts (the legacy
// shape) gets the restart callback instead.
type RuntimeWatcher interface {
	// WatchRuntimeLifecycle registers a lifecycle observer and returns its stop
	// func. It returns nil when the base service has no such surface.
	WatchRuntimeLifecycle(fn func(RuntimeLifecycleEvent)) func()
	// WatchRuntimeRestarted registers the legacy restart-only observer and returns
	// its stop func. It returns nil when there is no such surface.
	WatchRuntimeRestarted(fn func(workspaceKey string)) func()
}

// AttachRuntimeWatch installs the lifecycle observers on the base service. It is a
// no-op when the base service exposes neither surface.
func (s *Scope) AttachRuntimeWatch(watcher RuntimeWatcher) {
	if watcher == nil {
		return
	}
	if stop := watcher.WatchRuntimeLifecycle(s.onRuntimeLifecycle); stop != nil {
		s.stopRuntimeWatch = stop
		return
	}
	// Legacy base service: a restart IS a generation change, so bumping the
	// generation here gives the same refusal behaviour for a late ACK.
	if stop := watcher.WatchRuntimeRestarted(func(workspaceKey string) {
		s.invalidateRuntime(workspaceKey, s.owned.generation(workspaceKey)+1)
	}); stop != nil {
		s.stopRuntimeWatch = stop
	}
}

func (s *Scope) onRuntimeLifecycle(event RuntimeLifecycleEvent) {
	if event.State == RuntimeAvailable {
		// 首次冷启动的 subscribe 会先登记 pending，再由同一次启动发布
		// available(gen1)，最后才收到 ACK。available 不是旧 ownership 的失效边界。
		return
	}
	// attachment facade 若只监听 restart，runtime 退出但未重启时就会保留旧 owner，
	// 后续 UI cleanup 会重建 unsubscribe 参数并误走启动型 client；因此 unavailable
	// 直接失效本地 owner。
	s.invalidateRuntime(event.WorkspaceKey, event.Generation)
}

// invalidateRuntime retires the local ownership of one workspace at a generation.
// It is deliberately LOCAL: the runtime's subscriptions are already gone, so the
// facade forgets its owners and its per-route flow state without sending anything to
// the replacement. A reverse unsubscribe here would be a bug — the new runtime may
// have issued the same subscription id, and tearing that one down would silently
// kill a live subscription.
func (s *Scope) invalidateRuntime(workspaceKey string, generation int64) {
	s.owned.invalidateWorkspaceRuntime(workspaceKey, generation)
	// The remembered flow state for the retired runtime's routes goes with it: a
	// close forwarded later for such a route would be answered by the NEW runtime,
	// which never saw saturated.
	s.flow.mu.Lock()
	for key := range s.flow.forwardedByRoute {
		if key.workspace == workspaceKey {
			delete(s.flow.forwardedByRoute, key)
		}
	}
	s.flow.mu.Unlock()
}
