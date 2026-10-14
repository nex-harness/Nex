// snapshot.go ports parseClientConfigSnapshot + parsePluginStoreOrder
// (packages/shared/src/{clientConfig,pluginStoreOrder}.ts): the envelope is
// {code: 0, data: {configs: {pluginStoreOrder: …}}} and per-mode order shapes
// fall back independently.
package clientconfig

import (
	"encoding/json"
	"errors"
)

// ErrInvalidResponse is the TS's exact message.
var ErrInvalidResponse = errors.New("Invalid public client config response")

// ParseClientConfigSnapshot ports parseClientConfigSnapshot.
func ParseClientConfigSnapshot(payload []byte) (*Snapshot, error) {
	var envelope struct {
		Code *int64 `json:"code"`
		Data *struct {
			Configs *struct {
				PluginStoreOrder json.RawMessage `json:"pluginStoreOrder"`
			} `json:"configs"`
		} `json:"data"`
	}
	if err := json.Unmarshal(payload, &envelope); err != nil {
		return nil, ErrInvalidResponse
	}
	if envelope.Code == nil || *envelope.Code != 0 {
		return nil, ErrInvalidResponse
	}
	snapshot := &Snapshot{}
	if envelope.Data != nil && envelope.Data.Configs != nil {
		snapshot.PluginStoreOrder = parsePluginStoreOrder(envelope.Data.Configs.PluginStoreOrder)
	}
	return snapshot, nil
}

// parsePluginStoreOrder ports parsePluginStoreOrder: each mode's shape is
// optional and independently lenient (`.catch(undefined)`), a bad mode is
// dropped rather than poisoning the other; the result is an ordered map.
func parsePluginStoreOrder(raw json.RawMessage) any {
	if len(raw) == 0 {
		return nil
	}
	var modes map[string]json.RawMessage
	if err := json.Unmarshal(raw, &modes); err != nil {
		return nil
	}
	out := map[string]any{}
	for mode, modeRaw := range modes {
		if mode != "code" && mode != "work" {
			continue
		}
		parsed, ok := parseModeOrder(modeRaw)
		if ok {
			out[mode] = parsed
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func parseModeOrder(raw json.RawMessage) (any, bool) {
	var mode struct {
		CategoryOrder []string            `json:"categoryOrder"`
		PluginOrder   map[string][]string `json:"pluginOrder"`
	}
	if err := json.Unmarshal(raw, &mode); err != nil {
		return nil, false
	}
	out := map[string]any{}
	if mode.CategoryOrder != nil {
		out["categoryOrder"] = dedupeStrings(mode.CategoryOrder)
	}
	if mode.PluginOrder != nil {
		pluginOrder := map[string]any{}
		for key, items := range mode.PluginOrder {
			pluginOrder[key] = dedupeStrings(items)
		}
		out["pluginOrder"] = pluginOrder
	}
	if len(out) == 0 {
		return nil, false
	}
	return out, true
}

// dedupeStrings ports [...new Set(items)] — order-preserving unique.
func dedupeStrings(items []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(items))
	for _, item := range items {
		if seen[item] {
			continue
		}
		seen[item] = true
		out = append(out, item)
	}
	return out
}
