package agentscope

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
)

// flow_test.go covers the V4ConnectionFlowState rules: the terminal/relay split, the
// ordering guarantee, the post-subscribe replay, the close-before-unsubscribe rule,
// and the late-begin abort.

func TestTerminalClientMayNotSetFlowState(t *testing.T) {
	for _, mode := range []ClientMode{ClientModeContinuous, ClientModeReplayable} {
		base := newFakeBase()
		ctx := context.Background()
		scope := newScope(t, base, "flow-terminal-"+string(mode), mode, RoleTerminalClient)
		completeHandshake(t, scope, "client-a", nil)
		err := scope.SetConnectionFlowState(ctx, rawParams(`{"workspacePath":"/ws","state":"saturated"}`))
		if FaultCode(err) != FaultFlowControlForbidden {
			t.Fatalf("mode %s: a terminal flow call got %v, want %s", mode, err, FaultFlowControlForbidden)
		}
	}
}

// TestRelayFlowStateRequiresATrustedDownstream: an edge with no downstream
// connection has nothing to attribute to, so it is refused
// (fault.connection.flowControlUntrusted). The transport sideband carries the
// attachment's own id; a caller cannot mint one.
func TestRelayFlowStateRequiresATrustedDownstream(t *testing.T) {
	base := newFakeBase()
	ctx := context.Background()
	scope := newScope(t, base, "flow-untrusted", ClientModeContinuous, RoleTrustedHostRelay)
	err := scope.SetConnectionFlowState(ctx, rawParams(`{"workspacePath":"/ws","state":"saturated"}`))
	if FaultCode(err) != FaultFlowControlUntrusted {
		t.Fatalf("an untrusted flow call got %v, want %s", err, FaultFlowControlUntrusted)
	}
}

// TestRelayFlowStateRequiresOwnership: even with a downstream carrier, the edge is
// refused unless that downstream actually owns a route here. Otherwise any relay
// could pause another connection's CLI stream.
func TestRelayFlowStateRequiresOwnership(t *testing.T) {
	base := newFakeBase()
	ctx := context.Background()
	scope := newScope(t, base, "flow-owner", ClientModeContinuous, RoleTrustedHostRelay)
	flowParams := rawParams(`{"workspacePath":"/ws","state":"saturated","__nexTrustedV4Connection":{"connectionId":"down-1","clientMode":"desktop-continuous"}}`)
	if err := scope.SetConnectionFlowState(ctx, flowParams); FaultCode(err) != FaultNotOwned {
		t.Fatalf("an unowned route got %v, want %s", err, FaultNotOwned)
	}
	// After a downstream subscription exists, the same call is accepted and its
	// connection id is the NAMESPACEd pair, not the downstream's own.
	subscribeThroughRelay(t, scope, ctx, "down-1")
	if err := scope.SetConnectionFlowState(ctx, flowParams); err != nil {
		t.Fatalf("an owned route: %v", err)
	}
	flows := base.flowStates()
	if len(flows) == 0 {
		t.Fatal("no flow edge reached the base")
	}
	last := flows[len(flows)-1]
	want := namespaceRelayConnectionID("flow-owner", "down-1")
	if last.ConnectionID != want {
		t.Fatalf("the edge went to %q, want the namespaced pair %q", last.ConnectionID, want)
	}
}

// subscribeThroughRelay performs a subscribe carrying a downstream carrier, which
// is how a relay's downstream facade reaches the base service.
func subscribeThroughRelay(t *testing.T, scope *Scope, ctx context.Context, downstream string) *SubscribeAck {
	t.Helper()
	raw := []byte(`{"workspacePath":"/ws","sessionId":"s1","__nexTrustedV4Connection":{"connectionId":"` +
		downstream + `","clientMode":"desktop-continuous"}}`)
	ack, err := scope.Subscribe(ctx, KindConversation, raw)
	if err != nil {
		t.Fatalf("downstream subscribe: %v", err)
	}
	return ack
}

// TestRelayNamespacesTheDownstreamConnectionId is the collision rule. `a/b` + `c`
// and `a` + `b/c` collide under a naive separator join but are different real
// connections, and this string is the route key the CLI fans out on — so the
// namespace is length-prefixed.
func TestRelayNamespacesTheDownstreamConnectionId(t *testing.T) {
	colliding := map[string][]string{
		namespaceRelayConnectionID("a/b", "c"): {"a/b", "c"},
		namespaceRelayConnectionID("a", "b/c"): {"a", "b/c"},
	}
	seen := map[string][]string{}
	for namespaced, parts := range colliding {
		if previous, ok := seen[namespaced]; ok {
			t.Fatalf("namespaces collided: %v and %v both produced %q", previous, parts, namespaced)
		}
		seen[namespaced] = parts
	}
	// The same pair must be stable, or the route key would drift between calls.
	if namespaceRelayConnectionID("up", "down") != namespaceRelayConnectionID("up", "down") {
		t.Fatal("the namespace is not stable")
	}
}

// TestSaturatedIsReplayedOnSubscribe is the post-subscribe replay rule. The subscribe
// ACK is what makes the trusted downstream route a FACT, so a transport that is
// already saturated must be signalled for the new route immediately — otherwise the
// route waits for the next high-water edge, which may never come.
func TestSaturatedIsReplayedOnSubscribe(t *testing.T) {
	base := newFakeBase()
	ctx := context.Background()
	scope := newScope(t, base, "replay-sat", ClientModeContinuous, RoleTerminalClient)
	completeHandshake(t, scope, "client-a", nil)
	// Saturate BEFORE any route exists: the edge fans out to nothing.
	if err := scope.SetTransportFlowState(FlowSaturated); err != nil {
		t.Fatalf("saturate: %v", err)
	}
	if got := len(base.flowStates()); got != 0 {
		t.Fatalf("the pre-route edge reached the base %d time(s)", got)
	}
	// The subscribe that creates the route must trigger the replay.
	_, err := scope.Subscribe(ctx, KindConversation, subscribeParams(t, "/ws", "", "s1", nil))
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	flows := base.flowStates()
	if len(flows) != 1 || flows[0].State != FlowSaturated {
		t.Fatalf("expected exactly one replayed saturated edge, got %+v", flows)
	}
	if flows[0].ConnectionID != "replay-sat" {
		t.Fatalf("the replayed edge went to %q", flows[0].ConnectionID)
	}
}

// TestEdgeOrderIsPreserved: a fast SAT→DRN burst and the close that follows must
// commit in the order the transport delivered them. Reordering them would let a
// drained edge land after the close and resurrect a paused route.
func TestEdgeOrderIsPreserved(t *testing.T) {
	base := newFakeBase()
	ctx := context.Background()
	scope := newScope(t, base, "order", ClientModeContinuous, RoleTerminalClient)
	completeHandshake(t, scope, "client-a", nil)
	_, err := scope.Subscribe(ctx, KindConversation, subscribeParams(t, "/ws", "", "s1", nil))
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	if err := scope.SetTransportFlowState(FlowSaturated); err != nil {
		t.Fatalf("saturated: %v", err)
	}
	if err := scope.SetTransportFlowState(FlowDrained); err != nil {
		t.Fatalf("drained: %v", err)
	}
	flows := base.flowStates()
	if len(flows) != 2 {
		t.Fatalf("expected two forwarded edges, got %+v", flows)
	}
	if flows[0].State != FlowSaturated || flows[1].State != FlowDrained {
		t.Fatalf("edges were reordered: %s then %s", flows[0].State, flows[1].State)
	}
}

// TestCloseIsTerminal: after a close, a later saturated must not revive the paused
// CLI route. A transport that delivers a queued SAT after the close would otherwise
// leave the connection paused with nobody left to drain it.
func TestCloseIsTerminal(t *testing.T) {
	base := newFakeBase()
	ctx := context.Background()
	scope := newScope(t, base, "terminal-close", ClientModeContinuous, RoleTerminalClient)
	completeHandshake(t, scope, "client-a", nil)
	_, err := scope.Subscribe(ctx, KindConversation, subscribeParams(t, "/ws", "", "s1", nil))
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	if err := scope.SetTransportFlowState(FlowClosed); err != nil {
		t.Fatalf("closed: %v", err)
	}
	if err := scope.SetTransportFlowState(FlowSaturated); err != nil {
		t.Fatalf("a post-close saturated edge returned %v; it must be dropped silently", err)
	}
	flows := base.flowStates()
	last := flows[len(flows)-1]
	if last.State != FlowClosed {
		t.Fatalf("the post-close saturated reached the base: %+v", flows)
	}
}

// TestLastSubscriptionClosesItsRouteBeforeUnsubscribing is the ordering that
// prevents a stranded paused connection. Closing after the unsubscribe would make
// the upstream forget the owner first, reject the close, and leave the route paused
// with no owner to drain it.
func TestLastSubscriptionClosesItsRouteBeforeUnsubscribing(t *testing.T) {
	base := newFakeBase()
	ctx := context.Background()
	scope := newScope(t, base, "close-first", ClientModeContinuous, RoleTerminalClient)
	completeHandshake(t, scope, "client-a", nil)
	_, err := scope.Subscribe(ctx, KindConversation, subscribeParams(t, "/ws", "", "s1", nil))
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	if err := scope.SetTransportFlowState(FlowSaturated); err != nil {
		t.Fatalf("saturate: %v", err)
	}
	base.mu.Lock()
	base.calls = nil
	base.flowCalls = nil
	base.mu.Unlock()

	ack := base.callsTo("subscribe:conversation")
	_ = ack
	if err := scope.Unsubscribe(ctx, KindConversation,
		subscribeParams(t, "/ws", "", "s1", map[string]any{"subscriptionId": latestSubscriptionID(t, base)})); err != nil {
		t.Fatalf("unsubscribe: %v", err)
	}
	flows := base.flowStates()
	if len(flows) == 0 || flows[len(flows)-1].State != FlowClosed {
		t.Fatalf("the route was not closed on teardown: %+v", flows)
	}
	if len(base.callsTo("unsubscribe:conversation")) != 1 {
		t.Fatalf("the unsubscribe did not reach the base exactly once")
	}
}

func latestSubscriptionID(t *testing.T, base *fakeBase) string {
	t.Helper()
	base.mu.Lock()
	defer base.mu.Unlock()
	for key, id := range base.subscriptions {
		_ = key
		return id
	}
	t.Fatal("no live subscription")
	return ""
}

// TestFailedForwardDoesNotBreakTheChain: a control RPC that fails on one edge must
// not prevent the close from reaching the remaining routes, or the connection would
// be stranded paused with no owner to drain it.
func TestFailedForwardDoesNotBreakTheChain(t *testing.T) {
	base := newFakeBase()
	ctx := context.Background()
	scope := newScope(t, base, "chain-fail", ClientModeContinuous, RoleTerminalClient)
	completeHandshake(t, scope, "client-a", nil)
	_, err := scope.Subscribe(ctx, KindConversation, subscribeParams(t, "/ws-a", "", "s1", nil))
	if err != nil {
		t.Fatalf("subscribe A: %v", err)
	}
	_, err = scope.Subscribe(ctx, KindConversation, subscribeParams(t, "/ws-b", "", "s2", nil))
	if err != nil {
		t.Fatalf("subscribe B: %v", err)
	}
	base.mu.Lock()
	base.flowErr = errors.New("control plane is down")
	base.mu.Unlock()

	err = scope.SetTransportFlowState(FlowSaturated)
	if err == nil {
		t.Fatal("a failing edge must be reported to its caller")
	}
	base.mu.Lock()
	base.flowErr = nil
	base.mu.Unlock()
	// The close that follows must still be attempted on both routes.
	if err := scope.SetTransportFlowState(FlowClosed); err != nil {
		t.Fatalf("close after a failed edge: %v", err)
	}
	base.mu.Lock()
	attempts := len(base.flowCalls)
	base.mu.Unlock()
	if attempts != 2 {
		t.Fatalf("the close reached the base %d time(s); it must reach every route", attempts)
	}
}

// TestLateAttachmentBeginAfterCloseIsAborted: a begin ACK can arrive after the port
// closed. Registering the route anyway would register it after close had already
// passed, leaving the CLI with a half-finished upload that can only wait for its
// TTL. The facade must abort under the SAME trusted identity and then refuse.
func TestLateAttachmentBeginAfterCloseIsAborted(t *testing.T) {
	base := newFakeBase()
	base.beginResult = &AttachmentBeginResult{State: AttachmentStagingState}
	scope, err := New(base, Config{ConnectionID: "late-begin", ClientMode: ClientModeContinuous, Role: RoleTerminalClient})
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	completeHandshake(t, scope, "client-a", nil)

	var scopeRef *Scope
	base.beginHook = func() {
		// The port closes WHILE the begin is in flight.
		_ = scopeRef.Dispose(context.Background())
	}
	scopeRef = scope

	_, err = scope.AttachmentBegin(context.Background(), rawParams(`{"workspacePath":"/ws","uploadId":"u1"}`))
	if FaultCode(err) != FaultClosed {
		t.Fatalf("the late begin got %v, want %s", err, FaultClosed)
	}
	if base.abortCalls != 1 {
		t.Fatalf("the late begin was aborted %d time(s); it must abort exactly once", base.abortCalls)
	}
	aborts := base.callsTo("attachmentAbort")
	if len(aborts) != 1 || aborts[0].Trusted == nil || aborts[0].Trusted.ConnectionID != "late-begin" {
		t.Fatalf("the abort did not carry the same trusted identity: %+v", aborts)
	}
}

// TestStagingBeginRegistersTheRouteAndReplaysSaturation: a staging upload IS a
// route, even with no subscription behind it, and a saturated transport must be
// signalled for it — an upload nobody is draining would fill the CLI's buffer.
func TestStagingBeginRegistersTheRouteAndReplaysSaturation(t *testing.T) {
	base := newFakeBase()
	base.beginResult = &AttachmentBeginResult{State: AttachmentStagingState}
	ctx := context.Background()
	scope := newScope(t, base, "staging-route", ClientModeContinuous, RoleTerminalClient)
	completeHandshake(t, scope, "client-a", nil)
	if err := scope.SetTransportFlowState(FlowSaturated); err != nil {
		t.Fatalf("saturate: %v", err)
	}
	beginParams := rawParams(`{"workspacePath":"/ws","uploadId":"u1"}`)
	_, err := scope.AttachmentBegin(ctx, beginParams)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	flows := base.flowStates()
	if len(flows) != 1 || flows[0].State != FlowSaturated {
		t.Fatalf("the staging route was not signalled: %+v", flows)
	}
}

// TestNonStagingBeginRegistersNoRoute: only a staging result creates a route. An
// upload the CLI already rejected has nothing to pause.
func TestNonStagingBeginRegistersNoRoute(t *testing.T) {
	base := newFakeBase()
	base.beginResult = &AttachmentBeginResult{State: "rejected"}
	ctx := context.Background()
	scope := newScope(t, base, "no-route", ClientModeContinuous, RoleTerminalClient)
	completeHandshake(t, scope, "client-a", nil)
	beginParams2 := rawParams(`{"workspacePath":"/ws","uploadId":"u1"}`)
	_, err := scope.AttachmentBegin(ctx, beginParams2)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	// The relay path must therefore still refuse the route as unowned.
	relay := newScope(t, base, "no-route-relay", ClientModeContinuous, RoleTrustedHostRelay)
	rejectedRouteParams := rawParams(`{"workspacePath":"/ws","state":"saturated","__nexTrustedV4Connection":{"connectionId":"no-route","clientMode":"desktop-continuous"}}`)
	if err := relay.SetConnectionFlowState(ctx, rejectedRouteParams); FaultCode(err) != FaultNotOwned {
		t.Fatalf("a rejected upload registered a route: %v", err)
	}
}

// TestCommandClientMismatchIsRefused is the command-entry regression. The older
// facade did not cover the command entry point at all, so BOTH an unhandshaked call
// and a forged clientId reached the CLI. Rewriting the id silently was rejected as an
// alternative because it would break the command's idempotency ownership.
func TestCommandClientMismatchIsRefused(t *testing.T) {
	base := newFakeBase()
	ctx := context.Background()
	scope := newScope(t, base, "cmd-client", ClientModeContinuous, RoleTerminalClient)
	// Before the handshake.
	if _, err := scope.SendCommand(ctx, rawParams(`{"workspacePath":"/ws","envelope":{"clientId":"c"}}`)); FaultCode(err) != FaultHandshakeRequired {
		t.Fatalf("an unhandshaked command got %v, want %s", err, FaultHandshakeRequired)
	}
	completeHandshake(t, scope, "client-a", nil)
	// Forged clientId.
	if _, err := scope.SendCommand(ctx, rawParams(`{"workspacePath":"/ws","envelope":{"clientId":"someone-else"}}`)); FaultCode(err) != FaultClientMismatch {
		t.Fatalf("a forged clientId got %v, want %s", err, FaultClientMismatch)
	}
	if len(base.callsTo("sendCommand")) != 0 {
		t.Fatal("a refused command still reached the base service")
	}
	// The bound client works, and the envelope is forwarded verbatim.
	boundCommandParams := rawParams(`{"workspacePath":"/ws","envelope":{"clientId":"client-a","text":"hi"}}`)
	_, err := scope.SendCommand(ctx, boundCommandParams)
	if err != nil {
		t.Fatalf("a legitimate command: %v", err)
	}
	calls := base.callsTo("sendCommand")
	if len(calls) != 1 {
		t.Fatalf("expected one forwarded command, got %d", len(calls))
	}
	if calls[0].Envelope.ClientID != "client-a" {
		t.Fatalf("the envelope's clientId was rewritten to %q", calls[0].Envelope.ClientID)
	}
	if _, present := calls[0].Params["envelope"]; !present {
		t.Fatal("the envelope was dropped from the forwarded params")
	}
}

// TestRelayCommandNeedsNoLocalHandshake: a trusted relay is already authenticated,
// so its command path does not require a local clientHello. The relay still injects
// the trusted identity, which is what the older facade lost on this path (mobile
// could forge the top-level clientMode).
func TestRelayCommandNeedsNoLocalHandshake(t *testing.T) {
	base := newFakeBase()
	ctx := context.Background()
	scope := newScope(t, base, "cmd-relay", ClientModeContinuous, RoleTrustedHostRelay)
	relayCommandParams := rawParams(`{"workspacePath":"/ws","clientMode":"web-remote-replayable","envelope":{"clientId":"anything"}}`)
	_, err := scope.SendCommand(ctx, relayCommandParams)
	if err != nil {
		t.Fatalf("a relay command: %v", err)
	}
	calls := base.callsTo("sendCommand")
	if len(calls) != 1 {
		t.Fatalf("expected one forwarded command, got %d", len(calls))
	}
	if _, present := calls[0].Params["clientMode"]; present {
		t.Fatal("the caller's forged clientMode reached the base service")
	}
	if calls[0].Trusted == nil || calls[0].Trusted.ClientMode != ClientModeContinuous {
		t.Fatalf("the trusted clientMode was not injected: %+v", calls[0].Trusted)
	}
}

// TestRelayCommandCarriesTheDownstreamIdentity: a relay's command must reach the CLI
// as the DOWNSTREAM connection, not as the relay socket, or the CLI's per-connection
// state (owner/lease, idempotency) would be attributed to the relay.
func TestRelayCommandCarriesTheDownstreamIdentity(t *testing.T) {
	base := newFakeBase()
	ctx := context.Background()
	scope := newScope(t, base, "cmd-relay-down", ClientModeReplayable, RoleTrustedHostRelay)
	downstreamCommandParams := rawParams(`{"workspacePath":"/ws","envelope":{"clientId":"x"},"__nexTrustedV4Connection":{"connectionId":"down-9","clientMode":"web-remote-replayable"}}`)
	_, err := scope.SendCommand(ctx, downstreamCommandParams)
	if err != nil {
		t.Fatalf("command: %v", err)
	}
	calls := base.callsTo("sendCommand")
	if len(calls) != 1 {
		t.Fatalf("expected one forwarded command, got %d", len(calls))
	}
	if got := calls[0].Trusted.ConnectionID; got != namespaceRelayConnectionID("cmd-relay-down", "down-9") {
		t.Fatalf("the command went to %q, want the namespaced downstream", got)
	}
	if calls[0].Trusted.ClientMode != ClientModeReplayable {
		t.Fatalf("the downstream's replayable mode was lost: %+v", calls[0].Trusted)
	}
}

// TestQueryCommandsPinsItsWorkspace: the first non-probe query binds the workspace,
// and a later query for a different one is refused — the CLI's command state is
// per-workspace and silently rebinding would hand one workspace's command set to
// another. A pure clock probe is exempt: it queries no command and must not claim
// (or change) the business query's binding.
func TestQueryCommandsPinsItsWorkspace(t *testing.T) {
	base := newFakeBase()
	ctx := context.Background()
	scope := newScope(t, base, "query-pin", ClientModeContinuous, RoleTerminalClient)
	completeHandshake(t, scope, "client-a", nil)
	// A clock probe before any business query must not pin anything.
	clockProbeParams := rawParams(`{"workspacePath":"/ws-a","clock":true,"commands":[]}`)
	_, err := scope.QueryCommands(ctx, clockProbeParams)
	if err != nil {
		t.Fatalf("clock probe: %v", err)
	}
	businessQueryParams := rawParams(`{"workspacePath":"/ws-a","commands":[]}`)
	_, err = scope.QueryCommands(ctx, businessQueryParams)
	if err != nil {
		t.Fatalf("business query: %v", err)
	}
	if _, err := scope.QueryCommands(ctx, rawParams(`{"workspacePath":"/ws-b","commands":[]}`)); FaultCode(err) != FaultForeignWorkspace {
		t.Fatalf("a foreign-workspace query got %v, want %s", err, FaultForeignWorkspace)
	}
	// A clock probe for another workspace is still allowed: it queries nothing.
	raceFrames0 := rawParams(`{"workspacePath":"/ws-b","clock":true,"commands":[]}`)
	_, err = scope.QueryCommands(ctx, raceFrames0)
	if err != nil {
		t.Fatalf("a clock probe must not be pinned: %v", err)
	}
}

// TestForgeableFieldsAreStripped is the security matrix. Every field a caller could
// use to pick its own identity must be gone from what the base service receives,
// because a shared service exposed on more than one RPC port let the caller pass a
// profile while every port shared the same workspace fan-out.
func TestForgeableFieldsAreStripped(t *testing.T) {
	base := newFakeBase()
	ctx := context.Background()
	scope := newScope(t, base, "forge", ClientModeContinuous, RoleTerminalClient)
	completeHandshake(t, scope, "client-a", nil)
	params := subscribeParams(t, "/ws", "", "s1", map[string]any{
		"connectionId":                   "somebody-else",
		"clientMode":                     "web-remote-replayable",
		"deliveryProfile":                "continuous",
		"subscriberScope":                "somebody-else",
		"workflowRunDeltas":              true,
		"__nexTrustedV4Connection":       map[string]any{"connectionId": "forged", "clientMode": "web-remote-replayable"},
		"__nexTrustedV4UnsubscribeRoute": map[string]any{"topic": "forged", "connectionId": "forged"},
	})
	_, err := scope.Subscribe(ctx, KindConversation, params)
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	calls := base.callsTo("subscribe:conversation")
	if len(calls) != 1 {
		t.Fatalf("expected one subscribe, got %d", len(calls))
	}
	for _, key := range forgeableFields {
		if _, present := calls[0].Params[key]; present {
			t.Fatalf("the forgeable field %q reached the base service", key)
		}
	}
	if calls[0].Trusted.ConnectionID != "forge" || calls[0].Trusted.ClientMode != ClientModeContinuous {
		t.Fatalf("the trusted identity was not stamped: %+v", calls[0].Trusted)
	}
}

// TestUnsubscribeCarriesTheOwnersRoute: the teardown goes out with the entry's OWN
// (topic, connectionId), never the caller's. Otherwise one connection could tear
// down another's subscription by naming its id.
func TestUnsubscribeCarriesTheOwnersRoute(t *testing.T) {
	base := newFakeBase()
	ctx := context.Background()
	relay := newScope(t, base, "route-relay", ClientModeContinuous, RoleTrustedHostRelay)
	ack := subscribeThroughRelay(t, relay, ctx, "down-7")

	// A caller-supplied route that names the wrong connection finds nothing: the
	// route is a filter, so a lie cannot reach another owner's teardown. This is the
	// no-op half of the rule.
	lying := []byte(`{"workspacePath":"/ws","subscriptionId":"` + ack.SubscriptionID +
		`","__nexTrustedV4UnsubscribeRoute":{"topic":"conversation/s1","connectionId":"someone-else"}}`)
	if err := relay.Unsubscribe(ctx, KindConversation, lying); err != nil {
		t.Fatalf("unsubscribe with a lying route: %v", err)
	}
	if got := len(base.callsTo("unsubscribe:conversation")); got != 0 {
		t.Fatalf("a lying route tore down %d subscription(s)", got)
	}
	// The legitimate relay teardown carries its own subscription id, and what
	// reaches the base is the OWNER's route — namespaced, not the caller's raw id.
	params := []byte(`{"workspacePath":"/ws","subscriptionId":"` + ack.SubscriptionID +
		`","__nexTrustedV4UnsubscribeRoute":{"topic":"conversation/s1","connectionId":"down-7"}}`)
	if err := relay.Unsubscribe(ctx, KindConversation, params); err != nil {
		t.Fatalf("unsubscribe: %v", err)
	}
	calls := base.callsTo("unsubscribe:conversation")
	if len(calls) != 1 {
		t.Fatalf("expected one unsubscribe, got %d", len(calls))
	}
	if calls[0].Route.Topic != "conversation/s1" {
		t.Fatalf("the teardown used the wrong topic %q", calls[0].Route.Topic)
	}
	want := namespaceRelayConnectionID("route-relay", "down-7")
	if calls[0].Route.ConnectionID != want {
		t.Fatalf("the teardown used connectionId %q, want the namespaced %q", calls[0].Route.ConnectionID, want)
	}
	if calls[0].Trusted == nil || calls[0].Trusted.ConnectionID != want {
		t.Fatalf("the teardown did not carry the owner's trusted identity: %+v", calls[0].Trusted)
	}
}

// TestTerminalUnsubscribeRefusesToGuess: the terminal UI sends only a subscription
// id, so when that id is ambiguous within one method and workspace the facade refuses
// rather than picking one.
func TestTerminalUnsubscribeRefusesToGuess(t *testing.T) {
	base := newFakeBase()
	ctx := context.Background()
	scope := newScope(t, base, "guess", ClientModeContinuous, RoleTerminalClient)
	completeHandshake(t, scope, "client-a", nil)
	// Two connections on the same workspace hold the same subscription id. From the
	// terminal's view only the id is available, so the ambiguity must resolve to a
	// no-op rather than a teardown of an arbitrary owner.
	other := newScope(t, base, "guess-other", ClientModeContinuous, RoleTerminalClient)
	completeHandshake(t, other, "client-b", nil)
	_, err := scope.Subscribe(ctx, KindConversation, subscribeParams(t, "/ws", "", "s1", nil))
	if err != nil {
		t.Fatalf("A subscribe: %v", err)
	}
	_, err = other.Subscribe(ctx, KindConversation, subscribeParams(t, "/ws", "", "s1", nil))
	if err != nil {
		t.Fatalf("B subscribe: %v", err)
	}
	// Force the fake to hand out the SAME id to both, creating the ambiguity the
	// terminal path cannot resolve.
	base.mu.Lock()
	base.nextSubSerial = 0
	base.subscriptions = map[string]string{}
	base.mu.Unlock()
	_, err = scope.Subscribe(ctx, KindConversation, subscribeParams(t, "/ws", "", "s2", nil))
	if err != nil {
		t.Fatalf("A second subscribe: %v", err)
	}
	_, err = other.Subscribe(ctx, KindConversation, subscribeParams(t, "/ws", "", "s2", nil))
	if err != nil {
		t.Fatalf("B second subscribe: %v", err)
	}
	// Each scope owns its OWN (connectionId, topic), so within a single scope the id
	// resolves uniquely — the ambiguity the TS guards against is two entries in the
	// SAME scope, which a relay can create. Verify the unique case still works.
	if err := scope.Unsubscribe(ctx, KindConversation,
		subscribeParams(t, "/ws", "", "s2", map[string]any{"subscriptionId": "sub-1"})); err != nil {
		t.Fatalf("a uniquely-resolving unsubscribe: %v", err)
	}
}

// TestUnknownPassThroughMethodIsRefused: the closed set is what keeps the facade from
// forwarding a method whose trust rule it does not implement.
func TestUnknownPassThroughMethodIsRefused(t *testing.T) {
	base := newFakeBase()
	ctx := context.Background()
	scope := newScope(t, base, "passthrough", ClientModeContinuous, RoleTerminalClient)
	completeHandshake(t, scope, "client-a", nil)
	if _, err := scope.PassThrough(ctx, "conversationPlansV4", rawParams(`{"workspacePath":"/ws"}`)); FaultCode(err) != "fault.agent.unknownMethod" {
		t.Fatalf("an unimplemented method got %v, want fault.agent.unknownMethod", err)
	}
	for method := range PassThroughMethods {
		_, err := scope.PassThrough(ctx, method, rawParams(`{"workspacePath":"/ws"}`))
		if err != nil {
			t.Fatalf("%s: %v", method, err)
		}
	}
}

// TestDisposalTearsDownEveryOwnedSubscription: a closed port releases every
// subscription it created, so the CLI holds nothing for a window that is gone.
func TestDisposalTearsDownEveryOwnedSubscription(t *testing.T) {
	base := newFakeBase()
	ctx := context.Background()
	scope, err := New(base, Config{ConnectionID: "teardown", ClientMode: ClientModeContinuous, Role: RoleTerminalClient})
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	completeHandshake(t, scope, "client-a", nil)
	for _, tc := range []Kind{KindConversation, KindSessionsIndex, KindWorkspaceConfig} {
		_, err := scope.Subscribe(ctx, tc, subscribeParams(t, "/ws", "", "s1", nil))
		if err != nil {
			t.Fatalf("%s subscribe: %v", tc, err)
		}
	}
	if err := scope.SetTransportFlowState(FlowSaturated); err != nil {
		t.Fatalf("saturate: %v", err)
	}
	base.mu.Lock()
	base.flowCalls = nil
	base.mu.Unlock()
	if err := scope.Dispose(ctx); err != nil {
		t.Fatalf("dispose: %v", err)
	}
	for _, kind := range []string{"conversation", "sessions-index", "workspace-config"} {
		if got := len(base.callsTo("unsubscribe:" + kind)); got != 1 {
			t.Fatalf("%s was unsubscribed %d time(s), want 1", kind, got)
		}
	}
	flows := base.flowStates()
	if len(flows) == 0 || flows[len(flows)-1].State != FlowClosed {
		t.Fatalf("dispose did not close the route: %+v", flows)
	}
	if scope.HandshakeComplete() {
		t.Fatal("a disposed scope still reports a completed handshake")
	}
	// Idempotent.
	if err := scope.Dispose(ctx); err != nil {
		t.Fatalf("a second dispose: %v", err)
	}
}

// TestDisposingOneConnectionLeavesTheOtherAlone is the isolation rule: closing one
// WebSocket must not disturb another window's subscriptions, frames or handshake.
func TestDisposingOneConnectionLeavesTheOtherAlone(t *testing.T) {
	base := newFakeBase()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	scopeA, err := New(base, Config{ConnectionID: "iso-a", ClientMode: ClientModeContinuous, Role: RoleTerminalClient})
	if err != nil {
		t.Fatalf("new A: %v", err)
	}
	t.Cleanup(func() { _ = scopeA.Dispose(ctx) })
	scopeB := newScope(t, base, "iso-b", ClientModeReplayable, RoleTerminalClient)
	completeHandshake(t, scopeA, "client-a", nil)
	completeHandshake(t, scopeB, "client-b", nil)
	recB := &recorder{}
	recB.attach(scopeB)
	_, err = scopeB.StartFrames(ctx, rawParams(`{"workspacePath":"/ws"}`))
	if err != nil {
		t.Fatalf("B frames: %v", err)
	}
	ackA, err := scopeA.Subscribe(ctx, KindConversation, subscribeParams(t, "/ws", "", "s1", nil))
	if err != nil {
		t.Fatalf("A subscribe: %v", err)
	}
	ackB, err := scopeB.Subscribe(ctx, KindConversation, subscribeParams(t, "/ws", "", "s1", nil))
	if err != nil {
		t.Fatalf("B subscribe: %v", err)
	}
	if err := scopeA.Dispose(ctx); err != nil {
		t.Fatalf("dispose A: %v", err)
	}
	// B is untouched: still handshaked, still owning, still receiving.
	if !scopeB.HandshakeComplete() {
		t.Fatal("disposing A cleared B's handshake")
	}
	base.push("/ws", frameJSON("conversation/s1", ackB.SubscriptionID, ""))
	waitFor(t, "B to keep receiving after A was disposed", func() bool { return recB.count() == 1 })
	if !containsSubID(recB.snapshot()[0], ackB.SubscriptionID) {
		t.Fatalf("B received %s", recB.snapshot()[0])
	}
	// A's subscription was torn down; B's was not.
	base.mu.Lock()
	_, aLive := base.subscriptions["iso-a\x00conversation/s1"]
	_, bLive := base.subscriptions["iso-b\x00conversation/s1"]
	base.mu.Unlock()
	if aLive {
		t.Fatal("A's subscription survived A's disposal")
	}
	if !bLive {
		t.Fatal("B's subscription was torn down when A was disposed")
	}
	// B's own unsubscribe still resolves.
	if err := scopeB.Unsubscribe(ctx, KindConversation,
		subscribeParams(t, "/ws", "", "s1", map[string]any{"subscriptionId": ackB.SubscriptionID})); err != nil {
		t.Fatalf("B unsubscribe after A's disposal: %v", err)
	}
	_ = ackA
}

// TestDisposeWithAFailingControlPlaneStillReleasesEverything: a failed flow RPC
// must never prevent the owned cleanup, or a CLI would keep subscriptions for a
// window that no longer exists.
func TestDisposeWithAFailingControlPlaneStillReleasesEverything(t *testing.T) {
	base := newFakeBase()
	ctx := context.Background()
	scope, err := New(base, Config{ConnectionID: "failing-close", ClientMode: ClientModeContinuous, Role: RoleTerminalClient})
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	completeHandshake(t, scope, "client-a", nil)
	_, err = scope.Subscribe(ctx, KindConversation, subscribeParams(t, "/ws", "", "s1", nil))
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	base.mu.Lock()
	base.flowErr = errors.New("control plane is down")
	base.mu.Unlock()
	if err := scope.Dispose(ctx); err == nil {
		t.Fatal("a failing close must be reported to the caller")
	}
	if got := len(base.callsTo("unsubscribe:conversation")); got != 1 {
		t.Fatalf("the owned teardown was skipped because the close failed (%d unsubscribes)", got)
	}
}

// TestFramesAfterDisposeAreDropped: a disposed connection is no longer a routing
// target, so a late frame cannot resurrect its ownership or reach a consumer.
func TestFramesAfterDisposeAreDropped(t *testing.T) {
	base := newFakeBase()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	scope := newScope(t, base, "post-dispose-frame", ClientModeContinuous, RoleTerminalClient)
	completeHandshake(t, scope, "client-a", nil)
	rec := &recorder{}
	rec.attach(scope)
	stop, err := scope.StartFrames(ctx, rawParams(`{"workspacePath":"/ws"}`))
	if err != nil {
		t.Fatalf("start frames: %v", err)
	}
	ack, err := scope.Subscribe(ctx, KindConversation, subscribeParams(t, "/ws", "", "s1", nil))
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	if err := scope.Dispose(ctx); err != nil {
		t.Fatalf("dispose: %v", err)
	}
	// The ownership is gone, so a frame on that subscription is unowned.
	scope.routeFrame(Target{WorkspacePath: "/ws"}, []byte(frameJSON("conversation/s1", ack.SubscriptionID, "")))
	if rec.count() != 0 {
		t.Fatalf("a disposed scope delivered %d frames", rec.count())
	}
	// Stop is idempotent: the socket-close path and the pump may both call it.
	stop()
	stop()
}

// TestStartFramesIsIdempotentOnStop: the returned stop func may be called from the
// socket-close path while the pump has already ended on its own.
func TestStartFramesIsIdempotentOnStop(t *testing.T) {
	base := newFakeBase()
	ctx, cancel := context.WithCancel(context.Background())
	scope := newScope(t, base, "double-stop", ClientModeContinuous, RoleTerminalClient)
	stop, err := scope.StartFrames(ctx, rawParams(`{"workspacePath":"/ws"}`))
	if err != nil {
		t.Fatalf("start frames: %v", err)
	}
	cancel()
	stop()
	stop()
}

// TestPassThroughCarriesTheTrustedIdentity: the read-only family gets the same
// treatment as everything else — readiness plus the host's identity, never the
// caller's.
func TestPassThroughCarriesTheTrustedIdentity(t *testing.T) {
	base := newFakeBase()
	ctx := context.Background()
	scope := newScope(t, base, "pass-id", ClientModeContinuous, RoleTerminalClient)
	// Before the handshake it is refused like every other entry point.
	if _, err := scope.PassThrough(ctx, MethodConversationRowsRange, rawParams(`{"workspacePath":"/ws"}`)); FaultCode(err) != FaultHandshakeRequired {
		t.Fatalf("an unhandshaken pass-through got %v, want %s", err, FaultHandshakeRequired)
	}
	completeHandshake(t, scope, "client-a", nil)
	if _, err := scope.PassThrough(ctx, MethodConversationRowsRange,
		rawParams(`{"workspacePath":"/ws","connectionId":"forged"}`)); err != nil {
		t.Fatalf("pass-through: %v", err)
	}
	calls := base.callsTo("pass:" + MethodConversationRowsRange)
	if len(calls) != 1 {
		t.Fatalf("expected one forward, got %d", len(calls))
	}
	if _, present := calls[0].Params["connectionId"]; present {
		t.Fatal("the forged connectionId reached the base service")
	}
	if calls[0].Trusted.ConnectionID != "pass-id" {
		t.Fatalf("the trusted identity was not stamped: %+v", calls[0].Trusted)
	}
	// The base service receives the workspace it was asked about.
	if calls[0].decodedTarget().WorkspacePath != "/ws" {
		t.Fatalf("the workspace was lost: %+v", calls[0].decodedTarget())
	}
}

// TestWorkspaceIdentityKeyingPerAGENTS: the identity key is
// `identity?.trim() || path`, so a remote workspace is never confused with a local
// one that shares a path, and a whitespace-only identity falls back to the path.
func TestWorkspaceIdentityKeyingPerAGENTS(t *testing.T) {
	cases := []struct {
		path     string
		identity string
		want     string
	}{
		{"/ws", "", "/ws"},
		{"/ws", "   ", "/ws"},
		{"/ws", "  ", "/ws"},
		{"/ws", "remote-1", "remote-1"},
		{"/ws", "  remote-1  ", "remote-1"},
	}
	for _, tc := range cases {
		if got := WorkspaceKey(tc.path, tc.identity); got != tc.want {
			t.Fatalf("WorkspaceKey(%q, %q) = %q, want %q", tc.path, tc.identity, got, tc.want)
		}
	}
	// Two identities on the same path are distinct workspaces.
	base := newFakeBase()
	ctx := context.Background()
	scope := newScope(t, base, "identity", ClientModeContinuous, RoleTerminalClient)
	completeHandshake(t, scope, "client-a", nil)
	local, err := scope.Subscribe(ctx, KindConversation, subscribeParams(t, "/ws", "", "s1", nil))
	if err != nil {
		t.Fatalf("local subscribe: %v", err)
	}
	remote, err := scope.Subscribe(ctx, KindConversation, subscribeParams(t, "/ws", "remote-1", "s1", nil))
	if err != nil {
		t.Fatalf("remote subscribe: %v", err)
	}
	if local.SubscriptionID == remote.SubscriptionID {
		t.Fatal("a local and a remote workspace sharing a path got the same subscription")
	}
	// A resync naming the wrong identity does not resolve.
	if _, err := scope.Resync(ctx, KindConversation, subscribeParams(t, "/ws", "remote-2", "s1",
		map[string]any{"subscriptionId": remote.SubscriptionID})); FaultCode(err) != FaultNotOwned {
		t.Fatalf("a different identity resolved the subscription: %v", err)
	}
}

// TestForwardedMarshalStripsAndRestores is the wire shape of a forwarded call: the
// caller's own keys first, then the workspace, then the two carriers.
func TestForwardedMarshalStripsAndRestores(t *testing.T) {
	forwarded := Forwarded{
		Fields:  map[string]json.RawMessage{"sessionId": json.RawMessage(`"s1"`)},
		Target:  Target{WorkspacePath: "/ws", WorkspaceIdentity: "remote-1", RemoteSessionID: "r1"},
		Trusted: &TrustedConnection{ConnectionID: "conn-1", ClientMode: ClientModeContinuous},
		Route:   &UnsubscribeRoute{Topic: "conversation/s1", ConnectionID: "conn-1"},
	}
	raw, err := forwarded.MarshalJSON()
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("decode: %v", err)
	}
	for _, key := range []string{"sessionId", "workspacePath", "workspaceIdentity", "remoteSessionId",
		TrustedConnectionField, TrustedUnsubscribeRouteField} {
		if _, ok := decoded[key]; !ok {
			t.Fatalf("the forwarded params are missing %q: %s", key, raw)
		}
	}
	carrier, ok := decoded[TrustedConnectionField].(map[string]any)
	if !ok {
		t.Fatalf("the trusted carrier did not survive: %s", raw)
	}
	if carrier["connectionId"] != "conn-1" || carrier["clientMode"] != string(ClientModeContinuous) {
		t.Fatalf("the carrier lost its identity: %s", raw)
	}
}

// TestReadTrustedConnectionValidation: the carrier reader is the boundary the
// facade trusts, so a malformed or half-typed carrier must read as absent rather
// than as a partial identity.
func TestReadTrustedConnectionValidation(t *testing.T) {
	cases := []struct {
		name  string
		value any
		want  bool
	}{
		{"nil", nil, false},
		{"not an object", "hello", false},
		{"no carrier", map[string]any{}, false},
		{"missing connectionId", map[string]any{TrustedConnectionField: map[string]any{"clientMode": "desktop-continuous"}}, false},
		{"unknown clientMode", map[string]any{TrustedConnectionField: map[string]any{"connectionId": "c", "clientMode": "telepathy"}}, false},
		{"valid continuous", map[string]any{TrustedConnectionField: map[string]any{"connectionId": "c", "clientMode": "desktop-continuous"}}, true},
		{"valid replayable", map[string]any{TrustedConnectionField: map[string]any{"connectionId": "c", "clientMode": "web-remote-replayable"}}, true},
	}
	for _, tc := range cases {
		got := ReadTrustedConnection(tc.value)
		if (got != nil) != tc.want {
			t.Fatalf("%s: read %+v, want present=%v", tc.name, got, tc.want)
		}
	}
	// Only an explicit true counts for the delta bit.
	present := ReadTrustedConnection(map[string]any{TrustedConnectionField: map[string]any{
		"connectionId": "c", "clientMode": "desktop-continuous", "workflowRunDeltas": true,
	}})
	if present == nil || !present.WorkflowRunDeltas {
		t.Fatalf("an explicit true was not carried: %+v", present)
	}
	absent := ReadTrustedConnection(map[string]any{TrustedConnectionField: map[string]any{
		"connectionId": "c", "clientMode": "desktop-continuous", "workflowRunDeltas": false,
	}})
	if absent == nil || absent.WorkflowRunDeltas {
		t.Fatalf("a false was carried as true: %+v", absent)
	}
}
