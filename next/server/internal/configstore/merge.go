package configstore

import (
	"encoding/json"
	"sort"
)

// MergeLayers folds patches in layering order (global → runner → workspace →
// session) into one effective layer.
//
// Semantics (research §C1): maps merge by key, arrays replace, a JSON `null`
// deletes. A provider entry that only carries `enabled` toggles an inherited
// provider instead of replacing it; any other entry replaces the inherited one
// (the UI always sends complete provider entries from the effective view).
func MergeLayers(layers ...Layer) Layer {
	out := Layer{
		Providers:  map[string]*Provider{},
		Settings:   map[string]json.RawMessage{},
		ModelOrder: map[string][]string{},
	}
	for _, layer := range layers {
		for id, provider := range layer.Providers {
			if provider == nil {
				delete(out.Providers, id)
				continue
			}
			previous := out.Providers[id]
			if provider.isToggleOnly() && previous != nil {
				toggled := *previous
				toggled.Enabled = provider.Enabled
				out.Providers[id] = &toggled
				continue
			}
			copied := cloneProvider(*provider)
			out.Providers[id] = &copied
		}
		for key, value := range layer.Settings {
			if isJSONNull(value) {
				delete(out.Settings, key)
				continue
			}
			out.Settings[key] = append(json.RawMessage(nil), value...)
		}
		if layer.ProviderOrder != nil {
			out.ProviderOrder = append([]string(nil), layer.ProviderOrder...)
		}
		for providerID, order := range layer.ModelOrder {
			if order == nil {
				delete(out.ModelOrder, providerID)
				continue
			}
			out.ModelOrder[providerID] = append([]string(nil), order...)
		}
	}
	if len(out.Settings) == 0 {
		out.Settings = nil
	}
	if len(out.ModelOrder) == 0 {
		out.ModelOrder = nil
	}
	return out
}

// EffectiveForRunner merges the layers a runner sees. Workspace and session
// scopes are not part of P0's push; their maps stay empty.
func (d ConfigDoc) EffectiveForRunner(runnerID string) Layer {
	layers := []Layer{d.Scopes.Global}
	if layer, ok := d.Scopes.Runner[runnerID]; ok {
		layers = append(layers, layer)
	}
	return MergeLayers(layers...)
}

// ProviderList returns the effective providers in display order: ProviderOrder
// entries first (unknown ids skipped), then the remaining ids sorted.
func (l Layer) ProviderList() []*Provider {
	out := make([]*Provider, 0, len(l.Providers))
	seen := make(map[string]struct{}, len(l.Providers))
	for _, id := range l.ProviderOrder {
		provider, ok := l.Providers[id]
		if !ok || provider == nil {
			continue
		}
		if _, dup := seen[id]; dup {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, provider)
	}
	rest := make([]string, 0, len(l.Providers))
	for id, provider := range l.Providers {
		if provider == nil {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		rest = append(rest, id)
	}
	sort.Strings(rest)
	for _, id := range rest {
		out = append(out, l.Providers[id])
	}
	return out
}

func cloneProvider(provider Provider) Provider {
	copied := provider
	copied.AuthConfigured = false
	if provider.Headers != nil {
		copied.Headers = make(map[string]string, len(provider.Headers))
		for key, value := range provider.Headers {
			copied.Headers[key] = value
		}
	}
	copied.Models = make([]Model, len(provider.Models))
	for index, model := range provider.Models {
		copied.Models[index] = cloneModel(model)
	}
	return copied
}

func cloneModel(model Model) Model {
	copied := model
	if model.ReasoningLevels != nil {
		copied.ReasoningLevels = append([]string(nil), model.ReasoningLevels...)
	}
	if model.Input != nil {
		copied.Input = append([]string(nil), model.Input...)
	}
	if model.Cost != nil {
		cost := *model.Cost
		copied.Cost = &cost
	}
	return copied
}

func isJSONNull(raw json.RawMessage) bool {
	trimmed := make([]byte, 0, 4)
	for _, b := range raw {
		if b == ' ' || b == '\t' || b == '\n' || b == '\r' {
			continue
		}
		trimmed = append(trimmed, b)
	}
	return string(trimmed) == "null"
}
