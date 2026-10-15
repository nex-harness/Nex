// confighelpers.go: the pure helpers of ProviderConfigService
// (config-service.ts privates): membership CAS, template baselines,
// id/label generation, order normalization.
package modelprovider

import (
	"errors"
	"fmt"
	"strings"

	"github.com/tylinux/nex/go/internal/providerconfig"
)

// helpers (all pure ports of the config-service.ts privates)

func assertMembershipCurrent(membership *ModelMembership, providerID string, current *providerconfig.LayerSnapshot) error {
	if membership == nil {
		return nil
	}
	if err := membership.AssertCurrent(); err != nil {
		return err
	}
	if membership.ProviderID != providerID || membership.PersonalRevision != current.Revision {
		return errors.New("Provider Settings membership revision conflict")
	}
	return nil
}

// writableProviderOverlay: Personal 根记录只是覆盖层；仅继承 Provider 可按需创建覆盖.
func writableProviderOverlay(builtin *BuiltinConfigSnapshot, current *providerconfig.LayerSnapshot, providerID string) *providerconfig.ProviderConfig {
	provider := current.Providers.Get(providerID)
	if provider != nil {
		return provider
	}
	if builtin.Providers.Has(providerID) {
		return &providerconfig.ProviderConfig{}
	}
	// 已删除的自定义/模板实例没有继承 Provider 身份，迟到操作不能把它复活.
	panic(fmt.Sprintf("Provider 不存在: %s", providerID))
}

func resolveProviderBuiltinModelIDs(builtin *BuiltinConfigSnapshot, personal *providerconfig.ProviderConfigMap, providerID string) []string {
	if config := builtin.Providers.Get(providerID); config != nil {
		return orStrings(config.BuiltinModelIDs)
	}
	if baseline := resolveTemplateBaseline(builtin, personal, providerID); baseline != nil {
		return orStrings(baseline.BuiltinModelIDs)
	}
	return nil
}

func resolveTemplateBaseline(builtin *BuiltinConfigSnapshot, providers *providerconfig.ProviderConfigMap, providerID string) *providerconfig.ProviderConfig {
	rule := providers.GetRule(providerID)
	if rule == nil || rule.TemplateID == nil {
		return nil
	}
	if tmpl := builtin.ProviderTemplates.Get(*rule.TemplateID); tmpl != nil {
		return tmpl.Config
	}
	return nil
}

func resolvePersonalBaselines(builtin *BuiltinConfigSnapshot, personal *providerconfig.ProviderConfigMap) *providerconfig.ProviderConfigMap {
	personalOverlaid := personal.MapConfigs(func(config *providerconfig.ProviderConfig, _ string, rule *providerconfig.ProviderConfigRule) *providerconfig.ProviderConfig {
		if rule.TemplateID != nil {
			if tmpl := builtin.ProviderTemplates.Get(*rule.TemplateID); tmpl != nil {
				return tmpl.Config.Overlay(config)
			}
		}
		return config
	})
	return builtin.Providers.Overlay(personalOverlaid)
}

// normalizePersonalMembership ports normalizePersonalProviderMembership.
func normalizePersonalMembership(personal, builtin *providerconfig.ProviderConfig, inheritedModelIDs []string) *providerconfig.ProviderConfig {
	if personal == nil {
		return nil
	}
	source := inheritedModelIDs
	if source == nil && builtin != nil {
		source = orStrings(builtin.BuiltinModelIDs)
	}
	builtinModelIDs := uniqueStrings(source)
	builtinSet := stringSetOf(builtinModelIDs)
	personalModelIDs := make([]string, 0, len(orStrings(personal.PersonalModelIDs)))
	for _, id := range uniqueStrings(orStrings(personal.PersonalModelIDs)) {
		if !builtinSet[id] {
			personalModelIDs = append(personalModelIDs, id)
		}
	}
	normalized := personal.WithPersonalModelIDs(personalModelIDs)
	if personal.ModelOrder != nil {
		normalized = normalized.WithModelOrder(providerconfig.OwnedOrder(builtinModelIDs, personalModelIDs, personal.ModelOrder))
	}
	return normalized
}

func nextPersonalProviderID(occupied map[string]bool, templateID string) string {
	base := "new-provider"
	if templateID != "" {
		base = normalizeProviderIDSeed(templateID)
	}
	if !occupied[base] {
		return base
	}
	for suffix := 2; ; suffix++ {
		candidate := fmt.Sprintf("%s-%d", base, suffix)
		if !occupied[candidate] {
			return candidate
		}
	}
}

func normalizeProviderIDSeed(value string) string {
	var b strings.Builder
	lastDash := true
	for _, r := range strings.ToLower(strings.TrimSpace(value)) {
		isAlnum := (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9')
		if isAlnum {
			b.WriteRune(r)
			lastDash = false
		} else if !lastDash {
			b.WriteByte('-')
			lastDash = true
		}
	}
	seed := strings.Trim(b.String(), "-")
	if seed == "" {
		return "new-provider"
	}
	return seed
}

func nextPersonalProviderLabel(seed string, providers *providerconfig.ProviderConfigMap) string {
	base := strings.TrimSpace(seed)
	if base == "" {
		base = "new-provider"
	}
	labels := map[string]bool{}
	for _, rule := range providers.Rules() {
		if rule.ProviderName != nil && strings.TrimSpace(*rule.ProviderName) != "" {
			labels[strings.ToLower(strings.TrimSpace(*rule.ProviderName))] = true
		}
	}
	if !labels[strings.ToLower(base)] {
		return base
	}
	for suffix := 2; ; suffix++ {
		candidate := fmt.Sprintf("%s %d", base, suffix)
		if !labels[strings.ToLower(candidate)] {
			return candidate
		}
	}
}

func appendCurrentProviderOrder(builtin, personal *providerconfig.ProviderConfigMap, currentOrder []string, addedProviderID string) []string {
	var personalIDs []string
	for _, id := range personal.Keys() {
		if deref(personal.Get(id).Group) == providerconfig.GroupStandardPersonal {
			personalIDs = append(personalIDs, id)
		}
	}
	current := providerconfig.OwnedOrder(nil, personalIDs, orEmpty(currentOrder))
	requested := make([]string, 0, len(current)+1)
	for _, id := range current {
		if id != addedProviderID {
			requested = append(requested, id)
		}
	}
	requested = append(requested, addedProviderID)
	return providerconfig.OwnedOrder(nil, personalIDs, requested)
}

func normalizeID(label, value string) (string, error) {
	normalized := strings.TrimSpace(value)
	if normalized == "" {
		return "", fmt.Errorf("%s 不能为空", label)
	}
	return normalized, nil
}

func normalizeTwoIDs(firstLabel, first, secondLabel, second string) (string, string, error) {
	a, err := normalizeID(firstLabel, first)
	if err != nil {
		return "", "", err
	}
	b, err := normalizeID(secondLabel, second)
	if err != nil {
		return "", "", err
	}
	return a, b, nil
}
