// configservice.go ports ProviderConfigService
// (packages/provider/src/config-service.ts): the mutation boundary over the
// personal layer — create/save/delete/reorder providers and models, each a
// single repository update with membership CAS checks. Error strings are the
// UI contract; they are verbatim.
package modelprovider

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/tylinux/nex/go/internal/providerconfig"
)

// ConfigSnapshot ports ProviderConfigSnapshot (sources.ts): the merged
// builtin+personal view.
type ConfigSnapshot struct {
	Revision                    string
	NexBuiltinRevision          string
	PersonalRevision            string
	NexBuiltinProviders         *providerconfig.ProviderConfigMap
	NexBuiltinProviderTemplates *providerconfig.ProviderTemplateMap
	PersonalProviders           *providerconfig.ProviderConfigMap
	NexBuiltinModelRules        *providerconfig.ModelConfigRules
	PersonalModels              *providerconfig.ModelConfigRules
	PersonalProviderOrder       []string
}

// ModelMembership ports ProviderModelMembership (Host 内部成员事实；不得接受
// Renderer 自报的模型名单).
type ModelMembership struct {
	ProviderID        string
	InheritedModelIDs []string
	PersonalRevision  string
	AssertCurrent     func() error
}

// ConfigServiceOptions wires the sources.
type ConfigServiceOptions struct {
	BuiltinSource      BuiltinSourceAPI
	PersonalRepository *PersonalRepo
}

// BuiltinSourceAPI is the read surface ConfigService needs from the builtin
// source (the concrete BuiltinSource satisfies it; tests can fake it).
type BuiltinSourceAPI interface {
	Read(ctx context.Context) (*BuiltinConfigSnapshot, error)
	OnDidChange(listener func(reason string)) func()
}

// ConfigService ports ProviderConfigService.
type ConfigService struct {
	builtin      BuiltinSourceAPI
	personal     *PersonalRepo
	listenersMu  sync.Mutex
	listeners    []func(reason string)
	stopBuiltin  func()
	stopPersonal func()
}

// NewConfigService wires the source-change fan-in.
func NewConfigService(opts ConfigServiceOptions) *ConfigService {
	svc := &ConfigService{builtin: opts.BuiltinSource, personal: opts.PersonalRepository}
	svc.stopBuiltin = opts.BuiltinSource.OnDidChange(func(reason string) {
		svc.emit("nexBuiltin:" + reason)
	})
	svc.stopPersonal = opts.PersonalRepository.OnDidChange(func(reason string) {
		svc.emit("personal:" + reason)
	})
	return svc
}

// Read merges both sources (sources.ts ProviderConfigService.read).
func (s *ConfigService) Read(ctx context.Context) (*ConfigSnapshot, error) {
	builtin, err := s.builtin.Read(ctx)
	if err != nil {
		return nil, err
	}
	personal, err := s.personal.Read(ctx)
	if err != nil {
		return nil, err
	}
	templates := builtin.ProviderTemplates
	if templates == nil {
		templates = providerconfig.EmptyProviderTemplateMap()
	}
	order := personal.ProviderOrder
	if order == nil {
		order = []string{}
	}
	revision, _ := jsonMarshalString([]any{builtin.Revision, personal.Revision})
	return &ConfigSnapshot{
		Revision:                    revision,
		NexBuiltinRevision:          builtin.Revision,
		PersonalRevision:            personal.Revision,
		NexBuiltinProviders:         builtin.Providers,
		NexBuiltinProviderTemplates: templates,
		PersonalProviders:           personal.Providers,
		NexBuiltinModelRules:        builtin.Models,
		PersonalModels:              personal.Models,
		PersonalProviderOrder:       order,
	}, nil
}

// OnDidChange registers a change listener.
func (s *ConfigService) OnDidChange(listener func(reason string)) func() {
	s.listenersMu.Lock()
	defer s.listenersMu.Unlock()
	s.listeners = append(s.listeners, listener)
	return func() {
		s.listenersMu.Lock()
		defer s.listenersMu.Unlock()
		for i, candidate := range s.listeners {
			if &candidate == &listener {
				s.listeners = append(s.listeners[:i], s.listeners[i+1:]...)
				return
			}
		}
	}
}

// Dispose detaches the sources.
func (s *ConfigService) Dispose() {
	s.listenersMu.Lock()
	defer s.listenersMu.Unlock()
	if s.stopBuiltin != nil {
		s.stopBuiltin()
	}
	if s.stopPersonal != nil {
		s.stopPersonal()
	}
	s.listeners = nil
}

// CreatePersonalProviderInput ports CreatePersonalProviderInput.
type CreatePersonalProviderInput struct {
	TemplateID    string
	ProviderName  string
	Locale        string
	InitialConfig *providerconfig.ProviderConfig
}

// CreatePersonalProvider ports createPersonalProvider.
func (s *ConfigService) CreatePersonalProvider(ctx context.Context, input CreatePersonalProviderInput) (string, error) {
	builtin, err := s.builtin.Read(ctx)
	if err != nil {
		return "", err
	}
	templateID := strings.TrimSpace(input.TemplateID)
	var template *providerconfig.ProviderTemplate
	if templateID != "" {
		template = builtin.ProviderTemplates.Get(templateID)
		if template == nil {
			return "", fmt.Errorf("Provider Template 不存在: %s", templateID)
		}
	}
	if input.InitialConfig != nil {
		if input.InitialConfig.Group != nil {
			return "", errors.New("initialConfig 不能包含 group")
		}
		if input.InitialConfig.BuiltinModelIDs != nil {
			return "", errors.New("initialConfig 不能包含 builtinModelIds")
		}
	}
	createdID := ""
	_, err = s.updatePersonal(ctx, func(current *providerconfig.LayerSnapshot) (*providerconfig.LayerUpdate, error) {
		occupied := map[string]bool{}
		for _, id := range builtin.Providers.Keys() {
			occupied[id] = true
		}
		for _, id := range current.Providers.Keys() {
			occupied[id] = true
		}
		providerID := nextPersonalProviderID(occupied, templateID)
		createdID = providerID
		effective := resolvePersonalBaselines(builtin, current.Providers)
		labelSeed := input.ProviderName
		if labelSeed == "" && template != nil && templateID != "" {
			labelSeed = providerconfig.ResolveProviderTemplateName(templateID, template.TemplateNameMap, orDefault(input.Locale, "en-US"))
		}
		if labelSeed == "" {
			labelSeed = "new-provider"
		}
		label := nextPersonalProviderLabel(labelSeed, effective)
		initial := input.InitialConfig
		if initial == nil {
			initial = &providerconfig.ProviderConfig{}
		}
		config := (&providerconfig.ProviderConfig{
			Group:            strPtr(providerconfig.GroupStandardPersonal),
			PersonalModelIDs: []string{},
			ModelOrder:       []string{},
		}).Overlay(initial)
		if templateID == "" {
			config = config.Overlay(&providerconfig.ProviderConfig{Access: &providerconfig.ProviderAccess{APIKey: &providerconfig.ApiKeyAccessConfig{Type: providerconfig.AccessAPIKey}}})
		}
		providers := current.Providers.SetRule(providerconfig.ProviderConfigRule{
			ProviderID:   providerID,
			TemplateID:   optionalStr(templateID),
			ProviderName: &label,
			Config:       config,
		})
		return &providerconfig.LayerUpdate{
			Providers:     providers,
			Models:        current.Models,
			ProviderOrder: appendCurrentProviderOrder(builtin.Providers, providers, current.ProviderOrder, providerID),
		}, nil
	})
	if err != nil {
		return "", err
	}
	if createdID == "" {
		return "", errors.New("Personal Provider 创建失败")
	}
	return createdID, nil
}

// SaveProviderOverlayMetadata is the metadata pick.
type SaveProviderOverlayMetadata struct {
	ProviderName *string
	TemplateID   *string
	Enabled      *bool
}

// SavePersonalProviderOverlay ports savePersonalProviderOverlay.
func (s *ConfigService) SavePersonalProviderOverlay(ctx context.Context, providerID string, config *providerconfig.ProviderConfig, membership *ModelMembership, metadata *SaveProviderOverlayMetadata) error {
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
		builtinConfig := builtin.Providers.Get(providerID)
		currentPersonal := current.Providers.Get(providerID)
		// 账号总禁用已撤销；在公共写入边界拒绝新操作.
		if builtinConfig != nil && builtinConfig.Access != nil && builtinConfig.Access.AccessType() == providerconfig.AccessZhipuAccount {
			if metadata != nil && metadata.Enabled != nil && !*metadata.Enabled {
				return nil, fmt.Errorf("Account Provider 不允许禁用: %s", providerID)
			}
			if config.Access != nil {
				return nil, fmt.Errorf("固定 Account Provider 的 Access 只能由 Nex Built-in Config 声明: %s", providerID)
			}
		}
		// 普通保存曾同时承担创建语义，删除后的迟到保存可以凭空复活 Overlay.
		if currentPersonal == nil && builtinConfig == nil {
			return nil, fmt.Errorf("Personal Provider 尚未创建: %s", providerID)
		}
		normalized := config
		if builtinConfig != nil {
			if normalized.Group != nil && *normalized.Group != deref(builtinConfig.Group) {
				return nil, fmt.Errorf("Personal Overlay 不能改写 Built-in Provider group: %s", providerID)
			}
			normalized = normalized.WithoutGroup()
		} else {
			group := derefOr(normalized.Group, deref(orConfig(currentPersonal).Group))
			if group != providerconfig.GroupStandardPersonal {
				return nil, fmt.Errorf("Personal-only Provider 必须使用 standard-personal group: %s", providerID)
			}
			normalized = normalized.Overlay(&providerconfig.ProviderConfig{Group: &group})
		}
		var membershipBaseline *providerconfig.ProviderConfig
		if builtinConfig != nil {
			membershipBaseline = builtinConfig
		} else {
			membershipBaseline = resolveTemplateBaseline(builtin, current.Providers, providerID)
		}
		var inherited []string
		if membership != nil {
			inherited = membership.InheritedModelIDs
		}
		next := normalized.WithModelMembershipFrom(normalizePersonalMembership(currentPersonal, membershipBaseline, inherited))
		currentRule := current.Providers.GetRule(providerID)
		rule := providerconfig.ProviderConfigRule{ProviderID: providerID, Config: next}
		if currentRule != nil {
			rule = *currentRule
			rule.Config = next
			if metadata != nil {
				if metadata.TemplateID != nil {
					rule.TemplateID = metadata.TemplateID
				}
				if metadata.Enabled != nil {
					rule.Enabled = metadata.Enabled
				}
				if metadata.ProviderName != nil {
					name := strings.TrimSpace(*metadata.ProviderName)
					rule.ProviderName = &name
				}
			}
		} else if metadata != nil {
			if metadata.TemplateID != nil {
				rule.TemplateID = metadata.TemplateID
			}
			if metadata.Enabled != nil {
				rule.Enabled = metadata.Enabled
			}
			if metadata.ProviderName != nil {
				name := strings.TrimSpace(*metadata.ProviderName)
				rule.ProviderName = &name
			}
		}
		providers := current.Providers.SetRule(rule)
		return &providerconfig.LayerUpdate{
			Providers: providers, Models: current.Models, ProviderOrder: current.ProviderOrder,
		}, nil
	})
	return err
}

// DeletePersonalProvider ports deletePersonalProvider.
func (s *ConfigService) DeletePersonalProvider(ctx context.Context, providerID string) error {
	if strings.TrimSpace(providerID) == "" {
		return errors.New("providerId 不能为空")
	}
	_, err := s.updatePersonal(ctx, func(current *providerconfig.LayerSnapshot) (*providerconfig.LayerUpdate, error) {
		var order []string
		for _, id := range current.ProviderOrder {
			if id != providerID {
				order = append(order, id)
			}
		}
		return &providerconfig.LayerUpdate{
			Providers:     current.Providers.Delete(providerID),
			Models:        current.Models.DeleteExactForProvider(providerID),
			ProviderOrder: order,
		}, nil
	})
	return err
}

// ReorderPersonalProviders ports reorderPersonalProviders.
func (s *ConfigService) ReorderPersonalProviders(ctx context.Context, providerIDs []string) error {
	if _, err := s.updatePersonal(ctx, func(current *providerconfig.LayerSnapshot) (*providerconfig.LayerUpdate, error) {
		var personalIDs []string
		for _, id := range current.Providers.Keys() {
			if deref(current.Providers.Get(id).Group) == providerconfig.GroupStandardPersonal {
				personalIDs = append(personalIDs, id)
			}
		}
		return &providerconfig.LayerUpdate{
			Providers: current.Providers, Models: current.Models,
			ProviderOrder: providerconfig.OwnedOrder(nil, personalIDs, providerIDs),
		}, nil
	}); err != nil {
		return err
	}
	return nil
}

// ReplacePersonalConfig ports replacePersonalConfig — the provisioning/
// migration atomic replacement. 新信封未带默认选择时必须清除，不能继承接收端旧值.
func (s *ConfigService) ReplacePersonalConfig(ctx context.Context, config *providerconfig.LayerUpdate) error {
	_, err := s.personal.Update(ctx, func(current *providerconfig.LayerSnapshot) (*providerconfig.LayerUpdate, error) {
		return config, nil
	})
	return err
}

// updatePersonal ports #updatePersonal: the transform runs inside the
// repository update; defaultModelSelection is preserved (Provider/Model/排序
// 只修改自己的成员，不能因共用文件清掉默认选择).
func (s *ConfigService) updatePersonal(ctx context.Context, transform func(current *providerconfig.LayerSnapshot) (*providerconfig.LayerUpdate, error)) (*providerconfig.LayerSnapshot, error) {
	return s.personal.Update(ctx, func(current *providerconfig.LayerSnapshot) (*providerconfig.LayerUpdate, error) {
		next, err := transform(current)
		if err != nil {
			return nil, err
		}
		next.DefaultModelSelection = current.DefaultModelSelection
		return next, nil
	})
}

func (s *ConfigService) emit(reason string) {
	s.listenersMu.Lock()
	listeners := append([]func(string){}, s.listeners...)
	s.listenersMu.Unlock()
	if reason == "" {
		reason = "changed"
	}
	for _, listener := range listeners {
		listener(reason)
	}
}
