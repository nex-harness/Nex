// builtinrelease.go ports packages/provider-node/src/nex-builtin-release.ts:
// the Nex Built-in Release decode/encode/serialize boundary, shared by the
// bundled-config materializer and the remote-release apply path.
package providerconfig

import (
	"encoding/json"
	"fmt"
)

// NexBuiltinReleaseSchemaVersion is NEX_BUILTIN_RELEASE_SCHEMA_VERSION.
const NexBuiltinReleaseSchemaVersion = 1

// RetiredZapiProviderID: ZAPI 已退出产品，旧 Remote Release 或 LKG 不能在
// Renderer 静态入口删除后又通过目标 Host Registry 将它重新发布.
const RetiredZapiProviderID = "builtin:zapi"

// BuiltinConfigContent is the release's config layer.
type BuiltinConfigContent struct {
	Providers         *ProviderConfigMap
	ProviderTemplates *ProviderTemplateMap
	ModelConfigRules  *ModelConfigRules
}

// NexBuiltinRelease is the decoded release.
type NexBuiltinRelease struct {
	SchemaVersion int
	Revision      int64
	Config        BuiltinConfigContent
}

type releaseWire struct {
	SchemaVersion int             `json:"schemaVersion"`
	Revision      int64           `json:"revision"`
	Config        json.RawMessage `json:"config"`
}

type releaseConfigWire struct {
	ProviderConfigRules struct {
		TemplateRules []templateRuleWire   `json:"templateRules"`
		ProviderRules []ProviderConfigRule `json:"providerRules"`
	} `json:"providerConfigRules"`
	ModelConfigRules json.RawMessage `json:"modelConfigRules"`
}

// templateRuleWire: template rules carry the name map; config is the overlay.
type templateRuleWire struct {
	TemplateID      string            `json:"templateId"`
	TemplateNameMap map[string]string `json:"templateNameMap"`
	Config          *ProviderConfig   `json:"config"`
}

// DecodeNexBuiltinRelease ports decodeNexBuiltinRelease. .strict() semantics:
// unknown top-level keys are rejected by the shape check below.
func DecodeNexBuiltinRelease(data []byte) (*NexBuiltinRelease, error) {
	var probe map[string]json.RawMessage
	if err := json.Unmarshal(data, &probe); err != nil {
		return nil, err
	}
	for _, key := range []string{"schemaVersion", "revision", "config"} {
		if _, ok := probe[key]; !ok {
			return nil, fmt.Errorf("nex builtin release missing %q", key)
		}
	}
	if len(probe) != 3 {
		return nil, fmt.Errorf("nex builtin release has unknown keys")
	}
	var wire releaseWire
	if err := json.Unmarshal(data, &wire); err != nil {
		return nil, err
	}
	if wire.SchemaVersion != NexBuiltinReleaseSchemaVersion {
		return nil, fmt.Errorf("nex builtin release schemaVersion must be %d", NexBuiltinReleaseSchemaVersion)
	}
	if wire.Revision < 0 {
		return nil, fmt.Errorf("nex builtin release revision must be non-negative")
	}
	var configWire releaseConfigWire
	if err := json.Unmarshal(wire.Config, &configWire); err != nil {
		return nil, err
	}
	if len(configWire.ProviderConfigRules.ProviderRules) == 0 && len(configWire.ProviderConfigRules.TemplateRules) == 0 {
		// The TS relies on the providerRules array being present; an empty file
		// still parses. Nothing to reject here.
	}
	providers, err := NewProviderConfigMap(configWire.ProviderConfigRules.ProviderRules)
	if err != nil {
		return nil, err
	}
	if providers.Has(RetiredZapiProviderID) {
		return nil, fmt.Errorf("Nex Built-in Release 包含已退出的 Provider: %s", RetiredZapiProviderID)
	}
	templates := make([]ProviderTemplate, 0, len(configWire.ProviderConfigRules.TemplateRules))
	seen := map[string]bool{}
	for _, rule := range configWire.ProviderConfigRules.TemplateRules {
		if seen[rule.TemplateID] {
			return nil, &DuplicateKeyError{Kind: "templateId", Key: rule.TemplateID}
		}
		seen[rule.TemplateID] = true
		templates = append(templates, ProviderTemplate{
			TemplateID: rule.TemplateID, TemplateNameMap: rule.TemplateNameMap, Config: rule.Config,
		})
	}
	templateMap, err := NewProviderTemplateMap(templates)
	if err != nil {
		return nil, err
	}
	modelRules, err := ParseBuiltinModelConfigRules(configWire.ModelConfigRules)
	if err != nil {
		return nil, err
	}
	return &NexBuiltinRelease{
		SchemaVersion: wire.SchemaVersion,
		Revision:      wire.Revision,
		Config: BuiltinConfigContent{
			Providers: providers, ProviderTemplates: templateMap, ModelConfigRules: modelRules,
		},
	}, nil
}

// EncodeNexBuiltinRelease ports encodeNexBuiltinRelease.
func EncodeNexBuiltinRelease(release *NexBuiltinRelease) map[string]any {
	templates := release.Config.ProviderTemplates
	if templates == nil {
		templates = EmptyProviderTemplateMap()
	}
	templateRules := make([]map[string]any, 0, len(templates.order))
	for _, tmpl := range templates.Entries() {
		templateRules = append(templateRules, map[string]any{
			"templateId": tmpl.TemplateID, "templateNameMap": tmpl.TemplateNameMap, "config": tmpl.Config,
		})
	}
	rules := release.Config.ModelConfigRules.ToNexBuiltinJSON()
	return map[string]any{
		"schemaVersion": NexBuiltinReleaseSchemaVersion,
		"revision":      release.Revision,
		"config": map[string]any{
			"providerConfigRules": map[string]any{
				"templateRules": templateRules,
				"providerRules": release.Config.Providers.ToJSON(),
			},
			"modelConfigRules": rules,
		},
	}
}

// SerializeNexBuiltinRelease ports serializeNexBuiltinRelease (compact JSON).
func SerializeNexBuiltinRelease(release *NexBuiltinRelease) string {
	encoded, err := json.Marshal(EncodeNexBuiltinRelease(release))
	if err != nil {
		return ""
	}
	return string(encoded)
}

// ReleaseSignature is signatureOf: revision + serialized content.
func ReleaseSignature(release *NexBuiltinRelease) string {
	return fmt.Sprintf("%d:%s", release.Revision, SerializeNexBuiltinRelease(release))
}

// SelectReleaseCandidate ports selectReleaseCandidate: same-revision content
// conflicts resolve to the trusted bundled release; otherwise the newest
// revision wins; none readable is an aggregate error.
func SelectReleaseCandidate(bundled, active *NexBuiltinRelease, bundledErr, activeErr error) (*NexBuiltinRelease, error) {
	if bundled != nil && active != nil &&
		bundled.Revision == active.Revision &&
		SerializeNexBuiltinRelease(bundled) != SerializeNexBuiltinRelease(active) {
		// 同 revision 冲突属于 Active 缓存失效，不能反向使可信 Bundled 无法启动.
		return bundled, nil
	}
	var valid []*NexBuiltinRelease
	if bundled != nil {
		valid = append(valid, bundled)
	}
	if active != nil {
		valid = append(valid, active)
	}
	if len(valid) == 0 {
		return nil, fmt.Errorf("Bundled 与 Active Nex Built-in Release 均不可用 (bundled=%v, active=%v)", bundledErr, activeErr)
	}
	newest := valid[0]
	for _, candidate := range valid[1:] {
		if candidate.Revision > newest.Revision {
			newest = candidate
		}
	}
	return newest, nil
}
