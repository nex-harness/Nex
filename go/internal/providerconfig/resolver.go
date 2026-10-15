// resolver.go ports packages/provider/src/resolver.ts + registry.ts + the
// owned-order helper. It produces, from builtin/personal/account provider
// layers + model rules, the effective provider list, the layered resolution
// (settings view) and the registry list (execution view).
package providerconfig

// ResolvedModel is one resolved model candidate (resolver.ts
// ResolvedProviderModelCandidate).
type ResolvedModel struct {
	ModelID                string
	Source                 string // "builtin" | "personal"
	Config                 *ModelConfig
	EffectiveBuiltinConfig *ModelConfig
	Issues                 []ValidationIssue
	Enabled                bool
	Executable             bool
	Selectable             bool
}

// ResolvedProvider is one provider with its resolved models.
type ResolvedProvider struct {
	ProviderID             string
	ProviderName           *string
	TemplateID             *string
	Enabled                bool
	Config                 *ProviderConfig
	TemplateConfig         *ProviderConfig
	EffectiveBuiltinConfig *ProviderConfig
	ProviderIssues         []ValidationIssue
	Models                 []ResolvedModel
}

// RegistryModel is a registry-executable model.
type RegistryModel struct {
	ModelID string
	Config  *ModelConfig
}

// RegistryProvider is a registry-executable provider.
type RegistryProvider struct {
	ProviderID   string
	ProviderName *string
	TemplateID   *string
	Config       *ProviderConfig
	Models       []RegistryModel
}

// ResolutionInput is ProviderConfigResolverInput.
type ResolutionInput struct {
	NexBuiltinProviders         *ProviderConfigMap
	NexBuiltinProviderTemplates *ProviderTemplateMap
	PersonalProviders           *ProviderConfigMap
	NexBuiltinModelRules        *ModelConfigRules
	PersonalModels              *ModelConfigRules
	AccountProviders            *ProviderConfigMap
	AccountStates               map[string]AccountProviderState
	PersonalProviderOrder       []string
}

// Resolution is ProviderConfigResolution.
type Resolution struct {
	EffectiveBuiltinProviders *ProviderConfigMap
	EffectiveProviders        *ProviderConfigMap
	ResolvedProviders         []ResolvedProvider
	RegistryProviders         []RegistryProvider
	Issues                    []ValidationIssue
}

// AccountProviderState ports AccountProviderState (availability facts).
type AccountProviderState struct {
	Availability      string  `json:"availability"`
	Entitled          bool    `json:"entitled"`
	UnavailableReason *string `json:"unavailableReason,omitempty"`
	Current           *bool   `json:"current,omitempty"`
	ConnectionKey     *string `json:"connectionKey,omitempty"`
	EffectiveAt       *int64  `json:"effectiveAt,omitempty"`
}

// Resolve ports ProviderConfigResolver.resolve. Layering order verbatim from
// resolver.ts: account (without group) over builtin concrete; template over
// that; personal (without group for builtin ids, template-resolved otherwise)
// over everything.
func Resolve(input ResolutionInput) *Resolution {
	builtin := input.NexBuiltinProviders
	if builtin == nil {
		builtin = EmptyProviderConfigMap()
	}
	templates := input.NexBuiltinProviderTemplates
	if templates == nil {
		templates = EmptyProviderTemplateMap()
	}
	personal := input.PersonalProviders
	if personal == nil {
		personal = EmptyProviderConfigMap()
	}
	// Account providers only exist over builtin ids and lose their group.
	accountProviders := EmptyProviderConfigMap()
	for _, rule := range input.AccountProviders.Rules() {
		if builtin.Has(rule.ProviderID) {
			rule.Config = rule.Config.WithoutGroup()
			accountProviders = accountProviders.SetRule(rule)
		}
	}
	concreteBuiltin := builtin.Overlay(accountProviders)
	effectiveBuiltin := concreteBuiltin.MapConfigs(func(config *ProviderConfig, _ string, rule *ProviderConfigRule) *ProviderConfig {
		if rule.TemplateID != nil {
			if tmpl := templates.Get(*rule.TemplateID); tmpl != nil {
				return tmpl.Config.Overlay(config)
			}
		}
		return config
	})
	personalProviders := personal.MapConfigs(func(config *ProviderConfig, providerID string, _ *ProviderConfigRule) *ProviderConfig {
		if effectiveBuiltin.Has(providerID) {
			return config.WithoutGroup()
		}
		return config
	})
	templatePersonal := personalProviders.MapConfigs(func(config *ProviderConfig, _ string, rule *ProviderConfigRule) *ProviderConfig {
		if rule.TemplateID != nil {
			if tmpl := templates.Get(*rule.TemplateID); tmpl != nil {
				return tmpl.Config.Overlay(config)
			}
		}
		return config
	})
	effectiveProviders := effectiveBuiltin.Overlay(templatePersonal)
	effectiveModelRules := input.NexBuiltinModelRules.ComposeEffective(input.PersonalModels)

	var issues []ValidationIssue
	var resolvedProviders []ResolvedProvider
	var registryProviders []RegistryProvider

	for _, providerID := range resolveProviderOrder(input, effectiveProviders) {
		rule := effectiveProviders.GetRule(providerID)
		if rule == nil || rule.Config == nil {
			continue
		}
		config := rule.Config
		// 账号不再支持总禁用；旧覆盖值不能让无开关的账号永久失效.
		enabled := true
		if config.Access != nil && config.Access.AccessType() == AccessZhipuAccount {
			enabled = true
		} else if rule.Enabled != nil {
			enabled = *rule.Enabled
		}
		providerIssues := validateProviderComplete(config, providerID)
		var templateConfig *ProviderConfig
		if rule.TemplateID != nil {
			templateConfig = templates.Get(*rule.TemplateID).PickConfig()
		}
		if rule.TemplateID != nil && templateConfig == nil {
			providerIssues = append(providerIssues, ValidationIssue{
				Code: "missing-template", Path: []string{"providers", providerID, "templateId"},
				Message: "Provider Template 不存在: " + *rule.TemplateID,
			})
		}
		issues = append(issues, providerIssues...)
		builtinModelIDs := uniqueInOrder(config.BuiltinModelIDs)
		builtinSet := stringSet(builtinModelIDs)
		var personalIDs []string
		for _, id := range uniqueInOrder(config.PersonalModelIDs) {
			if !builtinSet[id] {
				personalIDs = append(personalIDs, id)
			}
		}
		orderedModelIDs := OwnedOrder(builtinModelIDs, personalIDs, config.ModelOrder)
		accessEntitled := true
		if config.Access != nil && config.Access.AccessType() == AccessZhipuAccount && config.Access.ZhipuAccount != nil &&
			config.Access.ZhipuAccount.Entitled != nil {
			accessEntitled = *config.Access.ZhipuAccount.Entitled
		}
		// 账号权益与当前连接是两件事。非当前账号仍保留设置展示，不向普通 Registry 发布模型.
		accountCurrent := true
		if state, ok := input.AccountStates[providerID]; ok && state.Current != nil && !*state.Current {
			accountCurrent = false
		}
		providerExecutable := enabled && accessEntitled && accountCurrent && len(providerIssues) == 0
		models := make([]ResolvedModel, 0, len(orderedModelIDs))
		for _, modelID := range orderedModelIDs {
			modelConfig := effectiveModelRules.Resolve(RuleResolutionInput{
				ProviderID: providerID, TemplateID: deref(rule.TemplateID), ModelID: modelID,
				APIType: config.APITypeOf(),
				BaseURL: config.APIBaseURLOf(),
			})
			effectiveBuiltinModel := input.NexBuiltinModelRules.Resolve(RuleResolutionInput{
				ProviderID: providerID, TemplateID: deref(rule.TemplateID), ModelID: modelID,
				APIType: config.APITypeOf(),
				BaseURL: config.APIBaseURLOf(),
			})
			modelIssues := validateModelComplete(modelConfig, []string{"providers", providerID, "models", modelID})
			issues = append(issues, modelIssues...)
			modelEnabled := modelConfig.Enabled != nil && *modelConfig.Enabled
			executable := providerExecutable && modelEnabled && len(modelIssues) == 0
			selectable := executable && derefString(config.Visibility != nil, config.Visibility) != "hidden"
			source := "personal"
			if builtinSet[modelID] {
				source = "builtin"
			}
			models = append(models, ResolvedModel{
				ModelID: modelID, Source: source, Config: modelConfig,
				EffectiveBuiltinConfig: effectiveBuiltinModel, Issues: modelIssues,
				Enabled: modelEnabled, Executable: executable, Selectable: selectable,
			})
		}

		resolvedProviders = append(resolvedProviders, ResolvedProvider{
			ProviderID: providerID, Enabled: enabled, ProviderName: rule.ProviderName,
			TemplateID: rule.TemplateID, Config: config, TemplateConfig: templateConfig,
			EffectiveBuiltinConfig: effectiveBuiltin.Get(providerID),
			ProviderIssues:         providerIssues, Models: models,
		})

		if len(providerIssues) > 0 {
			continue
		}
		var validModels []RegistryModel
		for _, model := range models {
			if !model.Executable {
				continue
			}
			if extra := validateModelComplete(model.Config, []string{"providers", providerID, "models", model.ModelID}); len(extra) > 0 {
				// Registry Model 完整性结果不一致 — an invariant break, surface it.
				issues = append(issues, extra...)
				continue
			}
			validModels = append(validModels, RegistryModel{ModelID: model.ModelID, Config: model.Config})
		}
		if len(validModels) == 0 {
			continue
		}
		registryProviders = append(registryProviders, RegistryProvider{
			ProviderID: providerID, ProviderName: rule.ProviderName, TemplateID: rule.TemplateID,
			Config: config, Models: validModels,
		})
	}

	return &Resolution{
		EffectiveBuiltinProviders: effectiveBuiltin,
		EffectiveProviders:        effectiveProviders,
		ResolvedProviders:         resolvedProviders,
		RegistryProviders:         registryProviders,
		Issues:                    issues,
	}
}

// small helpers keeping deref noise out of the layering loop.
func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
func derefString(set bool, s *string) string {
	if !set || s == nil {
		return ""
	}
	return *s
}

// APIType / APIBaseURL on ProviderConfig (nil-safe).
func (c *ProviderConfig) APITypeOf() string    { return derefString(c.API != nil, c.API.Type) }
func (c *ProviderConfig) APIBaseURLOf() string { return derefString(c.API != nil, c.API.BaseURL) }

// PickConfig on template (avoids nil-literal branch above).
func (t *ProviderTemplate) PickConfig() *ProviderConfig {
	if t == nil {
		return nil
	}
	return t.Config
}

// resolveProviderOrder ports resolveProviderOrder: family group first, then
// builtin (minus family), then personal-only.
func resolveProviderOrder(input ResolutionInput, effectiveProviders *ProviderConfigMap) []string {
	var familyIDs []string
	for _, id := range effectiveProviders.Keys() {
		group := derefString(true, effectiveProviders.Get(id).Group)
		if group == GroupZaiFamily || group == GroupBigmodelFamily {
			familyIDs = append(familyIDs, id)
		}
	}
	familySet := stringSet(familyIDs)
	var builtinIDs []string
	for _, id := range input.NexBuiltinProviders.Keys() {
		if !familySet[id] {
			builtinIDs = append(builtinIDs, id)
		}
	}
	builtinSet := stringSet(builtinIDs)
	var personalIDs []string
	for _, id := range input.PersonalProviders.Keys() {
		if !builtinSet[id] && !familySet[id] {
			personalIDs = append(personalIDs, id)
		}
	}
	return append(append([]string{}, familyIDs...), OwnedOrder(builtinIDs, personalIDs, input.PersonalProviderOrder)...)
}
