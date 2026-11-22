package ai

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"
)

// OpenAIChatConfig configures the OpenAI-compatible /v1/chat/completions streaming provider.
// It supports OpenAI, DeepSeek, GLM, Qwen, OpenRouter, Ollama, vLLM, and standard OpenAI proxies.
type OpenAIChatConfig struct {
	BaseURL      string
	APIKey       string
	HTTPClient   *http.Client
	ExtraHeaders map[string]string
}

// OpenAIChatProvider implements Provider over the OpenAI Chat Completions SSE API.
type OpenAIChatProvider struct {
	baseURL      string
	apiKey       string
	client       *http.Client
	extraHeaders map[string]string
}

// NewOpenAIChatProvider constructs a streaming Chat Completions provider.
func NewOpenAIChatProvider(cfg OpenAIChatConfig) *OpenAIChatProvider {
	base := strings.TrimRight(strings.TrimSpace(cfg.BaseURL), "/")
	if base == "" {
		base = "https://api.openai.com/v1"
	}
	client := cfg.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: 0}
	}
	return &OpenAIChatProvider{
		baseURL:      base,
		apiKey:       cfg.APIKey,
		client:       client,
		extraHeaders: cfg.ExtraHeaders,
	}
}

func (p *OpenAIChatProvider) Driver() Driver {
	return DriverOpenAIChat
}

func (p *OpenAIChatProvider) endpoint() string {
	if strings.HasSuffix(p.baseURL, "/chat/completions") {
		return p.baseURL
	}
	if strings.HasSuffix(p.baseURL, "/v1") {
		return p.baseURL + "/chat/completions"
	}
	return p.baseURL + "/chat/completions"
}

func (p *OpenAIChatProvider) Stream(ctx context.Context, req *StreamRequest) (<-chan StreamEvent, error) {
	payload, err := buildChatCompletionsPayload(req)
	if err != nil {
		return nil, err
	}
	rawBody, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("ai: marshal chat completions request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, p.endpoint(), bytes.NewReader(rawBody))
	if err != nil {
		return nil, fmt.Errorf("ai: create chat completions request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "text/event-stream")
	httpReq.Header.Set("User-Agent", DefaultUserAgent)
	if p.apiKey != "" {
		httpReq.Header.Set("Authorization", "Bearer "+p.apiKey)
	}
	for k, v := range p.extraHeaders {
		httpReq.Header.Set(k, v)
	}

	resp, err := p.client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("ai: chat completions request failed: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		defer resp.Body.Close()
		errBody, _ := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
		return nil, fmt.Errorf("ai: chat completions API HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(errBody)))
	}

	ch := make(chan StreamEvent, 64)
	go p.consumeStream(ctx, resp.Body, ch)
	return ch, nil
}

func buildChatCompletionsPayload(req *StreamRequest) (map[string]any, error) {
	out := map[string]any{
		"model":          req.Model,
		"stream":         true,
		"stream_options": map[string]any{"include_usage": true},
	}

	if req.MaxOutputTokens > 0 {
		out["max_tokens"] = req.MaxOutputTokens
	}
	if effortVal, active := ResolveWireEffort(req, []string{"minimal", "low", "medium", "high"}); active {
		out["reasoning_effort"] = effortVal
	} else if effortVal != "" {
		out["reasoning_effort"] = effortVal
	} else if req.Temperature != nil {
		out["temperature"] = *req.Temperature
	}

	if len(req.Tools) > 0 {
		toolDefs := make([]map[string]any, 0, len(req.Tools))
		for _, t := range req.Tools {
			schema := t.InputSchema
			if len(schema) == 0 {
				schema = json.RawMessage(`{"type":"object","properties":{}}`)
			}
			toolDefs = append(toolDefs, map[string]any{
				"type": "function",
				"function": map[string]any{
					"name":        t.Name,
					"description": t.Description,
					"parameters":  schema,
				},
			})
		}
		out["tools"] = toolDefs
	}

	var msgs []map[string]any
	if sys := req.FullSystemPrompt(); sys != "" {
		msgs = append(msgs, map[string]any{
			"role":    "system",
			"content": sys,
		})
	}

	var pendingToolImageParts []map[string]any
	flushToolImages := func() {
		if len(pendingToolImageParts) == 0 {
			return
		}
		parts := make([]map[string]any, 0, len(pendingToolImageParts)+1)
		parts = append(parts, map[string]any{
			"type": "text",
			"text": "[Tool screenshot attachment]",
		})
		parts = append(parts, pendingToolImageParts...)
		msgs = append(msgs, map[string]any{
			"role":    "user",
			"content": parts,
		})
		pendingToolImageParts = nil
	}

	for _, m := range req.Messages {
		if m.Role != RoleToolResult {
			flushToolImages()
		}
		switch m.Role {
		case RoleUser:
			hasImage := false
			for _, b := range m.Content {
				if b.Type == BlockImage {
					hasImage = true
					break
				}
			}
			if !hasImage {
				msgs = append(msgs, map[string]any{
					"role":    "user",
					"content": m.PlainText(),
				})
				continue
			}
			var parts []map[string]any
			for _, b := range m.Content {
				switch b.Type {
				case BlockText:
					parts = append(parts, map[string]any{"type": "text", "text": b.Text})
				case BlockImage:
					imgURL := b.ImageURL
					if imgURL == "" && b.DataB64 != "" {
						mime := b.MimeType
						if mime == "" {
							mime = "image/png"
						}
						imgURL = fmt.Sprintf("data:%s;base64,%s", mime, b.DataB64)
					}
					if imgURL != "" {
						parts = append(parts, map[string]any{
							"type":      "image_url",
							"image_url": map[string]string{"url": imgURL},
						})
					}
				}
			}
			msgs = append(msgs, map[string]any{
				"role":    "user",
				"content": parts,
			})

		case RoleAssistant:
			var textParts []string
			var reasoningParts []string
			var toolCalls []map[string]any
			for _, b := range m.Content {
				switch b.Type {
				case BlockText:
					if b.Text != "" {
						textParts = append(textParts, b.Text)
					}
				case BlockThinking:
					if b.Thinking != "" {
						reasoningParts = append(reasoningParts, b.Thinking)
					}
				case BlockToolCall:
					argStr := "{}"
					if len(b.Arguments) > 0 {
						argStr = string(b.Arguments)
					}
					toolCalls = append(toolCalls, map[string]any{
						"id":   b.ToolCallID,
						"type": "function",
						"function": map[string]any{
							"name":      b.ToolName,
							"arguments": argStr,
						},
					})
				}
			}
			entry := map[string]any{"role": "assistant"}
			if len(textParts) > 0 {
				entry["content"] = strings.Join(textParts, "")
			} else if len(toolCalls) == 0 {
				entry["content"] = ""
			}
			if len(reasoningParts) > 0 {
				entry["reasoning_content"] = strings.Join(reasoningParts, "")
			}
			if len(toolCalls) > 0 {
				entry["tool_calls"] = toolCalls
			}
			msgs = append(msgs, entry)

		case RoleToolResult:
			msgs = append(msgs, map[string]any{
				"role":         "tool",
				"tool_call_id": m.ToolCallID,
				"content":      m.PlainText(),
			})
			for _, b := range m.Content {
				if b.Type != BlockImage {
					continue
				}
				imgURL := b.ImageURL
				if imgURL == "" && b.DataB64 != "" {
					mime := b.MimeType
					if mime == "" {
						mime = "image/png"
					}
					imgURL = fmt.Sprintf("data:%s;base64,%s", mime, b.DataB64)
				}
				if imgURL != "" {
					pendingToolImageParts = append(pendingToolImageParts, map[string]any{
						"type":      "image_url",
						"image_url": map[string]string{"url": imgURL},
					})
				}
			}
		}
	}
	flushToolImages()

	out["messages"] = msgs
	return out, nil
}

type chatCompletionChunk struct {
	Choices []struct {
		Index int `json:"index"`
		Delta struct {
			Content          string `json:"content"`
			ReasoningContent string `json:"reasoning_content"`
			Reasoning        string `json:"reasoning"`
			Thinking         string `json:"thinking"`
			ToolCalls        []struct {
				Index    int    `json:"index"`
				ID       string `json:"id"`
				Type     string `json:"type"`
				Function struct {
					Name      string `json:"name"`
					Arguments string `json:"arguments"`
				} `json:"function"`
			} `json:"tool_calls"`
		} `json:"delta"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
	Usage *struct {
		PromptTokens        int `json:"prompt_tokens"`
		CompletionTokens    int `json:"completion_tokens"`
		TotalTokens         int `json:"total_tokens"`
		PromptTokensDetails *struct {
			CachedTokens int `json:"cached_tokens"`
		} `json:"prompt_tokens_details"`
		CompletionTokensDetails *struct {
			ReasoningTokens int `json:"reasoning_tokens"`
		} `json:"completion_tokens_details"`
	} `json:"usage"`
	Error *struct {
		Message string `json:"message"`
		Type    string `json:"type"`
	} `json:"error"`
}

func (p *OpenAIChatProvider) consumeStream(ctx context.Context, body io.ReadCloser, ch chan<- StreamEvent) {
	defer close(ch)
	defer body.Close()

	reader := bufio.NewReaderSize(body, 64*1024)

	var textBuf strings.Builder
	var thinkingBuf strings.Builder
	var usage Usage
	var stopReason string

	type tcAcc struct {
		id      string
		name    string
		args    strings.Builder
		started bool
	}
	tcByIndex := make(map[int]*tcAcc)

	for {
		if ctx.Err() != nil {
			ch <- StreamEvent{Type: StreamEventError, Err: ctx.Err()}
			return
		}
		line, err := reader.ReadString('\n')
		if err != nil && err != io.EOF {
			ch <- StreamEvent{Type: StreamEventError, Err: fmt.Errorf("ai: read chat stream: %w", err)}
			return
		}
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "data:") {
			data := strings.TrimSpace(strings.TrimPrefix(trimmed, "data:"))
			if data == "[DONE]" {
				break
			}
			if data != "" {
				var chunk chatCompletionChunk
				if uErr := json.Unmarshal([]byte(data), &chunk); uErr != nil {
					continue
				}
				if chunk.Error != nil {
					ch <- StreamEvent{Type: StreamEventError, Err: fmt.Errorf("ai: chat stream error (%s): %s", chunk.Error.Type, chunk.Error.Message)}
					return
				}
				if chunk.Usage != nil {
					cached := 0
					if chunk.Usage.PromptTokensDetails != nil {
						cached = chunk.Usage.PromptTokensDetails.CachedTokens
					}
					usage.CacheReadTokens = cached
					uncached := chunk.Usage.PromptTokens - cached
					if uncached < 0 {
						uncached = 0
					}
					usage.InputTokens = uncached
					usage.OutputTokens = chunk.Usage.CompletionTokens
					if chunk.Usage.CompletionTokensDetails != nil {
						usage.ReasoningTokens = chunk.Usage.CompletionTokensDetails.ReasoningTokens
					}
				}
				for _, choice := range chunk.Choices {
					if choice.FinishReason != "" {
						stopReason = choice.FinishReason
					}
					reasonDelta := choice.Delta.ReasoningContent
					if reasonDelta == "" {
						reasonDelta = choice.Delta.Reasoning
					}
					if reasonDelta == "" {
						reasonDelta = choice.Delta.Thinking
					}
					if reasonDelta != "" {
						thinkingBuf.WriteString(reasonDelta)
						ch <- StreamEvent{Type: StreamEventThinkingDelta, ThinkingDelta: reasonDelta}
					}
					if choice.Delta.Content != "" {
						textBuf.WriteString(choice.Delta.Content)
						ch <- StreamEvent{Type: StreamEventTextDelta, TextDelta: choice.Delta.Content}
					}
					for _, tc := range choice.Delta.ToolCalls {
						acc := tcByIndex[tc.Index]
						if acc == nil {
							acc = &tcAcc{}
							tcByIndex[tc.Index] = acc
						}
						if tc.ID != "" {
							acc.id = tc.ID
						}
						if tc.Function.Name != "" {
							acc.name = tc.Function.Name
						}
						if !acc.started && acc.name != "" {
							acc.started = true
							ch <- StreamEvent{
								Type:       StreamEventToolCallStart,
								ToolCallID: acc.id,
								ToolName:   acc.name,
							}
						}
						if tc.Function.Arguments != "" {
							acc.args.WriteString(tc.Function.Arguments)
							ch <- StreamEvent{
								Type:       StreamEventToolCallDelta,
								ToolCallID: acc.id,
								ToolName:   acc.name,
								ArgsDelta:  tc.Function.Arguments,
							}
						}
					}
				}
			}
		}
		if err == io.EOF {
			break
		}
	}

	var blocks []ContentBlock
	if thinkingBuf.Len() > 0 {
		blocks = append(blocks, ContentBlock{
			Type:     BlockThinking,
			Thinking: thinkingBuf.String(),
		})
	}
	if textBuf.Len() > 0 {
		blocks = append(blocks, ContentBlock{
			Type: BlockText,
			Text: textBuf.String(),
		})
	}

	if len(tcByIndex) > 0 {
		indices := make([]int, 0, len(tcByIndex))
		for idx := range tcByIndex {
			indices = append(indices, idx)
		}
		sort.Ints(indices)
		for _, idx := range indices {
			acc := tcByIndex[idx]
			rawArgs := strings.TrimSpace(acc.args.String())
			if rawArgs == "" {
				rawArgs = "{}"
			}
			callID := acc.id
			if callID == "" {
				callID = fmt.Sprintf("call_%d", idx)
			}
			ch <- StreamEvent{
				Type:       StreamEventToolCallEnd,
				ToolCallID: callID,
				ToolName:   acc.name,
			}
			blocks = append(blocks, ContentBlock{
				Type:       BlockToolCall,
				ToolCallID: callID,
				ToolName:   acc.name,
				Arguments:  json.RawMessage(rawArgs),
			})
		}
	}

	normalizedStop := StopReasonEndTurn
	switch stopReason {
	case "tool_calls", "function_call":
		normalizedStop = StopReasonToolUse
	case "length":
		normalizedStop = StopReasonMaxTokens
	}
	if len(tcByIndex) > 0 && normalizedStop == StopReasonEndTurn {
		normalizedStop = StopReasonToolUse
	}

	msg := &Message{
		Role:      RoleAssistant,
		Content:   blocks,
		Timestamp: time.Now().UnixMilli(),
	}
	ch <- StreamEvent{
		Type:       StreamEventDone,
		Message:    msg,
		Usage:      usage,
		StopReason: normalizedStop,
	}
}
