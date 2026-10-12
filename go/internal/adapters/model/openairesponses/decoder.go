package openairesponses

// Decoding of the OpenAI Responses SSE dialect into modelclient's closed event
// set.
//
// The vendor SDK decodes each event body into responses.ResponseStreamEventUnion
// (a flattened union discriminated by its `type` field), which is exactly what
// we want. What we do NOT use is its stream wrapper, for the two reasons the
// spike measured:
//
//   - R1: ssestream.NewStream checks gjson for a top-level "error" key on every
//     event and aborts when it is present. gjson reports an explicit
//     `"error": null` as present, and every successful Responses payload carries
//     `response.error = null`; a proxy that echoes the object at the top level
//     kills the stream on the first delta. Framing the bytes ourselves and
//     ignoring a null error is the fix.
//   - R4: the vendor decoder switches on the SSE `event:` header. Proxies drop
//     that header, so we branch on the JSON body's own `type` field and treat
//     `event:` as decoration (it is still surfaced on SSEEvent for logging).
//
// Block ids follow the provider, not our chat-completions convention: text
// blocks are keyed by the message item id and reasoning blocks by
// "{item_id}:{summary_index}", exactly as @ai-sdk/openai does, because those ids
// appear in the recorded traces and in the v4 projection.

import (
	"encoding/json"

	"github.com/openai/openai-go/responses"

	"github.com/tylinux/nex/go/internal/modelclient"
)

// Decoder turns a framed OpenAI Responses SSE body into modelclient events.
type Decoder struct {
	// framer is the SSE framing layer. It is modelclient's, deliberately: see the
	// package comment on R1/R4.
	framer *modelclient.SSEFramer

	// textStarted / reasoningStarted track the open block so EOF (or the
	// matching *-done event) can close it exactly once.
	textStarted      bool
	textID           string
	reasoningStarted bool
	reasoningID      string
	finish           string
	hasFinish        bool
	usage            *modelclient.Usage
	done             bool

	// calls indexes open function calls by the Responses *item* id, because the
	// argument deltas address the item (`item_id`) while the start event carries
	// both `item.id` and `item.call_id`. The wire-visible tool-call id is the
	// call_id: that is what a function_call_output must echo back.
	calls  map[string]*callState
	closed map[string]bool
}

// callState is one in-flight function call.
type callState struct {
	callID string
	name   string
}

// NewDecoder returns a decoder for one Responses stream.
func NewDecoder() *Decoder {
	return &Decoder{
		framer: modelclient.NewSSEFramer(),
		calls:  map[string]*callState{},
		closed: map[string]bool{},
	}
}

// DecoderFactory builds one Responses decoder per attempt, satisfying the
// modelclient.Config.NewDecoder seam.
func DecoderFactory() modelclient.DecoderFactory {
	return func() modelclient.StreamDecoder { return NewDecoder() }
}

// Start returns the synthetic stream-start event, matching what the chat
// decoder emits so the assembler and the runtime see one shape.
func (d *Decoder) Start() modelclient.Event { return modelclient.Event{Kind: modelclient.EventStart} }

// Decode consumes the next bytes of the Responses SSE stream and appends the
// events they produce to out. It implements modelclient.StreamDecoder; chunks may
// split at any byte boundary.
func (d *Decoder) Decode(chunk []byte, out []modelclient.Event) ([]modelclient.Event, error) {
	if d.done {
		return out, nil
	}
	for _, ev := range d.framer.Feed(chunk) {
		var err error
		out, err = d.decodeBody(ev.Data, out)
		if err != nil {
			d.done = true
			return out, err
		}
	}
	return out, nil
}

// Finish flushes the decoder at end of stream. R3: a stream that ends without
// [DONE] and without a terminal event must still produce a finish, so flush is
// unconditional and the finish reason it uses says the provider never reported
// one.
func (d *Decoder) Finish(out []modelclient.Event) ([]modelclient.Event, error) {
	if d.done {
		return out, nil
	}
	d.done = true
	for _, ev := range d.framer.Flush() {
		var err error
		out, err = d.decodeBody(ev.Data, out)
		if err != nil {
			return out, err
		}
	}
	return d.flush(out, true)
}

// decodeBody consumes one already-framed SSE event body.
//
// R1: a proxy that echoes the Responses envelope emits a top-level
// `"error": null` on every event. Only a *non-null* error is an error; the null
// is filtered out here, before the body reaches the SDK decoder, which would
// treat the key's mere presence as fatal.
func (d *Decoder) decodeBody(body string, out []modelclient.Event) ([]modelclient.Event, error) {
	if body == "" || body == "[DONE]" {
		return out, nil
	}
	if raw, ok := topLevelError(body); ok {
		return append(out, modelclient.Event{
			Kind: modelclient.EventError,
			Err: &modelclient.StreamError{
				Message: errorMessage(raw),
				Raw:     raw,
			},
		}), nil
	}

	var ev responses.ResponseStreamEventUnion
	if err := json.Unmarshal([]byte(body), &ev); err != nil {
		out = append(out, modelclient.Event{
			Kind: modelclient.EventError,
			Err: &modelclient.DecodeError{
				Kind:    modelclient.ErrInvalidResponse,
				Message: "Type validation failed: " + err.Error(),
				Cause:   err,
			},
		})
		return out, nil
	}
	return d.consume(ev, out)
}

// consume maps one decoded Responses event onto the closed event set.
func (d *Decoder) consume(ev responses.ResponseStreamEventUnion, out []modelclient.Event) ([]modelclient.Event, error) {
	switch ev.Type {
	case "response.output_text.delta":
		if ev.Delta.OfString == "" {
			return out, nil
		}
		if !d.textStarted {
			out = append(out, modelclient.Event{Kind: modelclient.EventTextStart, ID: ev.ItemID})
			d.textStarted, d.textID = true, ev.ItemID
		}
		return append(out, modelclient.Event{Kind: modelclient.EventTextDelta, ID: ev.ItemID, Text: ev.Delta.OfString}), nil

	case "response.output_item.added":
		return d.outputItemAdded(ev, out)

	case "response.output_item.done":
		return d.outputItemDone(ev, out)

	case "response.function_call_arguments.delta":
		// The delta addresses the item, but the wire-visible id is the call_id the
		// matching output_item.added registered. When the item was never seen (a
		// proxy that forwards only the argument events), fall back to the item id
		// so the delta is still attributable instead of silently dropped.
		id := d.resolveCallID(ev.ItemID)
		return append(out, modelclient.Event{Kind: modelclient.EventToolInputDelta, ID: id, Text: ev.Delta.OfString}), nil

	case "response.reasoning_summary_part.added":
		return d.reasoningStart(ev.ItemID, ev.SummaryIndex), nil

	case "response.reasoning_summary_text.delta":
		out = d.reasoningStart(ev.ItemID, ev.SummaryIndex)
		return append(out, modelclient.Event{
			Kind: modelclient.EventReasoningDelta,
			ID:   reasoningBlockID(ev.ItemID, ev.SummaryIndex),
			Text: ev.Delta.OfString,
		}), nil

	case "response.reasoning_summary_part.done":
		// The matching reasoning-end is emitted on output_item.done for the
		// reasoning item, so a part done without one does not close the block.
		return out, nil

	case "response.completed", "response.incomplete":
		d.finish = mapFinishReason(ev.Response.Status, ev.Response.IncompleteDetails.Reason, d.hasFunctionCall())
		d.hasFinish = true
		if u := convertUsage(ev.Response.Usage); u != nil {
			d.usage = u
		}
		if ev.Type == "response.incomplete" && ev.Response.IncompleteDetails.Reason != "" {
			// An incomplete response is not a stream error; the reason becomes the
			// finish reason (max_output_tokens -> length) and the caller decides
			// whether to continue.
			return out, nil
		}
		return out, nil

	case "response.failed":
		d.finish = modelclient.FinishError
		d.hasFinish = true
		if u := convertUsage(ev.Response.Usage); u != nil {
			d.usage = u
		}
		msg := ev.Response.Error.Message
		if msg == "" {
			msg = "OpenAI Responses stream failed."
		}
		code := string(ev.Response.Error.Code)
		if code == "" {
			code = string(ev.Response.Status)
		}
		return append(out, modelclient.Event{
			Kind: modelclient.EventError,
			Err: &modelclient.StreamError{
				Message: code + ": " + msg,
				Raw:     json.RawMessage(ev.RawJSON()),
			},
		}), nil

	case "error":
		return append(out, modelclient.Event{
			Kind: modelclient.EventError,
			Err: &modelclient.StreamError{
				Message: string(ev.Code) + ": " + ev.Message,
				Raw:     json.RawMessage(ev.RawJSON()),
			},
		}), nil

	default:
		// response.created, response.in_progress, response.queued,
		// response.content_part.*, annotation events, and anything a newer
		// provider adds: not part of the closed set, so they are skipped rather
		// than surfaced as noise.
		return out, nil
	}
}

// outputItemAdded opens the blocks the provider announced.
func (d *Decoder) outputItemAdded(ev responses.ResponseStreamEventUnion, out []modelclient.Event) ([]modelclient.Event, error) {
	switch ev.Item.Type {
	case "function_call":
		callID := ev.Item.CallID
		if callID == "" {
			callID = ev.Item.ID
		}
		d.calls[ev.Item.ID] = &callState{callID: callID, name: ev.Item.Name}
		out = append(out, modelclient.Event{
			Kind:     modelclient.EventToolInputStart,
			ID:       callID,
			ToolName: ev.Item.Name,
		})
		// The provider may pre-fill arguments on the added item; stream them so
		// the visible input matches the delta stream.
		if ev.Item.Arguments != "" {
			out = append(out, modelclient.Event{
				Kind: modelclient.EventToolInputDelta,
				ID:   callID,
				Text: ev.Item.Arguments,
			})
		}
		return out, nil
	case "message":
		// The provider opens the text block on the item, but @ai-sdk/openai only
		// emits text-start once a delta arrives; we match that so an empty
		// message produces no phantom block.
		return out, nil
	case "reasoning":
		return d.reasoningStart(ev.Item.ID, 0), nil
	}
	return out, nil
}

// outputItemDone closes the blocks the provider finished and emits the finished
// tool call.
func (d *Decoder) outputItemDone(ev responses.ResponseStreamEventUnion, out []modelclient.Event) ([]modelclient.Event, error) {
	switch ev.Item.Type {
	case "function_call":
		callID := ev.Item.CallID
		if callID == "" {
			callID = ev.Item.ID
		}
		if !d.closed[ev.Item.ID] {
			d.closed[ev.Item.ID] = true
			delete(d.calls, ev.Item.ID)
			out = append(out, modelclient.Event{Kind: modelclient.EventToolInputEnd, ID: callID})
		}
		out = append(out, modelclient.Event{
			Kind: modelclient.EventToolCall,
			ToolCall: modelclient.ToolCall{
				ID:        callID,
				Name:      ev.Item.Name,
				Arguments: ev.Item.Arguments,
			},
		})
		return out, nil
	case "message":
		if d.textStarted && (ev.Item.ID == d.textID || d.textID == "") {
			out = append(out, modelclient.Event{Kind: modelclient.EventTextEnd, ID: d.textID})
			d.textStarted, d.textID = false, ""
		}
		return out, nil
	case "reasoning":
		out = d.reasoningEnd(out)
		return out, nil
	}
	return out, nil
}

// reasoningStart opens the reasoning block for one summary part, once.
func (d *Decoder) reasoningStart(itemID string, summaryIndex int64) []modelclient.Event {
	id := reasoningBlockID(itemID, summaryIndex)
	if d.reasoningStarted && d.reasoningID == id {
		return nil
	}
	d.reasoningStarted, d.reasoningID = true, id
	return []modelclient.Event{{Kind: modelclient.EventReasoningStart, ID: id}}
}

// reasoningEnd closes the open reasoning block, once.
func (d *Decoder) reasoningEnd(out []modelclient.Event) []modelclient.Event {
	if !d.reasoningStarted {
		return out
	}
	d.reasoningStarted, d.reasoningID = false, ""
	return append(out, modelclient.Event{Kind: modelclient.EventReasoningEnd, ID: ""})
}

// resolveCallID maps an item id onto the call_id the wire-visible events use.
func (d *Decoder) resolveCallID(itemID string) string {
	if call, ok := d.calls[itemID]; ok && call.callID != "" {
		return call.callID
	}
	return itemID
}

func (d *Decoder) hasFunctionCall() bool { return len(d.calls) > 0 || len(d.closed) > 0 }

// flush closes every open block and emits the terminal finish event. It is
// idempotent and is called on EOF (R3: a stream that ends without a terminal
// event and without [DONE] must still produce a finish, and the caller can see
// that it was a clean flush rather than a provider-reported status).
func (d *Decoder) flush(out []modelclient.Event, done bool) ([]modelclient.Event, error) {
	if !done {
		return out, nil
	}
	out = d.reasoningEnd(out)
	if d.textStarted {
		out = append(out, modelclient.Event{Kind: modelclient.EventTextEnd, ID: d.textID})
		d.textStarted, d.textID = false, ""
	}
	// A function call the provider opened but never closed (truncated stream) is
	// still surfaced, so the caller sees the pending call instead of a silent
	// drop; the assembler normalizes it like any other call.
	for _, call := range d.calls {
		out = append(out, modelclient.Event{Kind: modelclient.EventToolInputEnd, ID: call.callID})
		out = append(out, modelclient.Event{
			Kind: modelclient.EventToolCall,
			ToolCall: modelclient.ToolCall{
				ID:        call.callID,
				Name:      call.name,
				Arguments: "{}",
			},
		})
	}
	d.calls = map[string]*callState{}
	if !d.hasFinish {
		d.finish = modelclient.FinishOther
		d.hasFinish = true
	}
	return append(out, modelclient.Event{
		Kind:         modelclient.EventFinish,
		FinishReason: d.finish,
		Usage:        d.usage,
	}), nil
}

// reasoningBlockID is the reasoning block id @ai-sdk/openai uses: the reasoning
// item id and its summary index joined by a colon.
func reasoningBlockID(itemID string, summaryIndex int64) string {
	return itemID + ":" + itoa64(summaryIndex)
}
