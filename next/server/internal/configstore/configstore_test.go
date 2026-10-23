package configstore

import (
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func testLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func testProvider(id string) Provider {
	return Provider{
		ID:      id,
		API:     APITypeOpenAIChatCompletions,
		BaseURL: "https://api.example.test/v1",
		Name:    id,
		Enabled: true,
		Models: []Model{{
			ID:              "test-model",
			Name:            "Test Model",
			Reasoning:       false,
			ReasoningLevels: []string{"off"},
			ContextWindow:   128000,
			MaxTokens:       8192,
			Input:           []string{"text"},
			Enabled:         true,
		}},
	}
}

func TestSecretBoxRoundTripAndWrongKey(t *testing.T) {
	dir := t.TempDir()
	box := NewSecretBox(dir)
	if err := box.Put("sec_a", "sk-secret-value"); err != nil {
		t.Fatalf("put: %v", err)
	}
	value, ok, err := box.Get("sec_a")
	if err != nil || !ok || value != "sk-secret-value" {
		t.Fatalf("get = %q, %v, %v", value, ok, err)
	}
	info, err := os.Stat(filepath.Join(dir, "keys", "config.key"))
	if err != nil {
		t.Fatalf("stat key: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("key mode = %o, want 600", info.Mode().Perm())
	}
	raw, err := os.ReadFile(filepath.Join(dir, "config", "secrets.enc"))
	if err != nil {
		t.Fatalf("read secrets: %v", err)
	}
	if strings.Contains(string(raw), "sk-secret-value") {
		t.Fatal("secrets.enc contains the plaintext")
	}

	// AAD is the secret id: the same ciphertext under another id must not open.
	other := NewSecretBox(dir)
	if err := other.Delete("sec_b"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	ciphertext, err := other.Seal("sec_a", "sk-secret-value")
	if err != nil {
		t.Fatalf("seal: %v", err)
	}
	if _, err := other.Open("sec_b", ciphertext); !errors.Is(err, ErrSecretIntegrity) {
		t.Fatalf("open under another id = %v, want integrity error", err)
	}

	// A different key file (new server data dir) must fail to open old ciphertext.
	otherDir := t.TempDir()
	wrong := NewSecretBox(otherDir)
	if _, err := wrong.Open("sec_a", ciphertext); !errors.Is(err, ErrSecretIntegrity) {
		t.Fatalf("open with another key = %v, want integrity error", err)
	}
	// Reload from disk with the same key still works.
	reloaded := NewSecretBox(dir)
	if value, ok, err := reloaded.Get("sec_a"); err != nil || !ok || value != "sk-secret-value" {
		t.Fatalf("reloaded get = %q, %v, %v", value, ok, err)
	}
}

func TestMergeLayersSemantics(t *testing.T) {
	global := Layer{
		Providers: map[string]*Provider{"a": {ID: "a", API: APITypeAnthropicMessages, BaseURL: "https://a.test", Enabled: true},
			"b": {ID: "b", API: APITypeAnthropicMessages, BaseURL: "https://b.test", Enabled: true}},
		Settings:      map[string]json.RawMessage{"defaultModel": json.RawMessage(`"m1"`)},
		ProviderOrder: []string{"b", "a"},
	}
	runner := Layer{
		Providers: map[string]*Provider{"a": {ID: "a", Enabled: false}, "b": nil},
		Settings:  map[string]json.RawMessage{"defaultModel": json.RawMessage(`null`)},
	}
	merged := MergeLayers(global, runner)
	if merged.Providers["a"] == nil || merged.Providers["a"].Enabled {
		t.Fatalf("toggle-only entry must flip enabled and keep the provider: %+v", merged.Providers["a"])
	}
	if merged.Providers["a"].BaseURL != "https://a.test" {
		t.Fatalf("toggle-only entry replaced the provider: %+v", merged.Providers["a"])
	}
	if _, ok := merged.Providers["b"]; ok {
		t.Fatal("null provider must delete")
	}
	if _, ok := merged.Settings["defaultModel"]; ok {
		t.Fatal("null setting must delete")
	}
	order := merged.ProviderList()
	if len(order) != 1 || order[0].ID != "a" {
		t.Fatalf("provider list = %+v", order)
	}
}

func TestValidateDocStrictShape(t *testing.T) {
	valid := EmptyDoc()
	provider := testProvider("p1")
	valid.Scopes.Global.Providers = map[string]*Provider{"p1": &provider}
	if issues := ValidateDoc(valid); len(issues) != 0 {
		t.Fatalf("valid doc issues: %+v", issues)
	}

	cases := []struct {
		name   string
		mutate func(*Provider)
		code   string
	}{
		{"empty reasoning levels", func(p *Provider) { p.Models[0].ReasoningLevels = nil }, "reasoning-levels-empty"},
		{"unknown reasoning level", func(p *Provider) { p.Models[0].ReasoningLevels = []string{"off", "ultra"} }, "reasoning-level-unsupported"},
		{"descending reasoning levels", func(p *Provider) { p.Models[0].ReasoningLevels = []string{"high", "off"} }, "reasoning-levels-order"},
		{"bad api type", func(p *Provider) { p.API = "sse" }, "api-unsupported"},
		{"bad base url", func(p *Provider) { p.BaseURL = "ftp://x" }, "base-url-invalid"},
		{"no context window", func(p *Provider) { p.Models[0].ContextWindow = 0 }, "context-window-invalid"},
		{"duplicate model", func(p *Provider) { p.Models = append(p.Models, p.Models[0]) }, "duplicate-model-id"},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			doc := EmptyDoc()
			broken := testProvider("p1")
			test.mutate(&broken)
			doc.Scopes.Global.Providers = map[string]*Provider{"p1": &broken}
			issues := ValidateDoc(doc)
			found := false
			for _, issue := range issues {
				if issue.Code == test.code {
					found = true
				}
			}
			if !found {
				t.Fatalf("issues %+v do not contain %s", issues, test.code)
			}
		})
	}
}

func TestStoreVersioningPersistenceAndRejection(t *testing.T) {
	dir := t.TempDir()
	store := NewStore(dir, testLogger())
	var changes []Change
	store.Subscribe(func(change Change) { changes = append(changes, change) })

	doc, err := store.UpsertProvider(testProvider("p1"))
	if err != nil {
		t.Fatalf("upsert: %v", err)
	}
	if doc.Version != 1 {
		t.Fatalf("version = %d, want 1", doc.Version)
	}
	if _, err := store.UpsertProvider(testProvider("p1")); err != nil {
		t.Fatalf("second upsert: %v", err)
	}
	if len(changes) != 2 || changes[1].Version != 2 || changes[1].Previous != 1 {
		t.Fatalf("changes = %+v", changes)
	}

	// An invalid write is rejected and leaves the stored doc at version 2.
	broken := testProvider("p2")
	broken.Models[0].ReasoningLevels = nil
	if _, err := store.UpsertProvider(broken); err == nil {
		t.Fatal("invalid provider accepted")
	}
	snapshot, err := store.Snapshot()
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	if snapshot.Version != 2 || snapshot.Scopes.Global.Providers["p2"] != nil {
		t.Fatalf("rejected write changed the doc: %+v", snapshot)
	}

	// A fresh store reads the same doc back.
	reloaded := NewStore(dir, testLogger())
	again, err := reloaded.Snapshot()
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if again.Version != 2 || again.Scopes.Global.Providers["p1"] == nil {
		t.Fatalf("reloaded doc = %+v", again)
	}
	if _, err := reloaded.DeleteProvider("p1"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := reloaded.DeleteProvider("p1"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("second delete = %v, want ErrNotFound", err)
	}
}

func TestProjectPushEntitlement(t *testing.T) {
	dir := t.TempDir()
	store := NewStore(dir, testLogger())
	enabled := testProvider("enabled")
	disabled := testProvider("disabled")
	disabled.Enabled = false
	disabled.APIKey = SecretID("disabled")
	enabled.APIKey = SecretID("enabled")
	for _, provider := range []Provider{enabled, disabled} {
		if _, err := store.UpsertProvider(provider); err != nil {
			t.Fatalf("upsert %s: %v", provider.ID, err)
		}
	}
	if err := store.Secrets().Put(SecretID("enabled"), "key-enabled"); err != nil {
		t.Fatal(err)
	}
	if err := store.Secrets().Put(SecretID("disabled"), "key-disabled"); err != nil {
		t.Fatal(err)
	}

	push, issues, err := store.ProjectPush("r1")
	if err != nil || len(issues) != 0 {
		t.Fatalf("project: %v %+v", err, issues)
	}
	if !push.Doc.Owned {
		t.Fatal("owned mode expected with an enabled provider")
	}
	if push.Secrets[SecretID("enabled")] != "key-enabled" {
		t.Fatalf("enabled secret missing: %+v", push.Secrets)
	}
	if _, ok := push.Secrets[SecretID("disabled")]; ok {
		t.Fatal("disabled provider's secret must not be entitled")
	}
	for _, provider := range push.Doc.Providers {
		if provider.APIKey != "" {
			t.Fatalf("push carries an inline key for %s", provider.ID)
		}
	}
	raw, _ := json.Marshal(push)
	var encoded map[string]json.RawMessage
	if err := json.Unmarshal(raw, &encoded); err != nil {
		t.Fatal(err)
	}
	withoutSecrets := map[string]json.RawMessage{}
	for key, value := range encoded {
		if key != "secrets" {
			withoutSecrets[key] = value
		}
	}
	rest, _ := json.Marshal(withoutSecrets)
	if strings.Contains(string(rest), "key-enabled") {
		t.Fatal("push JSON carries a secret value outside the secrets map")
	}

	// A runner-scope toggle disables the global provider for that runner only.
	if _, err := store.Update("runner-toggle", func(doc *ConfigDoc) error {
		doc.Scopes.Runner["r2"] = Layer{Providers: map[string]*Provider{"enabled": {ID: "enabled", Enabled: false}}}
		return nil
	}); err != nil {
		t.Fatalf("runner toggle: %v", err)
	}
	r2, _, err := store.ProjectPush("r2")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := r2.Secrets[SecretID("enabled")]; ok {
		t.Fatal("runner-scope disable must drop the secret for that runner")
	}
	if r2.Doc.Owned {
		t.Fatal("no enabled provider left for r2: fallback mode expected")
	}
}

func TestImportLayer(t *testing.T) {
	store := NewStore(t.TempDir(), testLogger())
	imported := Layer{Providers: map[string]*Provider{"local": func() *Provider {
		provider := testProvider("local")
		provider.APIKey = SecretID("local")
		return &provider
	}()}}
	doc, ids, err := store.ImportLayer("alpha", imported)
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	if len(ids) != 1 || ids[0] != "local" {
		t.Fatalf("imported ids = %v", ids)
	}
	if doc.Scopes.Runner["alpha"].Providers["local"] == nil {
		t.Fatalf("runner scope missing provider: %+v", doc.Scopes.Runner)
	}
	docs := doc.EffectiveForRunner("alpha")
	if len(docs.Providers) != 1 {
		t.Fatalf("effective providers = %+v", docs.ProviderList())
	}
}
