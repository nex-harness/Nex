package windowcontroller

import (
	"context"
	"sync"
)

// runtime.go is the shared, process-wide aggregation authority. It owns the
// projection, the per-source refresh bookkeeping and the mutation routing, and it
// creates the per-connection attachments.
//
// Three rules carry the correctness of the whole controller:
//
//   - refreshSource is SINGLE-FLIGHT per (source, reader). Two concurrent
//     listTaskList calls for the same source share one read; the second awaits
//     the first's promise. This is what stops an N-workspace sidebar from issuing
//     N parallel task-index reads per keystroke, and what makes the "wait for the
//     in-flight read, then coalesce" workspace-event path correct.
//   - A refresh that a generation bump invalidated NEVER writes its result. The
//     reader may have taken a long time; if the source was dropped, replaced or
//     disconnected meanwhile, applying the stale snapshot would resurrect rows
//     for a source that is gone. The generation is checked immediately before the
//     projection write, not after.
//   - A source is registered BEFORE its first read and is dropped only when the
//     runtime is told to. A failed read leaves the previous trusted snapshot in
//     place; it never empties the list. That is why the projection is only ever
//     written by replaceSourceSnapshot and never field-by-field.

// Runtime is the process-level controller. One exists per Host (Desktop) or per
// server process; each connection attaches to it.
type Runtime struct {
	projection *projection
	resolve    SourceResolver
	onError    OnSourceError
	createID   func() string
	now        func() int64

	// flightMu guards the per-source refresh bookkeeping below.
	flightMu sync.Mutex
	// flights is SourceKey → the in-flight refresh, so a second caller joins it.
	flights map[string]*refreshFlight
	// generations is SourceKey → the generation a refresh was started under. A
	// drop, replace or disconnect bumps it, invalidating any in-flight result.
	generations map[string]int64
	// liveOverlays is SourceKey → the source's current live facts.
	liveOverlays map[string][]SessionOverlay
	// snapshotted is SourceKey → whether the projection currently holds a
	// SUCCESSFUL snapshot from that source. It is what makes a repeated list free
	// without a second index read, and it is cleared by every event that
	// invalidates the projection (disconnect, remove, replace) so those paths
	// re-read. A FAILED read never sets it, so the next call retries.
	snapshotted map[string]bool
	// observed is SourceKey → the observer stop func, released on drop/dispose.
	observed map[string]func()
	// attached counts live attachments; it is what makes a leak visible instead
	// of silent (the server attaches once per WebSocket).
	attachedMu sync.Mutex
	attached   int

	disposeOnce sync.Once
}

// refreshFlight is one in-flight read of one source.
type refreshFlight struct {
	generation int64
	done       chan struct{}
	err        error
}

// RuntimeOptions configures NewRuntime.
type RuntimeOptions struct {
	// Resolve maps a scope to its source. Required.
	Resolve SourceResolver
	// OnError reports a per-source failure; it never aborts the call.
	OnError OnSourceError
	// CreateID mints subscription ids and log epochs. Required.
	CreateID func() string
	// Now is the frame clock. Defaults to wall-clock milliseconds.
	Now func() int64
}

// NewRuntime builds the shared runtime.
func NewRuntime(opts RuntimeOptions) *Runtime {
	if opts.CreateID == nil {
		opts.CreateID = defaultCreateID
	}
	if opts.Now == nil {
		opts.Now = nowMillis
	}
	rt := &Runtime{
		resolve:      opts.Resolve,
		onError:      opts.OnError,
		createID:     opts.CreateID,
		now:          opts.Now,
		flights:      map[string]*refreshFlight{},
		generations:  map[string]int64{},
		liveOverlays: map[string][]SessionOverlay{},
		snapshotted:  map[string]bool{},
		observed:     map[string]func(){},
	}
	rt.projection = newProjection(opts.CreateID, opts.Now)
	return rt
}

// CreateAttachment opens one connection's attachment. The attachment owns its own
// subscription set and frame emitter; the rows and cursors it reads are shared.
// Two connections therefore never see each other's frames, and disposing one
// cannot disturb the other — which is the entire reason this is not a singleton
// service.
func (rt *Runtime) CreateAttachment() *Attachment {
	att := newAttachment(rt)
	rt.attachedMu.Lock()
	rt.attached++
	rt.attachedMu.Unlock()
	return att
}

// LiveAttachments reports how many attachments are open. A window that closed
// without disposing its attachment would leave this above zero forever, so the
// value is observable rather than assumed.
func (rt *Runtime) LiveAttachments() int {
	rt.attachedMu.Lock()
	defer rt.attachedMu.Unlock()
	return rt.attached
}

// resolveSource resolves a scope and reports whether it is readable.
func (rt *Runtime) resolveSource(scope SourceScope) *ResolvedSource {
	if rt.resolve == nil {
		return nil
	}
	resolved := rt.resolve(scope)
	if resolved == nil {
		return nil
	}
	if resolved.Availability == "" {
		resolved.Availability = AvailabilityOnline
	}
	return resolved
}

// report is the per-source failure reporter.
func (rt *Runtime) report(scope SourceScope, operation string, err error) {
	if rt.onError != nil {
		rt.onError(scope, operation, err)
	}
}

// ---------------------------------------------------------------------------
// Refresh
// ---------------------------------------------------------------------------

// refreshSource rebuilds one source's projection from its reader.
//
// force re-reads even when the projection already holds a snapshot from this
// reader; without it a repeated listTaskList is free (the single-flight and the
// "already snapshotted by this reader" checks both short-circuit). The mutation
// paths pass force=true because they must converge on the write they just made.
func (rt *Runtime) refreshSource(ctx context.Context, resolved *ResolvedSource, force bool) error {
	if err := rt.projection.registerSource(resolved.Scope); err != nil {
		return err
	}
	if resolved.Reader == nil || resolved.Availability != AvailabilityOnline {
		return nil
	}
	key := SourceKey(resolved.Scope)
	rt.ensureObserver(resolved)

	rt.flightMu.Lock()
	if !force && rt.snapshotted[key] {
		// The projection already holds a snapshot from this source and nothing
		// invalidated it, so a repeated list is free. This is what keeps the
		// sidebar from re-reading the whole index on every keystroke, and it is
		// why a caller that arrives just after another caller's read finished does
		// not immediately start a second one.
		rt.flightMu.Unlock()
		return nil
	}
	if existing := rt.flights[key]; existing != nil {
		// Join the in-flight read instead of starting a second one. The joiner's
		// force is honoured by re-reading only after the running read lands, so a
		// forced refresh that arrives mid-read waits for it and then converges.
		rt.flightMu.Unlock()
		<-existing.done
		if force {
			return rt.refreshSource(ctx, resolved, true)
		}
		return existing.err
	}
	generation := rt.generations[key] + 1
	rt.generations[key] = generation
	flight := &refreshFlight{generation: generation, done: make(chan struct{})}
	rt.flights[key] = flight
	rt.flightMu.Unlock()

	err := rt.applySnapshot(ctx, resolved, generation)

	rt.flightMu.Lock()
	if rt.flights[key] == flight {
		delete(rt.flights, key)
	}
	// A successful read marks the source as snapshotted; a FAILED one does not,
	// so the next call retries instead of being answered from an empty projection.
	rt.snapshotted[key] = err == nil
	flight.err = err
	rt.flightMu.Unlock()
	close(flight.done)
	return err
}

// applySnapshot does the read and, if still valid, the projection write.
func (rt *Runtime) applySnapshot(ctx context.Context, resolved *ResolvedSource, generation int64) error {
	key := SourceKey(resolved.Scope)
	index, err := resolved.Reader.ListTaskIndex(ctx, resolved.Scope)
	if err != nil {
		return err
	}
	// The generation check is immediately before the write. A read that started
	// before a drop/replace/disconnect must not resurrect the source's rows.
	if !rt.generationCurrent(key, generation) {
		return nil
	}
	memberships := normalizeMemberships(index, resolved.Scope)
	rt.flightMu.Lock()
	overlays := rt.liveOverlays[key]
	rt.flightMu.Unlock()
	return rt.projection.replaceSourceSnapshot(resolved.Scope, memberships, overlays, nil)
}

// normalizeMemberships ports readSourceTaskIndex + normalizeTaskMeta: a source's
// read is pinned to ITS OWN scope before it becomes a projection row. The path
// and identity on the row's meta are overwritten with the scope's, because the
// reader answers per scope and a row that kept its own copy could disagree with
// the address the projection builds from the scope — which is exactly the
// identity mismatch windowHostControllerTaskRowSchema refuses.
func normalizeMemberships(index []TaskMembership, scope SourceScope) []taskMembership {
	out := make([]taskMembership, 0, len(index))
	for _, m := range index {
		if m.Meta == nil {
			continue
		}
		meta := *m.Meta
		meta.WorkspacePath = scope.WorkspacePath
		if scope.WorkspaceIdentity != "" {
			identity := scope.WorkspaceIdentity
			meta.WorkspaceIdentity = &identity
		} else {
			meta.WorkspaceIdentity = nil
		}
		out = append(out, taskMembership{meta: &meta, membership: m.Membership})
	}
	return out
}

func (rt *Runtime) generationCurrent(key string, generation int64) bool {
	rt.flightMu.Lock()
	defer rt.flightMu.Unlock()
	return rt.generations[key] == generation
}
