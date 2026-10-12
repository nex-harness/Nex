package permission

import (
	"github.com/google/uuid"
)

// Request mirrors the params of the interaction/requestPermission frame (legacy
// v3 shape used by the S04/S05 recordings). Field order matches the recorded
// frames: input, reason, requestId, riskLevel, sessionId, options, toolCallId,
// toolName, turnId.
type Request struct {
	Input      any                `json:"input"`
	Reason     string             `json:"reason"`
	RequestID  string             `json:"requestId"`
	RiskLevel  string             `json:"riskLevel"`
	SessionID  string             `json:"sessionId"`
	Options    []PermissionOption `json:"options"`
	ToolCallID string             `json:"toolCallId"`
	ToolName   string             `json:"toolName"`
	TurnID     string             `json:"turnId"`
}

// BuildRequest builds the requestPermission payload with a fresh perm_<uuid>
// requestId, mirroring the recorded frames.
func BuildRequest(params RequestParams) Request {
	requestID := "perm_" + uuid.NewString()
	return Request{
		Input:      params.Input,
		Reason:     params.Reason,
		RequestID:  requestID,
		RiskLevel:  params.RiskLevel,
		SessionID:  params.SessionID,
		Options:    BuildProtocolPermissionOptions(params.ToolName, params.Input, params.SuggestedUpdates, params.OptionsPolicy),
		ToolCallID: params.ToolCallID,
		ToolName:   params.ToolName,
		TurnID:     params.TurnID,
	}
}

// RequestParams are the inputs BuildRequest needs beyond the decision itself.
type RequestParams struct {
	Input            any
	Reason           string
	RiskLevel        string
	SessionID        string
	ToolCallID       string
	ToolName         string
	TurnID           string
	SuggestedUpdates []PermissionUpdate
	OptionsPolicy    string
}

// PermissionAskReason is the reason for a build-mode side-effect approval, exactly
// the string the S04/S05 requestPermission frames carry.
const PermissionAskReason = "Tool has side effects and requires approval"

// RiskLevelMedium is the riskLevel the requestPermission frames carry for
// Write/Edit in build mode.
const RiskLevelMedium = "medium"
