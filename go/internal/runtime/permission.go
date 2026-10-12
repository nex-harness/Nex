package runtime

import (
	"context"
	"encoding/json"

	"github.com/tylinux/nex/go/internal/emitters"
	"github.com/tylinux/nex/go/internal/permission"
	"github.com/tylinux/nex/go/internal/protocol"
	"github.com/tylinux/nex/go/internal/tools"
)

// capabilityFor builds the permission capability from the tool registry.
func capabilityFor(name string) (permission.Capability, bool) {
	for _, e := range tools.Registry() {
		if e.Metadata.Name != name {
			continue
		}
		return permission.Capability{
			ReadOnly:        e.Metadata.ReadOnly,
			Destructive:     e.Metadata.Destructive,
			SideEffectScope: e.Metadata.SideEffectScope,
			RiskLevel:       e.Metadata.RiskLevel,
			NeedsApproval:   e.Metadata.NeedsApproval,
			Permission:      e.Metadata.Permission,
		}, true
	}
	return permission.Capability{}, false
}

// permissionDecision is the outcome of the approval flow for one tool call.
type permissionDecision struct {
	asked     bool
	allowed   bool
	requestID string
	reason    string
}

// requestPermission runs the approval flow for a side-effecting tool call. It
// emits permission_requested, sends interaction/requestPermission to the host,
// and emits permission_resolved. It returns whether approval was asked and
// granted. The host answers with the chosen option's response.
func (s *session) requestPermission(ctx context.Context, turnID, toolCallID, toolName string, input any, inputJSON json.RawMessage) permissionDecision {
	cap, ok := capabilityFor(toolName)
	if !ok {
		return permissionDecision{allowed: true}
	}
	decision := permission.CheckPermission(permission.Context{
		ToolName: toolName, Input: input, Mode: "build", RiskLevel: cap.RiskLevel,
		WorkingDirectory: s.ws.Path,
	}, cap, nil, permission.DefaultConfig())
	if decision.Decision != "ask" {
		return permissionDecision{allowed: decision.Decision == "allow"}
	}

	requestID := s.rt.deps.IDs.UUID()
	permID := "perm_" + requestID
	s.appendEvent(sessionEvent{
		Type: evPermissionRequested, TurnID: turnID, HasTurnID: true,
		Payload: mustJSON(map[string]any{
			"toolCallId": toolCallID, "toolName": toolName,
			"riskLevel": "medium", "reason": permission.PermissionAskReason,
			"input": json.RawMessage(inputJSON), "requestId": permID, "fullAccessSupported": true,
		}),
	})

	req := permission.BuildRequest(permission.RequestParams{
		Input: input, Reason: permission.PermissionAskReason, RiskLevel: permission.RiskLevelMedium,
		SessionID: s.id, ToolCallID: toolCallID, ToolName: toolName, TurnID: turnID,
	})
	// The permission_requested payload and the request frame must carry one id,
	// and the frame's input keeps the model's key order.
	req.RequestID = permID
	req.Input = json.RawMessage(inputJSON)
	params := mustJSON(req)
	reply, err := s.rt.Request(ctx, protocol.Message{
		Kind: protocol.KindRequest, ID: s.rt.nextServerID(), Method: "interaction/requestPermission",
		Params: params, HasParams: true,
	})
	allowed := false
	reason := permission.PermissionDeniedByUserContent
	if err == nil {
		var resp struct {
			Decision string `json:"decision"`
			Reason   string `json:"reason"`
		}
		_ = json.Unmarshal(reply, &resp)
		allowed = resp.Decision == "allow"
		if resp.Reason != "" && !allowed {
			reason = resp.Reason
		}
	}
	resolvedDecision := "deny"
	if allowed {
		resolvedDecision = "allow"
	}
	s.appendEvent(sessionEvent{
		Type: evPermissionResolved, TurnID: turnID, HasTurnID: true,
		Payload: mustJSON(map[string]any{
			"toolCallId": toolCallID, "decision": resolvedDecision, "requestId": permID,
		}),
	})
	return permissionDecision{asked: true, allowed: allowed, requestID: permID, reason: reason}
}

var _ = emitters.MethodTelemetry

// registryEntry returns the full tools.Entry for a name.
func registryEntry(name string) (tools.Entry, bool) {
	for _, e := range tools.Registry() {
		if e.Metadata.Name == name {
			return e, true
		}
	}
	return tools.Entry{}, false
}
