// settingsmutations.go: the mutation methods of the provider-settings front —
// each withMutation(ctx, providerID, reason) then view rebuild (the TS
// #mutateProvider tails).
package modelprovider

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/tylinux/nex/go/internal/providerconfig"
)

// SavePersonalProviderOverlay / Delete / Reorder / model mutations: each
// serialized per provider (the TS #mutateProvider tails) then view rebuild.

func (s *ProviderSettingsService) SavePersonalProviderOverlay(ctx context.Context, providerID string, config *providerconfig.ProviderConfig, metadata map[string]any) (*SettingsView, error) {
	err := s.withMutation(ctx, providerID, "save-provider", func() error {
		return s.runtime.ConfigService.SavePersonalProviderOverlay(ctx, providerID, config, nil, decodeSaveMetadata(metadata))
	})
	if err != nil {
		return nil, err
	}
	return s.GetView(ctx)
}

func (s *ProviderSettingsService) DeletePersonalProvider(ctx context.Context, providerID string) (*SettingsView, error) {
	err := s.withMutation(ctx, providerID, "delete-provider", func() error {
		return s.runtime.ConfigService.DeletePersonalProvider(ctx, providerID)
	})
	if err != nil {
		return nil, err
	}
	return s.GetView(ctx)
}

func (s *ProviderSettingsService) ReorderPersonalProviders(ctx context.Context, providerIDs []string) (*SettingsView, error) {
	err := s.withMutation(ctx, "", "reorder-providers", func() error {
		return s.runtime.ConfigService.ReorderPersonalProviders(ctx, providerIDs)
	})
	if err != nil {
		return nil, err
	}
	return s.GetView(ctx)
}

func (s *ProviderSettingsService) ReorderPersonalModels(ctx context.Context, providerID string, modelIDs []string) (*SettingsView, error) {
	err := s.withMutation(ctx, providerID, "reorder-models", func() error {
		return s.runtime.ConfigService.ReorderPersonalModels(ctx, providerID, modelIDs, nil)
	})
	if err != nil {
		return nil, err
	}
	return s.GetView(ctx)
}

func (s *ProviderSettingsService) AddPersonalModel(ctx context.Context, providerID, modelID string, config *providerconfig.ModelConfig, useRecommended *bool) (*SettingsView, error) {
	err := s.withMutation(ctx, providerID, "add-model", func() error {
		return s.runtime.ConfigService.AddPersonalModel(ctx, providerID, modelID, config, nil, useRecommended)
	})
	if err != nil {
		return nil, err
	}
	return s.GetView(ctx)
}

func (s *ProviderSettingsService) RenamePersonalModel(ctx context.Context, providerID, currentModelID, nextModelID string) (*SettingsView, error) {
	err := s.withMutation(ctx, providerID, "rename-model", func() error {
		return s.runtime.ConfigService.RenamePersonalModel(ctx, providerID, currentModelID, nextModelID, nil)
	})
	if err != nil {
		return nil, err
	}
	return s.GetView(ctx)
}

func (s *ProviderSettingsService) DeletePersonalModel(ctx context.Context, providerID, modelID string) (*SettingsView, error) {
	err := s.withMutation(ctx, providerID, "delete-personal-model", func() error {
		return s.runtime.ConfigService.DeletePersonalModel(ctx, providerID, modelID, nil)
	})
	if err != nil {
		return nil, err
	}
	return s.GetView(ctx)
}

// SavePersonalModelDraftInput is the channel input shape.
type SavePersonalModelDraftInput struct {
	ProviderID           string         `json:"providerId"`
	OriginalModelID      string         `json:"originalModelId"`
	NextModelID          string         `json:"nextModelId"`
	PersonalConfig       map[string]any `json:"personalConfig"`
	UseRecommendedConfig *bool          `json:"useRecommendedConfig,omitempty"`
	BasedOnRevision      int            `json:"basedOnRevision"`
}

func (s *ProviderSettingsService) SavePersonalModelDraft(ctx context.Context, input *SavePersonalModelDraftInput) (*SettingsView, error) {
	configData, err := json.Marshal(input.PersonalConfig)
	if err != nil {
		return nil, err
	}
	config, err := providerconfig.ParseModelConfig(configData)
	if err != nil {
		return nil, err
	}
	err = s.withMutation(ctx, input.ProviderID, "save-model-draft", func() error {
		snapshot, err := requireSnapshot(s.runtime.RegistryService)
		if err != nil {
			return err
		}
		// Provider Settings revision conflict check (the facade's basedOnRevision).
		if snapshot.Registry.Revision != input.BasedOnRevision {
			return fmt.Errorf("Provider Settings revision conflict: expected %d, current %d", input.BasedOnRevision, snapshot.Registry.Revision)
		}
		personal, err := s.runtime.PersonalRepo.Read(ctx)
		if err != nil {
			return err
		}
		return s.runtime.ConfigService.SavePersonalModelDraft(
			ctx, input.ProviderID, input.OriginalModelID, input.NextModelID, config,
			personal.Revision, input.UseRecommendedConfig, nil,
		)
	})
	if err != nil {
		return nil, err
	}
	return s.GetView(ctx)
}

func (s *ProviderSettingsService) SetPersonalModelEnabled(ctx context.Context, providerID, modelID string, enabled bool) (*SettingsView, error) {
	err := s.withMutation(ctx, providerID, "set-model-enabled", func() error {
		return s.runtime.ConfigService.SetPersonalModelEnabled(ctx, providerID, modelID, enabled, nil)
	})
	if err != nil {
		return nil, err
	}
	return s.GetView(ctx)
}
