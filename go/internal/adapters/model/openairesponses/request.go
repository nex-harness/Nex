package openairesponses

// Request construction for the OpenAI Responses API.
//
// The body is built by github.com/openai/openai-go's param types
// (responses.ResponseNewParams) rather than by a hand-written marshaller (D14):
// FunctionToolParam.Parameters is an untyped map[string]any, so our raw
// tools.json schemas pass through with no schema generation and no rewriting.
// The one shape change the spike calls out is the flattening: tools.json stores
// the chat-completions shape ({"type":"function","function":{…}}) and the
// Responses API wants the flat shape ({"type":"function","name":…}). Order is
// preserved and no value is re-encoded.

import (
	"encoding/json"
	"fmt"

	"github.com/openai/openai-go/packages/param"
	"github.com/openai/openai-go/responses"
	"github.com/openai/openai-go/shared"

	"github.com/tylinux/nex/go/internal/tooldefs"
)

// ToolChoiceMode mirrors the Responses `tool_choice` union.
type ToolChoiceMode string

const (
	// ToolChoiceAuto lets the model decide (the agent's default).
	ToolChoiceAuto ToolChoiceMode = "auto"
	// ToolChoiceNone forbids tool calls.
	ToolChoiceNone ToolChoiceMode = "none"
	// ToolChoiceReq requires at least one tool call.
	ToolChoiceReq ToolChoiceMode = "required"
)

// Message is one conversation turn, in the provider-neutral shape the turn loop
// already produces (modelreq.Message).
//
// Responses has no per-message tool_calls array, so buildInput fans a turn's
// ToolCalls out into one function_call item per call, and a ToolCallID turn
// becomes a function_call_output item rather than a "tool" role message.
type Message struct {
	// Role is user, assistant, system or developer.
	Role string
	// Content is the turn's text.
	Content string
	// ToolCalls is a raw chat-completions tool_calls array.
	ToolCalls json.RawMessage
	// ToolCallID marks a tool result turn (chat-completions role "tool").
	ToolCallID string
}

// Request is one Responses request.
type Request struct {
	// Model is the model id sent as `model`.
	Model string
	// Instructions is the system/developer prompt. Responses takes it out of band
	// as `instructions` rather than as the first input item.
	Instructions string
	// Input is the conversation, oldest first.
	Input []Message
	// Tools are the tool definitions, sent verbatim once flattened.
	Tools []tooldefs.Def
	// ToolChoice selects how the model may call tools.
	ToolChoice ToolChoiceMode
	// MaxOutputTokens caps visible + reasoning tokens; 0 omits the field.
	MaxOutputTokens int
	// IncludeReasoningSummary asks for reasoning summaries, which is what makes
	// the provider emit the response.reasoning_summary_text.delta events we
	// decode into reasoning deltas.
	IncludeReasoningSummary bool
}

// BuildBody renders a Request as the exact JSON the wire carries.
func BuildBody(req Request) ([]byte, error) {
	params := responses.ResponseNewParams{
		Model: shared.ResponsesModel(req.Model),
	}
	if req.Instructions != "" {
		params.Instructions = param.NewOpt(req.Instructions)
	}
	if req.MaxOutputTokens > 0 {
		params.MaxOutputTokens = param.NewOpt(int64(req.MaxOutputTokens))
	}
	params.ToolChoice = toolChoice(req.ToolChoice)
	if req.IncludeReasoningSummary {
		params.Reasoning = shared.ReasoningParam{Summary: shared.ReasoningSummaryAuto}
	}
	items, err := buildInput(req.Input)
	if err != nil {
		return nil, err
	}
	params.Input = responses.ResponseNewParamsInputUnion{OfInputItemList: items}
	tools, err := buildTools(req.Tools)
	if err != nil {
		return nil, err
	}
	params.Tools = tools
	// Responses has no stream_options: asking for the event stream is the
	// `stream: true` body flag, and usage always arrives on the terminal event.
	// `stream` is not a typed field on ResponseNewParams in v1.12.0, so it goes
	// through the SDK's own extra-field mechanism (sjson on the marshalled
	// object) rather than being spliced into the bytes by hand.
	params.SetExtraFields(map[string]any{"stream": true})
	return params.MarshalJSON()
}

func toolChoice(mode ToolChoiceMode) responses.ResponseNewParamsToolChoiceUnion {
	var choice responses.ToolChoiceOptions
	switch mode {
	case ToolChoiceNone:
		choice = responses.ToolChoiceOptionsNone
	case ToolChoiceReq:
		choice = responses.ToolChoiceOptionsRequired
	case ToolChoiceAuto, "":
		choice = responses.ToolChoiceOptionsAuto
	}
	return responses.ResponseNewParamsToolChoiceUnion{OfToolChoiceMode: param.NewOpt(choice)}
}

// buildTools flattens the chat-completions tool entries into the Responses flat
// form. Each entry is decoded rather than mapped through a schema type, so the
// parameters object reaches the wire with the same keys and values, in the same
// order (JSON object key order is preserved by the decoder).
func buildTools(defs []tooldefs.Def) ([]responses.ToolUnionParam, error) {
	out := make([]responses.ToolUnionParam, 0, len(defs))
	for i, def := range defs {
		var entry struct {
			Function struct {
				Name        string          `json:"name"`
				Description string          `json:"description"`
				Parameters  json.RawMessage `json:"parameters"`
				Strict      *bool           `json:"strict"`
			} `json:"function"`
		}
		if err := json.Unmarshal(def.JSON, &entry); err != nil {
			return nil, fmt.Errorf("tool %d: %w", i, err)
		}
		if entry.Function.Name == "" {
			return nil, fmt.Errorf("tool %d has no function.name", i)
		}
		tool := responses.FunctionToolParam{
			Name:       entry.Function.Name,
			Parameters: map[string]any{},
		}
		if entry.Function.Description != "" {
			tool.Description = param.NewOpt(entry.Function.Description)
		}
		if len(entry.Function.Parameters) > 0 {
			if err := json.Unmarshal(entry.Function.Parameters, &tool.Parameters); err != nil {
				return nil, fmt.Errorf("tool %d: %w", i, err)
			}
		}
		if entry.Function.Strict != nil {
			tool.Strict = param.NewOpt(*entry.Function.Strict)
		}
		out = append(out, responses.ToolUnionParam{OfFunction: &tool})
	}
	return out, nil
}
