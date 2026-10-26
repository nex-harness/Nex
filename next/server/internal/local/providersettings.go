package local

// Server-owned `provider-settings` (spec docs/next/config-mcp-skills.md decision
// 6; research §A1/C5).
//
// The browser UI is unchanged, so the views below reproduce the shapes the
// `@nex/provider` facades return for a registry built from personal providers:
//
//	ProviderSettingsView {revision, providerTemplates, providerOrder, providers[]}
//	ModelSelectionView  {revision, providers[], preferredSelection?, effectiveSelection?,
//	                     selectionIssue?}
//
// Source of truth: the desired ConfigDoc (effective layer for the target runner)
// overlaid with runner facts (`configApplied.facts.modelsByProvider`: the models
// pi actually exposes, with their real thinking levels and auth state). Secret
// values never appear; `access.apiKey` is a presentation mask or empty.
//
// Writes compile the UI's sparse objects into the strict pi shape at write time
// (configstore.ValidateDoc) and bump the doc version, which pushes to runners.

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/nex-harness/nex/server/internal/configstore"
)

// ReasonationLevels are the values the composer's option validator accepts.
const reasoningMap = `{"reasoning_effort": reasoningLevel}`
const maxOutputMap = `{"max_tokens": maxOutputTokens}`

const defaultContextWindow = 200000
const defaultMaxTokens = 8192

// engineAPI maps a doc API type to the UI's provider API format.
func engineAPI(api string) string {
	switch api {
	case configstore.APITypeOpenAIChatCompletions:
		return configstore.APITypeOpenAIChatCompletionsEngine
	case configstore.APITypeOpenAIResponses:
		return "openai-responses"
	default:
		return "anthropic-messages"
	}
}

// piAPI maps the UI's provider API format onto pi's model API ids.
func piAPI(api string) (string, error) {
	switch api {
	case configstore.APITypeAnthropicMessages:
		return "anthropic-messages", nil
	case configstore.APITypeOpenAIChatCompletions, configstore.APITypeOpenAIChatCompletionsEngine:
		return configstore.APITypeOpenAIChatCompletions, nil
	case configstore.APITypeOpenAIResponses, "azure-openai-responses", "openai-codex-responses":
		return "openai-responses", nil
	}
	return "", fmt.Errorf("unsupported api type %q (use anthropic-messages, openai-chat-completions or openai-responses)", api)
}

// ProviderSettings serves the provider-settings channel from the config doc.
type ProviderSettings struct {
	manager *Manager
}

func newProviderSettings(manager *Manager) *ProviderSettings {
	return &ProviderSettings{manager: manager}
}

// viewTarget reads an optional {workspacePath, workspaceIdentity} argument.
type viewTarget struct {
	WorkspacePath     string
	WorkspaceIdentity string
	// RunnerID is an explicit runner override (config import/HTTP paths).
	RunnerID string
}

// runnerFor resolves the machine whose facts overlay the view: an explicit
// runnerId, then a `runner:<id>:<path>` identity, then the first connected
// runner (the doc itself is global).
func (p *ProviderSettings) view(target viewTarget) (map[string]any, error) {
	doc, err := p.manager.store.Snapshot()
	if err != nil {
		return nil, err
	}
	runnerID := p.runnerFor(target)
	effective := doc.EffectiveForRunner(runnerID)
	facts, factsRunner := runnerModelFacts(p.manager, runnerID)
	secrets := p.manager.store.Secrets()
	providers := []any{}
	order := []string{}
	for _, provider := range effective.ProviderList() {
		view := p.providerView(provider, facts, factsRunner, secrets)
		providers = append(providers, view)
		order = append(order, provider.ID)
	}
	return map[string]any{
		"revision":          doc.Version,
		"providerTemplates": []any{},
		"providerOrder":     order,
		"providers":         providers,
	}, nil
}

func (p *ProviderSettings) providerView(
	provider *configstore.Provider,
	facts map[string]runnerProviderFacts,
	factsRunner string,
	secrets *configstore.SecretBox,
) map[string]any {
	auth := provideAuthStateFor(secrets, provider, facts, factsRunner != "")
	lastIssue := auth.problem
	configured := auth.configured
	executable := provider.Enabled && configured && len(provider.Models) > 0
	credential := "configured"
	if !configured {
		credential = lastIssue
	}
	apiFormat := engineAPI(provider.API)
	apiKeyValue := ""
	if auth.hint != "" {
		apiKeyValue = auth.hint
	}
	issues := []any{}
	if lastIssue != "" {
		issues = append(issues, map[string]any{
			"path":    []string{"providers", provider.ID},
			"message": lastIssue,
			"code":    "runner-apply-failed",
		})
	}
	models := make([]any, 0, len(provider.Models))
	for index := range provider.Models {
		model := provider.Models[index]
		fact, hasFact := facts[provider.ID]
		levels := model.ReasoningLevels
		if hasFact {
			if reported, ok := fact.Models[model.ID]; ok {
				if len(reported.ReasoningLevels) > 0 {
					levels = reported.ReasoningLevels
				}
			}
		}
		modelExecutable := executable && model.Enabled
		models = append(models, map[string]any{
			"kind":                   "candidate",
			"modelId":                model.ID,
			"builtin":                false,
			"effectiveBuiltinConfig": map[string]any{},
			"personalExactConfig":    modelConfigObject(model, levels),
			"useRecommendedConfig":   false,
			"effectiveConfig":        modelConfigObject(model, levels),
			"enabled":                model.Enabled,
			"executable":             modelExecutable,
			"selectable":             modelExecutable,
			"issues":                 []any{},
		})
	}
	effectiveConfig := map[string]any{
		"group": "standard-personal",
		"access": map[string]any{
			"type":       "api-key",
			"apiKey":     apiKeyValue,
			"apiKeyHint": credential,
		},
		"api": map[string]any{
			"type":    apiFormat,
			"baseUrl": provider.BaseURL,
		},
		"visibility": "visible",
	}
	if len(provider.Headers) > 0 {
		effectiveConfig["api"].(map[string]any)["headers"] = provider.Headers
	}
	personalConfig := map[string]any{
		"group":  "standard-personal",
		"access": map[string]any{"type": "api-key", "apiKey": apiKeyValue},
		"api":    map[string]any{"type": apiFormat, "baseUrl": provider.BaseURL},
	}
	return map[string]any{
		"providerId":      provider.ID,
		"providerName":    providerName(provider),
		"templateId":      provider.ID,
		"enabled":         provider.Enabled,
		"executable":      executable,
		"effectiveConfig": effectiveConfig,
		"personalConfig":  personalConfig,
		"issues":          issues,
		"models":          models,
	}
}

func providerName(provider *configstore.Provider) string {
	if strings.TrimSpace(provider.Name) != "" {
		return provider.Name
	}
	return provider.ID
}

// modelConfigObject is the complete `ModelConfigObject` the UI validates.
func modelConfigObject(model configstore.Model, levels []string) map[string]any {
	if len(levels) == 0 {
		levels = []string{"off"}
	}
	maxTokens := model.MaxTokens
	if maxTokens <= 0 {
		maxTokens = defaultMaxTokens
	}
	contextWindow := model.ContextWindow
	if contextWindow <= 0 {
		contextWindow = defaultContextWindow
	}
	input := model.Input
	has := func(modality string) bool {
		for _, entry := range input {
			if entry == modality {
				return true
			}
		}
		return false
	}
	config := map[string]any{
		"enabled": model.Enabled,
		"properties": map[string]any{
			"requiresMfjsToolSchema": false,
			"contextWindow":          contextWindow,
			"inputFormat": map[string]any{
				"supportsText":  true,
				"supportsImage": has("image"),
				"supportsVideo": has("video"),
				"supportsAudio": has("audio"),
				"supportsPdf":   has("pdf"),
			},
			"outputFormat":                  map[string]any{"supportsText": true},
			"supportsToolCall":              true,
			"supportsJsonSchemaOutput":      true,
			"supportsNativeWebSearch":       false,
			"supportsMidConversationSystem": false,
		},
		"optionSpecs": map[string]any{
			"reasoningLevel":  map[string]any{"values": levels, "map": reasoningMap},
			"maxOutputTokens": map[string]any{"max": maxTokens, "map": maxOutputMap},
		},
	}
	if strings.TrimSpace(model.Name) != "" {
		config["name"] = strings.TrimSpace(model.Name)
	}
	return config
}

// secretHint proves a stored secret exists without revealing it: only a masked
// tail of short keys is shown, long keys show nothing.
func secretHint(secrets *configstore.SecretBox, provider *configstore.Provider) (bool, string) {
	if !strings.HasPrefix(strings.TrimSpace(provider.APIKey), configstore.SecretRefPrefix) {
		return provider.APIKey != "", maskInlineKey(provider.APIKey)
	}
	value, ok, err := secrets.Get(strings.TrimSpace(provider.APIKey))
	if err != nil || !ok {
		return false, ""
	}
	return true, maskInlineKey(value)
}

func maskInlineKey(value string) string {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return ""
	}
	if strings.HasPrefix(trimmed, "${") || strings.HasPrefix(trimmed, "!") {
		return trimmed // environment references are not secrets
	}
	if len(trimmed) <= 4 {
		return "••••"
	}
	return "••••" + trimmed[len(trimmed)-4:]
}

// maskSecretForDisplay is used by the E2E helper endpoint; the same rule as the
// UI: the browser only ever sees hasSecret and a hint.
func maskSecretForDisplay(value string) string { return maskInlineKey(value) }

func modelFromConfig(modelID string, config map[string]json.RawMessage) configstore.Model {
	properties := decodeObjectMap(config["properties"])
	inputFormat := decodeObjectMap(properties["inputFormat"])
	optionSpecs := decodeObjectMap(config["optionSpecs"])
	reasoningSpec := decodeObjectMap(optionSpecs["reasoningLevel"])
	maxSpec := decodeObjectMap(optionSpecs["maxOutputTokens"])

	name := strings.TrimSpace(jsonString(config["name"]))
	if len(name) > 64 {
		// The UI's display-name schema caps names at 64 characters; truncating
		// here keeps the stored model valid instead of rejecting the save.
		name = strings.TrimSpace(string(name[:64]))
	}
	model := configstore.Model{
		ID:              strings.TrimSpace(modelID),
		Name:            name,
		ContextWindow:   intField(properties, "contextWindow"),
		MaxTokens:       intField(maxSpec, "max"),
		Enabled:         true,
		Input:           []string{"text"},
		ReasoningLevels: []string{"off"},
	}
	if model.ContextWindow <= 0 {
		model.ContextWindow = defaultContextWindow
	}
	if model.MaxTokens <= 0 {
		model.MaxTokens = defaultMaxTokens
	}
	if _, ok := config["enabled"]; ok {
		var enabled bool
		if json.Unmarshal(config["enabled"], &enabled) == nil {
			model.Enabled = enabled
		}
	}
	if levels := stringListField(reasoningSpec, "values"); len(levels) > 0 {
		model.ReasoningLevels = levels
	}
	model.Reasoning = len(model.ReasoningLevels) > 1 || model.ReasoningLevels[0] != "off"
	if boolField(properties, "supportsImage") || boolField(inputFormat, "supportsImage") {
		model.Input = append(model.Input, "image")
	}
	return model
}

// readViewTarget reads {workspaceIdentity, workspacePath, runnerId} from a
// call's first argument.
