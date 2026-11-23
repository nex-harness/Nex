package runner

import (
	"context"
	"testing"

	"github.com/nex-harness/nex/pkg/ai"
	"github.com/nex-harness/nex/pkg/harness/tools"
)

type stubJevClassifier struct {
	answers map[string]ai.ClassifierAnswer
}

func (s *stubJevClassifier) Classify(_ context.Context, req *ai.ClassifyRequest) (*ai.ClassifyResponse, error) {
	return &ai.ClassifyResponse{
		Provider:   "typesafe",
		Model:      req.Model,
		StopReason: "stop",
		Answers:    s.answers,
	}, nil
}

func TestJevGuardrailAndRouter(t *testing.T) {
	yes := true
	no := false

	// 1. High-confidence safe command -> auto-approved
	dec := tools.EvaluateToolCallWithJev(context.Background(), tools.JevGuardrailConfig{
		Provider: &stubJevClassifier{answers: map[string]ai.ClassifierAnswer{
			"safe_to_auto_approve": {Type: ai.QuestionTypeBool, Bool: &yes, Probability: 0.94},
		}},
	}, "bash", "/tmp", map[string]string{"command": "go test ./..."})
	if !dec.AutoApprove {
		t.Fatalf("expected auto-approve, got %+v", dec)
	}

	// 2. Low-confidence or unsafe -> requires interactive approval
	dec2 := tools.EvaluateToolCallWithJev(context.Background(), tools.JevGuardrailConfig{
		Provider: &stubJevClassifier{answers: map[string]ai.ClassifierAnswer{
			"safe_to_auto_approve": {Type: ai.QuestionTypeBool, Bool: &no, Probability: 0.91},
		}},
	}, "bash", "/tmp", map[string]string{"command": "rm -rf /tmp/foo"})
	if dec2.AutoApprove {
		t.Fatalf("expected interactive approval required, got %+v", dec2)
	}

	// 3. Auto model router: simple QA -> fast / low
	r1 := RoutePromptWithJev(context.Background(), &stubJevClassifier{answers: map[string]ai.ClassifierAnswer{
		"complexity":   {Type: ai.QuestionTypeChoice, Choice: "simple"},
		"mistake_cost": {Type: ai.QuestionTypeChoice, Choice: "low"},
		"task_kind":    {Type: ai.QuestionTypeChoice, Choice: "qa"},
	}}, "jev-latest", "what does grep do?", false)
	if !r1.UsedClassifier || r1.Tier != RouteTierFast || r1.ThinkingEffort != ai.ThinkingLow {
		t.Fatalf("expected fast/low, got %+v", r1)
	}

	// 4. Auto model router: architecture / high mistake cost -> reasoning / high
	r2 := RoutePromptWithJev(context.Background(), &stubJevClassifier{answers: map[string]ai.ClassifierAnswer{
		"complexity":   {Type: ai.QuestionTypeChoice, Choice: "complex"},
		"mistake_cost": {Type: ai.QuestionTypeChoice, Choice: "high"},
		"task_kind":    {Type: ai.QuestionTypeChoice, Choice: "architecture"},
	}}, "jev-latest", "redesign distributed lock consensus", false)
	if !r2.UsedClassifier || r2.Tier != RouteTierReasoning || r2.ThinkingEffort != ai.ThinkingHigh {
		t.Fatalf("expected reasoning/high, got %+v", r2)
	}
}
