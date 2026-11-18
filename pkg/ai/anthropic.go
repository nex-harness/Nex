package ai

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// AnthropicConfig configures the Anthropic /v1/messages streaming adapter.
type AnthropicConfig struct {
	BaseURL       string
	APIKey        string
	UseBearerAuth bool
	BetaHeaders   []string
	HTTPClient    *http.Client
	ExtraHeaders  map[string]string
}

// AnthropicProvider implements Provider for the Anthropic Messages API.
type AnthropicProvider struct {
	cfg AnthropicConfig
}

// NewAnthropicProvider constructs a new AnthropicProvider.
func NewAnthropicProvider(cfg AnthropicConfig) *AnthropicProvider {
	if strings.TrimSpace(cfg.BaseURL) == "" {
		cfg.BaseURL = "https://api.anthropic.com/v1"
	}
	if cfg.HTTPClient == nil {
		cfg.HTTPClient = http.DefaultClient
	}
	return &AnthropicProvider{cfg: cfg}
}

// Driver returns DriverAnthropic.
func (p *AnthropicProvider) Driver() Driver {
	return DriverAnthropic
}

func (p *AnthropicProvider) endpointURL() string {
	base := strings.TrimRight(strings.TrimSpace(p.cfg.BaseURL), "/")
	if strings.HasSuffix(base, "/messages") {
		return base
	}
	return base + "/messages"
}

// Stream sends a streaming /v1/messages request and emits normalized StreamEvents.
func (p *AnthropicProvider) Stream(ctx context.Context, req *StreamRequest) (<-chan StreamEvent, error) {
	if req == nil {
		return nil, errors.New("ai: nil StreamRequest")
	}

	payload, err := buildAnthropicPayload(req)
	if err != nil {
		return nil, err
	}

	bodyBytes, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("ai: marshal anthropic request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, p.endpointURL(), bytes.NewReader(bodyBytes))
	if err != nil {
		return nil, fmt.Errorf("ai: build anthropic http request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "text/event-stream")
	httpReq.Header.Set("anthropic-version", "2023-06-01")
	if p.cfg.APIKey != "" {
		if p.cfg.UseBearerAuth {
			httpReq.Header.Set("Authorization", "Bearer "+p.cfg.APIKey)
		} else {
			httpReq.Header.Set("x-api-key", p.cfg.APIKey)
		}
	}
	if len(p.cfg.BetaHeaders) > 0 {
		httpReq.Header.Set("anthropic-beta", strings.Join(p.cfg.BetaHeaders, ","))
	}
	for k, v := range p.cfg.ExtraHeaders {
		httpReq.Header.Set(k, v)
	}

	resp, err := p.cfg.HTTPClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("ai: anthropic http request failed: %w", err)
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		defer resp.Body.Close()
		errBody, _ := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
		return nil, fmt.Errorf("ai: anthropic API HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(errBody)))
	}

	ch := make(chan StreamEvent, 64)
	go p.consumeStream(ctx, resp.Body, ch)
	return ch, nil
}

func thinkingBudgetForEffort(effort ThinkingEffort) int {
	switch effort {
	case ThinkingLow:
		return 2048
	case ThinkingMedium:
		return 8192
	case ThinkingHigh:
		return 16000
	default:
		return 0
	}
}

func buildAnthropicPayload(req *StreamRequest) (map[string]any, error) {
	maxTokens := req.MaxOutputTokens
	if maxTokens <= 0 {
		maxTokens = 16384
	}

	out := map[string]any{
		"model":  req.Model,
		"stream": true,
	}

	budget := thinkingBudgetForEffort(req.ThinkingEffort)
	if budget > 0 {
		if maxTokens <= budget {
			maxTokens = budget + 8192
		}
		out["thinking"] = map[string]any{
			"type":          "enabled",
			"budget_tokens": budget,
		}
	} else if req.Temperature != nil {
		out["temperature"] = *req.Temperature
	}
	out["max_tokens"] = maxTokens

	// Build system prompt blocks with ephemeral cache breakpoints
	var sysBlocks []map[string]any
	if req.SystemPrompt != "" && len(req.SystemSections) == 0 {
		sysBlocks = append(sysBlocks, map[string]any{
			"type":          "text",
			"text":          req.SystemPrompt,
			"cache_control": map[string]string{"type": "ephemeral"},
		})
	} else {
		if req.SystemPrompt != "" {
			sysBlocks = append(sysBlocks, map[string]any{
				"type": "text",
				"text": req.SystemPrompt,
			})
		}
		for _, sec := range req.SystemSections {
			if sec.Content == "" {
				continue
			}
			b := map[string]any{
				"type": "text",
				"text": sec.Content,
			}
			if sec.CacheControl {
				b["cache_control"] = map[string]string{"type": "ephemeral"}
			}
			sysBlocks = append(sysBlocks, b)
		}
	}
	if len(sysBlocks) > 0 {
		out["system"] = sysBlocks
	}

	// Build tools list (mark last tool with ephemeral cache control)
	if len(req.Tools) > 0 {
		tools := make([]map[string]any, 0, len(req.Tools))
		for i, t := range req.Tools {
			schema := t.InputSchema
			if len(schema) == 0 {
				schema = json.RawMessage(`{"type":"object","properties":{}}`)
			}
			td := map[string]any{
				"name":         t.Name,
				"description":  t.Description,
				"input_schema": schema,
			}
			if i == len(req.Tools)-1 {
				td["cache_control"] = map[string]string{"type": "ephemeral"}
			}
			tools = append(tools, td)
		}
		out["tools"] = tools
	}

	// Build messages, coalescing consecutive user/tool_result roles as required by Anthropic
	type wireMsg struct {
		Role    string `json:"role"`
		Content []any  `json:"content"`
	}
	var wireMsgs []wireMsg
	appendMsg := func(role string, blocks []any) {
		if len(blocks) == 0 {
			return
		}
		if len(wireMsgs) > 0 && wireMsgs[len(wireMsgs)-1].Role == role {
			wireMsgs[len(wireMsgs)-1].Content = append(wireMsgs[len(wireMsgs)-1].Content, blocks...)
			return
		}
		wireMsgs = append(wireMsgs, wireMsg{Role: role, Content: blocks})
	}

	for _, m := range req.Messages {
		switch m.Role {
		case RoleUser:
			var blocks []any
			for _, b := range m.Content {
				switch b.Type {
				case BlockText:
					if b.Text != "" {
						blocks = append(blocks, map[string]any{"type": "text", "text": b.Text})
					}
				case BlockImage:
					if b.DataB64 != "" {
						mime := b.MimeType
						if mime == "" {
							mime = "image/png"
						}
						blocks = append(blocks, map[string]any{
							"type": "image",
							"source": map[string]any{
								"type":       "base64",
								"media_type": mime,
								"data":       b.DataB64,
							},
						})
					}
				}
			}
			appendMsg("user", blocks)

		case RoleAssistant:
			var blocks []any
			for _, b := range m.Content {
				switch b.Type {
				case BlockThinking:
					if b.Signature != "" {
						blocks = append(blocks, map[string]any{
							"type":      "thinking",
							"thinking":  b.Thinking,
							"signature": b.Signature,
						})
					}
				case BlockText:
					if b.Text != "" {
						blocks = append(blocks, map[string]any{"type": "text", "text": b.Text})
					}
				case BlockToolCall:
					var inputObj any = map[string]any{}
					if len(b.Arguments) > 0 {
						_ = json.Unmarshal(b.Arguments, &inputObj)
					}
					blocks = append(blocks, map[string]any{
						"type":  "tool_use",
						"id":    b.ToolCallID,
						"name":  b.ToolName,
						"input": inputObj,
					})
				}
			}
			appendMsg("assistant", blocks)

		case RoleToolResult:
			var resultContent []any
			for _, b := range m.Content {
				switch b.Type {
				case BlockText:
					resultContent = append(resultContent, map[string]any{"type": "text", "text": b.Text})
				case BlockImage:
					if b.DataB64 != "" {
						mime := b.MimeType
						if mime == "" {
							mime = "image/png"
						}
						resultContent = append(resultContent, map[string]any{
							"type": "image",
							"source": map[string]any{
								"type":       "base64",
								"media_type": mime,
								"data":       b.DataB64,
							},
						})
					}
				}
			}
			trBlock := map[string]any{
				"type":        "tool_result",
				"tool_use_id": m.ToolCallID,
				"content":     resultContent,
			}
			if m.IsError {
				trBlock["is_error"] = true
			}
			appendMsg("user", []any{trBlock})
		}
	}

	out["messages"] = wireMsgs
	return out, nil
}

type anthropicBlockAcc struct {
	blockType     string
	textBuf       strings.Builder
	thinkingBuf   strings.Builder
	signatureBuf  strings.Builder
	toolID        string
	toolName      string
	toolIndex     int
	toolJSONBuf   strings.Builder
}

func (p *AnthropicProvider) consumeStream(ctx context.Context, body io.ReadCloser, ch chan<- StreamEvent) {
	defer close(ch)
	defer body.Close()

	emit := func(ev StreamEvent) bool {
		select {
		case ch <- ev:
			return true
		case <-ctx.Done():
			return false
		}
	}

	if !emit(StreamEvent{Type: StreamEventStart}) {
		return
	}

	scanner := NewSSEScanner(body)
	blocksByIdx := make(map[int]*anthropicBlockAcc)
	var orderedIndices []int
	var usage Usage
	stopReason := StopReasonEndTurn
	toolSeq := 0
	doneEmitted := false

	for {
		frame, err := scanner.Next(ctx)
		if err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			emit(StreamEvent{Type: StreamEventError, StopReason: StopReasonError, Err: err})
			return
		}

		data := strings.TrimSpace(frame.Data)
		if data == "" {
			continue
		}

		var ev map[string]any
		if err := json.Unmarshal([]byte(data), &ev); err != nil {
			continue
		}
		evType, _ := ev["type"].(string)
		if evType == "" {
			evType = frame.Event
		}

		switch evType {
		case "message_start":
			if msgObj, ok := ev["message"].(map[string]any); ok {
				if uMap, ok := msgObj["usage"].(map[string]any); ok {
					usage.InputTokens = intField(uMap, "input_tokens")
					usage.OutputTokens = intField(uMap, "output_tokens")
					usage.CacheReadTokens = intField(uMap, "cache_read_input_tokens")
					usage.CacheWriteTokens = intField(uMap, "cache_creation_input_tokens")
				}
			}

		case "content_block_start":
			idx := intField(ev, "index")
			cb, _ := ev["content_block"].(map[string]any)
			if cb == nil {
				continue
			}
			cbType, _ := cb["type"].(string)
			acc := &anthropicBlockAcc{blockType: cbType}
			if cbType == "tool_use" {
				acc.toolID, _ = cb["id"].(string)
				acc.toolName, _ = cb["name"].(string)
				acc.toolIndex = toolSeq
				toolSeq++
				if !emit(StreamEvent{
					Type:          StreamEventToolCallStart,
					ToolCallIndex: acc.toolIndex,
					ToolCallID:    acc.toolID,
					ToolName:      acc.toolName,
				}) {
					return
				}
			}
			blocksByIdx[idx] = acc
			orderedIndices = append(orderedIndices, idx)

		case "content_block_delta":
			idx := intField(ev, "index")
			acc := blocksByIdx[idx]
			deltaObj, _ := ev["delta"].(map[string]any)
			if acc == nil || deltaObj == nil {
				continue
			}
			dType, _ := deltaObj["type"].(string)
			switch dType {
			case "text_delta":
				txt, _ := deltaObj["text"].(string)
				if txt != "" {
					acc.textBuf.WriteString(txt)
					if !emit(StreamEvent{Type: StreamEventTextDelta, TextDelta: txt}) {
						return
					}
				}
			case "thinking_delta":
				th, _ := deltaObj["thinking"].(string)
				if th != "" {
					acc.thinkingBuf.WriteString(th)
					if !emit(StreamEvent{Type: StreamEventThinkingDelta, ThinkingDelta: th}) {
						return
					}
				}
			case "signature_delta":
				sig, _ := deltaObj["signature"].(string)
				if sig != "" {
					acc.signatureBuf.WriteString(sig)
				}
			case "input_json_delta":
				part, _ := deltaObj["partial_json"].(string)
				if part != "" {
					acc.toolJSONBuf.WriteString(part)
					if !emit(StreamEvent{
						Type:          StreamEventToolCallDelta,
						ToolCallIndex: acc.toolIndex,
						ToolCallID:    acc.toolID,
						ToolName:      acc.toolName,
						ArgsDelta:     part,
					}) {
						return
					}
				}
			}

		case "content_block_stop":
			idx := intField(ev, "index")
			acc := blocksByIdx[idx]
			if acc != nil && acc.blockType == "tool_use" {
				rawArgs := normalizeJSONArgs(acc.toolJSONBuf.String())
				block := ToolCallBlock(acc.toolID, acc.toolName, rawArgs)
				if !emit(StreamEvent{
					Type:          StreamEventToolCallEnd,
					ToolCallIndex: acc.toolIndex,
					ToolCallID:    acc.toolID,
					ToolName:      acc.toolName,
					ToolCall:      &block,
				}) {
					return
				}
			}

		case "message_delta":
			if dObj, ok := ev["delta"].(map[string]any); ok {
				sr, _ := dObj["stop_reason"].(string)
				switch sr {
				case "tool_use":
					stopReason = StopReasonToolUse
				case "max_tokens":
					stopReason = StopReasonMaxTokens
				default:
					stopReason = StopReasonEndTurn
				}
			}
			if uMap, ok := ev["usage"].(map[string]any); ok {
				if outTok := intField(uMap, "output_tokens"); outTok > 0 {
					usage.OutputTokens = outTok
				}
			}

		case "message_stop":
			msg := assembleAnthropicMessage(orderedIndices, blocksByIdx)
			doneEmitted = true
			emit(StreamEvent{
				Type:       StreamEventDone,
				Message:    msg,
				Usage:      usage,
				StopReason: stopReason,
			})
			return

		case "error":
			errMsg := "anthropic stream error"
			if errObj, ok := ev["error"].(map[string]any); ok {
				if m, _ := errObj["message"].(string); m != "" {
					errMsg += ": " + m
				}
			}
			emit(StreamEvent{
				Type:       StreamEventError,
				StopReason: StopReasonError,
				Err:        errors.New(errMsg),
			})
			return
		}
	}

	if !doneEmitted {
		msg := assembleAnthropicMessage(orderedIndices, blocksByIdx)
		emit(StreamEvent{
			Type:       StreamEventDone,
			Message:    msg,
			Usage:      usage,
			StopReason: stopReason,
		})
	}
}

func assembleAnthropicMessage(orderedIndices []int, blocksByIdx map[int]*anthropicBlockAcc) *Message {
	var content []ContentBlock
	for _, idx := range orderedIndices {
		acc := blocksByIdx[idx]
		if acc == nil {
			continue
		}
		switch acc.blockType {
		case "thinking":
			content = append(content, ThinkingBlock(acc.thinkingBuf.String(), acc.signatureBuf.String()))
		case "text":
			if acc.textBuf.Len() > 0 {
				content = append(content, TextBlock(acc.textBuf.String()))
			}
		case "tool_use":
			content = append(content, ToolCallBlock(acc.toolID, acc.toolName, normalizeJSONArgs(acc.toolJSONBuf.String())))
		}
	}
	return &Message{
		Role:    RoleAssistant,
		Content: content,
	}
}
