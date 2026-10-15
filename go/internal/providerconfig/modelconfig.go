// Package providerconfig is the Go port of packages/provider +
// packages/provider-node (step 3.6) and, simultaneously, the extraction the
// plan mandates: the provider_config.json parsing the Phase-1 Go agent needs
// (cmd/nex-agent/runtime.go) and the write/management side the Phase-3 Go
// server needs share THIS package, the same way the TS agent and server share
// @nex/provider + @nex/provider-node.
//
// modelconfig.go ports the value types of packages/provider/src/config/model-config.ts
// plus the shared schemas they validate against
// (packages/shared/src/model-config.ts): sparse overlay configs where every
// leaf is optional, with "undefined keeps base, null replaces, otherwise
// recursive merge" semantics (ConfigOverlay).
package providerconfig

import (
	"encoding/json"
	"strings"
)

// OptionSpecValues wraps the JSON of EnumOptionSpec/LimitOptionSpec so the map
// strings (JS expression source) stay raw text — they are opaque to this
// package; only the agent-side option compiler consumes them.
type optionSpecRaw struct {
	Values []string                   `json:"values,omitempty"` // enum spec
	Max    *int64                     `json:"max,omitempty"`    // limit spec
	Map    *string                    `json:"map,omitempty"`
	Extra  map[string]json.RawMessage `json:"-"`
}

// ModelInputFormat ports ModelInputFormatConfig (all leaves *optional*:
// nil = inherit).
type ModelInputFormat struct {
	SupportsText  *bool `json:"supportsText,omitempty"`
	SupportsImage *bool `json:"supportsImage,omitempty"`
	SupportsVideo *bool `json:"supportsVideo,omitempty"`
	SupportsAudio *bool `json:"supportsAudio,omitempty"`
	SupportsPdf   *bool `json:"supportsPdf,omitempty"`
}

// ModelOutputFormat ports ModelOutputFormatConfig.
type ModelOutputFormat struct {
	SupportsText *bool `json:"supportsText,omitempty"`
}

// ModelProperties ports ModelPropertiesConfig.
type ModelProperties struct {
	RequiresMfjsToolSchema        *bool              `json:"requiresMfjsToolSchema,omitempty"`
	ContextWindow                 *int64             `json:"contextWindow,omitempty"`
	InputFormat                   *ModelInputFormat  `json:"inputFormat,omitempty"`
	OutputFormat                  *ModelOutputFormat `json:"outputFormat,omitempty"`
	SupportsToolCall              *bool              `json:"supportsToolCall,omitempty"`
	SupportsJsonSchemaOutput      *bool              `json:"supportsJsonSchemaOutput,omitempty"`
	SupportsNativeWebSearch       *bool              `json:"supportsNativeWebSearch,omitempty"`
	SupportsMidConversationSystem *bool              `json:"supportsMidConversationSystem,omitempty"`
}

// EnumOptionSpec ports EnumOptionSpecConfig (values + map).
type EnumOptionSpec struct {
	Values []string `json:"values,omitempty"`
	Map    *string  `json:"map,omitempty"`
}

// LimitOptionSpec ports LimitOptionSpecConfig (max + map).
type LimitOptionSpec struct {
	Max *int64  `json:"max,omitempty"`
	Map *string `json:"map,omitempty"`
}

// ModelOptionSpecs ports ModelOptionSpecsConfig.
type ModelOptionSpecs struct {
	ReasoningLevel  *EnumOptionSpec  `json:"reasoningLevel,omitempty"`
	MaxOutputTokens *LimitOptionSpec `json:"maxOutputTokens,omitempty"`
}

// ModelConfig ports the sparse ModelConfig overlay type. All leaves optional;
// ToObject drops the keys a nil leaf would leave absent (objectWithoutUndefined).
type ModelConfig struct {
	Enabled     *bool             `json:"enabled,omitempty"`
	Name        *string           `json:"name,omitempty"`
	Properties  *ModelProperties  `json:"properties,omitempty"`
	OptionSpecs *ModelOptionSpecs `json:"optionSpecs,omitempty"`
}

// IsZero reports whether the config has no set leaf (isStructurallyEmpty is
// the TS spelling used by savePersonalModelDraft's "recommended && empty"
// branch — note the TS checks *structural* emptiness of the serialized JSON,
// so empty arrays/maps count as empty too; here all leaves are pointers so
// zero-ness is exactly that).
func (c *ModelConfig) IsZero() bool {
	return c == nil || (c.Enabled == nil && c.Name == nil && c.Properties.isZero() && c.OptionSpecs.isZero())
}

// Overlay ports ModelConfig.overlay: next's non-nil leaves win; nested
// properties/optionSpecs merge recursively (overlayConfig); nil next
// *properties* inherits base.
func (c *ModelConfig) Overlay(next *ModelConfig) *ModelConfig {
	if next == nil {
		return c
	}
	base := c
	if base == nil {
		base = &ModelConfig{}
	}
	return &ModelConfig{
		Enabled:     overlayPtr(base.Enabled, next.Enabled),
		Name:        overlayPtr(base.Name, next.Name),
		Properties:  overlayProperties(base.Properties, next.Properties),
		OptionSpecs: overlayOptionSpecs(base.OptionSpecs, next.OptionSpecs),
	}
}

// overlayValue: next wins unless nil (the ConfigOverlay.overlayValue rule).
func overlayPtr[T any](base, next *T) *T {
	if next == nil {
		return base
	}
	return next
}

func overlayProperties(base, next *ModelProperties) *ModelProperties {
	if next == nil {
		return base
	}
	if base == nil {
		return next
	}
	return &ModelProperties{
		RequiresMfjsToolSchema:        overlayPtr(base.RequiresMfjsToolSchema, next.RequiresMfjsToolSchema),
		ContextWindow:                 overlayPtr(base.ContextWindow, next.ContextWindow),
		InputFormat:                   overlayInputFormat(base.InputFormat, next.InputFormat),
		OutputFormat:                  overlayOutputFormat(base.OutputFormat, next.OutputFormat),
		SupportsToolCall:              overlayPtr(base.SupportsToolCall, next.SupportsToolCall),
		SupportsJsonSchemaOutput:      overlayPtr(base.SupportsJsonSchemaOutput, next.SupportsJsonSchemaOutput),
		SupportsNativeWebSearch:       overlayPtr(base.SupportsNativeWebSearch, next.SupportsNativeWebSearch),
		SupportsMidConversationSystem: overlayPtr(base.SupportsMidConversationSystem, next.SupportsMidConversationSystem),
	}
}

func (p *ModelProperties) isZero() bool {
	if p == nil {
		return true
	}
	return p.RequiresMfjsToolSchema == nil && p.ContextWindow == nil &&
		p.InputFormat.isZero() && p.OutputFormat.isZero() &&
		p.SupportsToolCall == nil && p.SupportsJsonSchemaOutput == nil &&
		p.SupportsNativeWebSearch == nil && p.SupportsMidConversationSystem == nil
}

func overlayInputFormat(base, next *ModelInputFormat) *ModelInputFormat {
	if next == nil {
		return base
	}
	if base == nil {
		return next
	}
	return &ModelInputFormat{
		SupportsText:  overlayPtr(base.SupportsText, next.SupportsText),
		SupportsImage: overlayPtr(base.SupportsImage, next.SupportsImage),
		SupportsVideo: overlayPtr(base.SupportsVideo, next.SupportsVideo),
		SupportsAudio: overlayPtr(base.SupportsAudio, next.SupportsAudio),
		SupportsPdf:   overlayPtr(base.SupportsPdf, next.SupportsPdf),
	}
}

func (f *ModelInputFormat) isZero() bool {
	return f == nil || (f.SupportsText == nil && f.SupportsImage == nil && f.SupportsVideo == nil &&
		f.SupportsAudio == nil && f.SupportsPdf == nil)
}

func overlayOutputFormat(base, next *ModelOutputFormat) *ModelOutputFormat {
	if next == nil {
		return base
	}
	if base == nil {
		return next
	}
	return &ModelOutputFormat{SupportsText: overlayPtr(base.SupportsText, next.SupportsText)}
}

func (f *ModelOutputFormat) isZero() bool { return f == nil || f.SupportsText == nil }

func overlayOptionSpecs(base, next *ModelOptionSpecs) *ModelOptionSpecs {
	if next == nil {
		return base
	}
	if base == nil {
		return next
	}
	return &ModelOptionSpecs{
		ReasoningLevel:  overlayEnumSpec(base.ReasoningLevel, next.ReasoningLevel),
		MaxOutputTokens: overlayLimitSpec(base.MaxOutputTokens, next.MaxOutputTokens),
	}
}

func (s *ModelOptionSpecs) isZero() bool {
	return s == nil || (s.ReasoningLevel == nil && s.MaxOutputTokens == nil)
}

func overlayEnumSpec(base, next *EnumOptionSpec) *EnumOptionSpec {
	if next == nil {
		return base
	}
	if base == nil {
		return next
	}
	return &EnumOptionSpec{Values: overlayValues(base.Values, next.Values), Map: overlayPtr(base.Map, next.Map)}
}

func overlayLimitSpec(base, next *LimitOptionSpec) *LimitOptionSpec {
	if next == nil {
		return base
	}
	if base == nil {
		return next
	}
	return &LimitOptionSpec{Max: overlayPtr(base.Max, next.Max), Map: overlayPtr(base.Map, next.Map)}
}

// overlayValues: next wins when non-empty, otherwise base (nil-leaf semantics
// for the array leaves: an empty slice serializes as absent).
func overlayValues(base, next []string) []string {
	if len(next) == 0 {
		return base
	}
	return next
}

// ParseModelConfig decodes a ModelConfigObject JSON blob.
func ParseModelConfig(data []byte) (*ModelConfig, error) {
	var cfg ModelConfig
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, err
	}
	return &cfg, nil
}

// ModelConfigFromObject converts a decoded JSON object (map form) back to the
// typed config, for call sites that hold rpc.Value.
func ModelConfigFromObject(obj map[string]any) (*ModelConfig, error) {
	data, err := json.Marshal(obj)
	if err != nil {
		return nil, err
	}
	return ParseModelConfig(data)
}

// normalizeModelID trims; the TS freezeModelIds rejects empty ids on
// construction. Callers that need the error keep it here.
func normalizeModelID(id string) (string, bool) {
	trimmed := strings.TrimSpace(id)
	return trimmed, trimmed != ""
}
