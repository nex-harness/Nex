package permission

// Context mirrors PermissionContext.
type Context struct {
	ToolName         string
	Input            any
	RiskLevel        string
	Mode             string // build | edit | plan | yolo | auto
	PlanEnabled      *bool
	WorkingDirectory string
}

// Capability mirrors PermissionToolCapability (the subset these tools declare).
type Capability struct {
	ReadOnly                bool
	Destructive             bool
	SideEffectScope         string
	RiskLevel               string
	NeedsApproval           bool
	RequiresUserInteraction bool
	Permission              string // the ToolPermissionSpec.permission name
	AlwaysAsk               bool
}

// Decision mirrors PermissionDecisionResult.
type Decision struct {
	Decision        string // allow | ask | deny
	Allowed         bool
	Escalated       bool
	Reason          string
	RuleID          string
	RiskLevel       string
	SideEffectScope string
}

// Config mirrors PermissionConfig.
type Config struct {
	AllowedTools        map[string]bool
	DisallowedTools     map[string]bool
	AutoApproveHighRisk bool
}

// DefaultConfig mirrors defaultPermissionConfig.
func DefaultConfig() Config {
	return Config{AllowedTools: map[string]bool{}, DisallowedTools: map[string]bool{}}
}

// CheckPermission mirrors PermissionService.checkPermission for the paths the
// Read/Write/Edit port needs. Plan-mode transitions (EnterPlanMode/ExitPlanMode)
// and workflow/webfetch preapprovals are out of scope for this port.
func CheckPermission(ctx Context, cap Capability, projectRules *Ruleset, cfg Config) Decision {
	resolved := resolveCapability(ctx, cap)

	if ctx.Mode == "yolo" && !planEnabled(ctx) {
		return allow(ctx, resolved, "mode.yolo", "Yolo mode bypasses permission prompts")
	}
	if ctx.Mode == "auto" {
		return deny(ctx, resolved, "mode.auto.unimplemented", "Auto mode is reserved but not implemented yet")
	}
	if cfg.DisallowedTools[ctx.ToolName] {
		return deny(ctx, resolved, "rule.disallowedTools", "Tool "+ctx.ToolName+" is explicitly disallowed")
	}
	if projectRules != nil && MatchesRuleset(projectRules.Deny, ctx.ToolName, ctx.Input) {
		return deny(ctx, resolved, "rule.project.deny", "Tool "+ctx.ToolName+" is denied by project permission rules")
	}
	if projectRules != nil && MatchesRuleset(projectRules.Ask, ctx.ToolName, ctx.Input) {
		return ask(ctx, resolved, "rule.project.ask", "Tool "+ctx.ToolName+" requires approval by project permission rules")
	}
	if planEnabled(ctx) {
		return checkPlanMode(ctx, resolved)
	}
	if projectRules != nil && MatchesRuleset(projectRules.Allow, ctx.ToolName, ctx.Input) {
		return allow(ctx, resolved, "rule.project.allow", "Tool "+ctx.ToolName+" is allowed by project permission rules")
	}
	if cfg.AllowedTools[ctx.ToolName] {
		return allow(ctx, resolved, "rule.allowedTools", "Tool "+ctx.ToolName+" is explicitly allowed")
	}
	if ctx.Mode == "edit" {
		return checkEditMode(ctx, resolved)
	}
	return checkBuildMode(ctx, resolved, cfg)
}

func planEnabled(ctx Context) bool {
	if ctx.PlanEnabled != nil {
		return *ctx.PlanEnabled
	}
	return ctx.Mode == "plan"
}

func checkPlanMode(ctx Context, cap resolvedCapability) Decision {
	if cap.readOnly && !cap.destructive {
		return allow(ctx, cap, "mode.plan.readOnly", "Plan mode allows read-only tool execution")
	}
	return deny(ctx, cap, "mode.plan.nonReadOnly", "Plan mode only allows read-only, non-destructive tools")
}

func checkEditMode(ctx Context, cap resolvedCapability) Decision {
	if cap.permissionName == "edit" && cap.sideEffectScope == "workspace" {
		return allow(ctx, cap, "mode.edit.fileEdit", "Edit mode allows file edit tools")
	}
	return checkBuildMode(ctx, cap, DefaultConfig())
}

func checkBuildMode(ctx Context, cap resolvedCapability, cfg Config) Decision {
	if cap.readOnly && !cap.destructive && !cap.needsApproval {
		return allow(ctx, cap, "mode.build.readOnly", "Build mode allows read-only tools")
	}
	if cap.riskLevel == "critical" {
		return ask(ctx, cap, "mode.build.criticalRisk", "Critical risk tools require explicit approval")
	}
	if cap.riskLevel == "high" && !cfg.AutoApproveHighRisk {
		return ask(ctx, cap, "mode.build.highRisk", "High risk tools require explicit approval")
	}
	if cap.sideEffectScope == "session" && cap.riskLevel == "low" && !cap.destructive && !cap.needsApproval {
		return allow(ctx, cap, "mode.build.sessionState", "Build mode allows low-risk session-local state updates")
	}
	if cap.needsApproval || cap.destructive || cap.sideEffectScope != "none" {
		return ask(ctx, cap, "mode.build.sideEffect", "Tool has side effects and requires approval")
	}
	return allow(ctx, cap, "mode.build.lowRisk", "Build mode allows low-risk tool execution")
}

type resolvedCapability struct {
	readOnly            bool
	destructive         bool
	requiresInteraction bool
	sideEffectScope     string
	riskLevel           string
	needsApproval       bool
	permissionName      string
	alwaysAsk           bool
}

func resolveCapability(ctx Context, cap Capability) resolvedCapability {
	sideEffect := cap.SideEffectScope
	if sideEffect == "" {
		if isReadOnlyTool(ctx.ToolName) {
			sideEffect = "none"
		} else {
			sideEffect = "workspace"
		}
	}
	risk := cap.RiskLevel
	if risk == "" {
		risk = defaultRiskLevel(ctx.ToolName)
	}
	return resolvedCapability{
		readOnly:            cap.ReadOnly || (cap.SideEffectScope == "" && isReadOnlyTool(ctx.ToolName)),
		destructive:         cap.Destructive || isDestructiveTool(ctx.ToolName),
		requiresInteraction: cap.RequiresUserInteraction || sideEffect == "userInteraction",
		sideEffectScope:     sideEffect,
		riskLevel:           risk,
		needsApproval:       cap.NeedsApproval || !isReadOnlyTool(ctx.ToolName),
		permissionName:      cap.Permission,
		alwaysAsk:           cap.AlwaysAsk,
	}
}

func allow(ctx Context, cap resolvedCapability, ruleID, reason string) Decision {
	return result("allow", ctx, cap, ruleID, reason)
}

func ask(ctx Context, cap resolvedCapability, ruleID, reason string) Decision {
	return result("ask", ctx, cap, ruleID, reason)
}

func deny(ctx Context, cap resolvedCapability, ruleID, reason string) Decision {
	return result("deny", ctx, cap, ruleID, reason)
}

func result(decision string, ctx Context, cap resolvedCapability, ruleID, reason string) Decision {
	return Decision{
		Decision:        decision,
		Allowed:         decision == "allow",
		Escalated:       decision == "ask",
		Reason:          reason,
		RuleID:          ruleID,
		RiskLevel:       cap.riskLevel,
		SideEffectScope: cap.sideEffectScope,
	}
}

func isReadOnlyTool(name string) bool {
	switch name {
	case "Read", "Glob", "Grep", "WebSearch", "WebFetch", "TodoRead", "TodoWrite", "AskUserQuestion", "Agent", "Task", "Skill":
		return true
	}
	return false
}

func isWriteTool(name string) bool {
	switch name {
	case "Write", "Edit", "ApplyPatch", "Bash":
		return true
	}
	return false
}

func isDestructiveTool(name string) bool { return name == "Bash" }

func defaultRiskLevel(name string) string {
	if isReadOnlyTool(name) {
		return "low"
	}
	if isWriteTool(name) {
		return "medium"
	}
	if isDestructiveTool(name) {
		return "high"
	}
	return "medium"
}
