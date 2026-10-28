package local

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"testing"

	"github.com/nex-harness/nex/server/internal/configstore"
	"github.com/nex-harness/nex/server/internal/rpcwire"
	"github.com/nex-harness/nex/server/internal/runners"
)

// fakeLink is a runners.Link that answers exportLocal.
type fakeLink struct {
	export any
}

func (f *fakeLink) Call(context.Context, string, string, json.RawMessage) (json.RawMessage, error) {
	encoded, err := json.Marshal(f.export)
	return encoded, err
}
func (f *fakeLink) Listen(string, string, string, json.RawMessage, func(json.RawMessage)) error {
	return nil
}
func (f *fakeLink) Unlisten(string)       {}
func (f *fakeLink) Close()                {}
func (f *fakeLink) Done() <-chan struct{} { return nil }

func newTestProviderSettings(t *testing.T) (*ProviderSettings, *Manager, *runners.Registry) {
	t.Helper()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	registry := runners.NewRegistry(logger)
	store := configstore.NewStore(t.TempDir(), logger)
	manager := NewManager(store, registry, logger, func() { PublishConfigViews() })
	return newProviderSettings(manager), manager, registry
}

func TestProviderViewCompilesDocAndFacts(t *testing.T) {
	settings, manager, registry := newTestProviderSettings(t)
	apiFormat := "openai-chat-completions"
	// The UI's create + overlay + add-model calls, in the same argument shapes
	// the browser sends (`[a, b, c]` raw JSON).
	created := callJSON(t, settings, "createPersonalProvider", `[{"providerName":"Acme"}]`)
	var createdBody map[string]any
	_ = json.Unmarshal(created, &createdBody)
	providerID, _ := createdBody["providerId"].(string)
	if providerID == "" {
		t.Fatalf("no provider id: %s", created)
	}
	callJSON(t, settings, "savePersonalProviderOverlay", `["`+providerID+`",{"group":"standard-personal",`+
		`"access":{"type":"api-key","apiKey":"sk-test-1234"},"api":{"type":"`+apiFormat+`","baseUrl":"https://api.acme.test/v1"}},{"enabled":false}]`)
	modelConfig := `{"enabled":true,"name":"Acme Large","properties":{"contextWindow":32000},` +
		`"optionSpecs":{"reasoningLevel":{"values":["off","low"],"map":"{\"reasoning_effort\": reasoningLevel}"},` +
		`"maxOutputTokens":{"max":2048,"map":"{\"max_tokens\": maxOutputTokens}"}}}`
	added := callJSON(t, settings, "addPersonalModel", `["`+providerID+`","acme-large",`+modelConfig+`,false]`)
	var view map[string]any
	if err := json.Unmarshal(added, &view); err != nil {
		t.Fatalf("view: %v (%s)", err, added)
	}
	providers, _ := view["providers"].([]any)
	if len(providers) != 1 {
		t.Fatalf("providers = %+v", view["providers"])
	}
	provider, _ := providers[0].(map[string]any)
	if provider["executable"] != false {
		t.Fatalf("a disabled provider must not be executable: %+v", provider)
	}
	effective, _ := provider["effectiveConfig"].(map[string]any)
	access, _ := effective["access"].(map[string]any)
	if access["apiKey"] != "••••1234" {
		t.Fatalf("apiKey hint = %v", access["apiKey"])
	}
	models, _ := provider["models"].([]any)
	model, _ := models[0].(map[string]any)
	specs, _ := model["effectiveConfig"].(map[string]any)
	optionSpecs, _ := specs["optionSpecs"].(map[string]any)
	reasoning, _ := optionSpecs["reasoningLevel"].(map[string]any)
	levels, _ := reasoning["values"].([]any)
	if len(levels) != 2 || levels[0] != "off" {
		t.Fatalf("reasoning levels = %+v", levels)
	}

	// Enable it and record runner facts: the model becomes selectable and the
	// fact's reasoning levels replace the doc's.
	callJSON(t, settings, "savePersonalProviderOverlay", `["`+providerID+`",{},{"enabled":true}]`)
	registry.Register(runners.Info{RunnerID: "alpha", Workspaces: []runners.Workspace{{Path: "/w"}}}, &fakeLink{})
	manager.ConfigApplied("alpha", 1, true, nil, map[string]any{
		"modelsByProvider": map[string]any{
			providerID: map[string]any{"configured": true, "models": []map[string]any{{
				"providerId": providerID, "modelId": "acme-large", "name": "Acme Large",
				"api": "openai-chat-completions", "baseUrl": "https://api.acme.test/v1",
				"reasoningLevels": []string{"off", "low", "high"}, "contextWindow": 32000, "maxTokens": 2048,
			}}},
		},
	})
	selectable := callJSON(t, settings, "model-selection", "")
	if selectable != nil {
		t.Fatal("unused")
	}
	selection := NewModelSelectionChannel(manager)
	result := callSelection(t, selection, `[{"selection":{"providerId":"`+providerID+`","modelId":"acme-large","options":{"reasoningLevel":"high"}}}]`)
	if result.SelectionIssue != "" || result.EffectiveModel != "acme-large" || result.EffectiveLevel != "high" {
		t.Fatalf("selection = %+v", result)
	}

	// Disabling the model removes it from the picker immediately (desired doc).
	callJSON(t, settings, "setPersonalModelEnabled", `["`+providerID+`","acme-large",false]`)
	empty := callSelection(t, selection, `[{"selection":null}]`)
	if len(empty.Providers) != 0 {
		t.Fatalf("providers after disable = %+v", empty.Providers)
	}
}

// modelSelectionArgs sends the positional call envelope as the browser does.
func modelSelectionArgs(arg string) rpcwire.Value {
	return rpcwire.Value(json.RawMessage(arg))
}

type selectionResult struct {
	Providers      []any
	EffectiveModel string
	EffectiveLevel string
	SelectionIssue string
}

func callSelection(t *testing.T, channel Channel, arg string) selectionResult {
	t.Helper()
	value, err := channel.Call(context.Background(), &Env{}, "getView", modelSelectionArgs(arg))
	if err != nil {
		t.Fatalf("model-selection.getView: %v", err)
	}
	encoded, _ := json.Marshal(value)
	var view struct {
		Providers          []any `json:"providers"`
		EffectiveSelection *struct {
			ModelID string `json:"modelId"`
			Options struct {
				ReasoningLevel string `json:"reasoningLevel"`
			} `json:"options"`
		} `json:"effectiveSelection"`
		SelectionIssue string `json:"selectionIssue"`
	}
	if err := json.Unmarshal(encoded, &view); err != nil {
		t.Fatalf("decoding selection view: %v (%s)", err, encoded)
	}
	result := selectionResult{Providers: view.Providers, SelectionIssue: view.SelectionIssue}
	if view.EffectiveSelection != nil {
		result.EffectiveModel = view.EffectiveSelection.ModelID
		result.EffectiveLevel = view.EffectiveSelection.Options.ReasoningLevel
	}
	return result
}

func callJSON(t *testing.T, settings *ProviderSettings, method, arg string) []byte {
	t.Helper()
	if method == "model-selection" {
		return nil
	}
	value, err := settings.Call(context.Background(), &Env{}, method, rpcwire.Value(json.RawMessage(arg)))
	if err != nil {
		t.Fatalf("%s(%s): %v", method, arg, err)
	}
	if value == nil {
		return nil
	}
	encoded, _ := json.Marshal(value)
	return encoded
}

func TestModelSelectionWithoutRunner(t *testing.T) {
	// Fallback mode (no owned provider) and no runner: the stable error.
	_, manager, _ := newTestProviderSettings(t)
	selection := NewModelSelectionChannel(manager)
	if _, err := selection.Call(context.Background(), &Env{}, "getView", modelSelectionArgs(`[{"selection":null}]`)); err == nil {
		t.Fatal("expected no runner connected")
	}
}

func TestModelSelectionServerOwned(t *testing.T) {
	settings, manager, _ := newTestProviderSettings(t)
	callJSON(t, settings, "createPersonalProvider", `[{"providerName":"Acme"}]`)
	doc, err := manager.store.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	var providerID string
	for id := range doc.Scopes.Global.Providers {
		providerID = id
	}
	callJSON(t, settings, "savePersonalProviderOverlay", `["`+providerID+`",{"access":{"type":"api-key","apiKey":"sk-1"},`+
		`"api":{"type":"openai-chat-completions","baseUrl":"https://api.acme.test/v1"}}]`)
	callJSON(t, settings, "addPersonalModel", `["`+providerID+`","m1",{"optionSpecs":{"reasoningLevel":{"values":["off","high"]}}},false]`)
	callJSON(t, settings, "savePersonalProviderOverlay", `["`+providerID+`",{},{"enabled":true}]`)
	selection := NewModelSelectionChannel(manager)
	result := callSelection(t, selection, `[{"selection":{"providerId":"`+providerID+`","modelId":"m1","options":{"reasoningLevel":"high"}}}]`)
	if result.SelectionIssue != "" || result.EffectiveModel != "m1" || result.EffectiveLevel != "high" {
		t.Fatalf("selection = %+v", result)
	}
	// No runner connected is fine in server-owned mode: the doc is the source.
	unknown := callSelection(t, selection, `[{"selection":{"providerId":"nope","modelId":"m1"}}]`)
	if unknown.SelectionIssue != "provider-not-found" {
		t.Fatalf("issue = %q", unknown.SelectionIssue)
	}
}

func TestUnsupportedProviderMethodsFailClearly(t *testing.T) {
	settings, _, _ := newTestProviderSettings(t)
	for _, method := range []string{"lookupModelInfo", "listRemoteModels", "testModelConnectivity"} {
		_, err := settings.Call(context.Background(), &Env{}, method, rpcwire.Value(json.RawMessage(`[{}]`)))
		if err == nil {
			t.Fatalf("%s accepted", method)
		}
	}
}

// TestModelSelectionFallbackFromFacts covers decision 2: with no server
// provider the runner keeps its own ~/.pi/agent catalog and reports it in
// configApplied.facts; the server compiles the picker from those facts instead
// of forwarding (a forward left peers without the channel unanswered).
func TestModelSelectionFallbackFromFacts(t *testing.T) {
	_, manager, registry := newTestProviderSettings(t)
	selection := NewModelSelectionChannel(manager)
	// No facts yet: the stable error the UI retries, never an empty view.
	if _, err := selection.Call(context.Background(), &Env{}, "getView",
		modelSelectionArgs(`[{"selection":null}]`)); err == nil {
		t.Fatal("expected no runner connected before the first facts")
	}
	registry.Register(runners.Info{RunnerID: "alpha", Workspaces: []runners.Workspace{{Path: "/w"}}}, &fakeLink{})
	manager.ConfigApplied("alpha", 1, true, nil, map[string]any{
		"mode": "fallback",
		"modelsByProvider": map[string]any{
			"faux": map[string]any{"configured": true, "models": []map[string]any{{
				"providerId": "faux", "modelId": "faux-1", "name": "Faux 1",
				"api": "openai-chat-completions", "baseUrl": "http://127.0.0.1:18080/v1",
				"reasoningLevels": []string{"off"}, "contextWindow": 128000, "maxTokens": 8192,
			}}},
			"unkeyed": map[string]any{"configured": false, "models": []map[string]any{{
				"providerId": "unkeyed", "modelId": "u1",
			}}},
		},
	})
	result := callSelection(t, selection, `[{"selection":{"providerId":"faux","modelId":"faux-1","options":{"reasoningLevel":"off"}}}]`)
	if len(result.Providers) != 1 {
		t.Fatalf("providers = %+v (an unconfigured provider must not be selectable)", result.Providers)
	}
	if result.SelectionIssue != "" || result.EffectiveModel != "faux-1" || result.EffectiveLevel != "off" {
		t.Fatalf("selection = %+v", result)
	}
}

// TestModelSelectionFallbackWithDisabledProvider 锁住 settings→mcp 全量回归：
// settings 套件会把自建的 provider 置为 disabled（doc 里只剩 disabled provider）。
// runner 侧 owned = enabledProviders>0 → fallback 模式保留本机 catalog；若
// executableModels 按 ProviderList()（含 disabled）判空，会误走 server-owned
// 分支并返回空模型列表，composer 显示 "No model available" 而 runner 实际可用。
// 这里断言：只剩 disabled provider 时，view 仍按 fallback facts 编译。
func TestModelSelectionFallbackWithDisabledProvider(t *testing.T) {
	settings, manager, registry := newTestProviderSettings(t)
	callJSON(t, settings, "createPersonalProvider", `[{"providerName":"Acme"}]`)
	doc, err := manager.store.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	var providerID string
	for id := range doc.Scopes.Global.Providers {
		providerID = id
	}
	callJSON(t, settings, "savePersonalProviderOverlay", `["`+providerID+`",`+
		`{"access":{"type":"api-key","apiKey":"sk-1"},"api":{"type":"openai-chat-completions","baseUrl":"https://api.acme.test/v1"}},`+
		`{"enabled":false}]`)

	registry.Register(runners.Info{RunnerID: "alpha", Workspaces: []runners.Workspace{{Path: "/w"}}}, &fakeLink{})
	manager.ConfigApplied("alpha", 1, true, nil, map[string]any{
		"mode": "fallback",
		"modelsByProvider": map[string]any{
			"faux": map[string]any{"configured": true, "models": []map[string]any{{
				"providerId": "faux", "modelId": "faux-1", "name": "Faux 1",
				"api": "openai-chat-completions", "baseUrl": "http://127.0.0.1:18080/v1",
				"reasoningLevels": []string{"off"}, "contextWindow": 128000, "maxTokens": 8192,
			}}},
		},
	})
	selection := NewModelSelectionChannel(manager)
	result := callSelection(t, selection, `[{"selection":{"providerId":"faux","modelId":"faux-1","options":{"reasoningLevel":"off"}}}]`)
	if len(result.Providers) != 1 {
		t.Fatalf("providers = %+v (a disabled-only doc must fall back to runner facts)", result.Providers)
	}
	if result.SelectionIssue != "" || result.EffectiveModel != "faux-1" {
		t.Fatalf("selection = %+v", result)
	}
}

// TestModelSelectionFallbackWithOtherRunnerScope 锁住 model-onboarding 决策 4：
// server-owned 模式必须按 runner 判定。另一个 runner 的 scope 拥有 enabled
// provider 时，服务端全局 OwnsProviders() 为 true；此时一个 effective 层里 0 个
// enabled provider 的 fallback runner 仍须用自己上报的 catalog 编译 view，而不是
// 返回空列表（与 push 的 ownedMode 一致）。
func TestModelSelectionFallbackWithOtherRunnerScope(t *testing.T) {
	settings, manager, registry := newTestProviderSettings(t)
	callJSON(t, settings, "createPersonalProvider", `[{"providerName":"Acme"}]`)
	doc, err := manager.store.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	var providerID string
	for id := range doc.Scopes.Global.Providers {
		providerID = id
	}
	// 把 provider 从 global 移出并只写进 beta 的 runner scope：alpha 的 effective
	// 层因此没有任何 provider，而 OwnsProviders() 仍为 true。
	callJSON(t, settings, "savePersonalProviderOverlay", `["`+providerID+`",`+
		`{"access":{"type":"api-key","apiKey":"sk-1"},"api":{"type":"openai-chat-completions","baseUrl":"https://api.acme.test/v1"}},`+
		`{"enabled":true}]`)
	owned := testProviderForScope(providerID)
	if _, _, err := manager.store.ImportLayer("beta", configstore.Layer{
		Providers: map[string]*configstore.Provider{providerID: owned},
	}); err != nil {
		t.Fatalf("beta import: %v", err)
	}
	if _, err := manager.store.Update("test-move", func(doc *configstore.ConfigDoc) error {
		delete(doc.Scopes.Global.Providers, providerID)
		doc.Scopes.Global.ProviderOrder = nil
		return nil
	}); err != nil {
		t.Fatalf("removing the global provider: %v", err)
	}
	if !manager.OwnsProviders() {
		t.Fatal("beta's runner scope must make the server report owned providers")
	}

	registry.Register(runners.Info{RunnerID: "alpha", Name: "Alpha", Workspaces: []runners.Workspace{{Path: "/w"}}}, &fakeLink{})
	manager.OnLinkReady("alpha", func(uint64, any) error { return nil })
	manager.ConfigApplied("alpha", 1, true, nil, map[string]any{
		"mode": "fallback",
		"modelsByProvider": map[string]any{
			"faux": map[string]any{"configured": true, "models": []map[string]any{{
				"providerId": "faux", "modelId": "faux-1", "name": "Faux 1",
				"api": "openai-chat-completions", "baseUrl": "http://127.0.0.1:18080/v1",
				"reasoningLevels": []string{"off"}, "contextWindow": 128000, "maxTokens": 8192,
			}}},
		},
	})
	selection := NewModelSelectionChannel(manager)
	result := callSelection(t, selection, `[{"selection":{"providerId":"faux","modelId":"faux-1","options":{"reasoningLevel":"off"}}}]`)
	if len(result.Providers) != 1 {
		t.Fatalf("providers = %+v (alpha has no enabled provider of its own, so it must fall back to its facts)", result.Providers)
	}
	if result.EffectiveModel != "faux-1" {
		t.Fatalf("selection = %+v", result)
	}

	// The status endpoint reports the same count for alpha.
	statuses := manager.RunnerModelStatuses()
	if len(statuses) != 1 || statuses[0].RunnerID != "alpha" || statuses[0].ModelCount != 1 {
		t.Fatalf("statuses = %+v", statuses)
	}
	if statuses[0].Mode != RunnerModeFallback || !statuses[0].Connected || statuses[0].Name != "Alpha" {
		t.Fatalf("status = %+v", statuses[0])
	}
	if statuses[0].LastAppliedVersion != 1 {
		t.Fatalf("lastAppliedVersion = %d", statuses[0].LastAppliedVersion)
	}
}

// testProviderForScope builds a complete enabled provider for a runner scope.
func testProviderForScope(id string) *configstore.Provider {
	return &configstore.Provider{
		ID: id, API: configstore.APITypeOpenAIChatCompletions,
		BaseURL: "https://api.acme.test/v1", Enabled: true,
		APIKey: configstore.SecretID(id),
		Models: []configstore.Model{{
			ID: "m1", Name: "M1", ReasoningLevels: []string{"off"},
			ContextWindow: 128000, MaxTokens: 8192, Input: []string{"text"}, Enabled: true,
		}},
	}
}

// TestRunnerModelStatusesSortsByName 锁住 GET /api/config/runners 的排序与
// name fallback：按 hello name 排序（同名再按 id），name 缺失时回退到 id，未上报
// facts 的 runner 是 unknown 且 modelCount=0。
func TestRunnerModelStatusesSortsByName(t *testing.T) {
	_, manager, registry := newTestProviderSettings(t)
	registry.Register(runners.Info{RunnerID: "zeta", Name: "Alpha", Workspaces: []runners.Workspace{{Path: "/z"}}}, &fakeLink{})
	registry.Register(runners.Info{RunnerID: "beta", Name: "Beta", Workspaces: []runners.Workspace{{Path: "/b"}}}, &fakeLink{})
	// KnownRunners/Connected come from the manager's links, not the registry.
	manager.OnLinkReady("zeta", func(uint64, any) error { return nil })
	manager.OnLinkReady("beta", func(uint64, any) error { return nil })
	// Facts-only runner: offline and unnamed.
	manager.ConfigApplied("ghost", 3, true, nil, map[string]any{"mode": "server"})

	statuses := manager.RunnerModelStatuses()
	if len(statuses) != 3 {
		t.Fatalf("statuses = %+v", statuses)
	}
	// Sorted by name: "Alpha" (zeta), "Beta" (beta), "ghost" (id fallback).
	if statuses[0].RunnerID != "zeta" || statuses[0].Name != "Alpha" {
		t.Fatalf("order by name = %+v", statuses)
	}
	if statuses[1].RunnerID != "beta" || statuses[2].RunnerID != "ghost" || statuses[2].Name != "ghost" {
		t.Fatalf("order by name/name fallback = %+v", statuses)
	}
	if statuses[2].Mode != RunnerModeServer || statuses[2].Connected || statuses[2].LastAppliedVersion != 3 {
		t.Fatalf("facts-only status = %+v", statuses[2])
	}
	for _, status := range statuses[:2] {
		if status.Mode != RunnerModeUnknown || status.ModelCount != 0 {
			t.Fatalf("a runner without facts must be unknown/0: %+v", status)
		}
		if !status.Connected {
			t.Fatalf("registered runner must report connected: %+v", status)
		}
	}
}
