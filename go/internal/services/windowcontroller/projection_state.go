package windowcontroller

import (
	"errors"
	"sort"
	"strings"
	"sync"
)

// projection_state.go is the projection object and the four mutations the runtime
// drives it with: registerSource, replaceSourceSnapshot, replaceSourceSessionOverlays,
// disconnectSource, removeSource.
//
// The single-writer discipline: every exported method takes p.mu, mutates the
// state, computes the deltas, and releases the lock BEFORE publishing them. The
// publish path is the only thing that calls a subscriber, and it runs unlocked so
// a slow or re-entrant subscriber cannot block another source's refresh or
// deadlock the projection. Because the state and the deltas are computed under
// one lock, the deltas always describe exactly the transition that was applied.

// projection is the shared, per-HOST state. It is NOT per connection: two
// windows looking at the same workspace must see the SAME list, so the rows and
// the two topic cursors live here once. What stays per connection is which
// SUBSCRIPTIONS exist — each attachment mints its own ids into the shared
// registry and disposes exactly its own, so a connection that never subscribed
// receives nothing and one that closed cannot remove another's stream.
type projection struct {
	mu sync.Mutex
	// sources is SourceKey → source state.
	sources map[string]*sourceState
	// subscribers is subscriptionID → subscriber, shared across attachments; each
	// attachment registers its own ids and disposes only its own.
	subscribers map[string]*subscriber
	// cursors is topic → the topic's (logEpoch, seq). One cursor per topic, not
	// per subscriber: the cursor is the topic's state, and every subscriber of a
	// topic reads the same sequence.
	cursors  map[string]*topicCursor
	createID func() string
	now      func() int64
}

func newProjection(createID func() string, now func() int64) *projection {
	return &projection{
		sources:     map[string]*sourceState{},
		subscribers: map[string]*subscriber{},
		cursors:     newProjectionCursors(createID),
		createID:    createID,
		now:         now,
	}
}

// registerSource ports registerSource. A remote scope without a workspaceIdentity
// is refused (controller.ts throws), because such a scope could only produce
// addresses the schema rejects. Re-registering an existing source keeps its rows
// and only refreshes the mutate closure; a repeated register for a known key is a
// no-op for state.
func (p *projection) registerSource(scope SourceScope) error {
	if scope.Kind == KindRemote && trimEmpty(scope.WorkspaceIdentity) == "" {
		return errors.New("remote controller source must carry workspaceIdentity")
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	key := SourceKey(scope)
	if _, ok := p.sources[key]; ok {
		return nil
	}
	p.sources[key] = &sourceState{
		scope:           scope,
		availability:    AvailabilityOffline,
		connectionState: StateConnecting,
		memberships:     map[string]taskMembership{},
		overlays:        map[string]SessionOverlay{},
		rows:            map[string]TaskRow{},
	}
	return nil
}

// replaceSourceSnapshot ports replaceSourceSnapshot: swap a source's task index
// and overlays atomically, optionally REMOVING a replaced (previous) source in the
// same delta frame. The combined removals+upserts in ONE frame is the reason a
// reconnecting remote (whose remoteSessionId changes) never shows an intermediate
// empty projection to a subscriber.
func (p *projection) replaceSourceSnapshot(
	scope SourceScope,
	taskIndex []taskMembership,
	sessionsIndex []SessionOverlay,
	replacesScope *SourceScope,
) error {
	p.mu.Lock()
	key := SourceKey(scope)
	src := p.sources[key]
	if src == nil {
		p.mu.Unlock()
		return errNoSource(scope)
	}
	previousFact := workspaceFactOf(src)

	// A replaced source is one whose key differs from the current source's; only
	// then is it actually a replacement (replacesScope == scope is a no-op).
	var replaced *sourceState
	if replacesScope != nil {
		if cand := p.sources[SourceKey(*replacesScope)]; cand != nil && SourceKey(cand.scope) != key {
			replaced = cand
		}
	}
	if replaced != nil {
		delete(p.sources, SourceKey(replaced.scope))
	}

	previousRows := src.rows
	// Rebuild memberships keyed by the NEW address.
	nextMemberships := make(map[string]taskMembership, len(taskIndex))
	for _, m := range taskIndex {
		if m.meta == nil {
			continue
		}
		if err := validateMembershipScope(src.scope, m.meta); err != nil {
			p.mu.Unlock()
			return err
		}
		addr := AddressOf(src.scope, m.meta.TaskID)
		nextMemberships[AddressTaskKey(addr)] = m
	}
	src.memberships = nextMemberships
	src.overlays = overlayMap(sessionsIndex)
	src.availability = AvailabilityOnline
	src.rows = buildRows(src)
	src.connectionState = StateOnline

	taskDeltas := computeSnapshotTaskDeltas(replaced, previousRows, src.rows)
	// nextFact is read here, under the lock, for the same reason previousFact was:
	// reading the source state after the unlock would race a concurrent
	// disconnect and publish a fact that describes neither the old nor the new
	// state.
	nextFact := workspaceFactOf(src)
	p.mu.Unlock()

	p.publishTaskDeltas(taskDeltas, frameClock(p))

	// Workspace facts: a replaced source is removed and the new source is upserted
	// in one batch, in that order.
	var wsDeltas []WorkspaceDelta
	if replaced != nil {
		wsDeltas = append(wsDeltas, removalDeltaForScope(replaced.scope))
	}
	if !workspaceFactEqual(previousFact, nextFact) || replaced != nil {
		fact := nextFact
		wsDeltas = append(wsDeltas, WorkspaceDelta{Op: OpWorkspaceUpserted, Workspace: &fact})
	}
	p.publishWorkspaceDeltas(wsDeltas, frameClock(p))
	return nil
}

// replaceSourceSessionOverlays ports replaceSourceSessionOverlays: a live-fact
// only update. It recomputes rows and publishes ONLY the rows that changed (every
// row is recomputed because an overlay can change a title, status, or liveStatus
// on any task), with NO removals — an overlay update never drops a row.
func (p *projection) replaceSourceSessionOverlays(scope SourceScope, sessionsIndex []SessionOverlay) error {
	p.mu.Lock()
	key := SourceKey(scope)
	src := p.sources[key]
	if src == nil {
		p.mu.Unlock()
		return errNoSource(scope)
	}
	previousRows := src.rows
	src.overlays = overlayMap(sessionsIndex)
	src.rows = buildRows(src)
	deltas := computeUpsertOnlyDeltas(previousRows, src.rows)
	p.mu.Unlock()
	p.publishTaskDeltas(deltas, frameClock(p))
	return nil
}

// disconnectSource ports disconnectSource: freeze the source. Its rows stay (the
// last trusted projection) but every row flips to offline availability, and the
// workspace fact becomes disconnected. The consumer therefore keeps rendering the
// frozen rows with a clear offline badge rather than losing the workspace.
func (p *projection) disconnectSource(scope SourceScope) error {
	p.mu.Lock()
	key := SourceKey(scope)
	src := p.sources[key]
	if src == nil {
		p.mu.Unlock()
		return errNoSource(scope)
	}
	src.availability = AvailabilityOffline
	src.connectionState = StateDisconnected
	deltas := make([]TaskDelta, 0, len(src.rows))
	for rowKey, row := range src.rows {
		offline := row
		offline.SourceAvailability = AvailabilityOffline
		src.rows[rowKey] = offline
		offlineCopy := offline
		deltas = append(deltas, TaskDelta{Op: OpTaskUpserted, Task: &offlineCopy})
	}
	fact := workspaceFactOf(src)
	p.mu.Unlock()
	p.publishTaskDeltas(deltas, frameClock(p))
	// An offline source is always disconnected/failed (schema refinement), so this
	// fact is always valid.
	p.publishWorkspaceDeltas([]WorkspaceDelta{{Op: OpWorkspaceUpserted, Workspace: &fact}}, frameClock(p))
	return nil
}

// removeSource ports removeSource: drop the source entirely. Every row it
// contributed is removed and its workspace fact removed. Used on runtime dispose
// and when a remote workspace is torn down.
func (p *projection) removeSource(scope SourceScope) {
	p.mu.Lock()
	key := SourceKey(scope)
	src := p.sources[key]
	if src == nil {
		p.mu.Unlock()
		return
	}
	delete(p.sources, key)
	deltas := make([]TaskDelta, 0, len(src.rows))
	for _, row := range src.rows {
		addr := row.Address
		deltas = append(deltas, TaskDelta{Op: OpTaskRemoved, Address: &addr})
	}
	p.mu.Unlock()
	p.publishTaskDeltas(deltas, frameClock(p))
	p.publishWorkspaceDeltas([]WorkspaceDelta{removalDeltaForScope(scope)}, frameClock(p))
}

// allWorkspaceFacts returns every source's fact in the deterministic snapshot
// order: remote-or-local marker then workspace key, ascending.
func allWorkspaceFacts(sources map[string]*sourceState) []WorkspaceFact {
	out := make([]WorkspaceFact, 0, len(sources))
	for _, src := range sources {
		out = append(out, workspaceFactOf(src))
	}
	sort.Slice(out, func(i, j int) bool {
		left := factKey(out[i])
		right := factKey(out[j])
		return left < right
	})
	return out
}

func factKey(f WorkspaceFact) string {
	remote := "local"
	if f.RemoteSessionID != "" {
		remote = f.RemoteSessionID
	}
	key := f.WorkspaceIdentity
	if key == "" {
		key = f.WorkspacePath
	}
	return remote + "\x00" + key
}

// workspaceFactOf builds a source's fact. It pairs availability with a compatible
// connection state so the schema refinement (offline ⇒ disconnected/failed) can
// never be violated by an inconsistent pair.
func workspaceFactOf(src *sourceState) WorkspaceFact {
	fact := WorkspaceFact{
		WorkspacePath:      src.scope.WorkspacePath,
		SourceAvailability: src.availability,
		ConnectionState:    src.connectionState,
	}
	if src.scope.Kind == KindRemote {
		fact.RemoteSessionID = src.scope.RemoteSessionID
	}
	if src.scope.WorkspaceIdentity != "" {
		fact.WorkspaceIdentity = src.scope.WorkspaceIdentity
	}
	if fact.SourceAvailability == AvailabilityOffline &&
		fact.ConnectionState != StateDisconnected && fact.ConnectionState != StateFailed {
		fact.ConnectionState = StateDisconnected
	}
	return fact
}

func workspaceFactEqual(a, b WorkspaceFact) bool { return a == b }

func removalDeltaForScope(scope SourceScope) WorkspaceDelta {
	return WorkspaceDelta{
		Op:                OpWorkspaceRemoved,
		RemoteSessionID:   scope.RemoteSessionID,
		WorkspacePath:     scope.WorkspacePath,
		WorkspaceIdentity: scope.WorkspaceIdentity,
	}
}

// overlayMap keys overlays by task id.
func overlayMap(overlays []SessionOverlay) map[string]SessionOverlay {
	out := make(map[string]SessionOverlay, len(overlays))
	for _, o := range overlays {
		out[o.TaskID] = o
	}
	return out
}

// validateMembershipScope refuses a membership whose meta disagrees with the
// source scope (the TS validateMembershipScope). A mismatched membership means the
// source returned a row from a different workspace, which must not be projected
// under this source.
func validateMembershipScope(scope SourceScope, meta *taskMeta) error {
	metaIdentity := ""
	if meta.WorkspaceIdentity != nil {
		metaIdentity = *meta.WorkspaceIdentity
	}
	if meta.WorkspacePath != scope.WorkspacePath || metaIdentity != scope.WorkspaceIdentity {
		return errors.New("task-index membership does not match source scope, taskId=" + meta.TaskID)
	}
	return nil
}

// frameClock reads the clock that stamps frame.sentAt. It is called with the
// projection lock RELEASED: sentAt is a wall-clock stamp for the consumer, not a
// consistency-relevant value, and a subscriber callback must never run under the
// lock.
func frameClock(p *projection) int64 { return p.now() }

func trimEmpty(v string) string { return strings.TrimSpace(v) }

func errNoSource(scope SourceScope) error {
	return errors.New("no controller source matches scope, workspacePath=" + scope.WorkspacePath)
}
