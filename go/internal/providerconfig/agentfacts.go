// agentfacts.go is the SHARED provider-config entry point the Phase-1 Go
// agent reads (cmd/nex-agent) and the Phase-3 Go server writes through: the
// extraction the plan's step 3.6 mandates — one implementation of provider
// config parsing, moved from cmd/nex-agent/runtime.go into this package.
//
// openProviderConfig resolves provider_config.json the same way the Node
// agent does (runtime-paths.ts): NEX_PERSONAL_PROVIDER_CONFIG_FILE wins over
// ~/.nex/v2/provider_config.json. parseProviderConfigFile turns the decoded
// personal layer + an optional builtin release into the runtime Provider
// facts (the first manual model rule with the matching provider rule).
package providerconfig

import (
	"errors"
	"fmt"
)

// AgentProviderFacts is the flat provider record runtime.Provider carries
// (cmd/nex-agent spelled it as runtime.Provider; this package owns it now).
type AgentProviderFacts struct {
	ProviderID       string
	ProviderName     string
	ProviderKind     string
	ModelID          string
	ModelName        string
	BaseURL          string
	APIKey           string
	ContextWindow    int64
	MaxOutputToken   int64
	ReasoningLevels  []string
	DefaultReasoning string
	HTTP             bool
}

// ErrNoManualModelRules matches the Phase-1 error string.
var ErrNoManualModelRules = errors.New("provider config has no manual model rules")

// ParseAgentProviderFacts ports the parser that lived in
// cmd/nex-agent/runtime.go: personal file → first manual model rule →
// runtime.Provider. The builtin release (when supplied) contributes the
// provider kind/baseURL defaults through the rule engine when the personal
// rule leaves them unset — the Phase-1 shape only read the personal provider
// rule's own api block, so the builtin argument is optional and unused by
// the agent call site today (kept for the server-side full resolution).
func ParseAgentProviderFacts(data []byte) (*AgentProviderFacts, error) {
	update, err := DecodeProviderConfigFile(data)
	if err != nil {
		return nil, err
	}
	manual := manualRules(update.Models)
	if len(manual) == 0 {
		return nil, ErrNoManualModelRules
	}
	m := manual[0]
	facts := &AgentProviderFacts{
		ProviderID: m.ProviderID,
		ModelID:    m.ModelID,
	}
	if m.Config != nil {
		if m.Config.Name != nil {
			facts.ModelName = *m.Config.Name
		}
		if m.Config.Properties != nil && m.Config.Properties.ContextWindow != nil {
			facts.ContextWindow = *m.Config.Properties.ContextWindow
		}
		if m.Config.OptionSpecs != nil {
			if m.Config.OptionSpecs.MaxOutputTokens != nil && m.Config.OptionSpecs.MaxOutputTokens.Max != nil {
				facts.MaxOutputToken = *m.Config.OptionSpecs.MaxOutputTokens.Max
			}
			if m.Config.OptionSpecs.ReasoningLevel != nil {
				facts.ReasoningLevels = append([]string{}, m.Config.OptionSpecs.ReasoningLevel.Values...)
			}
		}
	}
	if len(facts.ReasoningLevels) > 0 {
		facts.DefaultReasoning = facts.ReasoningLevels[0]
	}
	for _, rule := range update.Providers.Rules() {
		if rule.ProviderID != m.ProviderID || rule.Config == nil {
			continue
		}
		if rule.ProviderName != nil {
			facts.ProviderName = *rule.ProviderName
		}
		if rule.Config.API != nil {
			if rule.Config.API.BaseURL != nil {
				facts.BaseURL = *rule.Config.API.BaseURL
			}
			if rule.Config.API.Type != nil {
				facts.ProviderKind = *rule.Config.API.Type
			}
		}
		if rule.Config.Access != nil && rule.Config.Access.APIKey != nil {
			facts.APIKey = *rule.Config.Access.APIKey.APIKey
		}
	}
	// ProviderKind mapping kept from the Phase-1 parser: openai-chat-completions
	// normalizes to the model client's "openai-compatible".
	if facts.ProviderKind == "openai-chat-completions" {
		facts.ProviderKind = "openai-compatible"
	}
	return facts, nil
}

func manualRules(rules *ModelConfigRules) []ModelConfigRule {
	var out []ModelConfigRule
	for _, rule := range rules.Rules() {
		if rule.Type == RuleManualProviderModel {
			out = append(out, rule)
		}
	}
	return out
}

// FactsFromResolution builds agent facts from a fully-resolved registry
// provider/model pair — the server-side (testModelConnectivity, provisioning
// validation) spelling of the same facts.
func FactsFromResolution(provider *RegistryProvider, model *RegistryModel) (*AgentProviderFacts, error) {
	if provider == nil || model == nil {
		return nil, fmt.Errorf("providerconfig: unresolved provider/model")
	}
	facts := &AgentProviderFacts{
		ProviderID: provider.ProviderID,
		ModelID:    model.ModelID,
	}
	if provider.ProviderName != nil {
		facts.ProviderName = *provider.ProviderName
	}
	if provider.Config != nil {
		if provider.Config.API != nil {
			if provider.Config.API.BaseURL != nil {
				facts.BaseURL = *provider.Config.API.BaseURL
			}
			if provider.Config.API.Type != nil {
				facts.ProviderKind = *provider.Config.API.Type
			}
		}
		if provider.Config.Access != nil && provider.Config.Access.APIKey != nil &&
			provider.Config.Access.APIKey.APIKey != nil {
			facts.APIKey = *provider.Config.Access.APIKey.APIKey
		}
	}
	if model.Config != nil {
		if model.Config.Name != nil {
			facts.ModelName = *model.Config.Name
		}
		if model.Config.Properties != nil && model.Config.Properties.ContextWindow != nil {
			facts.ContextWindow = *model.Config.Properties.ContextWindow
		}
		if model.Config.OptionSpecs != nil {
			if model.Config.OptionSpecs.MaxOutputTokens != nil && model.Config.OptionSpecs.MaxOutputTokens.Max != nil {
				facts.MaxOutputToken = *model.Config.OptionSpecs.MaxOutputTokens.Max
			}
			if model.Config.OptionSpecs.ReasoningLevel != nil {
				facts.ReasoningLevels = append([]string{}, model.Config.OptionSpecs.ReasoningLevel.Values...)
			}
		}
	}
	if len(facts.ReasoningLevels) > 0 {
		facts.DefaultReasoning = facts.ReasoningLevels[0]
	}
	if facts.ProviderKind == "openai-chat-completions" {
		facts.ProviderKind = "openai-compatible"
	}
	if facts.BaseURL != "" && len(facts.BaseURL) > 7 && facts.BaseURL[:8] == "https://" {
		facts.HTTP = false
	} else if facts.BaseURL != "" && len(facts.BaseURL) > 6 && facts.BaseURL[:7] == "http://" {
		facts.HTTP = true
	}
	return facts, nil
}
