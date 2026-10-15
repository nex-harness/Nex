// modelmutations.go: the personal MODEL mutations of ProviderConfigService
// (config-service.ts) — reorder/add/rename/enable/save-draft/delete. Each is
// one repository update with the membership CAS.
package modelprovider

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/tylinux/nex/go/internal/providerconfig"
)

// ReorderPersonalModels ports reorderPersonalModels.
func (s *ConfigService) ReorderPersonalModels(ctx context.Context, providerID string, modelIDs []string, membership *ModelMembership) error {
	if strings.TrimSpace(providerID) == "" {
		return errors.New("providerId 不能为空")
	}
	builtin, err := s.builtin.Read(ctx)
	if err != nil {
		return err
	}
	_, err = s.updatePersonal(ctx, func(current *providerconfig.LayerSnapshot) (*providerconfig.LayerUpdate, error) {
		if err := assertMembershipCurrent(membership, providerID, current); err != nil {
			return nil, err
		}
		var builtinModelIDs []string
		if membership != nil {
			builtinModelIDs = membership.InheritedModelIDs
		} else {
			builtinModelIDs = resolveProviderBuiltinModelIDs(builtin, current.Providers, providerID)
		}
		provider := writableProviderOverlay(builtin, current, providerID)
		personalIDs := orStrings(provider.PersonalModelIDs)
		order := providerconfig.OwnedOrder(builtinModelIDs, personalIDs, modelIDs)
		return &providerconfig.LayerUpdate{
			Providers:     current.Providers.Set(providerID, provider.WithModelOrder(order)),
			Models:        current.Models,
			ProviderOrder: current.ProviderOrder,
		}, nil
	})
	return err
}

// AddPersonalModel ports addPersonalModel.
func (s *ConfigService) AddPersonalModel(ctx context.Context, providerID, modelID string, config *providerconfig.ModelConfig, membership *ModelMembership, useRecommendedConfig *bool) error {
	providerID, modelID, err := normalizeTwoIDs("providerId", providerID, "modelId", modelID)
	if err != nil {
		return err
	}
	builtin, err := s.builtin.Read(ctx)
	if err != nil {
		return err
	}
	_, err = s.updatePersonal(ctx, func(current *providerconfig.LayerSnapshot) (*providerconfig.LayerUpdate, error) {
		if err := assertMembershipCurrent(membership, providerID, current); err != nil {
			return nil, err
		}
		provider := writableProviderOverlay(builtin, current, providerID)
		var builtinModelIDs []string
		if membership != nil {
			builtinModelIDs = membership.InheritedModelIDs
		} else {
			builtinModelIDs = resolveProviderBuiltinModelIDs(builtin, current.Providers, providerID)
		}
		if containsStr(builtinModelIDs, modelID) {
			return nil, fmt.Errorf("Model 已存在: %s/%s", providerID, modelID)
		}
		currentModelIDs := orStrings(provider.PersonalModelIDs)
		if containsStr(currentModelIDs, modelID) {
			return nil, fmt.Errorf("Model 已存在: %s/%s", providerID, modelID)
		}
		nextPersonal := append(append([]string{}, currentModelIDs...), modelID)
		// 添加不能重新按成员名单排序，否则会丢掉用户已经保存的顺序.
		order := providerconfig.OwnedOrder(builtinModelIDs, nextPersonal, orStrings(provider.ModelOrder))
		enabledConfig := config.Overlay(&providerconfig.ModelConfig{Enabled: boolPtr(true)})
		models, err := current.Models.SetExact(providerID, modelID, enabledConfig, useRecommendedConfig)
		if err != nil {
			return nil, err
		}
		return &providerconfig.LayerUpdate{
			Providers:     current.Providers.Set(providerID, provider.WithPersonalModelIDs(nextPersonal).WithModelOrder(order)),
			Models:        models,
			ProviderOrder: current.ProviderOrder,
		}, nil
	})
	return err
}

// RenamePersonalModel ports renamePersonalModel.
func (s *ConfigService) RenamePersonalModel(ctx context.Context, providerID, currentModelID, nextModelID string, membership *ModelMembership) error {
	providerID, currentID, err := normalizeTwoIDs("providerId", providerID, "modelId", currentModelID)
	if err != nil {
		return err
	}
	nextID, err := normalizeID("modelId", nextModelID)
	if err != nil {
		return err
	}
	if currentID == nextID {
		_, err := s.personal.Read(ctx)
		return err
	}
	builtin, err := s.builtin.Read(ctx)
	if err != nil {
		return err
	}
	_, err = s.updatePersonal(ctx, func(current *providerconfig.LayerSnapshot) (*providerconfig.LayerUpdate, error) {
		if err := assertMembershipCurrent(membership, providerID, current); err != nil {
			return nil, err
		}
		provider := current.Providers.Get(providerID)
		var builtinModelIDs []string
		if membership != nil {
			builtinModelIDs = membership.InheritedModelIDs
		} else {
			builtinModelIDs = resolveProviderBuiltinModelIDs(builtin, current.Providers, providerID)
		}
		// 继承归属保护适用于 Facade 和底层直接调用.
		if containsStr(builtinModelIDs, currentID) {
			return nil, fmt.Errorf("Built-in Model 不能重命名: %s/%s", providerID, currentID)
		}
		personalIDs := orStrings(provider.PersonalModelIDs)
		if !containsStr(personalIDs, currentID) {
			return nil, fmt.Errorf("Personal Model 不存在: %s/%s", providerID, currentID)
		}
		if containsStr(personalIDs, nextID) || containsStr(builtinModelIDs, nextID) {
			return nil, fmt.Errorf("Model 已存在: %s/%s", providerID, nextID)
		}
		models := personalIDs
		for i, id := range models {
			if id == currentID {
				models[i] = nextID
			}
		}
		requested := orStrings(provider.ModelOrder)
		for i, id := range requested {
			if id == currentID {
				requested[i] = nextID
			}
		}
		return &providerconfig.LayerUpdate{
			Providers:     current.Providers.Set(providerID, provider.WithPersonalModelIDs(models).WithModelOrder(providerconfig.OwnedOrder(builtinModelIDs, models, requested))),
			Models:        current.Models.RenameExactModel(providerID, currentID, nextID),
			ProviderOrder: current.ProviderOrder,
		}, nil
	})
	return err
}

// SetPersonalModelEnabled ports setPersonalModelEnabled.
func (s *ConfigService) SetPersonalModelEnabled(ctx context.Context, providerID, modelID string, enabled bool, membership *ModelMembership) error {
	id, model, err := normalizeTwoIDs("providerId", providerID, "modelId", modelID)
	if err != nil {
		return err
	}
	builtin, err := s.builtin.Read(ctx)
	if err != nil {
		return err
	}
	_, err = s.updatePersonal(ctx, func(current *providerconfig.LayerSnapshot) (*providerconfig.LayerUpdate, error) {
		if err := assertMembershipCurrent(membership, id, current); err != nil {
			return nil, err
		}
		provider := current.Providers.Get(id)
		var inherited []string
		if membership != nil {
			inherited = membership.InheritedModelIDs
		} else {
			inherited = resolveProviderBuiltinModelIDs(builtin, current.Providers, id)
		}
		if !containsStr(inherited, model) && !containsStr(orStrings(provider.PersonalModelIDs), model) {
			return nil, fmt.Errorf("Model 不存在: %s/%s", id, model)
		}
		// 启停曾复用完整草稿保存：在事务内只修改最新 enabled.
		base := current.Models.GetExact(id, model)
		if base == nil {
			base = &providerconfig.ModelConfig{}
		}
		config := base.Overlay(&providerconfig.ModelConfig{Enabled: boolPtr(enabled)})
		models, err := current.Models.SetExact(id, model, config, nil)
		if err != nil {
			return nil, err
		}
		return &providerconfig.LayerUpdate{
			Providers: current.Providers, Models: models, ProviderOrder: current.ProviderOrder,
		}, nil
	})
	return err
}

// SavePersonalModelDraft ports savePersonalModelDraft — the single write
// boundary for the model editor (membership + order + exact rule together).
func (s *ConfigService) SavePersonalModelDraft(ctx context.Context, providerID, originalModelID, nextModelID string, config *providerconfig.ModelConfig, expectedPersonalRevision string, useRecommendedConfig *bool, membership *ModelMembership) error {
	providerID, originalID, err := normalizeTwoIDs("providerId", providerID, "modelId", originalModelID)
	if err != nil {
		return err
	}
	nextID, err := normalizeID("modelId", nextModelID)
	if err != nil {
		return err
	}
	builtin, err := s.builtin.Read(ctx)
	if err != nil {
		return err
	}
	_, err = s.updatePersonal(ctx, func(current *providerconfig.LayerSnapshot) (*providerconfig.LayerUpdate, error) {
		if err := assertMembershipCurrent(membership, providerID, current); err != nil {
			return nil, err
		}
		if current.Revision != expectedPersonalRevision {
			return nil, fmt.Errorf("Personal Provider Config revision conflict: expected %s, current %s", expectedPersonalRevision, current.Revision)
		}
		recommended := useRecommendedConfig
		if recommended == nil {
			rule := current.Models.GetExactRule(providerID, originalID)
			rec := rule == nil || rule.Type != providerconfig.RuleManualProviderModel
			recommended = &rec
		}
		provider := current.Providers.Get(providerID)
		var builtinModelIDs []string
		if membership != nil {
			builtinModelIDs = membership.InheritedModelIDs
		} else {
			builtinModelIDs = resolveProviderBuiltinModelIDs(builtin, current.Providers, providerID)
		}
		if originalID != nextID && (containsStr(builtinModelIDs, originalID) || containsStr(builtinModelIDs, nextID)) {
			if containsStr(builtinModelIDs, originalID) {
				return nil, fmt.Errorf("Built-in Model 不能重命名: %s/%s", providerID, originalID)
			}
			return nil, fmt.Errorf("Model 已存在: %s/%s", providerID, nextID)
		}
		personalModelIDs := orStrings(provider.PersonalModelIDs)
		if !containsStr(builtinModelIDs, originalID) && !containsStr(personalModelIDs, originalID) {
			return nil, fmt.Errorf("Model 不存在: %s/%s", providerID, originalID)
		}
		if originalID != nextID && (containsStr(personalModelIDs, nextID) || containsStr(builtinModelIDs, nextID)) {
			return nil, fmt.Errorf("Model 已存在: %s/%s", providerID, nextID)
		}
		providers := current.Providers
		models := current.Models
		if originalID != nextID {
			if !containsStr(personalModelIDs, originalID) {
				return nil, fmt.Errorf("Personal Model 不存在: %s/%s", providerID, originalID)
			}
			ids := personalModelIDs
			for i, id := range ids {
				if id == originalID {
					ids[i] = nextID
				}
			}
			requested := orStrings(provider.ModelOrder)
			for i, id := range requested {
				if id == originalID {
					requested[i] = nextID
				}
			}
			providers = providers.Set(providerID, provider.WithPersonalModelIDs(ids).WithModelOrder(providerconfig.OwnedOrder(builtinModelIDs, ids, requested)))
			models = models.RenameExactModel(providerID, originalID, nextID)
		}
		if *recommended && config.IsZero() {
			models = models.DeleteExact(providerID, nextID)
		} else {
			models, err = models.SetExact(providerID, nextID, config, recommended)
			if err != nil {
				return nil, err
			}
		}
		return &providerconfig.LayerUpdate{
			Providers: providers, Models: models, ProviderOrder: current.ProviderOrder,
		}, nil
	})
	return err
}

// DeletePersonalModel ports deletePersonalModel.
func (s *ConfigService) DeletePersonalModel(ctx context.Context, providerID, modelID string, membership *ModelMembership) error {
	providerID, modelID, err := normalizeTwoIDs("providerId", providerID, "modelId", modelID)
	if err != nil {
		return err
	}
	builtin, err := s.builtin.Read(ctx)
	if err != nil {
		return err
	}
	_, err = s.updatePersonal(ctx, func(current *providerconfig.LayerSnapshot) (*providerconfig.LayerUpdate, error) {
		if err := assertMembershipCurrent(membership, providerID, current); err != nil {
			return nil, err
		}
		provider := current.Providers.Get(providerID)
		var inherited []string
		if membership != nil {
			inherited = membership.InheritedModelIDs
		} else {
			inherited = resolveProviderBuiltinModelIDs(builtin, current.Providers, providerID)
		}
		if containsStr(inherited, modelID) {
			return nil, fmt.Errorf("Built-in Model 不能删除: %s/%s", providerID, modelID)
		}
		if !containsStr(orStrings(provider.PersonalModelIDs), modelID) {
			return nil, fmt.Errorf("Personal Model 不存在: %s/%s", providerID, modelID)
		}
		remaining := make([]string, 0, len(orStrings(provider.PersonalModelIDs)))
		for _, id := range orStrings(provider.PersonalModelIDs) {
			if id != modelID {
				remaining = append(remaining, id)
			}
		}
		return &providerconfig.LayerUpdate{
			Providers:     current.Providers.Set(providerID, provider.WithPersonalModelIDs(remaining).WithModelOrder(providerconfig.OwnedOrder(inherited, remaining, orStrings(provider.ModelOrder)))),
			Models:        current.Models.DeleteExact(providerID, modelID),
			ProviderOrder: current.ProviderOrder,
		}, nil
	})
	return err
}
