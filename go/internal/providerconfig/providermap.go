// providermap.go: the ProviderConfigMap ordered index (the ProviderConfigMap
// class in packages/provider/src/config/provider-config.ts) plus the rule
// pattern matcher (matchesRule / patternSchema).
package providerconfig

import "regexp"

// ProviderConfigMap ports ProviderConfigMap: an ordered rule index. The TS
// relies on Map insertion order; Go keeps an explicit order slice.
type ProviderConfigMap struct {
	order []string
	rules map[string]ProviderConfigRule
}

// NewProviderConfigMap builds from rules; duplicate ids are an error (the TS
// constructor throws 重复 Provider key).
func NewProviderConfigMap(rules []ProviderConfigRule) (*ProviderConfigMap, error) {
	m := &ProviderConfigMap{rules: map[string]ProviderConfigRule{}}
	for _, rule := range rules {
		if _, exists := m.rules[rule.ProviderID]; exists {
			return nil, &DuplicateKeyError{Kind: "Provider", Key: rule.ProviderID}
		}
		m.set(rule)
	}
	return m, nil
}

// EmptyProviderConfigMap returns the shared empty map.
func EmptyProviderConfigMap() *ProviderConfigMap {
	return &ProviderConfigMap{rules: map[string]ProviderConfigRule{}}
}

// DuplicateKeyError ports the duplicate-key constructor errors.
type DuplicateKeyError struct{ Kind, Key string }

func (e *DuplicateKeyError) Error() string { return "重复 " + e.Kind + " key: " + e.Key }

func (m *ProviderConfigMap) set(rule ProviderConfigRule) {
	if _, exists := m.rules[rule.ProviderID]; !exists {
		m.order = append(m.order, rule.ProviderID)
	}
	m.rules[rule.ProviderID] = rule
}

// Clone deep-copies (the TS freezes; snapshots must not alias mutable leaves).
func (m *ProviderConfigMap) Clone() *ProviderConfigMap {
	if m == nil {
		return EmptyProviderConfigMap()
	}
	out := &ProviderConfigMap{rules: map[string]ProviderConfigRule{}, order: cloneStrings(m.order)}
	for id, rule := range m.rules {
		out.rules[id] = rule.Clone()
	}
	return out
}

// Keys returns provider ids in insertion order.
func (m *ProviderConfigMap) Keys() []string { return cloneStrings(m.order) }

// Rules returns the rules in insertion order.
func (m *ProviderConfigMap) Rules() []ProviderConfigRule {
	out := make([]ProviderConfigRule, 0, len(m.order))
	for _, id := range m.order {
		out = append(out, m.rules[id])
	}
	return out
}

// Get returns the config for id, or nil.
func (m *ProviderConfigMap) Get(providerID string) *ProviderConfig {
	rule := m.getRule(providerID)
	if rule == nil {
		return nil
	}
	return rule.Config
}

// GetRule returns the full rule or nil.
func (m *ProviderConfigMap) getRule(providerID string) *ProviderConfigRule {
	if m == nil {
		return nil
	}
	rule, ok := m.rules[providerID]
	if !ok {
		return nil
	}
	return &rule
}

// GetRuleExported is the exported read the facades need.
func (m *ProviderConfigMap) GetRule(providerID string) *ProviderConfigRule {
	return m.getRule(providerID)
}

// Has reports membership.
func (m *ProviderConfigMap) Has(providerID string) bool {
	if m == nil {
		return false
	}
	_, ok := m.rules[providerID]
	return ok
}

// Len is the rule count.
func (m *ProviderConfigMap) Len() int {
	if m == nil {
		return 0
	}
	return len(m.rules)
}

// SetRule replaces/inserts one rule, returning a new map.
func (m *ProviderConfigMap) SetRule(rule ProviderConfigRule) *ProviderConfigMap {
	out := m.Clone()
	out.set(rule)
	return out
}

// Set replaces the config leaf of one rule (keeping identity metadata), like
// set() on the TS.
func (m *ProviderConfigMap) Set(providerID string, config *ProviderConfig) *ProviderConfigMap {
	rule := ProviderConfigRule{ProviderID: providerID, Config: config}
	if existing := m.getRule(providerID); existing != nil {
		rule = *existing
		rule.Config = config
	}
	return m.SetRule(rule)
}

// Delete removes one rule, returning a new map.
func (m *ProviderConfigMap) Delete(providerID string) *ProviderConfigMap {
	out := &ProviderConfigMap{rules: map[string]ProviderConfigRule{}}
	for _, id := range m.order {
		if id == providerID {
			continue
		}
		out.set(m.rules[id])
	}
	return out
}

// Overlay ports ProviderConfigMap.overlay: next's rules merge over base's —
// existing rules overlay config + identity leaves, new rules insert (at their
// position in next).
func (m *ProviderConfigMap) Overlay(next *ProviderConfigMap) *ProviderConfigMap {
	if next == nil || next.Len() == 0 {
		return m
	}
	if m == nil || m.Len() == 0 {
		return next
	}
	out := m.Clone()
	for _, id := range next.order {
		nextRule := next.rules[id]
		if current, ok := out.rules[id]; ok {
			merged := current
			merged.TemplateID = overlayPtr(current.TemplateID, nextRule.TemplateID)
			merged.ProviderName = overlayPtr(current.ProviderName, nextRule.ProviderName)
			merged.Enabled = overlayPtr(current.Enabled, nextRule.Enabled)
			merged.Config = current.Config.Overlay(nextRule.Config)
			out.rules[id] = merged
		} else {
			out.set(nextRule)
		}
	}
	return out
}

// MapConfigs ports mapConfigs: transform each rule's config in place.
func (m *ProviderConfigMap) MapConfigs(transform func(config *ProviderConfig, providerID string, rule *ProviderConfigRule) *ProviderConfig) *ProviderConfigMap {
	out := &ProviderConfigMap{rules: map[string]ProviderConfigRule{}}
	for _, id := range m.order {
		rule := m.rules[id]
		view := rule
		rule.Config = transform(rule.Config, id, &view)
		out.set(rule)
	}
	return out
}

// Reorder ports reorder: requested order first (existing rules only), then
// remaining rules in insertion order.
func (m *ProviderConfigMap) Reorder(providerIDs []string) *ProviderConfigMap {
	out := &ProviderConfigMap{rules: map[string]ProviderConfigRule{}}
	seen := map[string]bool{}
	for _, id := range providerIDs {
		if rule, ok := m.rules[id]; ok && !seen[id] {
			out.set(rule)
			seen[id] = true
		}
	}
	for _, id := range m.order {
		if !seen[id] {
			out.set(m.rules[id])
			seen[id] = true
		}
	}
	return out
}

// ToJSON renders the rules array (ProviderConfigRuleData[]), omitting unset
// identity leaves — byte parity with the TS toJSON.
func (m *ProviderConfigMap) ToJSON() []map[string]any {
	if m == nil {
		return []map[string]any{}
	}
	out := make([]map[string]any, 0, m.Len())
	for _, id := range m.order {
		rule := m.rules[id]
		entry := map[string]any{
			"providerId": rule.ProviderID,
			"config":     rule.Config,
		}
		if rule.TemplateID != nil {
			entry["templateId"] = *rule.TemplateID
		}
		if rule.ProviderName != nil {
			entry["providerName"] = *rule.ProviderName
		}
		if rule.Enabled != nil {
			entry["enabled"] = *rule.Enabled
		}
		out = append(out, entry)
	}
	return out
}

// ValidateModelIDs ports freezeModelIds: each id must trim non-empty.
func ValidateModelIDs(ids []string) error {
	for _, id := range ids {
		if _, ok := normalizeModelID(id); !ok {
			return errModelIDEmpty
		}
	}
	return nil
}

var errModelIDEmpty = jsonError("Model ID 不能为空")

func jsonError(msg string) error { return &simpleError{msg} }

type simpleError struct{ msg string }

func (e *simpleError) Error() string { return e.msg }

// PatternCache matches rule patterns ("^(?:pattern)$") — matchesRule. Patterns
// are validated once on decode (patternSchema); a broken pattern at match
// time simply does not match, mirroring the TS RegExp throw containment.
var patternCache = map[string]*regexp.Regexp{}

// MatchesRule ports matchesRule(pattern, value, ignoreCase).
func MatchesRule(pattern, value string, ignoreCase bool) bool {
	cacheKey := pattern
	if ignoreCase {
		cacheKey += "\x00i"
	}
	re, ok := patternCache[cacheKey]
	if !ok {
		flags := ""
		if ignoreCase {
			flags = "(?i)"
		}
		compiled, err := regexp.Compile(flags + "^(?:" + pattern + ")$")
		if err != nil {
			return false
		}
		re = compiled
		patternCache[cacheKey] = re
	}
	return re.MatchString(value)
}

// ValidPattern reports whether a rule pattern compiles (patternSchema).
func ValidPattern(pattern string) bool {
	_, err := regexp.Compile("^(?:" + pattern + ")$")
	return err == nil
}
