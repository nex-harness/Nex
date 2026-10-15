// settingsservice.go: the IProviderSettingsService front (the TS
// createProviderSettingsService): view/mutation forwarding, per-provider
// mutation tails, models.dev lookup, remote model list, connectivity test.
package modelprovider

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"

	"github.com/tylinux/nex/go/internal/providerconfig"
)

// ---------------------------------------------------------------------------
// ProviderSettingsService
// ---------------------------------------------------------------------------

// ProviderSettingsService ports createProviderSettingsService output.
type ProviderSettingsService struct {
	runtime          *ProviderRuntime
	modelsDev        *ModelsDev
	testConnectivity ConnectivityTester
	mutationMu       sync.Mutex
	mutationTails    map[string]chan struct{}
	listenersMu      sync.Mutex
	listeners        []func(*SettingsView)
}

// GetView returns the settings view (ensureReady first).
func (s *ProviderSettingsService) GetView(ctx context.Context) (*SettingsView, error) {
	if err := s.runtime.Start(ctx); err != nil {
		return nil, err
	}
	snapshot, err := requireSnapshot(s.runtime.RegistryService)
	if err != nil {
		return nil, err
	}
	return BuildSettingsView(snapshot)
}

// Refresh re-reads the sources then returns the view.
func (s *ProviderSettingsService) Refresh(ctx context.Context, reason string) (*SettingsView, error) {
	if err := s.runtime.Start(ctx); err != nil {
		return nil, err
	}
	if _, err := s.runtime.RegistryService.RequestRefresh(ctx, "settings:"+orDefault(reason, "explicit")); err != nil {
		return nil, err
	}
	return s.GetView(ctx)
}

// OnDidChange registers a view listener.
func (s *ProviderSettingsService) OnDidChange(listener func(*SettingsView)) func() {
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

func (s *ProviderSettingsService) emitViews() {
	ctx := context.Background()
	view, err := s.GetView(ctx)
	if err != nil {
		return
	}
	s.listenersMu.Lock()
	listeners := append([]func(*SettingsView){}, s.listeners...)
	s.listenersMu.Unlock()
	for _, listener := range listeners {
		listener(view)
	}
}

// CreatePersonalProvider returns {providerId, view}.
func (s *ProviderSettingsService) CreatePersonalProvider(ctx context.Context, templateID, providerName, locale string, initialConfig *providerconfig.ProviderConfig) (rpcResult, error) {
	var providerID string
	err := s.withMutation(ctx, "", "create-provider", func() error {
		id, err := s.runtime.ConfigService.CreatePersonalProvider(ctx, CreatePersonalProviderInput{
			TemplateID: templateID, ProviderName: providerName, Locale: locale, InitialConfig: initialConfig,
		})
		providerID = id
		return err
	})
	if err != nil {
		return nil, err
	}
	view, err := s.GetView(ctx)
	if err != nil {
		return nil, err
	}
	return rpcResult{"providerId": providerID, "view": viewToValue(view)}, nil
}

// ResolveModelConfig ports resolveModelConfig (inherited vs effective preview).
func (s *ProviderSettingsService) ResolveModelConfig(ctx context.Context, providerID, originalModelID, modelID *string, personalConfig map[string]any) (rpcResult, error) {
	if err := s.runtime.Start(ctx); err != nil {
		return nil, err
	}
	snapshot, err := requireSnapshot(s.runtime.RegistryService)
	if err != nil {
		return nil, err
	}
	provider := snapshot.Resolution.EffectiveProviders.GetRule(*providerID)
	if provider == nil {
		return nil, fmt.Errorf("Provider 不存在: %s", *providerID)
	}
	apiType, baseURL := "", ""
	if provider.Config != nil && provider.Config.API != nil {
		apiType = provider.Config.APITypeOf()
		baseURL = provider.Config.APIBaseURLOf()
	}
	templateID := ""
	if provider.TemplateID != nil {
		templateID = *provider.TemplateID
	}
	effectiveRules := snapshot.Config.NexBuiltinModelRules.ComposeEffective(snapshot.Config.PersonalModels)
	resolve := func(rules *providerconfig.ModelConfigRules) *providerconfig.ModelConfig {
		return rules.Resolve(providerconfig.RuleResolutionInput{
			ProviderID: *providerID, TemplateID: templateID, ModelID: *modelID,
			APIType: apiType, BaseURL: baseURL,
		})
	}
	if personalConfig == nil {
		config := resolve(effectiveRules)
		return rpcResult{
			"inheritedConfig": rawOf(config),
			"effectiveConfig": rawOf(config),
			"issues":          issuesOf(providerconfig.ValidationIssuesOf(config, []string{"providers", *providerID, "models", *modelID})),
		}, nil
	}
	configData, err := json.Marshal(personalConfig)
	if err != nil {
		return nil, err
	}
	personalParsed, err := providerconfig.ParseModelConfig(configData)
	if err != nil {
		return nil, err
	}
	inherited := resolve(snapshot.Config.NexBuiltinModelRules)
	personalRules := snapshot.Config.PersonalModels
	if *originalModelID != *modelID {
		personalRules = personalRules.RenameExactModel(*providerID, *originalModelID, *modelID)
	}
	// 此入口预览智能配置草稿；固定模式不请求推荐，重新开启时不能沿用旧固定标记.
	recommended := false
	personalRules, err = personalRules.SetExact(*providerID, *modelID, personalParsed, &recommended)
	if err != nil {
		return nil, err
	}
	effective := resolve(snapshot.Config.NexBuiltinModelRules.ComposeEffective(personalRules))
	return rpcResult{
		"inheritedConfig": rawOf(inherited),
		"effectiveConfig": rawOf(effective),
		"issues":          issuesOf(providerconfig.ValidationIssuesOf(effective, []string{"providers", *providerID, "models", *modelID})),
	}, nil
}

// LookupModelInfo ports lookupModelInfo (models.dev manual query).
func (s *ProviderSettingsService) LookupModelInfo(ctx context.Context, modelID string) (rpcResult, error) {
	if s.modelsDev == nil {
		// models.dev 查询依赖磁盘缓存，由服务端装配时覆盖；未装配时显式报错，避免
		// UI 误报"未收录".
		return nil, errors.New("Model info lookup is not available in this host")
	}
	result, err := s.modelsDev.LookupModelInfo(ctx, modelID)
	if err != nil {
		return nil, err
	}
	return rpcResult{
		"found":      result.Found,
		"providerId": result.ProviderID,
		"config":     orEmptyMap(result.Config),
	}, nil
}

// ListRemoteModels ports listRemoteModels.
func (s *ProviderSettingsService) ListRemoteModels(ctx context.Context, providerID string) (rpcResult, error) {
	if err := s.runtime.Start(ctx); err != nil {
		return nil, err
	}
	snapshot, err := requireSnapshot(s.runtime.RegistryService)
	if err != nil {
		return nil, err
	}
	var effective *providerconfig.ProviderConfig
	for _, resolved := range snapshot.Resolution.ResolvedProviders {
		if resolved.ProviderID == providerID {
			effective = resolved.Config
			break
		}
	}
	if effective == nil {
		return nil, fmt.Errorf("Provider not found: %s", providerID)
	}
	client := newRemoteHTTPClient()
	list, err := FetchRemoteModelList(ctx, client, effective)
	if err != nil {
		return nil, err
	}
	ids := list.IDs
	if ids == nil {
		ids = []string{}
	}
	return rpcResult{"ids": ids}, nil
}

// TestModelConnectivity ports testModelConnectivity: 禁用对象仍存在于配置视图，
// 但不进入执行 Registry；不能把未发布误报成配置丢失.
func (s *ProviderSettingsService) TestModelConnectivity(ctx context.Context, input *struct {
	WorkspacePath     string `json:"workspacePath"`
	WorkspaceIdentity string `json:"workspaceIdentity,omitempty"`
	ProviderID        string `json:"providerId"`
	ModelID           string `json:"modelId"`
}) (rpcResult, error) {
	if err := s.runtime.Start(ctx); err != nil {
		return nil, err
	}
	if s.testConnectivity == nil {
		return nil, errors.New("当前 Environment 未装配模型连通性测试能力")
	}
	view, err := s.GetView(ctx)
	if err != nil {
		return nil, err
	}
	unavailable := ""
	var provider *SettingsProviderView
	for i := range view.Providers {
		if view.Providers[i].ProviderID == input.ProviderID {
			provider = &view.Providers[i]
			break
		}
	}
	var model *SettingsModelView
	if provider != nil {
		for i := range provider.Models {
			if provider.Models[i].ModelID == input.ModelID {
				model = &provider.Models[i]
				break
			}
		}
	}
	switch {
	case provider == nil || !provider.Enabled:
		unavailable = "provider-unavailable"
	case model == nil || !model.Enabled || len(model.Issues) > 0:
		unavailable = "model-unavailable"
	case !provider.Executable:
		unavailable = "provider-unavailable"
	case !model.Executable:
		unavailable = "model-unavailable"
	}
	if unavailable != "" {
		message := "This model is currently unavailable for connectivity testing."
		if unavailable == "provider-unavailable" {
			message = "This provider is currently unavailable for connectivity testing."
		}
		return rpcResult{
			"success": false,
			"error":   rpcResult{"code": unavailable, "message": message},
		}, nil
	}
	if err := s.testConnectivity(ctx, ConnectivityTestInput{
		WorkspacePath: input.WorkspacePath, WorkspaceIdentity: input.WorkspaceIdentity,
		ProviderID: input.ProviderID, ModelID: input.ModelID,
	}); err != nil {
		return rpcResult{"success": false, "error": rpcResult{"message": err.Error()}}, nil
	}
	return rpcResult{"success": true}, nil
}

// withMutation ports #mutate/#mutateProvider: per-provider tail serialization
// (空 providerID = 全局序列化), refresh after the mutation, view emitted.
func (s *ProviderSettingsService) withMutation(ctx context.Context, providerID, reason string, operation func() error) error {
	s.mutationMu.Lock()
	if s.mutationTails == nil {
		s.mutationTails = map[string]chan struct{}{}
	}
	key := providerID
	previous := s.mutationTails[key]
	tail := make(chan struct{})
	s.mutationTails[key] = tail
	s.mutationMu.Unlock()
	if previous != nil {
		select {
		case <-previous:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	defer func() {
		close(tail)
		s.mutationMu.Lock()
		if s.mutationTails[key] == tail {
			delete(s.mutationTails, key)
		}
		s.mutationMu.Unlock()
	}()
	if err := s.runtime.Start(ctx); err != nil {
		return err
	}
	if err := operation(); err != nil {
		return err
	}
	if _, err := s.runtime.RegistryService.RequestRefresh(ctx, "settings:"+reason); err != nil {
		return err
	}
	s.emitViews()
	return nil
}

// WaitForProviderOperations ports waitForProviderOperations.
func (s *ProviderSettingsService) WaitForProviderOperations(providerID string) {
	s.mutationMu.Lock()
	tail := s.mutationTails[providerID]
	s.mutationMu.Unlock()
	if tail != nil {
		<-tail
	}
}

// decodeSaveMetadata ports the metadata pick.
func decodeSaveMetadata(metadata map[string]any) *SaveProviderOverlayMetadata {
	if metadata == nil {
		return nil
	}
	out := &SaveProviderOverlayMetadata{}
	if v, ok := metadata["providerName"].(string); ok {
		out.ProviderName = &v
	}
	if v, ok := metadata["templateId"].(string); ok {
		out.TemplateID = &v
	}
	if v, ok := metadata["enabled"].(bool); ok {
		out.Enabled = &v
	}
	return out
}
