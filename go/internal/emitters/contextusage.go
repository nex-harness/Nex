package emitters

import (
	"bytes"
	"encoding/json"
	"strings"

	"github.com/tylinux/nex/go/internal/v4pub"
)

// ContextUsageSection is one ContextBuilder section, as buildContextUsageSnapshot
// reads it: name, source, injectionTarget, cacheHint, chars, tokens.
type ContextUsageSection struct {
	Name            string
	Source          string
	InjectionTarget string // "system" | "meta_user"
	CacheHint       string
	Chars           int64
	Tokens          int64
}

// ToolContract mirrors the ModelToolContract fields buildToolUsageDetail
// stringifies. Only the fields the estimation uses are carried; the Go caller
// supplies the same JSON Schemas the runtime sends. JSON fields are raw so the
// serialized bytes match JSON.stringify exactly.
type ToolContract struct {
	Name            string
	Description     string
	Capability      string
	InputSchema     json.RawMessage
	OutputSchema    json.RawMessage
	ReadOnly        *bool
	Destructive     *bool
	SideEffectScope string
	Permission      json.RawMessage
	ResultBudget    json.RawMessage
}

// ContextUsageMessage is one model input message; the breakdown counts its
// stringified {role, content, toolCalls, toolCallId, toolName}.
type ContextUsageMessage struct {
	Role       string
	Content    json.RawMessage
	ToolCalls  json.RawMessage
	ToolCallID string
	ToolName   string
	// MetaUserContext marks a meta-user reminder injected by the runtime; those
	// are excluded from the `messages` category (isMetaUserContextMessage).
	MetaUserContext bool
}

// BreakdownItem is one entry of the contextUsageBreakdown list.
type BreakdownItem struct {
	Source string
	Chars  int64
}

// ContextUsage ports buildContextUsageSnapshot's category totals and
// buildContextUsageBreakdownFromSnapshot's filtering into the wire breakdown.
// It returns the items in category order, keeping only sources in the allowed
// set with chars > 0 (and whose category has tokens > 0).
func ContextUsage(sections []ContextUsageSection, tools []ToolContract, messages []ContextUsageMessage) []BreakdownItem {
	// Category sums, mirroring the section filters.
	var systemChars, systemTokens int64
	var metaChars, metaTokens int64
	var skillChars, skillTokens int64
	var toolPromptChars, toolPromptTokens int64
	for _, s := range sections {
		switch {
		case s.Source == "skills":
			skillChars += s.Chars
			skillTokens += s.Tokens
		case s.Source == "tools":
			toolPromptChars += s.Chars
			toolPromptTokens += s.Tokens
		case s.InjectionTarget == "system":
			systemChars += s.Chars
			systemTokens += s.Tokens
		case s.InjectionTarget == "meta_user":
			metaChars += s.Chars
			metaTokens += s.Tokens
		}
	}

	// Tool contracts are split into system vs mcp by name prefix.
	var sysToolChars, sysToolTokens int64
	var mcpToolChars, mcpToolTokens int64
	for _, t := range tools {
		content := string(toolContractContent(t))
		chars := jsLen(content)
		tokens := estimateTokens(content)
		if isMCPToolName(t.Name) {
			mcpToolChars += chars
			mcpToolTokens += tokens
		} else {
			sysToolChars += chars
			sysToolTokens += tokens
		}
	}

	// messages: non-system messages, excluding meta-user reminders
	// (isMetaUserContextMessage: role user && content text starts with the tag).
	var msgChars, msgTokens int64
	for _, m := range messages {
		if m.Role == "system" || m.MetaUserContext || isMetaUserContextMessage(m) {
			continue
		}
		content := string(messageContent(m))
		msgChars += jsLen(content)
		msgTokens += estimateTokens(content)
	}

	type cat struct {
		source string
		chars  int64
		tokens int64
	}
	categories := []cat{
		{"system_prompt", systemChars, systemTokens},
		{"meta_user_context", metaChars, metaTokens},
		{"skills", skillChars, skillTokens},
		{"tool_prompt", toolPromptChars, toolPromptTokens},
		{"system_tool_schemas", sysToolChars, sysToolTokens},
		{"mcp_tool_schemas", mcpToolChars, mcpToolTokens},
		{"messages", msgChars, msgTokens},
	}
	out := make([]BreakdownItem, 0, len(categories))
	for _, c := range categories {
		if c.tokens <= 0 || c.chars <= 0 {
			continue
		}
		out = append(out, BreakdownItem{Source: c.source, Chars: c.chars})
	}
	return out
}

// toolContractContent ports the stringifyForEstimation input of
// buildToolUsageDetail: {name, description, capability, inputSchema,
// outputSchema, readOnly, destructive, sideEffectScope, permission,
// resultBudget} with undefined fields omitted.
func toolContractContent(t ToolContract) json.RawMessage {
	o := newObj()
	o.set("name", v4pub.Str(t.Name))
	if t.Description != "" {
		o.set("description", v4pub.Str(t.Description))
	}
	if t.Capability != "" {
		o.set("capability", v4pub.Str(t.Capability))
	}
	if len(t.InputSchema) > 0 {
		o.set("inputSchema", v4pub.RawMessageOf(compactRaw(t.InputSchema)))
	}
	if len(t.OutputSchema) > 0 {
		o.set("outputSchema", v4pub.RawMessageOf(compactRaw(t.OutputSchema)))
	}
	if t.ReadOnly != nil {
		o.set("readOnly", v4pub.Bool(*t.ReadOnly))
	}
	if t.Destructive != nil {
		o.set("destructive", v4pub.Bool(*t.Destructive))
	}
	if t.SideEffectScope != "" {
		o.set("sideEffectScope", v4pub.Str(t.SideEffectScope))
	}
	if len(t.Permission) > 0 {
		o.set("permission", v4pub.RawMessageOf(compactRaw(t.Permission)))
	}
	if len(t.ResultBudget) > 0 {
		o.set("resultBudget", v4pub.RawMessageOf(compactRaw(t.ResultBudget)))
	}
	return o.raw()
}

// compactRaw removes insignificant JSON whitespace so a pretty-printed source
// (as stored in the vectors) matches the compact form JSON.stringify produces.
func compactRaw(raw json.RawMessage) json.RawMessage {
	if len(raw) == 0 {
		return raw
	}
	var buf bytes.Buffer
	if err := json.Compact(&buf, raw); err != nil {
		return raw
	}
	return json.RawMessage(buf.Bytes())
}

// isMetaUserContextMessage ports isMetaUserContextMessage: a user message whose
// content text, left-trimmed, starts with the system-reminder tag.
func isMetaUserContextMessage(m ContextUsageMessage) bool {
	if m.Role != "user" {
		return false
	}
	text := contentText(m.Content)
	return hasPrefix(trimSpaceStart(text), "<system-reminder>")
}

// contentText returns the text of a message content value: a plain string, or a
// string built from the text blocks of a content-part array.
func contentText(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s
	}
	var blocks []map[string]json.RawMessage
	if err := json.Unmarshal(raw, &blocks); err != nil {
		return ""
	}
	out := ""
	for _, b := range blocks {
		if typ := mustString(b, "type"); typ == "text" {
			if v, ok := str(b, "text"); ok {
				out += v
			}
		}
	}
	return out
}

// trimSpaceStart trims leading characters the way String.prototype.trimStart
// does for the ASCII whitespace that appears before a reminder tag.
func trimSpaceStart(s string) string {
	for len(s) > 0 {
		switch s[0] {
		case ' ', '\t', '\n', '\r', '\v', '\f':
			s = s[1:]
			continue
		}
		break
	}
	return s
}

// messageContent ports stringifyForEstimation({role, content, toolCalls,
// toolCallId, toolName}) with undefined fields omitted.
func messageContent(m ContextUsageMessage) json.RawMessage {
	o := newObj()
	o.setStr(true, "role", m.Role)
	if len(m.Content) > 0 {
		o.set("content", v4pub.RawMessageOf(m.Content))
	}
	if len(m.ToolCalls) > 0 {
		o.set("toolCalls", v4pub.RawMessageOf(m.ToolCalls))
	}
	if m.ToolCallID != "" {
		o.set("toolCallId", v4pub.Str(m.ToolCallID))
	}
	if m.ToolName != "" {
		o.set("toolName", v4pub.Str(m.ToolName))
	}
	return o.raw()
}

// isMCPToolName ports parseMcpToolName presence: a name starting with "mcp__".
func isMCPToolName(name string) bool {
	if !strings.HasPrefix(name, "mcp__") {
		return false
	}
	parts := strings.Split(name, "__")
	// ["", serverName, ...toolParts]; need serverName and >=1 tool part.
	return len(parts) >= 3 && parts[1] != "" && strings.Join(parts[2:], "__") != ""
}

// estimateTokenCharDivisor is ESTIMATED_TOKEN_CHAR_DIVISOR
// (packages/shared/src/usage-stats.ts).
const estimateTokenCharDivisor = 3

// estimateTokens ports core/src/context/utils.estimateTokens.
// Chinese chars count double; divide by ESTIMATED_TOKEN_CHAR_DIVISOR, ceil.
func estimateTokens(text string) int64 {
	chinese := int64(0)
	total := int64(0)
	for _, r := range text {
		total++
		if r >= 0x4E00 && r <= 0x9FFF {
			chinese++
		}
	}
	other := total - chinese
	num := chinese*2 + other
	return (num + estimateTokenCharDivisor - 1) / estimateTokenCharDivisor
}
