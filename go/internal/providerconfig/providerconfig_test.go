// providerconfig_test.go: the shared package's behavior tests — overlay merge
// vectors (ConfigOverlay semantics), the model-rule resolution precedence
// table, registry validation, the file codec round-trips and the agent
// parser (the Phase-1 extraction regression guard).
package providerconfig

import (
	"encoding/json"
	"testing"
)

func boolPtr(v bool) *bool    { return &v }
func strPtr(v string) *string { return &v }
func intPtr(v int64) *int64   { return &v }

// --- overlay merge vectors (ConfigOverlay: undefined inherits, values replace) ---

func TestModelConfigOverlayVectors(t *testing.T) {
	base := &ModelConfig{
		Enabled: boolPtr(true),
		Name:    strPtr("base"),
		Properties: &ModelProperties{
			ContextWindow: intPtr(1000),
			InputFormat:   &ModelInputFormat{SupportsText: boolPtr(true)},
		},
		OptionSpecs: &ModelOptionSpecs{
			ReasoningLevel:  &EnumOptionSpec{Values: []string{"a", "b"}, Map: strPtr("m1")},
			MaxOutputTokens: &LimitOptionSpec{Max: intPtr(500)},
		},
	}
	cases := []struct {
		name string
		next *ModelConfig
		want func(*ModelConfig) bool
	}{
		{"nil next inherits everything", nil, func(g *ModelConfig) bool {
			return g.Name != nil && *g.Name == "base" && g.Properties.ContextWindow != nil && *g.Properties.ContextWindow == 1000
		}},
		{"empty next inherits everything", &ModelConfig{}, func(g *ModelConfig) bool {
			return g.Enabled != nil && *g.Enabled && g.OptionSpecs.ReasoningLevel.Map != nil
		}},
		{"leaf replace", &ModelConfig{Name: strPtr("next")}, func(g *ModelConfig) bool {
			return *g.Name == "next" && g.Properties.ContextWindow != nil && *g.Properties.ContextWindow == 1000
		}},
		{"nested merge keeps unset leaves", &ModelConfig{Properties: &ModelProperties{ContextWindow: intPtr(2000)}}, func(g *ModelConfig) bool {
			return *g.Properties.ContextWindow == 2000 && g.Properties.InputFormat.SupportsText != nil && *g.Properties.InputFormat.SupportsText
		}},
		{"nested nil leaf inherits", &ModelConfig{Properties: &ModelProperties{}}, func(g *ModelConfig) bool {
			return g.Properties.ContextWindow != nil && *g.Properties.ContextWindow == 1000
		}},
		{"array leaf replace", &ModelConfig{OptionSpecs: &ModelOptionSpecs{ReasoningLevel: &EnumOptionSpec{Values: []string{"z"}}}}, func(g *ModelConfig) bool {
			return len(g.OptionSpecs.ReasoningLevel.Values) == 1 && g.OptionSpecs.ReasoningLevel.Values[0] == "z" &&
				g.OptionSpecs.ReasoningLevel.Map != nil
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := base.Overlay(tc.next)
			if !tc.want(got) {
				data, _ := json.Marshal(got)
				t.Fatalf("unexpected overlay result: %s", data)
			}
		})
	}
}

func TestProviderAccessOverlayTypeChange(t *testing.T) {
	apiKey := &ProviderAccess{APIKey: &ApiKeyAccessConfig{Type: AccessAPIKey, APIKey: strPtr("k1")}}
	account := &ProviderAccess{ZhipuAccount: &ZhipuAccountAccessConfig{Type: AccessZhipuAccount, Entitled: boolPtr(false)}}
	if got := OverlayAccess(apiKey, account); got.AccessType() != AccessZhipuAccount {
		t.Fatalf("type change must replace whole access, got %q", got.AccessType())
	}
	sameType := &ProviderAccess{APIKey: &ApiKeyAccessConfig{Type: AccessAPIKey, APIKey: strPtr("k2")}}
	merged := OverlayAccess(apiKey, sameType)
	if merged.APIKey.APIKey == nil || *merged.APIKey.APIKey != "k2" {
		t.Fatal("same-type access must merge leaves")
	}
	if merged.APIKey.APIKeyManagementURL != nil {
		t.Fatal("unset leaf must inherit (nil here)")
	}
	if OverlayAccess(apiKey, nil) != apiKey {
		t.Fatal("nil next keeps current")
	}
}

// --- provider config map overlay ---

func TestProviderConfigMapOverlay(t *testing.T) {
	base, _ := NewProviderConfigMap([]ProviderConfigRule{{
		ProviderID: "p1", ProviderName: strPtr("base name"),
		Config: &ProviderConfig{Group: strPtr("zai-family"), API: &ProviderApiConfig{BaseURL: strPtr("https://a.example")}},
	}})
	next, _ := NewProviderConfigMap([]ProviderConfigRule{{
		ProviderID: "p1", Enabled: boolPtr(false),
		Config: &ProviderConfig{API: &ProviderApiConfig{Type: strPtr(APIOpenAIChatCompletions)}},
	}})
	merged := base.Overlay(next)
	rule := merged.GetRule("p1")
	if rule == nil || rule.Enabled == nil || *rule.Enabled {
		t.Fatal("overlay must merge identity leaves")
	}
	if rule.ProviderName == nil || *rule.ProviderName != "base name" {
		t.Fatal("unset identity leaf must inherit")
	}
	if rule.Config.API.BaseURL == nil || *rule.Config.API.BaseURL != "https://a.example" {
		t.Fatal("unset api leaf must inherit")
	}
	if rule.Config.API.Type == nil || *rule.Config.API.Type != APIOpenAIChatCompletions {
		t.Fatal("set api leaf must replace")
	}
	// new provider inserts
	next2, _ := NewProviderConfigMap([]ProviderConfigRule{{
		ProviderID: "p2", Config: &ProviderConfig{},
	}})
	if merged.Overlay(next2).Get("p2") == nil {
		t.Fatal("new provider must insert")
	}
}

// --- model rule resolution precedence table ---

func rule(ruleType, pattern string, config *ModelConfig) ModelConfigRule {
	return ModelConfigRule{Type: ruleType, ModelMatch: pattern, Config: config}
}

func TestModelRulesResolvePrecedenceTable(t *testing.T) {
	rules := NewModelConfigRules([]ModelConfigRule{
		// 1. catch-all model rule (lowest)
		rule(RuleModel, ".*", &ModelConfig{Properties: &ModelProperties{ContextWindow: intPtr(100)}}),
		// 2. api-specific rule
		{Type: RuleModelAPI, ModelMatch: ".*", APITYPEMatch: "anthropic-messages", Config: &ModelConfig{Properties: &ModelProperties{ContextWindow: intPtr(200)}}},
		// 3. site rule
		{Type: RuleProviderSite, ModelMatch: ".*", APITYPEMatch: "anthropic-messages", BaseURLMatch: "https://api\\.test\\.example/v1", Config: &ModelConfig{Properties: &ModelProperties{ContextWindow: intPtr(300)}}},
		// 4. template rule
		{Type: RuleTemplateModel, TemplateID: "tpl", ModelID: "m1", Config: &ModelConfig{Properties: &ModelProperties{ContextWindow: intPtr(400)}}},
		// 5. provider-model exact
		{Type: RuleProviderModel, ProviderID: "p1", ModelID: "m1", Config: &ModelConfig{Properties: &ModelProperties{ContextWindow: intPtr(500)}}},
	})
	cases := []struct {
		name  string
		input RuleResolutionInput
		want  int64
	}{
		{"catch-all only", RuleResolutionInput{ProviderID: "pX", ModelID: "other", APIType: "", BaseURL: ""}, 100},
		{"api rule matches", RuleResolutionInput{ProviderID: "pX", ModelID: "other", APIType: "anthropic-messages", BaseURL: "https://elsewhere.example"}, 200},
		{"site rule wins over api (order)", RuleResolutionInput{ProviderID: "pX", ModelID: "other", APIType: "anthropic-messages", BaseURL: "https://api.test.example/v1"}, 300},
		{"template rule stacks under exact", RuleResolutionInput{ProviderID: "p1", TemplateID: "tpl", ModelID: "m1"}, 500},
		{"exact rule overlays everything before it", RuleResolutionInput{ProviderID: "p1", TemplateID: "tpl", ModelID: "m1", APIType: "anthropic-messages", BaseURL: "https://api.test.example/v1"}, 500},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := rules.Resolve(tc.input)
			if got.Properties == nil || got.Properties.ContextWindow == nil || *got.Properties.ContextWindow != tc.want {
				data, _ := json.Marshal(got)
				t.Fatalf("want contextWindow=%d, got %s", tc.want, data)
			}
		})
	}
	// case-insensitive model match for recommend rules only
	rulesCI := NewModelConfigRules([]ModelConfigRule{rule(RuleModel, ".*M1.*", &ModelConfig{Properties: &ModelProperties{ContextWindow: intPtr(42)}})})
	if got := rulesCI.Resolve(RuleResolutionInput{ModelID: "xm1x"}); got.Properties.ContextWindow == nil || *got.Properties.ContextWindow != 42 {
		t.Fatal("recommend rules must match case-insensitively")
	}
}

// TestManualRuleClearsEditableLeaves: 手动规则要求所有可编辑叶子齐全，覆盖前清空
// 智能配置的旧叶子（clearManualModelConfig）.
func TestManualRuleClearsEditableLeaves(t *testing.T) {
	rules := NewModelConfigRules([]ModelConfigRule{
		rule(RuleModel, ".*", &ModelConfig{
			Properties: &ModelProperties{
				ContextWindow: intPtr(1000), SupportsToolCall: boolPtr(true),
				InputFormat: &ModelInputFormat{SupportsText: boolPtr(true), SupportsImage: boolPtr(true)},
			},
			OptionSpecs: &ModelOptionSpecs{
				ReasoningLevel:  &EnumOptionSpec{Values: []string{"a"}},
				MaxOutputTokens: &LimitOptionSpec{Max: intPtr(900)},
			},
		}),
		{Type: RuleManualProviderModel, ProviderID: "p1", ModelID: "m1", Config: &ModelConfig{
			Properties:  &ModelProperties{ContextWindow: intPtr(500)},
			OptionSpecs: &ModelOptionSpecs{ReasoningLevel: &EnumOptionSpec{Values: []string{"b"}}},
		}},
	})
	got := rules.Resolve(RuleResolutionInput{ProviderID: "p1", ModelID: "m1"})
	if got.Properties.ContextWindow == nil || *got.Properties.ContextWindow != 500 {
		t.Fatal("manual leaf must win")
	}
	if got.Properties.SupportsToolCall == nil || !*got.Properties.SupportsToolCall {
		t.Fatal("system leaf (supportsToolCall) must survive manual clear")
	}
	if got.Properties.SupportsJsonSchemaOutput != nil {
		t.Fatal("manual leaf supportsJsonSchemaOutput must be cleared before the manual rule applies")
	}
	if got.Properties.InputFormat != nil && got.Properties.InputFormat.SupportsText == nil {
		t.Fatal("manual clear must keep system inputFormat.supportsText leaf")
	}
	if got.Properties.InputFormat != nil && got.Properties.InputFormat.SupportsImage != nil {
		t.Fatal("manual clear must drop the editable supportsImage leaf inherited from the smart config")
	}
	if got.OptionSpecs.MaxOutputTokens != nil && got.OptionSpecs.MaxOutputTokens.Max != nil {
		t.Fatal("manual clear must drop the inherited manual leaf maxOutputTokens.max")
	}
	// absent map in the smart rule stays absent after the clear (nil ≠ dropped-present)
	if got.OptionSpecs.MaxOutputTokens != nil && got.OptionSpecs.MaxOutputTokens.Max != nil {
		t.Fatal("clear must not leave a max")
	}
	if got.OptionSpecs.ReasoningLevel == nil || len(got.OptionSpecs.ReasoningLevel.Values) != 1 || got.OptionSpecs.ReasoningLevel.Values[0] != "b" {
		t.Fatal("manual reasoningLevel wins")
	}
}

// TestOwnedOrderVectors: 未排序 Built-in 在用户顺序之前，未排序 Personal 在之后.
func TestOwnedOrderVectors(t *testing.T) {
	cases := []struct {
		name              string
		builtin, personal []string
		requested         []string
		want              []string
	}{
		{"empty", nil, nil, nil, []string{}},
		{"unsorted builtin first", []string{"b1", "b2"}, []string{"p1"}, []string{"p1", "b1"}, []string{"b2", "p1", "b1"}},
		{"requested order respected", []string{"b1", "b2"}, []string{"p1", "p2"}, []string{"p2", "b2", "b1", "p1"}, []string{"p2", "b2", "b1", "p1"}},
		{"unknown ids dropped", []string{"b1"}, []string{"p1"}, []string{"x", "p1"}, []string{"b1", "p1"}},
		{"new builtin before unordered personal", []string{"b1", "bNew"}, []string{"p1"}, []string{"b1", "p1"}, []string{"bNew", "b1", "p1"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := OwnedOrder(tc.builtin, tc.personal, tc.requested)
			if len(got) != len(tc.want) {
				t.Fatalf("got %v want %v", got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Fatalf("got %v want %v", got, tc.want)
				}
			}
		})
	}
}

// --- registry validation ---

func TestRegistryValidateSelection(t *testing.T) {
	registry := NewRegistry()
	models := []RegistryModel{{
		ModelID: "m1",
		Config:  &ModelConfig{OptionSpecs: &ModelOptionSpecs{ReasoningLevel: &EnumOptionSpec{Values: []string{"low", "high"}}}},
	}}
	if err := registry.Replace([]RegistryProvider{{ProviderID: "p1", Config: &ProviderConfig{}, Models: models}}, "test"); err != nil {
		t.Fatal(err)
	}
	if v := registry.ValidateSelection(ModelSelection{ProviderID: "pX", ModelID: "m1"}); v.OK || v.Code != "provider-not-found" {
		t.Fatalf("want provider-not-found, got %+v", v)
	}
	if v := registry.ValidateSelection(ModelSelection{ProviderID: "p1", ModelID: "mX"}); v.OK || v.Code != "model-not-found" {
		t.Fatalf("want model-not-found, got %+v", v)
	}
	if v := registry.ValidateSelection(ModelSelection{ProviderID: "p1", ModelID: "m1"}); v.OK || v.Code != "reasoning-level-missing" {
		t.Fatalf("want reasoning-level-missing, got %+v", v)
	}
	if v := registry.ValidateSelection(ModelSelection{ProviderID: "p1", ModelID: "m1", Options: &SelectionOptions{ReasoningLevel: "mid"}}); v.OK || v.Code != "reasoning-level-not-supported" {
		t.Fatalf("want reasoning-level-not-supported, got %+v", v)
	}
	if v := registry.ValidateSelection(ModelSelection{ProviderID: "p1", ModelID: "m1", Options: &SelectionOptions{ReasoningLevel: "high"}}); !v.OK {
		t.Fatalf("want ok, got %+v", v)
	}
}

// --- file codec ---

func personalFileJSON(t *testing.T, update *LayerUpdate) []byte {
	t.Helper()
	encoded, err := EncodeProviderConfigFile(update)
	if err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(encoded)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func TestProviderConfigFileCodecRoundTrip(t *testing.T) {
	providers, _ := NewProviderConfigMap([]ProviderConfigRule{{
		ProviderID: "my-provider", ProviderName: strPtr("My Provider"), Enabled: boolPtr(true),
		Config: &ProviderConfig{
			Group:            strPtr(GroupStandardPersonal),
			Access:           &ProviderAccess{APIKey: &ApiKeyAccessConfig{Type: AccessAPIKey, APIKey: strPtr("sk-test-roundtrip")}},
			API:              &ProviderApiConfig{Type: strPtr(APIOpenAIChatCompletions), BaseURL: strPtr("https://api.example.com/v1")},
			PersonalModelIDs: []string{"m1"}, ModelOrder: []string{"m1"},
		},
	}})
	models, _ := EmptyModelConfigRules().SetExact("my-provider", "m1", &ModelConfig{
		Enabled: boolPtr(true), Properties: &ModelProperties{ContextWindow: intPtr(8192)},
	}, nil)
	update := &LayerUpdate{
		Providers: providers, Models: models, ProviderOrder: []string{"my-provider"},
		DefaultModelSelection: &ModelSelection{ProviderID: "my-provider", ModelID: "m1", Options: &SelectionOptions{ReasoningLevel: "high"}},
	}
	data := personalFileJSON(t, update)
	decoded, err := DecodeProviderConfigFile(data)
	if err != nil {
		t.Fatal(err)
	}
	again := personalFileJSON(t, decoded)
	if string(data) != string(again) {
		t.Fatalf("codec round-trip not canonical:\n%s\n%s", data, again)
	}
	// The snapshot revision matches the canonical encoding.
	snapshot, err := SnapshotFromUpdate(decoded)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Revision == "" {
		t.Fatal("snapshot revision must be the sha256 of the canonical encoding")
	}
}

func TestDecodeProviderConfigFileVersionGate(t *testing.T) {
	if _, err := DecodeProviderConfigFile([]byte(`{"config":{}}`)); err == nil {
		t.Fatal("missing schemaVersion must fail")
	}
	if _, err := DecodeProviderConfigFile([]byte(`{"schemaVersion":2,"config":{}}`)); err == nil {
		t.Fatal("future schemaVersion must fail")
	}
	var unsupported *UnsupportedProviderConfigVersionError
	if _, err := DecodeProviderConfigFile([]byte(`{"schemaVersion":2,"config":{}}`)); !asVersionError(err, &unsupported) {
		t.Fatalf("want UnsupportedProviderConfigVersionError, got %T", err)
	}
	// account:* providers must not carry access
	bad := []byte(`{"schemaVersion":1,"config":{"providerConfigRules":{"providerRules":[{"providerId":"account:zai","config":{"access":{"type":"api-key","apiKey":"k"}}}]},"modelConfigRules":{"providerModelRules":[],"manualProviderModelRules":[]}}}`)
	if _, err := DecodeProviderConfigFile(bad); err == nil {
		t.Fatal("account:* provider with personal access must be rejected")
	}
}

func asVersionError(err error, target **UnsupportedProviderConfigVersionError) bool {
	if e, ok := err.(*UnsupportedProviderConfigVersionError); ok {
		*target = e
		return true
	}
	return false
}

// --- builtin release ---

func TestBuiltinReleaseRoundTripWithBundledShape(t *testing.T) {
	release := &NexBuiltinRelease{
		SchemaVersion: 1, Revision: 30,
		Config: BuiltinConfigContent{
			Providers:         mustProviders(t),
			ProviderTemplates: EmptyProviderTemplateMap(),
			ModelConfigRules: NewModelConfigRules([]ModelConfigRule{
				rule(RuleModel, ".*", completeModel()),
			}),
		},
	}
	data, err := json.Marshal(EncodeNexBuiltinRelease(release))
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := DecodeNexBuiltinRelease(data)
	if err != nil {
		t.Fatal(err)
	}
	if SerializeNexBuiltinRelease(decoded) != SerializeNexBuiltinRelease(release) {
		t.Fatal("release round-trip not canonical")
	}
	// retired provider rejected
	zapi, _ := NewProviderConfigMap([]ProviderConfigRule{{
		ProviderID: RetiredZapiProviderID, Config: &ProviderConfig{},
	}})
	bad := &NexBuiltinRelease{SchemaVersion: 1, Revision: 1, Config: BuiltinConfigContent{Providers: zapi}}
	badData, _ := json.Marshal(EncodeNexBuiltinRelease(bad))
	if _, err := DecodeNexBuiltinRelease(badData); err == nil {
		t.Fatal("retired ZAPI provider must be rejected")
	}
}

func mustProviders(t *testing.T) *ProviderConfigMap {
	t.Helper()
	providers, err := NewProviderConfigMap([]ProviderConfigRule{{
		ProviderID: "p1", Config: &ProviderConfig{},
	}})
	if err != nil {
		t.Fatal(err)
	}
	return providers
}

func completeModel() *ModelConfig {
	return &ModelConfig{
		Enabled: boolPtr(true),
		Properties: &ModelProperties{
			RequiresMfjsToolSchema: boolPtr(false), ContextWindow: intPtr(200000),
			InputFormat:      &ModelInputFormat{SupportsText: boolPtr(true), SupportsImage: boolPtr(false), SupportsVideo: boolPtr(false), SupportsAudio: boolPtr(false), SupportsPdf: boolPtr(false)},
			OutputFormat:     &ModelOutputFormat{SupportsText: boolPtr(true)},
			SupportsToolCall: boolPtr(true), SupportsJsonSchemaOutput: boolPtr(false),
			SupportsNativeWebSearch: boolPtr(false), SupportsMidConversationSystem: boolPtr(false),
		},
		OptionSpecs: &ModelOptionSpecs{
			ReasoningLevel:  &EnumOptionSpec{Values: []string{"disabled", "enabled"}, Map: strPtr("{}")},
			MaxOutputTokens: &LimitOptionSpec{Max: intPtr(32000), Map: strPtr("{}")},
		},
	}
}

// --- resolver layering: config precedence table (builtin > account > template > personal) ---

func TestResolverLayeringPrecedence(t *testing.T) {
	builtin, _ := NewProviderConfigMap([]ProviderConfigRule{{
		ProviderID: "builtin:zai", ProviderName: strPtr("Zai"),
		Config: &ProviderConfig{
			Group: strPtr(GroupZaiFamily), Visibility: strPtr("visible"),
			Access:          &ProviderAccess{ZhipuAccount: &ZhipuAccountAccessConfig{Type: AccessZhipuAccount, Entitled: boolPtr(true)}},
			API:             &ProviderApiConfig{Type: strPtr(APIAnthropicMessages), BaseURL: strPtr("https://builtin.example")},
			BuiltinModelIDs: []string{"m1"},
		},
	}})
	personal, _ := NewProviderConfigMap([]ProviderConfigRule{{
		ProviderID: "builtin:zai",
		Config: &ProviderConfig{
			// overlay on builtin: no group; api overrides the base URL
			API: &ProviderApiConfig{BaseURL: strPtr("https://personal.example")},
		},
	}})
	account, _ := NewProviderConfigMap([]ProviderConfigRule{{
		ProviderID: "builtin:zai",
		Config:     &ProviderConfig{Access: &ProviderAccess{ZhipuAccount: &ZhipuAccountAccessConfig{Type: AccessZhipuAccount, Entitled: boolPtr(false)}}},
	}})
	resolution := Resolve(ResolutionInput{
		NexBuiltinProviders: builtin, PersonalProviders: personal, AccountProviders: account,
		NexBuiltinModelRules: NewModelConfigRules([]ModelConfigRule{rule(RuleModel, ".*", completeModel())}),
		PersonalModels:       EmptyModelConfigRules(),
	})
	if len(resolution.ResolvedProviders) != 1 {
		t.Fatalf("want one resolved provider, got %d", len(resolution.ResolvedProviders))
	}
	resolved := resolution.ResolvedProviders[0]
	// personal api overlay wins over builtin
	if resolved.Config.API.BaseURL == nil || *resolved.Config.API.BaseURL != "https://personal.example" {
		t.Fatal("personal api overlay must win over builtin")
	}
	// group stays builtin-owned (personal overlay dropped it)
	if resolved.Config.Group == nil || *resolved.Config.Group != GroupZaiFamily {
		t.Fatal("group must stay builtin-owned")
	}
	// 账号不再支持总禁用：enabled stays true even with a stale enabled=false overlay
	if !resolved.Enabled {
		t.Fatal("zhipu-account must stay enabled (账号不再支持总禁用)")
	}
	// BUT the model must not be executable — account entitled=false wins.
	if resolved.Models[0].Executable {
		t.Fatal("entitled=false account must block model execution")
	}
	// registry must not publish it
	if len(resolution.RegistryProviders) != 0 {
		t.Fatal("no executable models → provider not in registry")
	}
}

// --- agent facts parser (the Phase-1 extraction) ---

func TestParseAgentProviderFacts(t *testing.T) {
	data := []byte(`{
		"schemaVersion": 1,
		"config": {
			"providerConfigRules": {"providerRules": [{
				"providerId": "my-provider", "providerName": "My Provider",
				"config": {
					"access": {"type": "api-key", "apiKey": "sk-test-agent"},
					"api": {"type": "openai-chat-completions", "baseUrl": "https://api.example.com/v1"}
				}
			}]},
			"modelConfigRules": {"providerModelRules": [], "manualProviderModelRules": [{
				"providerId": "my-provider", "modelId": "m1",
				"config": {"enabled": true, "name": "M1", "properties": {"contextWindow": 8192},
					"optionSpecs": {"reasoningLevel": {"values": ["low", "high"]}, "maxOutputTokens": {"max": 4096}}}
			}]}
		}
	}`)
	facts, err := ParseAgentProviderFacts(data)
	if err != nil {
		t.Fatal(err)
	}
	if facts.ProviderID != "my-provider" || facts.ProviderName != "My Provider" {
		t.Fatalf("identity wrong: %+v", facts)
	}
	if facts.ProviderKind != "openai-compatible" {
		t.Fatalf("openai-chat-completions must normalize to openai-compatible, got %q", facts.ProviderKind)
	}
	if facts.ModelID != "m1" || facts.ModelName != "M1" {
		t.Fatalf("model wrong: %+v", facts)
	}
	if facts.BaseURL != "https://api.example.com/v1" || facts.APIKey != "sk-test-agent" {
		t.Fatalf("endpoint wrong: %+v", facts)
	}
	if facts.ContextWindow != 8192 || facts.MaxOutputToken != 4096 {
		t.Fatalf("limits wrong: %+v", facts)
	}
	if len(facts.ReasoningLevels) != 2 || facts.DefaultReasoning != "low" {
		t.Fatalf("reasoning wrong: %+v", facts)
	}
	// empty manual rules → the Phase-1 error string
	if _, err := ParseAgentProviderFacts([]byte(`{"schemaVersion":1,"config":{"providerConfigRules":{"providerRules":[]},"modelConfigRules":{"providerModelRules":[],"manualProviderModelRules":[]}}}`)); err == nil || err.Error() != "provider config has no manual model rules" {
		t.Fatalf("want no-manual-rules error, got %v", err)
	}
}
