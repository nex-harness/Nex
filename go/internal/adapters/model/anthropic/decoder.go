// Package anthropic implements the Anthropic Messages API adapter for the
// provider-neutral model client.
//
// The split follows spike R4 (docs/specs/go-rewrite/model-library-spike.md):
// framing stays in internal/modelclient (its SSE parser survives a proxy that
// omits or rewrites `event:` headers, which the SDK's Stream.Next cannot — it
// switches on `event:` against a hardcoded allow-list with no default branch).
// This package owns the dialect: it decodes each event body with
// anthropic-sdk-go's typed unions and emits modelclient's closed event set.
//
// The SDK is used for request construction (MessageNewParams), typed decoding
// (MessageStreamEventUnion) and error plumbing (apierror.Error), with
// WithMaxRetries(0) so the retry loop stays owned by internal/modelclient.
package anthropic

import (
	"encoding/json"
	"strings"

	"github.com/anthropics/anthropic-sdk-go"

	"github.com/tylinux/nex/go/internal/modelclient"
)

// Decoder turns Anthropic Messages SSE event bodies into the modelclient closed
// event set. It is fed whole event bodies (the SSE parser already did the
// framing), one event per call to Consume, plus Finish at EOF.
//
// Anthropic needs an index-keyed block state machine: a block's kind is known
// only at content_block_start, and every later content_block_delta/_stop for
// that block is positional. A delta for an unknown index is dropped rather than
// guessed, which is what keeps a proxy that interleaves blocks from producing
// a tool call whose id was never announced.
type Decoder struct {
	// blocks maps the content-block index to the open block's kind and, for
	// tool_use blocks, its id/name plus the accumulated argument string.
	blocks map[int64]*blockState
	// openToolBlocks is the count of tool_use blocks still streaming input, used
	// to order the finish flush after any unterminated tool call.
	messageID string
	usage     *modelclient.Usage
	// stopReason is the last raw Anthropic stop reason seen in message_delta,
	// mapped to the modelclient unified reason.
	stopReason string
	hasStop    bool
	// finished guards against a second Finish call.
	finished bool
	// errored records that an error event was emitted; a stream that errored has
	// no terminal finish, so Finish emits nothing.
	errored bool
}

type blockKind int

const (
	blockText blockKind = iota
	blockThinking
	blockToolUse
)

type blockState struct {
	kind blockKind
	// toolID/toolName are set for blockToolUse.
	toolID   string
	toolName string
	// input accumulates the input_json_delta partial JSON for blockToolUse.
	input strings.Builder
	// hasInput records whether any non-empty partial_json arrived.
	hasInput bool
}

// NewDecoder returns a decoder with no prior state.
func NewDecoder() *Decoder { return &Decoder{blocks: map[int64]*blockState{}} }

// Start returns the synthetic start event the stream always begins with.
func (d *Decoder) Start() modelclient.Event { return modelclient.Event{Kind: modelclient.EventStart} }

// Consume decodes one SSE event body and appends the events it produces.
//
// The branch is on the JSON "type" field, never the SSE "event:" header (R4), so
// a data-only stream decodes identically. Bodies that are not valid JSON, or whose
// "type" is not a known Messages event, are ignored — a proxy may inject
// keep-alives or vendor extensions, and the client is not the right place to fail
// a whole turn for them. A malformed body that claims to be a known type but fails
// to parse becomes a decode error event, matching the chat-completions decoder's
// "JSON parsing failed" behaviour.
func (d *Decoder) Consume(body string, out []modelclient.Event) ([]modelclient.Event, error) {
	if body == "" || body == "[DONE]" {
		return out, nil
	}
	var envelope struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal([]byte(body), &envelope); err != nil {
		return append(out, d.parseError(body, err)), nil
	}
	var event anthropic.MessageStreamEventUnion
	if err := json.Unmarshal([]byte(body), &event); err != nil {
		return append(out, d.parseError(body, err)), nil
	}

	// Dispatch on event.Type (the JSON discriminator), not on the SSE header.
	switch event.Type {
	case "message_start":
		return d.onMessageStart(event, out), nil
	case "content_block_start":
		return d.onContentBlockStart(event, out), nil
	case "content_block_delta":
		return d.onContentBlockDelta(event, out), nil
	case "content_block_stop":
		return d.onContentBlockStop(event, out), nil
	case "message_delta":
		return d.onMessageDelta(event, out), nil
	case "message_stop":
		// No modelclient event: the terminal finish is produced at Finish so the
		// unterminated tool-call flush can precede it.
		return out, nil
	case "error":
		d.errored = true
		return append(out, d.providerError(body)), nil
	case "ping":
		// ignored, matching the SDK and the AI SDK provider
		return out, nil
	default:
		// Unknown type (including message_delta variants we do not model and any
		// vendor extension): ignore rather than fail the stream.
		return out, nil
	}
}

func (d *Decoder) onMessageStart(event anthropic.MessageStreamEventUnion, out []modelclient.Event) []modelclient.Event {
	start, ok := event.AsAny().(anthropic.MessageStartEvent)
	if !ok {
		return out
	}
	d.messageID = start.Message.ID
	// message_start carries input_tokens; output_tokens here is only the initial
	// count and is superseded by the cumulative message_delta value.
	d.usage = mergeUsage(d.usage, anthropicUsage{
		inputTokens:              start.Message.Usage.InputTokens,
		outputTokens:             start.Message.Usage.OutputTokens,
		cacheReadInputTokens:     start.Message.Usage.CacheReadInputTokens,
		cacheCreationInputTokens: start.Message.Usage.CacheCreationInputTokens,
	}, false)
	return out
}

func (d *Decoder) onContentBlockStart(event anthropic.MessageStreamEventUnion, out []modelclient.Event) []modelclient.Event {
	start, ok := event.AsAny().(anthropic.ContentBlockStartEvent)
	if !ok {
		return out
	}
	block := start.ContentBlock
	state := &blockState{}
	switch block.Type {
	case "text":
		// Text blocks open here but the AI SDK only surfaces text-delta/text-end;
		// modelclient's text block is opened by its first delta, so nothing is
		// emitted at start.
		state.kind = blockText
	case "thinking", "redacted_thinking":
		state.kind = blockThinking
	case "tool_use":
		state.kind = blockToolUse
		state.toolID = block.ID
		state.toolName = block.Name
		// The SDK exposes the fully-typed ToolUseBlock via AsAny; fall back to the
		// flattened fields so a block the union does not cover still yields id+name.
		if toolUse, ok := block.AsAny().(anthropic.ToolUseBlock); ok {
			state.toolID = toolUse.ID
			state.toolName = toolUse.Name
		}
		// An Anthropic tool_use block may carry input directly when input streaming
		// is off. Seed the accumulator so those arguments are not lost.
		if block.Input != nil {
			if encoded, err := json.Marshal(block.Input); err == nil && string(encoded) != "{}" {
				state.input.Write(encoded)
				state.hasInput = true
			}
		}
	default:
		// server_tool_use, web_search_tool_result, etc.: not in our closed set.
		state.kind = blockText
	}
	d.blocks[start.Index] = state
	if state.kind != blockToolUse {
		return out
	}
	// A tool call's identity (id + name) is known here and nowhere else, so the
	// streaming input opens right here. The AI SDK provider does the same.
	return append(out, modelclient.Event{
		Kind:     modelclient.EventToolInputStart,
		ID:       state.toolID,
		ToolName: state.toolName,
	})
}

func (d *Decoder) onContentBlockDelta(event anthropic.MessageStreamEventUnion, out []modelclient.Event) []modelclient.Event {
	deltaEvent, ok := event.AsAny().(anthropic.ContentBlockDeltaEvent)
	if !ok {
		return out
	}
	delta := deltaEvent.Delta
	state := d.blocks[deltaEvent.Index]
	switch delta.Type {
	case "text_delta":
		out = append(out, modelclient.Event{Kind: modelclient.EventTextDelta, ID: textBlockID(deltaEvent.Index), Text: delta.Text})
	case "thinking_delta":
		out = append(out, modelclient.Event{Kind: modelclient.EventReasoningDelta, ID: reasoningBlockID(deltaEvent.Index), Text: delta.Thinking})
	case "input_json_delta":
		// The AI SDK provider skips empty partial_json deltas; mirroring that keeps
		// the tool-input lifecycle identical (an empty delta would still force the
		// assembler to treat input as started).
		if delta.PartialJSON == "" {
			return out
		}
		id := ""
		if state != nil {
			id = state.toolID
			state.input.WriteString(delta.PartialJSON)
			state.hasInput = true
		}
		out = append(out, modelclient.Event{Kind: modelclient.EventToolInputDelta, ID: id, Text: delta.PartialJSON})
	case "signature_delta":
		// Signature is not a visible delta; it rides along on the assistant
		// message's thinking block when replayed. No modelclient event.
		return out
	default:
		// citations_delta and anything else: not in our closed set.
		return out
	}
	return out
}

func (d *Decoder) onContentBlockStop(event anthropic.MessageStreamEventUnion, out []modelclient.Event) []modelclient.Event {
	stop, ok := event.AsAny().(anthropic.ContentBlockStopEvent)
	if !ok {
		return out
	}
	state := d.blocks[stop.Index]
	if state == nil || state.kind != blockToolUse {
		return out
	}
	// Close the tool call's streaming input; the final tool_call event is emitted
	// at Finish once the whole stream is accounted for.
	out = append(out, modelclient.Event{Kind: modelclient.EventToolInputEnd, ID: state.toolID})
	delete(d.blocks, stop.Index)
	return out
}

func (d *Decoder) onMessageDelta(event anthropic.MessageStreamEventUnion, out []modelclient.Event) []modelclient.Event {
	deltaEvent, ok := event.AsAny().(anthropic.MessageDeltaEvent)
	if !ok {
		return out
	}
	if stop := deltaEvent.Delta.StopReason; stop != "" {
		d.stopReason = MapStopReason(string(stop))
		d.hasStop = true
	}
	// message_delta usage is cumulative. input_tokens is present on some proxies
	// only in message_start; merge so whichever arrives wins, output_tokens is the
	// authoritative cumulative count.
	d.usage = mergeUsage(d.usage, anthropicUsage{
		inputTokens:              deltaEvent.Usage.InputTokens,
		outputTokens:             deltaEvent.Usage.OutputTokens,
		cacheReadInputTokens:     deltaEvent.Usage.CacheReadInputTokens,
		cacheCreationInputTokens: deltaEvent.Usage.CacheCreationInputTokens,
	}, true)
	out = append(out, modelclient.Event{Kind: modelclient.EventUsage, Usage: copyUsage(d.usage)})
	return out
}

// Finish flushes at EOF: it emits the tool_call for every still-open tool_use
// block, then the terminal finish event. It mirrors the chat-completions decoder's
// flush, which closes any unfinished tool call before the finish.
func (d *Decoder) Finish(out []modelclient.Event) ([]modelclient.Event, error) {
	if d.finished {
		return out, nil
	}
	d.finished = true
	if d.errored {
		// An errored stream has no completion to report.
		return out, nil
	}
	for _, index := range d.sortedBlockIndexes() {
		state := d.blocks[index]
		if state.kind != blockToolUse {
			continue
		}
		if _, open := findToolInputEnd(out, state.toolID); !open {
			out = append(out, modelclient.Event{Kind: modelclient.EventToolInputEnd, ID: state.toolID})
		}
		out = append(out, modelclient.Event{
			Kind: modelclient.EventToolCall,
			ToolCall: modelclient.ToolCall{
				ID:        state.toolID,
				Name:      state.toolName,
				Arguments: state.input.String(),
			},
		})
	}
	out = append(out, modelclient.Event{
		Kind:         modelclient.EventFinish,
		FinishReason: d.finishReason(),
		Usage:        copyUsage(d.usage),
	})
	return out, nil
}

// MessageID returns the message id observed in message_start, if any.
func (d *Decoder) MessageID() string { return d.messageID }

func (d *Decoder) finishReason() string {
	if !d.hasStop {
		// The Messages API always ends with message_stop after a message_delta, but
		// a truncated stream may not carry a stop reason. The chat decoder defaults
		// to "other" in the same situation.
		return modelclient.FinishOther
	}
	return d.stopReason
}
