package permission_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/tylinux/nex/go/internal/permission"
)

type decisionVector struct {
	Name         string          `json:"name"`
	Tool         string          `json:"tool"`
	Mode         string          `json:"mode"`
	Input        json.RawMessage `json:"input"`
	ProjectRules json.RawMessage `json:"projectRules"`
	Decision     struct {
		Decision        string  `json:"decision"`
		Allowed         bool    `json:"allowed"`
		Escalated       bool    `json:"escalated"`
		Reason          *string `json:"reason"`
		RuleID          string  `json:"ruleId"`
		RiskLevel       string  `json:"riskLevel"`
		SideEffectScope string  `json:"sideEffectScope"`
	} `json:"decision"`
}

type optionsVector struct {
	Name          string          `json:"name"`
	ToolName      string          `json:"toolName"`
	Input         json.RawMessage `json:"input"`
	OptionsPolicy *string         `json:"optionsPolicy"`
	Options       json.RawMessage `json:"options"`
}

type permissionVectors struct {
	Decisions []decisionVector `json:"decisions"`
	Options   []optionsVector  `json:"options"`
}

// capabilityFor mirrors what the executor passes: the tool's metadata + permission
// spec projected into PermissionToolCapability.
func capabilityFor(tool string) permission.Capability {
	switch tool {
	case "Read":
		return permission.Capability{ReadOnly: true, Destructive: false, SideEffectScope: "none", RiskLevel: "low", NeedsApproval: false, Permission: "read"}
	case "Write", "Edit":
		return permission.Capability{ReadOnly: false, Destructive: false, SideEffectScope: "workspace", RiskLevel: "medium", NeedsApproval: true, Permission: "edit"}
	}
	return permission.Capability{}
}

func decode(t *testing.T, raw json.RawMessage) any {
	t.Helper()
	if len(raw) == 0 || string(raw) == "null" {
		return nil
	}
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		t.Fatal(err)
	}
	return v
}

func TestPermissionDecisionVectors(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "testdata", "vectors", "tools-permission.json"))
	if err != nil {
		t.Fatal(err)
	}
	var v permissionVectors
	if err := json.Unmarshal(raw, &v); err != nil {
		t.Fatal(err)
	}
	if len(v.Decisions) < 15 {
		t.Fatalf("only %d permission decisions", len(v.Decisions))
	}
	for _, c := range v.Decisions {
		c := c
		t.Run(c.Name, func(t *testing.T) {
			var rules *permission.Ruleset
			if c.ProjectRules != nil && string(c.ProjectRules) != "null" {
				rules = parseRuleset(t, c.ProjectRules)
			}
			ctx := permission.Context{
				ToolName:  c.Tool,
				Input:     decode(t, c.Input),
				RiskLevel: "medium",
				Mode:      c.Mode,
			}
			got := permission.CheckPermission(ctx, capabilityFor(c.Tool), rules, permission.DefaultConfig())
			if got.Decision != c.Decision.Decision {
				t.Fatalf("decision: got %q want %q (%s)", got.Decision, c.Decision.Decision, got.Reason)
			}
			if got.Allowed != c.Decision.Allowed {
				t.Fatalf("allowed: got %v want %v", got.Allowed, c.Decision.Allowed)
			}
			if got.Escalated != c.Decision.Escalated {
				t.Fatalf("escalated: got %v want %v", got.Escalated, c.Decision.Escalated)
			}
			if got.RuleID != c.Decision.RuleID {
				t.Fatalf("ruleId: got %q want %q", got.RuleID, c.Decision.RuleID)
			}
			wantReason := ""
			if c.Decision.Reason != nil {
				wantReason = *c.Decision.Reason
			}
			if got.Reason != wantReason {
				t.Fatalf("reason: got %q want %q", got.Reason, wantReason)
			}
			if got.RiskLevel != c.Decision.RiskLevel {
				t.Fatalf("riskLevel: got %q want %q", got.RiskLevel, c.Decision.RiskLevel)
			}
			if c.Decision.SideEffectScope != "" && got.SideEffectScope != c.Decision.SideEffectScope {
				t.Fatalf("sideEffectScope: got %q want %q", got.SideEffectScope, c.Decision.SideEffectScope)
			}
		})
	}
}

func parseRuleset(t *testing.T, raw json.RawMessage) *permission.Ruleset {
	t.Helper()
	var m map[string]json.RawMessage
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	rs := &permission.Ruleset{}
	rs.Allow = parseRules(t, m["allow"])
	rs.Deny = parseRules(t, m["deny"])
	rs.Ask = parseRules(t, m["ask"])
	return rs
}

func parseRules(t *testing.T, raw json.RawMessage) []permission.RuleValue {
	t.Helper()
	if len(raw) == 0 {
		return nil
	}
	var rules []permission.RuleValue
	if err := json.Unmarshal(raw, &rules); err != nil {
		t.Fatal(err)
	}
	return rules
}

func TestPermissionOptionVectors(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "testdata", "vectors", "tools-permission.json"))
	if err != nil {
		t.Fatal(err)
	}
	var v permissionVectors
	if err := json.Unmarshal(raw, &v); err != nil {
		t.Fatal(err)
	}
	if len(v.Options) < 4 {
		t.Fatalf("only %d option vectors", len(v.Options))
	}
	for _, c := range v.Options {
		c := c
		t.Run(c.Name, func(t *testing.T) {
			policy := ""
			if c.OptionsPolicy != nil {
				policy = *c.OptionsPolicy
			}
			got := permission.BuildProtocolPermissionOptions(c.ToolName, decode(t, c.Input), nil, policy)
			gotJSON, _ := json.Marshal(got)
			var gotAny, wantAny any
			_ = json.Unmarshal(gotJSON, &gotAny)
			_ = json.Unmarshal(c.Options, &wantAny)
			if !reflect.DeepEqual(gotAny, wantAny) {
				g, _ := json.MarshalIndent(gotAny, "", " ")
				w, _ := json.MarshalIndent(wantAny, "", " ")
				t.Fatalf("options mismatch\n got %s\nwant %s", g, w)
			}
		})
	}
}
