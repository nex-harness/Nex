package agentscope

import (
	"sort"
	"strings"
	"sync"
)

// ownership.go is the facade's heart: the owned-subscription registry, the
// ACK-race staging window, and the runtime-generation invalidation. It is the
// part of nexAgentConnectionScope.ts that decides WHICH connection a frame
// belongs to, so it is kept apart from the handshake and the flow state.
//
// The rules it exists to keep:
//
//   - An owned entry is keyed by (kind, workspaceKey, topic, subscriptionId,
//     connectionId) — never by topic alone, because two connections on the same
//     workspace subscribe to the SAME topic and must not replace each other's
//     generation.
//   - The reverse index is the OWNERSHIP key (kind, workspaceKey, topic,
//     connectionId): a newer subscribe for the same ownership retires the older
//     entry, exactly like the TS routeKeyByOwnership.
//   - A frame that arrives BEFORE its subscribe ACK is staged against the pending
//     ownership, bounded by frame count AND encoded bytes, and released after the
//     ACK in arrival order. Overflow clears the WHOLE batch and fails loudly:
//     dropping the oldest would turn one logical frame into a permanent hole.
//   - A runtime restart bumps a per-workspace generation. A pending ownership
//     carries the generation it was created under; a late ACK whose generation no
//     longer matches is refused with fault.subscription.runtimeRestarted WITHOUT
//     touching the new runtime, because the new runtime may have reused the same
//     subscription id.

type subscriptionKind = Kind

// ownedEntry is one subscription this connection owns.
type ownedEntry struct {
	kind           Kind
	ownershipKey   string
	subscriptionID string
	topic          string
	connectionID   string
	clientMode     ClientMode
	target         Target
}

// pendingOwnership is a subscribe whose ACK has not returned yet. The promise
// continuation holds it, so it survives being detached from the routing group.
type pendingOwnership struct {
	kind   Kind
	target Target
	topic  string

	mu       sync.Mutex
	frames   []Frame
	bytes    int
	overflow bool

	runtimeGeneration           int64
	invalidatedByRuntimeRestart bool
}

// stage appends one early frame to the ACK-race window. It runs on the frame pump's
// goroutine while takeFrames runs on the subscribe's, so it takes the pending's own
// lock — the registry lock would not help, because the pending outlives its group
// entry and is reachable from both.
func (p *pendingOwnership) stage(frame Frame) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.overflow || p.invalidatedByRuntimeRestart {
		return
	}
	size := frameBytes(frame)
	// 双限额：帧数防住海量小帧，字节数防住少量巨型帧。两者都要，因为只限其一
	// 都会让异常 peer 在 subscribe pending 期间占住 host 内存。
	if len(p.frames)+1 > MaxPendingOwnershipFrames || p.bytes+size > MaxPendingOwnershipBytes {
		// 旧 ACK staging 超限时 shift 最旧帧，会把一个 logical frame 变成永久缺片。
		// 溢出必须清空整批并显式失败。
		p.frames = nil
		p.bytes = 0
		p.overflow = true
		return
	}
	p.frames = append(p.frames, frame)
	p.bytes += size
}

func (p *pendingOwnership) takeFrames() []Frame {
	p.mu.Lock()
	defer p.mu.Unlock()
	frames := p.frames
	p.frames = nil
	p.bytes = 0
	return frames
}

// discard empties the window. It does NOT clear overflow or the invalidation flag:
// both are one-shot facts the subscribe continuation still has to read.
func (p *pendingOwnership) discard() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.frames = nil
	p.bytes = 0
}

func (p *pendingOwnership) isOverflow() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.overflow
}

func (p *pendingOwnership) isInvalidated() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.invalidatedByRuntimeRestart
}

func (p *pendingOwnership) generation() int64 {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.runtimeGeneration
}

func (p *pendingOwnership) markInvalidated() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.invalidatedByRuntimeRestart = true
}

// ownershipRegistry is the per-connection owned-subscription state. It is guarded
// by its own mutex because the base service can call back (a frame arriving on the
// workspace stream) on a different goroutine than the RPC handler that subscribed.
type ownershipRegistry struct {
	mu sync.Mutex

	owned               map[string]*ownedEntry
	routeKeyByOwnership map[string]string
	pendingByOwnership  map[string]map[*pendingOwnership]struct{}
	runtimeGeneration   map[string]int64

	// deliver is how a released frame reaches the connection's consumer. It is
	// installed once by the scope (the RPC event fan-out) and is nil while the
	// scope is being built.
	deliver func(topic string, frame Frame)
}

func newOwnershipRegistry() *ownershipRegistry {
	return &ownershipRegistry{
		owned:               map[string]*ownedEntry{},
		routeKeyByOwnership: map[string]string{},
		pendingByOwnership:  map[string]map[*pendingOwnership]struct{}{},
		runtimeGeneration:   map[string]int64{},
	}
}

func routeKey(e *ownedEntry) string {
	return strings.Join([]string{string(e.kind), e.target.WorkspaceKey(), e.topic, e.subscriptionID, e.connectionID}, "\x00")
}

func ownershipKey(kind Kind, target Target, topic string) string {
	return strings.Join([]string{string(kind), target.WorkspaceKey(), topic}, "\x00")
}

// joinKey builds a composite key. The NUL separator cannot occur in any of the
// halves — a kind, a workspace key, a topic or a connection id is never expected to
// contain one — so two distinct tuples can never collapse into one key.
func joinKey(parts ...string) string { return strings.Join(parts, "\x00") }

// generation reports the current runtime generation for a workspace.
func (r *ownershipRegistry) generation(workspace string) int64 {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.runtimeGeneration[workspace]
}

// remember installs an entry, retiring the previous one for the same ownership.
func (r *ownershipRegistry) remember(e *ownedEntry) {
	r.mu.Lock()
	defer r.mu.Unlock()
	key := routeKey(e)
	if previous, ok := r.routeKeyByOwnership[e.ownershipKey]; ok {
		delete(r.owned, previous)
	}
	r.routeKeyByOwnership[e.ownershipKey] = key
	r.owned[key] = e
}

func (r *ownershipRegistry) forget(e *ownedEntry) {
	r.mu.Lock()
	defer r.mu.Unlock()
	key := routeKey(e)
	delete(r.owned, key)
	if r.routeKeyByOwnership[e.ownershipKey] == key {
		delete(r.routeKeyByOwnership, e.ownershipKey)
	}
}

// findBySubscription resolves a (kind, subscriptionId, workspaceKey) to the single
// owned entry. A trusted relay additionally pins topic + connectionId, so two
// downstream connections that happen to reuse an id cannot cross. When the
// terminal path (no route) sees a collision it returns nil rather than guessing —
// 宁可拒绝猜测。
func (r *ownershipRegistry) findBySubscription(kind Kind, target Target, subscriptionID string, route *UnsubscribeRoute) *ownedEntry {
	r.mu.Lock()
	defer r.mu.Unlock()
	workspace := target.WorkspaceKey()
	var match *ownedEntry
	for _, entry := range r.owned {
		if entry.kind != kind || entry.subscriptionID != subscriptionID {
			continue
		}
		if entry.target.WorkspaceKey() != workspace {
			continue
		}
		if route != nil && (entry.topic != route.Topic || entry.connectionID != route.ConnectionID) {
			continue
		}
		if match != nil {
			return nil
		}
		match = entry
	}
	return match
}

func (r *ownershipRegistry) ownsFrame(kind Kind, target Target, frame Frame) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	workspace := target.WorkspaceKey()
	for _, entry := range r.owned {
		if entry.kind == kind && entry.subscriptionID == frame.SubscriptionID &&
			entry.topic == frame.Topic && entry.target.WorkspaceKey() == workspace {
			return true
		}
	}
	return false
}

func (r *ownershipRegistry) beginPending(kind Kind, target Target, topic string) *pendingOwnership {
	r.mu.Lock()
	defer r.mu.Unlock()
	pending := &pendingOwnership{
		kind:              kind,
		target:            target,
		topic:             topic,
		runtimeGeneration: r.runtimeGeneration[target.WorkspaceKey()],
	}
	key := ownershipKey(kind, target, topic)
	group, ok := r.pendingByOwnership[key]
	if !ok {
		group = map[*pendingOwnership]struct{}{}
		r.pendingByOwnership[key] = group
	}
	group[pending] = struct{}{}
	return pending
}

func (r *ownershipRegistry) removePending(pending *pendingOwnership) {
	r.mu.Lock()
	defer r.mu.Unlock()
	key := ownershipKey(pending.kind, pending.target, pending.topic)
	group, ok := r.pendingByOwnership[key]
	if !ok {
		return
	}
	delete(group, pending)
	if len(group) == 0 {
		delete(r.pendingByOwnership, key)
	}
}

// deliverIncomingFrame is the routing decision for one workspace frame: an OWNED
// route fires immediately, an UNCLAIMED route is staged against every pending
// ownership of that (kind, workspace), and a route nobody owns or is waiting for
// is dropped.
func (r *ownershipRegistry) deliverIncomingFrame(kind Kind, target Target, frame Frame) {
	if r.ownsFrame(kind, target, frame) {
		if r.deliver != nil {
			r.deliver(frame.Topic, frame)
		}
		return
	}
	r.mu.Lock()
	group := r.pendingByOwnership[ownershipKey(kind, target, frame.Topic)]
	pendings := make([]*pendingOwnership, 0, len(group))
	for pending := range group {
		pendings = append(pendings, pending)
	}
	r.mu.Unlock()
	for _, pending := range pendings {
		pending.stage(frame)
	}
}

// invalidateWorkspaceRuntime retires every entry and pending of one workspace and
// bumps its generation. The runtime's subscriptions are already gone, so the
// facade forgets LOCALLY and does NOT unsubscribe against the replacement — the new
// runtime may have issued the same subscription id.
func (r *ownershipRegistry) invalidateWorkspaceRuntime(invalidatedWorkspace string, generation int64) {
	r.mu.Lock()
	r.runtimeGeneration[invalidatedWorkspace] = generation
	for key, entry := range r.owned {
		if entry.target.WorkspaceKey() == invalidatedWorkspace {
			delete(r.owned, key)
			delete(r.routeKeyByOwnership, entry.ownershipKey)
		}
	}
	var invalidated []*pendingOwnership
	for _, group := range r.pendingByOwnership {
		for pending := range group {
			if pending.target.WorkspaceKey() == invalidatedWorkspace {
				invalidated = append(invalidated, pending)
			}
		}
	}
	// The pending objects survive detachment (a promise continuation holds them)
	// so a late ACK can still be refused by generation; the group itself stops
	// accumulating frames for the dead runtime.
	for _, pending := range invalidated {
		pending.markInvalidated()
		r.removePendingLockedKey(pending)
		pending.discard()
	}
	r.mu.Unlock()
}

// removePendingLockedKey removes a pending from its group. The caller holds mu.
func (r *ownershipRegistry) removePendingLockedKey(pending *pendingOwnership) {
	key := ownershipKey(pending.kind, pending.target, pending.topic)
	group, ok := r.pendingByOwnership[key]
	if !ok {
		return
	}
	delete(group, pending)
	if len(group) == 0 {
		delete(r.pendingByOwnership, key)
	}
}

// ownedEntries snapshots every owned entry, for disposal.
func (r *ownershipRegistry) ownedEntries() []*ownedEntry {
	r.mu.Lock()
	defer r.mu.Unlock()
	entries := make([]*ownedEntry, 0, len(r.owned))
	for _, entry := range r.owned {
		entries = append(entries, entry)
	}
	return entries
}

// ownedForConnection reports whether THIS connection owns any entry for the given
// workspace + connectionId. It is the trusted relay's flow-control ownership check.
func (r *ownershipRegistry) ownsRoute(workspace, connectionID string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, entry := range r.owned {
		if entry.target.WorkspaceKey() == workspace && entry.connectionID == connectionID {
			return true
		}
	}
	return false
}

// liveFlowRoutes returns every (workspace, connectionId) the owned entries span,
// for the transport flow-state fan-out. Sorted so the fan-out order is stable.
func (r *ownershipRegistry) liveFlowRoutes() []flowRouteKey {
	r.mu.Lock()
	defer r.mu.Unlock()
	seen := map[flowRouteKey]struct{}{}
	var out []flowRouteKey
	for _, entry := range r.owned {
		key := flowRouteKey{workspace: entry.target.WorkspaceKey(), connectionID: entry.connectionID}
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, key)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].workspace != out[j].workspace {
			return out[i].workspace < out[j].workspace
		}
		return out[i].connectionID < out[j].connectionID
	})
	return out
}

// frameBytes ports frameBytes: the JSON-stringified byte length of a frame. It
// measures what the frame WOULD cost, which is the number the byte budget is
// defined over. The staged frame is already the exact bytes the CLI sent, so the
// measurement is on those bytes rather than on a re-serialisation that would change
// both the count and the escapes.
func frameBytes(frame Frame) int {
	return len(frame.Raw)
}
