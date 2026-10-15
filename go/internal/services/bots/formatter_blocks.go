package bots

import (
	"fmt"
	"strings"
)

// formatter_blocks.go ports the block-assembly half of replyFormatter.ts: the
// long-text splitter, the streaming-update accumulator and the block walker that
// turns content / tool-call / change-summary blocks into provider-sized messages.
// IsToolCallReplyTerminal ports isBotToolCallReplyTerminal.
func IsToolCallReplyTerminal(status string) bool {
	switch status {
	case "completed", "failed", "denied", "stopped":
		return true
	}
	return false
}

// ChangeFileStat is one row of NexTaskChangeSummary.files.
type ChangeFileStat struct {
	Path    string             `json:"path"`
	Added   int                `json:"added"`
	Removed int                `json:"removed"`
	Stat    ToolCallChangeStat `json:"-"`
}

// ChangeSummary is NexTaskChangeSummary (the fields the bot formatter reads).
type ChangeSummary struct {
	FileCount int              `json:"fileCount"`
	Files     []ChangeFileStat `json:"files"`
}

// formatChangeSummary ports formatBotChangeSummary.
func formatChangeSummary(changeSummary *ChangeSummary, locale string) string {
	if changeSummary == nil || changeSummary.FileCount <= 0 || len(changeSummary.Files) == 0 {
		return ""
	}
	header := formatterText(locale, fmtChangeSummary)
	var head string
	if locale == "en-US" {
		head = fmt.Sprintf("%s: %d files, %s", header, changeSummary.FileCount, formatChangeSummaryCount(changeSummary))
	} else {
		head = fmt.Sprintf("%s：%d 个文件，%s", header, changeSummary.FileCount, formatChangeSummaryCount(changeSummary))
	}
	lines := []string{head}
	limit := min(maxToolSummaryItems, len(changeSummary.Files))
	for _, file := range changeSummary.Files[:limit] {
		lines = append(lines, fmt.Sprintf("- %s (%s)", formatMarkdownInlineCode(file.Path), formatBotDiffCount(file.Stat)))
	}
	if len(changeSummary.Files) > maxToolSummaryItems {
		lines = append(lines, "- "+strings.ReplaceAll(formatterText(locale, fmtMoreFiles), "{count}",
			fmt.Sprintf("%d", len(changeSummary.Files)-maxToolSummaryItems)))
	}
	return strings.Join(lines, "\n")
}

// formatChangeSummaryCount ports the header's aggregate diff count.
//
// NexTaskChangeSummary carries no summary-level added/removed, so the TS call
// `formatBotDiffCount(changeSummary)` reads undefined for both sides and
// renders an EMPTY string — visible as a trailing ", " in the golden output.
// The Go port reproduces that exactly rather than "fixing" it into a per-file
// sum: the header line is already a chat-visible string in released builds, and
// changing it would make a running bot's /summary text differ from its history.
func formatChangeSummaryCount(summary *ChangeSummary) string {
	return formatBotDiffCount(ToolCallChangeStat{})
}

// AssistantReplyBlock is one block of BotAssistantReplyBlock.
type AssistantReplyBlock struct {
	// Content blocks set Text; tool-call blocks set ToolCall; change-summary
	// blocks set ChangeSummary.
	Text          string
	ToolCall      *ToolCallState
	ChangeSummary *ChangeSummary
}

// FormatAssistantReplyBlocks ports formatBotAssistantReplyBlocks.
func FormatAssistantReplyBlocks(blocks []AssistantReplyBlock, options *FormatOptions) []string {
	var messages []string
	for _, block := range blocks {
		switch {
		case block.ToolCall != nil:
			text := FormatToolCallReply(*block.ToolCall, options)
			messages = append(messages, splitLongReplyText(text)...)
		case block.ChangeSummary != nil:
			locale := ""
			if options != nil {
				locale = options.Locale
			}
			if text := formatChangeSummary(block.ChangeSummary, locale); text != "" {
				messages = append(messages, splitLongReplyText(text)...)
			}
		default:
			messages = append(messages, splitLongReplyText(strings.ReplaceAll(block.Text, "\r\n", "\n"))...)
		}
	}
	return messages
}
