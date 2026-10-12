package openairesponses

// Pure helpers pulled out of decoder.go so the decoder stays under the 400-line
// archcheck limit. These functions do no stream state work; they translate one
// provider value into one modelclient value.

import (
	"encoding/json"
	"strconv"

	"github.com/openai/openai-go/responses"

	"github.com/tylinux/nex/go/internal/modelclient"
)

// mapFinishReason normalises the Responses status vocabulary onto the unified
// reasons modelclient uses. Responses speaks `status` plus
// `incomplete_details.reason`, not chat-completions' finish_reason, so the
// mapping is explicit:
//
//	completed                -> stop   (or tool-calls when a client-side call happened)
//	incomplete/max_output_tokens -> length
//	incomplete/content_filter    -> content-filter
//	failed                   -> error
//	anything else            -> other  (or tool-calls when a client-side call happened)
func mapFinishReason(status responses.ResponseStatus, incompleteReason string, hasFunctionCall bool) string {
	switch string(status) {
	case "completed":
		if hasFunctionCall {
			return modelclient.FinishToolCalls
		}
		return modelclient.FinishStop
	case "failed":
		return modelclient.FinishError
	case "incomplete":
		switch incompleteReason {
		case "max_output_tokens":
			return modelclient.FinishLength
		case "content_filter":
			return modelclient.FinishContentFilter
		}
		if hasFunctionCall {
			return modelclient.FinishToolCalls
		}
		return modelclient.FinishOther
	case "":
		if hasFunctionCall {
			return modelclient.FinishToolCalls
		}
		return modelclient.FinishStop
	default:
		if hasFunctionCall {
			return modelclient.FinishToolCalls
		}
		return modelclient.FinishOther
	}
}

// convertUsage maps the Responses usage object onto modelclient.Usage. The field
// names differ from chat-completions (input_tokens/output_tokens rather than
// prompt_tokens/completion_tokens) but the shape is identical, including the
// cached and reasoning detail pointers.
func convertUsage(u responses.ResponseUsage) *modelclient.Usage {
	out := &modelclient.Usage{}
	in := u.InputTokens
	outTokens := u.OutputTokens
	total := u.TotalTokens
	out.PromptTokens = &in
	out.CompletionTokens = &outTokens
	out.TotalTokens = &total
	if u.JSON.InputTokensDetails.Valid() || u.InputTokensDetails.JSON.CachedTokens.Valid() {
		cached := u.InputTokensDetails.CachedTokens
		out.CachedTokens = &cached
	}
	if u.JSON.OutputTokensDetails.Valid() || u.OutputTokensDetails.JSON.ReasoningTokens.Valid() {
		reasoning := u.OutputTokensDetails.ReasoningTokens
		out.ReasoningTokens = &reasoning
		out.HasReasoningDetails = true
	}
	return out
}

// topLevelError returns the raw JSON of a top-level "error" key, and whether the
// key was present at all. An explicit null is present-but-not-an-error (R1):
// gjson's Exists() reports it as present, which is exactly the vendor-SDK bug
// the spike measured, so the null is filtered out here before the body reaches
// the SDK's decoder.
func topLevelError(body string) (json.RawMessage, bool) {
	var probe struct {
		Error json.RawMessage `json:"error"`
	}
	if err := json.Unmarshal([]byte(body), &probe); err != nil {
		return nil, false
	}
	if len(probe.Error) == 0 || string(probe.Error) == "null" {
		return nil, false
	}
	return probe.Error, true
}

// errorMessage pulls a human-readable message out of an OpenAI error object,
// which is either {"message":…} or a bare string.
func errorMessage(raw json.RawMessage) string {
	var obj struct {
		Message string `json:"message"`
		Code    string `json:"code"`
		Type    string `json:"type"`
	}
	if err := json.Unmarshal(raw, &obj); err == nil && obj.Message != "" {
		return obj.Message
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil && s != "" {
		return s
	}
	return string(raw)
}

// itoa64 formats an int64 without pulling strconv into every call site.
func itoa64(n int64) string { return strconv.FormatInt(n, 10) }
