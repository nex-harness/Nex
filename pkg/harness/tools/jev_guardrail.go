package tools

import (
	"context"
	"strings"
	"time"

	"github.com/nex-harness/nex/pkg/ai"
)

// JevGuardrailConfig tunes the Jev-backed smart permission gate for gray-area tool calls.
type JevGuardrailConfig struct {
	Provider       ai.ClassifierProvider
	Model          string
	MinProbability float64       // default 0.85
	Timeout        time.Duration // default 3s
}

// GuardrailDecision captures the outcome of a Jev safety classification.
type GuardrailDecision struct {
	AutoApprove bool
	Probability float64
	Reason      string
}

// EvaluateToolCallWithJev judges whether a gray-area tool call (e.g. a bash command that passed
// deterministic deny rules but was not on the deterministic allowlist) is safe to auto-approve.
// Any error, timeout, or low confidence conservatively falls back to AutoApprove=false.
func EvaluateToolCallWithJev(ctx context.Context, cfg JevGuardrailConfig, toolName, workDir string, argsSummary any) GuardrailDecision {
	if cfg.Provider == nil {
		return GuardrailDecision{AutoApprove: false, Reason: "no classifier configured"}
	}
	threshold := cfg.MinProbability
	if threshold <= 0 || threshold > 1 {
		threshold = 0.85
	}
	timeout := cfg.Timeout
	if timeout <= 0 {
		timeout = 3 * time.Second
	}
	model := strings.TrimSpace(cfg.Model)
	if model == "" {
		model = "jev-latest"
	}

	callCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	resp, err := cfg.Provider.Classify(callCtx, &ai.ClassifyRequest{
		Model: model,
		State: map[string]any{
			"tool":    toolName,
			"workdir": workDir,
			"args":    argsSummary,
		},
		Questions: map[string]ai.ClassifierQuestion{
			"safe_to_auto_approve": {
				Type:         ai.QuestionTypeBool,
				Instructions: "Is this coding-agent tool invocation clearly safe to auto-approve without user confirmation?",
				Criteria: map[string]string{
					"true":  "Read-only, local build/test/lint, or bounded workspace edit with no destructive deletion, credential exfiltration, or irreversible system/remote mutation.",
					"false": "Potentially destructive (rm -rf, git reset --hard, force push), accesses secrets/credentials, modifies system files outside workspace, or has unclear side effects.",
				},
			},
		},
	})
	if err != nil || resp == nil || resp.StopReason != "stop" {
		return GuardrailDecision{AutoApprove: false, Reason: "classifier unavailable"}
	}
	ans, ok := resp.Answers["safe_to_auto_approve"]
	if !ok || ans.Bool == nil {
		return GuardrailDecision{AutoApprove: false, Reason: "missing classifier answer"}
	}
	if *ans.Bool && ans.Probability >= threshold {
		return GuardrailDecision{
			AutoApprove: true,
			Probability: ans.Probability,
			Reason:      "auto-approved by Jev guardrail",
		}
	}
	return GuardrailDecision{
		AutoApprove: false,
		Probability: ans.Probability,
		Reason:      "requires interactive user approval",
	}
}
