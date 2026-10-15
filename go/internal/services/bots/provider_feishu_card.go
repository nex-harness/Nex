package bots

import (
	"strconv"
	"strings"
)

// provider_feishu_card.go builds the Card JSON 2.0 payloads: the interactive card
// for a selection / elicitation reply and the streaming card the "running"
// granularity maintains.
//
// The schema version is not cosmetic — replyMode `streaming_card` is only offered
// on Feishu/Lark because the streaming update path depends on Card JSON 2.0, and
// the tagged-element budget below is the platform's ceiling for one card.
// formatFeishuCardMarkdownContent ports formatFeishuCardMarkdownContent: the
// card markdown dialect is not Telegram's, so the two escaping rules differ.
func formatFeishuCardMarkdownContent(text string) string {
	return strings.ReplaceAll(text, "\\", "\\\\")
}

// BuildFeishuCardPayload ports buildFeishuInteractiveCardPayload's element
// assembly for a selection / elicitation message. The card is always JSON 2.0 —
// that is why streaming_card is the only granularity Feishu supports.
func BuildFeishuCardPayload(message OutboundMessage) map[string]any {
	elements := make([]any, 0, 8)
	if message.Text != "" {
		elements = append(elements, map[string]any{
			"tag":     "markdown",
			"content": formatFeishuCardMarkdownContent(message.Text),
		})
	}
	if message.Selection != nil {
		elements = append(elements, buildFeishuSelectionElements(*message.Selection)...)
	}
	if message.Elicitation != nil {
		elements = append(elements, buildFeishuElicitationElements(*message.Elicitation)...)
	}
	return map[string]any{
		"schema": "2.0",
		"config": map[string]any{"wide_screen_mode": true},
		"body":   map[string]any{"elements": elements},
	}
}

// buildFeishuSelectionElements ports the button row builder: one action button
// per option, whose value carries the command the parser understands.
func buildFeishuSelectionElements(selection SelectionPrompt) []any {
	elements := make([]any, 0, len(selection.Options))
	for index, option := range selection.Options {
		command := FormatSelectionCommand(&selection, option.ID)
		elements = append(elements, map[string]any{
			"tag":  "button",
			"text": map[string]any{"tag": "plain_text", "content": option.Label},
			"type": "primary",
			"behaviors": []any{map[string]any{
				"type": "callback",
				"value": map[string]any{
					"command": command,
					"index":   strconv.Itoa(index + 1),
				},
			}},
			// The trailing index keeps the ordering stable for the card tests.
			"value": map[string]any{"key": strconv.Itoa(index + 1)},
		})
	}
	return elements
}

// buildFeishuElicitationElements ports buildFeishuElicitationAnswerElements: a
// question block plus its option buttons, each carrying the elicitation token.
func buildFeishuElicitationElements(elicitation OutboundElicitationRequest) []any {
	elements := make([]any, 0, 4)
	if elicitation.Status == "cancelled" {
		return append(elements, map[string]any{
			"tag":     "markdown",
			"content": formatFeishuCardMarkdownContent(FormatMessage(NormalizeLocale(""), "elicitationCancelledCard", nil)),
		})
	}
	if elicitation.CurrentQuestion >= len(elicitation.Questions) {
		return elements
	}
	question := elicitation.Questions[elicitation.CurrentQuestion]
	if question.Header != "" {
		elements = append(elements, map[string]any{
			"tag":     "markdown",
			"content": formatFeishuCardMarkdownContent(question.Header),
		})
	}
	if question.Question != "" {
		elements = append(elements, map[string]any{
			"tag":     "markdown",
			"content": formatFeishuCardMarkdownContent(question.Question),
		})
	}
	for _, option := range question.Options {
		elements = append(elements, map[string]any{
			"tag":  "button",
			"text": map[string]any{"tag": "plain_text", "content": option},
			"type": "default",
			"behaviors": []any{map[string]any{
				"type": "callback",
				"value": map[string]any{
					"command": "/elicitation " + elicitation.RequestID + " " + option,
				},
			}},
			"value": map[string]any{"key": option},
		})
	}
	return elements
}

// FormatFeishuStreamingStatus ports formatFeishuStreamingStatus.
func FormatFeishuStreamingStatus(state StreamingCardState) string {
	switch state.Status {
	case "running":
		return "⏳ " + FormatMessage(NormalizeLocale(state.Locale), "streamingStatusRunning", nil)
	case "completed":
		return "✅ " + FormatMessage(NormalizeLocale(state.Locale), "streamingStatusCompleted", nil)
	case "error":
		return "❌ " + FormatMessage(NormalizeLocale(state.Locale), "streamingStatusFailed", nil)
	}
	return ""
}

// BuildFeishuStreamingCardPayload ports buildFeishuStreamingCardPayload.
func BuildFeishuStreamingCardPayload(state StreamingCardState) map[string]any {
	elements := make([]any, 0, len(state.Blocks)+2)
	if status := FormatFeishuStreamingStatus(state); status != "" {
		elements = append(elements, map[string]any{"tag": "markdown", "content": status})
	}
	for _, block := range state.Blocks {
		if block.IsTools {
			tools := block
			title := tools.Title
			if title == "" {
				title = FormatMessage(NormalizeLocale(state.Locale), "streamingToolSummaries", nil)
			}
			content := title + "\n" + strings.Join(tools.Summaries, "\n")
			elements = append(elements, map[string]any{
				"tag":     "markdown",
				"content": formatFeishuCardMarkdownContent(content),
			})
			continue
		}
		if block.Text != "" {
			elements = append(elements, map[string]any{
				"tag":     "markdown",
				"content": formatFeishuCardMarkdownContent(block.Text),
			})
		}
	}
	return map[string]any{
		"schema": "2.0",
		"config": map[string]any{"wide_screen_mode": true, "streaming_mode": true},
		"body":   map[string]any{"elements": elements},
	}
}

// CountFeishuCardTaggedElements ports countFeishuCardTaggedElements: the JSON 2.0
// element budget, counted over the card tree.
func CountFeishuCardTaggedElements(value any) int {
	record, ok := value.(map[string]any)
	if !ok {
		return 0
	}
	count := 0
	if tag, ok := record["tag"].(string); ok && tag != "" {
		count++
	}
	for _, nested := range record {
		count += CountFeishuCardTaggedElements(nested)
	}
	return count
}

// FeishuStreamingCardTaggedElementBudget is
// FEISHU_STREAMING_CARD_TAGGED_ELEMENT_BUDGET.
const FeishuStreamingCardTaggedElementBudget = 180
