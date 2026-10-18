package taskindex

// selection.go — the frozen 0002 decoding rules. provider-selection-v2.ts cannot
// call a runtime parser or identity table that may change after release; the
// escape and separator precedence of the old decodeCustomModelValue /
// parseModelPickerValue is reproduced here literally.

import (
	"encoding/json"
	"net/url"
	"strings"
)

// providerNames is the frozen provider rename table of 0002.
var providerNames = map[string]string{
	"builtin:bigmodel":             "bigmodel-api",
	"builtin:zai":                  "zai-api",
	"builtin:bigmodel-start-plan":  "account:bigmodel-start-plan",
	"builtin:zai-start-plan":       "account:zai-start-plan",
	"builtin:bigmodel-coding-plan": "account:bigmodel-individual-coding-plan",
	"builtin:zai-coding-plan":      "account:zai-individual-coding-plan",
}

// modelSelection mirrors the frozen ModelSelection written into model_selection.
// Options is a pointer so an absent reasoningLevel emits no "options" key at all
// (the TypeScript object literal spreads nothing in that case).
type modelSelection struct {
	ProviderID string
	ModelID    string
	Options    *selectionOptions
}

type selectionOptions struct {
	ReasoningLevel string `json:"reasoningLevel"`
}

// legacySelectionJSON is JSON.stringify of the decoded selection, in the key
// order of the TypeScript object literal: { providerId, modelId, options? }.
func legacySelectionJSON(sel modelSelection) string {
	out := struct {
		ProviderID string            `json:"providerId"`
		ModelID    string            `json:"modelId"`
		Options    *selectionOptions `json:"options,omitempty"`
	}{sel.ProviderID, sel.ModelID, sel.Options}
	raw, err := json.Marshal(out)
	if err != nil {
		// The struct holds only strings; marshalling cannot fail.
		panic("taskindex: legacy selection JSON: " + err.Error())
	}
	return string(raw)
}

func decodeComponent(value string) string {
	// decodeURIComponent leaves a malformed escape sequence alone (URIError →
	// the original value). PathUnescape has the same property and, unlike
	// QueryUnescape, does not turn "+" into a space.
	if decoded, err := url.PathUnescape(value); err == nil {
		return decoded
	}
	return value
}

// decodeLegacySelection ports decodeLegacySelection.
func decodeLegacySelection(model, provider, thoughtLevel string) (modelSelection, bool) {
	value := strings.TrimSpace(model)
	if value == "" {
		return modelSelection{}, false
	}
	reasoningLevel := strings.TrimSpace(thoughtLevel)
	// `let model = value` in the TypeScript: a bare model with a legacy provider
	// column keeps that column and takes the rename table.
	out := modelSelection{ModelID: value}
	if strings.HasPrefix(value, "custom:") {
		body := value[len("custom:"):]
		separator := strings.Index(body, ":")
		if separator < 0 {
			return modelSelection{}, false
		}
		parts := strings.Split(body, ":")
		if len(parts) >= 3 && parts[0] == "builtin" {
			provider = "builtin:" + parts[1]
			out.ModelID = decodeComponent(strings.Join(parts[2:], ":"))
		} else {
			provider = decodeComponent(body[:separator])
			out.ModelID = decodeComponent(body[separator+1:])
		}
	} else if strings.Contains(value, "/") {
		separator := strings.Index(value, "/")
		provider = value[:separator]
		out.ModelID = value[separator+1:]
		levelSeparator := strings.Index(out.ModelID, "$")
		if levelSeparator > 0 && levelSeparator < len(out.ModelID)-1 {
			reasoningLevel = strings.TrimSpace(out.ModelID[levelSeparator+1:])
			if reasoningLevel == "" {
				return modelSelection{}, false
			}
			out.ModelID = out.ModelID[:levelSeparator]
		}
	} else if provider == "glm" || provider == "nex" {
		// 旧 provider=glm/nex 是执行后端，不是供应商身份。
		return modelSelection{}, false
	}
	provider = strings.TrimSpace(provider)
	out.ModelID = strings.TrimSpace(out.ModelID)
	if provider == "" || out.ModelID == "" {
		return modelSelection{}, false
	}
	if strings.HasPrefix(provider, "builtin:") {
		renamed, ok := providerNames[provider]
		if !ok {
			return modelSelection{}, false
		}
		provider = renamed
	}
	out.ProviderID = provider
	if reasoningLevel != "" {
		out.Options = &selectionOptions{ReasoningLevel: reasoningLevel}
	}
	return out, true
}
