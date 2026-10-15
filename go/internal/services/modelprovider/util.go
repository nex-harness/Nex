// util.go: small helpers shared by the configservice and facades.
package modelprovider

import (
	"encoding/json"

	"github.com/tylinux/nex/go/internal/providerconfig"
)

// strPtr / boolPtr / optionalStr are the leaf constructors.
func strPtr(v string) *string { return &v }
func boolPtr(v bool) *bool    { return &v }
func optionalStr(v string) *string {
	if v == "" {
		return nil
	}
	return &v
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func derefOr(s *string, fallback string) string {
	if s == nil {
		return fallback
	}
	return *s
}

func orDefault(v, fallback string) string {
	if v == "" {
		return fallback
	}
	return v
}

func orConfig(c *providerconfig.ProviderConfig) *providerconfig.ProviderConfig {
	if c == nil {
		return &providerconfig.ProviderConfig{}
	}
	return c
}

func orStrings(v []string) []string { return v }

func orEmpty(v []string) []string {
	if v == nil {
		return []string{}
	}
	return v
}

func containsStr(values []string, target string) bool {
	for _, v := range values {
		if v == target {
			return true
		}
	}
	return false
}

func uniqueStrings(values []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(values))
	for _, v := range values {
		if seen[v] {
			continue
		}
		seen[v] = true
		out = append(out, v)
	}
	return out
}

func stringSetOf(values []string) map[string]bool {
	set := map[string]bool{}
	for _, v := range values {
		set[v] = true
	}
	return set
}

func jsonMarshalString(v any) (string, error) {
	data, err := json.Marshal(v)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

// jsonUnmarshalHelper decodes JSON into the target (kept here so
// legacyimporter.go stays free of an encoding/json import).
func jsonUnmarshalHelper(data []byte, target any) error {
	return json.Unmarshal(data, target)
}
