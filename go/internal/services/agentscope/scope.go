package agentscope

import (
	"context"
	"sort"
	"sync"
	"time"
)

// scope.go is the connection scope itself: construction, the forwarded-connection
// rule, the three subscribe families (one implementation, three topics), the
// unsubscribe and resync routes, and disposal.
//
// The forwarded-connection rule is the security core. `forwardedConnection` decides
// what connection identity the BASE service is told about, and it has three cases:
//
//   - trusted relay + a downstream carrier: the connection is the NAMESPACEd pair
//     (upstream, downstream) and the clientMode is the DOWNSTREAM's. The delta bit
//     belongs to that downstream end too — a relay never consumes frames itself, it
//     only carries the downstream clientHello's declaration.
//   - otherwise: the attachment's own connectionId and clientMode, plus whatever the
//     attachment's own clientHello declared.
//
// The connectionIds are length-prefixed when namespaced, so `a/b` + `c` and `a` +
// `b/c` — which collide under a naive separator join — stay distinct.

// Config is how a host builds a scope for one accepted connection.
type Config struct {
	// ConnectionID is the attachment's own id. It must be non-blank and within
	// MaxDownstreamConnectionIDLength.
	ConnectionID string
	// ClientMode is the delivery mode the attachment registered.
	ClientMode ClientMode
	// Role decides whether the attachment must complete hello → clientHello.
	Role Role
	// Now is the clock, injected so the hello's serverTime is testable.
	Now func() time.Time
	// Interaction, when set, is the protocol client whose interaction/* handlers
	// this scope owns (the step 3.10.2 seam).
	Interaction InteractionRegistry
}

// Scope is one connection's V4 facade.
type Scope struct {
	base      AgentBase
	cfg       Config
	handshake *handshakeState
	owned     *ownershipRegistry
	flow      *flowController

	// disposed guards every entry point. It is read under disposedMu together with
	// the setter so a dispose racing an in-flight subscribe cannot re-register an
	// owner.
	disposedMu sync.Mutex
	disposed   bool
	// disposing is set by the goroutine that owns the teardown. A second Dispose
	// waits on disposeDone instead of starting a parallel one.
	disposing bool
	// disposeDone is closed when the teardown finished; disposeErr is what it
	// reported, so every concurrent caller sees the same honest result.
	disposeDone chan struct{}
	disposeErr  error
	closeErr    error

	// runtimeWatch is the base service's runtime-lifecycle subscription, kept so
	// disposal can unsubscribe it.
	stopRuntimeWatch func()

	// queryMu guards commandQueryWorkspaceKey: the workspace the first non-probe
	// command query bound. A later query naming a different one is refused, because
	// the CLI's command state is per-workspace.
	queryMu                  sync.Mutex
	commandQueryWorkspaceKey string

	// interactionDirectory is the shared resolver table (interaction.go) and
	// interactionResolver is this scope's entry in it. The directory is what lets an
	// agent's interaction/* request be answered by the connection that owns it.
	interactionDirectory *InteractionDirectory
	interactionResolver  InteractionResolver

	// deliver routes a frame to the connection's consumer. It is set by the RPC
	// channel adapter (which owns the RPC event fan-out); without one the scope
	// still routes and stages, it just has nowhere to fire.
	deliverMu sync.Mutex
	deliver   func(topic string, frame Frame)
}

// New builds a scope. It fails with fault.connection.invalidConnectionId for a blank
// or over-long id, exactly as assertConnectionId throws.
func New(base AgentBase, cfg Config) (*Scope, error) {
	if base == nil {
		return nil, &Fault{Code: FaultInvalidConnectionID}
	}
	if err := assertConnectionID(cfg.ConnectionID); err != nil {
		return nil, err
	}
	if cfg.Role != RoleTrustedHostRelay {
		// An unset role is a terminal client, the TS default.
		cfg.Role = RoleTerminalClient
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.ClientMode != ClientModeContinuous && cfg.ClientMode != ClientModeReplayable {
		return nil, &Fault{Code: FaultInvalidConnectionID}
	}
	s := &Scope{
		base:        base,
		cfg:         cfg,
		handshake:   newHandshakeState(cfg.Role, cfg.ClientMode),
		owned:       newOwnershipRegistry(),
		flow:        newFlowController(base),
		disposeDone: make(chan struct{}),
	}
	s.owned.deliver = s.fire
	return s, nil
}

// SetDeliver installs the frame delivery hook. The RPC channel adapter calls it once,
// before it exposes the channel.
func (s *Scope) SetDeliver(fn func(topic string, frame Frame)) {
	s.deliverMu.Lock()
	s.deliver = fn
	s.deliverMu.Unlock()
}

func (s *Scope) fire(topic string, frame Frame) {
	s.deliverMu.Lock()
	fn := s.deliver
	s.deliverMu.Unlock()
	if fn != nil {
		fn(topic, frame)
	}
}

func (s *Scope) isDisposed() bool {
	s.disposedMu.Lock()
	defer s.disposedMu.Unlock()
	return s.disposed
}

// forwardedConnection is the trusted connection the base service is told about.
func (s *Scope) forwardedConnection(params incomingParams) TrustedConnection {
	downstream := ReadTrustedConnection(params.carrierValue())
	if s.cfg.Role == RoleTrustedHostRelay && downstream != nil {
		// The downstream id is attacker-influenced; the length prefix both keeps
		// the namespace collision-free and makes the over-long case a fault below
		// rather than a silent truncation.
		_ = assertConnectionID(downstream.ConnectionID)
		out := TrustedConnection{
			ConnectionID: namespaceRelayConnectionID(s.cfg.ConnectionID, downstream.ConnectionID),
			ClientMode:   downstream.ClientMode,
		}
		if downstream.WorkflowRunDeltas {
			out.WorkflowRunDeltas = true
		}
		return out
	}
	out := TrustedConnection{
		ConnectionID: s.cfg.ConnectionID,
		ClientMode:   s.cfg.ClientMode,
	}
	if s.handshake.workflowRunDeltas() {
		out.WorkflowRunDeltas = true
	}
	return out
}

// forwardedUnsubscribeRoute pins a trusted relay's unsubscribe to the exact
// (topic, namespaced connectionId). A terminal client has no route: it only sends a
// subscription id, and the facade refuses to guess when that is ambiguous.
func (s *Scope) forwardedUnsubscribeRoute(params incomingParams) *UnsubscribeRoute {
	if s.cfg.Role != RoleTrustedHostRelay {
		return nil
	}
	downstream := ReadTrustedUnsubscribeRoute(params.carrierValue())
	if downstream == nil {
		return nil
	}
	_ = assertConnectionID(downstream.ConnectionID)
	return &UnsubscribeRoute{
		Topic:        downstream.Topic,
		ConnectionID: namespaceRelayConnectionID(s.cfg.ConnectionID, downstream.ConnectionID),
	}
}

// call builds the Forwarded for one base call: caller fields with every forgeable
// key stripped, plus the workspace and the trusted identity.
func (s *Scope) call(params incomingParams) Forwarded {
	trusted := s.forwardedConnection(params)
	return Forwarded{
		Fields:  params.StripForgeable(),
		Target:  params.target(),
		Trusted: &trusted,
	}
}

// ---------------------------------------------------------------------------
// Hello / initialize
// ---------------------------------------------------------------------------

// Hello returns the host greeting. The delivery profile is DERIVED from the
// clientMode, so a caller cannot ask for continuous delivery on a replayable mode.
func (s *Scope) Hello() (HelloMessage, error) {
	return s.handshake.issueHello(s.isDisposed(), s.cfg.ConnectionID, s.cfg.Now())
}

// Initialize completes the handshake from the client's clientHello.
func (s *Scope) Initialize(rawClientHello []byte) error {
	return s.handshake.acceptClientHello(s.isDisposed(), rawClientHello)
}

// HandshakeComplete reports whether the connection may talk to the CLI.
func (s *Scope) HandshakeComplete() bool {
	s.handshake.mu.Lock()
	defer s.handshake.mu.Unlock()
	return s.handshake.handshakeComplete
}

// ---------------------------------------------------------------------------
// Flow control
// ---------------------------------------------------------------------------

// SetTransportFlowState forwards a transport sideband edge. The edge is serialised
// behind every already-accepted edge, so a burst and the final close keep their
// commit order. A non-close edge after a close is dropped.
func (s *Scope) SetTransportFlowState(state FlowState) error {
	if s.isDisposed() {
		return nil
	}
	return <-s.flow.enqueue(state, s.liveFlowRoutes)
}

// liveFlowRoutes is every route the attachment currently spans: the attachment Begin
// routes plus one per owned subscription, de-duplicated.
func (s *Scope) liveFlowRoutes() []flowRoute {
	seen := map[flowRouteKey]flowRoute{}
	s.flow.mu.Lock()
	for _, route := range s.flow.attachmentRoutes {
		seen[route.key] = route
	}
	s.flow.mu.Unlock()
	for _, key := range s.owned.liveFlowRoutes() {
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = flowRoute{key: key, target: Target{WorkspacePath: key.workspace}, trusted: TrustedConnection{ConnectionID: key.connectionID}}
	}
	keys := make([]flowRouteKey, 0, len(seen))
	for key := range seen {
		keys = append(keys, key)
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].workspace != keys[j].workspace {
			return keys[i].workspace < keys[j].workspace
		}
		return keys[i].connectionID < keys[j].connectionID
	})
	out := make([]flowRoute, 0, len(keys))
	for _, key := range keys {
		out = append(out, seen[key])
	}
	return out
}

// syncCurrentFlowForEntry replays a saturated edge onto a route that has just become
// real, because the subscribe ACK is what makes the trusted downstream route a fact.
// Without this, a saturated transport would not be signalled until the next
// high-water edge, which may never come.
func (s *Scope) syncCurrentFlowForEntry(entry *ownedEntry) error {
	if s.flow.currentState() != FlowSaturated {
		return nil
	}
	return s.flow.forward(s.routeForEntry(entry), FlowSaturated)
}

// ---------------------------------------------------------------------------
// Disposal
// ---------------------------------------------------------------------------

// Dispose tears the scope down. It is idempotent. The close edge is enqueued FIRST,
// so it lands after every already-accepted SAT/DRN, and a failed control RPC never
// prevents the owned cleanup. After dispose, every entry point refuses with
// fault.connection.closed and every owned subscription has been torn down on the
// base service — another connection's scopes are untouched.
func (s *Scope) Dispose(ctx context.Context) error {
	// Exactly one caller runs the teardown. The others wait for it and then return,
	// so a double dispose can never enqueue two closes or send two unsubscribes.
	s.disposedMu.Lock()
	if s.disposed {
		s.disposedMu.Unlock()
		<-s.disposeDone
		return s.disposeErr
	}
	if s.disposing {
		s.disposedMu.Unlock()
		<-s.disposeDone
		return s.disposeErr
	}
	s.disposing = true
	// The flag is set BEFORE the close edge is enqueued: every entry point must
	// refuse from this point on, and SetTransportFlowState refuses after dispose.
	s.disposed = true
	s.disposedMu.Unlock()

	defer func() {
		s.disposedMu.Lock()
		s.disposeErr = s.closeErr
		s.disposedMu.Unlock()
		close(s.disposeDone)
	}()

	// The close is enqueued directly through the flow controller rather than through
	// SetTransportFlowState, which now refuses: the edge still lands after every
	// already-accepted SAT/DRN because it joins the same serialised chain.
	closeErr := <-s.flow.enqueue(FlowClosed, s.liveFlowRoutes)
	s.disposedMu.Lock()
	s.closeErr = closeErr
	s.disposedMu.Unlock()

	s.handshake.mu.Lock()
	s.handshake.handshakeComplete = false
	s.handshake.helloIssued = false
	s.handshake.boundClientID = ""
	s.handshake.clientWorkflowRunDeltas = false
	s.handshake.mu.Unlock()

	entries := s.owned.ownedEntries()
	s.owned.mu.Lock()
	s.owned.owned = map[string]*ownedEntry{}
	s.owned.routeKeyByOwnership = map[string]string{}
	for _, group := range s.owned.pendingByOwnership {
		for pending := range group {
			pending.discard()
		}
	}
	s.owned.pendingByOwnership = map[string]map[*pendingOwnership]struct{}{}
	s.owned.mu.Unlock()

	if s.stopRuntimeWatch != nil {
		s.stopRuntimeWatch()
		s.stopRuntimeWatch = nil
	}
	// The interaction handlers go BEFORE the owned teardown: a window that is gone
	// must stop being the answer for the next prompt, and leaving the closure
	// installed would keep this scope reachable from the protocol client.
	s.ReleaseInteractionHandlers()
	s.owned.mu.Lock()
	s.owned.runtimeGeneration = map[string]int64{}
	s.owned.mu.Unlock()
	s.flow.reset()

	for _, entry := range entries {
		s.unsubscribeBase(ctx, entry.kind, entry, "")
	}
	return closeErr
}
