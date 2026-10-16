package agentscope

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/tylinux/nex/go/internal/agentproc/proto"
	"github.com/tylinux/nex/go/internal/protocol"
)

// interaction_test.go covers the step 3.10.2 seam as the facade consumes it: an
// agent's interaction/* request must be answered by the connection that can show it,
// and a request that belongs to no live connection must fall back to the
// method-not-found default the protocol client already has.

func agentRequest(method, connectionID string) proto.AgentRequest {
	params := map[string]any{"prompt": "allow?"}
	if connectionID != "" {
		params["connectionId"] = connectionID
	}
	raw, _ := json.Marshal(params)
	return proto.AgentRequest{
		ID:     protocol.IntID(7),
		Method: method,
		Params: raw,
	}
}

// TestInteractionMethodsAreAllRegistered: the facade owns every interaction/*
// method the protocol client routes, on both client modes and both roles. A missing
// method falls back to method-not-found, which silently degrades a permission prompt
// into an auto-deny.
func TestInteractionMethodsAreAllRegistered(t *testing.T) {
	for _, mode := range []ClientMode{ClientModeContinuous, ClientModeReplayable} {
		for _, role := range []Role{RoleTerminalClient, RoleTrustedHostRelay} {
			base := newFakeBase()
			client := newProtoClient()
			directory := NewInteractionDirectory()
			scope, err := New(base, Config{
				ConnectionID: "inter-" + string(mode) + "-" + string(role),
				ClientMode:   mode, Role: role, Interaction: client,
			})
			if err != nil {
				t.Fatalf("new: %v", err)
			}
			scope.SetInteractionDirectory(directory, func(_ context.Context, _ proto.AgentRequest) (json.RawMessage, error) {
				return json.RawMessage(`{"ok":true}`), nil
			})
			installed := map[string]bool{}
			for _, method := range client.installed() {
				installed[method] = true
			}
			if len(installed) != len(proto.InteractionMethods) {
				t.Fatalf("mode %s role %s: %d of %d interaction methods are installed",
					mode, role, len(installed), len(proto.InteractionMethods))
			}
			for _, method := range proto.InteractionMethods {
				if !installed[method] {
					t.Fatalf("mode %s role %s: %s has no handler; it would fall back to "+
						"method-not-found and silently degrade the prompt", mode, role, method)
				}
			}
			_ = scope.Dispose(context.Background())
			if got := len(client.installed()); got != 0 {
				t.Fatalf("%d handlers outlived the disposed scope", got)
			}
			if got := directory.Live(); got != 0 {
				t.Fatalf("%d resolvers outlived the disposed scope", got)
			}
		}
	}
}

// TestInteractionGoesToTheOwningConnection: a request that names a connection is
// answered by that connection's resolver. Two desktop windows can both own a
// subscription on the SAME topic, so "which connection asked" is not derivable from
// the topic — it comes from the request.
func TestInteractionGoesToTheOwningConnection(t *testing.T) {
	directory := NewInteractionDirectory()
	answeredBy := ""
	directory.Register("win-a", func(context.Context, proto.AgentRequest) (json.RawMessage, error) {
		answeredBy = "win-a"
		return json.RawMessage(`{"by":"a"}`), nil
	})
	directory.Register("win-b", func(context.Context, proto.AgentRequest) (json.RawMessage, error) {
		answeredBy = "win-b"
		return json.RawMessage(`{"by":"b"}`), nil
	})
	handler := directory.Handler(proto.InteractionRequestPermission)
	result, err := handler(context.Background(), agentRequest(proto.InteractionRequestPermission, "win-b"))
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	if answeredBy != "win-b" {
		t.Fatalf("answered by %q, want win-b", answeredBy)
	}
	if string(result) != `{"by":"b"}` {
		t.Fatalf("result = %s", result)
	}
}

// TestInteractionWithNoLiveConnectionIsDeclined: with no resolver for the named
// connection and no fallback, the handler returns (nil, nil). The protocol client
// turns that into method-not-found, which the agent already degrades on — inventing
// a synthetic answer would be worse than the documented degradation.
func TestInteractionWithNoLiveConnectionIsDeclined(t *testing.T) {
	directory := NewInteractionDirectory()
	directory.Register("win-a", func(context.Context, proto.AgentRequest) (json.RawMessage, error) {
		return json.RawMessage(`{"by":"a"}`), nil
	})
	handler := directory.Handler(proto.InteractionRequestUserInput)
	result, err := handler(context.Background(), agentRequest(proto.InteractionRequestUserInput, "gone"))
	if result != nil || err != nil {
		t.Fatalf("a request for a dead connection got (%s, %v); it must be declined", result, err)
	}
}

// TestInteractionAfterDisposeIsDeclined: a window that is gone must stop being the
// answer for the next prompt. Leaving its resolver installed would keep the whole
// scope reachable from the protocol client.
func TestInteractionAfterDisposeIsDeclined(t *testing.T) {
	base := newFakeBase()
	client := newProtoClient()
	directory := NewInteractionDirectory()
	scope, err := New(base, Config{ConnectionID: "gone", ClientMode: ClientModeContinuous, Role: RoleTerminalClient, Interaction: client})
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	scope.SetInteractionDirectory(directory, func(context.Context, proto.AgentRequest) (json.RawMessage, error) {
		return json.RawMessage(`{"ok":true}`), nil
	})
	if directory.Live() != 1 {
		t.Fatalf("live resolvers = %d, want 1", directory.Live())
	}
	if err := scope.Dispose(context.Background()); err != nil {
		t.Fatalf("dispose: %v", err)
	}
	if directory.Live() != 0 {
		t.Fatalf("live resolvers = %d after dispose", directory.Live())
	}
	handler := directory.Handler(proto.InteractionRequestPermission)
	result, err := handler(context.Background(), agentRequest(proto.InteractionRequestPermission, ""))
	if result != nil || err != nil {
		t.Fatalf("a request after dispose got (%s, %v)", result, err)
	}
}

// TestSoleLiveScopeAnswersAnUnnamedRequest: a request that names no connection
// belongs to the sole live scope. That is the single-window case every desktop host
// starts in, and declining it would break every prompt before a second window opens.
func TestSoleLiveScopeAnswersAnUnnamedRequest(t *testing.T) {
	directory := NewInteractionDirectory()
	answered := 0
	directory.Register("only", func(context.Context, proto.AgentRequest) (json.RawMessage, error) {
		answered++
		return json.RawMessage(`{"ok":true}`), nil
	})
	handler := directory.Handler(proto.InteractionRequestPermission)
	_, err := handler(context.Background(), agentRequest(proto.InteractionRequestPermission, ""))
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	if answered != 1 {
		t.Fatalf("the sole live scope answered %d time(s), want 1", answered)
	}
}

// TestAmbiguousUnnamedRequestIsDeclined: with TWO live scopes and no named
// connection there is no honest answer. Guessing would show one window's permission
// prompt for another window's turn.
func TestAmbiguousUnnamedRequestIsDeclined(t *testing.T) {
	directory := NewInteractionDirectory()
	for _, id := range []string{"win-a", "win-b"} {
		id := id
		directory.Register(id, func(context.Context, proto.AgentRequest) (json.RawMessage, error) {
			t.Fatalf("an ambiguous request was answered by %s", id)
			return nil, nil
		})
	}
	handler := directory.Handler(proto.InteractionRequestPermission)
	result, err := handler(context.Background(), agentRequest(proto.InteractionRequestPermission, ""))
	if result != nil || err != nil {
		t.Fatalf("an ambiguous request got (%s, %v); it must be declined", result, err)
	}
}

// TestFallbackAnswersUnattributedRequests: a host that owns a headless or windowless
// leg installs a fallback, so a request with no connection still gets a real answer
// instead of method-not-found.
func TestFallbackAnswersUnattributedRequests(t *testing.T) {
	directory := NewInteractionDirectory()
	directory.SetFallback(func(context.Context, proto.AgentRequest) (json.RawMessage, error) {
		return json.RawMessage(`{"fallback":true}`), nil
	})
	handler := directory.Handler(proto.InteractionBrowserExecute)
	result, err := handler(context.Background(), agentRequest(proto.InteractionBrowserExecute, "nobody"))
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	if string(result) != `{"fallback":true}` {
		t.Fatalf("result = %s", result)
	}
	// A named live connection still wins over the fallback.
	directory.Register("win-a", func(context.Context, proto.AgentRequest) (json.RawMessage, error) {
		return json.RawMessage(`{"by":"a"}`), nil
	})
	result, err = handler(context.Background(), agentRequest(proto.InteractionBrowserExecute, "win-a"))
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	if string(result) != `{"by":"a"}` {
		t.Fatalf("the live scope did not win over the fallback: %s", result)
	}
}

// TestTwoScopesDoNotStealEachOthersPrompts is the end-to-end version: two windows,
// two runtimes, one shared protocol client. A permission request for window A's turn
// must be answered by window A even while window B is alive and subscribed.
func TestTwoScopesDoNotStealEachOthersPrompts(t *testing.T) {
	base := newFakeBase()
	client := newProtoClient()
	directory := NewInteractionDirectory()
	ctx := context.Background()

	newWindow := func(connectionID string, mode ClientMode) *Scope {
		scope, err := New(base, Config{ConnectionID: connectionID, ClientMode: mode, Role: RoleTerminalClient, Interaction: client})
		if err != nil {
			t.Fatalf("new %s: %v", connectionID, err)
		}
		scope.SetInteractionDirectory(directory, func(_ context.Context, req proto.AgentRequest) (json.RawMessage, error) {
			result, _ := json.Marshal(map[string]any{"answeredBy": connectionID, "method": req.Method})
			return result, nil
		})
		completeHandshake(t, scope, connectionID+"-client", nil)
		_, err = scope.Subscribe(ctx, KindConversation, subscribeParams(t, "/ws", "", "s1", nil))
		if err != nil {
			t.Fatalf("%s subscribe: %v", connectionID, err)
		}
		return scope
	}
	windowA := newWindow("win-a", ClientModeContinuous)
	windowB := newWindow("win-b", ClientModeContinuous)
	t.Cleanup(func() {
		_ = windowA.Dispose(ctx)
		_ = windowB.Dispose(ctx)
	})

	handler := client.handlers[proto.InteractionRequestPermission]
	if handler == nil {
		t.Fatal("no permission handler was installed on the protocol client")
	}
	for _, expected := range []string{"win-a", "win-b"} {
		result, err := handler(ctx, agentRequest(proto.InteractionRequestPermission, expected))
		if err != nil {
			t.Fatalf("handler for %s: %v", expected, err)
		}
		var decoded struct {
			AnsweredBy string `json:"answeredBy"`
		}
		if err := json.Unmarshal(result, &decoded); err != nil {
			t.Fatalf("decode: %v", err)
		}
		if decoded.AnsweredBy != expected {
			t.Fatalf("a request for %s was answered by %s", expected, decoded.AnsweredBy)
		}
	}
}
