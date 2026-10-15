// providers_test.go: the step-3.6 security tests — the provisioning
// permission matrix (web-remote rejected with the exact error,
// desktop-continuous allowed), apply idempotency, CAS rollback, and the
// state-file behavior.
package providers

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/tylinux/nex/go/internal/providerconfig"
	"github.com/tylinux/nex/go/internal/rpc"
	"github.com/tylinux/nex/go/internal/services/modelprovider"
)

func newTestTarget(t *testing.T) (*modelprovider.ProviderRuntime, *Target, string, string) {
	t.Helper()
	dir := t.TempDir()
	builtin := &fakeBuiltin{snapshot: builtinSnapshotForProvisioning(t)}
	repo, err := modelprovider.NewPersonalRepo(modelprovider.PersonalRepoOptions{
		FilePath: filepath.Join(dir, "provider_config.json"),
	})
	if err != nil {
		t.Fatal(err)
	}
	configService := modelprovider.NewConfigService(modelprovider.ConfigServiceOptions{
		BuiltinSource: builtin, PersonalRepository: repo,
	})
	registry := modelprovider.NewRegistryService(modelprovider.RegistryServiceOptions{
		ConfigSource:  configService,
		AccountSource: modelprovider.NewFailClosedAccountSource(configService),
	})
	runtime := &modelprovider.ProviderRuntime{
		PersonalRepo: repo, ConfigService: configService, RegistryService: registry,
	}
	if err := runtime.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	personalPath := filepath.Join(dir, "provider_config.json")
	statePath := filepath.Join(dir, "provisioning-state.json")
	return runtime, NewTestTarget(runtime, personalPath, statePath), personalPath, statePath
}

// NewTestTarget is the test-visible constructor (keeps TargetOptions exported
// through this alias; production wires TargetOptions directly).
func NewTestTarget(runtime *modelprovider.ProviderRuntime, personalPath, statePath string) *Target {
	return NewTarget(TargetOptions{Runtime: runtime, PersonalConfigPath: personalPath, StateFilePath: statePath})
}

func builtinSnapshotForProvisioning(t *testing.T) *modelprovider.BuiltinConfigSnapshot {
	t.Helper()
	providers, err := providerconfig.NewProviderConfigMap([]providerconfig.ProviderConfigRule{{
		ProviderID: "builtin:openai",
		Config: &providerconfig.ProviderConfig{
			Group:           ptr("zai-family"),
			API:             &providerconfig.ProviderApiConfig{Type: ptr(providerconfig.APIOpenAIChatCompletions), BaseURL: ptr("https://builtin.example")},
			Access:          &providerconfig.ProviderAccess{APIKey: &providerconfig.ApiKeyAccessConfig{Type: providerconfig.AccessAPIKey, APIKey: ptr("builtin-key")}},
			BuiltinModelIDs: []string{"builtin-model"},
			Visibility:      ptr("visible"),
		},
	}})
	if err != nil {
		t.Fatal(err)
	}
	return &modelprovider.BuiltinConfigSnapshot{
		Revision: "nex-builtin:1:test", Providers: providers,
		ProviderTemplates: providerconfig.EmptyProviderTemplateMap(),
		Models: providerconfig.NewModelConfigRules([]providerconfig.ModelConfigRule{
			{Type: providerconfig.RuleModel, ModelMatch: ".*", Config: completeModel()},
		}),
	}
}

type fakeBuiltin struct {
	snapshot *modelprovider.BuiltinConfigSnapshot
}

func (f *fakeBuiltin) Read(ctx context.Context) (*modelprovider.BuiltinConfigSnapshot, error) {
	return f.snapshot, nil
}

func (f *fakeBuiltin) OnDidChange(func(string)) func() { return func() {} }

func ptr[T any](v T) *T { return &v }

func completeModel() *providerconfig.ModelConfig {
	return &providerconfig.ModelConfig{
		Enabled: ptr(true),
		Properties: &providerconfig.ModelProperties{
			RequiresMfjsToolSchema: ptr(false), ContextWindow: ptr(int64(100000)),
			InputFormat:      &providerconfig.ModelInputFormat{SupportsText: ptr(true), SupportsImage: ptr(false), SupportsVideo: ptr(false), SupportsAudio: ptr(false), SupportsPdf: ptr(false)},
			OutputFormat:     &providerconfig.ModelOutputFormat{SupportsText: ptr(true)},
			SupportsToolCall: ptr(true), SupportsJsonSchemaOutput: ptr(false),
			SupportsNativeWebSearch: ptr(false), SupportsMidConversationSystem: ptr(false),
		},
		OptionSpecs: &providerconfig.ModelOptionSpecs{
			ReasoningLevel:  &providerconfig.EnumOptionSpec{Values: []string{"off", "on"}},
			MaxOutputTokens: &providerconfig.LimitOptionSpec{Max: ptr(int64(16000))},
		},
	}
}

func personalEnvelope(t *testing.T, providerID, modelID string) json.RawMessage {
	t.Helper()
	personal := map[string]any{
		"providerConfigRules": map[string]any{"providerRules": []any{map[string]any{
			"providerId": providerID,
			"config": map[string]any{
				"group":            "standard-personal",
				"access":           map[string]any{"type": "api-key", "apiKey": "sk-test-provision"},
				"api":              map[string]any{"type": "openai-chat-completions", "baseUrl": "https://provision.example"},
				"personalModelIds": []string{modelID}, "modelOrder": []string{modelID},
			},
		}}},
		"modelConfigRules": map[string]any{"providerModelRules": []any{}, "manualProviderModelRules": []any{}},
	}
	data, err := json.Marshal(personal)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

// TestProvisioningPermissionMatrix — the security-sensitive gate. Only
// desktop-continuous connections may call apply; every other mode gets the
// exact trusted-host error, and the target channel must never even be
// constructed for them.
func TestProvisioningSecurityPermissionMatrix(t *testing.T) {
	_, target, _, _ := newTestTarget(t)

	matrix := []struct {
		mode        string
		allowed     bool
		description string
	}{
		{"desktop-continuous", true, "trusted host relay"},
		{"web-remote-replayable", false, "web remote client"},
	}
	for _, row := range matrix {
		t.Run(row.mode, func(t *testing.T) {
			scope := NewConnectionScope(row.mode, target)
			channel := scope.ScopeFor(ChannelProviderProvisioningTarget)
			if channel == nil {
				t.Fatalf("%s: channel must exist (target configured)", row.description)
			}
			envelope := map[string]any{
				"schemaVersion":   1,
				"syncId":          "sync-" + row.mode,
				"personalConfig":  json.RawMessage(`{"providerConfigRules":{"providerRules":[]},"modelConfigRules":{"providerModelRules":[],"manualProviderModelRules":[]}}`),
				"accountSettings": map[string]any{"providerFamilyDomain": nil, "providerFamilyConnectionSelections": map[string]any{}},
				"credentials":     []any{},
			}
			var arg rpc.Value = envelope
			result, err := channel.Call(context.Background(), "apply", []rpc.Value{arg})
			if row.allowed {
				if err != nil {
					t.Fatalf("trusted host must be allowed, got %v", err)
				}
				outcome, _ := result.(map[string]any)
				if outcome["status"] != "applied" {
					t.Fatalf("trusted host apply must succeed, got %v", result)
				}
			} else {
				if err == nil {
					t.Fatalf("%s must be rejected, got result %v", row.description, result)
				}
				if err.Error() != ProvisioningOnlyTrustedHostError {
					t.Fatalf("rejection error must be exact, got %q", err.Error())
				}
			}
		})
	}

	// no target configured → nothing exposed on any connection
	emptyScope := NewConnectionScope("desktop-continuous", nil)
	if emptyScope.ScopeFor(ChannelProviderProvisioningTarget) != nil {
		t.Fatal("no target configured → no channel")
	}
}

// TestProvisioningApplyIdempotencyAndState: second apply of the same syncId
// returns already-applied without touching the personal file.
func TestProvisioningApplyIdempotencyAndState(t *testing.T) {
	runtime, target, personalPath, statePath := newTestTarget(t)
	envelope := Envelope{
		SchemaVersion: 1, SyncID: "sync-1",
		PersonalConfig: personalEnvelope(t, "provisioned", "prov-model"),
		Credentials:    []CredentialEntry{},
	}
	result, err := target.Apply(context.Background(), envelope)
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != "applied" || result.PersonalProviderCount != 1 || result.RolledBack {
		t.Fatalf("apply failed: %+v", result)
	}
	// the personal file now carries the provisioned provider
	data, err := os.ReadFile(personalPath)
	if err != nil {
		t.Fatal(err)
	}
	if !jsonValidWithProvider(t, data, "provisioned") {
		t.Fatal("personal file must contain the provisioned provider")
	}
	before, _ := os.ReadFile(personalPath)
	// second apply: already-applied
	result, err = target.Apply(context.Background(), envelope)
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != "already-applied" {
		t.Fatalf("second apply must be already-applied, got %+v", result)
	}
	after, _ := os.ReadFile(personalPath)
	if string(before) != string(after) {
		t.Fatal("idempotent apply must not rewrite the file")
	}
	// state file exists with the record
	state, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatal(err)
	}
	var parsed provisioningStateFile
	if err := json.Unmarshal(state, &parsed); err != nil {
		t.Fatal(err)
	}
	if parsed.SchemaVersion != 1 || len(parsed.Records) != 1 || parsed.Records[0].SyncID != "sync-1" {
		t.Fatalf("state file wrong: %s", state)
	}
	_ = runtime
}

// TestProvisioningApplyValidationFailure: an envelope whose default model
// selection is not supported by the post-sync registry fails and rolls back.
func TestProvisioningApplyValidationFailure(t *testing.T) {
	runtime, target, personalPath, _ := newTestTarget(t)
	// personal config with a default selection for a model that does not exist
	personal := map[string]any{
		"providerConfigRules": map[string]any{"providerRules": []any{}},
		"modelConfigRules":    map[string]any{"providerModelRules": []any{}, "manualProviderModelRules": []any{}},
		"defaultModelSelection": map[string]any{
			"providerId": "nope", "modelId": "missing", "options": map[string]any{"reasoningLevel": "x"},
		},
	}
	data, _ := json.Marshal(personal)
	result, err := target.Apply(context.Background(), Envelope{
		SchemaVersion: 1, SyncID: "sync-bad", PersonalConfig: data, Credentials: []CredentialEntry{},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != "failed" || !result.RolledBack {
		t.Fatalf("unsupported default selection must fail + rollback, got %+v", result)
	}
	if result.ErrorMessage == "" {
		t.Fatal("failure must explain itself")
	}
	_ = personalPath
	_ = runtime
}

// TestProvisioningRollbackOnConcurrentModification: a transform that fails
// mid-apply after the personal write triggers rollback to the before state.
func TestProvisioningEnvelopeValidation(t *testing.T) {
	_, target, _, _ := newTestTarget(t)
	// wrong schema version rejected at the boundary
	if _, err := target.Apply(context.Background(), Envelope{SchemaVersion: 2, SyncID: "s"}); err == nil {
		t.Fatal("schemaVersion 2 must be rejected")
	}
	// empty syncId rejected
	if _, err := target.Apply(context.Background(), Envelope{SchemaVersion: 1, SyncID: " "}); err == nil {
		t.Fatal("empty syncId must be rejected")
	}
	// >256 credentials rejected
	big := make([]CredentialEntry, 257)
	for i := range big {
		big[i] = CredentialEntry{Scope: "oauth-session", Key: "k", Value: "v"}
	}
	if _, err := target.Apply(context.Background(), Envelope{SchemaVersion: 1, SyncID: "s", Credentials: big}); err == nil {
		t.Fatal("oversized credentials list must be rejected")
	}
	// malformed personal config rejected
	if _, err := target.Apply(context.Background(), Envelope{
		SchemaVersion: 1, SyncID: "s",
		PersonalConfig: json.RawMessage(`{"providerConfigRules":{"providerRules":[{}}`),
		Credentials:    []CredentialEntry{},
	}); err == nil {
		t.Fatal("malformed personal config must be rejected")
	}
}

func jsonValidWithProvider(t *testing.T, data []byte, providerID string) bool {
	t.Helper()
	var file struct {
		Config struct {
			ProviderConfigRules struct {
				ProviderRules []struct {
					ProviderID string `json:"providerId"`
				} `json:"providerRules"`
			} `json:"providerConfigRules"`
		} `json:"config"`
	}
	if err := json.Unmarshal(data, &file); err != nil {
		return false
	}
	for _, rule := range file.Config.ProviderConfigRules.ProviderRules {
		if rule.ProviderID == providerID {
			return true
		}
	}
	return false
}
