package permission

import "strings"

// This file ports bootstrap/src/permission-options.ts: the v3/v4 shared permission
// option projection and the user-denial content.

// PermissionDeniedByUserContent is the reason the model receives as the tool result
// text when the user denies a permission request.
const PermissionDeniedByUserContent = "The user doesn't want to proceed with this tool use. The tool use was rejected (eg. if it was a file edit, the new_string was NOT written to the file). STOP what you are doing and wait for the user to tell you how to proceed."

// projectRuleInputKeys mirror PROJECT_RULE_INPUT_KEYS.
var projectRuleInputKeys = []string{"command", "url", "file_path", "path", "pattern"}

// PermissionUpdate mirrors PermissionUpdate ({type:"addRules", behavior, rules}).
type PermissionUpdate struct {
	Type     string      `json:"type"`
	Behavior string      `json:"behavior"`
	Rules    []RuleValue `json:"rules"`
}

// PermissionOption mirrors NexPermissionOption.
type PermissionOption struct {
	Kind        string         `json:"kind"`
	Name        string         `json:"name"`
	OptionID    string         `json:"optionId"`
	Description string         `json:"description,omitempty"`
	Response    OptionResponse `json:"response"`
}

// OptionResponse mirrors the option response payload.
type OptionResponse struct {
	Decision          string             `json:"decision"`
	Reason            string             `json:"reason"`
	PermissionUpdates []PermissionUpdate `json:"permissionUpdates,omitempty"`
}

// BuildProtocolPermissionOptions mirrors buildProtocolPermissionOptions(): the
// allow_once / allow_project / deny option list the requestPermission frame
// carries (optionsPolicy "" is the default project-scope option).
func BuildProtocolPermissionOptions(toolName string, input any, suggested []PermissionUpdate, optionsPolicy string) []PermissionOption {
	updates := suggested
	if len(updates) == 0 {
		updates = defaultPermissionUpdates(toolName, input)
	}
	options := []PermissionOption{
		{
			Kind: "allow_once", Name: "Allow once", OptionID: "allow_once",
			Response: OptionResponse{Decision: "allow", Reason: "Approved once"},
		},
	}
	switch optionsPolicy {
	case "no-always-allow":
		// no project option
	case "session-always-allow":
		options = append(options, PermissionOption{
			Description: "Do not ask again for this tool in this session",
			Kind:        "allow_session",
			Name:        "Always allow in this session",
			OptionID:    "allowSession",
			Response:    OptionResponse{Decision: "allow", Reason: "Approved for this session"},
		})
	default:
		options = append(options, PermissionOption{
			Description: "Do not ask again for matching requests in this project",
			Kind:        "allow_always",
			Name:        "Always allow in this project",
			OptionID:    "allow_project",
			Response:    OptionResponse{Decision: "allow", Reason: "Approved for this project", PermissionUpdates: updates},
		})
	}
	options = append(options, PermissionOption{
		Kind: "deny", Name: "Deny", OptionID: "deny",
		Response: OptionResponse{Decision: "deny", Reason: PermissionDeniedByUserContent},
	})
	return options
}

// BuildSessionPermissionUpdates mirrors buildSessionPermissionUpdates().
func BuildSessionPermissionUpdates(toolName string) []PermissionUpdate {
	return []PermissionUpdate{{Behavior: "allow", Rules: []RuleValue{{ToolName: toolName}}, Type: "addRules"}}
}

func defaultPermissionUpdates(toolName string, input any) []PermissionUpdate {
	ruleContent := ruleContentFromPermissionInput(input)
	rule := RuleValue{ToolName: toolName}
	if ruleContent != "" {
		rule.RuleContent = ruleContent
	}
	return []PermissionUpdate{{Behavior: "allow", Rules: []RuleValue{rule}, Type: "addRules"}}
}

// ruleContentFromPermissionInput mirrors ruleContentFromPermissionInput(): a
// non-empty string input, else the first present subject key.
func ruleContentFromPermissionInput(input any) string {
	if s, ok := input.(string); ok && strings.TrimSpace(s) != "" {
		return s
	}
	record, ok := input.(map[string]any)
	if !ok {
		return ""
	}
	for _, key := range projectRuleInputKeys {
		if v, ok := record[key].(string); ok && strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

// BuildDeniedContent mirrors buildPermissionDeniedContent(feedback).
func BuildDeniedContent(feedback string) string {
	trimmed := strings.TrimSpace(feedback)
	if trimmed == "" {
		return PermissionDeniedByUserContent
	}
	return PermissionDeniedByUserContent + " To tell you how to proceed, the user said:\n" + trimmed
}
