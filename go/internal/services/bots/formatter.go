// formatter.go ports packages/services/src/bots/replyFormatter.ts — the text a
// bot user sees for tool calls, permissions, change summaries and long replies.
//
// Every truncation limit and separator is part of the visible contract (a bot
// message is not re-rendered anywhere else), so the constants and the golden
// outputs in formatter_test.go pin them.
package bots

import (
	"fmt"
	"math"
	"strings"
)

const (
	maxToolSummaryItems   = 10
	maxFieldLength        = 160
	maxCommandFieldLength = 96
	maxReplyMessageLength = 3500
	diffAddedMarker       = "\U0001F7E2" // 🟢
	diffRemovedMarker     = "\U0001F534" // 🔴
)

// FormatterMessageID is the key of the formatterMessages table below.
type FormatterMessageID string

const (
	fmtToolCalls     FormatterMessageID = "toolCalls"
	fmtCompleted     FormatterMessageID = "completed"
	fmtFailed        FormatterMessageID = "failed"
	fmtDenied        FormatterMessageID = "denied"
	fmtInProgress    FormatterMessageID = "inProgress"
	fmtPending       FormatterMessageID = "pending"
	fmtPermissionReq FormatterMessageID = "permissionRequired"
	fmtEditWriting   FormatterMessageID = "editWriting"
	fmtEditUpdating  FormatterMessageID = "editUpdating"
	fmtEditDeleting  FormatterMessageID = "editDeleting"
	fmtEditEditing   FormatterMessageID = "editEditing"
	fmtChangeSummary FormatterMessageID = "changeSummary"
	fmtMoreToolCalls FormatterMessageID = "moreToolCalls"
	fmtMoreFiles     FormatterMessageID = "moreFiles"
)

var formatterMessages = map[string]map[FormatterMessageID]string{
	"zh-CN": {
		fmtToolCalls:     "工具调用：",
		fmtCompleted:     "完成",
		fmtFailed:        "失败",
		fmtDenied:        "已拒绝",
		fmtInProgress:    "⏳ 运行中",
		fmtPending:       "等待中",
		fmtPermissionReq: "需要权限：",
		fmtEditWriting:   "写入中",
		fmtEditUpdating:  "更新中",
		fmtEditDeleting:  "删除中",
		fmtEditEditing:   "编辑中",
		fmtChangeSummary: "变更摘要",
		fmtMoreToolCalls: "还有 {count} 个工具调用",
		fmtMoreFiles:     "还有 {count} 个文件",
	},
	"en-US": {
		fmtToolCalls:     "Tool calls:",
		fmtCompleted:     "Completed",
		fmtFailed:        "Failed",
		fmtDenied:        "Denied",
		fmtInProgress:    "Running",
		fmtPending:       "Pending",
		fmtPermissionReq: "Permission required:",
		fmtEditWriting:   "Writing",
		fmtEditUpdating:  "Updating",
		fmtEditDeleting:  "Deleting",
		fmtEditEditing:   "Editing",
		fmtChangeSummary: "Change summary",
		fmtMoreToolCalls: "{count} more tool calls",
		fmtMoreFiles:     "{count} more files",
	},
}

func formatterText(locale string, id FormatterMessageID) string {
	return formatterMessages[NormalizeLocale(locale)][id]
}

func collapseWhitespace(value string) string {
	var builder strings.Builder
	builder.Grow(len(value))
	pendingSpace := false
	for _, r := range value {
		if r == ' ' || r == '\t' || r == '\n' || r == '\r' || r == '\v' || r == '\f' {
			pendingSpace = builder.Len() > 0
			continue
		}
		if pendingSpace {
			builder.WriteByte(' ')
			pendingSpace = false
		}
		builder.WriteRune(r)
	}
	return builder.String()
}

func truncateText(value string, maxLength int) string {
	if len(value) > maxLength {
		return value[:maxLength-3] + "..."
	}
	return value
}

func truncateMiddleText(value string, maxLength int) string {
	if len(value) <= maxLength {
		return value
	}
	headLength := int(math.Ceil(float64(maxLength-5) * 0.65))
	tailLength := max(0, maxLength-5-headLength)
	return value[:headLength] + " ... " + value[len(value)-tailLength:]
}

// formatMarkdownInlineCode ports formatMarkdownInlineCode: Telegram Markdown
// inline code needs backslash and backtick escaped or the text falls back to a
// literal rendering.
func formatMarkdownInlineCode(value string) string {
	escaped := strings.ReplaceAll(value, `\`, `\\`)
	escaped = strings.ReplaceAll(escaped, "`", "\\`")
	return "`" + escaped + "`"
}

func normalizePathSeparators(value string) string {
	return strings.ReplaceAll(value, `\`, "/")
}

func stripTrailingSlash(value string) string {
	if len(value) > 1 {
		return strings.TrimRight(value, "/")
	}
	return value
}

// toWorkspaceRelativePath ports toWorkspaceRelativePath: the comparison is
// case-insensitive (TS lowercases both sides) but the returned slice is from the
// original, case-preserving string.
func toWorkspaceRelativePath(pathValue, workspacePath string) string {
	normalizedPath := stripTrailingSlash(normalizePathSeparators(strings.TrimSpace(pathValue)))
	if strings.TrimSpace(workspacePath) == "" {
		return normalizedPath
	}
	normalizedWorkspace := stripTrailingSlash(normalizePathSeparators(strings.TrimSpace(workspacePath)))
	comparablePath := strings.ToLower(normalizedPath)
	comparableWorkspace := strings.ToLower(normalizedWorkspace)
	if comparablePath == comparableWorkspace {
		return "."
	}
	if strings.HasPrefix(comparablePath, comparableWorkspace+"/") {
		return normalizedPath[len(normalizedWorkspace)+1:]
	}
	return normalizedPath
}

// ToolCallState is BotReplyToolCallState.
type ToolCallState struct {
	ToolID          string
	ParentToolUseID string
	Title           string
	Kind            string
	Input           any
	Output          any
	Status          string
	Error           string
	Raw             any
}

// FormatOptions is BotReplyFormatOptions.
type FormatOptions struct {
	WorkspacePath string
	Locale        string
}

// normalizeToolCallSummaryInput ports normalizeToolCallSummaryInput: for edit
// tool calls the path fields are rewritten workspace-relative so a bot message
// does not carry a full local path.
func normalizeToolCallSummaryInput(toolCall ToolCallState, options *FormatOptions) any {
	input, ok := toolCall.Input.(map[string]any)
	if !ok || toolCall.Kind != "edit" {
		return toolCall.Input
	}
	next := make(map[string]any, len(input))
	for key, value := range input {
		next[key] = value
	}
	for _, key := range []string{"path", "file_path", "filePath"} {
		if value, ok := next[key].(string); ok {
			next[key] = toWorkspaceRelativePath(value, options.workspacePath())
		}
	}
	return next
}

func (o *FormatOptions) workspacePath() string {
	if o == nil {
		return ""
	}
	return o.WorkspacePath
}

// formatBotDiffCount ports formatBotDiffCount: only non-zero sides render, so a
// pure-add change shows one marker.
func formatBotDiffCount(stat ToolCallChangeStat) string {
	parts := make([]string, 0, 2)
	if stat.Added > 0 {
		parts = append(parts, fmt.Sprintf("%s %s", diffAddedMarker, formatMarkdownInlineCode(fmt.Sprintf("+%d", stat.Added))))
	}
	if stat.Removed > 0 {
		parts = append(parts, fmt.Sprintf("%s %s", diffRemovedMarker, formatMarkdownInlineCode(fmt.Sprintf("-%d", stat.Removed))))
	}
	return strings.Join(parts, " ")
}

// formatCompactSummaryDetail ports formatCompactSummaryDetail.
func formatCompactSummaryDetail(summary ToolCallSummary) string {
	parts := make([]string, 0, 2)
	if summary.SecondaryText != "" {
		parts = append(parts, formatMarkdownInlineCode(truncateMiddleText(normalizeDisplayText(summary.SecondaryText), maxCommandFieldLength)))
	}
	if summary.ChangeStat != nil {
		if stat := formatBotDiffCount(*summary.ChangeStat); stat != "" {
			parts = append(parts, stat)
		}
	}
	if len(parts) == 0 {
		return ""
	}
	return strings.Join(parts, " · ")
}

// formatToolStatus ports formatToolStatus.
func formatToolStatus(toolCall ToolCallState, locale string) string {
	switch toolCall.Status {
	case "completed":
		return formatterText(locale, fmtCompleted)
	case "failed":
		if toolCall.Error != "" {
			return fmt.Sprintf("%s: %s", formatterText(locale, fmtFailed), truncateText(normalizeDisplayText(toolCall.Error), maxFieldLength))
		}
		return formatterText(locale, fmtFailed)
	case "denied":
		return formatterText(locale, fmtDenied)
	case "in_progress":
		return formatterText(locale, fmtInProgress)
	}
	return formatterText(locale, fmtPending)
}

// FormatToolCallSummaryLine ports formatBotToolCallSummaryLine.
func FormatToolCallSummaryLine(toolCall ToolCallState, options *FormatOptions) string {
	kind := toolCall.Kind
	if kind == "" {
		kind = "tool"
	}
	summary := GetCompactToolCallSummary(ToolCallSummarySource{
		Title:  toolCall.Title,
		Kind:   kind,
		Input:  normalizeToolCallSummaryInput(toolCall, options),
		Output: toolCall.Output,
		Raw:    toolCall.Raw,
	})
	locale := ""
	if options != nil {
		locale = options.Locale
	}
	status := formatToolStatus(toolCall, locale)
	detail := formatCompactSummaryDetail(summary)
	line := fmt.Sprintf("- %s · %s", status, summary.PrimaryText)
	if detail != "" {
		line += " · " + detail
	}
	return line
}

// FormatToolCallReply ports formatBotToolCallReply.
func FormatToolCallReply(toolCall ToolCallState, options *FormatOptions) string {
	locale := ""
	if options != nil {
		locale = options.Locale
	}
	return formatterText(locale, fmtToolCalls) + "\n" + FormatToolCallSummaryLine(toolCall, options)
}

// splitLongReplyText ports splitLongReplyText: breaks on the last newline or
// space at or before the limit so a chunk never starts mid-word.
func splitLongReplyText(text string) []string {
	var chunks []string
	remaining := strings.TrimSpace(text)
	for len(remaining) > maxReplyMessageLength {
		breakpoint := maxLastIndexAtOrBefore(remaining, '\n', maxReplyMessageLength)
		if spaceIdx := maxLastIndexAtOrBefore(remaining, ' ', maxReplyMessageLength); spaceIdx > breakpoint {
			breakpoint = spaceIdx
		}
		end := breakpoint
		if end <= 0 {
			end = maxReplyMessageLength
		}
		chunks = append(chunks, strings.TrimSpace(remaining[:end]))
		remaining = strings.TrimSpace(remaining[end:])
	}
	if remaining != "" {
		chunks = append(chunks, remaining)
	}
	return chunks
}

// maxLastIndexAtOrBefore ports `remaining.lastIndexOf(char, MAX)`. JS's
// lastIndexOf with a position clamps the search to index <= pos, which is what
// the rune scan reproduces.
func maxLastIndexAtOrBefore(value string, target byte, limit int) int {
	start := limit
	if start >= len(value) {
		start = len(value) - 1
	}
	for i := start; i >= 0; i-- {
		if value[i] == target {
			return i
		}
	}
	return -1
}

// ExtractAssistantResponseMessages ports extractBotAssistantAssistantMessages:
// non-force keeps the buffer, force flushes it as provider-sized chunks.
func ExtractAssistantResponseMessages(buffer string, force bool) ([]string, string) {
	normalizedBuffer := strings.ReplaceAll(buffer, "\r\n", "\n")
	if !force {
		return nil, normalizedBuffer
	}
	// Bugfix (TS): provider chunk 常按词或子词到达，非终态时必须继续留在 buffer 里合并；
	// 真正的发送边界由 tool_call / task_complete 等 force flush 决定。
	return splitLongReplyText(normalizedBuffer), ""
}
