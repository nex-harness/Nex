// legacynexconfig.go ports legacyNexConfigProviderReader.ts (the reader of
// the published old ~/.nex/v2/config.json provider block): reads the
// OpenCode-shaped provider entries, normalizes them into the legacy store
// shape the personal importer consumes, and filters what may migrate
// (builtin:*/account:* never leak into the personal registry; retired ZAPI
// and unused default-* presets are dropped; deleted model tombstones are
// removed for read).
package modelprovider

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"strings"
)

// retiredZapiProviderID mirrors the TS constant.
const retiredZapiProviderID = "builtin:zapi"

// legacyModelProviderKinds.
const (
	kindAnthropic        = "anthropic"
	kindOpenAI           = "openai"
	kindOpenAICompatible = "openai-compatible"
)

// nexOpenCodeModelConfig is the model entry of the legacy config.json.
type nexOpenCodeModelConfig struct {
	ID        *string         `json:"id"`
	Name      *string         `json:"name"`
	Reasoning json.RawMessage `json:"reasoning"`
	Limit     *struct {
		Context *float64 `json:"context"`
		Input   *float64 `json:"input"`
		Output  *float64 `json:"output"`
	} `json:"limit"`
	ContextWindow   *float64 `json:"contextWindow"`
	MaxOutputTokens *float64 `json:"maxOutputTokens"`
	Modalities      *struct {
		Input  []string `json:"input"`
		Output []string `json:"output"`
	} `json:"modalities"`
	Options                  map[string]any     `json:"options"`
	Headers                  map[string]string  `json:"headers"`
	DisabledReason           *string            `json:"disabledReason"`
	SupportsTools            *bool              `json:"supportsTools"`
	SupportsStructuredOutput *bool              `json:"supportsStructuredOutput"`
	Priority                 *float64           `json:"priority"`
	Modified                 *bool              `json:"modified"`
	Deleted                  *bool              `json:"deleted"`
	Nex                      *nexModelExtension `json:"nex"`
}

type nexModelExtension struct {
	Kinds                    []string               `json:"kinds"`
	DefaultKind              *string                `json:"defaultKind"`
	DisabledReason           *string                `json:"disabledReason"`
	SupportsTools            *bool                  `json:"supportsTools"`
	SupportsStructuredOutput *bool                  `json:"supportsStructuredOutput"`
	Priority                 *float64               `json:"priority"`
	Modified                 *bool                  `json:"modified"`
	Deleted                  *bool                  `json:"deleted"`
	Reasoning                *nexReasoningExtension `json:"reasoning"`
}

type nexReasoningExtension struct {
	Enabled        *bool    `json:"enabled"`
	Variants       []string `json:"variants"`
	DefaultVariant *string  `json:"defaultVariant"`
}

// nexOpenCodeProviderConfig is the provider entry.
type nexOpenCodeProviderConfig struct {
	API            *string                           `json:"api"`
	Name           *string                           `json:"name"`
	Kind           *string                           `json:"kind"`
	NPM            *string                           `json:"npm"`
	Options        map[string]any                    `json:"options"`
	Enabled        *bool                             `json:"enabled"`
	Endpoints      *legacyEndpoints                  `json:"endpoints"`
	APIFormat      *string                           `json:"apiFormat"`
	Source         *string                           `json:"source"`
	APIKeyRequired *bool                             `json:"apiKeyRequired"`
	Headers        map[string]string                 `json:"headers"`
	LogoURL        *string                           `json:"logoUrl"`
	APIKeyURL      *string                           `json:"apiKeyUrl"`
	DefaultKind    *string                           `json:"defaultKind"`
	CreatedAt      *float64                          `json:"createdAt"`
	UpdatedAt      *float64                          `json:"updatedAt"`
	Models         map[string]nexOpenCodeModelConfig `json:"models"`
	Nex            *nexProviderExtension             `json:"nex"`
}

type nexProviderExtension struct {
	Enabled        *bool             `json:"enabled"`
	Endpoints      *legacyEndpoints  `json:"endpoints"`
	APIFormat      *string           `json:"apiFormat"`
	Source         *string           `json:"source"`
	APIKeyRequired *bool             `json:"apiKeyRequired"`
	Headers        map[string]string `json:"headers"`
	LogoURL        *string           `json:"logoUrl"`
	APIKeyURL      *string           `json:"apiKeyUrl"`
	DefaultKind    *string           `json:"defaultKind"`
	CreatedAt      *float64          `json:"createdAt"`
	UpdatedAt      *float64          `json:"updatedAt"`
	DeletedModels  []string          `json:"deletedModels"`
}

// legacyEndpoints is ModelProviderEndpoints (baseURL + paths + old
// anthropic/openai entries).
type legacyEndpoints struct {
	BaseURL   string            `json:"baseURL"`
	Paths     map[string]string `json:"paths"`
	Anthropic string            `json:"anthropic"`
	OpenAI    string            `json:"openai"`
}

// ReadLegacyNexConfigProviders ports readLegacyNexConfigProviders: read the
// file at path (nil when absent), normalize each provider entry, drop what
// must not migrate. IO errors other than ENOENT fail loudly (旧读取器把所有
// 失败当作文件不存在，Importer 会因此永久提交空 Personal 配置).
func ReadLegacyNexConfigProviders(path string) ([]LegacyModelProviderConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("旧 Provider 配置读取失败 stage=io path=%s code=%v", path, err)
	}
	var file struct {
		Provider map[string]nexOpenCodeProviderConfig `json:"provider"`
	}
	if err := json.Unmarshal(data, &file); err != nil {
		// JSON/Zod 原始错误可能包含 API Key 等输入值；不能写入日志或通过 cause 向外传递.
		return nil, fmt.Errorf("旧 Provider 配置解析失败 stage=json path=%s", path)
	}
	if file.Provider == nil {
		return nil, nil
	}
	providerIDs := make([]string, 0, len(file.Provider))
	for id := range file.Provider {
		providerIDs = append(providerIDs, id)
	}
	sortStrings(providerIDs)
	var out []LegacyModelProviderConfig
	for _, providerID := range providerIDs {
		provider := file.Provider[providerID]
		config := openCodeProviderToModelProviderConfig(providerID, provider)
		// Nex has no built-in providers: entries inherited from the ZCode
		// config (builtin:* templates/enabled states) must not leak into the
		// personal provider registry during migration.
		if strings.HasPrefix(config.ID, "builtin:") {
			continue
		}
		if config.ID == retiredZapiProviderID {
			continue
		}
		// default-* 预置模板不随产品分发：空 API Key 表示用户从未启用该供应商.
		if strings.HasPrefix(config.ID, "default-") && strings.TrimSpace(config.APIKey) == "" {
			continue
		}
		config.Models = filterDeletedModels(config.Models)
		out = append(out, config)
	}
	return out, nil
}

// openCodeProviderToModelProviderConfig ports openCodeProviderToModelProviderConfig.
func openCodeProviderToModelProviderConfig(providerID string, provider nexOpenCodeProviderConfig) LegacyModelProviderConfig {
	preferOpenCodeFields := hasOpenCodeProviderRuntimeFields(provider)
	configuredDefaultKind := resolveOpenCodeProviderDefaultKind(provider)
	options := provider.Options
	if options == nil {
		options = map[string]any{}
	}
	configuredRuntimeBaseURL := firstNonEmptyStr(readStringOption(options, "baseURL"), deref(provider.API))
	defaultKind := orDefault(resolvePresetProviderKindFromRuntimeBaseURL(providerID, configuredRuntimeBaseURL), configuredDefaultKind)
	endpoints := resolveOpenCodeProviderEndpoints(provider, defaultKind, providerID)
	apiKey := readStringOption(options, "apiKey")
	models := make([]LegacyModelEntry, 0, len(provider.Models))
	modelIDs := make([]string, 0, len(provider.Models))
	for modelID, model := range provider.Models {
		models = append(models, LegacyModelEntry{Config: openCodeModelToEntry(modelID, model, defaultKind, preferOpenCodeFields)})
		modelIDs = append(modelIDs, modelIDs...)
	}
	_ = modelIDs
	// deleted-model tombstones
	existing := map[string]bool{}
	for _, entry := range models {
		if entry.Config != nil {
			existing[strings.ToLower(entry.Config.ID)] = true
		}
	}
	if provider.Nex != nil {
		for _, deleted := range normalizeDeletedModelIDs(provider.Nex.DeletedModels) {
			key := strings.ToLower(deleted)
			if existing[key] {
				continue
			}
			deletedTrue := true
			models = append(models, LegacyModelEntry{Config: &LegacyModelConfig{ID: deleted, Deleted: &deletedTrue}})
			existing[key] = true
		}
	}
	config := LegacyModelProviderConfig{
		ID:        providerID,
		Name:      firstNonEmptyStr(deref(provider.Name), providerID),
		APIKey:    apiKey,
		Headers:   firstNonNil(provider.Headers, providerHeadersOf(provider.Nex), readStringRecordOption(options, "headers")),
		APIFormat: apiFormatOf(provider, preferOpenCodeFields, defaultKind),
		Source:    firstNonNilStr(provider.Source, sourceOf(provider.Nex), strPtr("custom")),
		Endpoints: struct {
			BaseURL string `json:"baseURL"`
		}{BaseURL: endpoints.BaseURL},
		Models: models,
	}
	if provider.Enabled != nil {
		config.Enabled = provider.Enabled
	} else if provider.Nex != nil && provider.Nex.Enabled != nil {
		config.Enabled = provider.Nex.Enabled
	}
	if provider.APIKeyURL != nil {
		config.APIKeyURL = *provider.APIKeyURL
	} else if provider.Nex != nil && provider.Nex.APIKeyURL != nil {
		config.APIKeyURL = *provider.Nex.APIKeyURL
	}
	return config
}

// openCodeModelToEntry ports openCodeModelToModelProviderModel (the subset
// the personal importer consumes: id, deleted, contextWindow).
func openCodeModelToEntry(modelID string, model nexOpenCodeModelConfig, providerDefaultKind string, preferOpenCodeFields bool) *LegacyModelConfig {
	nex := model.Nex
	if preferOpenCodeFields {
		nex = nil
	}
	modelIDEntry := modelID
	if model.ID != nil && strings.TrimSpace(*model.ID) != "" {
		modelIDEntry = strings.TrimSpace(*model.ID)
	}
	out := &LegacyModelConfig{ID: modelIDEntry}
	contextWindow := firstPositiveFloat(readLimit(model.Limit, "context"), derefFloat(model.ContextWindow))
	if contextWindow != nil {
		out.ContextWindow = contextWindow
	}
	deleted := firstNonNilBool(model.Deleted, deletedOf(nex))
	if deleted != nil {
		out.Deleted = deleted
	}
	return out
}
