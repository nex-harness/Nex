// legacylevel.go ports legacy-reasoning-level.ts +
// legacy-reasoning-level-renames.ts: 只为历史 Selection 恢复已知 Built-in 旧值域；
// 不迁移 Personal 配置或在执行层放宽校验.
package modelprovider

import (
	"github.com/tylinux/nex/go/internal/providerconfig"
)

// legacyReasoningLevelRenames is the 一次性改名清单 (exact selector matches).
var legacyReasoningLevelRenames = []struct {
	ModelMatch string
	OldLevel   string
}{
	{".*glm-5(?:[.\\-:/\\[].*)?", "off"},
	{".*GLM-5\\.2(?:[.\\-:/\\[].*)?", "nothink"},
	{".*GLM-5-Turbo(?:[.\\-:/\\[].*)?", "off"},
	{".*glm-5\\.1(?:[.\\-:/\\[].*)?", "off"},
	{".*glm-5\\.1-highspeed(?:[.\\-:/\\[].*)?", "off"},
	{".*glm-5v-turbo(?:[.\\-:/\\[].*)?", "off"},
	{".*glm-4\\.7(?:[.\\-:/\\[].*)?", "off"},
	{".*glm-4\\.7-flashx(?:[.\\-:/\\[].*)?", "off"},
	{".*glm-4\\.7-flash(?:[.\\-:/\\[].*)?", "off"},
	{".*glm-4\\.6(?:[.\\-:/\\[].*)?", "off"},
	{".*glm-4\\.5(?:[.\\-:/\\[].*)?", "off"},
	{".*glm-4\\.5-air(?:[.\\-:/\\[].*)?", "off"},
	{".*kimi-k2\\.7-code(?:[.\\-:/\\[].*)?", "off"},
	{".*kimi-k2\\.6(?:[.\\-:/\\[].*)?", "off"},
	{".*kimi-k2\\.5(?:[.\\-:/\\[].*)?", "off"},
	{".*deepseek-v4-flash(?:[.\\-:/\\[].*)?", "off"},
	{".*deepseek-v4-pro(?:[.\\-:/\\[].*)?", "off"},
	{".*qwen3\\.5-plus(?:[.\\-:/\\[].*)?", "off"},
	{".*qwen3\\.5-flash(?:[.\\-:/\\[].*)?", "off"},
	{".*qwen-plus(?:[.\\-:/\\[].*)?", "off"},
	{".*qwen-flash(?:[.\\-:/\\[].*)?", "off"},
	{".*qwen3-vl-plus(?:[.\\-:/\\[].*)?", "off"},
	{".*mimo-v2\\.5(?:[.\\-:/\\[].*)?", "off"},
	{".*mimo-v2\\.5-pro(?:[.\\-:/\\[].*)?", "off"},
	{".*mimo-v2-flash(?:[.\\-:/\\[].*)?", "off"},
}

// ResolveLegacyReasoningLevel ports resolveLegacyReasoningLevel, trimmed to
// the built-in rules (the snapshot cache in the TS exists to avoid re-mapping
// on every resolve; here the mapping is computed per call — the result is a
// pure function of the builtin rules and the selection).
func ResolveLegacyReasoningLevel(selection providerconfig.ModelSelection) string {
	oldLevel := ""
	if selection.Options != nil {
		oldLevel = selection.Options.ReasoningLevel
	}
	if oldLevel != "off" && oldLevel != "nothink" {
		return ""
	}
	// Personal manual rules opt out — the caller checks them against its own
	// snapshot before delegating here (this function is the pure leaf).
	return oldLevel
}

// ResolveLegacyReasoningLevelWithRules is the full port: given the builtin
// rules and personal exact-rule facts, resolve whether the old level maps to
// the current "disabled" value.
func ResolveLegacyReasoningLevelWithRules(
	builtinRules *providerconfig.ModelConfigRules,
	personalRule *providerconfig.ModelConfigRule,
	providerRule *providerconfig.ProviderConfigRule,
	selection providerconfig.ModelSelection,
) string {
	oldLevel := ""
	if selection.Options != nil {
		oldLevel = selection.Options.ReasoningLevel
	}
	if oldLevel != "off" && oldLevel != "nothink" {
		return ""
	}
	if personalRule != nil &&
		(personalRule.Type == providerconfig.RuleManualProviderModel ||
			(personalRule.Config != nil && personalRule.Config.OptionSpecs != nil && personalRule.Config.OptionSpecs.ReasoningLevel != nil &&
				(personalRule.Config.OptionSpecs.ReasoningLevel.Values != nil || personalRule.Config.OptionSpecs.ReasoningLevel.Map != nil))) {
		return ""
	}
	if providerRule == nil || providerRule.Config == nil {
		return ""
	}
	// 必须是已裁决的原始无站点/无 API 限制规则；新增同名站点规则不能误继承旧别名.
	oldRules := mapBuiltinRulesWithRenames(builtinRules)
	apiType, baseURL := "", ""
	if providerRule.Config.API != nil {
		if providerRule.Config.API.Type != nil {
			apiType = *providerRule.Config.API.Type
		}
		if providerRule.Config.API.BaseURL != nil {
			baseURL = *providerRule.Config.API.BaseURL
		}
	}
	templateID := ""
	if providerRule.TemplateID != nil {
		templateID = *providerRule.TemplateID
	}
	resolved := oldRules.Resolve(providerconfig.RuleResolutionInput{
		ProviderID: selection.ProviderID, TemplateID: templateID, ModelID: selection.ModelID,
		APIType: apiType, BaseURL: baseURL,
	})
	if resolved == nil || resolved.OptionSpecs == nil || resolved.OptionSpecs.ReasoningLevel == nil {
		return ""
	}
	if containsStr(resolved.OptionSpecs.ReasoningLevel.Values, oldLevel) {
		return "disabled"
	}
	return ""
}

func mapBuiltinRulesWithRenames(builtin *providerconfig.ModelConfigRules) *providerconfig.ModelConfigRules {
	rules := builtin.Rules()
	out := make([]providerconfig.ModelConfigRule, 0, len(rules))
	for _, rule := range rules {
		if rule.Type != providerconfig.RuleModel {
			out = append(out, rule)
			continue
		}
		rename := ""
		for _, entry := range legacyReasoningLevelRenames {
			if entry.ModelMatch == rule.ModelMatch {
				rename = entry.OldLevel
				break
			}
		}
		values := []string(nil)
		if rule.Config != nil && rule.Config.OptionSpecs != nil && rule.Config.OptionSpecs.ReasoningLevel != nil {
			values = rule.Config.OptionSpecs.ReasoningLevel.Values
		}
		if rename == "" || !containsStr(values, "disabled") {
			out = append(out, rule)
			continue
		}
		mapped := make([]string, len(values))
		for i, v := range values {
			if v == "disabled" {
				mapped[i] = rename
			} else {
				mapped[i] = v
			}
		}
		config := rule.Config.Overlay(&providerconfig.ModelConfig{
			OptionSpecs: &providerconfig.ModelOptionSpecs{
				ReasoningLevel: &providerconfig.EnumOptionSpec{Values: mapped},
			},
		})
		rule.Config = config
		out = append(out, rule)
	}
	return providerconfig.NewModelConfigRules(out)
}
