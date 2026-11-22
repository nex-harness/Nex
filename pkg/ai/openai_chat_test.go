package ai

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestOpenAIChatProviderStreamTextThinkingToolsAndUsage(t *testing.T) {
	var capturedBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			t.Errorf("unexpected path %q", r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer sk-chat-test" {
			t.Errorf("unexpected auth header %q", r.Header.Get("Authorization"))
		}
		if err := json.NewDecoder(r.Body).Decode(&capturedBody); err != nil {
			t.Errorf("decode request body: %v", err)
		}

		w.Header().Set("Content-Type", "text/event-stream")
		chunks := []string{
			`{"choices":[{"index":0,"delta":{"reasoning_content":"Let me inspect "}}]}`,
			`{"choices":[{"index":0,"delta":{"reasoning_content":"main.go."}}]}`,
			`{"choices":[{"index":0,"delta":{"content":"Reading "}}]}`,
			`{"choices":[{"index":0,"delta":{"content":"file now."}}]}`,
			`{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"read","arguments":"{\"file_"}}]}}]}`,
			`{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"path\":\"main.go\"}"}}]}}]}`,
			`{"choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":120,"completion_tokens":45,"total_tokens":165,"prompt_tokens_details":{"cached_tokens":80},"completion_tokens_details":{"reasoning_tokens":15}}}`,
		}
		for _, c := range chunks {
			fmt.Fprintf(w, "data: %s\n\n", c)
		}
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer srv.Close()

	prov, err := NewProvider(DriverOpenAIChat, srv.URL+"/v1", "sk-chat-test", srv.Client())
	if err != nil {
		t.Fatalf("NewProvider failed: %v", err)
	}
	if prov.Driver() != DriverOpenAIChat {
		t.Fatalf("expected DriverOpenAIChat, got %q", prov.Driver())
	}

	ch, err := prov.Stream(context.Background(), &StreamRequest{
		Model:           "deepseek-reasoner",
		SystemPrompt:    "Be helpful.",
		ThinkingEffort:  ThinkingHigh,
		MaxOutputTokens: 4096,
		Messages: []Message{
			NewUserTextMessage("Read main.go"),
		},
		Tools: []ToolDefinition{
			{
				Name:        "read",
				Description: "Read a file",
				InputSchema: json.RawMessage(`{"type":"object","properties":{"file_path":{"type":"string"}},"required":["file_path"]}`),
			},
		},
	})
	if err != nil {
		t.Fatalf("Stream failed: %v", err)
	}

	var gotText, gotThinking string
	var doneMsg *Message
	var doneUsage Usage
	for ev := range ch {
		switch ev.Type {
		case StreamEventTextDelta:
			gotText += ev.TextDelta
		case StreamEventThinkingDelta:
			gotThinking += ev.ThinkingDelta
		case StreamEventDone:
			doneMsg = ev.Message
			doneUsage = ev.Usage
		case StreamEventError:
			t.Fatalf("unexpected stream error: %v", ev.Err)
		}
	}

	if gotThinking != "Let me inspect main.go." {
		t.Fatalf("unexpected thinking: %q", gotThinking)
	}
	if gotText != "Reading file now." {
		t.Fatalf("unexpected text: %q", gotText)
	}
	if doneMsg == nil || len(doneMsg.Content) != 3 {
		t.Fatalf("expected 3 content blocks (thinking, text, tool_call), got %+v", doneMsg)
	}
	if doneMsg.Content[2].Type != BlockToolCall || doneMsg.Content[2].ToolName != "read" || !strings.Contains(string(doneMsg.Content[2].Arguments), "main.go") {
		t.Fatalf("unexpected tool call block: %+v", doneMsg.Content[2])
	}
	if doneUsage.InputTokens != 40 || doneUsage.TotalInputTokens() != 120 || doneUsage.CacheReadTokens != 80 || doneUsage.ReasoningTokens != 15 {
		t.Fatalf("unexpected usage: %+v", doneUsage)
	}
	if capturedBody["reasoning_effort"] != "high" {
		t.Fatalf("expected reasoning_effort=high, got %v", capturedBody["reasoning_effort"])
	}
}

func TestOpenAIChatToolResultImageSerialization(t *testing.T) {
	var captured map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&captured)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"ok\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n"))
	}))
	defer srv.Close()

	p := NewOpenAIChatProvider(OpenAIChatConfig{BaseURL: srv.URL + "/v1", APIKey: "k", HTTPClient: srv.Client()})
	req := &StreamRequest{
		Model: "gpt-4o",
		Messages: []Message{
			NewUserTextMessage("inspect page"),
			{
				Role: RoleAssistant,
				Content: []ContentBlock{
					ToolCallBlock("c1", "browser", json.RawMessage(`{"action":"screenshot"}`)),
					ToolCallBlock("c2", "read", json.RawMessage(`{"file_path":"a.go"}`)),
				},
			},
			NewToolResultMessage("c1", "browser", "captured viewport", false, ImageBase64Block("image/png", "BBB=")),
			NewToolResultMessage("c2", "read", "package main", false),
		},
	}
	ch, err := p.Stream(context.Background(), req)
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	for range ch {
	}
	msgs, _ := captured["messages"].([]any)
	if len(msgs) != 5 {
		t.Fatalf("expected 5 messages with trailing synthetic image attachment, got %d: %#v", len(msgs), msgs)
	}
	m2, _ := msgs[2].(map[string]any)
	m3, _ := msgs[3].(map[string]any)
	if m2["role"] != "tool" || m3["role"] != "tool" {
		t.Fatalf("consecutive tool messages must stay adjacent to assistant tool_calls")
	}
	attachMsg, _ := msgs[4].(map[string]any)
	if attachMsg["role"] != "user" {
		t.Fatalf("expected synthetic user attachment role, got %v", attachMsg["role"])
	}
	parts, _ := attachMsg["content"].([]any)
	if len(parts) != 2 {
		t.Fatalf("unexpected attachment parts: %#v", attachMsg["content"])
	}
	p1, _ := parts[1].(map[string]any)
	if p1["type"] != "image_url" {
		t.Fatalf("expected image_url attachment part, got %#v", p1)
	}
}
