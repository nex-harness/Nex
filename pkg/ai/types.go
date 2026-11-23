package ai

import (
	"context"
	"encoding/json"
)

// DefaultUserAgent is sent on all outbound HTTP requests to LLM providers and model catalogs.
const DefaultUserAgent = "Nex/0.3.0"

// Driver identifies the underlying wire protocol used by a Provider.
type Driver string

const (
	DriverOpenAIResponses Driver = "openai_responses" // OpenAI / Codex /v1/responses API
	DriverOpenAIChat      Driver = "openai_chat"      // OpenAI-compatible /v1/chat/completions API
	DriverAnthropic       Driver = "anthropic"        // Anthropic /v1/messages API
)

// Role represents a message participant role in the unified conversation model.
type Role string

const (
	RoleUser       Role = "user"
	RoleAssistant  Role = "assistant"
	RoleToolResult Role = "tool_result"
)

// BlockType discriminates unified content blocks inside a Message.
type BlockType string

const (
	BlockText     BlockType = "text"
	BlockThinking BlockType = "thinking"
	BlockImage    BlockType = "image"
	BlockToolCall BlockType = "tool_call"
)

// ContentBlock represents a polymorphic unit of message content.
type ContentBlock struct {
	Type BlockType `json:"type"`

	// Text payload (for BlockText, or textual tool output inside RoleToolResult)
	Text string `json:"text,omitempty"`

	// Thinking / Reasoning payload (for BlockThinking)
	Thinking string `json:"thinking,omitempty"`
	// Signature preserves Anthropic thinking signatures across multi-turn tool loops.
	Signature string `json:"signature,omitempty"`
	// ReasoningItemID and EncryptedContent preserve OpenAI Codex Responses reasoning state.
	ReasoningItemID  string `json:"reasoning_item_id,omitempty"`
	EncryptedContent string `json:"encrypted_content,omitempty"`

	// Multimodal image payload (for BlockImage)
	MimeType string `json:"mime_type,omitempty"`
	DataB64  string `json:"data_b64,omitempty"`
	ImageURL string `json:"image_url,omitempty"`

	// Tool invocation payload (for BlockToolCall)
	ToolCallID string          `json:"tool_call_id,omitempty"`
	ToolName   string          `json:"tool_name,omitempty"`
	Arguments  json.RawMessage `json:"arguments,omitempty"`
	// ProviderItemID preserves provider-specific item IDs (e.g., fc_... in Responses API).
	ProviderItemID string `json:"provider_item_id,omitempty"`
}

// TextBlock creates a text ContentBlock.
func TextBlock(text string) ContentBlock {
	return ContentBlock{Type: BlockText, Text: text}
}

// ThinkingBlock creates a reasoning/thinking ContentBlock.
func ThinkingBlock(thinking, signature string) ContentBlock {
	return ContentBlock{Type: BlockThinking, Thinking: thinking, Signature: signature}
}

// ImageBase64Block creates an inline base64 image ContentBlock.
func ImageBase64Block(mimeType, dataB64 string) ContentBlock {
	return ContentBlock{Type: BlockImage, MimeType: mimeType, DataB64: dataB64}
}

// ToolCallBlock creates a structured tool invocation ContentBlock.
func ToolCallBlock(id, name string, args json.RawMessage) ContentBlock {
	if len(args) == 0 {
		args = json.RawMessage(`{}`)
	}
	return ContentBlock{
		Type:       BlockType(BlockToolCall),
		ToolCallID: id,
		ToolName:   name,
		Arguments:  args,
	}
}

// Message represents a single turn entry in a conversation history.
type Message struct {
	ID         string         `json:"id,omitempty"`
	Role       Role           `json:"role"`
	Content    []ContentBlock `json:"content"`
	ToolCallID string         `json:"tool_call_id,omitempty"` // Set when Role == RoleToolResult
	ToolName   string         `json:"tool_name,omitempty"`    // Set when Role == RoleToolResult
	IsError    bool           `json:"is_error,omitempty"`     // Set when Role == RoleToolResult
	Timestamp  int64          `json:"timestamp,omitempty"`    // Unix milliseconds
}

// NewUserTextMessage constructs a simple user text Message.
func NewUserTextMessage(text string) Message {
	return Message{
		Role:    RoleUser,
		Content: []ContentBlock{TextBlock(text)},
	}
}

// NewToolResultMessage constructs a RoleToolResult response to a ToolCallID.
func NewToolResultMessage(toolCallID, toolName, output string, isError bool, extraBlocks ...ContentBlock) Message {
	blocks := make([]ContentBlock, 0, 1+len(extraBlocks))
	if output != "" || len(extraBlocks) == 0 {
		blocks = append(blocks, TextBlock(output))
	}
	blocks = append(blocks, extraBlocks...)
	return Message{
		Role:       RoleToolResult,
		ToolCallID: toolCallID,
		ToolName:   toolName,
		IsError:    isError,
		Content:    blocks,
	}
}

// PlainText concatenates all text blocks in the message.
func (m Message) PlainText() string {
	var out string
	for _, b := range m.Content {
		if b.Type == BlockText {
			out += b.Text
		}
	}
	return out
}

// ToolDefinition describes a callable tool exposed to the LLM.
type ToolDefinition struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"input_schema"` // JSON Schema Draft-07/2020-12 object
	Category    string          `json:"category,omitempty"` // "system" | "mcp" | "plugin" (for Context Windows breakdown)
}

// ThinkingEffort controls model reasoning budget/effort across providers.
type ThinkingEffort string

const (
	ThinkingOff     ThinkingEffort = ""
	ThinkingMinimal ThinkingEffort = "minimal"
	ThinkingLow     ThinkingEffort = "low"
	ThinkingMedium  ThinkingEffort = "medium"
	ThinkingOn      ThinkingEffort = "on"
	ThinkingHigh    ThinkingEffort = "high"
	ThinkingXHigh   ThinkingEffort = "xhigh"
	ThinkingMax     ThinkingEffort = "max"
)

// SystemSection represents a categorized segment of the assembled system instructions,
// enabling exact 6-category Context Windows breakdown and Prompt Caching breakpoints.
type SystemSection struct {
	Category     string `json:"category"` // "system_prompt" | "skills" | "mcp_tools" | "meta_context"
	Name         string `json:"name,omitempty"`
	Content      string `json:"content"`
	CacheControl bool   `json:"cache_control,omitempty"`
}

// StreamRequest encapsulates a single provider-agnostic streaming completion request.
type StreamRequest struct {
	Model           string           `json:"model"`
	SystemPrompt    string           `json:"system_prompt,omitempty"`
	SystemSections  []SystemSection  `json:"system_sections,omitempty"`
	Messages        []Message        `json:"messages"`
	Tools           []ToolDefinition `json:"tools,omitempty"`
	MaxOutputTokens int              `json:"max_output_tokens,omitempty"`
	Temperature     *float64         `json:"temperature,omitempty"`
	ThinkingEffort  ThinkingEffort    `json:"thinking_effort,omitempty"`
	ReasoningLevels []string          `json:"reasoning_levels,omitempty"`
	ReasoningMap    map[string]string `json:"reasoning_map,omitempty"`
	PromptCacheKey  string            `json:"prompt_cache_key,omitempty"`
}

// FullSystemPrompt returns the combined system instructions from SystemSections and SystemPrompt.
func (r *StreamRequest) FullSystemPrompt() string {
	if len(r.SystemSections) == 0 {
		return r.SystemPrompt
	}
	var out string
	if r.SystemPrompt != "" {
		out = r.SystemPrompt
	}
	for _, sec := range r.SystemSections {
		if sec.Content == "" {
			continue
		}
		if out != "" {
			out += "\n\n"
		}
		out += sec.Content
	}
	return out
}

// Usage captures normalized token consumption and prompt cache metrics for a single turn.
type Usage struct {
	InputTokens      int `json:"input_tokens"`       // Non-cached input tokens
	OutputTokens     int `json:"output_tokens"`      // Output tokens (including reasoning)
	ReasoningTokens  int `json:"reasoning_tokens"`   // Subset of output tokens spent thinking
	CacheReadTokens  int     `json:"cache_read_tokens"`            // Tokens served from prompt cache
	CacheWriteTokens int     `json:"cache_write_tokens"`           // Tokens written into prompt cache
	CostUSD          float64 `json:"cost_usd,omitempty"`           // Optional provider-reported cost in USD (e.g. OpenRouter / System One)
}

// TotalInputTokens returns total prompt context tokens processed (uncached + cache read + cache write).
func (u Usage) TotalInputTokens() int {
	return u.InputTokens + u.CacheReadTokens + u.CacheWriteTokens
}

// CacheHitRatePct returns the percentage (0..100) of total input tokens served from cache.
func (u Usage) CacheHitRatePct() float64 {
	total := u.TotalInputTokens()
	if total <= 0 {
		return 0
	}
	return float64(u.CacheReadTokens) * 100.0 / float64(total)
}

// StopReason normalizes completion termination reasons across providers.
type StopReason string

const (
	StopReasonEndTurn   StopReason = "end_turn"
	StopReasonToolUse   StopReason = "tool_use"
	StopReasonMaxTokens StopReason = "max_tokens"
	StopReasonError     StopReason = "error"
)

// StreamEventType enumerates events emitted by a Provider.Stream channel.
type StreamEventType string

const (
	StreamEventStart         StreamEventType = "start"
	StreamEventTextDelta     StreamEventType = "text_delta"
	StreamEventThinkingDelta StreamEventType = "thinking_delta"
	StreamEventToolCallStart StreamEventType = "toolcall_start"
	StreamEventToolCallDelta StreamEventType = "toolcall_delta"
	StreamEventToolCallEnd   StreamEventType = "toolcall_end"
	StreamEventDone          StreamEventType = "done"
	StreamEventError         StreamEventType = "error"
)

// StreamEvent represents an incremental chunk or terminal result from an LLM stream.
type StreamEvent struct {
	Type StreamEventType `json:"type"`

	// Incremental deltas
	TextDelta     string `json:"text_delta,omitempty"`
	ThinkingDelta string `json:"thinking_delta,omitempty"`
	Signature     string `json:"signature,omitempty"`

	// Tool call streaming state
	ToolCallIndex int          `json:"tool_call_index,omitempty"`
	ToolCallID    string       `json:"tool_call_id,omitempty"`
	ToolName      string       `json:"tool_name,omitempty"`
	ArgsDelta     string       `json:"args_delta,omitempty"`
	ToolCall      *ContentBlock `json:"tool_call,omitempty"` // Populated on StreamEventToolCallEnd

	// Terminal fields (populated on StreamEventDone)
	Message    *Message   `json:"message,omitempty"`
	Usage      Usage      `json:"usage,omitempty"`
	StopReason StopReason `json:"stop_reason,omitempty"`

	// Error (populated on StreamEventError)
	Err error `json:"-"`
}

// Provider streams completions from an upstream LLM endpoint or server proxy.
type Provider interface {
	Driver() Driver
	Stream(ctx context.Context, req *StreamRequest) (<-chan StreamEvent, error)
}

// ModelPricing stores USD cost per 1,000,000 tokens (aligned with models.dev).
type ModelPricing struct {
	InputPer1M      float64 `json:"input_per_1m"`
	OutputPer1M     float64 `json:"output_per_1m"`
	CacheReadPer1M  float64 `json:"cache_read_per_1m"`
	CacheWritePer1M float64 `json:"cache_write_per_1m"`
}

// CostEstimate holds calculated USD spend and prompt-cache savings.
type CostEstimate struct {
	EstimatedCostUSD float64 `json:"estimated_cost_usd"`
	CacheSavedUSD    float64 `json:"cache_saved_usd"`
}

// CalculateCost computes exact USD cost and cache savings from Usage and ModelPricing.
func CalculateCost(u Usage, p ModelPricing) CostEstimate {
	inCost := float64(u.InputTokens) / 1_000_000.0 * p.InputPer1M
	outCost := float64(u.OutputTokens) / 1_000_000.0 * p.OutputPer1M
	cReadCost := float64(u.CacheReadTokens) / 1_000_000.0 * p.CacheReadPer1M
	cWriteCost := float64(u.CacheWriteTokens) / 1_000_000.0 * p.CacheWritePer1M

	actual := inCost + outCost + cReadCost + cWriteCost
	withoutCache := float64(u.InputTokens+u.CacheReadTokens+u.CacheWriteTokens)/1_000_000.0*p.InputPer1M + outCost
	saved := withoutCache - actual
	if saved < 0 {
		saved = 0
	}
	return CostEstimate{
		EstimatedCostUSD: actual,
		CacheSavedUSD:    saved,
	}
}
