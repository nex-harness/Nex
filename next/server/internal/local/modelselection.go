package local

// Server-owned `model-selection` (spec decision 6, research §C5).
//
// The view is compiled from the same doc+facts pair as provider-settings and
// keeps the runner's `effectiveSelection` contract (docs/next/README.md "Runner
// view contracts"; the runner's own logic lives in packages/next-runner/src/
// channels/modelViews.ts): the composer reads `effectiveSelection ?? undefined`
// as its model and refuses to submit without it, so an executable intent must
// come back resolved.
//
// Decision: the server answers this channel itself in both modes, so the picker
// does not depend on which machine a tab was opened on and an old peer (or a
// runner without the channel) can never leave a getView unanswered.
//
//   - Server-owned mode (the doc has an enabled provider): the view is compiled
//     from the doc + that runner's facts.
//   - Fallback mode (no server providers, decision 2): the runner keeps using
//     its own ~/.pi/agent, so the server compiles its view from the catalog that
//     runner reported in `configApplied.facts.modelsByProvider`. Until the target
//     runner has reported one, the answer is the stable "no runner connected"
//     error (retried by the UI) instead of an empty picker.

import (
	"context"
	"encoding/json"
	"sort"

	"github.com/tylinux/nex-next/server/internal/configstore"
	"github.com/tylinux/nex-next/server/internal/rpcwire"
	"github.com/tylinux/nex-next/server/internal/runners"
)

// ModelSelection serves the model-selection channel.
type ModelSelection struct {
	providerSettings *ProviderSettings
}

func newModelSelection(providerSettings *ProviderSettings) *ModelSelection {
	return &ModelSelection{providerSettings: providerSettings}
}

// modelOption is one executable model of one provider (doc + facts).
type modelOption struct {
	ProviderID      string
	ModelID         string
	Name            string
	API             string
	BaseURL         string
	ReasoningLevels []string
	ContextWindow   int
	MaxTokens       int
	Input           []string
}

// executableModels lists the models the composer may offer for one runner and
// reports which source produced them (server doc or the runner's own catalog).
func (m *ModelSelection) executableModels(target viewTarget) ([]modelOption, string, uint64, error) {
	manager := m.providerSettings.manager
	doc, err := manager.store.Snapshot()
	if err != nil {
		return nil, "", 0, err
	}
	runnerID := m.providerSettings.runnerFor(target)
	effective := doc.EffectiveForRunner(runnerID)
	facts, factsRunner := runnerModelFacts(manager, runnerID)
	// 修复依据：runner 判定 server-owned 的谓词是「enabled provider 数 > 0」
	// （packages/next-runner/src/config/runtimeConfig.ts 的 enabledProviders），
	// 而 ProviderList() 含 disabled provider。一个只剩 disabled provider 的 doc
	// （settings 套件禁用了自己创建的 provider 后）在 runner 侧是 fallback 模式
	// （保留本机 catalog），但这里按 ProviderList 非空走了 server-owned 分支，
	// 遍历时 disabled provider 全部被跳过，view 变成空列表——composer 显示
	// "No model available" 而 runner 实际仍有模型。fallback 判定必须数 enabled
	// providers，与 OwnsProviders 的语义一致，picker 才不会与 runtime 分歧。
	enabledCount := 0
	for _, provider := range effective.ProviderList() {
		if provider.Enabled {
			enabledCount++
		}
	}
	if enabledCount == 0 {
		// Fallback mode is decided by exactly the same predicate the router and
		// the config push use, so the picker never disagrees with the runtime.
		if !manager.OwnsProviders() {
			return fallbackModels(facts, factsRunner, runnerID, doc.Version)
		}
		return nil, runnerID, doc.Version, nil
	}
	secrets := manager.store.Secrets()
	var models []modelOption
	for _, provider := range effective.ProviderList() {
		if !provider.Enabled {
			continue
		}
		if !provideAuthStateFor(secrets, provider, facts, factsRunner != "").configured {
			continue
		}
		for _, model := range provider.Models {
			if !model.Enabled {
				continue
			}
			option := modelOption{
				ProviderID:      provider.ID,
				ModelID:         model.ID,
				Name:            model.Name,
				API:             provider.API,
				BaseURL:         provider.BaseURL,
				ReasoningLevels: model.ReasoningLevels,
				ContextWindow:   model.ContextWindow,
				MaxTokens:       model.MaxTokens,
				Input:           model.Input,
			}
			if fact, ok := facts[provider.ID].Models[model.ID]; ok && len(fact.ReasoningLevels) > 0 {
				// Facts win: the runner reports what pi actually accepted.
				option.ReasoningLevels = fact.ReasoningLevels
				option.Name = fact.Name
				option.API = piAPIOr(option.API, fact.API)
				if fact.BaseURL != "" {
					option.BaseURL = fact.BaseURL
				}
			}
			if option.Name == "" {
				option.Name = option.ModelID
			}
			if len(option.ReasoningLevels) == 0 {
				option.ReasoningLevels = []string{"off"}
			}
			models = append(models, option)
		}
	}
	return models, runnerID, doc.Version, nil
}

// fallbackModels compiles the runner's own catalog (fallback mode, decision 2).
// Only the models pi actually exposes are listed; credential state is the
// runner's own (`configured`). A runner that never reported a catalog cannot be
// answered: `runners.ErrNoRunnerConnected` makes the UI retry instead of
// showing an empty picker that would block every send.
func fallbackModels(facts map[string]runnerProviderFacts, factsRunner, runnerID string, version uint64) ([]modelOption, string, uint64, error) {
	if factsRunner == "" {
		return nil, runnerID, version, runners.ErrNoRunnerConnected
	}
	providerIDs := make([]string, 0, len(facts))
	for providerID := range facts {
		providerIDs = append(providerIDs, providerID)
	}
	sort.Strings(providerIDs)
	var models []modelOption
	for _, providerID := range providerIDs {
		provider := facts[providerID]
		if !provider.Configured {
			continue
		}
		modelIDs := make([]string, 0, len(provider.Models))
		for modelID := range provider.Models {
			modelIDs = append(modelIDs, modelID)
		}
		sort.Strings(modelIDs)
		for _, modelID := range modelIDs {
			fact := provider.Models[modelID]
			name := fact.Name
			if name == "" {
				name = fact.ModelID
			}
			levels := fact.ReasoningLevels
			if len(levels) == 0 {
				levels = []string{"off"}
			}
			models = append(models, modelOption{
				ProviderID:      providerID,
				ModelID:         fact.ModelID,
				Name:            name,
				API:             fact.API,
				BaseURL:         fact.BaseURL,
				ReasoningLevels: levels,
				ContextWindow:   fact.ContextWindow,
				MaxTokens:       fact.MaxTokens,
				Input:           fact.Input,
			})
		}
	}
	return models, runnerID, version, nil
}

func piAPIOr(fallback, engineAPI string) string {
	if engineAPI == "" {
		return fallback
	}
	switch engineAPI {
	case configstore.APITypeOpenAIChatCompletionsEngine, configstore.APITypeOpenAIChatCompletions:
		return configstore.APITypeOpenAIChatCompletions
	case "openai-responses":
		return configstore.APITypeOpenAIResponses
	default:
		return engineAPI
	}
}

// Call implements Channel.
func (m *ModelSelection) Call(ctx context.Context, _ *Env, method string, arg rpcwire.Value) (rpcwire.Value, error) {
	if method != "getView" {
		return nil, &MethodNotFoundError{Method: method}
	}
	target := readViewTarget(arg)
	models, _, revision, err := m.executableModels(target)
	if err != nil {
		return nil, err
	}
	selection := readSelection(arg)
	view := buildSelectionView(models, revision, selection)
	encoded, err := json.Marshal(view)
	if err != nil {
		return nil, err
	}
	return rpcwire.Value(json.RawMessage(encoded)), nil
}

// Listen implements Channel: onDidChange re-broadcasts.
func (m *ModelSelection) Listen(_ context.Context, env *Env, event string, arg rpcwire.Value, emit func(rpcwire.Value)) (func(), error) {
	if event != "onDidChange" {
		return nil, nil
	}
	target := readViewTarget(arg)
	id, stop := viewBroadcasts.subscribe(func() {
		models, _, revision, err := m.executableModels(target)
		if err != nil {
			return
		}
		encoded, err := json.Marshal(buildSelectionView(models, revision, readSelection(arg)))
		if err != nil {
			return
		}
		emit(rpcwire.Value(json.RawMessage(encoded)))
	})
	_ = env
	return func() { viewBroadcasts.unsubscribe(id, stop) }, nil
}

// selectionFields is the raw first argument of getView (`{selection: {…}}`).
type selectionFields = map[string]json.RawMessage

// buildSelectionView mirrors the runner's buildModelSelectionViews.modelSelection
// and the @nex/provider resolver (research §A1).
func buildSelectionView(models []modelOption, revision uint64, selection selectionFields) map[string]any {
	type providerGroup struct {
		ProviderID string
		Name       string
		Models     []modelOption
	}
	var groups []providerGroup
	index := map[string]int{}
	for _, model := range models {
		position, ok := index[model.ProviderID]
		if !ok {
			position = len(groups)
			index[model.ProviderID] = position
			groups = append(groups, providerGroup{ProviderID: model.ProviderID, Name: model.ProviderID})
		}
		groups[position].Models = append(groups[position].Models, model)
	}
	providers := make([]any, 0, len(groups))
	for _, group := range groups {
		entryModels := make([]any, 0, len(group.Models))
		for _, model := range group.Models {
			entryModels = append(entryModels, map[string]any{
				"modelId": model.ModelID,
				"config":  modelConfigObject(modelAsDoc(model), model.ReasoningLevels),
			})
		}
		first := group.Models[0]
		providers = append(providers, map[string]any{
			"providerId":   group.ProviderID,
			"providerName": group.Name,
			"templateId":   group.ProviderID,
			"config": map[string]any{
				"group":  "standard-personal",
				"access": map[string]any{"type": "api-key", "apiKey": ""},
				"api":    map[string]any{"type": engineAPI(first.API), "baseUrl": first.BaseURL},
			},
			"models": entryModels,
		})
	}
	view := map[string]any{
		"revision":  revision,
		"providers": providers,
	}
	if preferred, ok := pickPreferred(models); ok {
		view["preferredSelection"] = preferred
	}
	effective, issue := resolveEffectiveSelection(models, selection)
	view["effectiveSelection"] = effective
	if issue != "" {
		view["selectionIssue"] = issue
	}
	return view
}

func modelAsDoc(option modelOption) configstore.Model {
	return configstore.Model{
		ID:              option.ModelID,
		Name:            option.Name,
		Reasoning:       len(option.ReasoningLevels) > 1,
		ReasoningLevels: option.ReasoningLevels,
		ContextWindow:   option.ContextWindow,
		MaxTokens:       option.MaxTokens,
		Input:           option.Input,
		Enabled:         true,
	}
}

func readSelection(arg rpcwire.Value) selectionFields {
	for _, entry := range positionalArguments(arg) {
		fields := decodeObjectMap(entry)
		if raw, ok := fields["selection"]; ok {
			if isNull(raw) {
				return nil
			}
			return decodeObjectMap(raw)
		}
	}
	return nil
}

func selectionIdentity(selection selectionFields) (string, string) {
	if selection == nil {
		return "", ""
	}
	return jsonString(selection["providerId"]), jsonString(selection["modelId"])
}

func selectionReasoning(selection selectionFields) (string, bool) {
	if selection == nil {
		return "", false
	}
	options := decodeObjectMap(selection["options"])
	level := jsonString(options["reasoningLevel"])
	return level, level != ""
}

// pickPreferred mirrors the runner's pickPreferred without a configured default.
func pickPreferred(models []modelOption) (map[string]any, bool) {
	if len(models) == 0 {
		return nil, false
	}
	first := models[0]
	return map[string]any{
		"providerId": first.ProviderID,
		"modelId":    first.ModelID,
		"options":    map[string]any{"reasoningLevel": first.ReasoningLevels[0]},
	}, true
}

// resolveEffectiveSelection mirrors the runner's resolveEffectiveSelection and
// the provider resolver: unknown provider/model → null + issue; missing or
// unsupported reasoning level → identity-only selection + issue; valid → the
// requested selection.
func resolveEffectiveSelection(models []modelOption, selection selectionFields) (map[string]any, string) {
	if selection == nil {
		return nil, "selection-missing"
	}
	providerID, modelID := selectionIdentity(selection)
	var found *modelOption
	providerExists := false
	for index := range models {
		if models[index].ProviderID == providerID {
			providerExists = true
		}
		if models[index].ProviderID == providerID && models[index].ModelID == modelID {
			found = &models[index]
			break
		}
	}
	if found == nil {
		if providerExists {
			return nil, "model-not-found"
		}
		return nil, "provider-not-found"
	}
	level, hasLevel := selectionReasoning(selection)
	if !hasLevel {
		return map[string]any{"providerId": providerID, "modelId": modelID}, "reasoning-level-missing"
	}
	supported := false
	for _, candidate := range found.ReasoningLevels {
		if candidate == level {
			supported = true
			break
		}
	}
	if !supported {
		return map[string]any{"providerId": providerID, "modelId": modelID}, "reasoning-level-not-supported"
	}
	return map[string]any{
		"providerId": providerID,
		"modelId":    modelID,
		"options":    map[string]any{"reasoningLevel": level},
	}, ""
}
