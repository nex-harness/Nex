package agentscope

import (
	"context"
	"encoding/json"
	"testing"
)

// handshake_test.go covers the two client modes × both roles on the hello /
// clientHello path, plus every rule the TS comments attach to the handshake.

func TestNewRejectsInvalidConnectionID(t *testing.T) {
	base := newFakeBase()
	for _, connectionID := range []string{"", "   ", string(make([]byte, MaxDownstreamConnectionIDLength+1))} {
		if _, err := New(base, Config{ConnectionID: connectionID, ClientMode: ClientModeContinuous}); FaultCode(err) != FaultInvalidConnectionID {
			t.Fatalf("connectionId %q: got %v, want %s", connectionID, err, FaultInvalidConnectionID)
		}
	}
	// The boundary itself is legal: the cap is on length, not length-1.
	exact := ""
	for len(exact) < MaxDownstreamConnectionIDLength {
		exact += "c"
	}
	_, err := New(base, Config{ConnectionID: exact, ClientMode: ClientModeContinuous})
	if err != nil {
		t.Fatalf("a connectionId of exactly the cap was refused: %v", err)
	}
}

// TestHelloDerivesDeliveryProfile is the mode × role matrix. The delivery profile is
// DERIVED from the clientMode, never chosen: a desktop-continuous attachment gets
// continuous, a replayable one gets replayable, regardless of the role it was built
// with. Both roles are exercised because a trusted relay starts handshaked and
// therefore never issues a hello — which is the assertion that distinguishes them.
func TestHelloDerivesDeliveryProfile(t *testing.T) {
	cases := []struct {
		name     string
		mode     ClientMode
		role     Role
		profile  string
		wantHand bool
	}{
		{"desktop terminal", ClientModeContinuous, RoleTerminalClient, "continuous", true},
		{"mobile terminal", ClientModeReplayable, RoleTerminalClient, "replayable", true},
		{"desktop relay", ClientModeContinuous, RoleTrustedHostRelay, "continuous", false},
		{"mobile relay", ClientModeReplayable, RoleTrustedHostRelay, "replayable", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			base := newFakeBase()
			scope, err := New(base, Config{ConnectionID: "conn-" + tc.name, ClientMode: tc.mode, Role: tc.role})
			if err != nil {
				t.Fatalf("new: %v", err)
			}
			t.Cleanup(func() { _ = scope.Dispose(context.Background()) })
			hello, err := scope.Hello()
			if err != nil {
				t.Fatalf("hello: %v", err)
			}
			if hello.DeliveryProfile != tc.profile {
				t.Fatalf("deliveryProfile = %q, want %q", hello.DeliveryProfile, tc.profile)
			}
			if hello.ClientMode != tc.mode {
				t.Fatalf("clientMode = %q, want %q", hello.ClientMode, tc.mode)
			}
			if hello.ConnectionID != scope.cfg.ConnectionID {
				t.Fatalf("connectionId = %q, want the attachment's own", hello.ConnectionID)
			}
			// A trusted relay is already authenticated by the outer transport, so it
			// is never handshakeRequired. A terminal client is, until it initializes.
			if got := scope.HandshakeComplete(); got != (tc.role == RoleTrustedHostRelay) {
				t.Fatalf("handshakeComplete right after construction = %v (role %s)", got, tc.role)
			}
		})
	}
}

// TestHelloAdvertisesWorkflowRunDeltas is the one-directional capability rule: the
// Host advertises the key so a client MAY declare it back, and it does so in every
// client mode (the Host forwards key-level increments on both). The reverse is not
// true — the client capabilities object is strict, so an old Host would fail the
// whole handshake on an unknown key — which is why the declaration is optional on
// the client's side.
func TestHelloAdvertisesWorkflowRunDeltas(t *testing.T) {
	for _, mode := range []ClientMode{ClientModeContinuous, ClientModeReplayable} {
		base := newFakeBase()
		scope, err := New(base, Config{ConnectionID: "cap-" + string(mode), ClientMode: mode, Role: RoleTerminalClient})
		if err != nil {
			t.Fatalf("new: %v", err)
		}
		hello, err := scope.Hello()
		if err != nil {
			t.Fatalf("hello: %v", err)
		}
		if hello.Capabilities.WorkflowRunDeltas == nil || !*hello.Capabilities.WorkflowRunDeltas {
			t.Fatalf("mode %s: the host must advertise workflowRunDeltas: true", mode)
		}
		// nativeDialogs / localTerminal are local desktop side effects, so they
		// track the continuous mode only.
		wantLocal := mode == ClientModeContinuous
		if hello.Capabilities.NativeDialogs != wantLocal || hello.Capabilities.LocalTerminal != wantLocal {
			t.Fatalf("mode %s: nativeDialogs/localTerminal must be %v", mode, wantLocal)
		}
		_ = scope.Dispose(context.Background())
	}
}

// TestTerminalClientMustHandshakeFirst is the handshakeRequired guard: a terminal
// subscription before hello → clientHello is refused, so it can never reach the CLI
// with no bound identity. A trusted relay is exempt — it is already authenticated.
func TestTerminalClientMustHandshakeFirst(t *testing.T) {
	for _, mode := range []ClientMode{ClientModeContinuous, ClientModeReplayable} {
		base := newFakeBase()
		scope := newScope(t, base, "unhandshaked-"+string(mode), mode, RoleTerminalClient)
		if _, err := scope.Subscribe(context.Background(), KindConversation,
			subscribeParams(t, "/ws", "", "s1", nil)); FaultCode(err) != FaultHandshakeRequired {
			t.Fatalf("mode %s: subscribe before hello got %v, want %s", mode, err, FaultHandshakeRequired)
		}
	}
	base := newFakeBase()
	relay := newScope(t, base, "relay-no-handshake", ClientModeContinuous, RoleTrustedHostRelay)
	if _, err := relay.Subscribe(context.Background(), KindConversation,
		subscribeParams(t, "/ws", "", "s1", nil)); err != nil {
		t.Fatalf("a trusted relay must be able to subscribe without a local handshake: %v", err)
	}
}

// TestInitializeRequiresHello is the helloRequired guard: a client that
// initializes without reading the host hello is refused. The reverse order would let
// a client pick capabilities it never saw the Host offer.
func TestInitializeRequiresHello(t *testing.T) {
	base := newFakeBase()
	scope := newScope(t, base, "no-hello", ClientModeContinuous, RoleTerminalClient)
	err := scope.Initialize(clientHelloJSON(t, "client-a", nil))
	if FaultCode(err) != FaultHelloRequired {
		t.Fatalf("initialize without hello got %v, want %s", err, FaultHelloRequired)
	}
}

// TestClientChangedIsRefused: a second initialize that names a different client is
// refused rather than rebinding. Silently rebinding would hand one client's
// subscriptions (and their command idempotency) to another mid-connection.
func TestClientChangedIsRefused(t *testing.T) {
	base := newFakeBase()
	scope := newScope(t, base, "client-bind", ClientModeContinuous, RoleTerminalClient)
	completeHandshake(t, scope, "client-a", nil)
	if err := scope.Initialize(clientHelloJSON(t, "client-a", nil)); err != nil {
		t.Fatalf("re-initializing with the SAME client must be idempotent: %v", err)
	}
	if err := scope.Initialize(clientHelloJSON(t, "client-b", nil)); FaultCode(err) != FaultClientChanged {
		t.Fatalf("a different clientId got %v, want %s", err, FaultClientChanged)
	}
}

// TestWorkflowRunDeltasIsAConnectionFact: the declaration is read from the
// clientHello and stamped on every forwarded call. A subscribe that tries to pick
// its own value must have that field stripped — the UI does not get to choose the
// delta encoding per subscription.
func TestWorkflowRunDeltasIsAConnectionFact(t *testing.T) {
	declared := true
	notDeclared := false
	for _, tc := range []struct {
		name     string
		declared *bool
		want     bool
	}{
		{"declared true", &declared, true},
		{"declared false", &notDeclared, false},
		{"absent", nil, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			base := newFakeBase()
			scope := newScope(t, base, "deltas-"+tc.name, ClientModeContinuous, RoleTerminalClient)
			completeHandshake(t, scope, "client-a", tc.declared)

			// The subscribe explicitly asks for the other value; it must be ignored.
			requested := !tc.want
			_, err := scope.Subscribe(context.Background(), KindConversation,
				subscribeParams(t, "/ws", "", "s1", map[string]any{"workflowRunDeltas": requested}))
			if err != nil {
				t.Fatalf("subscribe: %v", err)
			}
			calls := base.callsTo("subscribe:conversation")
			if len(calls) != 1 {
				t.Fatalf("expected one subscribe, got %d", len(calls))
			}
			if _, present := calls[0].Params["workflowRunDeltas"]; present {
				t.Fatalf("the caller's workflowRunDeltas reached the base service")
			}
			if calls[0].Trusted.WorkflowRunDeltas != tc.want {
				t.Fatalf("forwarded workflowRunDeltas = %v, want %v (the handshake's decision)",
					calls[0].Trusted.WorkflowRunDeltas, tc.want)
			}
		})
	}
}

// TestClientHelloIsStrictlyParsed: the strictness is load-bearing. An unknown
// top-level key, an unknown capability key, a wrong kind and a wrong protocol
// version are all refused, because a handshake that half-trusts a client the Host
// does not understand is worse than no handshake.
func TestClientHelloIsStrictlyParsed(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		want string
	}{
		{"unknown top-level key", `{"kind":"clientHello","protocolVersion":3,"clientId":"c","appVersion":"1","nope":1}`, FaultInvalidClientHello},
		{"unknown capability key", `{"kind":"clientHello","protocolVersion":3,"clientId":"c","appVersion":"1","capabilities":{"madeUp":true}}`, FaultInvalidClientHello},
		{"wrong kind", `{"kind":"hello","protocolVersion":3,"clientId":"c","appVersion":"1"}`, FaultInvalidClientHello},
		{"wrong protocol version", `{"kind":"clientHello","protocolVersion":2,"clientId":"c","appVersion":"1"}`, FaultProtocolMismatch},
		{"not an object", `"clientHello"`, FaultInvalidClientHello},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			base := newFakeBase()
			scope := newScope(t, base, "strict-"+tc.name, ClientModeContinuous, RoleTerminalClient)
			_, err := scope.Hello()
			if err != nil {
				t.Fatalf("hello: %v", err)
			}
			err = scope.Initialize([]byte(tc.raw))
			if FaultCode(err) != tc.want {
				t.Fatalf("got %v, want %s", err, tc.want)
			}
			if scope.HandshakeComplete() {
				t.Fatal("a refused clientHello must not complete the handshake")
			}
		})
	}
}

// TestHelloIsRefusedAfterClose: a disposed attachment answers nothing.
func TestHelloIsRefusedAfterClose(t *testing.T) {
	base := newFakeBase()
	scope, err := New(base, Config{ConnectionID: "post-close", ClientMode: ClientModeContinuous, Role: RoleTerminalClient})
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	if err := scope.Dispose(context.Background()); err != nil {
		t.Fatalf("dispose: %v", err)
	}
	if _, err := scope.Hello(); FaultCode(err) != FaultClosed {
		t.Fatalf("hello after dispose got %v, want %s", err, FaultClosed)
	}
	if err := scope.Initialize(clientHelloJSON(t, "c", nil)); FaultCode(err) != FaultClosed {
		t.Fatalf("initialize after dispose got %v, want %s", err, FaultClosed)
	}
	// A relay that already completed its handshake still gets fault.connection.closed
	// from every entry point, so a torn-down attachment cannot keep issuing commands.
	relay, err := New(base, Config{ConnectionID: "relay-post-close", ClientMode: ClientModeContinuous, Role: RoleTrustedHostRelay})
	if err != nil {
		t.Fatalf("new relay: %v", err)
	}
	_ = relay.Dispose(context.Background())
	if _, err := relay.SendCommand(context.Background(), json.RawMessage(`{"envelope":{"clientId":"c"}}`)); FaultCode(err) != FaultClosed {
		t.Fatalf("command after dispose got %v, want %s", err, FaultClosed)
	}
}
