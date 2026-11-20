package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/nex-harness/nex/pkg/ai"
	"github.com/nex-harness/nex/pkg/protocol"
)

// UIPrompter abstracts interactive user prompts (satisfied by Runner's UIBridge).
type UIPrompter interface {
	HasUI() bool
	Prompt(ctx context.Context, req protocol.UIPromptRequestPayload) (protocol.UIResolvePayload, error)
}

// AskUserQuestionTool lets the agent pause and ask the user a structured question
// (multiple-choice select, confirmation, or free-form input) via the connected Web/Desktop UI.
type AskUserQuestionTool struct {
	ui UIPrompter
}

func NewAskUserQuestionTool(ui UIPrompter) *AskUserQuestionTool {
	return &AskUserQuestionTool{ui: ui}
}

func (t *AskUserQuestionTool) Name() string                      { return "ask_user_question" }
func (t *AskUserQuestionTool) Category() string                  { return "system" }
func (t *AskUserQuestionTool) IsReadOnly(_ json.RawMessage) bool { return true }

func (t *AskUserQuestionTool) Definition() ai.ToolDefinition {
	return ai.ToolDefinition{
		Name:        "ask_user_question",
		Category:    "system",
		Description: "Ask the user a structured clarifying question in the UI and wait for their answer. Use when requirements, design trade-offs, or target choices are genuinely ambiguous. Supports `select` (choices), `confirm` (yes/no), and `input` (free text).",
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"question": {"type": "string", "description": "Clear, concise question title shown to the user"},
				"message": {"type": "string", "description": "Optional additional context or details"},
				"kind": {"type": "string", "enum": ["select", "confirm", "input"], "description": "Prompt type (defaults to 'select' if options are provided, otherwise 'input')"},
				"options": {"type": "array", "items": {"type": "string"}, "description": "List of selectable options when kind='select'"},
				"default_value": {"type": "string", "description": "Optional default answer"},
				"placeholder": {"type": "string", "description": "Optional input placeholder when kind='input'"}
			},
			"required": ["question"]
		}`),
	}
}

func (t *AskUserQuestionTool) Execute(ctx context.Context, _ string, rawArgs json.RawMessage, _ UpdateFunc) (Result, error) {
	var args struct {
		Question     string   `json:"question"`
		Message      string   `json:"message,omitempty"`
		Kind         string   `json:"kind,omitempty"`
		Options      []string `json:"options,omitempty"`
		DefaultValue string   `json:"default_value,omitempty"`
		Placeholder  string   `json:"placeholder,omitempty"`
	}
	if err := json.Unmarshal(rawArgs, &args); err != nil {
		return Result{Output: "invalid ask_user_question arguments: " + err.Error(), IsError: true}, nil
	}
	q := strings.TrimSpace(args.Question)
	if q == "" {
		return Result{Output: "question is required", IsError: true}, nil
	}

	kind := strings.TrimSpace(args.Kind)
	if kind == "" {
		if len(args.Options) > 0 {
			kind = "select"
		} else {
			kind = "input"
		}
	}
	if kind == "select" && len(args.Options) == 0 {
		return Result{Output: "options must be provided when kind='select'", IsError: true}, nil
	}

	if t.ui == nil || !t.ui.HasUI() {
		if args.DefaultValue != "" {
			return Result{
				Output:   fmt.Sprintf("No interactive UI connected (headless mode); using default answer: %s", args.DefaultValue),
				Metadata: map[string]any{"headless": true, "answer": args.DefaultValue},
			}, nil
		}
		return Result{
			Output:  "No interactive UI connected (headless mode); proceed with your best documented default assumption or state the open question in your final response.",
			IsError: true,
		}, nil
	}

	res, err := t.ui.Prompt(ctx, protocol.UIPromptRequestPayload{
		Kind:         kind,
		Title:        q,
		Message:      args.Message,
		Options:      args.Options,
		DefaultValue: args.DefaultValue,
		Placeholder:  args.Placeholder,
	})
	if err != nil {
		return Result{Output: fmt.Sprintf("ask_user_question failed: %v", err), IsError: true}, nil
	}
	if res.Canceled {
		return Result{
			Output:   "User dismissed/canceled the question prompt.",
			Metadata: map[string]any{"canceled": true},
		}, nil
	}

	if kind == "confirm" {
		ans := "no"
		if res.Confirmed {
			ans = "yes"
		}
		return Result{
			Output:   fmt.Sprintf("User answered: %s", ans),
			Metadata: map[string]any{"confirmed": res.Confirmed, "answer": ans},
		}, nil
	}

	return Result{
		Output:   fmt.Sprintf("User answered: %s", res.Value),
		Metadata: map[string]any{"answer": res.Value},
	}, nil
}
