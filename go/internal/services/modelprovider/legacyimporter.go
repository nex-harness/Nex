// legacyimporter.go ports legacyPersonalProviderConfigImporter.ts: 从旧
// Effective Store 中只提取仍能确认的 Personal 用户意图 — only custom
// providers, their membership order and context windows survive; builtin:*
// and account:* identities are rebuilt by the current sources.
package modelprovider

import (
	"strings"

	"github.com/tylinux/nex/go/internal/providerconfig"
)

// LegacyModelProviderConfig is the legacyModelProviderSerialized.ts shape the
// importer consumes (only the fields it reads).
type LegacyModelProviderConfig struct {
	ID        string            `json:"id"`
	Name      string            `json:"name"`
	APIKey    string            `json:"apiKey"`
	APIKeyURL string            `json:"apiKeyUrl"`
	Enabled   *bool             `json:"enabled"`
	Source    *string           `json:"source"`
	APIFormat *string           `json:"apiFormat"`
	Headers   map[string]string `json:"headers"`
	Endpoints struct {
		BaseURL string `json:"baseURL"`
	} `json:"endpoints"`
	Models []LegacyModelEntry `json:"models"`
}

// LegacyModelEntry is string | object.
type LegacyModelEntry struct {
	Raw    string
	Config *LegacyModelConfig
}

// LegacyModelConfig is the object model entry.
type LegacyModelConfig struct {
	ID            string `json:"id"`
	Deleted       *bool  `json:"deleted"`
	ContextWindow *int64 `json:"contextWindow"`
}

// UnmarshalJSON accepts string or object entries.
func (e *LegacyModelEntry) UnmarshalJSON(data []byte) error {
	if len(data) > 0 && data[0] == '"' {
		return jsonUnmarshalHelper(data, &e.Raw)
	}
	e.Config = &LegacyModelConfig{}
	return jsonUnmarshalHelper(data, e.Config)
}

// builtin template associations (BUILTIN_PROVIDER_TEMPLATE_IDS).
const (
	templateBigmodel = "bigmodel"
	templateZai      = "zai"
)

// ImportLegacyPersonalProviderConfig ports importLegacyPersonalProviderConfig.
func ImportLegacyPersonalProviderConfig(legacyProviders []LegacyModelProviderConfig) *providerconfig.LayerUpdate {
	providers := providerconfig.EmptyProviderConfigMap()
	models := providerconfig.EmptyModelConfigRules()
	for _, legacy := range legacyProviders {
		providerID := strings.TrimSpace(legacy.ID)
		if providerID == "" {
			continue
		}
		// 已发布 config.json 也把 builtin:* 标成 custom；保留身份必须先于 source.
		// 只有按量 API 的 Key 是用户输入，模板关联使用当前身份.
		apiTemplateID := ""
		switch providerID {
		case "builtin:bigmodel":
			apiTemplateID = templateBigmodel
		case "builtin:zai":
			apiTemplateID = templateZai
		}
		if apiTemplateID != "" {
			apiKey := strings.TrimSpace(legacy.APIKey)
			if apiKey != "" {
				providers = providers.SetRule(providerconfig.ProviderConfigRule{
					ProviderID: apiTemplateID,
					TemplateID: strPtr(apiTemplateID),
					Config: &providerconfig.ProviderConfig{
						Group:  strPtr(providerconfig.GroupStandardPersonal),
						Access: &providerconfig.ProviderAccess{APIKey: &providerconfig.ApiKeyAccessConfig{Type: providerconfig.AccessAPIKey, APIKey: strPtr(apiKey)}},
					},
				})
			}
			continue
		}
		if strings.HasPrefix(providerID, "builtin:") || strings.HasPrefix(providerID, "account:") {
			continue
		}
		// Built-in 整体由当前 Nex Built-in Config 与 Account Overlay 重建；只允许旧
		// 自定义 Provider 进入新文件.
		if legacy.Source != nil && *legacy.Source != "custom" {
			continue
		}
		members := collectLegacyPersonalModelMembers(legacy.Models)
		modelIDs := make([]string, 0, len(members))
		for _, member := range members {
			modelIDs = append(modelIDs, member.ModelID)
		}
		rule := providerconfig.ProviderConfigRule{
			ProviderID: providerID,
			Config:     createPersonalProviderConfig(legacy, modelIDs),
		}
		// 旧启停是用户意图；遗漏 false 会被统一默认值重新启用，必须保留在规则外层.
		if legacy.Enabled != nil {
			rule.Enabled = legacy.Enabled
		}
		if name := strings.TrimSpace(legacy.Name); name != "" && name != providerID {
			rule.ProviderName = &name
		}
		providers = providers.SetRule(rule)
		for _, member := range members {
			if member.ContextWindow == nil {
				continue
			}
			var err error
			models, err = models.SetExact(providerID, member.ModelID, &providerconfig.ModelConfig{
				Properties: &providerconfig.ModelProperties{ContextWindow: member.ContextWindow},
			}, nil)
			if err != nil {
				continue
			}
		}
	}
	return &providerconfig.LayerUpdate{Providers: providers, Models: models}
}

func createPersonalProviderConfig(legacy LegacyModelProviderConfig, modelIDs []string) *providerconfig.ProviderConfig {
	config := &providerconfig.ProviderConfig{
		Group: strPtr(providerconfig.GroupStandardPersonal),
		Access: &providerconfig.ProviderAccess{APIKey: &providerconfig.ApiKeyAccessConfig{
			Type: providerconfig.AccessAPIKey,
		}},
		API: &providerconfig.ProviderApiConfig{
			Type:    legacyAPIFormat(legacy),
			Headers: legacy.Headers,
		},
	}
	if key := strings.TrimSpace(legacy.APIKey); key != "" {
		config.Access.APIKey.APIKey = strPtr(key)
	}
	if legacy.APIKeyURL != "" {
		config.Access.APIKey.APIKeyManagementURL = strPtr(legacy.APIKeyURL)
	}
	if baseURL := resolveLegacyRuntimeBaseURL(legacy); baseURL != "" {
		config.API.BaseURL = strPtr(baseURL)
	} else if configured := strings.TrimSpace(legacy.Endpoints.BaseURL); configured != "" {
		config.API.BaseURL = strPtr(strings.TrimRight(configured, "/"))
	}
	if len(modelIDs) > 0 {
		config.PersonalModelIDs = modelIDs
		config.ModelOrder = modelIDs
	}
	return config
}

// legacyAPIFormat ports resolveModelProviderApiFormat for the importer's
// subset: apiFormat when present, else derived from kind.
func legacyAPIFormat(legacy LegacyModelProviderConfig) *string {
	if legacy.APIFormat != nil && *legacy.APIFormat != "" {
		switch *legacy.APIFormat {
		case "anthropic", "anthropic-messages":
			return strPtr(providerconfig.APIAnthropicMessages)
		case "openai", "openai-chat-completions", "openai-compatible":
			return strPtr(providerconfig.APIOpenAIChatCompletions)
		case "openai-responses":
			return strPtr(providerconfig.APIOpenAIResponses)
		}
	}
	return nil
}

// resolveLegacyRuntimeBaseURL ports resolveModelProviderRuntimeBaseUrl for
// the importer's subset: the configured baseURL only.
func resolveLegacyRuntimeBaseURL(legacy LegacyModelProviderConfig) string {
	return strings.TrimRight(strings.TrimSpace(legacy.Endpoints.BaseURL), "/")
}

type legacyPersonalModelMember struct {
	ModelID       string
	ContextWindow *int64
}

func collectLegacyPersonalModelMembers(entries []LegacyModelEntry) []legacyPersonalModelMember {
	seen := map[string]bool{}
	members := make([]legacyPersonalModelMember, 0, len(entries))
	for _, entry := range entries {
		modelID := strings.TrimSpace(entry.Raw)
		if entry.Config != nil {
			modelID = strings.TrimSpace(entry.Config.ID)
		}
		if modelID == "" || seen[modelID] {
			continue
		}
		if entry.Config != nil && entry.Config.Deleted != nil && *entry.Config.Deleted {
			continue
		}
		seen[modelID] = true
		member := legacyPersonalModelMember{ModelID: modelID}
		if entry.Config != nil && entry.Config.ContextWindow != nil && *entry.Config.ContextWindow > 0 {
			member.ContextWindow = entry.Config.ContextWindow
		}
		members = append(members, member)
	}
	return members
}
