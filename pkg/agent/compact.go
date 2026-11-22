package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/nex-harness/nex/pkg/ai"
)

const (
	// DefaultMaxToolOutputBytes caps any single tool output inserted into LLM history.
	DefaultMaxToolOutputBytes = 64 * 1024
	// microcompactMinBytes is the threshold above which historical tool outputs are pruned.
	microcompactMinBytes = 1200
	// microcompactKeepRecent preserves tool outputs in the latest N messages untouched.
	microcompactKeepRecent = 6
	// compactKeepRecentMsgs preserves the latest N messages across full LLM compaction.
	compactKeepRecentMsgs = 6
	// microcompactThresholdPct triggers zero-LLM microcompaction before a turn.
	microcompactThresholdPct = 0.65
	// autoCompactThresholdPct triggers full LLM-backed compaction before a turn.
	autoCompactThresholdPct = 0.85
)

// GuardToolOutput ensures no single tool result blows up the context window,
// preserving both the head and tail when truncation occurs.
func GuardToolOutput(output string, maxBytes int) string {
	if maxBytes <= 0 {
		maxBytes = DefaultMaxToolOutputBytes
	}
	if len(output) <= maxBytes {
		return output
	}
	headLen := (maxBytes * 2) / 3
	tailLen := maxBytes - headLen
	if headLen < 128 {
		headLen = maxBytes
		tailLen = 0
	}
	head := trimUTF8Prefix(output, headLen)
	if tailLen <= 0 {
		return fmt.Sprintf("%s\n\n... [truncated %d bytes]", head, len(output)-len(head))
	}
	tail := trimUTF8Suffix(output, tailLen)
	omitted := len(output) - len(head) - len(tail)
	if omitted < 0 {
		omitted = 0
	}
	return fmt.Sprintf("%s\n\n... [truncated %d bytes of %d total bytes] ...\n\n%s", head, omitted, len(output), tail)
}

func trimUTF8Prefix(s string, maxBytes int) string {
	if len(s) <= maxBytes {
		return s
	}
	cut := maxBytes
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut]
}

func trimUTF8Suffix(s string, maxBytes int) string {
	if len(s) <= maxBytes {
		return s
	}
	start := len(s) - maxBytes
	for start < len(s) && !utf8.RuneStart(s[start]) {
		start++
	}
	return s[start:]
}

// EstimateContextTokens provides a fast pre-flight token estimate for auto-compaction decisions.
func EstimateContextTokens(sections []ai.SystemSection, systemPrompt string, toolDefs []ai.ToolDefinition, msgs []ai.Message) int {
	totalBytes := len(systemPrompt)
	for _, sec := range sections {
		totalBytes += len(sec.Content)
	}
	for _, td := range toolDefs {
		totalBytes += len(td.Name) + len(td.Description) + len(td.InputSchema) + 32
	}
	for _, m := range msgs {
		totalBytes += 16
		for _, b := range m.Content {
			totalBytes += len(b.Text) + len(b.Thinking) + len(b.Arguments) + len(b.ToolName)
			if b.Type == ai.BlockImage {
				totalBytes += 4000 // ~1000 tokens per image estimate
			}
		}
	}
	// Conservative ~3.5 bytes/token average across English, code, and CJK.
	return (totalBytes*10 + 34) / 35
}

// MicrocompactMessages trims bulky older non-error tool_result outputs while preserving
// recent turns, user messages, assistant text, and all tool_call/tool_result structural pairs.
// Returns the pruned slice and the count of compacted tool results.
func MicrocompactMessages(msgs []ai.Message, keepRecent int) ([]ai.Message, int) {
	if keepRecent <= 0 {
		keepRecent = microcompactKeepRecent
	}
	if len(msgs) <= keepRecent {
		return msgs, 0
	}
	cutoff := len(msgs) - keepRecent
	out := make([]ai.Message, len(msgs))
	copy(out, msgs)
	pruned := 0

	for i := 0; i < cutoff; i++ {
		m := out[i]
		if m.Role != ai.RoleToolResult || m.IsError {
			continue
		}
		txt := m.PlainText()
		if len(txt) < microcompactMinBytes {
			continue
		}
		if strings.HasPrefix(txt, "[Compacted tool output") {
			continue
		}
		name := m.ToolName
		if name == "" {
			name = "tool"
		}
		placeholder := fmt.Sprintf("[Compacted tool output from %s (%d bytes); re-read or re-run if needed]", name, len(txt))
		out[i] = ai.Message{
			Role:       ai.RoleToolResult,
			ToolCallID: m.ToolCallID,
			ToolName:   m.ToolName,
			IsError:    m.IsError,
			Content:    []ai.ContentBlock{ai.TextBlock(placeholder)},
			Timestamp:  m.Timestamp,
		}
		pruned++
	}
	return out, pruned
}

// PruneOldToolImages preserves raw BlockImage payloads on only the latest keepLast
// RoleToolResult messages that carry images, replacing older screenshots with compact
// text placeholders so long browser sessions do not exhaust context windows.
func PruneOldToolImages(msgs []ai.Message, keepLast int) ([]ai.Message, int) {
	if keepLast <= 0 {
		keepLast = 3
	}
	var imageMsgIndices []int
	for i := range msgs {
		if msgs[i].Role != ai.RoleToolResult {
			continue
		}
		for _, b := range msgs[i].Content {
			if b.Type == ai.BlockImage && (b.DataB64 != "" || b.ImageURL != "") {
				imageMsgIndices = append(imageMsgIndices, i)
				break
			}
		}
	}
	if len(imageMsgIndices) <= keepLast {
		return msgs, 0
	}

	cutoff := len(imageMsgIndices) - keepLast
	out := make([]ai.Message, len(msgs))
	copy(out, msgs)
	pruned := 0
	for _, idx := range imageMsgIndices[:cutoff] {
		orig := out[idx]
		nextBlocks := make([]ai.ContentBlock, 0, len(orig.Content))
		for _, b := range orig.Content {
			if b.Type == ai.BlockImage {
				nextBlocks = append(nextBlocks, ai.TextBlock("[Earlier screenshot omitted to save context]"))
				pruned++
			} else {
				nextBlocks = append(nextBlocks, b)
			}
		}
		orig.Content = nextBlocks
		out[idx] = orig
	}
	return out, pruned
}

// findCompactionSplitIndex finds an index splitIdx such that msgs[:splitIdx] is summarized
// and msgs[splitIdx:] is kept verbatim, WITHOUT ever splitting an assistant(tool_call)
// message from its immediately following RoleToolResult messages.
func findCompactionSplitIndex(msgs []ai.Message, keepRecent int) int {
	n := len(msgs)
	if n <= 2 {
		return 0
	}
	if keepRecent < 2 {
		keepRecent = 2
	}
	if n <= keepRecent {
		return 0
	}
	split := n - keepRecent
	// Walk forward if split lands on a tool_result so we never start the kept tail with an orphan tool_result.
	for split < n && msgs[split].Role == ai.RoleToolResult {
		split++
	}
	// If walking forward consumed everything, try walking backward to include the preceding assistant tool_call turn.
	if split >= n {
		split = n - keepRecent
		for split > 0 && msgs[split].Role == ai.RoleToolResult {
			split--
		}
		// Keep the assistant message that triggered those tool calls in the tail as well.
		if split > 0 && msgs[split-1].Role == ai.RoleAssistant {
			split--
		}
	}
	if split <= 0 || split >= n {
		return 0
	}
	return split
}

const compactionSystemPrompt = `You are a technical conversation compactor for a software engineering agent.
Produce a concise, high-density structured handoff summary of the conversation history so the agent can seamlessly continue without losing critical context.

Include these sections (omit empty ones):
## Goal & Constraints
- User's core objective, requirements, and explicit preferences/constraints.

## Key Decisions & Findings
- Architectural choices, root causes found, important file paths and symbol names.

## Work Completed
- Concrete file changes, commands run, and verified test/build results.

## Remaining Work & Open Issues
- Pending steps, unresolved errors, or blockers.

Rules:
- Preserve exact file paths, function names, error messages, and branch/command details.
- Never fabricate test passes or file edits.
- Keep it tight and structured so it uses minimal tokens.`

// CompactMessages summarizes older conversation turns into a single structured summary
// message while retaining recent turns intact. If prov is nil or the LLM summary call fails,
// it falls back to a deterministic digest so context overflow recovery never crashes.
func CompactMessages(
	ctx context.Context,
	prov ai.Provider,
	model string,
	msgs []ai.Message,
	focusInstructions string,
) ([]ai.Message, string, error) {
	splitIdx := findCompactionSplitIndex(msgs, compactKeepRecentMsgs)
	if splitIdx == 0 {
		// Nothing safe to split; at least microcompact everything except the last message.
		pruned, count := MicrocompactMessages(msgs, 2)
		if count > 0 {
			return pruned, fmt.Sprintf("Microcompacted %d historical tool outputs.", count), nil
		}
		return msgs, "", nil
	}

	prefix := msgs[:splitIdx]
	tail := msgs[splitIdx:]

	transcript := renderTranscriptForCompaction(prefix, 48*1024)
	var summary string

	if prov != nil && model != "" {
		userPrompt := "Summarize the following conversation history for continuation:\n\n" + transcript
		if strings.TrimSpace(focusInstructions) != "" {
			userPrompt += "\n\nAdditional focus instructions from the user:\n" + strings.TrimSpace(focusInstructions)
		}
		sumCtx, cancel := context.WithTimeout(ctx, 45*time.Second)
		ch, err := prov.Stream(sumCtx, &ai.StreamRequest{
			Model:           model,
			SystemPrompt:    compactionSystemPrompt,
			Messages:        []ai.Message{ai.NewUserTextMessage(userPrompt)},
			MaxOutputTokens: 2048,
		})
		if err == nil {
			var sb strings.Builder
			for ev := range ch {
				if ev.Type == ai.StreamEventTextDelta {
					sb.WriteString(ev.TextDelta)
				}
			}
			summary = strings.TrimSpace(sb.String())
		}
		cancel()
	}

	if summary == "" {
		summary = buildFallbackCompactionSummary(prefix, focusInstructions)
	}

	wrapped := fmt.Sprintf("<context-compaction-summary compacted_messages=\"%d\">\n%s\n</context-compaction-summary>", len(prefix), summary)
	summaryMsg := ai.Message{
		Role:      ai.RoleUser,
		Content:   []ai.ContentBlock{ai.TextBlock(wrapped)},
		Timestamp: time.Now().UnixMilli(),
	}

	compacted := make([]ai.Message, 0, 1+len(tail))
	compacted = append(compacted, summaryMsg)
	compacted = append(compacted, tail...)
	return compacted, summary, nil
}

func renderTranscriptForCompaction(msgs []ai.Message, maxBytes int) string {
	var sb strings.Builder
	for i, m := range msgs {
		if sb.Len() >= maxBytes {
			fmt.Fprintf(&sb, "\n... [%d more messages omitted]\n", len(msgs)-i)
			break
		}
		switch m.Role {
		case ai.RoleUser:
			fmt.Fprintf(&sb, "[User]: %s\n", GuardToolOutput(strings.TrimSpace(m.PlainText()), 2000))
		case ai.RoleAssistant:
			if txt := strings.TrimSpace(m.PlainText()); txt != "" {
				fmt.Fprintf(&sb, "[Assistant]: %s\n", GuardToolOutput(txt, 2000))
			}
			for _, b := range m.Content {
				if b.Type == ai.BlockToolCall {
					argPreview := GuardToolOutput(string(b.Arguments), 400)
					fmt.Fprintf(&sb, "[Assistant ToolCall %s]: %s\n", b.ToolName, argPreview)
				}
			}
		case ai.RoleToolResult:
			status := "ok"
			if m.IsError {
				status = "error"
			}
			fmt.Fprintf(&sb, "[ToolResult %s (%s)]: %s\n", m.ToolName, status, GuardToolOutput(strings.TrimSpace(m.PlainText()), 800))
		}
	}
	return sb.String()
}

func buildFallbackCompactionSummary(prefix []ai.Message, focus string) string {
	var sb strings.Builder
	sb.WriteString("## Deterministic Context Summary (Fallback)\n")
	if strings.TrimSpace(focus) != "" {
		sb.WriteString("Focus: " + strings.TrimSpace(focus) + "\n")
	}
	var userGoals []string
	var toolsUsed []string
	seenTools := map[string]bool{}
	for _, m := range prefix {
		if m.Role == ai.RoleUser {
			t := strings.TrimSpace(m.PlainText())
			if t != "" && !strings.HasPrefix(t, "<context-compaction-summary") {
				if len(t) > 240 {
					t = trimUTF8Prefix(t, 240) + "..."
				}
				userGoals = append(userGoals, t)
			}
		}
		if m.Role == ai.RoleAssistant {
			for _, b := range m.Content {
				if b.Type == ai.BlockToolCall && b.ToolName != "" && !seenTools[b.ToolName] {
					seenTools[b.ToolName] = true
					toolsUsed = append(toolsUsed, b.ToolName)
				}
			}
		}
	}
	if len(userGoals) > 0 {
		sb.WriteString("\n### Earlier User Requests\n")
		limit := len(userGoals)
		if limit > 5 {
			limit = 5
		}
		for _, g := range userGoals[:limit] {
			sb.WriteString("- " + g + "\n")
		}
	}
	if len(toolsUsed) > 0 {
		sb.WriteString("\n### Tools Invoked Earlier\n- " + strings.Join(toolsUsed, ", ") + "\n")
	}
	// Include snippet of last assistant message before cutoff
	for i := len(prefix) - 1; i >= 0; i-- {
		if prefix[i].Role == ai.RoleAssistant {
			if txt := strings.TrimSpace(prefix[i].PlainText()); txt != "" {
				sb.WriteString("\n### Last Assistant Update Before Cut\n")
				sb.WriteString(GuardToolOutput(txt, 1200) + "\n")
				break
			}
		}
	}
	return strings.TrimSpace(sb.String())
}

// ParseCompactCommand detects a leading `/compact` command and extracts optional focus instructions.
func ParseCompactCommand(raw string) (focus string, ok bool) {
	trimmed := strings.TrimSpace(raw)
	if len(trimmed) < len("/compact") {
		return "", false
	}
	if !strings.EqualFold(trimmed[:len("/compact")], "/compact") {
		return "", false
	}
	rest := trimmed[len("/compact"):]
	if rest != "" && rest[0] != ' ' && rest[0] != '\t' && rest[0] != '\n' {
		return "", false
	}
	return strings.TrimSpace(rest), true
}

// IsCompactionEventSummary detects whether a stored user message represents a compaction checkpoint.
func IsCompactionEventSummary(raw json.RawMessage) bool {
	return strings.Contains(string(raw), "<context-compaction-summary")
}
