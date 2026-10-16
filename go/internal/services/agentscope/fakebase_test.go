package agentscope

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"sync"
	"testing"

	"github.com/tylinux/nex/go/internal/agentproc/proto"
	"github.com/tylinux/nex/go/internal/protocol"
)

// fakeBase is the in-memory INexAgentService. It records every call so a test can
// assert on what the base actually saw — which is the only way to prove the facade
// forwarded the TRUSTED identity rather than the caller's.
//
// It deliberately models the two things the facade depends on and the tests must be
// able to break on demand:
//
//   - a per-(connectionId, topic) subscription table, so a re-subscribe on the same
//     ownership genuinely retires the old generation;
//   - a workspace frame stream, so a frame can be pushed at a chosen moment relative
//     to a subscribe ACK (which is how the ACK-race staging is exercised).
type fakeBase struct {
	mu sync.Mutex

	// calls records every method the facade invoked, in order.
	calls []recordedCall

	// subscriptions is the live (connectionId, topic) → subscriptionId table.
	subscriptions map[string]string
	nextSubSerial int

	// streams maps a workspace key to the per-subscriber channels its frame stream
	// fans out to. The real base service delivers a workspace frame to EVERY
	// subscriber of that workspace — which is exactly why the facade has to filter
	// by ownership — so the fake must fan out too, or cross-delivery would never be
	// exercised.
	streams   map[string][]chan []byte
	streamsMu sync.Mutex

	// gates lets a test hold a subscribe ACK open while it pushes frames.
	subscribeGate chan struct{}
	// subscribeGateHits counts how many subscribes waited on the gate.
	subscribeGateHits int

	// flowErr, when set, makes every flow forward fail.
	flowErr error
	// flowCalls records the (route key, state) of every accepted flow edge.
	flowCalls []recordedFlow

	// beginResult is what AttachmentBegin answers.
	beginResult *AttachmentBeginResult
	beginErr    error
	// beginHook runs inside AttachmentBegin, before it returns, so a test can close
	// the scope in the middle of a begin.
	beginHook func()
	// abortCalls records the attachment aborts.
	abortCalls int

	// resyncUnknown: when true, ResyncTopic refuses for an unknown subscription.
	interaction protoClient
}

// protoClient is the fake InteractionRegistry.
type protoClient struct {
	mu       sync.Mutex
	handlers map[string]proto.InteractionHandler
}

func newProtoClient() *protoClient {
	return &protoClient{handlers: map[string]proto.InteractionHandler{}}
}

func (c *protoClient) RegisterInteractionHandler(method string, fn proto.InteractionHandler) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if fn == nil {
		delete(c.handlers, method)
		return
	}
	c.handlers[method] = fn
}

func (c *protoClient) installed() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]string, 0, len(c.handlers))
	for method := range c.handlers {
		out = append(out, method)
	}
	sort.Strings(out)
	return out
}

type recordedCall struct {
	Method   string
	Target   Target
	Trusted  *TrustedConnection
	Route    *UnsubscribeRoute
	Params   map[string]json.RawMessage
	Raw      json.RawMessage
	Envelope CommandEnvelope
}

type recordedFlow struct {
	Workspace    string
	ConnectionID string
	State        FlowState
	Trusted      TrustedConnection
}

func newFakeBase() *fakeBase {
	return &fakeBase{
		subscriptions: map[string]string{},
		streams:       map[string][]chan []byte{},
		interaction:   protoClient{handlers: map[string]proto.InteractionHandler{}},
	}
}

func (b *fakeBase) record(c recordedCall) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.calls = append(b.calls, c)
}

func (b *fakeBase) callMethods() []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := make([]string, 0, len(b.calls))
	for _, c := range b.calls {
		out = append(out, c.Method)
	}
	return out
}

func (b *fakeBase) callsTo(method string) []recordedCall {
	b.mu.Lock()
	defer b.mu.Unlock()
	var out []recordedCall
	for _, c := range b.calls {
		if c.Method == method {
			out = append(out, c)
		}
	}
	return out
}

// decodedTarget re-decodes a forwarded call's params the way the base service would,
// so a test can read the carrier the facade wrote.
func (r recordedCall) decodedTarget() Target {
	return Target{
		WorkspacePath:     r.stringMember("workspacePath"),
		WorkspaceIdentity: r.stringMember("workspaceIdentity"),
		RemoteSessionID:   r.stringMember("remoteSessionId"),
	}
}

func (r recordedCall) stringMember(key string) string {
	raw := r.Params[key]
	if len(raw) == 0 {
		return ""
	}
	var value string
	_ = json.Unmarshal(raw, &value)
	return value
}

func (b *fakeBase) SubscribeTopic(_ context.Context, kind Kind, call Forwarded, args SubscribeArgs) (*SubscribeAck, error) {
	b.record(recordedCall{Method: "subscribe:" + string(kind), Target: call.Target, Trusted: call.Trusted, Params: call.Fields})
	if gate := b.subscribeGate; gate != nil {
		b.mu.Lock()
		b.subscribeGateHits++
		b.mu.Unlock()
		<-gate
	}
	b.mu.Lock()
	b.nextSubSerial++
	subscriptionID := fmt.Sprintf("sub-%d", b.nextSubSerial)
	topic := kind.Topic(args.SessionID, call.Target.WorkspaceKey())
	if call.Trusted != nil {
		b.subscriptions[call.Trusted.ConnectionID+"\x00"+topic] = subscriptionID
	}
	b.mu.Unlock()
	return &SubscribeAck{SubscriptionID: subscriptionID, Mode: "snapshot", LogEpoch: "e1"}, nil
}

func (b *fakeBase) UnsubscribeTopic(_ context.Context, kind Kind, call Forwarded, args UnsubscribeArgs, route UnsubscribeRoute) error {
	b.record(recordedCall{Method: "unsubscribe:" + string(kind), Target: call.Target, Trusted: call.Trusted, Route: &route, Params: call.Fields})
	b.mu.Lock()
	defer b.mu.Unlock()
	delete(b.subscriptions, route.ConnectionID+"\x00"+route.Topic)
	return nil
}

func (b *fakeBase) ResyncTopic(_ context.Context, kind Kind, call Forwarded, _ ResyncArgs) (json.RawMessage, error) {
	b.record(recordedCall{Method: "resync:" + string(kind), Target: call.Target, Trusted: call.Trusted, Route: call.Route, Params: call.Fields})
	return json.RawMessage(`{"mode":"resume"}`), nil
}

func (b *fakeBase) TopicFrames(_ context.Context, target Target) (<-chan []byte, func(), error) {
	key := target.WorkspaceKey()
	stream := make(chan []byte, 64)
	b.streamsMu.Lock()
	b.streams[key] = append(b.streams[key], stream)
	b.streamsMu.Unlock()
	var once sync.Once
	return stream, func() {
		once.Do(func() {
			b.streamsMu.Lock()
			list := b.streams[key]
			for i, existing := range list {
				if existing == stream {
					b.streams[key] = append(list[:i], list[i+1:]...)
					break
				}
			}
			b.streamsMu.Unlock()
		})
	}, nil
}

func (b *fakeBase) SetConnectionFlowState(_ context.Context, call Forwarded, state FlowState) error {
	workspace := call.Target.WorkspaceKey()
	connectionID := ""
	trusted := TrustedConnection{}
	if call.Trusted != nil {
		connectionID = call.Trusted.ConnectionID
		trusted = *call.Trusted
	}
	b.mu.Lock()
	if b.flowErr != nil {
		err := b.flowErr
		b.mu.Unlock()
		return err
	}
	b.flowCalls = append(b.flowCalls, recordedFlow{Workspace: workspace, ConnectionID: connectionID, State: state, Trusted: trusted})
	b.mu.Unlock()
	return nil
}

func (b *fakeBase) SendCommand(_ context.Context, call Forwarded, envelope CommandEnvelope) (json.RawMessage, error) {
	b.record(recordedCall{Method: "sendCommand", Target: call.Target, Trusted: call.Trusted, Params: call.Fields, Envelope: envelope})
	return json.RawMessage(`{"accepted":true}`), nil
}

func (b *fakeBase) QueryCommands(_ context.Context, call Forwarded, params json.RawMessage) (json.RawMessage, error) {
	b.record(recordedCall{Method: "queryCommands", Target: call.Target, Trusted: call.Trusted, Params: call.Fields, Raw: params})
	return json.RawMessage(`{"keys":[]}`), nil
}

func (b *fakeBase) AttachmentBegin(_ context.Context, call Forwarded, params json.RawMessage) (*AttachmentBeginResult, error) {
	b.record(recordedCall{Method: "attachmentBegin", Target: call.Target, Trusted: call.Trusted, Params: call.Fields, Raw: params})
	if b.beginHook != nil {
		b.beginHook()
	}
	if b.beginErr != nil {
		return nil, b.beginErr
	}
	if b.beginResult != nil {
		return b.beginResult, nil
	}
	return &AttachmentBeginResult{State: "staged"}, nil
}

func (b *fakeBase) AttachmentAbort(_ context.Context, call Forwarded, params json.RawMessage) error {
	b.mu.Lock()
	b.abortCalls++
	b.mu.Unlock()
	b.record(recordedCall{Method: "attachmentAbort", Target: call.Target, Trusted: call.Trusted, Params: call.Fields, Raw: params})
	return nil
}

func (b *fakeBase) PassThrough(_ context.Context, method string, call Forwarded, params json.RawMessage) (json.RawMessage, error) {
	b.record(recordedCall{Method: "pass:" + method, Target: call.Target, Trusted: call.Trusted, Params: call.Fields, Raw: params})
	return json.RawMessage(`{"ok":true}`), nil
}

// push publishes one frame to EVERY subscriber of a workspace stream, which is the
// fan-out the base service performs before ownership filtering.
func (b *fakeBase) push(workspace string, frame string) {
	b.streamsMu.Lock()
	subscribers := append([]chan []byte(nil), b.streams[workspace]...)
	b.streamsMu.Unlock()
	for _, stream := range subscribers {
		stream <- []byte(frame)
	}
}

func (b *fakeBase) flowStates() []recordedFlow {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]recordedFlow(nil), b.flowCalls...)
}

// frameJSON builds a physical wire frame with the routing keys the facade reads.
func frameJSON(topic, subscriptionID string, extra string) string {
	if extra != "" {
		extra = "," + extra
	}
	return fmt.Sprintf(`{"wireVersion":3,"kind":"complete","logicalFrameId":"lf-%s","logicalFrameOrdinal":1,"topic":%q,"subscriptionId":%q,"frame":{"payload":{"kind":"snapshot"}}%s}`,
		subscriptionID, topic, subscriptionID, extra)
}

// subscribeParams builds a subscribe argument for one workspace.
func subscribeParams(t *testing.T, path, identity, sessionID string, extra map[string]any) json.RawMessage {
	t.Helper()
	object := map[string]any{"workspacePath": path}
	if identity != "" {
		object["workspaceIdentity"] = identity
	}
	if sessionID != "" {
		object["sessionId"] = sessionID
	}
	for key, value := range extra {
		object[key] = value
	}
	raw, err := json.Marshal(object)
	if err != nil {
		t.Fatalf("subscribe params: %v", err)
	}
	return raw
}

func clientHelloJSON(t *testing.T, clientID string, deltas *bool) json.RawMessage {
	t.Helper()
	capabilities := map[string]any{}
	if deltas != nil {
		capabilities["workflowRunDeltas"] = *deltas
	}
	object := map[string]any{
		"kind":            "clientHello",
		"protocolVersion": HostProtocolVersion,
		"clientId":        clientID,
		"appVersion":      "1.0.0",
	}
	if len(capabilities) > 0 {
		object["capabilities"] = capabilities
	}
	raw, err := json.Marshal(object)
	if err != nil {
		t.Fatalf("client hello: %v", err)
	}
	return raw
}

// completeHandshake drives hello → initialize on a scope.
func completeHandshake(t *testing.T, scope *Scope, clientID string, deltas *bool) {
	t.Helper()
	_, err := scope.Hello()
	if err != nil {
		t.Fatalf("hello: %v", err)
	}
	if err := scope.Initialize(clientHelloJSON(t, clientID, deltas)); err != nil {
		t.Fatalf("initialize: %v", err)
	}
}

// newScope builds a scope over the fake, for one mode and role.
func newScope(t *testing.T, base *fakeBase, connectionID string, mode ClientMode, role Role) *Scope {
	t.Helper()
	scope, err := New(base, Config{ConnectionID: connectionID, ClientMode: mode, Role: role})
	if err != nil {
		t.Fatalf("new scope: %v", err)
	}
	t.Cleanup(func() { _ = scope.Dispose(context.Background()) })
	return scope
}

var _ AgentBase = (*fakeBase)(nil)
var _ InteractionRegistry = (*protoClient)(nil)
var _ = protocol.KindRequest

// rawParams wraps a JSON literal for a call written in an `if` header. The Go
// grammar does not allow a composite literal (`[]byte(…)`) in an if/for/switch
// header without extra parentheses, and `[]byte(…)` is parsed as one there, so the
// conversion lives behind a function and every such call site reads normally.
func rawParams(literal string) []byte { return []byte(literal) }
