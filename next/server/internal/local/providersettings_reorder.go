package local

// Order-only provider-settings writes (drag reorder). Split out of
// providersettings_writes.go for the 400-line file budget.

import (
	"context"
	"fmt"

	"github.com/tylinux/nex-next/server/internal/configstore"
	"github.com/tylinux/nex-next/server/internal/rpcwire"
)

func (p *ProviderSettings) reorderProviders(ctx context.Context, arg rpcwire.Value) (rpcwire.Value, error) {
	ids := argumentStringList(firstArgument(arg))
	if _, err := p.manager.store.SetProviderOrder(ids); err != nil {
		return nil, err
	}
	view, err := p.view(viewTarget{})
	return encodeObject(view, err)
}

func (p *ProviderSettings) reorderModels(ctx context.Context, arg rpcwire.Value) (rpcwire.Value, error) {
	args := positionalArguments(arg)
	if len(args) < 2 {
		return nil, fmt.Errorf("reorderPersonalModels(providerId, modelIds)")
	}
	providerID := argumentString(args[0])
	order := argumentStringList(args[1])
	_, err := p.manager.store.UpdateProvider(providerID, "model-order", func(provider *configstore.Provider) error {
		byID := map[string]configstore.Model{}
		for _, model := range provider.Models {
			byID[model.ID] = model
		}
		reordered := make([]configstore.Model, 0, len(provider.Models))
		seen := map[string]struct{}{}
		for _, id := range order {
			if model, ok := byID[id]; ok {
				reordered = append(reordered, model)
				seen[id] = struct{}{}
			}
		}
		for _, model := range provider.Models {
			if _, ok := seen[model.ID]; !ok {
				reordered = append(reordered, model)
			}
		}
		provider.Models = reordered
		return nil
	})
	if err != nil {
		return nil, err
	}
	view, err := p.view(readViewTargetFromArgs(args))
	return encodeObject(view, err)
}
