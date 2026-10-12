package anthropic

import (
	"github.com/tylinux/nex/go/internal/modelclient"
)

// MapStopReason normalizes an Anthropic stop_reason onto the modelclient unified
// set. The mapping is the one @ai-sdk/anthropic uses (mapAnthropicStopReason):
//
//	pause_turn | end_turn | stop_sequence       -> stop
//	refusal                                   -> content-filter
//	tool_use                                  -> tool-calls
//	max_tokens | model_context_window_exceeded -> length
//	anything else                             -> other
//
// model_context_window_exceeded matters beyond the label: the failure classifier
// treats it as a context-window overflow, and losing it would classify a context
// overflow as a plain length stop.
func MapStopReason(stop string) string {
	switch stop {
	case "pause_turn", "end_turn", "stop_sequence":
		return modelclient.FinishStop
	case "refusal":
		return modelclient.FinishContentFilter
	case "tool_use":
		return modelclient.FinishToolCalls
	case "max_tokens", "model_context_window_exceeded":
		return modelclient.FinishLength
	default:
		return modelclient.FinishOther
	}
}

// anthropicUsage holds the four token counters both anthropic.Usage
// (message_start) and anthropic.MessageDeltaUsage (message_delta) carry, so the
// merge logic is written once instead of per variant.
type anthropicUsage struct {
	inputTokens              int64
	outputTokens             int64
	cacheReadInputTokens     int64
	cacheCreationInputTokens int64
}

// mergeUsage folds Anthropic usage numbers into the running totals.
//
// Anthropic reports usage in two places: message_start carries input_tokens (plus
// an initial output_tokens that is only a running count), and message_delta carries
// the cumulative output_tokens. The AI SDK's provider keeps the last non-null value
// of each field across both events, so input_tokens survives a message_delta that
// omits it. `isDelta` selects that last-wins behaviour for message_delta; for
// message_start it is a first write.
//
// The token fields map onto the modelclient Usage struct as follows:
//
//	input_tokens            -> PromptTokens
//	output_tokens           -> CompletionTokens
//	cache_read_input_tokens -> CachedTokens
//
// cache_creation_input_tokens has no home in modelclient.Usage. It is deliberately
// not folded into PromptTokens here: PromptTokens is the field the parity oracle
// (chat-completions prompt_tokens) maps onto, and silently inflating it would make
// the two dialects disagree. Anthropic callers that care about cache writes read
// CachedTokens for reads and the provider's own usage for writes.
func mergeUsage(acc *modelclient.Usage, next anthropicUsage, isDelta bool) *modelclient.Usage {
	if acc == nil {
		acc = &modelclient.Usage{}
	}
	// input_tokens: message_start seeds it; message_delta overrides only when
	// present, matching the provider's "last non-null wins".
	if v := next.inputTokens; v > 0 || (!isDelta && acc.PromptTokens == nil) {
		acc.PromptTokens = ptr(v)
	}
	if v := next.cacheReadInputTokens; v > 0 {
		acc.CachedTokens = ptr(v)
	}
	// output_tokens is cumulative and always overwritten by the latest value.
	if isDelta || acc.CompletionTokens == nil {
		acc.CompletionTokens = ptr(next.outputTokens)
	}
	acc.TotalTokens = ptr(usageTotal(acc))
	return acc
}

// usageTotal is prompt + completion. Both pointer fields may be nil when the
// provider reported nothing.
func usageTotal(u *modelclient.Usage) int64 {
	var in, out int64
	if u.PromptTokens != nil {
		in = *u.PromptTokens
	}
	if u.CompletionTokens != nil {
		out = *u.CompletionTokens
	}
	return in + out
}

// Copy returns a deep copy of a Usage, or nil for nil, so callers cannot alias
// the decoder's running total.
func copyUsage(u *modelclient.Usage) *modelclient.Usage {
	if u == nil {
		return nil
	}
	clone := *u
	clone.PromptTokens = clonePtr(u.PromptTokens)
	clone.CompletionTokens = clonePtr(u.CompletionTokens)
	clone.TotalTokens = clonePtr(u.TotalTokens)
	clone.CachedTokens = clonePtr(u.CachedTokens)
	clone.ReasoningTokens = clonePtr(u.ReasoningTokens)
	return &clone
}

func clonePtr(v *int64) *int64 {
	if v == nil {
		return nil
	}
	c := *v
	return &c
}

func ptr(v int64) *int64 { return &v }
