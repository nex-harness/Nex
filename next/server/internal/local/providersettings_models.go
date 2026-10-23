package local

// Provider-settings model writes: add/edit/enable/delete a model on a
// server-owned provider (docs/next/config-mcp-skills.md decision 6, research
// §A1). Split out of providersettings_writes.go for the 400-line file budget.

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/tylinux/nex-next/server/internal/configstore"
	"github.com/tylinux/nex-next/server/internal/rpcwire"
)

// addModel appends a model to a provider.
func (p *ProviderSettings) addModel(ctx context.Context, arg rpcwire.Value) (rpcwire.Value, error) {
	args := positionalArguments(arg)
	if len(args) < 3 {
		return nil, fmt.Errorf("addPersonalModel(providerId, modelId, config, useRecommended?)")
	}
	providerID, modelID := argumentString(args[0]), argumentString(args[1])
	if providerID == "" || modelID == "" {
		return nil, fmt.Errorf("providerId and modelId are required")
	}
	config := decodeObjectMap(args[2])
	_, err := p.manager.store.UpdateProvider(providerID, "model-add", func(provider *configstore.Provider) error {
		for _, existing := range provider.Models {
			if existing.ID == modelID {
				// The UI re-adds the same id after a failed save; treat it as an
				// overlay update instead of a duplicate error.
				return nil
			}
		}
		provider.Models = append(provider.Models, modelFromConfig(modelID, config))
		return nil
	})
	if err != nil {
		return nil, err
	}
	view, err := p.view(readViewTargetFromArgs(args))
	return encodeObject(view, err)
}

func (p *ProviderSettings) saveModelDraft(ctx context.Context, arg rpcwire.Value) (rpcwire.Value, error) {
	args := positionalArguments(arg)
	if len(args) < 1 {
		return nil, fmt.Errorf("savePersonalModelDraft(input)")
	}
	input := decodeObjectMap(args[0])
	if len(input) == 0 {
		input = decodeObjectMap(arg)
	}
	providerID := jsonString(input["providerId"])
	original := jsonString(input["originalModelId"])
	next := jsonString(input["nextModelId"])
	personal := decodeObjectMap(input["personalConfig"])
	if providerID == "" || original == "" || next == "" {
		return nil, fmt.Errorf("providerId, originalModelId and nextModelId are required")
	}
	_, err := p.manager.store.UpdateProvider(providerID, "model-draft", func(provider *configstore.Provider) error {
		index := -1
		for position := range provider.Models {
			if provider.Models[position].ID == original {
				index = position
				break
			}
		}
		if index < 0 {
			return fmt.Errorf("model %s: %w", original, configstore.ErrNotFound)
		}
		updated := modelFromConfig(next, personal)
		// Keep the previous enabled flag unless the draft sets it.
		updated.Enabled = provider.Models[index].Enabled
		if _, ok := personal["enabled"]; ok {
			var enabled bool
			if json.Unmarshal(personal["enabled"], &enabled) == nil {
				updated.Enabled = enabled
			}
		}
		for position, existing := range provider.Models {
			if position != index && existing.ID == next {
				return fmt.Errorf("model %s already exists: %w", next, configstore.ErrNotFound)
			}
		}
		provider.Models[index] = updated
		return nil
	})
	if err != nil {
		return nil, err
	}
	view, err := p.view(viewTarget{RunnerID: jsonString(input["runnerId"])})
	return encodeObject(view, err)
}

func (p *ProviderSettings) setModelEnabled(ctx context.Context, arg rpcwire.Value) (rpcwire.Value, error) {
	args := positionalArguments(arg)
	if len(args) < 3 {
		return nil, fmt.Errorf("setPersonalModelEnabled(providerId, modelId, enabled)")
	}
	providerID, modelID := argumentString(args[0]), argumentString(args[1])
	var enabled bool
	if err := json.Unmarshal(rawBytes(args[2]), &enabled); err != nil {
		return nil, fmt.Errorf("enabled must be a boolean")
	}
	_, err := p.manager.store.UpdateProvider(providerID, "model-enabled", func(provider *configstore.Provider) error {
		for position := range provider.Models {
			if provider.Models[position].ID == modelID {
				provider.Models[position].Enabled = enabled
				return nil
			}
		}
		return fmt.Errorf("model %s: %w", modelID, configstore.ErrNotFound)
	})
	if err != nil {
		return nil, err
	}
	view, err := p.view(readViewTargetFromArgs(args))
	return encodeObject(view, err)
}

func (p *ProviderSettings) deleteModel(ctx context.Context, arg rpcwire.Value) (rpcwire.Value, error) {
	args := positionalArguments(arg)
	if len(args) < 2 {
		return nil, fmt.Errorf("deletePersonalModel(providerId, modelId)")
	}
	providerID, modelID := argumentString(args[0]), argumentString(args[1])
	_, err := p.manager.store.UpdateProvider(providerID, "model-delete", func(provider *configstore.Provider) error {
		kept := provider.Models[:0]
		found := false
		for _, model := range provider.Models {
			if model.ID == modelID {
				found = true
				continue
			}
			kept = append(kept, model)
		}
		if !found {
			return fmt.Errorf("model %s: %w", modelID, configstore.ErrNotFound)
		}
		provider.Models = append([]configstore.Model(nil), kept...)
		if len(provider.Models) == 0 {
			provider.Enabled = false
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	view, err := p.view(readViewTargetFromArgs(args))
	return encodeObject(view, err)
}
