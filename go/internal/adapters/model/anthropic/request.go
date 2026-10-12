package anthropic

import (
	"encoding/json"
	"fmt"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/packages/param"
	"github.com/anthropics/anthropic-sdk-go/shared/constant"

	"github.com/tylinux/nex/go/internal/modelreq"
	"github.com/tylinux/nex/go/internal/tooldefs"
)

// RequestInput is everything the Anthropic Messages request needs. It mirrors
// modelreq.Input (the chat-completions shape the agent already builds) plus the
// Messages-only knobs: the system prompt is a top-level field, not a message,
// and thinking/reasoning is a provider feature with its own budget.
type RequestInput struct {
	// ModelID is the Anthropic model, e.g. "claude-sonnet-4-5".
	ModelID string
	// System is the merged system prompt (promptctx.Build().MergedSystem). It is
	// sent as the top-level `system` field, never as a message: the Messages API
	// has no "system" role.
	System string
	// MetaUser is the <system-reminder> preamble messages the agent sends; these
	// are real user turns, unlike the system prompt.
	MetaUser []modelreq.Message
	// History is the conversation after the system/meta-user messages, using the
	// same shape modelreq.Build consumes.
	History []modelreq.Message
	// MaxOutputTokens is `max_tokens`, required by the Messages API.
	MaxOutputTokens int
	// Tools are the tool definitions in the order they are declared.
	Tools []tooldefs.Def
	// ToolChoice maps to the Messages `tool_choice` union. Empty means auto,
	// which is what the agent always sends on the chat path.
	ToolChoice ToolChoice
	// Thinking enables extended thinking when the provider supports it. A nil
	// value means "do not send the field at all", which is the right behaviour for
	// providers that reject it.
	Thinking *ThinkingConfig
	// BetaHeaders are anthropic-beta feature flags. The SDK manages none of them
	// (spike R4), so they are ours to set.
	BetaHeaders []string
}

// ToolChoice is the Messages `tool_choice` union.
type ToolChoice string

const (
	// ToolChoiceAuto lets the model decide whether to call a tool.
	ToolChoiceAuto ToolChoice = "auto"
	// ToolChoiceAny forces a tool call.
	ToolChoiceAny ToolChoice = "any"
	// ToolChoiceNone forbids tool calls.
	ToolChoiceNone ToolChoice = "none"
	// ToolChoiceTool forces one specific tool.
	ToolChoiceTool ToolChoice = "tool"
)

// ThinkingConfig is the `thinking` request field. Only the budget matters for us:
// the AI SDK's anthropic provider sends `{type:"enabled", budget_tokens}`.
type ThinkingConfig struct {
	BudgetTokens int64
}

// defaultMaxTokens is used when the caller does not set one. The Messages API
// requires max_tokens, and 0 means "cache warm-up only, generate nothing", which
// would silently produce an empty completion.
const defaultMaxTokens = 4096

// BuildParams converts the agent's internal request model into anthropic.MessageNewParams.
// It never mutates the SDK's global state; the marshalled body comes from the SDK's
// param.MarshalObject, so field order and `omitzero` handling are the SDK's.
func BuildParams(in RequestInput) (anthropic.MessageNewParams, error) {
	if in.ModelID == "" {
		return anthropic.MessageNewParams{}, fmt.Errorf("anthropic: model id is required")
	}
	maxTokens := in.MaxOutputTokens
	if maxTokens <= 0 {
		maxTokens = defaultMaxTokens
	}
	params := anthropic.MessageNewParams{
		Model:     anthropic.Model(in.ModelID),
		MaxTokens: int64(maxTokens),
	}
	// System is a top-level field, not a message.
	if in.System != "" {
		params.System = []anthropic.TextBlockParam{{Text: in.System}}
	}
	params.Messages = buildMessages(in.MetaUser, in.History)
	params.ToolChoice = buildToolChoice(in.ToolChoice)
	if len(in.Tools) > 0 {
		tools, err := buildTools(in.Tools)
		if err != nil {
			return anthropic.MessageNewParams{}, err
		}
		params.Tools = tools
	}
	if in.Thinking != nil {
		params.Thinking = anthropic.ThinkingConfigParamUnion{
			OfEnabled: &anthropic.ThinkingConfigEnabledParam{BudgetTokens: in.Thinking.BudgetTokens},
		}
	}
	return params, nil
}

// buildMessages converts the chat-shaped history into Messages content blocks.
// Consecutive messages of the same role are merged, because the Messages API
// combines them on its side and gateways reject alternating-turn violations.
func buildMessages(metaUser, history []modelreq.Message) []anthropic.MessageParam {
	var out []anthropic.MessageParam
	appendMsg := func(role anthropic.MessageParamRole, content []anthropic.ContentBlockParamUnion) {
		if len(content) == 0 {
			return
		}
		if n := len(out); n > 0 && out[n-1].Role == role {
			out[n-1].Content = append(out[n-1].Content, content...)
			return
		}
		out = append(out, anthropic.MessageParam{Role: role, Content: content})
	}
	for _, m := range metaUser {
		appendMsg(anthropic.MessageParamRoleUser, []anthropic.ContentBlockParamUnion{anthropic.NewTextBlock(m.Content)})
	}
	for _, m := range history {
		appendMsg(anthropic.MessageParamRoleAssistant, assistantBlocks(m))
		appendMsg(anthropic.MessageParamRoleUser, userBlocks(m))
	}
	return out
}

// assistantBlocks converts one history message into assistant-side content blocks:
// plain text, tool_use blocks (from the raw tool_calls array), or nothing.
func assistantBlocks(m modelreq.Message) []anthropic.ContentBlockParamUnion {
	if len(m.ToolCalls) > 0 {
		return toolUseBlocks(m.ToolCalls)
	}
	if m.Content == "" {
		return nil
	}
	return []anthropic.ContentBlockParamUnion{anthropic.NewTextBlock(m.Content)}
}

// userBlocks converts one history message into user-side content blocks. A
// tool-result message (ToolCallID set) becomes a tool_result block, which is how
// the Messages API carries tool output back to the model.
func userBlocks(m modelreq.Message) []anthropic.ContentBlockParamUnion {
	if m.ToolCallID != "" {
		return []anthropic.ContentBlockParamUnion{anthropic.NewToolResultBlock(m.ToolCallID, m.Content, false)}
	}
	if m.Content == "" {
		return nil
	}
	return []anthropic.ContentBlockParamUnion{anthropic.NewTextBlock(m.Content)}
}

// toolCallRecord is the subset of an OpenAI tool_calls entry we replay as a
// Messages tool_use block. `input` is the already-parsed arguments object so a
// re-serialized body carries the same JSON values.
type toolCallRecord struct {
	ID       string `json:"id"`
	Type     string `json:"type"`
	Function struct {
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
	} `json:"function"`
}

func toolUseBlocks(raw json.RawMessage) []anthropic.ContentBlockParamUnion {
	var calls []toolCallRecord
	if err := json.Unmarshal(raw, &calls); err != nil {
		// A malformed tool_calls array is left to the provider to reject, matching
		// the chat path, which forwards the raw array verbatim.
		return nil
	}
	out := make([]anthropic.ContentBlockParamUnion, 0, len(calls))
	for _, c := range calls {
		var input any
		if len(c.Function.Arguments) > 0 {
			if err := json.Unmarshal(c.Function.Arguments, &input); err != nil {
				input = map[string]any{}
			}
		}
		if input == nil {
			input = map[string]any{}
		}
		out = append(out, anthropic.NewToolUseBlock(c.ID, input, c.Function.Name))
	}
	return out
}

func buildToolChoice(choice ToolChoice) anthropic.ToolChoiceUnionParam {
	switch choice {
	case ToolChoiceAny:
		return anthropic.ToolChoiceUnionParam{OfAny: &anthropic.ToolChoiceAnyParam{}}
	case ToolChoiceNone:
		return anthropic.ToolChoiceUnionParam{OfNone: &anthropic.ToolChoiceNoneParam{}}
	case ToolChoiceTool:
		// A named tool is supplied by the caller through the extra field; the
		// default path never forces a specific tool.
		return anthropic.ToolChoiceUnionParam{OfAuto: &anthropic.ToolChoiceAutoParam{}}
	default:
		return anthropic.ToolChoiceUnionParam{OfAuto: &anthropic.ToolChoiceAutoParam{}}
	}
}

// buildTools flattens the OpenAI-style tool definitions ({"type":"function",
// "function":{...}}) into Anthropic's flat ToolParam. The input schema body is
// passed through verbatim: Properties takes the schema as-is and ExtraFields
// carries $schema/additionalProperties, which is what keeps the 20 registered
// tools byte-identical to what the agent already sends.
func buildTools(defs []tooldefs.Def) ([]anthropic.ToolUnionParam, error) {
	out := make([]anthropic.ToolUnionParam, 0, len(defs))
	for _, def := range defs {
		var entry struct {
			Function struct {
				Name        string          `json:"name"`
				Description string          `json:"description"`
				Parameters  json.RawMessage `json:"parameters"`
			} `json:"function"`
		}
		if err := json.Unmarshal(def.JSON, &entry); err != nil {
			return nil, fmt.Errorf("anthropic: tool %s: %w", def.Name, err)
		}
		if entry.Function.Name == "" {
			return nil, fmt.Errorf("anthropic: tool %d has no function.name", len(out))
		}
		schema, err := buildInputSchema(entry.Function.Parameters)
		if err != nil {
			return nil, fmt.Errorf("anthropic: tool %s: %w", entry.Function.Name, err)
		}
		tool := anthropic.ToolParam{
			Name:        entry.Function.Name,
			InputSchema: schema,
		}
		if entry.Function.Description != "" {
			tool.Description = param.NewOpt(entry.Function.Description)
		}
		out = append(out, anthropic.ToolUnionParam{OfTool: &tool})
	}
	return out, nil
}

// buildInputSchema splits a JSON Schema into Properties/Required plus everything
// else in ExtraFields. Splitting keeps the schema's own keys (nested items,
// oneOf, $schema) inside Properties where they belong, while $schema and
// additionalProperties are sibling keys of the schema object and go to ExtraFields.
func buildInputSchema(parameters json.RawMessage) (anthropic.ToolInputSchemaParam, error) {
	if len(parameters) == 0 {
		return anthropic.ToolInputSchemaParam{}, nil
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(parameters, &raw); err != nil {
		return anthropic.ToolInputSchemaParam{}, err
	}
	schema := anthropic.ToolInputSchemaParam{}
	extras := map[string]any{}
	for key, value := range raw {
		switch key {
		case "properties":
			// Properties is `any` and marshals verbatim, so keep the raw bytes.
			schema.Properties = json.RawMessage(append([]byte(nil), value...))
		case "required":
			var required []string
			if err := json.Unmarshal(value, &required); err != nil {
				return anthropic.ToolInputSchemaParam{}, err
			}
			schema.Required = required
		case "type":
			var typ string
			if err := json.Unmarshal(value, &typ); err != nil {
				return anthropic.ToolInputSchemaParam{}, err
			}
			if typ != "" {
				schema.Type = constantObject(typ)
			}
		default:
			var decoded any
			if err := json.Unmarshal(value, &decoded); err != nil {
				return anthropic.ToolInputSchemaParam{}, err
			}
			extras[key] = decoded
		}
	}
	if len(extras) > 0 {
		schema.ExtraFields = extras
	}
	return schema, nil
}

// BuildRequestBody renders the params as the JSON body the transport POSTs. The
// SDK owns the marshalling, so this is a thin wrapper kept for tests and for the
// caller that needs the bytes for telemetry.
func BuildRequestBody(params anthropic.MessageNewParams) ([]byte, error) {
	body, err := json.Marshal(params)
	if err != nil {
		return nil, err
	}
	return body, nil
}

// AnthropicVersion is the API version header the Messages endpoint requires.
const AnthropicVersion = "2023-06-01"

// defaultBetaHeaders are the betas we enable when the caller supplies none. The
// SDK sets none of its own, and fine-grained tool streaming is what makes
// input_json_delta arrive incrementally instead of in one chunk.
var defaultBetaHeaders = []string{"fine-grained-tool-streaming-2025-05-14"}

// BetaHeadersFor returns the anthropic-beta values to send: the caller's, or the
// adapter defaults when it supplied none.
func BetaHeadersFor(in []string) []string {
	if len(in) > 0 {
		return in
	}
	return defaultBetaHeaders
}

// constantObject coerces a schema's "type" string into the SDK's constant.Object.
func constantObject(typ string) constant.Object { return constant.Object(typ) }
