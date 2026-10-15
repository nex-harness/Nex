// migration.go ports packages/services/src/bots/storageMigration.ts plus the two
// shared legacy-identity helpers it calls.
//
// These run ONLY when the v3 file is absent (首次读取迁移); they are not a
// compatibility reader for the current options. The functions work on raw JSON
// because the legacy shapes are wide and versioned: re-declaring them as Go
// structs would silently drop fields the migration is meant to carry forward.
package bots

import (
	"encoding/json"
	"net/url"
	"regexp"
	"strings"
)

// ImportLegacyConfig ports importLegacyBotConfig: force version 3 and run each
// bot's currentOptions through migrateSelection + normalizeBotCurrentOptions.
// A non-object / non-array value is returned unchanged so the caller's decode
// produces the real error.
func ImportLegacyConfig(value json.RawMessage) json.RawMessage {
	root, ok := decodeObject(value)
	if !ok {
		return value
	}
	bots, ok := root["bots"].([]any)
	if !ok {
		return value
	}
	migrated := make([]any, 0, len(bots))
	for _, botValue := range bots {
		botRecord, ok := botValue.(map[string]any)
		if !ok {
			migrated = append(migrated, botValue)
			continue
		}
		options, _ := botRecord["currentOptions"].(map[string]any)
		if options == nil {
			options = map[string]any{}
		}
		botRecord["currentOptions"] = normalizeCurrentOptionsValue(migrateSelection(options))
		migrated = append(migrated, botRecord)
	}
	root["version"] = float64(ConfigVersion)
	root["bots"] = migrated
	return mustMarshal(root)
}

// ImportLegacyState ports importLegacyBotState.
func ImportLegacyState(value json.RawMessage) json.RawMessage {
	root, ok := decodeObject(value)
	if !ok {
		return value
	}
	bots, ok := root["bots"].(map[string]any)
	if !ok {
		return value
	}
	for _, stateValue := range bots {
		stateRecord, ok := stateValue.(map[string]any)
		if !ok {
			continue
		}
		options, ok := stateRecord["draftOptions"].(map[string]any)
		if !ok {
			continue
		}
		normalized := map[string]any{
			// NEX_AGENT_PROVIDER is the only value botDraftOptionsSchema accepts.
			"provider":       "nex-agent",
			"modelSelection": migrateSelection(options),
		}
		if mode, ok := options["mode"].(string); ok {
			normalized["mode"] = mode
		}
		stateRecord["draftOptions"] = normalizeDraftOptionsValue(normalized)
	}
	root["version"] = float64(StateVersion)
	return mustMarshal(root)
}

// migrateSelection ports migrateSelection. The two documented bug roots are
// preserved verbatim because they are the reason the function is shaped this way:
//
//   - Bug 根因：旧 thoughtLevel 曾覆盖已保存的新档位；新字段存在时禁止回读旧字段。
//   - Bug 根因：候选为空时曾把明确旧选择永久写空到 v3；迁移只搬意图，不能检查当前可用性。
func migrateSelection(options map[string]any) any {
	if _, hasNew := options["modelSelection"]; hasNew {
		// The new field exists: never read the legacy thoughtLevel/model back.
		selection, ok := decodeModelSelection(options["modelSelection"])
		if !ok {
			return nil
		}
		if !strings.HasPrefix(selection.ProviderID, "builtin:") {
			return selectionValue(selection)
		}
		providerID, ok := migrateLegacyModelProviderID(selection.ProviderID)
		if !ok {
			return nil
		}
		migrated := selectionValue(selection)
		migrated["providerId"] = providerID
		migrated["modelId"] = migrateLegacyOfficialGLMModelID(selection.ProviderID, selection.ModelID)
		return migrated
	}
	// Legacy "provider/model" (or "custom:...") string form.
	rawModel, _ := options["model"].(string)
	value := strings.TrimSpace(rawModel)
	decoded, hasDecoded := decodeCustomModelValue(value)
	separator := strings.Index(value, "/")
	oldProviderID, oldModelID := "", ""
	switch {
	case hasDecoded:
		oldProviderID, oldModelID = decoded.providerID, decoded.modelName
	case separator > 0 && separator < len(value)-1:
		oldProviderID, oldModelID = value[:separator], value[separator+1:]
	}
	if oldProviderID == "" || oldModelID == "" {
		return nil
	}
	providerID, ok := migrateLegacyModelProviderID(oldProviderID)
	if !ok {
		return nil
	}
	migrated := map[string]any{
		"providerId": providerID,
		"modelId":    migrateLegacyOfficialGLMModelID(oldProviderID, oldModelID),
	}
	if rawLevel, ok := options["thoughtLevel"].(string); ok {
		if trimmed := strings.TrimSpace(rawLevel); trimmed != "" {
			migrated["options"] = map[string]any{"reasoningLevel": trimmed}
		}
	}
	return migrated
}

// normalizeCurrentOptionsValue ports normalizeBotCurrentOptions over a decoded
// object: only a valid modelSelection survives; the legacy model/thoughtLevel
// fields are read by migrateSelection only and never saved back.
func normalizeCurrentOptionsValue(selection any) map[string]any {
	normalized := map[string]any{}
	if record, ok := selection.(map[string]any); ok {
		if providerID, _ := record["providerId"].(string); providerID != "" {
			if modelID, _ := record["modelId"].(string); modelID != "" {
				normalized["modelSelection"] = record
			}
		}
	}
	return normalized
}

// normalizeDraftOptionsValue ports normalizeBotDraftOptions.
func normalizeDraftOptionsValue(options map[string]any) map[string]any {
	normalized := map[string]any{"provider": options["provider"]}
	if selection, ok := options["modelSelection"].(map[string]any); ok {
		if providerID, _ := selection["providerId"].(string); providerID != "" {
			if modelID, _ := selection["modelId"].(string); modelID != "" {
				normalized["modelSelection"] = selection
			}
		}
	}
	if mode, ok := options["mode"].(string); ok && mode != "" {
		normalized["mode"] = mode
	}
	return normalized
}

// customModelValue is DecodedCustomModelValue.
type customModelValue struct {
	providerID string
	modelName  string
}

// decodeCustomModelValue ports decodeCustomModelValue. The prefix is "custom:"
// and the body is "encodedProviderId[:encodedModelName]", with a legacy
// three-part "builtin:provider:model" form.
func decodeCustomModelValue(value string) (customModelValue, bool) {
	const prefix = "custom:"
	if !strings.HasPrefix(value, prefix) {
		return customModelValue{}, false
	}
	body := strings.TrimPrefix(value, prefix)
	separatorIndex := strings.Index(body, ":")
	if separatorIndex < 0 {
		return customModelValue{providerID: safeDecodeURIComponent(body)}, true
	}
	legacyParts := strings.Split(body, ":")
	if len(legacyParts) >= 3 && legacyParts[0] == "builtin" {
		return customModelValue{
			providerID: legacyParts[0] + ":" + legacyParts[1],
			modelName:  safeDecodeURIComponent(strings.Join(legacyParts[2:], ":")),
		}, true
	}
	return customModelValue{
		providerID: safeDecodeURIComponent(body[:separatorIndex]),
		modelName:  safeDecodeURIComponent(body[separatorIndex+1:]),
	}, true
}

// safeDecodeURIComponent ports safeDecodeURIComponent: a malformed escape
// sequence is returned unchanged rather than throwing.
func safeDecodeURIComponent(value string) string {
	decoded, err := url.PathUnescape(value)
	if err != nil {
		return value
	}
	return decoded
}

// legacyBuiltinTemplateIDs and legacyBuiltinModelIDs are
// BUILTIN_PROVIDER_TEMPLATE_IDS / BUILTIN_MODEL_PROVIDER_IDS
// (model-provider-types.ts), the only entries the switch below reaches.
const (
	builtinTemplateZai      = "zai-api"
	builtinTemplateBigmodel = "bigmodel-api"

	builtinModelZaiStartPlan       = "account:zai-start-plan"
	builtinModelBigmodelStartPlan  = "account:bigmodel-start-plan"
	builtinModelZaiCodingPlan      = "account:zai-individual-coding-plan"
	builtinModelBigmodelCodingPlan = "account:bigmodel-individual-coding-plan"
)

// migrateLegacyModelProviderID ports migrateLegacyModelProviderId. Only an
// explicit legacy id migrates; an unknown `builtin:` id returns false so the
// caller drops the selection instead of writing an empty one back (a non-builtin
// id is already current and passes through).
func migrateLegacyModelProviderID(providerID string) (string, bool) {
	switch providerID {
	case "builtin:bigmodel":
		return builtinTemplateBigmodel, true
	case "builtin:zai":
		return builtinTemplateZai, true
	case "builtin:bigmodel-start-plan":
		return builtinModelBigmodelStartPlan, true
	case "builtin:zai-start-plan":
		return builtinModelZaiStartPlan, true
	case "builtin:bigmodel-coding-plan":
		return builtinModelBigmodelCodingPlan, true
	case "builtin:zai-coding-plan":
		return builtinModelZaiCodingPlan, true
	}
	if strings.HasPrefix(providerID, "builtin:") {
		return "", false
	}
	return providerID, true
}

// officialGLMProviderPattern is the provider shape migrateLegacyOfficialGLMModelId
// normalises model ids for.
var officialGLMProviderPattern = regexp.MustCompile(
	`^(?:builtin:(?:zai|bigmodel)(?:-start-plan|-coding-plan)?` +
		`|account:(?:zai|bigmodel)-(?:start-plan|individual-coding-plan|team-coding-plan))$`)

// migrateLegacyOfficialGLMModelID ports migrateLegacyOfficialGlmModelId: only the
// official GLM provider shapes get a canonical model id; everything else keeps
// its own model name verbatim.
func migrateLegacyOfficialGLMModelID(providerID, modelID string) string {
	if officialGLMProviderPattern.MatchString(providerID) {
		return normalizeOfficialGLMModelID(modelID)
	}
	return modelID
}

// officialGLMModelIDs is the canonical list (official-glm-model-id.ts); the
// lookup is case-insensitive and an unknown id passes through unchanged.
var officialGLMModelIDs = []string{
	"GLM-5.3", "GLM-5.3-Flash", "GLM-5V-Turbo", "GLM-5.2", "GLM-5.1", "GLM-5.1-Highspeed",
	"GLM-5", "GLM-5-Turbo", "GLM-4.7", "GLM-4.7-FlashX", "GLM-4.7-Flash", "GLM-4.6",
	"GLM-4.5-Air", "GLM-4.5", "GLM-4.6V", "GLM-4.6V-Flash", "GLM-4.6V-FlashX",
	"GLM-4.1V-Thinking-FlashX", "GLM-4.1V-Thinking-Flash", "GLM-4-FlashX-250414",
	"GLM-4-Flash-250414", "GLM-4V-Flash",
}

var officialGLMModelByLowercase = func() map[string]string {
	index := make(map[string]string, len(officialGLMModelIDs))
	for _, id := range officialGLMModelIDs {
		index[strings.ToLower(id)] = id
	}
	return index
}()

func normalizeOfficialGLMModelID(modelID string) string {
	if canonical, ok := officialGLMModelByLowercase[strings.ToLower(modelID)]; ok {
		return canonical
	}
	return modelID
}

func selectionValue(selection ModelSelection) map[string]any {
	value := map[string]any{
		"providerId": selection.ProviderID,
		"modelId":    selection.ModelID,
	}
	if len(selection.Options) > 0 {
		value["options"] = selection.Options
	}
	return value
}

func decodeModelSelection(value any) (ModelSelection, bool) {
	record, ok := value.(map[string]any)
	if !ok {
		return ModelSelection{}, false
	}
	providerID, ok := record["providerId"].(string)
	if !ok || providerID == "" {
		return ModelSelection{}, false
	}
	modelID, ok := record["modelId"].(string)
	if !ok || modelID == "" {
		return ModelSelection{}, false
	}
	selection := ModelSelection{ProviderID: providerID, ModelID: modelID}
	if options, ok := record["options"].(map[string]any); ok {
		selection.Options = options
	}
	return selection, true
}

func decodeObject(value json.RawMessage) (map[string]any, bool) {
	if len(value) == 0 {
		return nil, false
	}
	var decoded map[string]any
	if err := json.Unmarshal(value, &decoded); err != nil {
		return nil, false
	}
	// The TS isRecord rejects arrays too; unmarshalling into a map already does.
	if decoded == nil {
		return nil, false
	}
	return decoded, true
}

func mustMarshal(value any) json.RawMessage {
	data, err := json.Marshal(value)
	if err != nil {
		return json.RawMessage(`{}`)
	}
	return data
}
