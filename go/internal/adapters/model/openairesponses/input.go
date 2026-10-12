package openairesponses

// The Responses `input` array has no single message shape: an assistant turn
// that carried tool calls becomes two separate items (the message and one
// function_call per call), and a tool result is its own function_call_output
// item rather than a "tool" role message. buildInput performs that projection
// from the chat-completions conversation modelreq.Message already produces.

import (
	"encoding/json"
	"fmt"

	"github.com/openai/openai-go/packages/param"
	"github.com/openai/openai-go/responses"
	"github.com/openai/openai-go/shared/constant"
)

// buildInput projects a chat-completions conversation onto Responses input items.
func buildInput(msgs []Message) (responses.ResponseInputParam, error) {
	items := make(responses.ResponseInputParam, 0, len(msgs))
	for i, m := range msgs {
		switch {
		case m.ToolCallID != "":
			// chat-completions: {"role":"tool","tool_call_id":…,"content":…}
			// Responses:    {"type":"function_call_output","call_id":…,"output":…}
			items = append(items, responses.ResponseInputItemUnionParam{
				OfFunctionCallOutput: &responses.ResponseInputItemFunctionCallOutputParam{
					Type:   constant.FunctionCallOutput("function_call_output"),
					CallID: m.ToolCallID,
					Output: m.Content,
				},
			})
		case len(m.ToolCalls) > 0:
			// An assistant turn's tool_calls fan out into one function_call item
			// per call. Any text on the same message becomes an assistant message
			// item first, matching what the provider sends back on output.
			items = append(items, messageItems(m)...)
			calls, err := functionCallItems(m.ToolCalls)
			if err != nil {
				return nil, fmt.Errorf("message %d: %w", i, err)
			}
			items = append(items, calls...)
		default:
			items = append(items, messageItems(m)...)
		}
	}
	return items, nil
}

// messageItems renders one plain message. System/developer roles carry the
// out-of-band `instructions` field instead of being repeated as input items.
func messageItems(m Message) []responses.ResponseInputItemUnionParam {
	if m.Role == "system" || m.Role == "developer" {
		return nil
	}
	if m.Content == "" {
		return nil
	}
	return []responses.ResponseInputItemUnionParam{{
		OfMessage: &responses.EasyInputMessageParam{
			Type: responses.EasyInputMessageTypeMessage,
			Role: responses.EasyInputMessageRole(m.Role),
			// A string content marshals as `"content": "…"`; the content-list form
			// would add an input_text wrapper the provider does not need here.
			Content: responses.EasyInputMessageContentUnionParam{OfString: param.NewOpt(m.Content)},
		},
	}}
}

// functionCallItems converts a chat-completions tool_calls array into Responses
// function_call items. The raw JSON is decoded, never re-encoded through a
// schema type, so `arguments` reaches the wire byte-identical.
func functionCallItems(raw json.RawMessage) ([]responses.ResponseInputItemUnionParam, error) {
	var calls []struct {
		ID       string `json:"id"`
		Function struct {
			Name      string `json:"name"`
			Arguments string `json:"arguments"`
		} `json:"function"`
	}
	if err := json.Unmarshal(raw, &calls); err != nil {
		return nil, err
	}
	out := make([]responses.ResponseInputItemUnionParam, 0, len(calls))
	for i, c := range calls {
		if c.Function.Name == "" {
			return nil, fmt.Errorf("tool call %d has no function.name", i)
		}
		item := responses.ResponseFunctionToolCallParam{
			Type:      constant.FunctionCall("function_call"),
			Name:      c.Function.Name,
			Arguments: c.Function.Arguments,
		}
		// Responses keys a tool result off call_id; chat-completions names the
		// same field id, so it carries over unchanged.
		item.CallID = c.ID
		out = append(out, responses.ResponseInputItemUnionParam{OfFunctionCall: &item})
	}
	return out, nil
}
