package agentscope

import (
	"encoding/json"
	"strings"
)

// carrier.go ports the two trusted-carrier helpers of nexAgentConnectionScope.ts:
// readTrustedNexAgentV4Connection and readTrustedNexAgentV4UnsubscribeRoute, plus
// withTrustedConnection's deletion rule.
//
// Both carriers travel ONLY between the facade and the base/remote service proxy.
// The facade overwrites every same-named field in the UI's params, so a renderer or
// a phone cannot forge the trusted connection mode — it can only end up with the
// one the host vouched for.

const (
	// TrustedConnectionField is __nexTrustedV4Connection.
	TrustedConnectionField = "__nexTrustedV4Connection"
	// TrustedUnsubscribeRouteField is __nexTrustedV4UnsubscribeRoute.
	TrustedUnsubscribeRouteField = "__nexTrustedV4UnsubscribeRoute"
)

// forgeableFields are the params keys a caller must never be able to set. The TS
// deletes all of them before writing the carrier: a shared service exposed on more
// than one RPC port let a caller pass a profile while every port shared the same
// workspace fan-out, so the facade has to strip first and write last.
var forgeableFields = []string{
	TrustedConnectionField,
	TrustedUnsubscribeRouteField,
	"connectionId",
	"clientMode",
	"deliveryProfile",
	"subscriberScope",
	// workflowRunDeltas is in the same family as clientMode: whether a client
	// understands the key-level increment is a CONNECTION fact, not a per-subscribe
	// preference the UI may pick.
	"workflowRunDeltas",
}

// ReadTrustedConnection ports readTrustedNexAgentV4Connection. It returns nil for
// anything that is not a well-formed carrier: a missing connectionId, or a
// clientMode that is neither of the two known values. Only `true` is honoured for
// workflowRunDeltas — absence and false are the same thing downstream, because the
// read side tests `=== true`.
func ReadTrustedConnection(value any) *TrustedConnection {
	object, ok := value.(map[string]any)
	if !ok {
		return nil
	}
	raw, ok := object[TrustedConnectionField].(map[string]any)
	if !ok {
		return nil
	}
	connectionID, ok := raw["connectionId"].(string)
	if !ok {
		return nil
	}
	clientMode, _ := raw["clientMode"].(string)
	if clientMode != string(ClientModeContinuous) && clientMode != string(ClientModeReplayable) {
		return nil
	}
	out := &TrustedConnection{ConnectionID: connectionID, ClientMode: ClientMode(clientMode)}
	if deltas, ok := raw["workflowRunDeltas"].(bool); ok && deltas {
		out.WorkflowRunDeltas = true
	}
	return out
}

// ReadTrustedUnsubscribeRoute ports readTrustedNexAgentV4UnsubscribeRoute.
func ReadTrustedUnsubscribeRoute(value any) *UnsubscribeRoute {
	object, ok := value.(map[string]any)
	if !ok {
		return nil
	}
	raw, ok := object[TrustedUnsubscribeRouteField].(map[string]any)
	if !ok {
		return nil
	}
	topic, topicOK := raw["topic"].(string)
	connectionID, connectionOK := raw["connectionId"].(string)
	if !topicOK || !connectionOK {
		return nil
	}
	return &UnsubscribeRoute{Topic: topic, ConnectionID: connectionID}
}

// incomingParams is one decoded RPC argument: the caller's own JSON members.
type incomingParams map[string]json.RawMessage

// mustJSON marshals a value the facade itself wrote (a subscription id, a flow
// state). It cannot fail for any of them, and a failure here would be a bug in the
// facade rather than in the caller's input, so it panics loudly instead of
// silently dropping a key the base service needs.
func mustJSON(value any) json.RawMessage {
	raw, err := json.Marshal(value)
	if err != nil {
		panic("agentscope: marshalling a facade-written value failed: " + err.Error())
	}
	return raw
}

// marshalFields renders the stripped caller fields back into one JSON object for a
// base method that takes raw params. The members keep their original bytes, so the
// base service sees exactly what the caller sent minus the forgeable keys.
func marshalFields(fields map[string]json.RawMessage) json.RawMessage {
	if len(fields) == 0 {
		return nil
	}
	return json.RawMessage(mustJSON(fields))
}

// carrierValue re-decodes the caller's params into the plain map the carrier
// readers take, so ReadTrustedConnection sees exactly what the wire carried. A
// member whose value is not an object simply reads as absent, which is the TS
// `(value as Carrier)?.[FIELD]` behaviour for a non-object member.
func (p incomingParams) carrierValue() map[string]any {
	out := make(map[string]any, len(p))
	for key, raw := range p {
		var value any
		if err := json.Unmarshal(raw, &value); err != nil {
			continue
		}
		out[key] = value
	}
	return out
}

// decodeParams turns one decoded argument into its JSON members. A nil or
// non-object argument is an empty set, matching the TS spreading `...undefined`.
func decodeParams(raw json.RawMessage) (incomingParams, error) {
	if len(raw) == 0 {
		return incomingParams{}, nil
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal(raw, &object); err != nil {
		return nil, err
	}
	if object == nil {
		return incomingParams{}, nil
	}
	return incomingParams(object), nil
}

// StripForgeable removes every key a caller could use to pick its own identity.
// It is the first thing Forwarded construction does; doing it here rather than at
// each call site is what makes the guarantee checkable in one place.
func (p incomingParams) StripForgeable() map[string]json.RawMessage {
	out := make(map[string]json.RawMessage, len(p))
	for key, value := range p {
		if isForgeable(key) {
			continue
		}
		out[key] = value
	}
	return out
}

func isForgeable(key string) bool {
	for _, forbidden := range forgeableFields {
		if key == forbidden {
			return true
		}
	}
	return false
}

// target reads the workspace off the caller's params. The identity/remote fields
// the caller sends are its own business; the facade only ever ADDS them back.
func (p incomingParams) target() Target {
	return Target{
		WorkspacePath:     p.stringOrEmpty("workspacePath"),
		WorkspaceIdentity: p.stringOrEmpty("workspaceIdentity"),
		RemoteSessionID:   p.stringOrEmpty("remoteSessionId"),
	}
}

func (p incomingParams) stringOrEmpty(key string) string {
	var value string
	if err := json.Unmarshal(p[key], &value); err != nil {
		return ""
	}
	return value
}

// stringField decodes a string member. A member of the wrong type reads as absent
// rather than as an error, which is what the TS `params.x` does when x is not a
// string (it simply is not `=== "..."`).
func (p incomingParams) stringField(key string) string { return p.stringOrEmpty(key) }

func (p incomingParams) rawField(key string) json.RawMessage {
	return p[key]
}

func (p incomingParams) boolField(key string) bool {
	var value bool
	if err := json.Unmarshal(p[key], &value); err != nil {
		return false
	}
	return value
}

// envelope reads the command envelope. It is forwarded verbatim; only clientId is
// read, because the terminal path refuses a command that does not carry the client
// the handshake bound.
func (p incomingParams) envelope() CommandEnvelope {
	raw := p["envelope"]
	var fields struct {
		ClientID string `json:"clientId"`
	}
	_ = json.Unmarshal(raw, &fields)
	return CommandEnvelope{ClientID: fields.ClientID, Raw: raw}
}

// subscribeArgs reads the members a subscribe call carries beyond the workspace.
// A member of the wrong type is dropped, not an error: the base service revalidates
// against the CLI, and a facade that invented a schema of its own would only be a
// second place for the two engines to disagree.
func (p incomingParams) subscribeArgs() SubscribeArgs {
	args := SubscribeArgs{
		SessionID:       p.stringField("sessionId"),
		SubscriberScope: p.stringField("subscriberScope"),
		Visibility:      p.stringField("visibility"),
	}
	args.Base = p.baseField("base")
	return args
}

func (p incomingParams) baseField(key string) *SubscribeBase {
	raw := p[key]
	if len(raw) == 0 {
		return nil
	}
	var base SubscribeBase
	if err := json.Unmarshal(raw, &base); err != nil {
		return nil
	}
	return &base
}

func (p incomingParams) unsubscribeArgs() UnsubscribeArgs {
	return UnsubscribeArgs{
		SubscriptionID: p.stringField("subscriptionId"),
		RuntimePolicy:  p.stringField("runtimePolicy"),
	}
}

func (p incomingParams) resyncArgs() ResyncArgs {
	return ResyncArgs{
		SubscriptionID: p.stringField("subscriptionId"),
		Base:           p.baseField("base"),
		ForceSnapshot:  p.boolField("forceSnapshot"),
		RuntimePolicy:  p.stringField("runtimePolicy"),
	}
}

// namespaceRelayConnectionID ports namespaceRelayConnectionId. The length prefix is
// what kills the separator-collision: `a/b` + `c` and `a` + `b/c` produce the same
// naive join but different real connections, and this string is the route key the
// CLI fans out on. The connection id stays an opaque route key — nothing parses it.
func namespaceRelayConnectionID(upstreamConnectionID, downstreamConnectionID string) string {
	return "relay:" + itoa(len([]rune(upstreamConnectionID))) + ":" + upstreamConnectionID +
		itoa(len([]rune(downstreamConnectionID))) + ":" + downstreamConnectionID
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var digits [20]byte
	index := len(digits)
	for n > 0 {
		index--
		digits[index] = byte('0' + n%10)
		n /= 10
	}
	return string(digits[index:])
}

// assertConnectionID ports assertConnectionId. It throws `fault.…` in the TS; here
// the constructor returns the fault, because a constructor cannot fail midway.
func assertConnectionID(connectionID string) error {
	if strings.TrimSpace(connectionID) == "" || len([]rune(connectionID)) > MaxDownstreamConnectionIDLength {
		return &Fault{Code: FaultInvalidConnectionID}
	}
	return nil
}
