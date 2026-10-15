// accessunion.go: the provider access union (api-key / zhipu-coding-plan-api-key /
// zhipu-account) with its overlay + parse rules (provider-config.ts).
package providerconfig

import "encoding/json"

// ApiKeyAccessConfig ports ApiKeyAccessConfig.
type ApiKeyAccessConfig struct {
	Type                string  `json:"type"`
	APIKey              *string `json:"apiKey,omitempty"`
	APIKeyManagementURL *string `json:"apiKeyManagementUrl,omitempty"`
}

// ZhipuAccountAccessConfig ports ZhipuAccountAccessConfig.
type ZhipuAccountAccessConfig struct {
	Type        string  `json:"type"`
	AccountType *string `json:"accountType,omitempty"`
	Mode        *string `json:"mode,omitempty"`
	Entitled    *bool   `json:"entitled,omitempty"`
}

// ProviderAccess is the discriminated union (type field).
type ProviderAccess struct {
	APIKey       *ApiKeyAccessConfig
	ZhipuAccount *ZhipuAccountAccessConfig
}

// AccessType returns the discriminator, "" when unset.
func (a *ProviderAccess) AccessType() string {
	if a == nil {
		return ""
	}
	if a.APIKey != nil {
		return a.APIKey.Type
	}
	if a.ZhipuAccount != nil {
		return a.ZhipuAccount.Type
	}
	return ""
}

// ToObject serializes the access union.
func (a *ProviderAccess) ToObject() any {
	if a == nil {
		return nil
	}
	if a.APIKey != nil {
		return a.APIKey
	}
	if a.ZhipuAccount != nil {
		return a.ZhipuAccount
	}
	return nil
}

// OverlayAccess ports overlayProviderAccess: next==nil keeps current; an
// explicit next replaces null/undefined current; equal types merge leaf-wise;
// a type change replaces whole.
func OverlayAccess(current, next *ProviderAccess) *ProviderAccess {
	if next == nil {
		return current
	}
	if current == nil {
		return next
	}
	if current.AccessType() != next.AccessType() {
		return next
	}
	switch {
	case current.APIKey != nil && next.APIKey != nil:
		return &ProviderAccess{APIKey: &ApiKeyAccessConfig{
			Type:                next.APIKey.Type,
			APIKey:              overlayPtr(current.APIKey.APIKey, next.APIKey.APIKey),
			APIKeyManagementURL: overlayPtr(current.APIKey.APIKeyManagementURL, next.APIKey.APIKeyManagementURL),
		}}
	case current.ZhipuAccount != nil && next.ZhipuAccount != nil:
		return &ProviderAccess{ZhipuAccount: &ZhipuAccountAccessConfig{
			AccountType: overlayPtr(current.ZhipuAccount.AccountType, next.ZhipuAccount.AccountType),
			Mode:        overlayPtr(current.ZhipuAccount.Mode, next.ZhipuAccount.Mode),
			Entitled:    overlayPtr(current.ZhipuAccount.Entitled, next.ZhipuAccount.Entitled),
		}}
	}
	return next
}

// ParseAccess decodes the access JSON object into the union.
func ParseAccess(data json.RawMessage) (*ProviderAccess, error) {
	if len(data) == 0 || string(data) == "null" {
		return nil, nil
	}
	var probe struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(data, &probe); err != nil {
		return nil, err
	}
	switch probe.Type {
	case AccessAPIKey, AccessZhipuCodingPlanAPIKey:
		var cfg ApiKeyAccessConfig
		if err := json.Unmarshal(data, &cfg); err != nil {
			return nil, err
		}
		cfg.Type = probe.Type
		return &ProviderAccess{APIKey: &cfg}, nil
	case AccessZhipuAccount:
		var cfg ZhipuAccountAccessConfig
		if err := json.Unmarshal(data, &cfg); err != nil {
			return nil, err
		}
		cfg.Type = probe.Type
		return &ProviderAccess{ZhipuAccount: &cfg}, nil
	}
	return &ProviderAccess{}, nil
}
