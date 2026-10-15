// facades.go ports the view-projection halves of
// packages/provider/src/facades.ts: ProviderSettingsFacade.getView (the
// layered settings view with executable/enabled/selectable + issues) and
// ModelSelectionFacade.getView (registry candidates + initial/effective
// selection). The mutation sequencing (#mutateProvider tails) lives in
// runtime.go — one process shares the same ConfigService/RegistryService.
package modelprovider

import (
	"encoding/json"
	"errors"

	"github.com/tylinux/nex/go/internal/providerconfig"
)

// SettingsTemplateView ports ProviderSettingsTemplateView.
type SettingsTemplateView struct {
	TemplateID      string            `json:"templateId"`
	TemplateNameMap map[string]string `json:"templateNameMap"`
	Config          json.RawMessage   `json:"config"`
}

// SettingsModelView ports ProviderSettingsModelCandidateView.
type SettingsModelView struct {
	Kind                   string                           `json:"kind"`
	ModelID                string                           `json:"modelId"`
	Builtin                bool                             `json:"builtin"`
	EffectiveBuiltinConfig json.RawMessage                  `json:"effectiveBuiltinConfig"`
	PersonalExactConfig    json.RawMessage                  `json:"personalExactConfig,omitempty"`
	UseRecommendedConfig   *bool                            `json:"useRecommendedConfig,omitempty"`
	EffectiveConfig        json.RawMessage                  `json:"effectiveConfig"`
	Enabled                bool                             `json:"enabled"`
	Executable             bool                             `json:"executable"`
	Selectable             bool                             `json:"selectable"`
	Issues                 []providerconfig.ValidationIssue `json:"issues"`
}

// SettingsProviderView ports ProviderSettingsProviderView.
type SettingsProviderView struct {
	ProviderID             string                           `json:"providerId"`
	ProviderName           *string                          `json:"providerName"`
	TemplateID             *string                          `json:"templateId"`
	Enabled                bool                             `json:"enabled"`
	Executable             bool                             `json:"executable"`
	TemplateConfig         json.RawMessage                  `json:"templateConfig,omitempty"`
	EffectiveBuiltinConfig json.RawMessage                  `json:"effectiveBuiltinConfig,omitempty"`
	PersonalConfig         json.RawMessage                  `json:"personalConfig,omitempty"`
	EffectiveConfig        json.RawMessage                  `json:"effectiveConfig"`
	Issues                 []providerconfig.ValidationIssue `json:"issues"`
	Models                 []SettingsModelView              `json:"models"`
}

// SettingsView ports ProviderSettingsView.
type SettingsView struct {
	Revision          int                    `json:"revision"`
	ProviderTemplates []SettingsTemplateView `json:"providerTemplates"`
	ProviderOrder     []string               `json:"providerOrder"`
	Providers         []SettingsProviderView `json:"providers"`
}

// requireSnapshot: 尚未 start() 时读快照是调用方错误.
func requireSnapshot(r *RegistryService) (*RegistryServiceSnapshot, error) {
	snapshot := r.GetSnapshot()
	if snapshot == nil {
		return nil, errors.New("ProviderRegistryService 尚未 start()")
	}
	return snapshot, nil
}

// BuildSettingsView ports createProviderSettingsView.
func BuildSettingsView(snapshot *RegistryServiceSnapshot) (*SettingsView, error) {
	view := &SettingsView{Revision: snapshot.Registry.Revision}
	for _, tmpl := range snapshot.Config.NexBuiltinProviderTemplates.Entries() {
		config, err := marshalRaw(tmpl.Config)
		if err != nil {
			return nil, err
		}
		view.ProviderTemplates = append(view.ProviderTemplates, SettingsTemplateView{
			TemplateID: tmpl.TemplateID, TemplateNameMap: tmpl.TemplateNameMap, Config: config,
		})
	}
	executableSet := map[string]bool{}
	for _, provider := range snapshot.Resolution.RegistryProviders {
		executableSet[provider.ProviderID] = true
	}
	for _, resolved := range snapshot.Resolution.ResolvedProviders {
		if deref(resolved.Config.Visibility) == "hidden" {
			continue
		}
		personalConfig := snapshot.Config.PersonalProviders.Get(resolved.ProviderID)
		providerView := SettingsProviderView{
			ProviderID:   resolved.ProviderID,
			ProviderName: resolved.ProviderName,
			TemplateID:   resolved.TemplateID,
			Enabled:      resolved.Enabled,
			Executable:   executableSet[resolved.ProviderID],
			Issues:       resolved.ProviderIssues,
		}
		var err error
		if providerView.TemplateConfig, err = marshalRaw(resolved.TemplateConfig); err != nil {
			return nil, err
		}
		if providerView.EffectiveBuiltinConfig, err = marshalRaw(resolved.EffectiveBuiltinConfig); err != nil {
			return nil, err
		}
		if personalConfig != nil {
			if providerView.PersonalConfig, err = marshalRaw(personalConfig); err != nil {
				return nil, err
			}
		}
		if providerView.EffectiveConfig, err = marshalRaw(resolved.Config); err != nil {
			return nil, err
		}
		if deref(resolved.Config.Group) == providerconfig.GroupStandardPersonal {
			view.ProviderOrder = append(view.ProviderOrder, resolved.ProviderID)
		}
		for _, model := range resolved.Models {
			personalExact := snapshot.Config.PersonalModels.GetExact(resolved.ProviderID, model.ModelID)
			exactRule := snapshot.Config.PersonalModels.GetExactRule(resolved.ProviderID, model.ModelID)
			modelView := SettingsModelView{
				Kind: "candidate", ModelID: model.ModelID, Builtin: model.Source == "builtin",
				Enabled: model.Enabled, Executable: model.Executable, Selectable: model.Selectable,
				Issues: model.Issues,
			}
			if modelView.EffectiveBuiltinConfig, err = marshalRaw(model.EffectiveBuiltinConfig); err != nil {
				return nil, err
			}
			if personalExact != nil {
				if modelView.PersonalExactConfig, err = marshalRaw(personalExact); err != nil {
					return nil, err
				}
			}
			if exactRule != nil {
				recommended := exactRule.Type != providerconfig.RuleManualProviderModel
				modelView.UseRecommendedConfig = &recommended
			}
			if modelView.EffectiveConfig, err = marshalRaw(model.Config); err != nil {
				return nil, err
			}
			providerView.Models = append(providerView.Models, modelView)
		}
		view.Providers = append(view.Providers, providerView)
	}
	if view.ProviderOrder == nil {
		view.ProviderOrder = []string{}
	}
	if view.Providers == nil {
		view.Providers = []SettingsProviderView{}
	}
	if view.ProviderTemplates == nil {
		view.ProviderTemplates = []SettingsTemplateView{}
	}
	return view, nil
}

// SelectionModelView / SelectionProviderView / SelectionView port the
// model-selection projection.
type SelectionModelView struct {
	ModelID string          `json:"modelId"`
	Config  json.RawMessage `json:"config"`
}

type SelectionProviderView struct {
	ProviderID   string               `json:"providerId"`
	ProviderName *string              `json:"providerName"`
	TemplateID   *string              `json:"templateId"`
	Config       json.RawMessage      `json:"config"`
	Models       []SelectionModelView `json:"models"`
}

type SelectionView struct {
	Revision           int                            `json:"revision"`
	Providers          []SelectionProviderView        `json:"providers"`
	PreferredSelection *providerconfig.ModelSelection `json:"preferredSelection,omitempty"`
	EffectiveSelection *providerconfig.ModelSelection `json:"effectiveSelection,omitempty"`
	SelectionIssue     *string                        `json:"selectionIssue,omitempty"`
}

// ClassifyProvider ports createNodeModelSelectionFacade's classifier:
// Start 按真实 ID 解析，不参与付费连接唯一性判断；付费档 account-plan；off-peak
// account-offpeak；其余 ordinary.
func ClassifyProvider(providerID string) string {
	switch providerID {
	case "account:zai-start-plan", "account:bigmodel-start-plan":
		return "ordinary"
	case "account:zai-individual-coding-plan", "account:zai-team-coding-plan",
		"account:bigmodel-individual-coding-plan", "account:bigmodel-team-coding-plan":
		return "account-plan"
	case "account:zai-offpeak-idle-plan", "account:bigmodel-offpeak-idle-plan":
		return "account-offpeak"
	}
	return "ordinary"
}

// BuildSelectionView ports ModelSelectionFacade.getView + the facades'
// initial/effective resolution.
func BuildSelectionView(r *RegistryService, configuredDefault *providerconfig.ModelSelection, input *providerconfig.ModelSelection) (*SelectionView, error) {
	registryView := r.View()
	snapshot := r.GetSnapshot()
	view := &SelectionView{Revision: registryView.Revision}
	for _, provider := range registryView.Providers {
		if provider.Config != nil && deref(provider.Config.Visibility) == "hidden" {
			continue
		}
		config, err := marshalRaw(provider.Config)
		if err != nil {
			return nil, err
		}
		providerView := SelectionProviderView{
			ProviderID: provider.ProviderID, ProviderName: provider.ProviderName,
			TemplateID: provider.TemplateID, Config: config,
		}
		for _, model := range provider.Models {
			modelConfig, err := marshalRaw(model.Config)
			if err != nil {
				return nil, err
			}
			providerView.Models = append(providerView.Models, SelectionModelView{ModelID: model.ModelID, Config: modelConfig})
		}
		if providerView.Models == nil {
			providerView.Models = []SelectionModelView{}
		}
		view.Providers = append(view.Providers, providerView)
	}
	if view.Providers == nil {
		view.Providers = []SelectionProviderView{}
	}
	// Initial selection: configured default when still selectable, else first
	// registry candidate with a highest reasoning level.
	if initial := resolveInitialSelection(registryView, configuredDefault); initial != nil {
		preferred := *initial
		view.PreferredSelection = &preferred
	}
	if input != nil {
		var states map[string]providerconfig.AccountProviderState
		if snapshot != nil {
			states = snapshot.Account.States
		}
		// 账号事实与候选从同一已应用快照读取；不把最新 Settings 配给旧 Registry.
		if effective, issue := ResolveEffectiveSelection(registryView, states, *input); effective != nil || issue != "" {
			if effective != nil {
				view.EffectiveSelection = effective
			}
			if issue != "" {
				view.SelectionIssue = &issue
			}
		}
	}
	return view, nil
}

// resolveInitialSelection ports resolveInitialModelSelection.
func resolveInitialSelection(registry *RegistryView, configuredDefault *providerconfig.ModelSelection) *providerconfig.ModelSelection {
	if configuredDefault != nil {
		if selection := completeNewModelSelection(registry, *configuredDefault); selection != nil {
			return selection
		}
	}
	// 仅用于全新草稿的 Host 初始推荐：按 Registry 顺序推荐第一个可补档位的模型.
	for _, provider := range registry.Providers {
		if provider.Config != nil && deref(provider.Config.Visibility) == "hidden" {
			continue
		}
		for _, model := range provider.Models {
			candidate := providerconfig.ModelSelection{ProviderID: provider.ProviderID, ModelID: model.ModelID}
			if selection := completeNewModelSelection(registry, candidate); selection != nil {
				return selection
			}
		}
	}
	return nil
}

// completeNewModelSelection ports completeNewModelSelection: 最高档；不能用于
// 恢复/重解析已有选择.
func completeNewModelSelection(registry *RegistryView, selection providerconfig.ModelSelection) *providerconfig.ModelSelection {
	for _, provider := range registry.Providers {
		if provider.ProviderID != selection.ProviderID {
			continue
		}
		for _, model := range provider.Models {
			if model.ModelID != selection.ModelID {
				continue
			}
			values := modelReasoningValuesOf(model)
			if len(values) == 0 {
				return nil
			}
			return &providerconfig.ModelSelection{
				ProviderID: selection.ProviderID, ModelID: selection.ModelID,
				Options: &providerconfig.SelectionOptions{ReasoningLevel: values[len(values)-1]},
			}
		}
	}
	return nil
}

func modelReasoningValuesOf(model providerconfig.RegistryModel) []string {
	if model.Config == nil || model.Config.OptionSpecs == nil || model.Config.OptionSpecs.ReasoningLevel == nil {
		return nil
	}
	return model.Config.OptionSpecs.ReasoningLevel.Values
}

// ResolveEffectiveSelection ports resolveEffectiveModelSelection
// (effective-model-selection.ts): 只解析未来执行的意图，不改原选择、持久记录.
func ResolveEffectiveSelection(registry *RegistryView, accountStates map[string]providerconfig.AccountProviderState, selection providerconfig.ModelSelection) (*providerconfig.ModelSelection, string) {
	kind := ClassifyProvider(selection.ProviderID)
	providerID := selection.ProviderID
	if kind == "account-plan" {
		var current []string
		for id, state := range accountStates {
			if state.Current != nil && *state.Current && ClassifyProvider(id) == "account-plan" {
				current = append(current, id)
			}
		}
		if len(current) != 1 {
			return nil, "account-connection-unavailable"
		}
		providerID = current[0]
	}
	var provider *providerconfig.RegistryProvider
	for i := range registry.Providers {
		if registry.Providers[i].ProviderID == providerID {
			provider = &registry.Providers[i]
			break
		}
	}
	if provider == nil || (provider.Config != nil && deref(provider.Config.Visibility) == "hidden" && kind != "account-offpeak") {
		return nil, "provider-not-found"
	}
	var model *providerconfig.RegistryModel
	for i := range provider.Models {
		if provider.Models[i].ModelID == selection.ModelID {
			model = &provider.Models[i]
			break
		}
	}
	if model == nil {
		return nil, "model-not-found"
	}
	validation := providerconfig.ValidateModelSelectionOptions(model, selection)
	if !validation.OK && validation.Code == "reasoning-level-not-supported" {
		if legacy := ResolveLegacyReasoningLevel(selection); legacy != "" {
			candidate := selection
			candidate.Options = &providerconfig.SelectionOptions{ReasoningLevel: legacy}
			if checked := providerconfig.ValidateModelSelectionOptions(model, candidate); checked.OK {
				return &candidate, ""
			}
		}
	}
	if validation.OK {
		return &selection, ""
	}
	if validation.Code == "reasoning-level-missing" || validation.Code == "reasoning-level-not-supported" {
		return nil, validation.Code
	}
	return &selection, ""
}

func marshalRaw(v any) (json.RawMessage, error) {
	if v == nil {
		return json.RawMessage("null"), nil
	}
	data, err := jsonMarshalString(v)
	if err != nil {
		return nil, err
	}
	return json.RawMessage(data), nil
}

// MarshalSelection renders a selection as the RPC result.
func MarshalSelection(selection *providerconfig.ModelSelection) json.RawMessage {
	if selection == nil {
		return json.RawMessage("null")
	}
	return mustMarshalRaw(selection)
}

func mustMarshalRaw(v any) json.RawMessage {
	data, _ := jsonMarshalString(v)
	return json.RawMessage(data)
}
