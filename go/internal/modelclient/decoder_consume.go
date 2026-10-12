package modelclient

import (
	"encoding/json"
	"strings"
)

func (d *Decoder) consumeData(data string, out []Event) ([]Event, error) {
	var raw map[string]json.RawMessage
	dec := json.NewDecoder(strings.NewReader(data))
	dec.UseNumber()
	if err := dec.Decode(&raw); err != nil {
		// The SDK surfaces a JSON parse failure as an error part and sets the
		// finish reason to "error"; the stream continues to its flush.
		d.finish = FinishError
		d.hasFinish = true
		return append(out, Event{
			Kind: EventError,
			Err: &DecodeError{
				Kind:    ErrInvalidResponse,
				Message: "JSON parsing failed: " + jsonParseMessage(err),
				Cause:   err,
			},
		}), nil
	}
	var chunk decoderChunk
	if err := json.Unmarshal([]byte(data), &chunk); err != nil {
		d.finish = FinishError
		d.hasFinish = true
		return append(out, Event{
			Kind: EventError,
			Err: &DecodeError{
				Kind:    ErrInvalidResponse,
				Message: "Type validation failed: " + err.Error(),
				Cause:   err,
			},
		}), nil
	}

	if chunk.Error != nil {
		// The SDK forwards the parsed error message as an error part; @nex then
		// re-detects the provider business error from the raw chunk.
		d.finish = FinishError
		d.hasFinish = true
		return append(out, Event{
			Kind: EventError,
			Err: &StreamError{
				Message: chunk.Error.Message,
				Raw:     json.RawMessage(data),
			},
		}), nil
	}

	// first chunk emits response metadata; not part of the closed event set other
	// than being observable, so we simply do not surface it.
	if chunk.Usage != nil {
		d.usage = convertUsage(chunk.Usage)
	}

	var choice *decoderChoice
	if len(chunk.Choices) > 0 {
		choice = &chunk.Choices[0]
	}
	if choice != nil && choice.FinishReason != nil {
		d.finish = mapFinishReason(*choice.FinishReason)
		d.hasFinish = true
	}
	if choice == nil || choice.Delta == nil {
		return out, nil
	}
	delta := choice.Delta

	reasoning := delta.ReasoningContent
	if reasoning == nil {
		reasoning = delta.Reasoning
	}
	if reasoning != nil && *reasoning != "" {
		if !d.reasoningStarted {
			out = append(out, Event{Kind: EventReasoningStart, ID: ReasoningBlockID})
			d.reasoningStarted = true
		}
		out = append(out, Event{Kind: EventReasoningDelta, ID: ReasoningBlockID, Text: *reasoning})
	}

	if delta.Content != nil && *delta.Content != "" {
		if d.reasoningStarted {
			out = append(out, Event{Kind: EventReasoningEnd, ID: ReasoningBlockID})
			d.reasoningStarted = false
		}
		if !d.textStarted {
			out = append(out, Event{Kind: EventTextStart, ID: TextBlockID})
			d.textStarted = true
		}
		out = append(out, Event{Kind: EventTextDelta, ID: TextBlockID, Text: *delta.Content})
	}

	if delta.ToolCalls != nil {
		if d.reasoningStarted {
			out = append(out, Event{Kind: EventReasoningEnd, ID: ReasoningBlockID})
			d.reasoningStarted = false
		}
		var err error
		out, err = d.consumeToolCallDeltas(delta.ToolCalls, out)
		if err != nil {
			return out, err
		}
	}
	return out, nil
}

func thoughtSignature(tcd decoderToolDelta) *string {
	if tcd.ExtraContent == nil || tcd.ExtraContent.Google == nil {
		return nil
	}
	return tcd.ExtraContent.Google.ThoughtSignature
}

func nullOrString(s string, has bool) any {
	if !has || s == "" {
		return nil
	}
	return s
}

func stringOr(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

func mustJSON(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return "{}"
	}
	return string(b)
}

// jsonParseMessage approximates the SDK's JSONParseError message so the Go error
// text mirrors what the Node stack produces ("JSON parsing failed: ...").
func jsonParseMessage(err error) string {
	msg := err.Error()
	// encoding/json and JSON.parse phrase offsets differently; keep the raw text
	// after the well-known prefixes for stable substring matching.
	return msg
}

// sortInts is a small insertion sort; index lists are tiny (tool-call count).
