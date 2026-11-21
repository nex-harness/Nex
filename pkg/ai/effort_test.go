package ai

import (
	"reflect"
	"testing"
)

func TestDefaultReasoningLevelsByModelFamily(t *testing.T) {
	cases := []struct {
		name string
		meta ModelMeta
		want []string
	}{
		{
			name: "non-reasoning model returns nil",
			meta: ModelMeta{ID: "gpt-4o-mini", SupportsReasoning: false},
			want: nil,
		},
		{
			name: "claude uses max",
			meta: ModelMeta{ID: "claude-opus-4-6", SupportsReasoning: true},
			want: []string{"off", "low", "medium", "high", "max"},
		},
		{
			name: "gpt-5 codex uses xhigh",
			meta: ModelMeta{ID: "gpt-5.2-codex", SupportsReasoning: true},
			want: []string{"off", "low", "medium", "high", "xhigh"},
		},
		{
			name: "gemini 2.5 includes minimal",
			meta: ModelMeta{ID: "gemini-2.5-pro", SupportsReasoning: true},
			want: []string{"off", "minimal", "low", "medium", "high"},
		},
		{
			name: "gemini 4 defaults to low/medium/high without off/minimal",
			meta: ModelMeta{ID: "gemini-4-carbon", SupportsReasoning: true},
			want: []string{"low", "medium", "high"},
		},
		{
			name: "deepseek uses low/high/max",
			meta: ModelMeta{ID: "auto/deepseek-flash", SupportsReasoning: true},
			want: []string{"low", "high", "max"},
		},
		{
			name: "explicit custom levels take precedence",
			meta: ModelMeta{ID: "my-model", SupportsReasoning: false, ReasoningLevels: []string{"off", "turbo", "ultra"}},
			want: []string{"off", "turbo", "ultra"},
		},
	}

	for _, tc := range cases {
		got := DefaultReasoningLevels(tc.meta)
		if !reflect.DeepEqual(got, tc.want) {
			t.Errorf("%s: got %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestResolveWireEffortMappingAndClamping(t *testing.T) {
	// 1. OpenAI Responses clamps max -> xhigh
	val, active := ResolveWireEffort(&StreamRequest{ThinkingEffort: ThinkingMax}, []string{"minimal", "low", "medium", "high", "xhigh"})
	if !active || val != "xhigh" {
		t.Fatalf("expected max->xhigh on responses, got val=%q active=%v", val, active)
	}

	// 2. OpenAI Chat clamps xhigh/max -> high
	val, active = ResolveWireEffort(&StreamRequest{ThinkingEffort: ThinkingXHigh}, []string{"minimal", "low", "medium", "high"})
	if !active || val != "high" {
		t.Fatalf("expected xhigh->high on chat, got val=%q active=%v", val, active)
	}

	// 3. Explicit ReasoningMap overrides default clamp (e.g. gateway accepting "ultra" or explicit "off":"none")
	val, active = ResolveWireEffort(&StreamRequest{
		ThinkingEffort: ThinkingMax,
		ReasoningMap:   map[string]string{"max": "ultra", "off": "none"},
	}, []string{"low", "medium", "high"})
	if !active || val != "ultra" {
		t.Fatalf("expected ReasoningMap override max->ultra, got val=%q active=%v", val, active)
	}

	val, active = ResolveWireEffort(&StreamRequest{
		ThinkingEffort: ThinkingOff,
		ReasoningMap:   map[string]string{"off": "none"},
	}, []string{"low", "medium", "high"})
	if active || val != "none" {
		t.Fatalf("expected explicit off->none mapping, got val=%q active=%v", val, active)
	}

	// 4. Unknown custom effort word passes through verbatim
	val, active = ResolveWireEffort(&StreamRequest{ThinkingEffort: "deep_think"}, []string{"low", "medium", "high"})
	if !active || val != "deep_think" {
		t.Fatalf("expected custom word pass-through, got val=%q active=%v", val, active)
	}

	// 5. DeepSeek declared ReasoningLevels ["low","high","max"] passes "max" verbatim on Responses/Chat!
	val, active = ResolveWireEffort(&StreamRequest{
		ThinkingEffort:  ThinkingMax,
		ReasoningLevels: []string{"low", "high", "max"},
	}, []string{"minimal", "low", "medium", "high", "xhigh"})
	if !active || val != "max" {
		t.Fatalf("expected DeepSeek declared max to pass through verbatim, got val=%q active=%v", val, active)
	}

	// 6. Anthropic numeric budget override in ReasoningMap
	budget := thinkingBudgetForEffort(&StreamRequest{
		ThinkingEffort: ThinkingHigh,
		ReasoningMap:   map[string]string{"high": "12345"},
	})
	if budget != 12345 {
		t.Fatalf("expected custom numeric budget 12345, got %d", budget)
	}
}

func TestParseModelsDevReasoningOptionsAndProviderPriority(t *testing.T) {
	raw := []byte(`{
		"aaa-gateway": {
			"id": "aaa-gateway",
			"models": {
				"gemini-4-carbon": {
					"id": "gemini-4-carbon",
					"reasoning": true,
					"reasoning_options": [{"type": "effort", "values": ["medium", "high"]}]
				}
			}
		},
		"google": {
			"id": "google",
			"models": {
				"gemini-4-carbon": {
					"id": "gemini-4-carbon",
					"reasoning": true,
					"reasoning_options": [{"type": "effort", "values": ["low", "medium", "high"]}]
				},
				"gemini-4-hydrogen": {
					"id": "gemini-4-hydrogen",
					"reasoning": true,
					"reasoning_options": [{"type": "effort", "values": ["low", "medium", "high", "xhigh"]}]
				}
			}
		},
		"openai": {
			"id": "openai",
			"models": {
				"gpt-5.4": {
					"id": "gpt-5.4",
					"reasoning": true,
					"reasoning_options": [{"type": "effort", "values": ["none", "low", "medium", "high", "xhigh"]}]
				}
			}
		},
		"deepseek": {
			"id": "deepseek",
			"models": {
				"deepseek-v4-pro": {
					"id": "deepseek-v4-pro",
					"reasoning": true,
					"reasoning_options": [{"type": "toggle"}, {"type": "effort", "values": ["low", "high", "max"]}]
				}
			}
		},
		"zai": {
			"id": "zai",
			"models": {
				"glm-5": {
					"id": "glm-5",
					"reasoning": true,
					"reasoning_options": [{"type": "toggle"}]
				}
			}
		}
	}`)

	catalog, err := ParseModelsDevJSON(raw)
	if err != nil {
		t.Fatalf("ParseModelsDevJSON failed: %v", err)
	}

	// Official provider "google" should win over alphabetically earlier "aaa-gateway"
	carbon, ok := LookupModelMeta(catalog, "my-gateway/google/gemini-4-carbon")
	if !ok || carbon.ProviderID != "google" {
		t.Fatalf("expected google provider priority, got %+v ok=%v", carbon, ok)
	}
	if !reflect.DeepEqual(carbon.ReasoningLevels, []string{"low", "medium", "high"}) {
		t.Fatalf("unexpected gemini-4-carbon levels: %v", carbon.ReasoningLevels)
	}

	// Preferred provider explicitly requested wins first
	gwCarbon, ok := LookupModelMetaForProvider(catalog, "gemini-4-carbon", "aaa-gateway")
	if !ok || gwCarbon.ProviderID != "aaa-gateway" || !reflect.DeepEqual(gwCarbon.ReasoningLevels, []string{"medium", "high"}) {
		t.Fatalf("expected preferred provider aaa-gateway, got %+v", gwCarbon)
	}

	// "none" normalizes to "off"
	gpt54, ok := LookupModelMeta(catalog, "gpt-5.4")
	if !ok || !reflect.DeepEqual(gpt54.ReasoningLevels, []string{"off", "low", "medium", "high", "xhigh"}) {
		t.Fatalf("unexpected gpt-5.4 levels: %+v", gpt54)
	}

	// toggle + effort prepends "off"
	ds, ok := LookupModelMeta(catalog, "deepseek-v4-pro")
	if !ok || !reflect.DeepEqual(ds.ReasoningLevels, []string{"off", "low", "high", "max"}) {
		t.Fatalf("unexpected deepseek-v4-pro levels: %+v", ds)
	}

	// pure toggle maps to ["off", "on"]
	glm, ok := LookupModelMeta(catalog, "glm-5")
	if !ok || !reflect.DeepEqual(glm.ReasoningLevels, []string{"off", "on"}) {
		t.Fatalf("unexpected glm-5 levels: %+v", glm)
	}
}
