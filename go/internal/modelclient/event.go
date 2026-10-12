// Package modelclient holds the pure logic of the OpenAI chat-completions model
// client: the SSE parser, the chunk decoder that produces a closed event set, the
// failure classifier, and the retry policy. Nothing here performs network or file
// I/O; transport lives in internal/adapters/model/openai behind a small interface.
//
// The behaviour reproduced here is the Vercel `ai` SDK (v6) plus
// @ai-sdk/openai-compatible (v2) plus eventsource-parser, because that is the
// HTTP/SSE layer the Node agent actually delegates to. Those three are the parity
// oracle for parsing and for what counts as an error; the agent's own retry loop
// (runner-stream.ts / runner-generate.ts) is reproduced on top.
package modelclient

// EventKind enumerates the closed set of decoded stream events. It mirrors the
// ModelStreamEvent union in @nex/contracts, restricted to what the OpenAI
// chat-completions decoder can produce plus stream errors.
type EventKind string

const (
	// EventStart is the synthetic stream start the SDK emits before any provider
	// byte is read.
	EventStart EventKind = "start"
	// EventTextStart opens a text block. The OpenAI-compatible provider always
	// uses the id "txt-0" and only opens one text block per response.
	EventTextStart EventKind = "text_start"
	// EventTextDelta carries one text increment.
	EventTextDelta EventKind = "text_delta"
	// EventTextEnd closes the text block.
	EventTextEnd EventKind = "text_end"
	// EventReasoningStart opens the reasoning block (id "reasoning-0").
	EventReasoningStart EventKind = "reasoning_start"
	// EventReasoningDelta carries one reasoning increment.
	EventReasoningDelta EventKind = "reasoning_delta"
	// EventReasoningEnd closes the reasoning block.
	EventReasoningEnd EventKind = "reasoning_end"
	// EventToolInputStart opens a tool call after its id and name are known.
	EventToolInputStart EventKind = "tool_input_start"
	// EventToolInputDelta carries an increment of the raw JSON argument string.
	EventToolInputDelta EventKind = "tool_input_delta"
	// EventToolInputEnd closes a tool call's input; the final argument string is
	// complete but not yet parsed.
	EventToolInputEnd EventKind = "tool_input_end"
	// EventToolCall is the finished, validated tool call.
	EventToolCall EventKind = "tool_call"
	// EventUsage carries provider token usage (may arrive alone in a final chunk).
	EventUsage EventKind = "usage"
	// EventFinish closes the stream with a finish reason.
	EventFinish EventKind = "finish"
	// EventError is a stream error produced by the decoder (malformed chunk,
	// invalid tool-call shape, provider error object).
	EventError EventKind = "error"
)

// Finish reason values, normalized exactly as mapOpenAICompatibleFinishReason
// does: raw stop/length/content_filter/function_call/tool_calls are mapped to the
// AI SDK's unified reasons; everything else becomes "other". The AI SDK then
// surfaces these unified strings, and toModelStreamEvent copies them verbatim.
const (
	FinishStop          = "stop"
	FinishLength        = "length"
	FinishToolCalls     = "tool-calls"
	FinishContentFilter = "content-filter"
	FinishOther         = "other"
	FinishError         = "error"
)

// TextBlockID and ReasoningBlockID are the fixed ids the OpenAI-compatible
// provider assigns to the single text/reasoning block it emits.
const (
	TextBlockID      = "txt-0"
	ReasoningBlockID = "reasoning-0"
)

// ToolCall is a finished tool call. Before normalization it carries the raw JSON
// argument string in Arguments; after the assembler processes it, Input holds the
// parsed value and HasInput is true.
type ToolCall struct {
	ID   string
	Name string
	// Arguments is the raw JSON string accumulated from deltas.
	Arguments string
	// Input is the parsed/validated tool input. HasInput is true after the
	// assembler has normalized it (a JSON literal null becomes an empty object).
	Input    any
	HasInput bool
	// ProviderExecuted mirrors the ModelToolCall field. HasProviderExecuted
	// distinguishes an explicit false from "absent".
	ProviderExecuted    bool
	HasProviderExecuted bool
}

// Usage is provider token usage. Pointer fields distinguish "absent" from zero,
// matching the SDK schema which allows nullish numbers.
type Usage struct {
	PromptTokens        *int64
	CompletionTokens    *int64
	TotalTokens         *int64
	ReasoningTokens     *int64
	CachedTokens        *int64
	AcceptedPrediction  *int64
	RejectedPrediction  *int64
	HasReasoningDetails bool
}

// Event is one decoded stream event. Field usage depends on Kind:
//
//   - Start: no fields.
//   - TextStart/TextEnd: ID.
//   - TextDelta: ID, Text, HasProviderMetadata+ProviderMetadata.
//   - ReasoningStart/ReasoningEnd: ID, HasProviderMetadata+ProviderMetadata.
//   - ReasoningDelta: ID, Text, HasProviderMetadata+ProviderMetadata.
//   - ToolInputStart: ID, ToolName.
//   - ToolInputDelta: ID, Text (the raw argument increment).
//   - ToolInputEnd: ID.
//   - ToolCall: ToolCall.
//   - Usage: Usage.
//   - Finish: FinishReason, Usage.
//   - Error: Err.
type Event struct {
	Kind         EventKind
	ID           string
	Text         string
	ToolName     string
	ToolCall     ToolCall
	Usage        *Usage
	FinishReason string
	Err          error

	// HasProviderMetadata reports whether ProviderMetadata is meaningful; the SDK
	// emits providerMetadata only when non-empty.
	HasProviderMetadata bool
	ProviderMetadata    map[string]any

	// ProviderExecuted applies to ToolInputStart (and ToolCall via ToolCall).
	// HasProviderExecuted distinguishes an explicit false from "absent".
	ProviderExecuted    bool
	HasProviderExecuted bool
}

// ProviderMetadataKey is the namespace the OpenAI-compatible provider writes its
// metadata under (metadataKey). Used for thoughtSignature passthrough.
const ProviderMetadataKey = "openaiCompatible"
