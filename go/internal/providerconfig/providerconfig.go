// providerconfig.go ports packages/provider/src/config/provider-config.ts:
// ApiKeyAccess / ZhipuAccountAccess / ProviderApi / ProviderConfig overlays,
// the ProviderConfigRule/ProviderConfigMap index, and the ProviderTemplate
// shell. Access/config overlays follow the same leaf rules as modelconfig.go.
//
// 架构说明（对应 TS 注释保留）：Template 是元数据与 Provider Overlay 的领域壳，
// 本身不是 Provider；Template 永不进入 Registry。
package providerconfig

import (
	"encoding/json"
)

// Access type discriminators (provider-data-schema.ts).
const (
	AccessAPIKey                = "api-key"
	AccessZhipuCodingPlanAPIKey = "zhipu-coding-plan-api-key"
	AccessZhipuAccount          = "zhipu-account"
)

// API types (providerApiTypeDataSchema).
const (
	APIAnthropicMessages     = "anthropic-messages"
	APIOpenAIChatCompletions = "openai-chat-completions"
	APIOpenAIResponses       = "openai-responses"
)

// Provider groups (providerGroupDataSchema).
const (
	GroupStandardPersonal = "standard-personal"
	GroupZaiFamily        = "zai-family"
	GroupBigmodelFamily   = "bigmodel-family"
)

// IsAPIKeyAccess ports isApiKeyAccess: manual Key edit/save share the
// capability check, 不把套餐 Key 误写成普通 API Key.
func IsAPIKeyAccess(accessType string) bool {
	return accessType == AccessAPIKey || accessType == AccessZhipuCodingPlanAPIKey
}

// ProviderApiConfig ports ProviderApiConfig.
type ProviderApiConfig struct {
	Type    *string           `json:"type,omitempty"`
	BaseURL *string           `json:"baseUrl,omitempty"`
	Headers map[string]string `json:"headers,omitempty"`
}

// Overlay ports ProviderApiConfig.overlay.
func (a *ProviderApiConfig) Overlay(next *ProviderApiConfig) *ProviderApiConfig {
	if next == nil {
		return a
	}
	if a == nil {
		return next
	}
	merged := overlayStringMap(a.Headers, next.Headers)
	return &ProviderApiConfig{
		Type:    overlayPtr(a.Type, next.Type),
		BaseURL: overlayPtr(a.BaseURL, next.BaseURL),
		Headers: merged,
	}
}

func overlayStringMap(base, next map[string]string) map[string]string {
	if next == nil {
		return base
	}
	if base == nil {
		return next
	}
	merged := make(map[string]string, len(base)+len(next))
	for k, v := range base {
		merged[k] = v
	}
	for k, v := range next {
		merged[k] = v
	}
	return merged
}

// ProviderLogoRef ports providerLogoDataSchema ({type:"builtin", key}).
type ProviderLogoRef struct {
	Type string `json:"type"`
	Key  string `json:"key"`
}

// ProviderConfig ports ProviderConfig (all leaves optional).
type ProviderConfig struct {
	Group            *string            `json:"group,omitempty"`
	Logo             *ProviderLogoRef   `json:"logo,omitempty"`
	Access           *ProviderAccess    `json:"-"`
	API              *ProviderApiConfig `json:"-"`
	BuiltinModelIDs  []string           `json:"builtinModelIds,omitempty"`
	PersonalModelIDs []string           `json:"personalModelIds,omitempty"`
	ModelOrder       []string           `json:"modelOrder,omitempty"`
	Visibility       *string            `json:"visibility,omitempty"`
}

// providerConfigWire is the JSON envelope for ProviderConfig: access/api are
// unions needing custom decode, so they round-trip through raw messages.
type providerConfigWire struct {
	Group            *string          `json:"group,omitempty"`
	Logo             *ProviderLogoRef `json:"logo,omitempty"`
	Access           json.RawMessage  `json:"access,omitempty"`
	API              json.RawMessage  `json:"api,omitempty"`
	BuiltinModelIDs  []string         `json:"builtinModelIds,omitempty"`
	PersonalModelIDs []string         `json:"personalModelIds,omitempty"`
	ModelOrder       []string         `json:"modelOrder,omitempty"`
	Visibility       *string          `json:"visibility,omitempty"`
}

// MarshalJSON keeps absent access/api keys absent when nil.
func (c *ProviderConfig) MarshalJSON() ([]byte, error) {
	wire := providerConfigWire{
		Group: c.Group, Logo: c.Logo,
		BuiltinModelIDs: c.BuiltinModelIDs, PersonalModelIDs: c.PersonalModelIDs,
		ModelOrder: c.ModelOrder, Visibility: c.Visibility,
	}
	if c.Access != nil {
		if b, err := json.Marshal(c.Access.ToObject()); err == nil {
			wire.Access = b
		} else {
			return nil, err
		}
	}
	if c.API != nil {
		if b, err := json.Marshal(c.API); err == nil {
			wire.API = b
		} else {
			return nil, err
		}
	}
	return json.Marshal(wire)
}

// UnmarshalJSON decodes the envelope including the access union.
func (c *ProviderConfig) UnmarshalJSON(data []byte) error {
	var wire providerConfigWire
	if err := json.Unmarshal(data, &wire); err != nil {
		return err
	}
	*c = ProviderConfig{
		Group: wire.Group, Logo: wire.Logo,
		BuiltinModelIDs: wire.BuiltinModelIDs, PersonalModelIDs: wire.PersonalModelIDs,
		ModelOrder: wire.ModelOrder, Visibility: wire.Visibility,
	}
	if len(wire.Access) > 0 && string(wire.Access) != "null" {
		access, err := ParseAccess(wire.Access)
		if err != nil {
			return err
		}
		c.Access = access
	}
	if len(wire.API) > 0 && string(wire.API) != "null" {
		var api ProviderApiConfig
		if err := json.Unmarshal(wire.API, &api); err != nil {
			return err
		}
		c.API = &api
	}
	return nil
}

// Overlay ports ProviderConfig.overlay.
func (c *ProviderConfig) Overlay(next *ProviderConfig) *ProviderConfig {
	if next == nil {
		return c
	}
	if c == nil {
		return next
	}
	return &ProviderConfig{
		Group:            overlayPtr(c.Group, next.Group),
		Logo:             overlayPtr(c.Logo, next.Logo),
		Access:           OverlayAccess(c.Access, next.Access),
		API:              c.API.Overlay(next.API),
		BuiltinModelIDs:  overlayStrings(c.BuiltinModelIDs, next.BuiltinModelIDs),
		PersonalModelIDs: overlayStrings(c.PersonalModelIDs, next.PersonalModelIDs),
		ModelOrder:       overlayStrings(c.ModelOrder, next.ModelOrder),
		Visibility:       overlayPtr(c.Visibility, next.Visibility),
	}
}

func overlayStrings(base, next []string) []string {
	if next == nil {
		return base
	}
	return next
}

// WithoutGroup ports withoutGroup: overlays on a builtin provider drop the
// group leaf so the builtin's group stays authoritative.
func (c *ProviderConfig) WithoutGroup() *ProviderConfig {
	if c == nil {
		return nil
	}
	return &ProviderConfig{
		Logo:             c.Logo,
		Access:           c.Access,
		API:              c.API,
		BuiltinModelIDs:  c.BuiltinModelIDs,
		PersonalModelIDs: c.PersonalModelIDs,
		ModelOrder:       c.ModelOrder,
		Visibility:       c.Visibility,
	}
}

// WithModelMembershipFrom ports withModelMembershipFrom: 普通 Provider 字段
// 保存不拥有成员变更；成员只能由明确的领域操作更新。
func (c *ProviderConfig) WithModelMembershipFrom(source *ProviderConfig) *ProviderConfig {
	if c == nil {
		return nil
	}
	return &ProviderConfig{
		Group:            c.Group,
		Logo:             c.Logo,
		Access:           c.Access,
		API:              c.API,
		BuiltinModelIDs:  sourceMembership(source, func(p *ProviderConfig) []string { return p.BuiltinModelIDs }),
		PersonalModelIDs: sourceMembership(source, func(p *ProviderConfig) []string { return p.PersonalModelIDs }),
		ModelOrder:       sourceMembership(source, func(p *ProviderConfig) []string { return p.ModelOrder }),
		Visibility:       c.Visibility,
	}
}

func sourceMembership(source *ProviderConfig, pick func(*ProviderConfig) []string) []string {
	if source == nil {
		return nil
	}
	return pick(source)
}

// WithBuiltinModelIDs / WithPersonalModelIds / WithModelOrder port the same
// one-leaf overlay helpers.
func (c *ProviderConfig) WithBuiltinModelIDs(ids []string) *ProviderConfig {
	return c.Overlay(&ProviderConfig{BuiltinModelIDs: ids})
}
func (c *ProviderConfig) WithPersonalModelIDs(ids []string) *ProviderConfig {
	return c.Overlay(&ProviderConfig{PersonalModelIDs: ids})
}
func (c *ProviderConfig) WithModelOrder(order []string) *ProviderConfig {
	return c.Overlay(&ProviderConfig{ModelOrder: order})
}

// Clone copies the config (the TS freezes; Go call sites must not share
// mutable leaves across snapshots, so builders clone on write).
func (c *ProviderConfig) Clone() *ProviderConfig {
	if c == nil {
		return nil
	}
	dup := *c
	dup.BuiltinModelIDs = cloneStrings(c.BuiltinModelIDs)
	dup.PersonalModelIDs = cloneStrings(c.PersonalModelIDs)
	dup.ModelOrder = cloneStrings(c.ModelOrder)
	return &dup
}

func cloneStrings(values []string) []string {
	if values == nil {
		return nil
	}
	out := make([]string, len(values))
	copy(out, values)
	return out
}

// ProviderConfigRule ports the rule (identity outside the config overlay).
type ProviderConfigRule struct {
	ProviderID   string          `json:"providerId"`
	TemplateID   *string         `json:"templateId,omitempty"`
	ProviderName *string         `json:"providerName,omitempty"`
	Enabled      *bool           `json:"enabled,omitempty"`
	Config       *ProviderConfig `json:"config"`
}

// Clone copies the rule and its config.
func (r ProviderConfigRule) Clone() ProviderConfigRule {
	name := r.ProviderName
	if name != nil {
		v := *name
		name = &v
	}
	tmpl := r.TemplateID
	if tmpl != nil {
		v := *tmpl
		tmpl = &v
	}
	enabled := r.Enabled
	if enabled != nil {
		v := *enabled
		enabled = &v
	}
	return ProviderConfigRule{
		ProviderID: r.ProviderID, TemplateID: tmpl, ProviderName: name,
		Enabled: enabled, Config: r.Config.Clone(),
	}
}
