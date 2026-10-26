package local

// Server-owned provider-settings writes: the UI's sparse create/overlay/model
// calls compiled into the strict pi shape (docs/next/config-mcp-skills.md
// decision 6, research §A1). Split out of providersettings.go for the 400-line
// file budget. The dispatch (Call/Listen) and the create/overlay writes live
// here; model writes are in providersettings_models.go.

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/nex-harness/nex/server/internal/configstore"
	"github.com/nex-harness/nex/server/internal/rpcwire"
)

// Call implements Channel.
func (p *ProviderSettings) Call(ctx context.Context, _ *Env, method string, arg rpcwire.Value) (rpcwire.Value, error) {
	switch method {
	case "getView":
		target := readViewTarget(arg)
		view, err := p.view(target)
		return encodeObject(view, err)
	case "refresh":
		// The server has no async sources for P0: answer with the current view
		// (the UI treats it as the authoritative refresh result).
		view, err := p.view(readViewTargetFromReason(arg))
		return encodeObject(view, err)
	case "createPersonalProvider":
		return p.create(ctx, arg)
	case "savePersonalProviderOverlay":
		return p.saveOverlay(ctx, arg)
	case "deletePersonalProvider":
		return p.deleteProvider(ctx, arg)
	case "addPersonalModel":
		return p.addModel(ctx, arg)
	case "savePersonalModelDraft":
		return p.saveModelDraft(ctx, arg)
	case "setPersonalModelEnabled":
		return p.setModelEnabled(ctx, arg)
	case "deletePersonalModel":
		return p.deleteModel(ctx, arg)
	case "reorderPersonalProviders":
		return p.reorderProviders(ctx, arg)
	case "reorderPersonalModels":
		return p.reorderModels(ctx, arg)
	case "resolveModelConfig":
		return p.resolveModelConfig(arg)
	case "lookupModelInfo", "listRemoteModels", "testModelConnectivity":
		return nil, fmt.Errorf("%s is not available on the nex-next server in this release: this server does not call model providers or models.dev; use \"fetch model info\" from a local desktop, or type the model id manually", method)
	}
	return nil, &MethodNotFoundError{Method: method}
}

// Listen implements Channel: onDidChange re-broadcasts the view.
func (p *ProviderSettings) Listen(_ context.Context, env *Env, event string, arg rpcwire.Value, emit func(rpcwire.Value)) (func(), error) {
	if event != "onDidChange" {
		return nil, nil
	}
	target := readViewTarget(arg)
	id, stop := viewBroadcasts.subscribe(func() {
		view, err := p.view(target)
		if err != nil {
			return
		}
		emit(rpcwire.Value(viewJSON(view)))
	})
	_ = env
	return func() { viewBroadcasts.unsubscribe(id, stop) }, nil
}

func encodeObject(value map[string]any, err error) (rpcwire.Value, error) {
	if err != nil {
		return nil, err
	}
	return rpcwire.Value(viewJSON(value)), nil
}

func viewJSON(value any) json.RawMessage {
	encoded, err := json.Marshal(value)
	if err != nil {
		return json.RawMessage(`{}`)
	}
	return encoded
}

func (p *ProviderSettings) create(ctx context.Context, arg rpcwire.Value) (rpcwire.Value, error) {
	input := decodeObjectMap(firstArgument(arg))
	providerName := strings.TrimSpace(jsonString(input["providerName"]))
	templateID := strings.TrimSpace(jsonString(input["templateId"]))
	detail := decodeObjectMap(input["initialConfig"])

	id := strings.TrimSpace(jsonString(input["providerId"]))
	if id == "" {
		id = strings.TrimSpace(jsonString(detail["providerId"]))
	}
	if id == "" {
		if templateID != "" && templateID != "custom" {
			id = templateID
		} else {
			id = "custom-" + randomSuffix()
		}
	}
	if providerName == "" {
		providerName = strings.TrimSpace(jsonString(detail["providerName"]))
	}
	if providerName == "" {
		providerName = id
	}
	apiFormat := jsonString(detail["apiFormat"])
	if apiFormat == "" {
		apiFormat = "anthropic-messages"
	}
	apiType, err := piAPI(apiFormat)
	if err != nil {
		return nil, err
	}
	// A new provider starts disabled: it has no BaseURL/key yet, and enabling it
	// would put an unusable provider into every runner's server-owned config.
	provider := configstore.Provider{
		ID:      id,
		Name:    providerName,
		API:     apiType,
		BaseURL: strings.TrimSpace(jsonString(detail["baseUrl"])),
		Enabled: false,
		Models:  []configstore.Model{},
	}
	if provider.BaseURL == "" {
		provider.BaseURL = "https://api.example.invalid/v1"
	}
	doc, err := p.manager.store.UpsertProvider(provider)
	if err != nil {
		return nil, err
	}
	order := append([]string{}, doc.Scopes.Global.ProviderOrder...)
	order = append(order, id)
	if _, err := p.manager.store.SetProviderOrder(order); err != nil {
		return nil, err
	}
	view, err := p.view(viewTarget{})
	if err != nil {
		return nil, err
	}
	result, err := json.Marshal(map[string]any{"providerId": id, "view": view})
	if err != nil {
		return nil, err
	}
	return rpcwire.Value(json.RawMessage(result)), nil
}

// saveOverlay applies the UI's sparse provider overlay (group/access/api/
// visibility) plus optional metadata (providerName/templateId/enabled).
func (p *ProviderSettings) saveOverlay(ctx context.Context, arg rpcwire.Value) (rpcwire.Value, error) {
	args := positionalArguments(arg)
	if len(args) < 2 {
		return nil, fmt.Errorf("savePersonalProviderOverlay(providerId, config, metadata?)")
	}
	providerID := argumentString(args[0])
	if providerID == "" {
		return nil, fmt.Errorf("providerId is required")
	}
	config := decodeObjectMap(args[1])
	var metadata map[string]json.RawMessage
	// The UI omits `metadata` entirely (or sends undefined) when only the form
	// fields changed; decodeObjectMap maps null to an empty map.
	if len(args) >= 3 && !isUndefined(args[2]) {
		metadata = decodeObjectMap(args[2])
	}
	_, err := p.manager.store.UpdateProvider(providerID, "provider-overlay", func(provider *configstore.Provider) error {
		applyProviderOverlay(provider, config)
		applyProviderMetadata(provider, metadata)
		if len(provider.Models) == 0 {
			provider.Enabled = false
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	if err := p.storeProviderSecret(providerID); err != nil {
		return nil, err
	}
	view, err := p.view(readViewTargetFromArgs(args))
	return encodeObject(view, err)
}

// storeProviderSecret moves an inline key out of the doc into the secret store.
//
// The doc may carry a literal key (the UI always sends the effective `access.
// apiKey` back), but persisted config must never contain one: the value is
// sealed under SecretID(providerID) and the doc keeps only the reference. A
// `${ENV}`/`!command` reference and an existing `sec_…` reference stay as-is; an
// empty value clears the stored key.
func (p *ProviderSettings) storeProviderSecret(providerID string) error {
	doc, err := p.manager.store.Snapshot()
	if err != nil {
		return err
	}
	provider := doc.Scopes.Global.Providers[providerID]
	if provider == nil {
		return nil
	}
	key := strings.TrimSpace(provider.APIKey)
	switch {
	case key == "":
		return p.manager.store.Secrets().Delete(configstore.SecretID(providerID))
	case strings.HasPrefix(key, configstore.SecretRefPrefix),
		strings.HasPrefix(key, "${"), strings.HasPrefix(key, "!"):
		return nil
	}
	if err := p.manager.store.Secrets().Put(configstore.SecretID(providerID), key); err != nil {
		return err
	}
	_, err = p.manager.store.UpdateProvider(providerID, "provider-secret", func(provider *configstore.Provider) error {
		provider.APIKey = configstore.SecretID(providerID)
		return nil
	})
	return err
}

func applyProviderOverlay(provider *configstore.Provider, config map[string]json.RawMessage) {
	if api, ok := config["api"]; ok && !isNull(api) {
		fields := decodeObjectMap(api)
		if value := strings.TrimSpace(jsonString(fields["type"])); value != "" {
			mapped, err := piAPI(value)
			if err == nil {
				provider.API = mapped
			}
		}
		if baseURL := strings.TrimSpace(jsonString(fields["baseUrl"])); baseURL != "" {
			provider.BaseURL = baseURL
		}
		if headers, ok := fields["headers"]; ok && !isNull(headers) {
			var parsed map[string]string
			if json.Unmarshal(headers, &parsed) == nil {
				provider.Headers = parsed
			}
		}
	}
	if access, ok := config["access"]; ok && !isNull(access) {
		fields := decodeObjectMap(access)
		if key, ok := fields["apiKey"]; ok && !isNull(key) {
			provider.APIKey = strings.TrimSpace(jsonString(key))
		}
	}
}

func applyProviderMetadata(provider *configstore.Provider, metadata map[string]json.RawMessage) {
	if metadata == nil {
		return
	}
	if name := strings.TrimSpace(jsonString(metadata["providerName"])); name != "" {
		provider.Name = name
	}
	if enabled, ok := metadata["enabled"]; ok && !isNull(enabled) {
		var value bool
		if json.Unmarshal(enabled, &value) == nil {
			provider.Enabled = value
		}
	}
}

func (p *ProviderSettings) deleteProvider(ctx context.Context, arg rpcwire.Value) (rpcwire.Value, error) {
	providerID := argumentString(firstArgument(arg))
	if providerID == "" {
		return nil, fmt.Errorf("providerId is required")
	}
	if _, err := p.manager.store.DeleteProvider(providerID); err != nil {
		return nil, err
	}
	view, err := p.view(viewTarget{})
	return encodeObject(view, err)
}

// resolveModelConfig previews a draft (inherited = effective here: a doc model
// has no inheritance layer in P0).
func (p *ProviderSettings) resolveModelConfig(arg rpcwire.Value) (rpcwire.Value, error) {
	input := decodeObjectMap(firstArgument(arg))
	providerID := jsonString(input["providerId"])
	if providerID == "" {
		input = decodeObjectMap(arg)
		providerID = jsonString(input["providerId"])
	}
	config := decodeObjectMap(input["personalConfig"])
	modelID := jsonString(input["modelId"])
	model := modelFromConfig(modelID, config)
	effective := modelConfigObject(model, model.ReasoningLevels)
	body, err := json.Marshal(map[string]any{
		"inheritedConfig": effective,
		"effectiveConfig": effective,
		"issues":          []any{},
	})
	if err != nil {
		return nil, err
	}
	return rpcwire.Value(json.RawMessage(body)), nil
}

// modelFromConfig compiles the UI's sparse ModelConfigObject into the strict doc
// model. Defaults match the composer's requirements (non-empty reasoning levels).
