// modelrules.go ports the rule engine of
// packages/provider/src/config/model-config.ts (ModelConfigRules): ordered
// match rules (model / model-api / provider-site / template-model /
// provider-model / manual-provider-model) resolved by overlaying every match
// in order, plus the personal-rule set/get/rename/delete operations the
// write side uses.
package providerconfig

import (
	"encoding/json"
	"net/url"
	"strings"
)

// Rule types; the type tag identifies the in-memory layer origin and is NOT
// written to the config file (TS comment: type 只标识内存执行规则的来源层).
const (
	RuleModel               = "model"
	RuleModelAPI            = "model-api"
	RuleProviderSite        = "provider-site"
	RuleTemplateModel       = "template-model"
	RuleProviderModel       = "provider-model"
	RuleManualProviderModel = "manual-provider-model"
)

// ModelConfigRule is one ordered rule.
type ModelConfigRule struct {
	Type         string       `json:"-"`
	ProviderID   string       `json:"providerId,omitempty"` // exact rules
	TemplateID   string       `json:"templateId,omitempty"` // template rules
	ModelID      string       `json:"modelId,omitempty"`    // exact/template rules
	ModelMatch   string       `json:"modelMatch,omitempty"`
	APITYPEMatch string       `json:"apiTypeMatch,omitempty"`
	BaseURLMatch string       `json:"baseUrlMatch,omitempty"`
	Config       *ModelConfig `json:"config"`
}

// IsExact reports the two exact-rule kinds.
func (r ModelConfigRule) IsExact() bool {
	return r.Type == RuleProviderModel || r.Type == RuleManualProviderModel
}

// ModelConfigRules is the ordered rule list.
type ModelConfigRules struct{ rules []ModelConfigRule }

// EmptyModelConfigRules returns an empty set.
func EmptyModelConfigRules() *ModelConfigRules { return &ModelConfigRules{} }

// NewModelConfigRules builds from an ordered list.
func NewModelConfigRules(rules []ModelConfigRule) *ModelConfigRules {
	return &ModelConfigRules{rules: rules}
}

// Rules returns the ordered rules.
func (rs *ModelConfigRules) Rules() []ModelConfigRule {
	if rs == nil {
		return nil
	}
	out := make([]ModelConfigRule, len(rs.rules))
	copy(out, rs.rules)
	return out
}

// ComposeEffective ports ModelConfigRules.composeEffective: builtin keeps its
// layers/order; personal contributes only exact (normal/manual) rules.
func (rs *ModelConfigRules) ComposeEffective(personal *ModelConfigRules) *ModelConfigRules {
	merged := make([]ModelConfigRule, 0, rs.Len()+personal.Len())
	merged = append(merged, rs.Rules()...)
	for _, rule := range personal.Rules() {
		if rule.IsExact() {
			merged = append(merged, rule)
		}
	}
	return &ModelConfigRules{rules: merged}
}

// Len is the rule count.
func (rs *ModelConfigRules) Len() int {
	if rs == nil {
		return 0
	}
	return len(rs.rules)
}

// RuleResolutionInput ports ModelConfigRuleResolutionInput.
type RuleResolutionInput struct {
	ProviderID string
	TemplateID string
	ModelID    string
	APIType    string
	BaseURL    string
}

// Resolve ports ModelConfigRules.resolve: fold every matching rule's config
// in order. Manual rules clear the accumulated config first
// (clearManualModelConfig semantics: the manual mode owns every editable
// leaf, so the accumulated recommended leaves are dropped).
func (rs *ModelConfigRules) Resolve(input RuleResolutionInput) *ModelConfig {
	result := &ModelConfig{}
	baseURL := normalizeBaseURLForRuleMatch(input.BaseURL)
	for _, rule := range rs.Rules() {
		if rule.IsExact() {
			if rule.ProviderID != input.ProviderID || rule.ModelID != input.ModelID {
				continue
			}
			if rule.Type == RuleManualProviderModel {
				// 手动规则要求所有可编辑叶子齐全，因此可直接覆盖；系统叶子继续
				// 来自当前身份的规则（TS: clearManualModelConfig(result)）。
				result = clearManualLeaves(result)
			}
			result = result.Overlay(rule.Config)
			continue
		}
		if rule.Type == RuleTemplateModel {
			if rule.TemplateID == input.TemplateID && rule.ModelID == input.ModelID {
				result = result.Overlay(rule.Config)
			}
			continue
		}
		// 只放宽推荐规则匹配，不改真实请求里的模型 ID (ignoreCase=true).
		if !MatchesRule(rule.ModelMatch, input.ModelID, true) {
			continue
		}
		if (rule.Type == RuleModelAPI || rule.Type == RuleProviderSite) && rule.APITYPEMatch != "" {
			if input.APIType == "" || !MatchesRule(rule.APITYPEMatch, input.APIType, false) {
				continue
			}
		}
		if rule.Type == RuleProviderSite && (baseURL == "" || !MatchesRule(rule.BaseURLMatch, baseURL, false)) {
			continue
		}
		result = result.Overlay(rule.Config)
	}
	return result
}

// clearManualLeaves ports clearManualModelConfig: drop the manual-editable
// leaves (contextWindow, supportsJsonSchemaOutput, supportsNativeWebSearch,
// supportsMidConversationSystem, inputFormat.{image,video,pdf},
// optionSpecs.reasoningLevel, optionSpecs.maxOutputTokens.max), keep the rest.
func clearManualLeaves(config *ModelConfig) *ModelConfig {
	if config == nil {
		return &ModelConfig{}
	}
	out := &ModelConfig{Enabled: config.Enabled, Name: config.Name}
	if p := config.Properties; p != nil {
		next := &ModelProperties{
			RequiresMfjsToolSchema: p.RequiresMfjsToolSchema,
			SupportsToolCall:       p.SupportsToolCall,
			InputFormat:            p.InputFormat,
			OutputFormat:           p.OutputFormat,
		}
		if p.InputFormat != nil {
			next.InputFormat = &ModelInputFormat{SupportsText: p.InputFormat.SupportsText, SupportsAudio: p.InputFormat.SupportsAudio}
		}
		out.Properties = next
	}
	if s := config.OptionSpecs; s != nil {
		next := &ModelOptionSpecs{}
		if s.ReasoningLevel != nil {
			next.ReasoningLevel = &EnumOptionSpec{Map: s.ReasoningLevel.Map}
		}
		if s.MaxOutputTokens != nil {
			next.MaxOutputTokens = &LimitOptionSpec{Map: s.MaxOutputTokens.Map}
		}
		out.OptionSpecs = next
	}
	return out
}

// GetExact ports getExact: overlay every exact rule for the pair in order.
func (rs *ModelConfigRules) GetExact(providerID, modelID string) *ModelConfig {
	var result *ModelConfig
	for _, rule := range rs.Rules() {
		if !rule.IsExact() || rule.ProviderID != providerID || rule.ModelID != modelID {
			continue
		}
		if result == nil {
			result = rule.Config
		} else {
			result = result.Overlay(rule.Config)
		}
	}
	return result
}

// GetExactRule ports getExactRule: the LAST exact rule for the pair.
func (rs *ModelConfigRules) GetExactRule(providerID, modelID string) *ModelConfigRule {
	rules := rs.Rules()
	for i := len(rules) - 1; i >= 0; i-- {
		if rules[i].IsExact() && rules[i].ProviderID == providerID && rules[i].ModelID == modelID {
			return &rules[i]
		}
	}
	return nil
}

// SetExact ports setExact: replace (or append) the exact rule for the pair;
// manual is decided by useRecommendedConfig (nil keeps the previous mode).
func (rs *ModelConfigRules) SetExact(providerID, modelID string, config *ModelConfig, useRecommendedConfig *bool) (*ModelConfigRules, error) {
	previous := rs.GetExactRule(providerID, modelID)
	manual := false
	if useRecommendedConfig == nil {
		manual = previous != nil && previous.Type == RuleManualProviderModel
	} else {
		manual = !*useRecommendedConfig
	}
	ruleType := RuleProviderModel
	if manual {
		ruleType = RuleManualProviderModel
	}
	replacement := ModelConfigRule{Type: ruleType, ProviderID: providerID, ModelID: modelID, Config: config}
	out := make([]ModelConfigRule, 0, len(rs.rules)+1)
	replaced := false
	for _, rule := range rs.Rules() {
		if rule.IsExact() && rule.ProviderID == providerID && rule.ModelID == modelID {
			if !replaced {
				out = append(out, replacement)
				replaced = true
			}
			continue
		}
		out = append(out, rule)
	}
	if !replaced {
		out = append(out, replacement)
	}
	return &ModelConfigRules{rules: out}, nil
}

// DeleteExact removes exact rules for the pair.
func (rs *ModelConfigRules) DeleteExact(providerID, modelID string) *ModelConfigRules {
	out := make([]ModelConfigRule, 0, len(rs.rules))
	for _, rule := range rs.Rules() {
		if rule.IsExact() && rule.ProviderID == providerID && rule.ModelID == modelID {
			continue
		}
		out = append(out, rule)
	}
	return &ModelConfigRules{rules: out}
}

// RenameExactModel renames the exact rules for (provider, current) to next.
func (rs *ModelConfigRules) RenameExactModel(providerID, currentModelID, nextModelID string) *ModelConfigRules {
	if currentModelID == nextModelID {
		return rs
	}
	out := make([]ModelConfigRule, len(rs.rules))
	copy(out, rs.Rules())
	for i, rule := range out {
		if rule.IsExact() && rule.ProviderID == providerID && rule.ModelID == currentModelID {
			out[i].ModelID = nextModelID
		}
	}
	return &ModelConfigRules{rules: out}
}

// DeleteExactForProvider removes all exact rules of one provider.
func (rs *ModelConfigRules) DeleteExactForProvider(providerID string) *ModelConfigRules {
	out := make([]ModelConfigRule, 0, len(rs.rules))
	for _, rule := range rs.Rules() {
		if rule.IsExact() && rule.ProviderID == providerID {
			continue
		}
		out = append(out, rule)
	}
	return &ModelConfigRules{rules: out}
}

// builtinModelConfigRulesData / personalModelConfigRulesData are the wire
// shapes of toNexBuiltinJSON / toPersonalJSON.
type builtinModelConfigRulesData struct {
	ModelRules                []ModelConfigRule `json:"modelRules"`
	ModelAPIRules             []ModelConfigRule `json:"modelApiRules"`
	ProviderSiteRules         []ModelConfigRule `json:"providerSiteRules"`
	TemplateModelRules        []ModelConfigRule `json:"templateModelRules"`
	BuiltinProviderModelRules []ModelConfigRule `json:"builtinProviderModelRules"`
}

type personalModelConfigRulesData struct {
	ProviderModelRules       []ModelConfigRule `json:"providerModelRules"`
	ManualProviderModelRules []ModelConfigRule `json:"manualProviderModelRules"`
}

// ToNexBuiltinJSON renders the builtin wire shape (collected by type).
func (rs *ModelConfigRules) ToNexBuiltinJSON() builtinModelConfigRulesData {
	return builtinModelConfigRulesData{
		ModelRules:                rs.collect(RuleModel),
		ModelAPIRules:             rs.collect(RuleModelAPI),
		ProviderSiteRules:         rs.collect(RuleProviderSite),
		TemplateModelRules:        rs.collect(RuleTemplateModel),
		BuiltinProviderModelRules: rs.collect(RuleProviderModel),
	}
}

// ToPersonalJSON renders the personal wire shape.
func (rs *ModelConfigRules) ToPersonalJSON() personalModelConfigRulesData {
	return personalModelConfigRulesData{
		ProviderModelRules:       rs.collect(RuleProviderModel),
		ManualProviderModelRules: rs.collect(RuleManualProviderModel),
	}
}

func (rs *ModelConfigRules) collect(ruleType string) []ModelConfigRule {
	var out []ModelConfigRule
	for _, rule := range rs.Rules() {
		if rule.Type == ruleType {
			out = append(out, rule)
		}
	}
	return out
}

// ParseBuiltinModelConfigRules decodes the builtin wire shape (used by the
// nex-builtin release decode) assigning layer types.
func ParseBuiltinModelConfigRules(data json.RawMessage) (*ModelConfigRules, error) {
	if len(data) == 0 {
		return EmptyModelConfigRules(), nil
	}
	var wire builtinModelConfigRulesData
	if err := json.Unmarshal(data, &wire); err != nil {
		return nil, err
	}
	var rules []ModelConfigRule
	appendTyped := func(ruleType string, list []ModelConfigRule) {
		for _, rule := range list {
			rule.Type = ruleType
			rules = append(rules, rule)
		}
	}
	appendTyped(RuleModel, wire.ModelRules)
	appendTyped(RuleModelAPI, wire.ModelAPIRules)
	appendTyped(RuleProviderSite, wire.ProviderSiteRules)
	appendTyped(RuleTemplateModel, wire.TemplateModelRules)
	appendTyped(RuleProviderModel, wire.BuiltinProviderModelRules)
	return &ModelConfigRules{rules: rules}, nil
}

// ParsePersonalModelConfigRules decodes the personal wire shape.
func ParsePersonalModelConfigRules(data json.RawMessage) (*ModelConfigRules, error) {
	if len(data) == 0 {
		return EmptyModelConfigRules(), nil
	}
	var wire personalModelConfigRulesData
	if err := json.Unmarshal(data, &wire); err != nil {
		return nil, err
	}
	// 完整规则必须在编码边界再校验：同一 Provider/Model 不能同时声明智能和手动配置。
	smart := map[string]bool{}
	for _, rule := range wire.ProviderModelRules {
		smart[rule.ProviderID+"\x00"+rule.ModelID] = true
	}
	for _, rule := range wire.ManualProviderModelRules {
		if smart[rule.ProviderID+"\x00"+rule.ModelID] {
			return nil, &simpleError{"同一 Provider/Model 不能同时声明智能和手动配置"}
		}
	}
	var rules []ModelConfigRule
	for _, rule := range wire.ProviderModelRules {
		rule.Type = RuleProviderModel
		rules = append(rules, rule)
	}
	for _, rule := range wire.ManualProviderModelRules {
		rule.Type = RuleManualProviderModel
		rules = append(rules, rule)
	}
	return &ModelConfigRules{rules: rules}, nil
}

// normalizeBaseURLForRuleMatch ports normalizeBaseURLForRuleMatch: Host
// case, default port and trailing slashes must not change config hits; path
// and query keep their case.
func normalizeBaseURLForRuleMatch(value string) string {
	if value == "" {
		return ""
	}
	parsed, err := url.Parse(value)
	if err != nil {
		return ""
	}
	suffix := parsed.RawQuery
	if parsed.Fragment != "" {
		if suffix == "" {
			suffix = parsed.Fragment
		} else {
			suffix = parsed.RawQuery + "#" + parsed.Fragment
		}
	}
	endpoint := strings.TrimRight(parsed.String(), "/")
	// url.String keeps path+query; trailing-slash trim applies to the endpoint
	// part only, then the suffix is re-appended verbatim.
	if suffix != "" {
		endpoint = strings.TrimSuffix(parsed.String(), suffix)
		endpoint = strings.TrimRight(endpoint, "/") + suffix
	}
	return endpoint
}
