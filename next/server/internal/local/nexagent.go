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

// AutomationsDelegate is the subset of automations.Service the nex-agent
// table delegates to. *automations.Service satisfies it.
//
// The automations methods carry no workspace in their argument, so the router
// marks them TargetLocalUnsupported (internal/router/router.go) and the
// browser dispatcher only reaches them through this table
// (internal/browserws/browserws.go).
type AutomationsDelegate interface {
	HandlesCall(method string) bool
	Call(ctx context.Context, method string, arg rpcwire.Value) (rpcwire.Value, error)
}

// NexAgentOption configures a NexAgentLocal.
type NexAgentOption func(*NexAgentLocal)

// WithAutomations routes the ten automation methods to svc instead of the
// empty-list placeholder. Nil leaves the placeholder in place (tests that
// build the table alone).
func WithAutomations(svc AutomationsDelegate) NexAgentOption {
	return func(n *NexAgentLocal) { n.automations = svc }
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
	// automations owns the server-side definitions and serves the automation
	// methods (docs/next/automations-views-search.md §A). Nil = placeholder.
	automations AutomationsDelegate
}

// NewNexAgentLocal creates the local nex-agent fallback table.
func NewNexAgentLocal(options ...NexAgentOption) *NexAgentLocal {
	table := &NexAgentLocal{connectionID: newConnectionID()}
	for _, option := range options {
		if option != nil {
			option(table)
		}
	}
	return table
}

// HandlesCall reports whether method is answered locally.
func (n *NexAgentLocal) HandlesCall(method string) bool {
	// 修复：automations 服务注册后，它的 10 个方法（除 listAllAutomations 外）不在固定表里，
	// 必须优先委托，否则除历史占位方法外的自动化调用都会落到 "Method not found"。
	if n.automations != nil && n.automations.HandlesCall(method) {
		return true
	}
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
func (n *NexAgentLocal) Call(ctx context.Context, _ *Env, method string, arg rpcwire.Value) (rpcwire.Value, error) {
	// 先委托 automations：listAllAutomations 在下面固定表里也有一个返回 [] 的占位，
	// 委托必须排在固定表前面，否则页面仍读到空列表。
	if n.automations != nil && n.automations.HandlesCall(method) {
		return n.automations.Call(ctx, method, arg)
	}
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
		// 占位只在未注入 automations.Service 时生效（单独构造表做协议测试）。
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
