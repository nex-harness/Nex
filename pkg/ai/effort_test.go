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
			name: "gemini includes minimal",
			meta: ModelMeta{ID: "gemini-2.5-pro", SupportsReasoning: true},
			want: []string{"off", "minimal", "low", "medium", "high"},
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
