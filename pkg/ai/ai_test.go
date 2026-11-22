package ai

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestSSEScannerLargeFrames(t *testing.T) {
	largeText := strings.Repeat("A", 150*1024) // 150KB > 64KB bufio.Scanner limit
	raw := fmt.Sprintf(": keepalive comment\n\nevent: chunk\ndata: %s\ndata: line2\n\n", largeText)

	sc := NewSSEScanner(strings.NewReader(raw))
	frame, err := sc.Next(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if frame.Event != "chunk" {
		t.Fatalf("expected event 'chunk', got %q", frame.Event)
	}
	if frame.Data != largeText+"\nline2" {
		t.Fatalf("data length mismatch: got %d", len(frame.Data))
	}

	_, err = sc.Next(context.Background())
	if err != io.EOF {
		t.Fatalf("expected io.EOF, got %v", err)
	}
}

func TestOpenAIResponsesProviderStreamAndRoundTrip(t *testing.T) {
	var capturedPayload map[string]any

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/responses" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer sk-test-codex" {
			t.Errorf("missing auth header: %s", r.Header.Get("Authorization"))
		}
		if err := json.NewDecoder(r.Body).Decode(&capturedPayload); err != nil {
			t.Errorf("decode body: %v", err)
		}

		w.Header().Set("Content-Type", "text/event-stream")
		flusher, _ := w.(http.Flusher)

		events := []string{
			`{"type":"response.output_item.added","output_index":0,"item":{"id":"rs_01","type":"reasoning"}}`,
			`{"type":"response.reasoning_summary_text.delta","output_index":0,"delta":"Let me check "}`,
			`{"type":"response.reasoning_summary_text.delta","output_index":0,"delta":"the file."}`,
			`{"type":"response.output_item.done","output_index":0,"item":{"id":"rs_01","type":"reasoning","encrypted_content":"enc_xyz_999"}}`,
			`{"type":"response.output_item.added","output_index":1,"item":{"id":"msg_01","type":"message"}}`,
			`{"type":"response.output_text.delta","output_index":1,"delta":"Reading "}`,
			`{"type":"response.output_text.delta","output_index":1,"delta":"main.go..."}`,
			`{"type":"response.output_item.done","output_index":1,"item":{"id":"msg_01","type":"message","content":[{"type":"output_text","text":"Reading main.go..."}]}}`,
			`{"type":"response.output_item.added","output_index":2,"item":{"id":"fc_01","call_id":"call_read_1","type":"function_call","name":"read"}}`,
			`{"type":"response.function_call_arguments.delta","output_index":2,"item_id":"fc_01","delta":"{\"file_path\":"}`,
			`{"type":"response.function_call_arguments.delta","output_index":2,"item_id":"fc_01","delta":"\"main.go\"}"}`,
			`{"type":"response.output_item.done","output_index":2,"item":{"id":"fc_01","call_id":"call_read_1","type":"function_call","name":"read","arguments":"{\"file_path\":\"main.go\"}"}}`,
			`{"type":"response.completed","response":{"usage":{"input_tokens":10000,"output_tokens":350,"input_tokens_details":{"cached_tokens":8500},"output_tokens_details":{"reasoning_tokens":200}}}}`,
		}
		for _, e := range events {
			fmt.Fprintf(w, "data: %s\n\n", e)
			if flusher != nil {
				flusher.Flush()
			}
		}
	}))
	defer srv.Close()

	prov, err := NewProvider(DriverOpenAIResponses, srv.URL+"/v1", "sk-test-codex", srv.Client())
	if err != nil {
		t.Fatalf("NewProvider failed: %v", err)
	}

	// Include prior turn with encrypted reasoning + tool call + tool result to test round-trip serialization
	priorAssistant := Message{
		Role: RoleAssistant,
		Content: []ContentBlock{
			{Type: BlockThinking, Thinking: "prior thought", ReasoningItemID: "rs_00", EncryptedContent: "enc_00"},
			{Type: BlockToolCall, ToolCallID: "call_prev", ProviderItemID: "fc_00", ToolName: "glob", Arguments: json.RawMessage(`{"pattern":"*.go"}`)},
		},
	}

	req := &StreamRequest{
		Model: "gpt-5-codex",
		SystemSections: []SystemSection{
			{Category: "system_prompt", Content: "You are Nex."},
			{Category: "meta_context", Content: "OS: darwin"},
		},
		ThinkingEffort: ThinkingHigh,
		PromptCacheKey: "sess_123",
		Tools: []ToolDefinition{
			{Name: "read", Description: "Read a file", InputSchema: json.RawMessage(`{"type":"object"}`)},
		},
		Messages: []Message{
			{Role: RoleUser, Content: []ContentBlock{TextBlock("inspect repo"), ImageBase64Block("image/png", "iVBORw0KGgo=")}},
			priorAssistant,
			NewToolResultMessage("call_prev", "glob", "main.go", false),
		},
	}

	ch, err := prov.Stream(context.Background(), req)
	if err != nil {
		t.Fatalf("Stream failed: %v", err)
	}

	var gotText, gotThinking string
	var doneEv *StreamEvent
	for ev := range ch {
		switch ev.Type {
		case StreamEventTextDelta:
			gotText += ev.TextDelta
		case StreamEventThinkingDelta:
			gotThinking += ev.ThinkingDelta
		case StreamEventDone:
			evCopy := ev
			doneEv = &evCopy
		case StreamEventError:
			t.Fatalf("stream error: %v", ev.Err)
		}
	}

	if gotThinking != "Let me check the file." {
		t.Fatalf("unexpected thinking delta: %q", gotThinking)
	}
	if gotText != "Reading main.go..." {
		t.Fatalf("unexpected text delta: %q", gotText)
	}
	if doneEv == nil || doneEv.Message == nil {
		t.Fatalf("missing done event")
	}
	if doneEv.StopReason != StopReasonToolUse {
		t.Fatalf("expected StopReasonToolUse, got %s", doneEv.StopReason)
	}
	// Check normalized token usage: 10000 total input with 8500 cached -> 1500 uncached + 8500 cache_read
	if doneEv.Usage.InputTokens != 1500 || doneEv.Usage.CacheReadTokens != 8500 || doneEv.Usage.TotalInputTokens() != 10000 {
		t.Fatalf("unexpected normalized usage: %+v", doneEv.Usage)
	}
	if math.Abs(doneEv.Usage.CacheHitRatePct()-85.0) > 0.01 {
		t.Fatalf("unexpected cache hit rate: %v", doneEv.Usage.CacheHitRatePct())
	}
	if len(doneEv.Message.Content) != 3 {
		t.Fatalf("expected 3 content blocks, got %d: %+v", len(doneEv.Message.Content), doneEv.Message.Content)
	}
	if doneEv.Message.Content[0].EncryptedContent != "enc_xyz_999" {
		t.Fatalf("encrypted_content not preserved: %+v", doneEv.Message.Content[0])
	}
	if doneEv.Message.Content[2].ToolCallID != "call_read_1" || string(doneEv.Message.Content[2].Arguments) != `{"file_path":"main.go"}` {
		t.Fatalf("tool call block mismatch: %+v", doneEv.Message.Content[2])
	}

	// Verify request payload had instructions and reasoning.encrypted_content include
	if capturedPayload["instructions"] != "You are Nex.\n\nOS: darwin" {
		t.Fatalf("unexpected instructions: %v", capturedPayload["instructions"])
	}
}

func TestAnthropicProviderStreamCoalescingAndThinking(t *testing.T) {
	var capturedPayload map[string]any

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/messages" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		if r.Header.Get("x-api-key") != "sk-ant-test" {
			t.Errorf("missing x-api-key: %s", r.Header.Get("x-api-key"))
		}
		_ = json.NewDecoder(r.Body).Decode(&capturedPayload)

		w.Header().Set("Content-Type", "text/event-stream")
		sse := strings.Join([]string{
			`event: message_start`,
			`data: {"type":"message_start","message":{"usage":{"input_tokens":500,"cache_read_input_tokens":4000,"cache_creation_input_tokens":500,"output_tokens":1}}}`,
			``,
			`event: content_block_start`,
			`data: {"type":"content_block_start","index":0,"content_block":{"type":"thinking","thinking":""}}`,
			``,
			`event: content_block_delta`,
			`data: {"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"Analyzing both results."}}`,
			``,
			`event: content_block_delta`,
			`data: {"type":"content_block_delta","index":0,"delta":{"type":"signature_delta","signature":"sig_claude_abc"}}`,
			``,
			`event: content_block_stop`,
			`data: {"type":"content_block_stop","index":0}`,
			``,
			`event: content_block_start`,
			`data: {"type":"content_block_start","index":1,"content_block":{"type":"text","text":""}}`,
			``,
			`event: content_block_delta`,
			`data: {"type":"content_block_delta","index":1,"delta":{"type":"text_delta","text":"All checks passed."}}`,
			``,
			`event: content_block_stop`,
			`data: {"type":"content_block_stop","index":1}`,
			``,
			`event: content_block_start`,
			`data: {"type":"content_block_start","index":2,"content_block":{"type":"tool_use","id":"toolu_99","name":"bash"}}`,
			``,
			`event: content_block_delta`,
			`data: {"type":"content_block_delta","index":2,"delta":{"type":"input_json_delta","partial_json":"{\"command\":\"go test\"}"}}`,
			``,
			`event: content_block_stop`,
			`data: {"type":"content_block_stop","index":2}`,
			``,
			`event: message_delta`,
			`data: {"type":"message_delta","delta":{"stop_reason":"tool_use"},"usage":{"output_tokens":120}}`,
			``,
			`event: message_stop`,
			`data: {"type":"message_stop"}`,
			``,
		}, "\n")
		_, _ = w.Write([]byte(sse))
	}))
	defer srv.Close()

	prov, err := NewProvider(DriverAnthropic, srv.URL+"/v1", "sk-ant-test", srv.Client())
	if err != nil {
		t.Fatalf("NewProvider: %v", err)
	}

	// Send two consecutive RoleToolResult messages to verify Anthropic single-user-message coalescing!
	req := &StreamRequest{
		Model:          "claude-sonnet-4-5",
		ThinkingEffort: ThinkingMedium,
		SystemSections: []SystemSection{
			{Category: "system_prompt", Content: "Core prompt", CacheControl: true},
		},
		Tools: []ToolDefinition{
			{Name: "bash", Description: "Run bash", InputSchema: json.RawMessage(`{"type":"object"}`)},
		},
		Messages: []Message{
			NewUserTextMessage("run two tools"),
			{
				Role: RoleAssistant,
				Content: []ContentBlock{
					ThinkingBlock("step 1", "sig_prev"),
					ToolCallBlock("t1", "read", json.RawMessage(`{"file_path":"a.go"}`)),
					ToolCallBlock("t2", "read", json.RawMessage(`{"file_path":"b.go"}`)),
				},
			},
			NewToolResultMessage("t1", "read", "package a", false),
			NewToolResultMessage("t2", "read", "file missing", true),
		},
	}

	ch, err := prov.Stream(context.Background(), req)
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}

	var final *StreamEvent
	for ev := range ch {
		if ev.Type == StreamEventError {
			t.Fatalf("unexpected error: %v", ev.Err)
		}
		if ev.Type == StreamEventDone {
			evCopy := ev
			final = &evCopy
		}
	}

	if final == nil || final.Message == nil {
		t.Fatalf("missing final message")
	}
	if final.StopReason != StopReasonToolUse {
		t.Fatalf("expected tool_use, got %s", final.StopReason)
	}
	if final.Usage.TotalInputTokens() != 5000 || final.Usage.OutputTokens != 120 {
		t.Fatalf("unexpected usage: %+v", final.Usage)
	}
	if final.Message.Content[0].Signature != "sig_claude_abc" {
		t.Fatalf("missing signature: %+v", final.Message.Content[0])
	}
	if final.Message.Content[1].Text != "All checks passed." {
		t.Fatalf("unexpected text: %+v", final.Message.Content[1])
	}
	if final.Message.Content[2].ToolName != "bash" {
		t.Fatalf("unexpected tool block: %+v", final.Message.Content[2])
	}

	// Verify consecutive RoleToolResult messages were coalesced into a single user message with 2 tool_result blocks
	wireMsgs, _ := capturedPayload["messages"].([]any)
	if len(wireMsgs) != 3 {
		t.Fatalf("expected 3 coalesced wire messages (user, assistant, user), got %d", len(wireMsgs))
	}
	lastMsg, _ := wireMsgs[2].(map[string]any)
	lastBlocks, _ := lastMsg["content"].([]any)
	if len(lastBlocks) != 2 {
		t.Fatalf("expected 2 coalesced tool_result blocks, got %d", len(lastBlocks))
	}
}

func TestProviderErrorAndCancellation(t *testing.T) {
	errSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":{"message":"invalid api key"}}`))
	}))
	defer errSrv.Close()

	p := NewOpenAIResponsesProvider(OpenAIResponsesConfig{BaseURL: errSrv.URL, APIKey: "bad"})
	if _, err := p.Stream(context.Background(), &StreamRequest{Model: "gpt-5"}); err == nil || !strings.Contains(err.Error(), "401") {
		t.Fatalf("expected HTTP 401 error, got %v", err)
	}

	// Context cancellation mid-stream
	hangSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher, _ := w.(http.Flusher)
		fmt.Fprintf(w, "data: {\"type\":\"response.output_text.delta\",\"delta\":\"hello\"}\n\n")
		if flusher != nil {
			flusher.Flush()
		}
		<-r.Context().Done()
	}))
	defer hangSrv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Millisecond)
	defer cancel()
	pHang := NewOpenAIResponsesProvider(OpenAIResponsesConfig{BaseURL: hangSrv.URL})
	ch, err := pHang.Stream(ctx, &StreamRequest{Model: "gpt-5", Messages: []Message{NewUserTextMessage("hi")}})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	for range ch {
	}
}

func TestModelsCatalogAndPricing(t *testing.T) {
	seed, err := EmbeddedModelsCatalog()
	if err != nil || len(seed) < 5 {
		t.Fatalf("EmbeddedModelsCatalog failed: len=%d err=%v", len(seed), err)
	}

	sampleModelsDev := []byte(`{
		"anthropic": {
			"id": "anthropic",
			"name": "Anthropic",
			"models": {
				"claude-sonnet-4-5": {
					"id": "claude-sonnet-4-5",
					"name": "Claude Sonnet 4.5",
					"attachment": true,
					"reasoning": true,
					"tool_call": true,
					"modalities": { "input": ["text", "image", "pdf"] },
					"cost": { "input": 3.0, "output": 15.0, "cache_read": 0.3, "cache_write": 3.75 },
					"limit": { "context": 200000, "output": 64000 }
				}
			}
		}
	}`)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(sampleModelsDev)
	}))
	defer srv.Close()

	parsed, err := FetchModelsDevCatalog(context.Background(), srv.Client(), srv.URL)
	if err != nil || len(parsed) != 1 {
		t.Fatalf("FetchModelsDevCatalog failed: %v, len=%d", err, len(parsed))
	}
	m := parsed[0]
	if !m.SupportsVision || !m.SupportsPDF || !m.SupportsReasoning || !m.SupportsCaching || m.ContextWindow != 200000 {
		t.Fatalf("parsed capabilities mismatch: %+v", m)
	}

	// Verify cost & cache savings math: 100k cache read ($0.03 instead of $0.30 -> saves $0.27) + 10k output ($0.15)
	est := CalculateCost(Usage{
		InputTokens:     0,
		CacheReadTokens: 100_000,
		OutputTokens:    10_000,
	}, m.Pricing)
	if math.Abs(est.EstimatedCostUSD-0.18) > 1e-6 || math.Abs(est.CacheSavedUSD-0.27) > 1e-6 {
		t.Fatalf("cost estimate mismatch: %+v", est)
	}
}

func TestOpenAIResponsesToolResultImageSerialization(t *testing.T) {
	var captured map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&captured)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"usage\":{\"input_tokens\":1,\"output_tokens\":1}}}\n\n"))
	}))
	defer srv.Close()

	p := NewOpenAIResponsesProvider(OpenAIResponsesConfig{BaseURL: srv.URL, APIKey: "k", HTTPClient: srv.Client()})
	req := &StreamRequest{
		Model: "gpt-5",
		Messages: []Message{
			NewUserTextMessage("take screenshot"),
			NewToolResultMessage("call_1", "browser", "screenshot taken", false, ImageBase64Block("image/jpeg", "AAA=")),
			NewToolResultMessage("call_2", "read", "plain file text", false),
		},
	}
	ch, err := p.Stream(context.Background(), req)
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	for range ch {
	}
	items, _ := captured["input"].([]any)
	if len(items) != 3 {
		t.Fatalf("expected 3 input items, got %d", len(items))
	}
	firstOut, _ := items[1].(map[string]any)
	parts, ok := firstOut["output"].([]any)
	if !ok || len(parts) != 2 {
		t.Fatalf("expected structured [input_text, input_image] output, got %#v", firstOut["output"])
	}
	p0, _ := parts[0].(map[string]any)
	p1, _ := parts[1].(map[string]any)
	if p0["type"] != "input_text" || p1["type"] != "input_image" || p1["image_url"] != "data:image/jpeg;base64,AAA=" {
		t.Fatalf("unexpected image tool output parts: %+v", parts)
	}
	secondOut, _ := items[2].(map[string]any)
	if secondOut["output"] != "plain file text" {
		t.Fatalf("expected plain string output when no images, got %#v", secondOut["output"])
	}
}
