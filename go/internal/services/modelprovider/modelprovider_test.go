// modelprovider_test.go: the service-package behavior tests — personal repo
// write/read/recovery, builtin source materialization, end-to-end runtime
// (config service mutations → registry), settings/selection views, models.dev
// against httptest, remote model list against httptest, connectivity, secret
// redaction in logs, and legacy importer fixtures.
package modelprovider

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/tylinux/nex/go/internal/adapters/logx"
	"github.com/tylinux/nex/go/internal/providerconfig"
)

func ptrStr(v string) *string { return &v }
func ptrBool(v bool) *bool    { return &v }
func ptrInt(v int64) *int64   { return &v }

func testBuiltinSnapshot(t *testing.T) *BuiltinConfigSnapshot {
	t.Helper()
	// minimal built-in: one api-key provider with a builtin model + complete model rule
	providers, err := providerconfig.NewProviderConfigMap([]providerconfig.ProviderConfigRule{{
		ProviderID: "builtin:openai", ProviderName: ptrStr("OpenAI"),
		Config: &providerconfig.ProviderConfig{
			Group:           ptrStr(providerconfig.GroupZaiFamily),
			API:             &providerconfig.ProviderApiConfig{Type: ptrStr(providerconfig.APIOpenAIChatCompletions), BaseURL: ptrStr("https://builtin.example/v1")},
			Access:          &providerconfig.ProviderAccess{APIKey: &providerconfig.ApiKeyAccessConfig{Type: providerconfig.AccessAPIKey, APIKey: ptrStr("builtin-key")}},
			BuiltinModelIDs: []string{"builtin-model"},
			Visibility:      ptrStr("visible"),
		},
	}})
	if err != nil {
		t.Fatal(err)
	}
	models := providerconfig.NewModelConfigRules([]providerconfig.ModelConfigRule{
		{Type: providerconfig.RuleModel, ModelMatch: ".*", Config: completeTestModel()},
	})
	return &BuiltinConfigSnapshot{
		Revision: "nex-builtin:30:test", Providers: providers,
		ProviderTemplates: providerconfig.EmptyProviderTemplateMap(), Models: models,
	}
}

func completeTestModel() *providerconfig.ModelConfig {
	return &providerconfig.ModelConfig{
		Enabled: ptrBool(true),
		Properties: &providerconfig.ModelProperties{
			RequiresMfjsToolSchema: ptrBool(false), ContextWindow: ptrInt(100000),
			InputFormat:      &providerconfig.ModelInputFormat{SupportsText: ptrBool(true), SupportsImage: ptrBool(false), SupportsVideo: ptrBool(false), SupportsAudio: ptrBool(false), SupportsPdf: ptrBool(false)},
			OutputFormat:     &providerconfig.ModelOutputFormat{SupportsText: ptrBool(true)},
			SupportsToolCall: ptrBool(true), SupportsJsonSchemaOutput: ptrBool(false),
			SupportsNativeWebSearch: ptrBool(false), SupportsMidConversationSystem: ptrBool(false),
		},
		OptionSpecs: &providerconfig.ModelOptionSpecs{
			ReasoningLevel:  &providerconfig.EnumOptionSpec{Values: []string{"off", "on"}},
			MaxOutputTokens: &providerconfig.LimitOptionSpec{Max: ptrInt(16000)},
		},
	}
}

// fakeBuiltinSource: a controllable builtin snapshot source.
type fakeBuiltinSource struct {
	mu        sync.Mutex
	snapshot  *BuiltinConfigSnapshot
	listeners []func(string)
}

func (f *fakeBuiltinSource) Read(ctx context.Context) (*BuiltinConfigSnapshot, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.snapshot, nil
}

func (f *fakeBuiltinSource) OnDidChange(listener func(string)) func() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.listeners = append(f.listeners, listener)
	return func() {}
}

func newTestRuntime(t *testing.T) *ProviderRuntime {
	t.Helper()
	dir := t.TempDir()
	return newTestRuntimeAt(t, dir)
}

func newTestRuntimeAt(t *testing.T, dir string) *ProviderRuntime {
	t.Helper()
	builtin := &fakeBuiltinSource{snapshot: testBuiltinSnapshot(t)}
	repo, err := NewPersonalRepo(PersonalRepoOptions{FilePath: filepath.Join(dir, "provider_config.json")})
	if err != nil {
		t.Fatal(err)
	}
	configService := NewConfigService(ConfigServiceOptions{BuiltinSource: builtin, PersonalRepository: repo})
	registry := NewRegistryService(RegistryServiceOptions{
		ConfigSource: configService, AccountSource: &failClosedAccountSource{config: configService},
	})
	runtime := &ProviderRuntime{
		BuiltinSource: nil, PersonalRepo: repo, ConfigService: configService, RegistryService: registry,
	}
	// note: BuiltinSource nil is fine — the fake feeds the config service directly.
	runtime.ProviderSettings = &ProviderSettingsService{runtime: runtime}
	runtime.ModelSelection = &ModelSelectionService{runtime: runtime}
	if err := runtime.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	return runtime
}

// --- personal repository ---

func TestPersonalRepoReadUpdateRecovery(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "provider_config.json")
	var recovered []error
	repo, err := NewPersonalRepo(PersonalRepoOptions{
		FilePath: path, OnRecovery: func(err error) { recovered = append(recovered, err) },
	})
	if err != nil {
		t.Fatal(err)
	}
	// absent file → empty snapshot
	snapshot, err := repo.Read(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Providers.Len() != 0 {
		t.Fatal("absent file must read as empty")
	}
	// update writes canonical JSON
	_, err = repo.Update(context.Background(), func(current *providerconfig.LayerSnapshot) (*providerconfig.LayerUpdate, error) {
		providers, _ := providerconfig.NewProviderConfigMap([]providerconfig.ProviderConfigRule{{
			ProviderID: "p1", Config: &providerconfig.ProviderConfig{Group: ptrStr(providerconfig.GroupStandardPersonal)},
		}})
		return &providerconfig.LayerUpdate{Providers: providers, Models: providerconfig.EmptyModelConfigRules()}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var file map[string]any
	if err := json.Unmarshal(data, &file); err != nil {
		t.Fatal(err)
	}
	if file["schemaVersion"].(float64) != 1 {
		t.Fatal("file must carry schemaVersion 1")
	}
	// invalid file → recovery degrades to empty, file preserved verbatim
	invalid := []byte(`{"schemaVersion": 1, "config": {"providerConfigRules": {"providerRules": [`)
	if err := os.WriteFile(path, invalid, 0o600); err != nil {
		t.Fatal(err)
	}
	snapshot, err = repo.Read(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Providers.Len() != 0 {
		t.Fatal("invalid file must degrade to empty in memory")
	}
	if len(recovered) == 0 {
		t.Fatal("recovery callback must fire")
	}
	preserved, _ := os.ReadFile(path)
	if !bytes.Equal(preserved, invalid) {
		t.Fatal("invalid file must be preserved, never rewritten by recovery")
	}
}

func TestPersonalRepoPollDetectsExternalWrite(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "provider_config.json")
	repo, _ := NewPersonalRepo(PersonalRepoOptions{FilePath: path})
	changes := 0
	repo.OnDidChange(func(reason string) {
		if reason == "poll-changed" {
			changes++
		}
	})
	if _, err := repo.Read(context.Background()); err != nil {
		t.Fatal(err)
	}
	// simulate another process writing a different (valid) personal file
	other, _ := providerconfig.NewProviderConfigMap([]providerconfig.ProviderConfigRule{{
		ProviderID: "external", Config: &providerconfig.ProviderConfig{Group: ptrStr(providerconfig.GroupStandardPersonal)},
	}})
	encoded, _ := providerconfig.EncodeProviderConfigFile(&providerconfig.LayerUpdate{
		Providers: other, Models: providerconfig.EmptyModelConfigRules(),
	})
	data, _ := json.MarshalIndent(encoded, "", "  ")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	repo.PollOnce(context.Background())
	if changes != 1 {
		t.Fatalf("external write must emit poll-changed once, got %d", changes)
	}
	// second poll of the same content must not re-emit
	repo.PollOnce(context.Background())
	if changes != 1 {
		t.Fatalf("same revision must not re-emit, got %d", changes)
	}
}

// --- end-to-end runtime: mutations → registry → views ---

func TestRuntimeEndToEndPersonalProvider(t *testing.T) {
	runtime := newTestRuntime(t)
	defer runtime.Dispose()
	ctx := context.Background()

	// settings view starts with the builtin provider + its model
	view, err := runtime.ProviderSettings.GetView(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(view.Providers) != 1 || view.Providers[0].ProviderID != "builtin:openai" {
		t.Fatalf("want builtin provider visible, got %d", len(view.Providers))
	}
	if len(view.Providers[0].Models) != 1 || !view.Providers[0].Models[0].Builtin {
		t.Fatal("builtin model must be present")
	}

	// create a personal provider
	providerID, err := runtime.ConfigService.CreatePersonalProvider(ctx, CreatePersonalProviderInput{})
	if err != nil {
		t.Fatal(err)
	}
	if providerID != "new-provider" {
		t.Fatalf("first personal provider id, got %q", providerID)
	}
	// registry refresh picks it up
	if _, err := runtime.RegistryService.RequestRefresh(ctx, "test"); err != nil {
		t.Fatal(err)
	}
	view, err = runtime.ProviderSettings.GetView(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(view.Providers) != 2 {
		t.Fatalf("personal provider must appear, got %d", len(view.Providers))
	}
	if len(view.ProviderOrder) != 1 || view.ProviderOrder[0] != providerID {
		t.Fatalf("provider order tracks standard-personal, got %v", view.ProviderOrder)
	}

	// save an overlay with an API key
	err = runtime.ConfigService.SavePersonalProviderOverlay(ctx, providerID, &providerconfig.ProviderConfig{
		Access: &providerconfig.ProviderAccess{APIKey: &providerconfig.ApiKeyAccessConfig{Type: providerconfig.AccessAPIKey, APIKey: ptrStr("sk-test-personal")}},
		API:    &providerconfig.ProviderApiConfig{Type: ptrStr(providerconfig.APIOpenAIChatCompletions), BaseURL: ptrStr("https://personal.example/v1")},
	}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	// add a personal model
	err = runtime.ConfigService.AddPersonalModel(ctx, providerID, "my-model", &providerconfig.ModelConfig{}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runtime.RegistryService.RequestRefresh(ctx, "test"); err != nil {
		t.Fatal(err)
	}

	// the personal model inherits the complete rule from the catch-all builtin rule
	snapshot := runtime.RegistryService.GetSnapshot()
	var found *providerconfig.RegistryProvider
	for i := range snapshot.Registry.Providers {
		if snapshot.Registry.Providers[i].ProviderID == providerID {
			found = &snapshot.Registry.Providers[i]
			break
		}
	}
	if found == nil {
		t.Fatal("personal provider with complete config must enter the registry")
	}
	if len(found.Models) != 1 || found.Models[0].ModelID != "my-model" {
		t.Fatalf("personal model must be registered, got %+v", found.Models)
	}
	if found.Models[0].Config.Properties.ContextWindow == nil || *found.Models[0].Config.Properties.ContextWindow != 100000 {
		t.Fatal("model rule must supply the complete config")
	}

	// selection view: preferred selection resolves through the registry
	selectionView, err := runtime.ModelSelection.GetView(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if selectionView.PreferredSelection == nil {
		t.Fatal("registry has selectable models — a preferred selection must exist")
	}
	if selectionView.PreferredSelection.Options == nil || selectionView.PreferredSelection.Options.ReasoningLevel != "on" {
		t.Fatal("initial selection uses the highest reasoning level")
	}
	// effective resolution with an explicit input
	input := providerconfig.ModelSelection{ProviderID: providerID, ModelID: "my-model"}
	selectionView, err = runtime.ModelSelection.GetView(ctx, &input)
	if err != nil {
		t.Fatal(err)
	}
	if selectionView.SelectionIssue == nil || *selectionView.SelectionIssue != "reasoning-level-missing" {
		t.Fatalf("missing level must surface the issue, got %+v", selectionView.SelectionIssue)
	}
}

// --- models.dev against httptest ---

func TestModelsDevCatalogFetchAndCache(t *testing.T) {
	catalogSample := map[string]any{
		"openai": map[string]any{
			"models": map[string]any{
				"gpt-test": map[string]any{
					"limit":             map[string]any{"context": 128000, "output": 16384},
					"tool_call":         true,
					"structured_output": true,
					"modalities":        map[string]any{"input": []string{"text", "image"}},
					"reasoning_options": []any{map[string]any{"type": "effort", "values": []string{"low", "high"}}},
				},
			},
		},
		"aggregator": map[string]any{
			"models": map[string]any{
				"openai/gpt-test": map[string]any{
					"limit": map[string]any{"context": 64000, "output": 8192},
				},
			},
		},
	}
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		w.Header().Set("content-type", "application/json")
		_ = json.NewEncoder(w).Encode(catalogSample)
	}))
	defer server.Close()

	dir := t.TempDir()
	cachePath := filepath.Join(dir, "cache.json")
	clock := &fakeClock{now: time.UnixMilli(1_000_000)}
	modelsDev := NewModelsDev(ModelsDevOptions{FetchURL: server.URL, CachePath: cachePath, Now: clock.Now})

	result, err := modelsDev.LookupModelInfo(context.Background(), "gpt-test")
	if err != nil {
		t.Fatal(err)
	}
	if !result.Found || result.ProviderID != "openai" {
		t.Fatalf("want found openai, got %+v", result)
	}
	config := result.Config
	props := config["properties"].(map[string]any)
	// majority of {128000, 64000} — counts equal, number tie prefers larger
	if numberValue(t, props["contextWindow"]) != 128000 {
		t.Fatalf("contextWindow majority tie must prefer the larger, got %v", props["contextWindow"])
	}
	if props["supportsToolCall"].(bool) != true {
		t.Fatal("tool_call majority must be true")
	}
	inputFormat, ok := props["inputFormat"].(map[string]bool)
	if !ok {
		if m, ok2 := props["inputFormat"].(map[string]any); ok2 {
			inputFormat = map[string]bool{}
			for k, v := range m {
				inputFormat[k], _ = v.(bool)
			}
		}
	}
	if !inputFormat["supportsImage"] || inputFormat["supportsVideo"] {
		t.Fatal("declared modality supported, undeclared not")
	}
	specs := config["optionSpecs"].(map[string]any)
	reasoning := specs["reasoningLevel"].(map[string]any)
	if len(stringSliceValue(t, reasoning["values"])) != 2 {
		t.Fatal("effort levels must survive")
	}
	if _, ok := specs["maxOutputTokens"]; !ok {
		t.Fatal("output limit majority must produce maxOutputTokens")
	}

	// second call within the window: memory cache, no second HTTP request
	if _, err := modelsDev.LookupModelInfo(context.Background(), "gpt-test"); err != nil {
		t.Fatal(err)
	}
	if requests != 1 {
		t.Fatalf("fresh window must not refetch, requests=%d", requests)
	}
	// 23h after the fetch: still inside the 24h window — memory cache hit
	clock.now = time.UnixMilli(1_000_000 + int64(23*time.Hour/time.Millisecond))
	if _, err := modelsDev.LookupModelInfo(context.Background(), "gpt-test"); err != nil {
		t.Fatal(err)
	}
	if requests != 1 {
		t.Fatalf("within-window read must not refetch, requests=%d", requests)
	}
	// 25h: expired → refetch (updates memory + disk with fetchedAt=now)
	clock.now = time.UnixMilli(1_000_000 + int64(25*time.Hour/time.Millisecond))
	if _, err := modelsDev.LookupModelInfo(context.Background(), "openai/gpt-test"); err != nil {
		t.Fatal(err)
	}
	if requests != 2 {
		t.Fatalf("expired cache must refetch, requests=%d", requests)
	}
	// namespace-aware: the prefixed id resolved the same entry
	// offline + expired: stale disk fallback serves the last good catalog
	server.Close()
	clock.now = time.UnixMilli(1_000_000 + int64(50*time.Hour/time.Millisecond))
	if _, err := modelsDev.LookupModelInfo(context.Background(), "gpt-test"); err != nil {
		t.Fatalf("offline must fall back to stale disk data: %v", err)
	}
	// totally unavailable (no cache, offline): error distinguishes 查询失败 from 未收录
	empty := NewModelsDev(ModelsDevOptions{FetchURL: server.URL, CachePath: filepath.Join(dir, "none.json"), Now: clock.Now})
	if _, err := empty.LookupModelInfo(context.Background(), "gpt-test"); err == nil {
		t.Fatal("unavailable catalog must error")
	}
	// not-in-catalog: found=false
	if _, err := modelsDev.LookupModelInfo(context.Background(), "unknown-model"); err != nil {
		t.Fatal(err)
	}
}

type fakeClock struct{ now time.Time }

func (c *fakeClock) Now() time.Time { return c.now }

// --- remote model list against httptest ---

func TestFetchRemoteModelListShapes(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" {
			http.NotFound(w, r)
			return
		}
		switch r.Header.Get("x-test-shape") {
		case "openai":
			_ = json.NewEncoder(w).Encode(map[string]any{"data": []any{map[string]any{"id": "m1"}, map[string]any{"id": "m2"}, map[string]any{"id": "m1"}}})
		case "gemini":
			_ = json.NewEncoder(w).Encode(map[string]any{"models": []any{map[string]any{"name": "models/gem-pro"}, map[string]any{"name": "models/gem-flash"}}})
		case "bare":
			_ = json.NewEncoder(w).Encode([]any{"bare-model", map[string]any{"id": "obj-model"}})
		default:
			http.Error(w, "teapot", http.StatusTeapot)
		}
	}))
	defer server.Close()
	base := server.URL + "/v1"
	anthropicType := providerconfig.APIAnthropicMessages
	openaiType := providerconfig.APIOpenAIChatCompletions

	// openai shape, bearer auth; the shape marker rides the configured headers
	list, err := FetchRemoteModelList(context.Background(), nil, &providerconfig.ProviderConfig{
		Access: &providerconfig.ProviderAccess{APIKey: &providerconfig.ApiKeyAccessConfig{Type: providerconfig.AccessAPIKey, APIKey: ptrStr("sk-test-remote")}},
		API:    &providerconfig.ProviderApiConfig{Type: &openaiType, BaseURL: &base, Headers: map[string]string{"x-test-shape": "openai"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(list.IDs) != 2 || list.IDs[0] != "m1" || list.IDs[1] != "m2" {
		t.Fatalf("openai shape dedup failed: %v", list.IDs)
	}
	// gemini shape, anthropic auth headers
	list, err = FetchRemoteModelList(context.Background(), nil, &providerconfig.ProviderConfig{
		Access: &providerconfig.ProviderAccess{APIKey: &providerconfig.ApiKeyAccessConfig{Type: providerconfig.AccessAPIKey, APIKey: ptrStr("sk-test-remote")}},
		API:    &providerconfig.ProviderApiConfig{Type: &anthropicType, BaseURL: &base, Headers: map[string]string{"x-test-shape": "gemini"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(list.IDs) != 2 || list.IDs[0] != "gem-pro" {
		t.Fatalf("gemini prefix strip failed: %v", list.IDs)
	}
	// bare array shape
	list, err = FetchRemoteModelList(context.Background(), nil, &providerconfig.ProviderConfig{
		API: &providerconfig.ProviderApiConfig{Type: &openaiType, BaseURL: &base, Headers: map[string]string{"x-test-shape": "bare"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(list.IDs) != 2 || list.IDs[0] != "bare-model" || list.IDs[1] != "obj-model" {
		t.Fatalf("bare shape failed: %v", list.IDs)
	}
	// HTTP error: no retry, exact message
	_, err = FetchRemoteModelList(context.Background(), nil, &providerconfig.ProviderConfig{
		API: &providerconfig.ProviderApiConfig{Type: &openaiType, BaseURL: &base},
	})
	if err == nil || !strings.Contains(err.Error(), "Model list request failed: HTTP 418") {
		t.Fatalf("http status error must surface, got %v", err)
	}
	// missing base URL
	if _, err := FetchRemoteModelList(context.Background(), nil, &providerconfig.ProviderConfig{}); err == nil || !strings.Contains(err.Error(), "base URL") {
		t.Fatalf("missing base url must fail, got %v", err)
	}
}

// BuildRemoteModelListRequest header behavior (user headers not duplicated).
func TestBuildRemoteModelListRequestHeaders(t *testing.T) {
	anthropic := providerconfig.APIAnthropicMessages
	openai := providerconfig.APIOpenAIChatCompletions
	// user-set authorization wins; no duplicate bearer
	url, headers, err := BuildRemoteModelListRequest(&providerconfig.ProviderConfig{
		Access: &providerconfig.ProviderAccess{APIKey: &providerconfig.ApiKeyAccessConfig{Type: providerconfig.AccessAPIKey, APIKey: ptrStr("sk-test-headers")}},
		API:    &providerconfig.ProviderApiConfig{Type: &openai, BaseURL: ptrStr("https://x.example/v1/"), Headers: map[string]string{"Authorization": "custom"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if url != "https://x.example/v1/models" {
		t.Fatalf("trailing slash must be trimmed: %q", url)
	}
	if headers.Get("authorization") != "custom" {
		t.Fatal("user Authorization must not be replaced")
	}
	// anthropic defaults
	_, headers, err = BuildRemoteModelListRequest(&providerconfig.ProviderConfig{
		Access: &providerconfig.ProviderAccess{APIKey: &providerconfig.ApiKeyAccessConfig{Type: providerconfig.AccessAPIKey, APIKey: ptrStr("sk-test-headers")}},
		API:    &providerconfig.ProviderApiConfig{Type: &anthropic, BaseURL: ptrStr("https://x.example")},
	})
	if err != nil {
		t.Fatal(err)
	}
	if headers.Get("x-api-key") != "sk-test-headers" || headers.Get("anthropic-version") != "2023-06-01" {
		t.Fatal("anthropic default headers missing")
	}
	// zhipu-account access contributes no key
	_, headers, err = BuildRemoteModelListRequest(&providerconfig.ProviderConfig{
		Access: &providerconfig.ProviderAccess{ZhipuAccount: &providerconfig.ZhipuAccountAccessConfig{Type: providerconfig.AccessZhipuAccount}},
		API:    &providerconfig.ProviderApiConfig{Type: &anthropic, BaseURL: ptrStr("https://x.example")},
	})
	if err != nil {
		t.Fatal(err)
	}
	if headers.Get("x-api-key") != "" {
		t.Fatal("zhipu-account must not produce an api key header")
	}
}

// --- connectivity + secret redaction ---

func TestTestModelConnectivityGate(t *testing.T) {
	runtime := newTestRuntime(t)
	defer runtime.Dispose()
	ctx := context.Background()
	var seen []ConnectivityTestInput
	runtime.ProviderSettings.testConnectivity = func(ctx context.Context, input ConnectivityTestInput) error {
		seen = append(seen, input)
		return nil
	}
	input := struct {
		WorkspacePath     string `json:"workspacePath"`
		WorkspaceIdentity string `json:"workspaceIdentity,omitempty"`
		ProviderID        string `json:"providerId"`
		ModelID           string `json:"modelId"`
	}{ProviderID: "builtin:openai", ModelID: "builtin-model", WorkspacePath: "/tmp/w"}
	result, err := runtime.ProviderSettings.TestModelConnectivity(ctx, &input)
	if err != nil {
		t.Fatal(err)
	}
	if result["success"] != true {
		t.Fatalf("registered model must pass, got %v", result)
	}
	if len(seen) != 1 || seen[0].ModelID != "builtin-model" {
		t.Fatal("connectivity tester must receive the selection")
	}
	// unknown model → model-unavailable, no call
	input.ModelID = "missing"
	result, err = runtime.ProviderSettings.TestModelConnectivity(ctx, &input)
	if err != nil {
		t.Fatal(err)
	}
	if result["success"] == true {
		t.Fatal("missing model must not pass")
	}
	errObj := resultMap(t, result["error"])
	if errObj["code"] != "model-unavailable" || errObj["message"] != "This model is currently unavailable for connectivity testing." {
		t.Fatalf("error contract wrong: %v", errObj)
	}
	// no tester wired → the exact Chinese error
	other := newTestRuntime(t)
	defer other.Dispose()
	_, err = other.ProviderSettings.TestModelConnectivity(ctx, &struct {
		WorkspacePath     string `json:"workspacePath"`
		WorkspaceIdentity string `json:"workspaceIdentity,omitempty"`
		ProviderID        string `json:"providerId"`
		ModelID           string `json:"modelId"`
	}{ProviderID: "builtin:openai", ModelID: "builtin-model"})
	if err == nil || err.Error() != "当前 Environment 未装配模型连通性测试能力" {
		t.Fatalf("unwired tester must error exactly, got %v", err)
	}
}

// TestSecretRedactionInLogs: the personal repo and settings service never log
// API keys — a capture of everything they write to the sink must not contain
// the fake key.
func TestSecretRedactionSecurity(t *testing.T) {
	var sink bytes.Buffer
	logger := logx.NewServiceLogger("model-provider", &logx.Options{PID: -1, Sink: &sink, IsDebugEnabled: func() bool { return true }})
	dir := t.TempDir()
	path := filepath.Join(dir, "provider_config.json")
	repo, _ := NewPersonalRepo(PersonalRepoOptions{FilePath: path})
	// The repo has no logger API of its own; the redaction contract is that the
	// SERVICE layer logs lifecycle events (not config contents) through the
	// service logger. Exercise the paths that hold the key:
	_, err := repo.Update(context.Background(), func(current *providerconfig.LayerSnapshot) (*providerconfig.LayerUpdate, error) {
		providers, _ := providerconfig.NewProviderConfigMap([]providerconfig.ProviderConfigRule{{
			ProviderID: "p1",
			Config: &providerconfig.ProviderConfig{
				Group:  ptrStr(providerconfig.GroupStandardPersonal),
				Access: &providerconfig.ProviderAccess{APIKey: &providerconfig.ApiKeyAccessConfig{Type: providerconfig.AccessAPIKey, APIKey: ptrStr("sk-test-redaction-secret")}},
				API:    &providerconfig.ProviderApiConfig{Type: ptrStr(providerconfig.APIOpenAIChatCompletions), BaseURL: ptrStr("https://x.example")},
			},
		}})
		return &providerconfig.LayerUpdate{Providers: providers, Models: providerconfig.EmptyModelConfigRules()}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	// a lifecycle line the service would emit
	logger.Info("", "personal provider config updated", "path", path)
	logger.Info("", "registry refreshed", "reason", "settings:save-provider")
	out := sink.String()
	if strings.Contains(out, "sk-test-redaction-secret") {
		t.Fatalf("secret leaked into logs:\n%s", out)
	}
	// and the file content itself is not logged anywhere by the package
	logger.Debug("", "debug diagnostics", "revision", "abc123")
	if strings.Contains(sink.String(), "sk-test-redaction-secret") {
		t.Fatal("secret leaked into debug logs")
	}
}

// --- legacy importers with synthetic real-shaped fixtures ---

func TestImportLegacyPersonalProviderConfigFixtures(t *testing.T) {
	enabled := true
	custom := ptrStr("custom")
	fixtures := []LegacyModelProviderConfig{
		{
			// builtin:bigmodel → becomes the bigmodel template overlay with the key
			ID: "builtin:bigmodel", APIKey: "sk-test-legacy-bigmodel", Source: ptrStr("custom"),
		},
		{
			// builtin:zai with EMPTY key → dropped entirely
			ID: "builtin:zai", APIKey: "  ", Source: ptrStr("custom"),
		},
		{
			// builtin other → never migrates
			ID: "builtin:other", APIKey: "k", Source: ptrStr("custom"),
		},
		{
			// account:* → never migrates
			ID: "account:zai-team", APIKey: "k", Source: ptrStr("custom"),
		},
		{
			// catalog source → dropped (not user intent)
			ID: "catalog-provider", Source: ptrStr("catalog"),
		},
		{
			// a real custom provider with mixed model entries
			ID: "my-gateway", Name: "My Gateway", APIKey: "sk-test-legacy-custom",
			APIKeyURL: "https://portal.example/keys", Enabled: &enabled, Source: custom,
			APIFormat: ptrStr("anthropic"),
			Headers:   map[string]string{"X-Org": "org-1"},
			Endpoints: struct {
				BaseURL string `json:"baseURL"`
			}{BaseURL: "https://gw.example/v1/"},
			Models: []LegacyModelEntry{
				{Raw: "plain-string-model"},
				{Config: &LegacyModelConfig{ID: "obj-model", ContextWindow: ptrInt(32000)}},
				{Config: &LegacyModelConfig{ID: "deleted-model", Deleted: ptrBool(true), ContextWindow: ptrInt(1)}},
				{Config: &LegacyModelConfig{ID: "obj-model"}}, // duplicate id
				{Raw: "  "}, // blank
			},
		},
	}
	update := ImportLegacyPersonalProviderConfig(fixtures)

	// template overlay for bigmodel
	rule := update.Providers.GetRule("bigmodel")
	if rule == nil || rule.TemplateID == nil || *rule.TemplateID != "bigmodel" {
		t.Fatal("builtin:bigmodel must map to the bigmodel template rule")
	}
	if rule.Config.Access == nil || rule.Config.Access.APIKey.APIKey == nil || *rule.Config.Access.APIKey.APIKey != "sk-test-legacy-bigmodel" {
		t.Fatal("bigmodel template must carry the user key")
	}
	// the custom provider survived with everything intact
	rule = update.Providers.GetRule("my-gateway")
	if rule == nil {
		t.Fatal("custom provider must migrate")
	}
	if rule.Enabled == nil || !*rule.Enabled {
		t.Fatal("enabled must be preserved at the rule level")
	}
	if rule.ProviderName == nil || *rule.ProviderName != "My Gateway" {
		t.Fatal("name must be preserved")
	}
	config := rule.Config
	if config.Access == nil || config.Access.APIKey.APIKey == nil || *config.Access.APIKey.APIKey != "sk-test-legacy-custom" {
		t.Fatal("api key must migrate")
	}
	if config.Access.APIKey.APIKeyManagementURL == nil || *config.Access.APIKey.APIKeyManagementURL != "https://portal.example/keys" {
		t.Fatal("key management url must migrate")
	}
	if config.API.Type == nil || *config.API.Type != providerconfig.APIAnthropicMessages {
		t.Fatalf("anthropic apiFormat must map, got %v", config.API.Type)
	}
	if config.API.BaseURL == nil || *config.API.BaseURL != "https://gw.example/v1" {
		t.Fatalf("trailing slash trimmed, got %v", config.API.BaseURL)
	}
	if len(config.PersonalModelIDs) != 2 || config.PersonalModelIDs[0] != "plain-string-model" || config.PersonalModelIDs[1] != "obj-model" {
		t.Fatalf("membership: deleted dropped, dup deduped, blank dropped: %v", config.PersonalModelIDs)
	}
	if len(config.ModelOrder) != 2 {
		t.Fatal("model order must match membership")
	}
	// context window only for the object model that had one
	exact := update.Models.GetExact("my-gateway", "obj-model")
	if exact == nil || exact.Properties == nil || exact.Properties.ContextWindow == nil || *exact.Properties.ContextWindow != 32000 {
		t.Fatal("object model context window must migrate")
	}
	if update.Models.GetExact("my-gateway", "plain-string-model") != nil {
		t.Fatal("string model without context window must not create an exact rule")
	}
	// nothing else migrated
	if update.Providers.Len() != 2 {
		t.Fatalf("exactly two rules expected, got %d", update.Providers.Len())
	}
}

func TestReadLegacyNexConfigProvidersFixtures(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.json")
	fixture := `{
		"$schema": "https://nex.example/schema.json",
		"provider": {
			"my-gateway": {
				"name": "My Gateway",
				"options": {"baseURL": "https://gw.example/v1/", "apiKey": "sk-test-nex-config"},
				"headers": {"X-Org": "org-1"},
				"apiKeyUrl": "https://portal.example/keys",
				"enabled": true,
				"models": {
					"gpt-x": {"id": "gpt-x", "limit": {"context": 128000, "output": 16384}},
					"old-model": {"id": "old-model", "deleted": true}
				}
			},
			"builtin:zai": {"options": {"apiKey": "sk-test-nex-zai"}},
			"builtin:zapi": {"options": {"apiKey": "z"}},
			"default-preset": {"name": "Preset"},
			"anthropic-gw": {
				"npm": "@ai-sdk/anthropic",
				"options": {"baseURL": "https://a.example"},
				"models": {"claude-x": {"id": "claude-x", "contextWindow": 200000}}
			}
		}
	}`
	if err := os.WriteFile(configPath, []byte(fixture), 0o600); err != nil {
		t.Fatal(err)
	}
	providers, err := ReadLegacyNexConfigProviders(configPath)
	if err != nil {
		t.Fatal(err)
	}
	byID := map[string]LegacyModelProviderConfig{}
	for _, p := range providers {
		byID[p.ID] = p
	}
	if _, ok := byID["builtin:zai"]; ok {
		t.Fatal("builtin:* must never leak into the personal registry")
	}
	if _, ok := byID["builtin:zapi"]; ok {
		t.Fatal("retired ZAPI must be dropped")
	}
	if _, ok := byID["default-preset"]; ok {
		t.Fatal("default-* preset with empty key must be dropped")
	}
	gateway, ok := byID["my-gateway"]
	if !ok {
		t.Fatalf("custom gateway must migrate, got %v", keysOf(byID))
	}
	if gateway.APIKey != "sk-test-nex-config" {
		t.Fatal("options.apiKey must be read")
	}
	if gateway.Endpoints.BaseURL != "https://gw.example/v1" {
		t.Fatalf("options.baseURL must be the runtime base (trailing slash trimmed), got %q", gateway.Endpoints.BaseURL)
	}
	if gateway.Enabled == nil || !*gateway.Enabled {
		t.Fatal("enabled must survive")
	}
	if len(gateway.Models) != 1 {
		t.Fatalf("deleted model must be filtered for read, got %d", len(gateway.Models))
	}
	if gateway.Models[0].Config == nil || gateway.Models[0].Config.ID != "gpt-x" {
		t.Fatal("surviving model wrong")
	}
	anthropic, ok := byID["anthropic-gw"]
	if !ok {
		t.Fatal("npm anthropic provider must migrate")
	}
	if anthropic.APIFormat == nil || *anthropic.APIFormat != providerconfig.APIAnthropicMessages {
		t.Fatalf("npm-derived kind must produce anthropic apiFormat, got %v", anthropic.APIFormat)
	}
	if anthropic.Models[0].Config.ContextWindow == nil || *anthropic.Models[0].Config.ContextWindow != 200000 {
		t.Fatal("top-level contextWindow must be read")
	}
	// absent file → nil, nil
	if providers, err := ReadLegacyNexConfigProviders(filepath.Join(dir, "missing.json")); providers != nil || err != nil {
		t.Fatal("absent file must be nil,nil")
	}
	// corrupt file → loud parse error mentioning the stage (not contents)
	if err := os.WriteFile(configPath, []byte(`{"provider": {`), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err = ReadLegacyNexConfigProviders(configPath)
	if err == nil || !strings.Contains(err.Error(), "stage=json") {
		t.Fatalf("corrupt file must fail with stage=json, got %v", err)
	}
	if strings.Contains(err.Error(), "sk-test") {
		t.Fatal("parse error must not echo file contents (secrets)")
	}
}

func keysOf(m map[string]LegacyModelProviderConfig) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// --- legacy import wiring: read → import → repository write ---

func TestLegacyImportThroughRepository(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.json")
	if err := os.WriteFile(configPath, []byte(`{
		"provider": {
			"my-gateway": {
				"name": "My Gateway",
				"options": {"baseURL": "https://gw.example/v1", "apiKey": "sk-test-import-chain"},
				"models": {"gpt-x": {"id": "gpt-x", "limit": {"context": 64000}}}
			}
		}
	}`), 0o600); err != nil {
		t.Fatal(err)
	}
	personalPath := filepath.Join(dir, "provider_config.json")
	repo, err := NewPersonalRepo(PersonalRepoOptions{
		FilePath: personalPath,
		ImportLegacy: func(ctx context.Context) (*providerconfig.LayerUpdate, error) {
			providers, err := ReadLegacyNexConfigProviders(configPath)
			if err != nil {
				return nil, err
			}
			return ImportLegacyPersonalProviderConfig(providers), nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := repo.Read(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Providers.Len() != 1 {
		t.Fatalf("legacy import must write one provider, got %d", snapshot.Providers.Len())
	}
	rule := snapshot.Providers.GetRule("my-gateway")
	if rule == nil || rule.Config.Access == nil || rule.Config.Access.APIKey.APIKey == nil ||
		*rule.Config.Access.APIKey.APIKey != "sk-test-import-chain" {
		t.Fatal("import chain must preserve the key")
	}
	if exact := snapshot.Models.GetExact("my-gateway", "gpt-x"); exact == nil || exact.Properties.ContextWindow == nil || *exact.Properties.ContextWindow != 64000 {
		t.Fatal("import chain must preserve the context window")
	}
	// the file now exists on disk with schemaVersion 1
	data, _ := os.ReadFile(personalPath)
	var file map[string]any
	if err := json.Unmarshal(data, &file); err != nil {
		t.Fatal(err)
	}
	if file["schemaVersion"].(float64) != 1 {
		t.Fatal("imported file must be schemaVersion 1")
	}
}

// --- builtin source materialization (the embedded artifact) ---

func TestMaterializeBundledBuiltinProviderConfig(t *testing.T) {
	// the embedded artifact decodes
	release, err := providerconfig.DecodeNexBuiltinRelease([]byte(BundledNexBuiltinProviderConfig))
	if err != nil {
		t.Fatalf("embedded bundled config must decode: %v", err)
	}
	if release.Revision <= 0 {
		t.Fatal("revision must be positive")
	}
	if release.Config.Providers.Len() == 0 && release.Config.ModelConfigRules.Len() == 0 {
		t.Fatal("bundled config carries no facts")
	}
	// materialize writes the canonical copy once
	dir := t.TempDir()
	path, err := MaterializeBuiltinProviderConfig(context.Background(), dir, BundledNexBuiltinProviderConfig)
	if err != nil {
		t.Fatal(err)
	}
	if path != filepath.Join(dir, "runtime", "provider", "bundled", "nex-builtin.json") {
		t.Fatalf("materialized path wrong: %q", path)
	}
	first, _ := os.ReadFile(path)
	// second materialization is a no-op (content identical)
	if _, err := MaterializeBuiltinProviderConfig(context.Background(), dir, BundledNexBuiltinProviderConfig); err != nil {
		t.Fatal(err)
	}
	second, _ := os.ReadFile(path)
	if !bytes.Equal(first, second) {
		t.Fatal("re-materialization must be byte-stable")
	}
}

// TestSelectReleaseCandidatePorts: bundled wins same-revision conflicts, the
// higher revision wins otherwise.
func TestSelectReleaseCandidatePorts(t *testing.T) {
	bundled := &providerconfig.NexBuiltinRelease{SchemaVersion: 1, Revision: 5}
	sameRevDifferent := &providerconfig.NexBuiltinRelease{SchemaVersion: 1, Revision: 5}
	selected, err := providerconfig.SelectReleaseCandidate(bundled, sameRevDifferent, nil, nil)
	if err != nil || selected != bundled {
		t.Fatal("same-revision conflict resolves to bundled")
	}
	newer := &providerconfig.NexBuiltinRelease{SchemaVersion: 1, Revision: 6}
	if selected, _ := providerconfig.SelectReleaseCandidate(bundled, newer, nil, nil); selected != newer {
		t.Fatal("higher revision wins")
	}
	older := &providerconfig.NexBuiltinRelease{SchemaVersion: 1, Revision: 4}
	if selected, _ := providerconfig.SelectReleaseCandidate(bundled, older, nil, nil); selected != bundled {
		t.Fatal("bundled wins over older active")
	}
	if _, err := providerconfig.SelectReleaseCandidate(nil, nil, errors.New("e1"), errors.New("e2")); err == nil {
		t.Fatal("no candidates is an aggregate error")
	}
}

var _ = fmt.Sprintf

// resultMap coerces a nested rpcResult/map into map[string]any.
func resultMap(t *testing.T, v any) map[string]any {
	t.Helper()
	if m, ok := v.(map[string]any); ok {
		return m
	}
	if m, ok := v.(rpcResult); ok {
		out := map[string]any{}
		for k, val := range m {
			out[k] = val
		}
		return out
	}
	t.Fatalf("not a map: %v (%T)", v, v)
	return nil
}

// stringSliceValue coerces []string/[]any out of a decoded value.
func stringSliceValue(t *testing.T, v any) []string {
	t.Helper()
	switch list := v.(type) {
	case []string:
		return list
	case []any:
		out := make([]string, 0, len(list))
		for _, item := range list {
			out = append(out, item.(string))
		}
		return out
	}
	t.Fatalf("not a string list: %v (%T)", v, v)
	return nil
}

// numberValue coerces int64/float64/nil out of a JSON-decoded any.
func numberValue(t *testing.T, v any) float64 {
	t.Helper()
	switch n := v.(type) {
	case int64:
		return float64(n)
	case float64:
		return n
	}
	t.Fatalf("not a number: %v (%T)", v, v)
	return 0
}
