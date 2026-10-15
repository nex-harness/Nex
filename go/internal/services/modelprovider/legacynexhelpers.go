// legacynexhelpers.go: the pure readers of the legacy nex config reader —
// option/header/limit extraction, kind inference, preset-URL classification.
package modelprovider

import (
	"net/url"
	"reflect"
	"strings"
)

// helpers

func hasOpenCodeProviderRuntimeFields(provider nexOpenCodeProviderConfig) bool {
	// config.json 的权威运行态字段是 kind/options.baseURL.
	return provider.Kind != nil ||
		readStringOption(provider.Options, "baseURL") != "" ||
		deref(provider.API) != "" ||
		strings.TrimSpace(deref(provider.NPM)) != ""
}

func resolveOpenCodeProviderDefaultKind(provider nexOpenCodeProviderConfig) string {
	if provider.Kind != nil {
		return *provider.Kind
	}
	if readStringOption(provider.Options, "baseURL") != "" || deref(provider.API) != "" || strings.TrimSpace(deref(provider.NPM)) != "" {
		return inferModelProviderKindFromOpenCodeProvider(provider)
	}
	if provider.DefaultKind != nil {
		return *provider.DefaultKind
	}
	if provider.Nex != nil && provider.Nex.DefaultKind != nil {
		return *provider.Nex.DefaultKind
	}
	return inferModelProviderKindFromOpenCodeProvider(provider)
}

func inferModelProviderKindFromOpenCodeProvider(provider nexOpenCodeProviderConfig) string {
	npm := strings.ToLower(deref(provider.NPM))
	if strings.Contains(npm, "anthropic") {
		return kindAnthropic
	}
	if strings.Contains(npm, "openai-compatible") {
		return kindOpenAICompatible
	}
	if strings.Contains(npm, "openai") {
		return kindOpenAI
	}
	return kindOpenAICompatible
}

// legacyPresetGlmProviderIDs: the ZAI/BigModel preset provider set whose
// runtime protocol must align with the known runtime URL.
var legacyPresetGlmProviderIDs = map[string]bool{
	"zai-api":                                 true,
	"account:zai-individual-coding-plan":      true,
	"account:zai-start-plan":                  true,
	"bigmodel-api":                            true,
	"account:bigmodel-individual-coding-plan": true,
	"account:bigmodel-start-plan":             true,
}

func resolvePresetProviderKindFromRuntimeBaseURL(providerID, baseURL string) string {
	if baseURL == "" || !legacyPresetGlmProviderIDs[providerID] {
		return ""
	}
	normalized := strings.ToLower(strings.TrimSpace(baseURL))
	if normalized == "" {
		return ""
	}
	path := resolveRuntimeBaseUrlPath(normalized)
	if providerID == "account:bigmodel-individual-coding-plan" && strings.Contains(path, "/coding/paas/v4") {
		// BigModel Coding Plan 的默认 runtime 已统一回 Anthropic.
		return kindAnthropic
	}
	if strings.Contains(path, "/coding/paas/v4") {
		return kindOpenAICompatible
	}
	if strings.Contains(path, "/api/anthropic") || strings.Contains(path, "/nex-plan/anthropic") {
		return kindAnthropic
	}
	return ""
}

func resolveRuntimeBaseUrlPath(baseURL string) string {
	if parsed, err := url.Parse(baseURL); err == nil && parsed.Path != "" {
		return strings.ToLower(parsed.Path)
	}
	return strings.ToLower(baseURL)
}

func resolveOpenCodeProviderEndpoints(provider nexOpenCodeProviderConfig, defaultKind, providerID string) legacyEndpoints {
	baseURL := firstNonEmptyStr(readStringOption(provider.Options, "baseURL"), deref(provider.API))
	if baseURL != "" {
		if providerID == "account:bigmodel-individual-coding-plan" {
			// 旧版本把 BigModel Coding Plan 保存到 OpenAI Chat 的 coding endpoint；
			// 现在该内置 provider 默认使用 Anthropic endpoint. Environment-dependent
			// rewrite is a desktop bootstrap concern; the server-side import keeps the
			// configured URL (both engines resolve it at read time).
			normalized := normalizeConfiguredBaseURL(baseURL)
			return legacyEndpoints{BaseURL: normalized, Paths: map[string]string{"anthropic": "/v1/messages"}}
		}
		// config.json 新结构的 options.baseURL 是用户配置的 runtime baseURL. 读取时
		// 不能按 kind 删除 /v1、/responses 等路径段.
		return legacyEndpoints{BaseURL: normalizeConfiguredBaseURL(baseURL)}
	}
	if provider.Endpoints != nil {
		return *provider.Endpoints
	}
	if provider.Nex != nil && provider.Nex.Endpoints != nil {
		return *provider.Nex.Endpoints
	}
	return legacyEndpoints{}
}

// normalizeConfiguredBaseURL ports normalizeModelProviderConfiguredBaseUrl:
// strip trailing slashes only.
func normalizeConfiguredBaseURL(value string) string {
	return strings.TrimRight(strings.TrimSpace(value), "/")
}

func apiFormatOf(provider nexOpenCodeProviderConfig, preferOpenCodeFields bool, defaultKind string) *string {
	if preferOpenCodeFields {
		return kindAPIFormat(defaultKind)
	}
	if provider.APIFormat != nil {
		return provider.APIFormat
	}
	if provider.Nex != nil && provider.Nex.APIFormat != nil {
		return provider.Nex.APIFormat
	}
	return kindAPIFormat(defaultKind)
}

func kindAPIFormat(kind string) *string {
	switch kind {
	case kindAnthropic:
		return strPtr(providerconfigAPIAnthropic)
	case kindOpenAI:
		return strPtr(providerconfigAPIOpenAI)
	default:
		return strPtr(providerconfigAPIOpenAICompatible)
	}
}

func sourceOf(nex *nexProviderExtension) *string {
	if nex == nil {
		return nil
	}
	return nex.Source
}

func providerHeadersOf(nex *nexProviderExtension) map[string]string {
	if nex == nil {
		return nil
	}
	return nex.Headers
}

func deletedOf(nex *nexModelExtension) *bool {
	if nex == nil {
		return nil
	}
	return nex.Deleted
}

func normalizeDeletedModelIDs(ids []string) []string {
	out := make([]string, 0, len(ids))
	seen := map[string]bool{}
	for _, raw := range ids {
		id := strings.TrimSpace(raw)
		key := strings.ToLower(id)
		if id == "" || seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, id)
	}
	return out
}

func filterDeletedModels(models []LegacyModelEntry) []LegacyModelEntry {
	out := make([]LegacyModelEntry, 0, len(models))
	for _, model := range models {
		if model.Config == nil || model.Config.Deleted == nil || !*model.Config.Deleted {
			out = append(out, model)
		}
	}
	return out
}

func readStringOption(options map[string]any, key string) string {
	if options == nil {
		return ""
	}
	if v, ok := options[key].(string); ok {
		return strings.TrimSpace(v)
	}
	return ""
}

func readStringRecordOption(options map[string]any, key string) map[string]string {
	if options == nil {
		return nil
	}
	raw, ok := options[key].(map[string]any)
	if !ok {
		return nil
	}
	out := map[string]string{}
	for k, v := range raw {
		normalizedKey := strings.TrimSpace(k)
		if s, ok := v.(string); ok && normalizedKey != "" && strings.TrimSpace(s) != "" {
			out[normalizedKey] = strings.TrimSpace(s)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func readLimit(limit *struct {
	Context *float64 `json:"context"`
	Input   *float64 `json:"input"`
	Output  *float64 `json:"output"`
}, key string) *float64 {
	if limit == nil {
		return nil
	}
	switch key {
	case "context":
		return limit.Context
	case "output":
		return limit.Output
	}
	return nil
}

func firstPositiveFloat(values ...*float64) *int64 {
	for _, v := range values {
		if v != nil && *v > 0 {
			out := int64(*v)
			return &out
		}
	}
	return nil
}

func derefFloat(v *float64) *float64 { return v }

func firstNonNilBool(values ...*bool) *bool {
	for _, v := range values {
		if v != nil {
			return v
		}
	}
	return nil
}

func firstNonNil[T any](values ...T) T {
	var zero T
	// reflect-free nil test: comparable types compare directly; maps/slices/
	// pointers need the IsNil path (comparing uncomparable types panics).
	for _, v := range values {
		rv := reflect.ValueOf(v)
		switch {
		case !rv.IsValid():
			continue // nil interface
		case rv.Kind() == reflect.Map || rv.Kind() == reflect.Slice || rv.Kind() == reflect.Ptr || rv.Kind() == reflect.Interface:
			if rv.IsNil() {
				continue
			}
			return v
		default:
			if any(v) == any(zero) {
				continue
			}
			return v
		}
	}
	return zero
}

func firstNonNilStr(values ...*string) *string {
	for _, v := range values {
		if v != nil {
			return v
		}
	}
	return nil
}

func firstNonEmptyStr(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}

func sortStrings(values []string) {
	for i := 1; i < len(values); i++ {
		for j := i; j > 0 && values[j] < values[j-1]; j-- {
			values[j], values[j-1] = values[j-1], values[j]
		}
	}
}

// API type constants (mirror providerconfig's, kept local to avoid the
// service package importing providerconfig here just for three strings).
const (
	providerconfigAPIAnthropic        = "anthropic-messages"
	providerconfigAPIOpenAI           = "openai-chat-completions"
	providerconfigAPIOpenAICompatible = "openai-chat-completions"
)
