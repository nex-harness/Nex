package modelclient

import "encoding/json"

type decoderChunk struct {
	Error *struct {
		Message string          `json:"message"`
		Type    string          `json:"type"`
		Code    json.RawMessage `json:"code"`
	} `json:"error"`
	Usage   *decoderUsage   `json:"usage"`
	Choices []decoderChoice `json:"choices"`
}

type decoderChoice struct {
	FinishReason *string       `json:"finish_reason"`
	Delta        *decoderDelta `json:"delta"`
}

type decoderDelta struct {
	Content          *string            `json:"content"`
	ReasoningContent *string            `json:"reasoning_content"`
	Reasoning        *string            `json:"reasoning"`
	ToolCalls        []decoderToolDelta `json:"tool_calls"`
}

type decoderToolDelta struct {
	Index    *int    `json:"index"`
	ID       *string `json:"id"`
	Function *struct {
		Name      *string `json:"name"`
		Arguments *string `json:"arguments"`
	} `json:"function"`
	ExtraContent *struct {
		Google *struct {
			ThoughtSignature *string `json:"thought_signature"`
		} `json:"google"`
	} `json:"extra_content"`
}

type decoderUsage struct {
	PromptTokens        *int64 `json:"prompt_tokens"`
	CompletionTokens    *int64 `json:"completion_tokens"`
	TotalTokens         *int64 `json:"total_tokens"`
	PromptTokensDetails *struct {
		CachedTokens *int64 `json:"cached_tokens"`
	} `json:"prompt_tokens_details"`
	CompletionTokensDetails *struct {
		ReasoningTokens          *int64 `json:"reasoning_tokens"`
		AcceptedPredictionTokens *int64 `json:"accepted_prediction_tokens"`
		RejectedPredictionTokens *int64 `json:"rejected_prediction_tokens"`
	} `json:"completion_tokens_details"`
}
