package runtime

// OpenAI Responses dialect: build the request body in the shape /v1/responses
// expects (instructions + input items) instead of chat-completions' messages
// array. Split out of model.go so that file stays under the 400-line archcheck
// limit.

import (
	"github.com/tylinux/nex/go/internal/adapters/model/openairesponses"
	"github.com/tylinux/nex/go/internal/modelreq"
	"github.com/tylinux/nex/go/internal/promptctx"
	"github.com/tylinux/nex/go/internal/tooldefs"
)

// buildBodyResponses renders the same conversation the chat builder would
// produce, but in the OpenAI Responses envelope. The system prompt travels out
// of band as `instructions`; the meta-user reminder becomes the first user item
// so the model still sees it before the real user text.
func buildBodyResponses(
	provider Provider,
	promptCfg promptctx.Config,
	firstText string,
	history []modelreq.Message,
	defs []tooldefs.Def,
) ([]byte, error) {
	built := promptctx.Build(promptCfg)
	if len(history) == 0 {
		history = []modelreq.Message{{Role: "user", Content: firstText}}
	}
	input := make([]openairesponses.Message, 0, len(history)+1)
	if len(built.MetaUser) > 0 {
		input = append(input, openairesponses.Message{
			Role:    "user",
			Content: built.MetaUser[0].Content,
		})
	}
	for _, m := range history {
		input = append(input, openairesponses.Message{
			Role:       m.Role,
			Content:    m.Content,
			ToolCalls:  m.ToolCalls,
			ToolCallID: m.ToolCallID,
		})
	}
	return openairesponses.BuildBody(openairesponses.Request{
		Model:           provider.ModelID,
		Instructions:    built.MergedSystem,
		Input:           input,
		Tools:           defs,
		ToolChoice:      openairesponses.ToolChoiceAuto,
		MaxOutputTokens: int(provider.MaxOutputToken),
	})
}
