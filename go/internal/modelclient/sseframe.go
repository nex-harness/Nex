package modelclient

// This file exposes the SSE framing layer (sse.go) and the streaming UTF-8
// decoder (textdecode.go) to adapters that speak a different provider dialect
// but must produce the identical byte-level framing behaviour.
//
// Why it exists: D14 adopted github.com/openai/openai-go for the OpenAI Responses
// API, but the spike's risks R1 and R4 both require that the SSE framing stay
// ours:
//
//   - R1: openai-go's ssestream aborts the stream the moment gjson reports a
//     top-level "error" key, and gjson reports an explicit `"error": null` as
//     present. Every successful Responses payload carries `response.error = null`,
//     so the vendor stream dies on the first delta of every response.
//   - R4: the vendor decoder allow-lists SSE event names and switches on the
//     `event:` header; proxies that forward only `data:` lines lose every event.
//
// So the vendor SDK is used for request construction and typed per-event
// decoding, and this file hands it the already-framed event bodies.
//
// Nothing here changes the behaviour of the chat-completions decoder: the
// existing Decoder still uses sseParser and textDecoder directly, and this is
// the same code path, not a reimplementation.

// SSEEvent is one dispatched SSE event: the accumulated `data` payload plus the
// `event:` name and last `id:` when the stream carried them.
//
// Adapters MUST NOT branch on Event (see R4); the JSON body's own `type` field is
// the discriminator. Event is carried only so a caller can log or assert on it.
type SSEEvent struct {
	// Data is the joined `data:` lines of one event (multi-line values join with
	// "\n" per the SSE spec).
	Data string
	// Event is the SSE `event:` name, empty when the stream omitted it.
	Event string
	// ID is the last `id:` seen before this event, empty when none was sent.
	ID string
}

// SSEFramer frames a provider's SSE byte stream into events. It is the single
// owner of chunk-boundary handling: bytes are UTF-8 decoded with a streaming
// decoder (a multi-byte sequence split across a read boundary is buffered, not
// mangled), the leading BOM is stripped from the first chunk, and CR, LF and
// CRLF all terminate a line.
type SSEFramer struct {
	parser *sseParser
	// text mirrors the SDK's TextDecoderStream: bytes become text before framing.
	text textDecoder
}

// NewSSEFramer returns a framer for one provider stream.
func NewSSEFramer() *SSEFramer {
	return &SSEFramer{parser: newSSEParser()}
}

// Feed consumes the next bytes of the stream and returns the events they
// completed. Chunks may split at any byte boundary, including inside a
// multi-byte UTF-8 sequence.
func (f *SSEFramer) Feed(chunk []byte) []SSEEvent {
	text := f.text.decode(chunk, false)
	if text == "" {
		return nil
	}
	return f.frame(text)
}

// Flush drains the decoder's trailing partial sequence (which becomes U+FFFD,
// as the WHATWG decoder does) and any complete line the parser still holds.
// Call it once, after the transport signals end of stream.
func (f *SSEFramer) Flush() []SSEEvent {
	tail := f.text.decode(nil, true)
	if tail == "" {
		return nil
	}
	return f.frame(tail)
}

func (f *SSEFramer) frame(text string) []SSEEvent {
	raw := f.parser.feed(text)
	if len(raw) == 0 {
		return nil
	}
	out := make([]SSEEvent, len(raw))
	for i, ev := range raw {
		out[i] = SSEEvent{Data: ev.Data, Event: ev.Event, ID: ev.ID}
	}
	return out
}
