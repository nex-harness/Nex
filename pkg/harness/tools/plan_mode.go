package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/nex-harness/nex/pkg/ai"
)

type exitPlanModeTool struct{}

// NewExitPlanModeTool creates the `exit_plan_mode` tool used in Plan Mode to submit a structured
// implementation plan for user review and approval before executing any workspace edits.
func NewExitPlanModeTool() Tool { return &exitPlanModeTool{} }

func (t *exitPlanModeTool) Name() string                  { return "exit_plan_mode" }
func (t *exitPlanModeTool) Category() string              { return "system" }
func (t *exitPlanModeTool) IsReadOnly(json.RawMessage) bool { return true }

func (t *exitPlanModeTool) Definition() ai.ToolDefinition {
	return ai.ToolDefinition{
		Name:     "exit_plan_mode",
		Category: "system",
		Description: "Present your completed implementation plan for user review and approval while in Plan Mode. " +
			"Call this once you have finished read-only exploration and written a clear, actionable plan with exact file paths, design choices, and verification steps.",
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"title": {
					"type": "string",
					"description": "Concise title summarizing the proposed plan."
				},
				"plan_markdown": {
					"type": "string",
					"description": "Full Markdown implementation plan including target files, key changes, risks, and test/verification steps."
				}
			},
			"required": ["plan_markdown"]
		}`),
	}
}

func (t *exitPlanModeTool) Execute(_ context.Context, _ string, rawArgs json.RawMessage, _ UpdateFunc) (Result, error) {
	var args struct {
		Title        string `json:"title"`
		PlanMarkdown string `json:"plan_markdown"`
	}
	if err := json.Unmarshal(rawArgs, &args); err != nil {
		return Result{Output: "invalid exit_plan_mode arguments: " + err.Error(), IsError: true}, nil
	}
	plan := strings.TrimSpace(args.PlanMarkdown)
	if plan == "" {
		return Result{Output: "plan_markdown is required", IsError: true}, nil
	}
	title := strings.TrimSpace(args.Title)
	if title == "" {
		title = "Implementation Plan"
	}
	return Result{
		Output: fmt.Sprintf("Plan submitted for user approval: %s\n\nPresent a concise summary to the user and wait for approval before making edits.", title),
		Metadata: map[string]any{
			"kind":          "plan_proposal",
			"title":         title,
			"plan_markdown": plan,
		},
	}, nil
}
