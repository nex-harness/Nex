// normalize.go: NormalizeSettings turns a validated raw settings map into the
// full parse output (defaults applied, unknown keys dropped, schema key order)
// — the exact value appSettingsSchema.parse produces. NormalizePatch ports
// normalizeSettingsPatch.ts verbatim.

package setting

import (
	"strings"
)

// NormalizeSettings applies defaults over the validated raw map and returns
// the schema-ordered parse output. raw may be nil.
func NormalizeSettings(raw map[string]any) *Ordered {
	out := NewOrdered()
	defaults := DefaultSettings()
	for _, key := range settingsFieldOrder {
		value, present := raw[key]
		if !present || value == nil {
			if defaultValue, hasDefault := defaults.Get(key); hasDefault {
				out.Set(key, deepCloneJSON(defaultValue))
			}
			continue
		}
		out.Set(key, value)
	}
	return out
}

// NormalizePatch ports normalizeSettingsPatch.ts. The patch is a plain map
// because callers hand over decoded RPC JSON; returned keys/absence mirror the
// TS exactly (undefined-valued keys are DELETED, e.g. empty-string proxies).
func NormalizePatch(patch map[string]any) map[string]any {
	normalized := cloneMap(patch)

	// locale → localePreference (only when the patch did not set it).
	if _, hasLocale := normalized["locale"]; hasLocale {
		if _, hasPreference := normalized["localePreference"]; !hasPreference {
			if s, ok := normalized["locale"].(string); ok {
				// locale 现在只表示已解析后的实际语言，localePreference 才表示用户偏好。
				// 兼容旧调用只写 locale 的路径，把它视为用户显式选择固定语言。
				normalized["localePreference"] = s
			}
		}
	}

	// terminalFontFamily: trim; empty → delete (回到系统 profile 自动探测).
	if value, ok := normalized["terminalFontFamily"]; ok {
		if s, isStr := value.(string); isStr {
			trimmed := trimSpace(s)
			if trimmed != "" {
				normalized["terminalFontFamily"] = trimmed
			} else {
				delete(normalized, "terminalFontFamily")
			}
		}
	}

	// integratedTerminalShell: auto → delete; shell → trim fields.
	if value, ok := normalized["integratedTerminalShell"]; ok {
		if selection, isObj := value.(map[string]any); isObj {
			mode, _ := selection["mode"].(string)
			if mode == "auto" {
				// 设置页的"自动选择"表示移除用户覆盖。
				delete(normalized, "integratedTerminalShell")
			} else if mode == "shell" {
				trimmed := cloneMap(selection)
				for _, key := range []string{"id", "label", "path"} {
					if s, ok := trimmed[key].(string); ok {
						trimmed[key] = trimSpace(s)
					}
				}
				normalized["integratedTerminalShell"] = trimmed
			}
		}
	}

	// Empty-string → delete for the clearable string fields (RPC 传输会吞掉
	// undefined，空串语义由这里归一).
	for _, key := range []string{
		"httpProxy", "httpProxyNoProxy", "httpProxyCaCertPath",
		"nexEndpointOrigin", "providerFamilyDomain",
	} {
		if value, ok := normalized[key]; ok {
			if s, isStr := value.(string); isStr {
				trimmed := trimSpace(s)
				if trimmed != "" {
					normalized[key] = trimmed
				} else {
					delete(normalized, key)
				}
			}
		}
	}
	return normalized
}

// DedupeRecentProjects ports the update() step: unique + first MAX_RECENT_PROJECTS.
func DedupeRecentProjects(projects []any) []any {
	seen := map[string]bool{}
	out := make([]any, 0, len(projects))
	for _, item := range projects {
		s, ok := item.(string)
		if !ok || seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
		if len(out) >= MaxRecentProjects {
			break
		}
	}
	return out
}

// MergePatch merges a validated patch over the current full settings, producing
// the schema-ordered merged value (the TS `{...current, ...validatedPatch}`
// then re-parse — the re-parse normalizes key order back to schema order, so
// the Go merge writes schema order directly).
func MergePatch(current *Ordered, patch map[string]any) *Ordered {
	merged := current.Clone()
	for key, value := range patch {
		merged.Set(key, deepCloneJSON(value))
	}
	// Re-apply key order the way the final appSettingsSchema.parse does.
	return OrderedFromMap(merged.Map(), settingsFieldOrder)
}

// settingsEqualJSON compares two settings values the way the TS does for the
// account-settings guard: JSON.stringify equality of
// providerFamilyConnectionSelections.
func settingsEqualJSON(a, b any) bool {
	return jsonEncode(a) == jsonEncode(b)
}

func jsonEncode(v any) string {
	if v == nil {
		return "null"
	}
	if ordered, ok := v.(*Ordered); ok {
		data, err := ordered.MarshalJSON()
		if err != nil {
			return ""
		}
		return string(data)
	}
	if obj, ok := v.(map[string]any); ok {
		wrapped := OrderedFromMap(obj, nil)
		data, err := wrapped.MarshalJSON()
		if err != nil {
			return ""
		}
		return string(data)
	}
	if s, ok := v.(string); ok {
		return `"` + strings.ReplaceAll(s, `"`, `\"`) + `"`
	}
	// Numbers/bools: JS JSON.stringify renders integral numbers without ".0".
	switch t := v.(type) {
	case bool:
		if t {
			return "true"
		}
		return "false"
	case int64:
		return itoa(t)
	case int:
		return itoa(int64(t))
	case float64:
		if t == float64(int64(t)) {
			return itoa(int64(t))
		}
		return ftoa(t)
	case []any:
		parts := make([]string, len(t))
		for i, item := range t {
			parts[i] = jsonEncode(item)
		}
		return "[" + strings.Join(parts, ",") + "]"
	default:
		return ""
	}
}
