package runner

import (
	"context"
	"strings"
	"time"

	"github.com/nex-harness/nex/pkg/ai"
)

// JevRouteTier is the capability tier chosen by the Jev Auto Model Router.
type JevRouteTier string

const (
	RouteTierFast      JevRouteTier = "fast"
	RouteTierBalanced  JevRouteTier = "balanced"
	RouteTierReasoning JevRouteTier = "reasoning"
)

// JevRouteResult holds the routed tier, recommended thinking effort, and raw classification metadata.
type JevRouteResult struct {
	Tier           JevRouteTier
	ThinkingEffort ai.ThinkingEffort
	Complexity     string
	MistakeCost    string
	TaskKind       string
	UsedClassifier bool
}

// RoutePromptWithJev classifies a user turn with a Jev classifier to pick a capability tier and thinking effort.
// On any error or timeout, it falls back safely to RouteTierBalanced / medium effort.
func RoutePromptWithJev(ctx context.Context, provider ai.ClassifierProvider, model, prompt string, hasImages bool) JevRouteResult {
	fallback := JevRouteResult{
		Tier:           RouteTierBalanced,
		ThinkingEffort: ai.ThinkingMedium,
		UsedClassifier: false,
	}
	if provider == nil || strings.TrimSpace(prompt) == "" {
		return fallback
	}
	if strings.TrimSpace(model) == "" {
		model = "jev-latest"
	}

	callCtx, cancel := context.WithTimeout(ctx, 2500*time.Millisecond)
	defer cancel()

	resp, err := provider.Classify(callCtx, &ai.ClassifyRequest{
		Model: model,
		State: map[string]any{
			"prompt":     prompt,
			"has_images": hasImages,
		},
		Questions: map[string]ai.ClassifierQuestion{
			"complexity": {
				Type:         ai.QuestionTypeChoice,
				Instructions: "How complex is this engineering request?",
				Choices:      []string{"simple", "moderate", "complex"},
			},
			"mistake_cost": {
				Type:         ai.QuestionTypeChoice,
				Instructions: "How costly would a mistake be on this turn?",
				Choices:      []string{"low", "high"},
			},
			"task_kind": {
				Type:         ai.QuestionTypeChoice,
				Instructions: "What kind of work is requested?",
				Choices:      []string{"qa", "edit", "architecture"},
			},
		},
	})
	if err != nil || resp == nil || resp.StopReason != "stop" {
		return fallback
	}

	complexity := resp.Answers["complexity"].Choice
	mistakeCost := resp.Answers["mistake_cost"].Choice
	taskKind := resp.Answers["task_kind"].Choice

	res := JevRouteResult{
		Complexity:     complexity,
		MistakeCost:    mistakeCost,
		TaskKind:       taskKind,
		UsedClassifier: true,
	}

	switch {
	case complexity == "complex" || mistakeCost == "high" || taskKind == "architecture":
		res.Tier = RouteTierReasoning
		res.ThinkingEffort = ai.ThinkingHigh
	case complexity == "simple" && mistakeCost == "low" && taskKind == "qa":
		res.Tier = RouteTierFast
		res.ThinkingEffort = ai.ThinkingLow
	default:
		res.Tier = RouteTierBalanced
		res.ThinkingEffort = ai.ThinkingMedium
	}
	return res
}
