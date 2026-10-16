package agentscope

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/tylinux/nex/go/internal/agentproc/proto"
)

// types.go is the facade's vocabulary: the subscription kinds, the trusted
// carrier shapes, the flow states, the argument structs the base service sees and
// the AgentBase seam itself. They are data rather than the ownership rules that
// consume them (ownership.go) or the handshake (handshake.go).

// Kind is one of the three homogeneous V4 topic families. They differ only in the
// topic they key on and the base method they land on, so the facade owns ONE
// ownership implementation for all three rather than three copies.
type Kind string

const (
	KindConversation    Kind = "conversation"
	KindSessionsIndex   Kind = "sessions-index"
	KindWorkspaceConfig Kind = "workspace-config"
)

// Topic is the wire topic key for a kind. conversation keys on the session id; the
// two workspace-scoped families key on the workspace key (identity || path).
func (k Kind) Topic(sessionID string, workspaceKey string) string {
	if k == KindConversation {
		return "conversation/" + sessionID
	}
	return string(k) + "/" + workspaceKey
}

// ClientMode is the delivery mode the connection registered at handshake.
type ClientMode string

const (
	// ClientModeContinuous is the desktop window: live deltas, no replay.
	ClientModeContinuous ClientMode = "desktop-continuous"
	// ClientModeReplayable is the phone / Web remote: every open resumes from a
	// watermark instead of relying on a continuous stream.
	ClientModeReplayable ClientMode = "web-remote-replayable"
)

// Role decides what a connection is allowed to do before it has handshaked.
type Role string

const (
	// RoleTerminalClient is a UI attachment. It must complete hello → clientHello.
	RoleTerminalClient Role = "terminal-client"
	// RoleTrustedHostRelay is already authenticated by the outer transport (stdio or
	// the Node-only WS role header), so it starts handshaked and only forwards a
	// downstream connection's identity.
	RoleTrustedHostRelay Role = "trusted-host-relay"
)

// FlowState is V4ConnectionFlowState: the transport sideband's edge.
type FlowState string

const (
	FlowSaturated FlowState = "saturated"
	FlowDrained   FlowState = "drained"
	FlowClosed    FlowState = "closed"
)

// Fault is a refusal the facade raises. Its message IS the code: the TS throws
// `new Error("fault.…")` and the UI branches on that literal, so the Go error
// string is the same string rather than a wrapped sentence around it.
type Fault struct{ Code string }

func (f *Fault) Error() string { return f.Code }

// The facade's fault codes, byte-identical to the strings nexAgentConnectionScope.ts
// throws and the UI reads.
const (
	FaultInvalidConnectionID  = "fault.connection.invalidConnectionId"
	FaultClosed               = "fault.connection.closed"
	FaultHandshakeRequired    = "fault.connection.handshakeRequired"
	FaultHelloRequired        = "fault.connection.helloRequired"
	FaultClientChanged        = "fault.connection.clientChanged"
	FaultFlowControlForbidden = "fault.connection.flowControlForbidden"
	FaultFlowControlUntrusted = "fault.connection.flowControlUntrusted"
	FaultNotOwned             = "fault.subscription.notOwned"
	FaultFrameStagingOverflow = "fault.subscription.initialFrameStagingOverflow"
	FaultRuntimeRestarted     = "fault.subscription.runtimeRestarted"
	FaultClientMismatch       = "fault.command.clientMismatch"
	FaultForeignWorkspace     = "fault.command.queryForeignWorkspace"
)

// FaultCode reports the fault code an error carries, or "" when it carries none.
// It unwraps, so a wrapped base error is still reported honestly.
func FaultCode(err error) string {
	var f *Fault
	if errors.As(err, &f) {
		return f.Code
	}
	return ""
}

// MaxDownstreamConnectionIDLength is the cap assertConnectionId enforces. A relay
// downstream id is attacker-influenced, and it is written into the wire identity
// the CLI fans out on, so an unbounded one would let one connection push another
// into unbounded route keys.
const MaxDownstreamConnectionIDLength = 256

// MaxPendingOwnershipFrames / MaxPendingOwnershipBytes bound the ACK staging window.
// Staging only covers the control-plane race (a frame arriving between the subscribe
// request and its ACK); it is NOT the subscriber's buffer, which lives behind the
// resync path.
const (
	MaxPendingOwnershipFrames = 1024
	MaxPendingOwnershipBytes  = 32 * 1024 * 1024
)

// Target is the workspace a call names. Per AGENTS.md the identity key is always
// `identity?.trim() || path` — never the path alone.
type Target struct {
	WorkspacePath     string
	WorkspaceIdentity string
	RemoteSessionID   string
}

// WorkspaceKey is the one keying rule: identity when present, path otherwise.
func (t Target) WorkspaceKey() string { return WorkspaceKey(t.WorkspacePath, t.WorkspaceIdentity) }

// WorkspaceKey ports workspaceKey(target).
func WorkspaceKey(workspacePath, workspaceIdentity string) string {
	if trimmed := strings.TrimSpace(workspaceIdentity); trimmed != "" {
		return trimmed
	}
	return workspacePath
}

// TrustedConnection is the connection identity the facade vouches for. It is
// written by the host, never by the caller.
type TrustedConnection struct {
	ConnectionID string     `json:"connectionId"`
	ClientMode   ClientMode `json:"clientMode"`
	// WorkflowRunDeltas is the client's clientHello declaration. Only true is
	// carried: absence and false are the same thing downstream (a legacy consumer),
	// and the read side tests `=== true`.
	WorkflowRunDeltas bool `json:"workflowRunDeltas,omitempty"`
}

// UnsubscribeRoute pins an unsubscribe to the exact (topic, connectionId) that
// created it. The terminal UI only sends a subscription id, so this is how the
// trusted relay path disambiguates; the terminal path refuses to guess.
type UnsubscribeRoute struct {
	Topic        string `json:"topic"`
	ConnectionID string `json:"connectionId"`
}

// Forwarded is one base-service call target: the caller's own params with every
// forgeable identity key REMOVED, plus the host truth.
//
// The TS builds this by copying the params object and deleting
// connectionId / clientMode / deliveryProfile / subscriberScope / workflowRunDeltas
// before writing the carrier. This is the same operation: Fields is the caller's
// params, StripForgeableFields has already removed the forgeable keys from it, and
// MarshalJSON adds the workspace and the two carrier objects back.
type Forwarded struct {
	// Fields are the caller's remaining params (JSON members). Nil is legal.
	Fields map[string]json.RawMessage
	// Target is the workspace, taken from the caller's params.
	Target Target
	// Trusted is the connection identity this facade vouched for. Nil only for the
	// calls that genuinely carry no identity (runtime lifecycle observers).
	Trusted *TrustedConnection
	// Route is the unsubscribe route, set on unsubscribe / resync only.
	Route *UnsubscribeRoute
}

// MarshalJSON renders the forwarded params in the TS shape: the caller's own keys
// first (the forgeable ones already gone), then workspacePath / workspaceIdentity /
// remoteSessionId, then the two carriers.
func (f Forwarded) MarshalJSON() ([]byte, error) {
	out := make(map[string]json.RawMessage, len(f.Fields)+4)
	for k, v := range f.Fields {
		out[k] = v
	}
	put := func(key string, value any) error {
		raw, err := json.Marshal(value)
		if err != nil {
			return err
		}
		out[key] = raw
		return nil
	}
	if err := put("workspacePath", f.Target.WorkspacePath); err != nil {
		return nil, err
	}
	if f.Target.WorkspaceIdentity != "" {
		if err := put("workspaceIdentity", f.Target.WorkspaceIdentity); err != nil {
			return nil, err
		}
	}
	if f.Target.RemoteSessionID != "" {
		if err := put("remoteSessionId", f.Target.RemoteSessionID); err != nil {
			return nil, err
		}
	}
	if f.Trusted != nil {
		if err := put(TrustedConnectionField, f.Trusted); err != nil {
			return nil, err
		}
	}
	if f.Route != nil {
		if err := put(TrustedUnsubscribeRouteField, f.Route); err != nil {
			return nil, err
		}
	}
	return json.Marshal(out)
}

// SubscribeBase is a client's confirmed watermark (transport.ts subscribeParams).
// The water-mark invariant is the client's: a base may only be sent when the client
// really holds consistent state at that instant, otherwise the ACK's mode would lie.
type SubscribeBase struct {
	LogEpoch string  `json:"logEpoch"`
	Seq      float64 `json:"seq"`
}

// SubscribeArgs is what a subscribe call names beyond the workspace.
type SubscribeArgs struct {
	SessionID       string
	SubscriberScope string
	Base            *SubscribeBase
	Visibility      string
}

// SubscribeAck is the CLI's verdict. Only the subscription id is the facade's
// concern; mode and logEpoch travel back to the caller verbatim.
type SubscribeAck struct {
	SubscriptionID string          `json:"subscriptionId"`
	Mode           string          `json:"mode"`
	LogEpoch       string          `json:"logEpoch"`
	Raw            json.RawMessage `json:"-"`
}

// UnsubscribeArgs is the teardown request.
type UnsubscribeArgs struct {
	SubscriptionID string
	RuntimePolicy  string
}

// ResyncArgs re-arbitrates an active subscription from the caller's watermark.
type ResyncArgs struct {
	SubscriptionID string
	Base           *SubscribeBase
	ForceSnapshot  bool
	RuntimePolicy  string
}

// CommandEnvelope is the part of a command this facade checks: it must carry the
// clientId the handshake bound, or the terminal path refuses it.
type CommandEnvelope struct {
	ClientID string
	Raw      json.RawMessage
}

// AttachmentBeginResult reports whether an upload was accepted into staging. Only
// a staging begin registers a flow route; a rejected one has nothing to close.
type AttachmentBeginResult struct {
	State string
}

// Frame is one physical wire frame on a workspace stream. Only the routing keys
// are decoded here: every other defect belongs to the OWNED assembler
// (proto.Assembler), which turns it into a typed fault. A frame with no topic
// cannot be routed at all and is the one thing DecodeFrame rejects.
type Frame struct {
	Raw            []byte
	Topic          string
	SubscriptionID string
}

// DecodeFrame reads the routing keys out of one physical frame.
func DecodeFrame(raw []byte) (Frame, error) {
	candidate, err := proto.ParseCandidate(raw)
	if err != nil {
		return Frame{}, err
	}
	if candidate.Topic == "" || candidate.SubscriptionID == "" {
		return Frame{}, fmt.Errorf("%w: frame has no routable topic", errUnroutableFrame)
	}
	return Frame{Raw: raw, Topic: candidate.Topic, SubscriptionID: candidate.SubscriptionID}, nil
}

type unroutableFrameError string

func (e unroutableFrameError) Error() string { return string(e) }

const errUnroutableFrame = unroutableFrameError("frame is not routable")

// AgentBase is the shared INexAgentService seam this facade wraps. The three topic
// families are one method each because they are structurally identical here (the
// CLI splits one workspace stream by topic prefix), and the forward-only methods
// are one Pass-through table because they carry no ownership rule of their own.
//
// It is an interface on purpose: the agent bridge of step 3.10.5 implements it, and
// the tests implement it in memory. The facade never reaches past it.
type AgentBase interface {
	// SubscribeTopic opens one subscription. The facade calls it once per ACK and
	// relies on SubscriptionID being the identity the frames will carry.
	SubscribeTopic(ctx context.Context, kind Kind, call Forwarded, args SubscribeArgs) (*SubscribeAck, error)
	// UnsubscribeTopic tears one subscription down. Route is set so the base
	// service can verify the subscription still belongs to that connection.
	UnsubscribeTopic(ctx context.Context, kind Kind, call Forwarded, args UnsubscribeArgs, route UnsubscribeRoute) error
	// ResyncTopic re-arbitrates an active subscription.
	ResyncTopic(ctx context.Context, kind Kind, call Forwarded, args ResyncArgs) (json.RawMessage, error)
	// TopicFrames subscribes to the workspace's downstream frame stream. One stream
	// carries all three families; the facade routes by (connectionId, topic).
	TopicFrames(ctx context.Context, target Target) (<-chan []byte, func(), error)

	// SetConnectionFlowState forwards a flow edge for one (workspace, connection)
	// route. It is the trusted-relay path only.
	SetConnectionFlowState(ctx context.Context, call Forwarded, state FlowState) error
	// SendCommand delivers a turn's command envelope.
	SendCommand(ctx context.Context, call Forwarded, envelope CommandEnvelope) (json.RawMessage, error)
	// QueryCommands answers a command-key query; a clock probe is the `clock` field.
	QueryCommands(ctx context.Context, call Forwarded, params json.RawMessage) (json.RawMessage, error)
	// AttachmentBegin opens an upload. A staging result registers a flow route.
	AttachmentBegin(ctx context.Context, call Forwarded, params json.RawMessage) (*AttachmentBeginResult, error)
	// AttachmentAbort abandons an upload under the SAME trusted identity, which is
	// what a late begin ACK after close must use.
	AttachmentAbort(ctx context.Context, call Forwarded, params json.RawMessage) error

	// PassThrough is the forward-only remainder of INexAgentService (attachments,
	// rows range, plans, workflow runs, bash output, background queries). The
	// facade's whole contribution to these is the readiness gate and the trusted
	// identity, so it keeps one code path instead of forty copies. The method name
	// set is the contract and PassThroughMethods is the table.
	PassThrough(ctx context.Context, method string, call Forwarded, params json.RawMessage) (json.RawMessage, error)
}

// InteractionRegistry is step 3.10.2's RegisterInteractionHandler seam as the facade
// sees it: the protocol client the agent leg belongs to.
type InteractionRegistry interface {
	RegisterInteractionHandler(method string, fn proto.InteractionHandler)
}
