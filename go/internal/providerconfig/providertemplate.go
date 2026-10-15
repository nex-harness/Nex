// providertemplate.go: the ProviderTemplate shell and name resolution
// (provider-config.ts). Template 是元数据与 Provider Overlay 的领域壳，本身不是
// Provider；Template 永不进入 Registry.
package providerconfig

import "strings"

// ProviderTemplate ports ProviderTemplate (metadata + config overlay shell).
type ProviderTemplate struct {
	TemplateID      string            `json:"templateId"`
	TemplateNameMap map[string]string `json:"templateNameMap"`
	Config          *ProviderConfig   `json:"config"`
}

// ResolveProviderTemplateName ports resolveProviderTemplateName: locale →
// en-US → templateId.
func ResolveProviderTemplateName(templateID string, nameMap map[string]string, locale string) string {
	for _, key := range []string{locale, "en-US"} {
		if v, ok := nameMap[key]; ok && strings.TrimSpace(v) != "" {
			return v
		}
	}
	return templateID
}
