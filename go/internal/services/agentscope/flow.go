package agentscope

import (
	"context"
	"encoding/json"
	"sync"
)

// flow.go is the transport sideband's edge fan-out: V4ConnectionFlowState per
// (workspace, connectionId) route, serialised so a fast SAT→DRN pair and the final
// close keep their commit order.
//
// The rules from nexAgentConnectionScope.ts it preserves:
//
//   - Every accepted edge is forwarded to the routes that exist AT THAT MOMENT. A
//     route created later does not get a replay of an old edge; instead the
//     subscribe path replays the CURRENT state once the route becomes real (see
//     syncCurrentFlowForEntry), because waiting for the next high-water edge would
//     leave a saturated route unsignalled indefinitely.
//   - close is terminal. A saturated/drained arriving after a close is ignored, so
//     a late edge cannot revive a paused CLI route.
//   - A single failed forward does not break the chain: close must still reach the
//     remaining routes even when one of them errors.
//   - The LAST subscription on a route must close that route BEFORE it
//     unsubscribes. The reverse order makes the upstream forget the owner first, so
//     the close is rejected and the connection is stranded paused.

type flowRouteKey struct {
	workspace    string
	connectionID string
}

// flowRoute is one route's forwarding target. The connection it carries is the
// TRUSTED downstream identity, not the attachment's own, so a relay's edge lands on
// the right downstream connection rather than on the relay socket.
type flowRoute struct {
	key     flowRouteKey
	target  Target
	trusted TrustedConnection
}

// flowCall builds the forwarded params for one route's edge: the workspace, the
// state, and the trusted downstream identity. It is the
// withTrustedConnection({...workspaceTarget(route.target), state}, route.connection)
// of the TS.
func (r flowRoute) flowCall(state FlowState) Forwarded {
	raw, _ := json.Marshal(string(state))
	return Forwarded{
		Fields:  map[string]json.RawMessage{"state": raw},
		Target:  r.target,
		Trusted: &r.trusted,
	}
}

type flowController struct {
	base AgentBase

	mu      sync.Mutex
	current FlowState
	closed  bool
	// forwardedByRoute is the last state successfully forwarded per route, so a
	// repeated edge is not re-sent.
	forwardedByRoute map[flowRouteKey]FlowState
	// attachmentRoutes are the routes an attachment Begin created. They survive with
	// no subscription so an upload in staging can still be closed on dispose.
	attachmentRoutes map[flowRouteKey]flowRoute

	// chain serialises the edges. Each edge's fan-out starts only after the
	// previous one settled, so a burst commits in the order the transport
	// delivered it.
	chainMu sync.Mutex
	chain   chan struct{}
}

func newFlowController(base AgentBase) *flowController {
	return &flowController{
		base:             base,
		current:          FlowDrained,
		forwardedByRoute: map[flowRouteKey]FlowState{},
		attachmentRoutes: map[flowRouteKey]flowRoute{},
	}
}

// enqueue appends one edge to the serialised chain and returns a future for it.
// A non-close edge after a close is dropped, and its future is already resolved:
// that is what keeps a late SAT from reviving a paused CLI route.
func (f *flowController) enqueue(state FlowState, routes func() []flowRoute) <-chan error {
	result := make(chan error, 1)
	f.chainMu.Lock()
	defer f.chainMu.Unlock()
	if f.closed && state != FlowClosed {
		result <- nil
		close(result)
		return result
	}
	previous := f.chain
	tail := make(chan struct{})
	f.chain = tail
	go func() {
		if previous != nil {
			// The previous edge's failure is not this edge's failure: close must
			// still run when an earlier forward failed.
			<-previous
		}
		err := f.apply(state, routes)
		close(tail)
		result <- err
		close(result)
	}()
	return result
}

// apply fans one edge out to the routes that exist now. The first failure is
// returned to this edge's caller, but every remaining route is still attempted —
// one bad route must not strand the others.
func (f *flowController) apply(state FlowState, routes func() []flowRoute) error {
	f.mu.Lock()
	if f.closed && state != FlowClosed {
		f.mu.Unlock()
		return nil
	}
	f.current = state
	if state == FlowClosed {
		f.closed = true
	}
	f.mu.Unlock()

	var firstErr error
	for _, route := range routes() {
		if err := f.forward(route, state); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

// forward sends one edge to one route, skipping a repeat of the last state. close is
// never skipped: it is the only edge that clears the route's remembered state.
func (f *flowController) forward(route flowRoute, state FlowState) error {
	f.mu.Lock()
	if state != FlowClosed && f.forwardedByRoute[route.key] == state {
		f.mu.Unlock()
		return nil
	}
	f.mu.Unlock()

	if err := f.base.SetConnectionFlowState(context.Background(), route.flowCall(state), state); err != nil {
		return err
	}
	f.mu.Lock()
	f.forwardedByRoute[route.key] = state
	f.mu.Unlock()
	return nil
}

// closeUnused closes a route that has no attachment and no subscription left, then
// forgets it. It is the last-subscription teardown the TS closeUnusedFlowRoute
// performs: the close goes out BEFORE the unsubscribe reaches the base, because the
// reverse order makes the upstream forget the owner first and reject the close.
func (f *flowController) closeUnused(route flowRoute, stillUsed bool) error {
	if stillUsed {
		return nil
	}
	f.mu.Lock()
	_, forwarded := f.forwardedByRoute[route.key]
	if _, attached := f.attachmentRoutes[route.key]; attached {
		f.mu.Unlock()
		return nil
	}
	f.mu.Unlock()
	if !forwarded {
		return nil
	}
	if err := f.forward(route, FlowClosed); err != nil {
		return err
	}
	f.mu.Lock()
	delete(f.forwardedByRoute, route.key)
	f.mu.Unlock()
	return nil
}

// registerAttachment adds an attachment Begin route (an upload reached staging).
func (f *flowController) registerAttachment(route flowRoute) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.attachmentRoutes[route.key] = route
}

func (f *flowController) unregisterAttachment(route flowRoute) {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.attachmentRoutes, route.key)
}

// currentState reports the last applied state, for the post-subscribe replay.
func (f *flowController) currentState() FlowState {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.current
}

// isClosed reports whether a close edge has been applied.
func (f *flowController) isClosed() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.closed
}

// reset clears every remembered route, for disposal.
func (f *flowController) reset() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.forwardedByRoute = map[flowRouteKey]FlowState{}
	f.attachmentRoutes = map[flowRouteKey]flowRoute{}
}
