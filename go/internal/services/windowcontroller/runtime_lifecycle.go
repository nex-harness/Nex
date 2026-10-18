package windowcontroller

import "context"

// runtime_lifecycle.go is the half of the runtime that owns the sources' LIVES
// rather than their content: the live-session observer, the disconnect /
// reconnect-replacement / remove transitions, and the runtime's own teardown.
//
// Every transition there bumps the source's GENERATION first. A refresh that was
// already reading when the transition happened is invalidated and will not write
// its result, so a source being torn down can never be resurrected by a slow read
// that lands afterwards.

// ---------------------------------------------------------------------------
// Live session observation
// ---------------------------------------------------------------------------

// ensureObserver starts the source's live observer once. The observer is the only
// source of running/waiting live status; a source without one keeps its persisted
// rows and never claims a live phase.
func (rt *Runtime) ensureObserver(resolved *ResolvedSource) {
	if resolved.Observer == nil || resolved.Availability != AvailabilityOnline {
		return
	}
	key := SourceKey(resolved.Scope)
	rt.flightMu.Lock()
	if rt.observed[key] != nil {
		rt.flightMu.Unlock()
		return
	}
	rt.flightMu.Unlock()

	scope := resolved.Scope
	stop, err := resolved.Observer.Observe(context.Background(), scope, func(overlays []SessionOverlay) {
		rt.setOverlays(scope, overlays)
	})
	if err != nil {
		rt.report(scope, "refresh", err)
		return
	}
	rt.flightMu.Lock()
	if existing := rt.observed[key]; existing != nil {
		// Lost a race with another refresh; keep the first observer and release
		// this one rather than leaking a subscription.
		rt.flightMu.Unlock()
		if stop != nil {
			stop()
		}
		return
	}
	rt.observed[key] = stop
	rt.flightMu.Unlock()
}

// setOverlays stores a source's live facts and pushes them into the projection.
// A source that was dropped in the meantime is ignored rather than resurrected.
func (rt *Runtime) setOverlays(scope SourceScope, overlays []SessionOverlay) {
	key := SourceKey(scope)
	rt.flightMu.Lock()
	rt.liveOverlays[key] = overlays
	rt.flightMu.Unlock()
	if err := rt.projection.replaceSourceSessionOverlays(scope, overlays); err != nil {
		// An unregistered source means the observer outlived its source; the
		// projection refuses the write and the caller (dispose) releases it.
		return
	}
}

// ---------------------------------------------------------------------------
// Source lifecycle
// ---------------------------------------------------------------------------

// DisconnectSource freezes a source: its rows stay as the last trusted snapshot
// but every row and its workspace fact go offline. It is the remote-disconnect
// path.
func (rt *Runtime) DisconnectSource(scope SourceScope) {
	key := SourceKey(scope)
	rt.bumpGeneration(key)
	rt.releaseObserver(key)
	rt.flightMu.Lock()
	delete(rt.liveOverlays, key)
	rt.flightMu.Unlock()
	_ = rt.projection.disconnectSource(scope)
}

// ReplaceDisconnectedSource swaps a reconnected source for the one it replaces,
// in ONE delta frame so a subscriber never sees an empty projection in between.
// It refuses unless the new source is already online: replacing a frozen offline
// projection with a source that cannot yet be read would lose the trusted rows.
func (rt *Runtime) ReplaceDisconnectedSource(ctx context.Context, previous SourceScope, resolved *ResolvedSource) error {
	if resolved.Reader == nil || resolved.Availability != AvailabilityOnline {
		return ErrSourceUnavailable
	}
	if err := rt.projection.registerSource(resolved.Scope); err != nil {
		return err
	}
	rt.ensureObserver(resolved)
	index, err := resolved.Reader.ListTaskIndex(ctx, resolved.Scope)
	if err != nil {
		return err
	}
	memberships := normalizeMemberships(index, resolved.Scope)
	key := SourceKey(resolved.Scope)
	rt.flightMu.Lock()
	overlays := rt.liveOverlays[key]
	// The replacement read SUCCEEDED, so the projection now holds a trusted
	// snapshot of this source: the next list is free rather than a second read.
	// The source it replaced is unmarked, so a later call holding the OLD scope
	// can never be answered from the replacement's snapshot.
	rt.snapshotted[key] = true
	rt.snapshotted[SourceKey(previous)] = false
	rt.flightMu.Unlock()
	return rt.projection.replaceSourceSnapshot(resolved.Scope, memberships, overlays, &previous)
}

// RemoveSource drops a source and everything it projected. It also drops the
// source a pending reconnect was borrowing rows from, so a closed logical session
// cannot leave an orphan projection behind.
func (rt *Runtime) RemoveSource(scope SourceScope) {
	key := SourceKey(scope)
	rt.bumpGeneration(key)
	rt.releaseObserver(key)
	rt.flightMu.Lock()
	delete(rt.liveOverlays, key)
	rt.flightMu.Unlock()
	rt.projection.removeSource(scope)
}

// bumpGeneration invalidates any in-flight read for a source. A started read
// whose generation no longer matches will not write its result.
func (rt *Runtime) bumpGeneration(key string) {
	rt.flightMu.Lock()
	rt.generations[key]++
	// The projection for this source is no longer a trusted snapshot of a live
	// source, so the next list must re-read rather than answer from it.
	rt.snapshotted[key] = false
	rt.flightMu.Unlock()
}

func (rt *Runtime) releaseObserver(key string) {
	rt.flightMu.Lock()
	stop := rt.observed[key]
	delete(rt.observed, key)
	rt.flightMu.Unlock()
	if stop != nil {
		stop()
	}
}

// Dispose releases the runtime: every source's projection is removed, every
// observer is stopped and every state map is emptied. It is idempotent.
func (rt *Runtime) Dispose() {
	rt.disposeOnce.Do(func() {
		rt.flightMu.Lock()
		keys := make([]string, 0, len(rt.observed))
		for key := range rt.observed {
			keys = append(keys, key)
		}
		rt.flightMu.Unlock()
		for _, key := range keys {
			rt.releaseObserver(key)
		}
		rt.projection.dispose()
		rt.flightMu.Lock()
		rt.generations = map[string]int64{}
		rt.liveOverlays = map[string][]SessionOverlay{}
		rt.snapshotted = map[string]bool{}
		rt.observed = map[string]func(){}
		rt.flights = map[string]*refreshFlight{}
		rt.flightMu.Unlock()
	})
}
