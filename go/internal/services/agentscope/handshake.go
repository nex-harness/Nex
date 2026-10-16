package agentscope

import (
	"encoding/json"
	"sync"
	"time"
)

// handshake.go ports the hello → clientHello exchange and the connection facts it
// writes. Those facts are the reason the whole facade exists: clientMode, role and
// workflowRunDeltas are decided ONCE per connection and then stamped on every
// forwarded call.
//
// Two hard rules from the TS comments:
//
//   - hello must precede initialize. A client that subscribes without either is
//     refused with fault.connection.handshakeRequired rather than given a CLI
//     subscription with no bound identity.
//   - clientHello may NOT change the bound client. A second initialize with a
//     different clientId is fault.connection.clientChanged: silently rebinding
//     would hand one client's subscriptions to another.
//   - workflowRunDeltas is read ONCE, from the clientHello, and only `=== true`
//     counts. The Host advertises workflowRunDeltas: true in its hello so a client
//     that understands the key-level increment may declare it back; the reverse
//     direction is not true, because that capabilities object is .strict() and an
//     old Host would fail the whole handshake on an unknown key.

// HostProtocolVersion is V4_WIRE_PROTOCOL_VERSION (core.ts).
const HostProtocolVersion = 3

// HostCapabilities is the hello capability set. The optional flags are pointers
// because "absent" and "false" are the same thing on the wire (a .strict() schema
// rejects an unknown key, so an old consumer must not be handed a key it does not
// know), and the read side tests `=== true`.
type HostCapabilities struct {
	NativeDialogs        bool   `json:"nativeDialogs"`
	LocalTerminal        bool   `json:"localTerminal"`
	BinaryFrames         bool   `json:"binaryFrames"`
	Compression          string `json:"compression"`
	WorkspaceHookReview  *bool  `json:"workspaceHookReview,omitempty"`
	IndependentPlanState *bool  `json:"independentPlanState,omitempty"`
	WorkflowRunDeltas    *bool  `json:"workflowRunDeltas,omitempty"`
}

// HelloMessage is the host's greeting. Its member ORDER is the wire contract only
// as JSON, but the shape is fixed by helloMessageSchema.
type HelloMessage struct {
	Kind            string            `json:"kind"`
	ProtocolVersion int               `json:"protocolVersion"`
	ConnectionID    string            `json:"connectionId"`
	ClientMode      ClientMode        `json:"clientMode"`
	DeliveryProfile string            `json:"deliveryProfile"`
	ServerTime      int64             `json:"serverTime"`
	Capabilities    HostCapabilities  `json:"capabilities"`
	Auth            map[string]string `json:"auth"`
}

// ClientHello is the client's answer. Capabilities is strict in the TS, so the Go
// struct keeps only the two declared keys: an unknown key in the incoming JSON must
// fail the parse, not be silently ignored.
type ClientHello struct {
	Kind            string      `json:"kind"`
	ProtocolVersion int         `json:"protocolVersion"`
	ClientID        string      `json:"clientId"`
	ClientKind      string      `json:"clientKind,omitempty"`
	AppVersion      string      `json:"appVersion"`
	Capabilities    *ClientCaps `json:"capabilities,omitempty"`
}

// ClientCaps is the strict client capability object.
type ClientCaps struct {
	WorkspaceHookReviewUI *bool `json:"workspaceHookReviewUi,omitempty"`
	WorkflowRunDeltas     *bool `json:"workflowRunDeltas,omitempty"`
}

// DeliveryProfileFor ports deliveryProfileFor: the profile is DERIVED from the
// clientMode and is never chosen independently.
func DeliveryProfileFor(mode ClientMode) string {
	if mode == ClientModeContinuous {
		return "continuous"
	}
	return "replayable"
}

func boolPtr(v bool) *bool { return &v }

// CreateHello ports createHello. The Host advertises workflowRunDeltas: true
// unconditionally, because that is what lets a client declare it back in its
// clientHello; a Host that omitted it would lock every client into the legacy
// whole-key encoding.
func CreateHello(connectionID string, mode ClientMode, now time.Time) HelloMessage {
	continuous := mode == ClientModeContinuous
	return HelloMessage{
		Kind:            "hello",
		ProtocolVersion: HostProtocolVersion,
		ConnectionID:    connectionID,
		ClientMode:      mode,
		DeliveryProfile: DeliveryProfileFor(mode),
		ServerTime:      now.UnixMilli(),
		Capabilities: HostCapabilities{
			NativeDialogs:        continuous,
			LocalTerminal:        continuous,
			BinaryFrames:         false,
			Compression:          "none",
			WorkspaceHookReview:  boolPtr(true),
			IndependentPlanState: boolPtr(true),
			WorkflowRunDeltas:    boolPtr(true),
		},
		Auth: map[string]string{},
	}
}

// handshakeState is the per-connection handshake bookkeeping.
type handshakeState struct {
	mu sync.Mutex

	role              Role
	mode              ClientMode
	handshakeComplete bool
	helloIssued       bool
	boundClientID     string
	// clientWorkflowRunDeltas is the clientHello declaration. A trusted relay has
	// no clientHello of its own; it carries the DOWNSTREAM's declaration instead.
	clientWorkflowRunDeltas bool
}

func newHandshakeState(role Role, mode ClientMode) *handshakeState {
	// A trusted relay has already completed identity selection at the outer
	// transport (stdio / Node-only WS role header), so it starts handshaked: the
	// terminal UI is the one that must go through hello → clientHello.
	complete := role == RoleTrustedHostRelay
	return &handshakeState{
		role:              role,
		mode:              mode,
		handshakeComplete: complete,
		helloIssued:       complete,
	}
}

func (h *handshakeState) assertOpen(disposed bool) error {
	if disposed {
		return &Fault{Code: FaultClosed}
	}
	return nil
}

func (h *handshakeState) assertReady(disposed bool) error {
	if err := h.assertOpen(disposed); err != nil {
		return err
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if !h.handshakeComplete {
		return &Fault{Code: FaultHandshakeRequired}
	}
	return nil
}

// issueHello marks the hello as issued and returns it. It is refused once closed.
// The connectionId and clientMode come from the attachment's own trusted context,
// never from the caller: a UI cannot ask for a different identity here.
func (h *handshakeState) issueHello(disposed bool, connectionID string, now time.Time) (HelloMessage, error) {
	if err := h.assertOpen(disposed); err != nil {
		return HelloMessage{}, err
	}
	h.mu.Lock()
	h.helloIssued = true
	mode := h.mode
	h.mu.Unlock()
	return CreateHello(connectionID, mode, now), nil
}

// acceptClientHello ports initializeConversationV4. It parses strictly, refuses a
// client that changed, and records the workflowRunDeltas declaration.
func (h *handshakeState) acceptClientHello(disposed bool, raw json.RawMessage) error {
	if err := h.assertOpen(disposed); err != nil {
		return err
	}
	h.mu.Lock()
	helloIssued := h.helloIssued
	h.mu.Unlock()
	if !helloIssued {
		return &Fault{Code: FaultHelloRequired}
	}
	parsed, err := ParseClientHello(raw)
	if err != nil {
		return err
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.boundClientID != "" && h.boundClientID != parsed.ClientID {
		return &Fault{Code: FaultClientChanged}
	}
	h.boundClientID = parsed.ClientID
	h.clientWorkflowRunDeltas = parsed.SupportsWorkflowRunDeltas()
	h.handshakeComplete = true
	return nil
}

// boundClient returns the clientId the handshake committed to.
func (h *handshakeState) boundClient() string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.boundClientID
}

// workflowRunDeltas reports the connection's declaration, read under the lock.
func (h *handshakeState) workflowRunDeltas() bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.clientWorkflowRunDeltas
}

// setWorkflowRunDeltas lets the trusted relay carry a downstream's declaration.
func (h *handshakeState) setWorkflowRunDeltas(value bool) {
	h.mu.Lock()
	h.clientWorkflowRunDeltas = value
	h.mu.Unlock()
}

// ParseClientHello is the strict parse of clientHelloSchema. It rejects an unknown
// top-level key and an unknown key inside capabilities, because that strictness is
// load-bearing: it is what stops an old Host from completing a handshake with a
// client that declares a capability it has never heard of.
func ParseClientHello(raw json.RawMessage) (ClientHello, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil || fields == nil {
		// A clientHello that is not a JSON object at all is refused with the same
		// code as one carrying an unknown key: from the schema's point of view both
		// fail to parse, and the UI needs one branch, not two.
		return ClientHello{}, &Fault{Code: FaultInvalidClientHello}
	}
	allowed := map[string]bool{
		"kind": true, "protocolVersion": true, "clientId": true,
		"clientKind": true, "appVersion": true, "capabilities": true,
	}
	for key := range fields {
		if !allowed[key] {
			return ClientHello{}, &Fault{Code: FaultInvalidClientHello}
		}
	}
	var hello ClientHello
	if err := json.Unmarshal(raw, &hello); err != nil {
		return ClientHello{}, err
	}
	if hello.Kind != "clientHello" {
		return ClientHello{}, &Fault{Code: FaultInvalidClientHello}
	}
	if hello.ProtocolVersion != HostProtocolVersion {
		return ClientHello{}, &Fault{Code: FaultProtocolMismatch}
	}
	if extra := hello.Capabilities.extraKeys(fields["capabilities"]); len(extra) > 0 {
		return ClientHello{}, &Fault{Code: FaultInvalidClientHello}
	}
	return hello, nil
}

// SupportsWorkflowRunDeltas ports clientSupportsWorkflowRunDeltas: only an explicit
// true counts.
func (c ClientHello) SupportsWorkflowRunDeltas() bool {
	return c.Capabilities != nil && c.Capabilities.WorkflowRunDeltas != nil && *c.Capabilities.WorkflowRunDeltas
}

// The two extra strict-parse faults. A clientHello the facade cannot parse must not
// complete the handshake: the whole point of the strict schema is that a client
// declaring something the Host does not understand is refused rather than
// half-trusted.
const (
	FaultInvalidClientHello = "fault.connection.invalidClientHello"
	FaultProtocolMismatch   = "fault.connection.protocolMismatch"
)

func (c *ClientCaps) extraKeys(raw json.RawMessage) []string {
	if c == nil || len(raw) == 0 {
		return nil
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return []string{"<invalid>"}
	}
	allowed := map[string]bool{"workspaceHookReviewUi": true, "workflowRunDeltas": true}
	var extra []string
	for key := range fields {
		if !allowed[key] {
			extra = append(extra, key)
		}
	}
	return extra
}
