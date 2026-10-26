package local

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/nex-harness/nex/server/internal/rpcwire"
)

// v4WireProtocolVersion mirrors V4_WIRE_PROTOCOL_VERSION in
// packages/shared/src/nex-protocol-v4/core.ts.
const v4WireProtocolVersion = 3

// connectionSequence numbers v4 connection ids per process.
var connectionSequence atomic.Uint64

func newConnectionID() string {
	if connectionSequence.Add(1) == 0 {
		connectionSequence.Store(1)
	}
	var raw [8]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return fmt.Sprintf("nex-next-%d", time.Now().UnixNano())
	}
	return "nex-next-" + hex.EncodeToString(raw[:])
}

// NexAgentLocal answers the `nex-agent` methods that have no workspace in
// their argument. Everything else is forwarded to the owning runner.
//
// The hello/clientHello shapes come from
// packages/shared/src/nex-protocol-v4/transport.ts:41-97 and
// packages/services/src/nex-agent/nexAgentService.ts:4154-4192 with MVP
// capabilities false (the server has no desktop-only native features).
type NexAgentLocal struct {
	mu           sync.Mutex
	connectionID string
	issued       bool
	boundClient  string
}

// NewNexAgentLocal creates the local nex-agent fallback table.
func NewNexAgentLocal() *NexAgentLocal {
	return &NexAgentLocal{connectionID: newConnectionID()}
}

// HandlesCall reports whether method is answered locally.
func (n *NexAgentLocal) HandlesCall(method string) bool {
	switch method {
	case "helloConversationV4",
		"initializeConversationV4",
		"syncAppRuntimePreferences",
		"listAllAutomations",
		"hasActiveCuaOperationTurn":
		return true
	}
	return false
}

// Call implements the local table.
func (n *NexAgentLocal) Call(_ context.Context, _ *Env, method string, arg rpcwire.Value) (rpcwire.Value, error) {
	switch method {
	case "helloConversationV4":
		n.mu.Lock()
		n.issued = true
		connectionID := n.connectionID
		n.mu.Unlock()
		hello := map[string]any{
			"kind":            "hello",
			"protocolVersion": v4WireProtocolVersion,
			"connectionId":    connectionID,
			"clientMode":      "desktop-continuous",
			"deliveryProfile": "continuous",
			"serverTime":      time.Now().UnixMilli(),
			"capabilities": map[string]any{
				"nativeDialogs":        false,
				"localTerminal":        false,
				"binaryFrames":         false,
				"compression":          "none",
				"workspaceHookReview":  true,
				"independentPlanState": true,
				"workflowRunDeltas":    false,
			},
			"auth": map[string]any{},
		}
		encoded, err := json.Marshal(hello)
		return json.RawMessage(encoded), err

	case "initializeConversationV4":
		object, err := decodeObject(arg)
		if err != nil {
			return nil, err
		}
		var clientID string
		if raw, ok := object["clientId"]; ok {
			_ = json.Unmarshal(raw, &clientID)
		}
		n.mu.Lock()
		if !n.issued {
			n.mu.Unlock()
			return nil, errors.New("fault.connection.helloRequired")
		}
		if n.boundClient != "" && clientID != "" && n.boundClient != clientID {
			n.mu.Unlock()
			return nil, errors.New("fault.connection.clientChanged")
		}
		if clientID != "" {
			n.boundClient = clientID
		}
		n.mu.Unlock()
		return rpcwire.Undefined{}, nil

	case "syncAppRuntimePreferences":
		return rpcwire.Undefined{}, nil

	case "listAllAutomations":
		return json.RawMessage("[]"), nil

	case "hasActiveCuaOperationTurn":
		return false, nil
	}
	return nil, &MethodNotFoundError{Method: method}
}

// Listen implements the local table: registered events never fire.
func (n *NexAgentLocal) Listen(context.Context, *Env, string, rpcwire.Value, func(rpcwire.Value)) (func(), error) {
	return nil, nil
}
