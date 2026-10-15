// templatemap.go: the ordered ProviderTemplateMap index (provider-config.ts).
package providerconfig

// ProviderTemplateMap ports ProviderTemplateMap (ordered).
type ProviderTemplateMap struct {
	order     []string
	templates map[string]ProviderTemplate
}

// NewProviderTemplateMap builds from templates (dup keys error).
func NewProviderTemplateMap(templates []ProviderTemplate) (*ProviderTemplateMap, error) {
	m := &ProviderTemplateMap{templates: map[string]ProviderTemplate{}}
	for _, tmpl := range templates {
		if _, exists := m.templates[tmpl.TemplateID]; exists {
			return nil, &DuplicateKeyError{Kind: "Provider Template", Key: tmpl.TemplateID}
		}
		m.order = append(m.order, tmpl.TemplateID)
		m.templates[tmpl.TemplateID] = tmpl
	}
	return m, nil
}

// EmptyProviderTemplateMap is the shared empty map.
func EmptyProviderTemplateMap() *ProviderTemplateMap {
	return &ProviderTemplateMap{templates: map[string]ProviderTemplate{}}
}

// Get / Has / Keys / Entries.
func (m *ProviderTemplateMap) Get(id string) *ProviderTemplate {
	if m == nil {
		return nil
	}
	if tmpl, ok := m.templates[id]; ok {
		return &tmpl
	}
	return nil
}
func (m *ProviderTemplateMap) Has(id string) bool { return m.Get(id) != nil }
func (m *ProviderTemplateMap) Keys() []string {
	if m == nil {
		return nil
	}
	return cloneStrings(m.order)
}
func (m *ProviderTemplateMap) Entries() []ProviderTemplate {
	if m == nil {
		return nil
	}
	out := make([]ProviderTemplate, 0, len(m.order))
	for _, id := range m.order {
		out = append(out, m.templates[id])
	}
	return out
}

// Overlay ports ProviderTemplateMap.overlay: next's entries replace.
func (m *ProviderTemplateMap) Overlay(next *ProviderTemplateMap) *ProviderTemplateMap {
	if next == nil || len(next.order) == 0 {
		return m
	}
	if m == nil || len(m.order) == 0 {
		return next
	}
	out := &ProviderTemplateMap{templates: map[string]ProviderTemplate{}}
	for _, id := range m.order {
		out.order = append(out.order, id)
		out.templates[id] = m.templates[id]
	}
	for _, id := range next.order {
		if _, exists := out.templates[id]; !exists {
			out.order = append(out.order, id)
		}
		out.templates[id] = next.templates[id]
	}
	return out
}
