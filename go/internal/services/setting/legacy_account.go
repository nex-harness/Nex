// legacy_account.go ports legacyAccountConnectionSettings.ts verbatim: the
// old modelProviderFamily* navigation keys are only interpreted at the
// settings-file read boundary; runtime code must not read them.

package setting

import (
	"encoding/json"
	"strings"
)

// ProviderFamilyDomains are the two families the legacy migration knows.
var ProviderFamilyDomains = []string{"zai", "bigmodel"}

// LegacyTeamConnection is LegacyTeamConnection.
type LegacyTeamConnection struct {
	Family    string `json:"family"`
	ProductID string `json:"productId"`
	ProjectID string `json:"projectId"`
}

func legacyRecord(value any) map[string]any {
	obj, ok := value.(map[string]any)
	if !ok {
		return map[string]any{}
	}
	return obj
}

// NeedsLegacyAccountConnectionMigration ports
// needsLegacyAccountConnectionMigration.
func NeedsLegacyAccountConnectionMigration(value any) bool {
	raw := legacyRecord(value)
	if _, has := raw["providerFamilyConnectionSelections"]; has {
		return false
	}
	_, hasModes := raw["modelProviderFamilyModes"]
	_, hasKeys := raw["modelProviderFamilySelectedKeys"]
	return hasModes || hasKeys
}

// ReadIncompleteLegacyTeamConnections ports
// readIncompleteLegacyTeamConnections: team-plan keys without an organization
// (2 parts) still need the Host's OAuth query.
func ReadIncompleteLegacyTeamConnections(value any) []LegacyTeamConnection {
	if !NeedsLegacyAccountConnectionMigration(value) {
		return nil
	}
	raw := legacyRecord(value)
	modes := legacyRecord(raw["modelProviderFamilyModes"])
	keys := legacyRecord(raw["modelProviderFamilySelectedKeys"])
	var out []LegacyTeamConnection
	for _, family := range ProviderFamilyDomains {
		if mode, _ := modes[family].(string); mode == "apiKey" {
			continue
		}
		prefix := "team-plan:builtin:" + family + "-coding-plan:"
		key := ""
		if s, ok := keys[family].(string); ok {
			key = trimSpace(s)
		}
		if !strings.HasPrefix(key, prefix) {
			continue
		}
		parts := strings.Split(key[len(prefix):], ":")
		if len(parts) != 2 {
			continue
		}
		productID, projectID := decodeURIComponent(parts[0]), decodeURIComponent(parts[1])
		if trimSpace(productID) == "" || trimSpace(projectID) == "" {
			continue
		}
		out = append(out, LegacyTeamConnection{
			Family:    family,
			ProductID: trimSpace(productID),
			ProjectID: trimSpace(projectID),
		})
	}
	return out
}

// MigrateLegacyAccountConnectionSettings ports the same-named TS function:
// synthesize providerFamilyConnectionSelections from the legacy keys.
func MigrateLegacyAccountConnectionSettings(value any) map[string]any {
	if !NeedsLegacyAccountConnectionMigration(value) {
		if raw, ok := value.(map[string]any); ok {
			return raw
		}
		return map[string]any{}
	}
	raw := cloneMap(legacyRecord(value))
	modes := legacyRecord(raw["modelProviderFamilyModes"])
	keys := legacyRecord(raw["modelProviderFamilySelectedKeys"])
	selections := map[string]any{}
	for _, family := range ProviderFamilyDomains {
		// API 实例由 Provider 配置迁移负责；旧 API 模式不是一个账号套餐。
		if mode, _ := modes[family].(string); mode == "apiKey" {
			continue
		}
		key := ""
		if s, ok := keys[family].(string); ok {
			key = trimSpace(s)
		}
		switch key {
		case "coding-plan:builtin:" + family + "-start-plan":
			selections[family] = map[string]any{"kind": "start-plan"}
		case "coding-plan:builtin:" + family + "-coding-plan":
			selections[family] = map[string]any{"kind": "individual-coding-plan"}
		default:
			prefix := "team-plan:builtin:" + family + "-coding-plan:"
			if !strings.HasPrefix(key, prefix) {
				continue
			}
			parts := strings.Split(key[len(prefix):], ":")
			if len(parts) != 3 {
				// 旧 project-only key 无法确定组织，保留原字段而非捏造新版 Team 身份。
				continue
			}
			productID := trimSpace(decodeURIComponent(parts[0]))
			organizationID := trimSpace(decodeURIComponent(parts[1]))
			projectID := trimSpace(decodeURIComponent(parts[2]))
			if productID == "" || organizationID == "" || projectID == "" {
				continue
			}
			selections[family] = map[string]any{
				"kind":           "team-coding-plan",
				"productId":      productID,
				"organizationId": organizationID,
				"projectId":      projectID,
			}
		}
	}
	raw["providerFamilyConnectionSelections"] = selections
	return raw
}

// RetainLegacyAccountConnectionFields ports retainLegacyAccountConnectionFields:
// the old keys ride along on writes for rollback, in TS declaration order.
func RetainLegacyAccountConnectionFields(value any) *Ordered {
	raw := legacyRecord(value)
	out := NewOrdered()
	for _, key := range []string{"modelProviderFamilyModes", "modelProviderFamilySelectedKeys"} {
		if v, has := raw[key]; has {
			out.Set(key, v)
		}
	}
	return out
}

// ReadRawSettingsFile reads the raw JSON object from a settings file ({} when
// missing; other errors propagate like the TS).
func ReadRawSettingsFile(data []byte) (map[string]any, error) {
	var parsed any
	if err := json.Unmarshal(data, &parsed); err != nil {
		return nil, err
	}
	return legacyRecord(parsed), nil
}

// decodeURIComponent ports decodeURIComponent for the ASCII-safe inputs the
// legacy keys use. Malformed escapes pass through (the TS try/catch drops the
// whole entry on URIError; here per-part pass-through matches that contract
// because the trimmed-empty check then rejects the part).
func decodeURIComponent(s string) string {
	if !strings.Contains(s, "%") {
		return s
	}
	if out, err := urlUnescape(s); err == nil {
		return out
	}
	return s
}
