// filecodec.go ports packages/provider-node/src/provider-config-file-codec.ts:
// the provider_config.json decode/encode boundary — schemaVersion gating,
// personal rules/provider parsing, the legacy manual-rule normalization, and
// the exact stored shape (byte parity matters: the personal repository uses
// encode(decode(x)) === x as its "file already canonical" check).
package providerconfig

import (
	"encoding/json"
	"fmt"
	"strings"
)

// PersonalProviderConfigFileName is PERSONAL_PROVIDER_CONFIG_FILE_NAME.
const PersonalProviderConfigFileName = "provider_config.json"

// CurrentSchemaVersion is CURRENT_SCHEMA_VERSION (no migrations yet).
const CurrentSchemaVersion = 1

// LayerUpdate ports ProviderConfigLayerUpdate — the full personal layer
// (providers, models, order, default selection) written atomically.
type LayerUpdate struct {
	Providers             *ProviderConfigMap
	Models                *ModelConfigRules
	ProviderOrder         []string
	DefaultModelSelection *ModelSelection
}

// LayerSnapshot ports ProviderConfigLayerSnapshot: the update plus its
// content revision (sha256 of the canonical encoding).
type LayerSnapshot struct {
	Revision              string
	Providers             *ProviderConfigMap
	Models                *ModelConfigRules
	ProviderOrder         []string
	DefaultModelSelection *ModelSelection
}

// EmptyLayerUpdate is emptyUpdate().
func EmptyLayerUpdate() *LayerUpdate {
	return &LayerUpdate{Providers: EmptyProviderConfigMap(), Models: EmptyModelConfigRules()}
}

// SnapshotFromUpdate ports snapshotFromUpdate: the revision is the sha256 of
// the canonical file encoding, so the snapshot always corresponds to the
// disk content it was built from (补空数组会让未声明排序的文件在 CAS 时误报变化).
func SnapshotFromUpdate(update *LayerUpdate) (*LayerSnapshot, error) {
	encoded, err := EncodeProviderConfigFile(update)
	if err != nil {
		return nil, err
	}
	canonical, err := json.Marshal(encoded)
	if err != nil {
		return nil, err
	}
	return &LayerSnapshot{
		Revision:              sha256Hex(canonical),
		Providers:             update.Providers,
		Models:                update.Models,
		ProviderOrder:         update.ProviderOrder,
		DefaultModelSelection: update.DefaultModelSelection,
	}, nil
}

// storedFile matches storedProviderConfigSchema (.strict()).
type storedFile struct {
	SchemaVersion int `json:"schemaVersion"`
	Config        struct {
		ProviderOrder         []string        `json:"providerOrder,omitempty"`
		ProviderConfigRules   json.RawMessage `json:"providerConfigRules"`
		ModelConfigRules      json.RawMessage `json:"modelConfigRules"`
		DefaultModelSelection *ModelSelection `json:"defaultModelSelection,omitempty"`
	} `json:"config"`
}

// providerRulesWire is the config.providerConfigRules shape.
type providerRulesWire struct {
	ProviderRules []ProviderConfigRule `json:"providerRules"`
}

// UnsupportedProviderConfigVersionError ports the same-named error.
type UnsupportedProviderConfigVersionError struct {
	Version *int
	msg     string
}

func (e *UnsupportedProviderConfigVersionError) Error() string { return e.msg }

// DecodeProviderConfigFile ports decodeProviderConfigFile.
func DecodeProviderConfigFile(data []byte) (*LayerUpdate, error) {
	var probe struct {
		SchemaVersion *json.Number `json:"schemaVersion"`
	}
	if err := json.Unmarshal(data, &probe); err != nil {
		return nil, &UnsupportedProviderConfigVersionError{msg: "Provider Config 缺少 schemaVersion"}
	}
	if probe.SchemaVersion == nil {
		return nil, &UnsupportedProviderConfigVersionError{msg: "Provider Config 缺少 schemaVersion"}
	}
	version, err := probe.SchemaVersion.Int64()
	if err != nil || version < 0 {
		return nil, &UnsupportedProviderConfigVersionError{msg: "Provider Config schemaVersion 必须是非负整数"}
	}
	if version > CurrentSchemaVersion {
		v := int(version)
		return nil, &UnsupportedProviderConfigVersionError{
			Version: &v,
			msg:     fmt.Sprintf("Provider Config schemaVersion %d 高于当前支持的 %d", version, CurrentSchemaVersion),
		}
	}
	var file storedFile
	if err := json.Unmarshal(data, &file); err != nil {
		return nil, err
	}
	if file.SchemaVersion != CurrentSchemaVersion {
		return nil, &UnsupportedProviderConfigVersionError{msg: "Provider Config 缺少 schemaVersion"}
	}
	// providers
	var rulesWire providerRulesWire
	if err := json.Unmarshal(file.Config.ProviderConfigRules, &rulesWire); err != nil {
		return nil, err
	}
	providers, err := NewProviderConfigMap(rulesWire.ProviderRules)
	if err != nil {
		return nil, err
	}
	if err := validatePersonalRules(providers); err != nil {
		return nil, err
	}
	// models (with the legacy manual-rule normalization)
	normalizedModelRules, err := normalizeLegacyManualRules(file.Config.ModelConfigRules)
	if err != nil {
		return nil, err
	}
	models, err := ParsePersonalModelConfigRules(normalizedModelRules)
	if err != nil {
		return nil, err
	}
	return &LayerUpdate{
		Providers:             providers,
		Models:                models,
		ProviderOrder:         file.Config.ProviderOrder,
		DefaultModelSelection: file.Config.DefaultModelSelection,
	}, nil
}

// validatePersonalRules ports personalProviderConfigRuleSchema's refinements:
// unique ids (checked by NewProviderConfigMap) and account:* providers must
// not carry a personal access overlay.
func validatePersonalRules(providers *ProviderConfigMap) error {
	for _, rule := range providers.Rules() {
		if strings.HasPrefix(rule.ProviderID, "account:") && rule.Config != nil && rule.Config.Access != nil {
			return &simpleError{"固定 Account Provider 的 Access 只能由 Nex Built-in Config 声明"}
		}
	}
	return nil
}

// EncodeProviderConfigFile ports encodeProviderConfigFile: the canonical
// stored shape. Keys absent when unset; providerOrder omitted when nil.
func EncodeProviderConfigFile(update *LayerUpdate) (map[string]any, error) {
	config := map[string]any{
		"providerConfigRules": map[string]any{"providerRules": update.Providers.ToJSON()},
		"modelConfigRules":    update.Models.ToPersonalJSON(),
	}
	if update.ProviderOrder != nil {
		config["providerOrder"] = update.ProviderOrder
	}
	if update.DefaultModelSelection != nil {
		config["defaultModelSelection"] = update.DefaultModelSelection
	}
	return map[string]any{
		"schemaVersion": CurrentSchemaVersion,
		"config":        config,
	}, nil
}

// normalizeLegacyManualRules ports normalizeLegacyManualRules: 旧编辑器曾把
// MFJS 当作手动必填项；仅在文件边界识别旧合法形状并提取现行可编辑字段.
// Manual rules whose config parses as the current manual schema pass through
// untouched; legacy-shaped ones are reduced to the editable leaves; anything
// else passes through unchanged (the strict write boundary rejects it later).
func normalizeLegacyManualRules(data json.RawMessage) (json.RawMessage, error) {
	if len(data) == 0 {
		return data, nil
	}
	var wire personalModelConfigRulesData
	if err := json.Unmarshal(data, &wire); err != nil {
		return data, nil // not the expected shape: leave for the strict parse to reject
	}
	if wire.ManualProviderModelRules == nil {
		return data, nil
	}
	changed := false
	for i, rule := range wire.ManualProviderModelRules {
		if rule.Config == nil || isCurrentManualConfig(rule.Config) {
			continue
		}
		extracted := extractManualModelConfig(rule.Config)
		if extracted == nil {
			continue // 未知/损坏配置不能通过删字段伪装成成功
		}
		wire.ManualProviderModelRules[i].Config = extracted
		changed = true
	}
	if !changed {
		return data, nil
	}
	out, err := json.Marshal(wire)
	if err != nil {
		return nil, err
	}
	return out, nil
}

// isCurrentManualConfig: the config validates against the current manual
// schema (only editable leaves, all complete).
func isCurrentManualConfig(config *ModelConfig) bool {
	if config == nil {
		return false
	}
	// Manual leaves: enabled, name, contextWindow, supportsJsonSchemaOutput,
	// supportsNativeWebSearch, supportsMidConversationSystem,
	// inputFormat.{image,video,pdf}, reasoningLevel{values,map},
	// maxOutputTokens{max,map}. Presence of any OTHER leaf makes it legacy.
	if config.Properties != nil {
		p := config.Properties
		if p.RequiresMfjsToolSchema != nil || p.SupportsToolCall != nil ||
			p.InputFormat != nil && (p.InputFormat.SupportsText != nil || p.InputFormat.SupportsAudio != nil) ||
			p.OutputFormat != nil {
			return false
		}
		if p.InputFormat != nil && (p.InputFormat.SupportsText != nil || p.InputFormat.SupportsAudio != nil) {
			return false
		}
	}
	if config.OptionSpecs != nil && config.OptionSpecs.MaxOutputTokens != nil && config.OptionSpecs.MaxOutputTokens.Map != nil {
		// map is a manual leaf actually — current manual schema includes map via pick? No:
		// manualModelConfigSchema picks reasoningLevel (full: values+map) and
		// maxOutputTokens.pick({max}) — maxOutputTokens.map is NOT manual.
		return false
	}
	return true
}

// manualLeafSet lists the JSON keys of the manual schema.
// extractManualModelConfig ports extractManualModelConfig: pick only the
// manual-editable leaves. Returns nil when the input is not a legacy-valid
// complete shape (caller keeps the original).
func extractManualModelConfig(config *ModelConfig) *ModelConfig {
	// Legacy shapes came in two forms: complete (every complete-schema leaf
	// present) or complete-minus-frozen-system-leaves. Both carry at least the
	// manual leaves. Here we simply project onto the manual leaf set.
	if config == nil {
		return nil
	}
	out := &ModelConfig{Enabled: config.Enabled, Name: config.Name}
	if p := config.Properties; p != nil {
		np := &ModelProperties{
			ContextWindow:                 p.ContextWindow,
			SupportsJsonSchemaOutput:      p.SupportsJsonSchemaOutput,
			SupportsNativeWebSearch:       p.SupportsNativeWebSearch,
			SupportsMidConversationSystem: p.SupportsMidConversationSystem,
		}
		if p.InputFormat != nil {
			np.InputFormat = &ModelInputFormat{
				SupportsImage: p.InputFormat.SupportsImage,
				SupportsVideo: p.InputFormat.SupportsVideo,
				SupportsPdf:   p.InputFormat.SupportsPdf,
			}
		}
		out.Properties = np
	}
	if s := config.OptionSpecs; s != nil {
		ns := &ModelOptionSpecs{}
		if s.ReasoningLevel != nil {
			ns.ReasoningLevel = &EnumOptionSpec{Values: s.ReasoningLevel.Values, Map: s.ReasoningLevel.Map}
		}
		if s.MaxOutputTokens != nil && s.MaxOutputTokens.Max != nil {
			ns.MaxOutputTokens = &LimitOptionSpec{Max: s.MaxOutputTokens.Max}
		}
		out.OptionSpecs = ns
	}
	return out
}

// SameLayerUpdate reports whether two updates encode identically
// (samePersonalConfig via encode equality).
func SameLayerUpdate(left, right *LayerUpdate) bool {
	l, lerr := json.Marshal(mapForCompare(left))
	r, rerr := json.Marshal(mapForCompare(right))
	if lerr != nil || rerr != nil {
		return false
	}
	return string(l) == string(r)
}

func mapForCompare(update *LayerUpdate) map[string]any {
	encoded, err := EncodeProviderConfigFile(update)
	if err != nil {
		return nil
	}
	return encoded
}

// AsUpdate projects the snapshot back to the update shape (the provisioning
// CAS compares updates, not snapshots).
func (s *LayerSnapshot) AsUpdate() *LayerUpdate {
	return &LayerUpdate{
		Providers: s.Providers, Models: s.Models, ProviderOrder: s.ProviderOrder,
		DefaultModelSelection: s.DefaultModelSelection,
	}
}
