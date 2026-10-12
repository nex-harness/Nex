package modelclient

// Decoder turns a byte stream of OpenAI chat-completions SSE data into the closed
// event set. It reproduces the @ai-sdk/openai-compatible doStream transform:
//
//   - the SDK emits a synthetic "start" event before reading any provider byte;
//   - each parsed `data:` payload is JSON-parsed against the chunk schema; a
//     payload that is not valid JSON, or fails the schema, becomes a stream error
//     ("JSON parsing failed" / "Type validation failed") whose text matches the
//     SDK, and terminates decoding with an *invalid_response* error;
//   - `[DONE]` is skipped;
//   - a top-level `error` field in the JSON becomes a provider error event;
//   - usage from any chunk replaces the running usage;
//   - the first choice's finish_reason sets the finish reason;
//   - delta.reasoning_content (or delta.reasoning) opens/closes the reasoning
//     block and emits reasoning deltas;
//   - delta.content closes reasoning, opens the text block and emits text deltas;
//   - delta.tool_calls are assembled by index, buffering id/name until both are
//     present, then emitting tool-input-start and tool-input deltas;
//   - at EOF the flush closes reasoning/text, throws on any tool call whose name
//     never arrived, emits tool-input-end for every unfinished tool call, and
//     emits the finish event.
type Decoder struct {
	parser *sseParser
	// textDecoder mirrors the SDK's TextDecoderStream: bytes are decoded to text
	// before the SSE parser, buffering incomplete multi-byte sequences.
	textDecoder textDecoder

	textStarted      bool
	reasoningStarted bool
	finish           string
	hasFinish        bool
	usage            *Usage

	// toolCalls is the sparse map the SDK keeps, keyed by index. A nil entry with
	// a pending record is tracked in pending; a materialized call is tracked in
	// calls.
	calls   map[int]*decToolCall
	pending map[int]*decPending
	// order preserves first-seen index order for flush (JS object property order
	// is insertion order for integer-like keys, which for indices is ascending
	// numeric order; we sort ascending at flush to match).
	maxIndex int

	rawProviderMetadata map[string]any

	done bool
}

type decToolCall struct {
	id          string
	name        string
	arguments   string
	hasFinished bool
	signature   string
	hasSig      bool
}

type decPending struct {
	id        string
	hasID     bool
	arguments string
	signature string
	hasSig    bool
}

// NewDecoder returns a decoder that will emit a leading start event.
func NewDecoder() *Decoder {
	return &Decoder{
		parser:  newSSEParser(),
		calls:   map[int]*decToolCall{},
		pending: map[int]*decPending{},
	}
}

// StreamDecoder turns one provider's raw response bytes into the closed event
// set. It is the seam a provider dialect plugs into: the chat-completions
// dialect (this package's Decoder) and the OpenAI Responses dialect
// (internal/adapters/model/openairesponses) both satisfy it, so the retry loop,
// the idle timeout, the assembler and the telemetry stay dialect-agnostic.
//
// The three methods are the ones stream.go already called on the chat decoder,
// so adding the interface changed no call site.
type StreamDecoder interface {
	// Start returns the synthetic stream-start event.
	Start() Event
	// Decode consumes the next bytes and appends decoded events to out.
	Decode(chunk []byte, out []Event) ([]Event, error)
	// Finish flushes the decoder at end of stream and appends the closing
	// events. It is called once, after the transport signals EOF.
	Finish(out []Event) ([]Event, error)
}

// DecoderFactory builds one decoder per attempt. A Client builds a fresh one for
// each physical request because a decoder carries per-stream state.
type DecoderFactory func() StreamDecoder

// chatDecoderFactory is the default: the chat-completions decoder.
func chatDecoderFactory() StreamDecoder { return NewDecoder() }

// decoderChunk is the subset of the OpenAI-compatible chunk schema the decoder
// reads. Unknown fields are tolerated (the schema is a loose object at the top
// level and on choices/delta).
// Start returns the synthetic start event and the initial provider start.
func (d *Decoder) Start() Event { return Event{Kind: EventStart} }

// Decode consumes the next bytes and appends decoded events to out. It returns
// out and any terminal error (only the flush-time invalid tool call is terminal,
// matching the SDK which throws InvalidResponseDataError from its flush).
func (d *Decoder) Decode(chunk []byte, out []Event) ([]Event, error) {
	if d.done {
		return out, nil
	}
	text := d.textDecoder.decode(chunk, false)
	if text == "" {
		return out, nil
	}
	events := d.parser.feed(text)
	for _, ev := range events {
		if ev.Data == "[DONE]" {
			continue
		}
		var err error
		out, err = d.consumeData(ev.Data, out)
		if err != nil {
			d.done = true
			return out, err
		}
	}
	return out, nil
}

// Finish flushes the decoder at EOF and returns the closing events. It must be
// called once after the transport signals end of stream.
func (d *Decoder) Finish(out []Event) ([]Event, error) {
	if d.done {
		return out, nil
	}
	d.done = true

	// Flush the text decoder's trailing partial sequence (becomes U+FFFD) and any
	// complete lines the SSE parser still holds.
	if tail := d.textDecoder.decode(nil, true); tail != "" {
		for _, ev := range d.parser.feed(tail) {
			if ev.Data == "[DONE]" {
				continue
			}
			var err error
			out, err = d.consumeData(ev.Data, out)
			if err != nil {
				return out, err
			}
		}
	}

	if d.reasoningStarted {
		out = append(out, Event{Kind: EventReasoningEnd, ID: ReasoningBlockID})
		d.reasoningStarted = false
	}
	if d.textStarted {
		out = append(out, Event{Kind: EventTextEnd, ID: TextBlockID})
		d.textStarted = false
	}

	// flush: any pending tool call whose name never arrived is a hard error
	if len(d.pending) > 0 {
		idx := d.minPendingIndex()
		p := d.pending[idx]
		return out, &DecodeError{
			Kind:    ErrInvalidResponse,
			Message: "Expected 'function.name' to be a string.",
			Data: map[string]any{
				"index": idx,
				"id":    nullOrString(p.id, p.hasID),
				"function": map[string]any{
					"arguments": p.arguments,
				},
			},
		}
	}

	for _, idx := range d.sortedIndices() {
		tc := d.calls[idx]
		if tc == nil || tc.hasFinished {
			continue
		}
		out = append(out, Event{Kind: EventToolInputEnd, ID: tc.id})
		out = append(out, Event{
			Kind: EventToolCall,
			ToolCall: ToolCall{
				ID:        tc.id,
				Name:      tc.name,
				Arguments: tc.arguments,
			},
		})
		tc.hasFinished = true
	}

	if !d.hasFinish {
		d.finish = FinishOther
		d.hasFinish = true
	}
	out = append(out, Event{
		Kind:         EventFinish,
		FinishReason: d.finish,
		Usage:        d.convertedUsage(),
	})
	return out, nil
}

func (d *Decoder) sortedIndices() []int {
	idx := make([]int, 0, len(d.calls))
	for k := range d.calls {
		idx = append(idx, k)
	}
	// insertion order for JS integer-like keys is ascending; sort to match.
	sortInts(idx)
	return idx
}

func (d *Decoder) minPendingIndex() int {
	min := -1
	for k := range d.pending {
		if min == -1 || k < min {
			min = k
		}
	}
	return min
}

// convertedUsage mirrors convertUsage in the provider: it maps provider usage to
// the SDK's LanguageModelUsage, keeping the raw usage and prediction token
// details in provider metadata. We keep the raw fields directly.
func (d *Decoder) convertedUsage() *Usage {
	return d.usage
}

func convertUsage(u *decoderUsage) *Usage {
	if u == nil {
		return nil
	}
	out := &Usage{
		PromptTokens:     u.PromptTokens,
		CompletionTokens: u.CompletionTokens,
		TotalTokens:      u.TotalTokens,
	}
	if u.PromptTokensDetails != nil {
		out.CachedTokens = u.PromptTokensDetails.CachedTokens
	}
	if u.CompletionTokensDetails != nil {
		out.ReasoningTokens = u.CompletionTokensDetails.ReasoningTokens
		out.AcceptedPrediction = u.CompletionTokensDetails.AcceptedPredictionTokens
		out.RejectedPrediction = u.CompletionTokensDetails.RejectedPredictionTokens
		out.HasReasoningDetails = true
	}
	return out
}

func mapFinishReason(raw string) string {
	switch raw {
	case "stop":
		return FinishStop
	case "length":
		return FinishLength
	case "function_call", "tool_calls":
		return FinishToolCalls
	case "content_filter":
		return FinishContentFilter
	default:
		return FinishOther
	}
}

func sortInts(a []int) {
	for i := 1; i < len(a); i++ {
		for j := i; j > 0 && a[j-1] > a[j]; j-- {
			a[j-1], a[j] = a[j], a[j-1]
		}
	}
}
