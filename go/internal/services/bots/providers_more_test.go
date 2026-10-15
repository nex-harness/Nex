package bots

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// fakeFeishuServer serves the open-apis REST shape.
type fakeFeishuServer struct {
	server  *httptest.Server
	mu      chan struct{}
	records []recordedRequest
	// baseURL is what the provider must call; the test points the provider at it
	// by rewriting its base, so the global FeishuFeishuBaseURL is never touched.
	baseURL string
}

func newFakeFeishu(t *testing.T, handler func(recordedRequest) (int, string)) *fakeFeishuServer {
	t.Helper()
	fake := &fakeFeishuServer{mu: make(chan struct{}, 1)}
	fake.mu <- struct{}{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		data, _ := io.ReadAll(r.Body)
		body := map[string]any{}
		_ = json.Unmarshal(data, &body)
		record := recordedRequest{
			Method: r.Method, Path: r.URL.Path + queryOf(r), Header: r.Header.Clone(),
			Body: body, Raw: string(data),
		}
		<-fake.mu
		fake.records = append(fake.records, record)
		fake.mu <- struct{}{}
		status, payload := http.StatusOK, `{"code":0,"msg":"ok"}`
		if handler != nil {
			status, payload = handler(record)
		}
		w.Header().Set("content-type", "application/json")
		w.WriteHeader(status)
		_, _ = io.WriteString(w, payload)
	}))
	fake.server = server
	fake.baseURL = server.URL
	t.Cleanup(server.Close)
	return fake
}

func queryOf(r *http.Request) string {
	if r.URL.RawQuery == "" {
		return ""
	}
	return "?" + r.URL.RawQuery
}

func (f *fakeFeishuServer) last(t *testing.T) recordedRequest {
	t.Helper()
	<-f.mu
	defer func() { f.mu <- struct{}{} }()
	if len(f.records) == 0 {
		t.Fatal("no request reached the fake Feishu server")
	}
	return f.records[len(f.records)-1]
}

// newTestFeishuProvider builds a provider whose base URL is the fake. The
// provider resolves its base from the bot provider name, so the fake is injected
// through a package-level override that is restored on cleanup.
func newTestFeishuProvider(t *testing.T, handler func(recordedRequest) (int, string)) (*FeishuProvider, *fakeFeishuServer, *stubCredentials) {
	t.Helper()
	fake := newFakeFeishu(t, handler)
	restore := feishuBaseURLOverride(fake.baseURL)
	t.Cleanup(restore)
	credentials := newStubCredentials()
	credentials.values[BuildCredentialKey("f1")] = "APPSECRET"
	provider := &FeishuProvider{
		Client:     &ProviderClient{Doer: fake.server.Client()},
		Credential: credentials,
	}
	return provider, fake, credentials
}

func feishuTestBot() Config {
	return Config{
		ID: "f1", Provider: ProviderFeishu, Enabled: true,
		FeishuAppID:     "cli_abc",
		CredentialRef:   BuildCredentialKey("f1"),
		AllowedCommands: DefaultAllowedCommands(),
	}
}

// TestFeishuTenantAccessTokenAndSend drives the two-step send: the token call,
// then im/v1/messages with the receive_id_type derived from the target.
func TestFeishuTenantAccessTokenAndSend(t *testing.T) {
	provider, fake, _ := newTestFeishuProvider(t, func(record recordedRequest) (int, string) {
		if strings.Contains(record.Path, "tenant_access_token") {
			return http.StatusOK, `{"code":0,"tenant_access_token":"t-abc","expire":7200}`
		}
		return http.StatusOK, `{"code":0,"data":{"message_id":"om-1"}}`
	})
	err := provider.Send(context.Background(), feishuTestBot(), OutboundMessage{
		ProviderUserID: "ou_user", Text: "hello",
	})
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	<-fake.mu
	defer func() { fake.mu <- struct{}{} }()
	if len(fake.records) != 2 {
		t.Fatalf("got %d requests, want token + message", len(fake.records))
	}
	tokenRecord := fake.records[0]
	if !strings.Contains(tokenRecord.Path, "/open-apis/auth/v3/tenant_access_token/internal") {
		t.Errorf("first path = %q", tokenRecord.Path)
	}
	if tokenRecord.Body["app_id"] != "cli_abc" || tokenRecord.Body["app_secret"] != "APPSECRET" {
		t.Errorf("token body = %v", tokenRecord.Body)
	}
	messageRecord := fake.records[1]
	if !strings.Contains(messageRecord.Path, "/open-apis/im/v1/messages?receive_id_type=open_id") {
		t.Errorf("message path = %q, want receive_id_type=open_id for an ou_ target", messageRecord.Path)
	}
	if got := messageRecord.Header.Get("Authorization"); got != "Bearer t-abc" {
		t.Errorf("Authorization = %q, want the tenant token", got)
	}
	if messageRecord.Body["msg_type"] != "text" {
		t.Errorf("msg_type = %v", messageRecord.Body["msg_type"])
	}
	content, _ := messageRecord.Body["content"].(string)
	var decoded map[string]any
	if err := json.Unmarshal([]byte(content), &decoded); err != nil {
		t.Fatalf("content is not JSON: %v", err)
	}
	if decoded["text"] != "hello" {
		t.Errorf("content.text = %v", decoded["text"])
	}
}

// TestFeishuReceiveIDType pins the open_id / chat_id discrimination, because
// sending to the wrong type is rejected by the platform with no useful error.
func TestFeishuReceiveIDType(t *testing.T) {
	cases := map[string]string{
		"ou_abc": "open_id",
		"on_abc": "open_id",
		"oc_abc": "chat_id",
		"12345":  "chat_id",
		"":       "chat_id",
	}
	for target, want := range cases {
		if got := resolveReceiveIDType(target); got != want {
			t.Errorf("resolveReceiveIDType(%q) = %q, want %q", target, got, want)
		}
	}
}

// TestFeishuTokenFailure proves a non-zero code is an error and never a silent
// empty token that would send an unauthenticated request.
func TestFeishuTokenFailure(t *testing.T) {
	t.Run("a non-zero code surfaces the message", func(t *testing.T) {
		provider, _, _ := newTestFeishuProvider(t, func(recordedRequest) (int, string) {
			return http.StatusOK, `{"code":10003,"msg":"app not found"}`
		})
		_, err := provider.tenantAccessToken(context.Background(), feishuTestBot())
		if err == nil || !strings.Contains(err.Error(), "app not found") {
			t.Fatalf("err = %v", err)
		}
	})

	t.Run("an HTTP failure names the status", func(t *testing.T) {
		provider, _, _ := newTestFeishuProvider(t, func(recordedRequest) (int, string) {
			return http.StatusInternalServerError, `{}`
		})
		_, err := provider.tenantAccessToken(context.Background(), feishuTestBot())
		if err == nil || !strings.Contains(err.Error(), "HTTP 500") {
			t.Fatalf("err = %v", err)
		}
	})

	t.Run("a missing secret yields an empty token and no error", func(t *testing.T) {
		provider, _, credentials := newTestFeishuProvider(t, nil)
		delete(credentials.values, BuildCredentialKey("f1"))
		token, err := provider.tenantAccessToken(context.Background(), feishuTestBot())
		if err != nil || token != "" {
			t.Fatalf("token = %q, err = %v; want empty and nil", token, err)
		}
	})
}

// TestFeishuParseCallbackTextMessage covers the flattened-`event` fallback (the
// TS Bugfix where a genuinely received message parsed to zero inbound) and the
// post-message fallback.
func TestFeishuParseCallbackTextMessage(t *testing.T) {
	provider, _, _ := newTestFeishuProvider(t, nil)
	cases := []struct {
		name     string
		payload  map[string]any
		wantText string
		wantChat ChatType
	}{
		{
			name: "nested event",
			payload: map[string]any{"botId": "f1", "event": map[string]any{
				"message": map[string]any{
					"message_type": "text",
					"message_id":   "om-1",
					"chat_id":      "oc-1",
					"chat_type":    "group",
					"content":      `{"text":"hello team"}`,
				},
				"sender": map[string]any{"sender_id": map[string]any{"open_id": "ou_1"}},
			}},
			wantText: "hello team", wantChat: ChatGroup,
		},
		{
			name: "flattened event still parses",
			payload: map[string]any{"botId": "f1",
				"message": map[string]any{
					"message_type": "text",
					"message_id":   "om-2",
					"content":      `{"text":"flat"}`,
				},
				"sender":  map[string]any{"sender_id": map[string]any{"open_id": "ou_2"}},
				"open_id": "ou_2",
			},
			wantText: "flat", wantChat: ChatPrivate,
		},
		{
			name: "mentions are stripped",
			payload: map[string]any{"botId": "f1", "event": map[string]any{
				"message": map[string]any{
					"message_type": "text",
					"message_id":   "om-3",
					"content":      `{"text":"<at id=\"all\">Bot</at> hello"}`,
				},
				"sender": map[string]any{"sender_id": map[string]any{"open_id": "ou_3"}},
			}},
			wantText: "hello", wantChat: ChatPrivate,
		},
		{
			name: "a post message keeps its text",
			payload: map[string]any{"botId": "f1", "event": map[string]any{
				"message": map[string]any{
					"message_type": "post",
					"message_id":   "om-4",
					"content":      `{"post":{"zh_cn":{"content":[{"tag":"text","text":"rich body"}]}}}`,
				},
				"sender": map[string]any{"sender_id": map[string]any{"open_id": "ou_4"}},
			}},
			wantText: "rich body", wantChat: ChatPrivate,
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			messages := provider.ParseCallback(testCase.payload)
			if len(messages) != 1 {
				t.Fatalf("got %d messages, want 1", len(messages))
			}
			if messages[0].Text != testCase.wantText {
				t.Errorf("text = %q, want %q", messages[0].Text, testCase.wantText)
			}
			if messages[0].Actor.ChatType != testCase.wantChat {
				t.Errorf("chatType = %q, want %q", messages[0].Actor.ChatType, testCase.wantChat)
			}
		})
	}
}

// TestFeishuParseCallbackCardAction covers the card-action shape and the two
// documented traps: a private-chat button that carries open_chat_id must not be
// rejected as group-only, and the card update token must be found wherever
// Card JSON 2.0 puts it.
func TestFeishuParseCallbackCardAction(t *testing.T) {
	provider, _, _ := newTestFeishuProvider(t, nil)
	payload := map[string]any{"botId": "f1", "header": map[string]any{"event_id": "ev-1"}, "event": map[string]any{
		"action": map[string]any{
			"value":             map[string]any{"command": "/approve req-1 opt-1"},
			"behaviors":         []any{map[string]any{"type": "callback"}},
			"card_update_token": "cu-1",
		},
		"operator": map[string]any{"operator_id": map[string]any{"open_id": "ou_9"}},
		"context":  map[string]any{"open_chat_id": "oc-9", "chat_type": "private"},
	}}
	messages := provider.ParseCallback(payload)
	if len(messages) != 1 {
		t.Fatalf("got %d messages, want 1", len(messages))
	}
	if messages[0].Text != "/approve req-1 opt-1" {
		t.Errorf("text = %q", messages[0].Text)
	}
	// Bugfix: open_chat_id is present but chat_type is private, so this is NOT a
	// group message and the bot must not answer "private chats only".
	if messages[0].Actor.ChatType != ChatPrivate {
		t.Errorf("chatType = %q, want private", messages[0].Actor.ChatType)
	}
	if messages[0].Actor.ChatID != "" {
		t.Errorf("chatId = %q, want empty for a private chat", messages[0].Actor.ChatID)
	}
	if messages[0].Actor.ProviderMessageID != "ev-1" {
		t.Errorf("providerMessageId = %q, want the header event_id", messages[0].Actor.ProviderMessageID)
	}
	if got := ReadFeishuCardUpdateToken(payload); got != "cu-1" {
		t.Errorf("card update token = %q, want cu-1", got)
	}
}

// TestFeishuCardUpdateTokenLocations pins the five places Card JSON 2.0 puts the
// token; missing one makes card/update a silent no-op.
func TestFeishuCardUpdateTokenLocations(t *testing.T) {
	cases := []struct {
		name    string
		payload map[string]any
		want    string
	}{
		{"event token", map[string]any{"event": map[string]any{"token": "t1"}}, "t1"},
		{"event card_update_token", map[string]any{"event": map[string]any{"card_update_token": "t2"}}, "t2"},
		{"event update_token", map[string]any{"event": map[string]any{"update_token": "t3"}}, "t3"},
		{"payload token", map[string]any{"token": "t4"}, "t4"},
		{"context token", map[string]any{"event": map[string]any{"context": map[string]any{"card_update_token": "t5"}}}, "t5"},
		{"legacy action token", map[string]any{"event": map[string]any{"action": map[string]any{"token": "t6"}}}, "t6"},
		{"action card_update_token", map[string]any{"event": map[string]any{"action": map[string]any{"card_update_token": "t7"}}}, "t7"},
		{"action update_token", map[string]any{"event": map[string]any{"action": map[string]any{"update_token": "t8"}}}, "t8"},
		{"context update_token", map[string]any{"event": map[string]any{"context": map[string]any{"update_token": "t9"}}}, "t9"},
		{"payload card_update_token", map[string]any{"card_update_token": "t10"}, "t10"},
		{"payload update_token", map[string]any{"update_token": "t11"}, "t11"},
		{"absent", map[string]any{}, ""},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			if got := ReadFeishuCardUpdateToken(testCase.payload); got != testCase.want {
				t.Errorf("ReadFeishuCardUpdateToken() = %q, want %q", got, testCase.want)
			}
		})
	}
}

// TestFeishuCardIsJSON20 pins the card schema version: streaming_card is only
// offered on Feishu precisely because the card dialect is JSON 2.0.
func TestFeishuCardIsJSON20(t *testing.T) {
	card := BuildFeishuCardPayload(OutboundMessage{
		Text:      "choose",
		Selection: &SelectionPrompt{Action: "reply.set", Options: []SelectionOption{{ID: "a", Label: "A"}}},
	})
	if card["schema"] != "2.0" {
		t.Errorf("schema = %v, want 2.0", card["schema"])
	}
	body, _ := card["body"].(map[string]any)
	elements, _ := body["elements"].([]any)
	if len(elements) != 2 {
		t.Fatalf("got %d elements, want a markdown block + one button", len(elements))
	}
	if CountFeishuCardTaggedElements(card) > FeishuStreamingCardTaggedElementBudget {
		t.Error("a two-element card must be far below the tagged-element budget")
	}
}

// TestFeishuStreamingCardPayload pins the streaming card's elements and status.
func TestFeishuStreamingCardPayload(t *testing.T) {
	payload := BuildFeishuStreamingCardPayload(StreamingCardState{
		ProviderUserID: "ou_1",
		Status:         "running",
		Blocks: []StreamingCardBlock{
			{Text: "working on it"},
			{IsTools: true, Summaries: []string{"- Read a.ts"}},
		},
	})
	if payload["schema"] != "2.0" {
		t.Errorf("schema = %v", payload["schema"])
	}
	config, _ := payload["config"].(map[string]any)
	if config["streaming_mode"] != true {
		t.Errorf("streaming_mode = %v, want true", config["streaming_mode"])
	}
	body, _ := payload["body"].(map[string]any)
	elements, _ := body["elements"].([]any)
	if len(elements) != 3 {
		t.Fatalf("got %d elements, want status + text + tools", len(elements))
	}
	first, _ := elements[0].(map[string]any)
	if !strings.Contains(first["content"].(string), "运行中") {
		t.Errorf("status element = %v", first)
	}
}

// TestFeishuLarkUsesItsOwnDomain pins the feishu/lark host split.
func TestFeishuLarkUsesItsOwnDomain(t *testing.T) {
	provider := &FeishuProvider{}
	if got := provider.baseURL(Config{Provider: ProviderLark}); got != LarkBaseURL {
		t.Errorf("lark base = %q, want %q", got, LarkBaseURL)
	}
	if got := provider.baseURL(Config{Provider: ProviderFeishu}); got != FeishuFeishuBaseURL {
		t.Errorf("feishu base = %q, want %q", got, FeishuFeishuBaseURL)
	}
}

// TestFeishuParseCallbackKeepsProvider proves nexProvider decides feishu vs lark
// for one adapter.
func TestFeishuParseCallbackKeepsProvider(t *testing.T) {
	provider, _, _ := newTestFeishuProvider(t, nil)
	payload := map[string]any{"botId": "f1", "nexProvider": "lark", "event": map[string]any{
		"message": map[string]any{"message_type": "text", "message_id": "om", "content": `{"text":"x"}`},
		"sender":  map[string]any{"sender_id": map[string]any{"open_id": "ou"}},
	}}
	messages := provider.ParseCallback(payload)
	if len(messages) != 1 {
		t.Fatalf("got %d messages", len(messages))
	}
	if messages[0].Actor.Provider != ProviderLark {
		t.Errorf("provider = %q, want lark", messages[0].Actor.Provider)
	}
}

// TestWebhookSendCarriesSecretAndSelectionText drives the outbound webhook
// against a loopback server: the auth header, the selection text and the
// elicitation type marker.
func TestWebhookSendCarriesSecretAndSelectionText(t *testing.T) {
	var got recordedRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		data, _ := io.ReadAll(r.Body)
		body := map[string]any{}
		_ = json.Unmarshal(data, &body)
		got = recordedRequest{Method: r.Method, Path: r.URL.Path, Header: r.Header.Clone(), Body: body, Raw: string(data)}
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(server.Close)

	credentials := newStubCredentials()
	credentials.values[BuildWebhookSecretKey("b1")] = "shh"
	provider := &WebhookProvider{
		Client:     &ProviderClient{Doer: server.Client()},
		Credential: credentials,
		Now:        func() time.Time { return time.UnixMilli(1700000000000) },
		Sleep:      func(context.Context, time.Duration) error { return nil },
	}
	bot := Config{
		ID: "b1", Provider: ProviderWebhook, Enabled: true,
		WebhookURL: server.URL, WebhookSecretRef: BuildWebhookSecretKey("b1"),
		AllowedCommands: DefaultAllowedCommands(),
	}
	err := provider.Send(context.Background(), bot, OutboundMessage{
		ProviderUserID: "u1", Text: "pick one",
		Selection: &SelectionPrompt{
			Action:  "reply.set",
			Options: []SelectionOption{{ID: "summary", Label: "Summary"}, {ID: "full", Label: "Full", Description: "everything"}},
		},
	})
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	if got.Header.Get(DefaultWebhookAuthHeader) != "shh" {
		t.Errorf("secret header = %q, want shh", got.Header.Get(DefaultWebhookAuthHeader))
	}
	if got.Body["type"] != "nex.bot.message" || got.Body["botId"] != "b1" {
		t.Errorf("body = %v", got.Body)
	}
	text, _ := got.Body["text"].(string)
	wantText := "pick one\n1. Summary\n/reply summary\n2. Full - everything\n/reply full"
	if text != wantText {
		t.Errorf("text =\n%q\nwant\n%q", text, wantText)
	}
	if _, hasSelection := got.Body["selection"]; !hasSelection {
		t.Error("the selection prompt must travel alongside the text")
	}
	if got.Body["sentAt"] != float64(1700000000000) {
		t.Errorf("sentAt = %v, want the injected clock", got.Body["sentAt"])
	}
}

// TestWebhookCustomAuthHeaderName covers bot.webhookAuthHeaderName overriding the
// default, which the route reads back through the same name.
func TestWebhookCustomAuthHeaderName(t *testing.T) {
	var header http.Header
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		header = r.Header.Clone()
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(server.Close)
	credentials := newStubCredentials()
	credentials.values[BuildWebhookSecretKey("b1")] = "shh"
	provider := &WebhookProvider{
		Client:     &ProviderClient{Doer: server.Client()},
		Credential: credentials,
		Now:        func() time.Time { return time.Now() },
		Sleep:      func(context.Context, time.Duration) error { return nil },
	}
	bot := Config{
		ID: "b1", Provider: ProviderWebhook, Enabled: true,
		WebhookURL: server.URL, WebhookSecretRef: BuildWebhookSecretKey("b1"),
		WebhookAuthHeaderName: "x-custom-secret",
	}
	if err := provider.Send(context.Background(), bot, OutboundMessage{ProviderUserID: "u"}); err != nil {
		t.Fatalf("Send: %v", err)
	}
	if header.Get("x-custom-secret") != "shh" {
		t.Errorf("custom header = %q", header.Get("x-custom-secret"))
	}
}

// TestWebhookRetryLadder pins postWebhookWithRetry: 5xx and network errors retry
// on the 0/500/1500 ladder, a 4xx does not.
func TestWebhookRetryLadder(t *testing.T) {
	t.Run("a 500 is retried and then succeeds", func(t *testing.T) {
		attempts := 0
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			attempts++
			if attempts < 3 {
				w.WriteHeader(http.StatusInternalServerError)
				return
			}
			w.WriteHeader(http.StatusOK)
		}))
		t.Cleanup(server.Close)
		credentials := newStubCredentials()
		provider := &WebhookProvider{
			Client: &ProviderClient{Doer: server.Client()},
			Now:    func() time.Time { return time.Now() },
			Sleep:  func(context.Context, time.Duration) error { return nil },
		}
		bot := Config{ID: "b1", Provider: ProviderWebhook, Enabled: true, WebhookURL: server.URL}
		_ = credentials
		if err := provider.Send(context.Background(), bot, OutboundMessage{ProviderUserID: "u"}); err != nil {
			t.Fatalf("Send: %v", err)
		}
		if attempts != 3 {
			t.Errorf("attempts = %d, want 3", attempts)
		}
	})

	t.Run("a 4xx is terminal", func(t *testing.T) {
		attempts := 0
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			attempts++
			w.WriteHeader(http.StatusBadRequest)
		}))
		t.Cleanup(server.Close)
		provider := &WebhookProvider{
			Client: &ProviderClient{Doer: server.Client()},
			Now:    func() time.Time { return time.Now() },
			Sleep:  func(context.Context, time.Duration) error { return nil },
		}
		bot := Config{ID: "b1", Provider: ProviderWebhook, Enabled: true, WebhookURL: server.URL}
		err := provider.Send(context.Background(), bot, OutboundMessage{ProviderUserID: "u"})
		if err == nil || !strings.Contains(err.Error(), "HTTP 400") {
			t.Fatalf("err = %v, want the 4xx reported", err)
		}
		if attempts != 1 {
			t.Errorf("attempts = %d, want 1 (a 4xx must not be retried)", attempts)
		}
	})
}

// TestWebhookParseCallback covers the inbound webhook shapes, including the
// message-id fallback that feeds the dedupe layer.
func TestWebhookParseCallback(t *testing.T) {
	provider := &WebhookProvider{}
	t.Run("messageId becomes the provider message id", func(t *testing.T) {
		messages := provider.ParseCallback(map[string]any{
			"botId": "b1", "userId": "u1", "text": "hi", "messageId": "m-1", "chatType": "group",
		})
		if len(messages) != 1 {
			t.Fatalf("got %d messages", len(messages))
		}
		if messages[0].Actor.ProviderMessageID != "m-1" {
			t.Errorf("providerMessageId = %q", messages[0].Actor.ProviderMessageID)
		}
		if messages[0].Actor.ChatType != ChatGroup {
			t.Errorf("chatType = %q", messages[0].Actor.ChatType)
		}
	})
	t.Run("id is the fallback", func(t *testing.T) {
		messages := provider.ParseCallback(map[string]any{
			"botId": "b1", "userId": "u1", "text": "hi", "id": "m-2",
		})
		if len(messages) != 1 || messages[0].Actor.ProviderMessageID != "m-2" {
			t.Errorf("got %+v", messages)
		}
	})
	t.Run("an elicitation response needs no text", func(t *testing.T) {
		messages := provider.ParseCallback(map[string]any{
			"botId": "b1", "userId": "u1",
			"type": "nex.bot.elicitation_response", "requestId": "r1", "action": "accept",
		})
		if len(messages) != 1 {
			t.Fatalf("got %d messages, want 1", len(messages))
		}
		if messages[0].ElicitationResponse == nil || messages[0].ElicitationResponse.Action != "accept" {
			t.Errorf("elicitationResponse = %+v", messages[0].ElicitationResponse)
		}
	})
	t.Run("an invalid action drops the response and the message", func(t *testing.T) {
		messages := provider.ParseCallback(map[string]any{
			"botId": "b1", "userId": "u1",
			"type": "nex.bot.elicitation_response", "requestId": "r1", "action": "nope",
		})
		if len(messages) != 0 {
			t.Errorf("got %d messages, want 0", len(messages))
		}
	})
	t.Run("a message without a user id is dropped", func(t *testing.T) {
		if got := provider.ParseCallback(map[string]any{"botId": "b1", "text": "hi"}); len(got) != 0 {
			t.Errorf("got %d messages, want 0", len(got))
		}
	})
}

// TestWebhookSelectionCommandSpellings pins FormatSelectionCommand per action:
// each action has its own reply syntax, and a typo sends an unusable button.
func TestWebhookSelectionCommandSpellings(t *testing.T) {
	cases := []struct {
		action string
		token  string
		id     string
		want   string
	}{
		{"permission.respond", "", "opt-1", "opt-1"},
		{"elicitation.respond", "tok", "opt-1", "/elicitation tok opt-1"},
		{"elicitation.respond", "", "opt-1", "/elicitation opt-1"},
		{"model.provider.set", "", "openai", "/model provider openai"},
		{"model.set", "", "gpt-4", "/model model gpt-4"},
		{"workspace.set", "", "api", "/workspace api"},
		{"thoughtLevel.set", "", "high", "/thoughtLevel high"},
		{"task.set", "", "42", "/task 42"},
		{"mode.set", "", "yolo", "/mode yolo"},
	}
	for _, testCase := range cases {
		selection := &SelectionPrompt{Action: testCase.action, Token: testCase.token}
		if got := FormatSelectionCommand(selection, testCase.id); got != testCase.want {
			t.Errorf("FormatSelectionCommand(%s, %s) = %q, want %q",
				testCase.action, testCase.id, got, testCase.want)
		}
	}
}
