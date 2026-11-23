package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/nex-harness/nex/pkg/ai"
	"github.com/nex-harness/nex/pkg/protocol"
)

// flexiblePromptOption accepts either a JSON string ("PostgreSQL") or an object
// ({"label": "...", "description": "..."}) so callers/LLMs can use either format.
type flexiblePromptOption protocol.UIPromptOption

func (o *flexiblePromptOption) UnmarshalJSON(data []byte) error {
	trimmed := strings.TrimSpace(string(data))
	if trimmed == "" || trimmed == "null" {
		return nil
	}
	if strings.HasPrefix(trimmed, `"`) {
		var label string
		if err := json.Unmarshal(data, &label); err != nil {
			return err
		}
		o.Label = label
		o.Description = ""
		return nil
	}
	var obj struct {
		Label       string `json:"label"`
		Title       string `json:"title"`
		Description string `json:"description"`
	}
	if err := json.Unmarshal(data, &obj); err != nil {
		return err
	}
	label := strings.TrimSpace(obj.Label)
	if label == "" {
		label = strings.TrimSpace(obj.Title)
	}
	o.Label = label
	o.Description = strings.TrimSpace(obj.Description)
	return nil
}

type askUserQuestionItemArg struct {
	ID           string                 `json:"id,omitempty"`
	Header       string                 `json:"header,omitempty"`
	Question     string                 `json:"question"`
	Message      string                 `json:"message,omitempty"`
	Kind         string                 `json:"kind,omitempty"`
	Options      []flexiblePromptOption `json:"options,omitempty"`
	MultiSelect  bool                   `json:"multi_select,omitempty"`
	AllowCustom  *bool                  `json:"allow_custom,omitempty"`
	Placeholder  string                 `json:"placeholder,omitempty"`
	DefaultValue string                 `json:"default_value,omitempty"`
}

func convertPromptOptions(raw []flexiblePromptOption) ([]protocol.UIPromptOption, []string) {
	if len(raw) == 0 {
		return nil, nil
	}
	items := make([]protocol.UIPromptOption, 0, len(raw))
	labels := make([]string, 0, len(raw))
	for _, r := range raw {
		label := strings.TrimSpace(r.Label)
		desc := strings.TrimSpace(r.Description)
		if label == "" {
			continue
		}
		items = append(items, protocol.UIPromptOption{Label: label, Description: desc})
		labels = append(labels, label)
	}
	return items, labels
}

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
		Description: "Ask the user one or more structured clarifying questions in the interactive UI card and wait for their answers. Use when requirements, design trade-offs, or target choices are genuinely ambiguous. Supports `select` (choices with optional descriptions and custom answer input), `confirm` (yes/no), `input` (free text), and multi-question paginated prompts via `questions`.",
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"header": {"type": "string", "description": "Optional short category/tag badge shown next to the question (e.g., 'Architecture', 'Testing')"},
				"question": {"type": "string", "description": "Clear, concise question title shown to the user"},
				"message": {"type": "string", "description": "Optional additional context or details"},
				"kind": {"type": "string", "enum": ["select", "confirm", "input"], "description": "Prompt type (defaults to 'select' if options are provided, otherwise 'input')"},
				"options": {
					"type": "array",
					"items": {
						"oneOf": [
							{"type": "string"},
							{
								"type": "object",
								"properties": {
									"label": {"type": "string", "description": "Short bold option label"},
									"description": {"type": "string", "description": "Concise muted explanation shown beside or under the label"}
								},
								"required": ["label"]
							}
						]
					},
					"description": "List of selectable options when kind='select' (strings or {label, description} objects)"
				},
				"multi_select": {"type": "boolean", "description": "Allow selecting multiple options for this question"},
				"allow_custom": {"type": "boolean", "description": "Whether to show an inline custom answer text input row (defaults to true for select)"},
				"default_value": {"type": "string", "description": "Optional default answer"},
				"placeholder": {"type": "string", "description": "Optional input placeholder"},
				"questions": {
					"type": "array",
					"description": "Optional list of multiple paginated questions shown sequentially inside the question card (< 1 / N >)",
					"items": {
						"type": "object",
						"properties": {
							"id": {"type": "string"},
							"header": {"type": "string"},
							"question": {"type": "string"},
							"message": {"type": "string"},
							"kind": {"type": "string", "enum": ["select", "confirm", "input"]},
							"options": {"type": "array"},
							"multi_select": {"type": "boolean"},
							"allow_custom": {"type": "boolean"},
							"placeholder": {"type": "string"},
							"default_value": {"type": "string"}
						},
						"required": ["question"]
					}
				}
			}
		}`),
	}
}

func resolvePromptKind(rawKind string, optionCount int) string {
	kind := strings.TrimSpace(rawKind)
	if kind != "" {
		return kind
	}
	if optionCount > 0 {
		return "select"
	}
	return "input"
}

func (t *AskUserQuestionTool) Execute(ctx context.Context, _ string, rawArgs json.RawMessage, _ UpdateFunc) (Result, error) {
	var args struct {
		Header       string                   `json:"header,omitempty"`
		Question     string                   `json:"question"`
		Message      string                   `json:"message,omitempty"`
		Kind         string                   `json:"kind,omitempty"`
		Options      []flexiblePromptOption   `json:"options,omitempty"`
		MultiSelect  bool                     `json:"multi_select,omitempty"`
		AllowCustom  *bool                    `json:"allow_custom,omitempty"`
		DefaultValue string                   `json:"default_value,omitempty"`
		Placeholder  string                   `json:"placeholder,omitempty"`
		Questions    []askUserQuestionItemArg `json:"questions,omitempty"`
	}
	if err := json.Unmarshal(rawArgs, &args); err != nil {
		return Result{Output: "invalid ask_user_question arguments: " + err.Error(), IsError: true}, nil
	}

	var req protocol.UIPromptRequestPayload
	isMultiQuestion := len(args.Questions) > 0
	if isMultiQuestion {
		questions := make([]protocol.UIPromptQuestion, 0, len(args.Questions))
		for idx, item := range args.Questions {
			qText := strings.TrimSpace(item.Question)
			if qText == "" {
				return Result{Output: fmt.Sprintf("questions[%d].question is required", idx), IsError: true}, nil
			}
			optItems, _ := convertPromptOptions(item.Options)
			qKind := resolvePromptKind(item.Kind, len(optItems))
			if qKind == "select" && len(optItems) == 0 {
				return Result{Output: fmt.Sprintf("questions[%d].options must be provided when kind='select'", idx), IsError: true}, nil
			}
			questions = append(questions, protocol.UIPromptQuestion{
				ID:           strings.TrimSpace(item.ID),
				Header:       strings.TrimSpace(item.Header),
				Question:     qText,
				Message:      strings.TrimSpace(item.Message),
				Kind:         qKind,
				Options:      optItems,
				MultiSelect:  item.MultiSelect,
				AllowCustom:  item.AllowCustom,
				Placeholder:  strings.TrimSpace(item.Placeholder),
				DefaultValue: item.DefaultValue,
			})
		}
		first := questions[0]
		firstLabels := make([]string, 0, len(first.Options))
		for _, o := range first.Options {
			firstLabels = append(firstLabels, o.Label)
		}
		req = protocol.UIPromptRequestPayload{
			Kind:         first.Kind,
			Header:       first.Header,
			Title:        first.Question,
			Message:      first.Message,
			Options:      firstLabels,
			OptionItems:  first.Options,
			MultiSelect:  first.MultiSelect,
			AllowCustom:  first.AllowCustom,
			Placeholder:  first.Placeholder,
			DefaultValue: first.DefaultValue,
			Questions:    questions,
		}
	} else {
		q := strings.TrimSpace(args.Question)
		if q == "" {
			return Result{Output: "question is required", IsError: true}, nil
		}
		optItems, optLabels := convertPromptOptions(args.Options)
		kind := resolvePromptKind(args.Kind, len(optItems))
		if kind == "select" && len(optItems) == 0 {
			return Result{Output: "options must be provided when kind='select'", IsError: true}, nil
		}
		req = protocol.UIPromptRequestPayload{
			Kind:         kind,
			Header:       strings.TrimSpace(args.Header),
			Title:        q,
			Message:      strings.TrimSpace(args.Message),
			Options:      optLabels,
			OptionItems:  optItems,
			MultiSelect:  args.MultiSelect,
			AllowCustom:  args.AllowCustom,
			DefaultValue: args.DefaultValue,
			Placeholder:  strings.TrimSpace(args.Placeholder),
		}
	}

	if t.ui == nil || !t.ui.HasUI() {
		if req.DefaultValue != "" {
			return Result{
				Output:   fmt.Sprintf("No interactive UI connected (headless mode); using default answer: %s", req.DefaultValue),
				Metadata: map[string]any{"headless": true, "answer": req.DefaultValue},
			}, nil
		}
		return Result{
			Output:  "No interactive UI connected (headless mode); proceed with your best documented default assumption or state the open question in your final response.",
			IsError: true,
		}, nil
	}

	res, err := t.ui.Prompt(ctx, req)
	if err != nil {
		return Result{Output: fmt.Sprintf("ask_user_question failed: %v", err), IsError: true}, nil
	}
	if res.Canceled {
		return Result{
			Output:   "User dismissed/canceled the question prompt.",
			Metadata: map[string]any{"canceled": true},
		}, nil
	}

	if len(req.Questions) > 1 {
		var parsed struct {
			Answers []struct {
				Question string `json:"question"`
				Answer   string `json:"answer"`
			} `json:"answers"`
		}
		if err := json.Unmarshal([]byte(res.Value), &parsed); err == nil && len(parsed.Answers) > 0 {
			var sb strings.Builder
			sb.WriteString("User answered:")
			for idx, item := range parsed.Answers {
				fmt.Fprintf(&sb, "\n%d. %s: %s", idx+1, item.Question, item.Answer)
			}
			return Result{
				Output:   sb.String(),
				Metadata: map[string]any{"answers": parsed.Answers},
			}, nil
		}
	}

	if req.Kind == "confirm" && len(req.Questions) <= 1 {
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
