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

// OpenAIResponsesConfig configures the OpenAI / Codex /v1/responses streaming adapter.
type OpenAIResponsesConfig struct {
	BaseURL      string
	APIKey       string
	HTTPClient   *http.Client
	ExtraHeaders map[string]string
}

// OpenAIResponsesProvider implements Provider for the OpenAI / Codex Responses API.
type OpenAIResponsesProvider struct {
	cfg OpenAIResponsesConfig
}

// NewOpenAIResponsesProvider constructs a new OpenAIResponsesProvider.
func NewOpenAIResponsesProvider(cfg OpenAIResponsesConfig) *OpenAIResponsesProvider {
	if strings.TrimSpace(cfg.BaseURL) == "" {
		cfg.BaseURL = "https://api.openai.com/v1"
	}
	if cfg.HTTPClient == nil {
		cfg.HTTPClient = http.DefaultClient
	}
	return &OpenAIResponsesProvider{cfg: cfg}
}

// Driver returns DriverOpenAIResponses.
func (p *OpenAIResponsesProvider) Driver() Driver {
	return DriverOpenAIResponses
}

func (p *OpenAIResponsesProvider) endpointURL() string {
	base := strings.TrimRight(strings.TrimSpace(p.cfg.BaseURL), "/")
	if strings.HasSuffix(base, "/responses") {
		return base
	}
	return base + "/responses"
}

// Stream sends a streaming /v1/responses request and emits normalized StreamEvents.
func (p *OpenAIResponsesProvider) Stream(ctx context.Context, req *StreamRequest) (<-chan StreamEvent, error) {
	if req == nil {
		return nil, errors.New("ai: nil StreamRequest")
	}

	payload, err := buildResponsesPayload(req)
	if err != nil {
		return nil, err
	}

	bodyBytes, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("ai: marshal responses request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, p.endpointURL(), bytes.NewReader(bodyBytes))
	if err != nil {
		return nil, fmt.Errorf("ai: build http request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "text/event-stream")
	httpReq.Header.Set("User-Agent", DefaultUserAgent)
	if p.cfg.APIKey != "" {
		httpReq.Header.Set("Authorization", "Bearer "+p.cfg.APIKey)
	}
	for k, v := range p.cfg.ExtraHeaders {
		httpReq.Header.Set(k, v)
	}

	resp, err := p.cfg.HTTPClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("ai: responses http request failed: %w", err)
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		defer resp.Body.Close()
		errBody, _ := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
		return nil, fmt.Errorf("ai: responses API HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(errBody)))
	}

	ch := make(chan StreamEvent, 64)
	go p.consumeStream(ctx, resp.Body, ch)
	return ch, nil
}

func buildResponsesPayload(req *StreamRequest) (map[string]any, error) {
	storeFalse := false
	out := map[string]any{
		"model":  req.Model,
		"stream": true,
		"store":  storeFalse,
	}

	if sys := req.FullSystemPrompt(); sys != "" {
		out["instructions"] = sys
	}
	if req.MaxOutputTokens > 0 {
		out["max_output_tokens"] = req.MaxOutputTokens
	}
	if req.PromptCacheKey != "" {
		out["prompt_cache_key"] = req.PromptCacheKey
	}

	if effortVal, active := ResolveWireEffort(req, []string{"minimal", "low", "medium", "high", "xhigh"}); active {
		out["reasoning"] = map[string]any{
			"effort":  effortVal,
			"summary": "auto",
		}
		out["include"] = []string{"reasoning.encrypted_content"}
	} else if effortVal != "" {
		// Explicit off mapping via ReasoningMap (e.g. {"off": "none"})
		out["reasoning"] = map[string]any{"effort": effortVal}
	} else if req.Temperature != nil {
		out["temperature"] = *req.Temperature
	}

	if len(req.Tools) > 0 {
		tools := make([]map[string]any, 0, len(req.Tools))
		for _, t := range req.Tools {
			schema := t.InputSchema
			if len(schema) == 0 {
				schema = json.RawMessage(`{"type":"object","properties":{}}`)
			}
			tools = append(tools, map[string]any{
				"type":        "function",
				"name":        t.Name,
				"description": t.Description,
				"parameters":  schema,
			})
		}
		out["tools"] = tools
	}

	var inputItems []any
	for _, m := range req.Messages {
		switch m.Role {
		case RoleUser:
			var parts []any
			for _, b := range m.Content {
				switch b.Type {
				case BlockText:
					parts = append(parts, map[string]any{
						"type": "input_text",
						"text": b.Text,
					})
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
							"type":      "input_image",
							"image_url": imgURL,
						})
					}
				}
			}
			if len(parts) > 0 {
				inputItems = append(inputItems, map[string]any{
					"type":    "message",
					"role":    "user",
					"content": parts,
				})
			}

		case RoleAssistant:
			for _, b := range m.Content {
				switch b.Type {
				case BlockThinking:
					if b.ReasoningItemID != "" || b.EncryptedContent != "" {
						item := map[string]any{
							"type":    "reasoning",
							"summary": []any{},
						}
						if b.ReasoningItemID != "" {
							item["id"] = b.ReasoningItemID
						}
						if b.EncryptedContent != "" {
							item["encrypted_content"] = b.EncryptedContent
						}
						if b.Thinking != "" {
							item["summary"] = []any{
								map[string]any{"type": "summary_text", "text": b.Thinking},
							}
						}
						inputItems = append(inputItems, item)
					}
				case BlockText:
					if b.Text != "" {
						inputItems = append(inputItems, map[string]any{
							"type": "message",
							"role": "assistant",
							"content": []any{
								map[string]any{"type": "output_text", "text": b.Text},
							},
						})
					}
				case BlockToolCall:
					argsStr := string(b.Arguments)
					if strings.TrimSpace(argsStr) == "" {
						argsStr = "{}"
					}
					fc := map[string]any{
						"type":      "function_call",
						"call_id":   b.ToolCallID,
						"name":      b.ToolName,
						"arguments": argsStr,
					}
					if b.ProviderItemID != "" {
						fc["id"] = b.ProviderItemID
					}
					inputItems = append(inputItems, fc)
				}
			}

		case RoleToolResult:
			var imgParts []map[string]any
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
					imgParts = append(imgParts, map[string]any{
						"type":      "input_image",
						"image_url": imgURL,
					})
				}
			}
			var outputVal any = m.PlainText()
			if len(imgParts) > 0 {
				items := make([]map[string]any, 0, len(imgParts)+1)
				if txt := m.PlainText(); txt != "" {
					items = append(items, map[string]any{
						"type": "input_text",
						"text": txt,
					})
				}
				items = append(items, imgParts...)
				outputVal = items
			}
			inputItems = append(inputItems, map[string]any{
				"type":    "function_call_output",
				"call_id": m.ToolCallID,
				"output":  outputVal,
			})
		}
	}

	if inputItems == nil {
		inputItems = []any{}
	}
	out["input"] = inputItems
	return out, nil
}

type pendingFuncCall struct {
	index   int
	itemID  string
	callID  string
	name    string
	argsBuf strings.Builder
}

type pendingReasoning struct {
	itemID           string
	encryptedContent string
	textBuf          strings.Builder
}

func (p *OpenAIResponsesProvider) consumeStream(ctx context.Context, body io.ReadCloser, ch chan<- StreamEvent) {
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
	var textBuf strings.Builder
	funcCallsByItem := make(map[string]*pendingFuncCall)
	funcCallsByIdx := make(map[int]*pendingFuncCall)
	reasoningByIdx := make(map[int]*pendingReasoning)
	var assembledBlocks []ContentBlock
	toolCallCount := 0
	doneEmitted := false
	var finalUsage Usage
	finalStop := StopReasonEndTurn

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
		if data == "" || data == "[DONE]" {
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

		outIdx := intField(ev, "output_index")

		switch evType {
		case "response.output_item.added":
			item, _ := ev["item"].(map[string]any)
			if item == nil {
				continue
			}
			itemType, _ := item["type"].(string)
			itemID, _ := item["id"].(string)
			switch itemType {
			case "function_call":
				callID, _ := item["call_id"].(string)
				if callID == "" {
					callID = itemID
				}
				name, _ := item["name"].(string)
				fc := &pendingFuncCall{
					index:  toolCallCount,
					itemID: itemID,
					callID: callID,
					name:   name,
				}
				toolCallCount++
				if itemID != "" {
					funcCallsByItem[itemID] = fc
				}
				funcCallsByIdx[outIdx] = fc
				if !emit(StreamEvent{
					Type:          StreamEventToolCallStart,
					ToolCallIndex: fc.index,
					ToolCallID:    fc.callID,
					ToolName:      fc.name,
				}) {
					return
				}
			case "reasoning":
				enc, _ := item["encrypted_content"].(string)
				reasoningByIdx[outIdx] = &pendingReasoning{
					itemID:           itemID,
					encryptedContent: enc,
				}
			}

		case "response.output_text.delta":
			delta, _ := ev["delta"].(string)
			if delta != "" {
				textBuf.WriteString(delta)
				if !emit(StreamEvent{Type: StreamEventTextDelta, TextDelta: delta}) {
					return
				}
			}

		case "response.reasoning_summary_text.delta", "response.reasoning_text.delta", "response.reasoning_summary.delta", "response.reasoning.delta":
			delta, _ := ev["delta"].(string)
			if delta != "" {
				r := reasoningByIdx[outIdx]
				if r == nil {
					r = &pendingReasoning{}
					reasoningByIdx[outIdx] = r
				}
				r.textBuf.WriteString(delta)
				if !emit(StreamEvent{Type: StreamEventThinkingDelta, ThinkingDelta: delta}) {
					return
				}
			}

		case "response.reasoning_summary_text.done", "response.reasoning_text.done":
			fullText, _ := ev["text"].(string)
			if fullText != "" {
				r := reasoningByIdx[outIdx]
				if r == nil {
					r = &pendingReasoning{}
					reasoningByIdx[outIdx] = r
				}
				if r.textBuf.Len() == 0 {
					r.textBuf.WriteString(fullText)
					if !emit(StreamEvent{Type: StreamEventThinkingDelta, ThinkingDelta: fullText}) {
						return
					}
				}
			}

		case "response.function_call_arguments.delta":
			delta, _ := ev["delta"].(string)
			itemID, _ := ev["item_id"].(string)
			fc := funcCallsByItem[itemID]
			if fc == nil {
				fc = funcCallsByIdx[outIdx]
			}
			if fc != nil && delta != "" {
				fc.argsBuf.WriteString(delta)
				if !emit(StreamEvent{
					Type:          StreamEventToolCallDelta,
					ToolCallIndex: fc.index,
					ToolCallID:    fc.callID,
					ToolName:      fc.name,
					ArgsDelta:     delta,
				}) {
					return
				}
			}

		case "response.output_item.done":
			item, _ := ev["item"].(map[string]any)
			if item == nil {
				continue
			}
			itemType, _ := item["type"].(string)
			itemID, _ := item["id"].(string)

			switch itemType {
			case "reasoning":
				r := reasoningByIdx[outIdx]
				if r == nil {
					r = &pendingReasoning{itemID: itemID}
				}
				if itemID != "" {
					r.itemID = itemID
				}
				if enc, _ := item["encrypted_content"].(string); enc != "" {
					r.encryptedContent = enc
				}
				thinkingText := r.textBuf.String()
				if thinkingText == "" {
					var sb strings.Builder
					if summaries, ok := item["summary"].([]any); ok {
						for _, s := range summaries {
							if sm, ok := s.(map[string]any); ok {
								if t, _ := sm["text"].(string); t != "" {
									sb.WriteString(t)
								}
							}
						}
					}
					if sb.Len() == 0 {
						if contents, ok := item["content"].([]any); ok {
							for _, c := range contents {
								if cm, ok := c.(map[string]any); ok {
									if t, _ := cm["text"].(string); t != "" {
										sb.WriteString(t)
									}
								}
							}
						}
					}
					thinkingText = sb.String()
				}
				delete(reasoningByIdx, outIdx)
				assembledBlocks = append(assembledBlocks, ContentBlock{
					Type:             BlockThinking,
					Thinking:         thinkingText,
					ReasoningItemID:  r.itemID,
					EncryptedContent: r.encryptedContent,
				})

			case "message":
				msgText := extractResponsesMessageText(item)
				if msgText == "" && textBuf.Len() > 0 {
					msgText = textBuf.String()
				}
				textBuf.Reset()
				if msgText != "" {
					assembledBlocks = append(assembledBlocks, TextBlock(msgText))
				}

			case "function_call":
				fc := funcCallsByItem[itemID]
				if fc == nil {
					fc = funcCallsByIdx[outIdx]
				}
				callID, _ := item["call_id"].(string)
				name, _ := item["name"].(string)
				argsStr, _ := item["arguments"].(string)
				idx := 0
				if fc != nil {
					idx = fc.index
					if callID == "" {
						callID = fc.callID
					}
					if name == "" {
						name = fc.name
					}
					if argsStr == "" {
						argsStr = fc.argsBuf.String()
					}
				}
				if callID == "" {
					callID = itemID
				}
				rawArgs := normalizeJSONArgs(argsStr)
				block := ContentBlock{
					Type:           BlockToolCall,
					ToolCallID:     callID,
					ProviderItemID: itemID,
					ToolName:       name,
					Arguments:      rawArgs,
				}
				assembledBlocks = append(assembledBlocks, block)
				blockCopy := block
				if !emit(StreamEvent{
					Type:          StreamEventToolCallEnd,
					ToolCallIndex: idx,
					ToolCallID:    callID,
					ToolName:      name,
					ToolCall:      &blockCopy,
				}) {
					return
				}
			}

		case "response.completed", "response.incomplete":
			respObj, _ := ev["response"].(map[string]any)
			finalUsage = parseResponsesUsage(respObj)
			for idx, r := range reasoningByIdx {
				if r != nil && (r.textBuf.Len() > 0 || r.encryptedContent != "") {
					assembledBlocks = append([]ContentBlock{{
						Type:             BlockThinking,
						Thinking:         r.textBuf.String(),
						ReasoningItemID:  r.itemID,
						EncryptedContent: r.encryptedContent,
					}}, assembledBlocks...)
				}
				delete(reasoningByIdx, idx)
			}
			if textBuf.Len() > 0 {
				assembledBlocks = append(assembledBlocks, TextBlock(textBuf.String()))
				textBuf.Reset()
			}
			hasToolCall := false
			for _, b := range assembledBlocks {
				if b.Type == BlockToolCall {
					hasToolCall = true
					break
				}
			}
			if hasToolCall {
				finalStop = StopReasonToolUse
			} else if evType == "response.incomplete" {
				finalStop = StopReasonMaxTokens
			} else {
				finalStop = StopReasonEndTurn
			}

			msg := &Message{
				Role:    RoleAssistant,
				Content: assembledBlocks,
			}
			doneEmitted = true
			emit(StreamEvent{
				Type:       StreamEventDone,
				Message:    msg,
				Usage:      finalUsage,
				StopReason: finalStop,
			})
			return

		case "response.failed", "error":
			errMsg := extractResponsesError(ev)
			emit(StreamEvent{
				Type:       StreamEventError,
				StopReason: StopReasonError,
				Err:        errors.New(errMsg),
			})
			return
		}
	}

	if !doneEmitted {
		if textBuf.Len() > 0 {
			assembledBlocks = append(assembledBlocks, TextBlock(textBuf.String()))
		}
		hasToolCall := false
		for _, b := range assembledBlocks {
			if b.Type == BlockToolCall {
				hasToolCall = true
				break
			}
		}
		if hasToolCall {
			finalStop = StopReasonToolUse
		}
		emit(StreamEvent{
			Type: StreamEventDone,
			Message: &Message{
				Role:    RoleAssistant,
				Content: assembledBlocks,
			},
			Usage:      finalUsage,
			StopReason: finalStop,
		})
	}
}

func parseResponsesUsage(respObj map[string]any) Usage {
	if respObj == nil {
		return Usage{}
	}
	uMap, _ := respObj["usage"].(map[string]any)
	if uMap == nil {
		return Usage{}
	}
	totalInput := intField(uMap, "input_tokens")
	output := intField(uMap, "output_tokens")
	cached := 0
	cacheWrite := 0
	if inDetails, ok := uMap["input_tokens_details"].(map[string]any); ok {
		cached = intField(inDetails, "cached_tokens")
		cacheWrite = intField(inDetails, "cache_write_tokens")
	}
	reasoning := 0
	if outDetails, ok := uMap["output_tokens_details"].(map[string]any); ok {
		reasoning = intField(outDetails, "reasoning_tokens")
	}
	uncached := totalInput - cached - cacheWrite
	if uncached < 0 {
		uncached = 0
	}
	return Usage{
		InputTokens:      uncached,
		OutputTokens:     output,
		ReasoningTokens:  reasoning,
		CacheReadTokens:  cached,
		CacheWriteTokens: cacheWrite,
	}
}

func extractResponsesMessageText(item map[string]any) string {
	content, ok := item["content"].([]any)
	if !ok {
		return ""
	}
	var sb strings.Builder
	for _, c := range content {
		cm, ok := c.(map[string]any)
		if !ok {
			continue
		}
		if t, _ := cm["text"].(string); t != "" {
			sb.WriteString(t)
		}
	}
	return sb.String()
}

func extractResponsesError(ev map[string]any) string {
	if errObj, ok := ev["error"].(map[string]any); ok {
		if msg, _ := errObj["message"].(string); msg != "" {
			return "responses stream error: " + msg
		}
	}
	if respObj, ok := ev["response"].(map[string]any); ok {
		if errObj, ok := respObj["error"].(map[string]any); ok {
			if msg, _ := errObj["message"].(string); msg != "" {
				return "responses failed: " + msg
			}
		}
	}
	return "responses stream failed"
}

func intField(m map[string]any, key string) int {
	if m == nil {
		return 0
	}
	switch v := m[key].(type) {
	case float64:
		return int(v)
	case int:
		return v
	default:
		return 0
	}
}

func normalizeJSONArgs(raw string) json.RawMessage {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return json.RawMessage(`{}`)
	}
	if json.Valid([]byte(trimmed)) {
		return json.RawMessage(trimmed)
	}
	b, _ := json.Marshal(map[string]string{"_raw": trimmed})
	return json.RawMessage(b)
}
