package bots

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// stubCredentials is an in-memory CredentialStore.
type stubCredentials struct {
	values map[string]string
}

func newStubCredentials() *stubCredentials {
	return &stubCredentials{values: map[string]string{}}
}

func (s *stubCredentials) Load(_ context.Context, key string) (string, error) {
	return s.values[key], nil
}

func (s *stubCredentials) Save(_ context.Context, key, value string) error {
	s.values[key] = value
	return nil
}

func (s *stubCredentials) Delete(_ context.Context, key string) error {
	delete(s.values, key)
	return nil
}

// recordedRequest is one request a fake provider server received.
type recordedRequest struct {
	Method string
	Path   string
	Header http.Header
	Body   map[string]any
	Raw    string
}

// fakeProviderServer serves a Telegram-shaped Bot API for the tests. No test in
// this package ever reaches api.telegram.org.
type fakeProviderServer struct {
	server *httptest.Server
	// handler answers one call; the default answers 200 {"ok":true,"result":[]}.
	handler func(recordedRequest) (int, string)
	mu      chan struct{}
	records []recordedRequest
}

func newFakeTelegram(t *testing.T, handler func(recordedRequest) (int, string)) *fakeProviderServer {
	t.Helper()
	fake := &fakeProviderServer{mu: make(chan struct{}, 1)}
	fake.mu <- struct{}{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		data, _ := io.ReadAll(r.Body)
		body := map[string]any{}
		_ = json.Unmarshal(data, &body)
		record := recordedRequest{
			Method: r.Method, Path: r.URL.Path, Header: r.Header.Clone(),
			Body: body, Raw: string(data),
		}
		<-fake.mu
		fake.records = append(fake.records, record)
		fake.mu <- struct{}{}
		status, payload := http.StatusOK, `{"ok":true,"result":[]}`
		if handler != nil {
			status, payload = handler(record)
		}
		w.Header().Set("content-type", "application/json")
		w.WriteHeader(status)
		_, _ = io.WriteString(w, payload)
	}))
	fake.server = server
	t.Cleanup(server.Close)
	previous := TelegramBaseURL
	TelegramBaseURL = server.URL
	t.Cleanup(func() { TelegramBaseURL = previous })
	return fake
}

func (f *fakeProviderServer) last(t *testing.T) recordedRequest {
	t.Helper()
	<-f.mu
	defer func() { f.mu <- struct{}{} }()
	if len(f.records) == 0 {
		t.Fatal("no request reached the fake provider")
	}
	return f.records[len(f.records)-1]
}

func (f *fakeProviderServer) count() int {
	<-f.mu
	defer func() { f.mu <- struct{}{} }()
	return len(f.records)
}

func newTestTelegramProvider(t *testing.T, handler func(recordedRequest) (int, string)) (*TelegramProvider, *fakeProviderServer, *stubCredentials) {
	t.Helper()
	fake := newFakeTelegram(t, handler)
	credentials := newStubCredentials()
	credentials.values["bot:t1:credential"] = "TOKEN"
	provider := &TelegramProvider{
		Client:     &ProviderClient{Doer: fake.server.Client()},
		Credential: credentials,
	}
	return provider, fake, credentials
}

func telegramTestBot() Config {
	return Config{
		ID: "t1", Provider: ProviderTelegram, Enabled: true,
		CredentialRef:   "bot:t1:credential",
		AllowedCommands: DefaultAllowedCommands(),
	}
}

// TestTelegramGetMeShape drives the fake with the Telegram getMe answer shape
// the TS reads (result.first_name / result.username).
func TestTelegramGetMeShape(t *testing.T) {
	provider, fake, _ := newTestTelegramProvider(t, func(record recordedRequest) (int, string) {
		if strings.HasSuffix(record.Path, "/getMe") {
			return http.StatusOK, `{"ok":true,"result":{"id":7,"username":"nexbot","first_name":"Nex Bot"}}`
		}
		return http.StatusOK, `{"ok":true,"result":[]}`
	})
	result, err := provider.Test(context.Background(), telegramTestBot())
	if err != nil {
		t.Fatalf("Test: %v", err)
	}
	if !result.OK || result.Name != "Nex Bot" {
		t.Errorf("Test() = %+v, want ok with the first_name", result)
	}
	if path := fake.last(t).Path; !strings.HasSuffix(path, "/botTOKEN/getMe") {
		t.Errorf("path = %q, want /botTOKEN/getMe", path)
	}
}

// TestTelegramTestFailures covers the three non-ok answers the TS distinguishes.
func TestTelegramTestFailures(t *testing.T) {
	t.Run("a missing credentialRef short-circuits without a request", func(t *testing.T) {
		provider, fake, _ := newTestTelegramProvider(t, nil)
		bot := telegramTestBot()
		bot.CredentialRef = ""
		result, err := provider.Test(context.Background(), bot)
		if err != nil {
			t.Fatalf("Test: %v", err)
		}
		if result.OK || result.Message != "Telegram bot token is missing." {
			t.Errorf("Test() = %+v", result)
		}
		if fake.count() != 0 {
			t.Errorf("a missing token must not reach the API (%d requests)", fake.count())
		}
	})

	t.Run("an HTTP error maps to the getMe failure message", func(t *testing.T) {
		provider, _, _ := newTestTelegramProvider(t, func(recordedRequest) (int, string) {
			return http.StatusUnauthorized, `{"ok":false,"description":"Unauthorized"}`
		})
		result, err := provider.Test(context.Background(), telegramTestBot())
		if err != nil {
			t.Fatalf("Test: %v", err)
		}
		if result.OK || result.Message != "Telegram getMe failed." {
			t.Errorf("Test() = %+v", result)
		}
	})

	t.Run("an ok:false payload carries the provider description", func(t *testing.T) {
		provider, _, _ := newTestTelegramProvider(t, func(recordedRequest) (int, string) {
			return http.StatusOK, `{"ok":false,"description":"bot was blocked"}`
		})
		result, err := provider.Test(context.Background(), telegramTestBot())
		if err != nil {
			t.Fatalf("Test: %v", err)
		}
		if result.OK || result.Message != "bot was blocked" {
			t.Errorf("Test() = %+v", result)
		}
	})
}

// TestTelegramSendFallsBackToPlainText is the Markdown-rejection fallback: a
// Markdown sendMessage that the API rejects is resent without parse_mode.
func TestTelegramSendFallsBackToPlainText(t *testing.T) {
	var calls int
	provider, fake, _ := newTestTelegramProvider(t, func(recordedRequest) (int, string) {
		calls++
		if calls == 1 {
			return http.StatusBadRequest, `{"ok":false,"description":"can't parse entities"}`
		}
		return http.StatusOK, `{"ok":true,"result":{}}`
	})
	err := provider.Send(context.Background(), telegramTestBot(), OutboundMessage{
		ProviderUserID: "42", Text: "*unclosed markdown",
	})
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	if fake.count() != 2 {
		t.Fatalf("got %d requests, want 2 (Markdown then plain text)", fake.count())
	}
	<-fake.mu
	defer func() { fake.mu <- struct{}{} }()
	if _, hasParseMode := fake.records[0].Body["parse_mode"]; !hasParseMode {
		t.Error("the first attempt must carry parse_mode")
	}
	if _, hasParseMode := fake.records[1].Body["parse_mode"]; hasParseMode {
		t.Error("the retry must drop parse_mode")
	}
	if fake.records[1].Body["chat_id"] != "42" {
		t.Errorf("retry chat_id = %v", fake.records[1].Body["chat_id"])
	}
}

// TestTelegramSendChunking pins the 3900-char split and the rule that the inline
// keyboard follows the LAST chunk (Bugfix: 审批提示位于最后一条).
func TestTelegramSendChunking(t *testing.T) {
	provider, fake, _ := newTestTelegramProvider(t, nil)
	long := strings.Repeat("a", 3900*2+10)
	err := provider.Send(context.Background(), telegramTestBot(), OutboundMessage{
		ProviderUserID: "42",
		Text:           long,
		Selection:      &SelectionPrompt{Action: "reply.set", Options: []SelectionOption{{ID: "a", Label: "A"}}},
	})
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	if fake.count() != 3 {
		t.Fatalf("got %d chunks, want 3", fake.count())
	}
	<-fake.mu
	defer func() { fake.mu <- struct{}{} }()
	for i, record := range fake.records {
		_, hasMarkup := record.Body["reply_markup"]
		if i < len(fake.records)-1 && hasMarkup {
			t.Errorf("chunk %d carries the keyboard; only the last chunk may", i)
		}
		if i == len(fake.records)-1 && !hasMarkup {
			t.Error("the last chunk must carry the inline keyboard")
		}
	}
}

// TestTelegramSyncCommands covers the menu sync: enabled publishes the filtered
// command list, disabled clears it, and an absent background flag still allows
// the explicit call (the suppression lives in the runtime, not the provider).
func TestTelegramSyncCommands(t *testing.T) {
	provider, fake, _ := newTestTelegramProvider(t, nil)
	bot := telegramTestBot()
	bot.AllowedCommands.Workspace = false
	bot.AllowedCommands.Reply = false
	if err := provider.SyncCommands(context.Background(), bot); err != nil {
		t.Fatalf("SyncCommands: %v", err)
	}
	record := fake.last(t)
	if !strings.HasSuffix(record.Path, "/setMyCommands") {
		t.Fatalf("path = %q, want setMyCommands", record.Path)
	}
	commands, _ := record.Body["commands"].([]any)
	names := make([]string, 0, len(commands))
	for _, command := range commands {
		entry, _ := command.(map[string]any)
		names = append(names, entry["command"].(string))
	}
	want := []string{"help", "status", "new", "model", "mode", "think", "bind"}
	if len(names) != len(want) {
		t.Fatalf("commands = %v, want %v (workspace and reply are disabled)", names, want)
	}
	for i, name := range want {
		if names[i] != name {
			t.Errorf("commands[%d] = %q, want %q", i, names[i], name)
		}
	}

	bot.Enabled = false
	if err := provider.SyncCommands(context.Background(), bot); err != nil {
		t.Fatalf("SyncCommands(disabled): %v", err)
	}
	if path := fake.last(t).Path; !strings.HasSuffix(path, "/deleteMyCommands") {
		t.Errorf("disabled path = %q, want deleteMyCommands", path)
	}
}

// TestTelegramAcknowledgeCallback covers answerCallbackQuery plus the keyboard
// strip, and the toast truncation at 180 chars.
func TestTelegramAcknowledgeCallback(t *testing.T) {
	provider, fake, _ := newTestTelegramProvider(t, nil)
	payload := map[string]any{"update": map[string]any{
		"callback_query": map[string]any{
			"id":   "cq-1",
			"data": "zc:reply.set:2",
			"message": map[string]any{
				"message_id": float64(55),
				"chat":       map[string]any{"id": float64(42)},
			},
		},
	}}
	if _, err := provider.AcknowledgeCallback(context.Background(), telegramTestBot(), payload,
		strings.Repeat("x", 200), nil); err != nil {
		t.Fatalf("AcknowledgeCallback: %v", err)
	}
	if fake.count() != 2 {
		t.Fatalf("got %d requests, want answerCallbackQuery + editMessageReplyMarkup", fake.count())
	}
	<-fake.mu
	defer func() { fake.mu <- struct{}{} }()
	toast, _ := fake.records[0].Body["text"].(string)
	if len(toast) != 180 {
		t.Errorf("toast length = %d, want 180 (177 + the ellipsis)", len(toast))
	}
	if fake.records[1].Body["message_id"] != float64(55) {
		t.Errorf("message_id = %v", fake.records[1].Body["message_id"])
	}
}

// TestTelegramParseCallback covers the three inbound shapes: a private message, a
// group message with a caption, and an inline-keyboard callback.
func TestTelegramParseCallback(t *testing.T) {
	provider, _, _ := newTestTelegramProvider(t, nil)
	cases := []struct {
		name      string
		payload   map[string]any
		wantCount int
		wantText  string
		wantChat  ChatType
		wantMsgID string
	}{
		{
			name: "private text message",
			payload: map[string]any{"botId": "t1", "update": map[string]any{
				"message": map[string]any{
					"message_id": float64(11),
					"text":       "hello",
					"from":       map[string]any{"id": float64(9), "username": "alice"},
					"chat":       map[string]any{"id": float64(9), "type": "private"},
				},
			}},
			wantCount: 1, wantText: "hello", wantChat: ChatPrivate, wantMsgID: "11",
		},
		{
			name: "group caption message",
			payload: map[string]any{"botId": "t1", "update": map[string]any{
				"message": map[string]any{
					"message_id": float64(12),
					"caption":    "a photo",
					"from":       map[string]any{"id": float64(9)},
					"chat":       map[string]any{"id": float64(-100), "type": "group"},
				},
			}},
			wantCount: 1, wantText: "a photo", wantChat: ChatGroup, wantMsgID: "12",
		},
		{
			name: "callback query decodes the command",
			payload: map[string]any{"botId": "t1", "update": map[string]any{
				"callback_query": map[string]any{
					"id":      "cq-9",
					"data":    "zc:reply.set:2",
					"from":    map[string]any{"id": float64(9)},
					"message": map[string]any{"chat": map[string]any{"id": float64(9), "type": "private"}},
				},
			}},
			wantCount: 1, wantText: "/reply.set 2", wantChat: ChatPrivate, wantMsgID: "cq-9",
		},
		{
			name: "an update without a bot id is dropped",
			payload: map[string]any{"update": map[string]any{
				"message": map[string]any{"text": "x", "from": map[string]any{"id": float64(1)},
					"chat": map[string]any{"id": float64(1), "type": "private"}},
			}},
			wantCount: 0,
		},
		{
			name:      "an update with no message is dropped",
			payload:   map[string]any{"botId": "t1", "update": map[string]any{}},
			wantCount: 0,
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			messages := provider.ParseCallback(testCase.payload)
			if len(messages) != testCase.wantCount {
				t.Fatalf("got %d messages, want %d", len(messages), testCase.wantCount)
			}
			if testCase.wantCount == 0 {
				return
			}
			message := messages[0]
			if message.Text != testCase.wantText {
				t.Errorf("text = %q, want %q", message.Text, testCase.wantText)
			}
			if message.Actor.ChatType != testCase.wantChat {
				t.Errorf("chatType = %q, want %q", message.Actor.ChatType, testCase.wantChat)
			}
			if message.Actor.ProviderMessageID != testCase.wantMsgID {
				t.Errorf("providerMessageId = %q, want %q", message.Actor.ProviderMessageID, testCase.wantMsgID)
			}
		})
	}
}

// TestTelegramAttachmentSlotOrder pins the fixed five slots and the "last photo
// wins" rule.
func TestTelegramAttachmentSlotOrder(t *testing.T) {
	message := map[string]any{
		"message_id": float64(1),
		"photo":      []any{map[string]any{"file_id": "small"}, map[string]any{"file_id": "big", "file_size": float64(99)}},
		"document":   map[string]any{"file_id": "doc", "file_name": "a.pdf"},
		"voice":      map[string]any{"file_id": "voice"},
		"from":       map[string]any{"id": float64(9)},
		"chat":       map[string]any{"id": float64(9), "type": "private"},
	}
	attachments := readTelegramAttachments(message)
	if len(attachments) != 3 {
		t.Fatalf("got %d attachments, want 3 (photo, document, voice)", len(attachments))
	}
	if attachments[0].ProviderFileID != "big" {
		t.Errorf("photo = %q, want the LAST (largest) photo", attachments[0].ProviderFileID)
	}
	if attachments[0].Kind != AttachmentImage || attachments[0].MimeType != "image/jpeg" {
		t.Errorf("photo kind/mime = %q/%q", attachments[0].Kind, attachments[0].MimeType)
	}
	if attachments[1].Kind != AttachmentFile || attachments[1].Filename != "a.pdf" {
		t.Errorf("document = %+v", attachments[1])
	}
	if attachments[2].Kind != AttachmentAudio || attachments[2].Filename != "telegram-voice.ogg" {
		t.Errorf("voice = %+v", attachments[2])
	}
}

// TestTelegramCallbackDataRoundTrip pins the 64-byte rule: only the list index
// travels, so a long workspace id or path is never sent to Telegram.
func TestTelegramCallbackDataRoundTrip(t *testing.T) {
	// A workspace identity long enough to break Telegram's 64-byte callback_data
	// limit if the id (rather than the index) travelled.
	selection := &SelectionPrompt{
		Action: "workspace.set",
		Options: []SelectionOption{{
			ID:    "ssh://a-very-long-identity@example.com:/home/user/project",
			Label: "L",
		}},
	}
	markup := BuildSelectionReplyMarkup(selection)
	rows, _ := markup["inline_keyboard"].([]any)
	if len(rows) != 2 {
		t.Fatalf("got %d rows, want 2 (option + cancel)", len(rows))
	}
	first, _ := rows[0].([]any)
	button, _ := first[0].(map[string]any)
	data, _ := button["callback_data"].(string)
	if data != "zc:workspace:1" {
		t.Errorf("callback_data = %q, want zc:workspace:1 (the index, not the id)", data)
	}
	if len(data) > 64 {
		t.Errorf("callback_data is %d bytes, over Telegram's 64-byte limit", len(data))
	}
	if got := DecodeTelegramCallbackData(data); got != "/workspace 1" {
		t.Errorf("round trip = %q, want /workspace 1", got)
	}
	second, _ := rows[1].([]any)
	cancel, _ := second[0].(map[string]any)
	if cancel["callback_data"] != "zc:cancel" {
		t.Errorf("cancel callback_data = %v", cancel["callback_data"])
	}
	if got := DecodeTelegramCallbackData("zc:cancel"); got != "/cancel" {
		t.Errorf("cancel round trip = %q", got)
	}
}

// TestTelegramShowCancelFalseOmitsTheCancelButton covers the selection flag.
func TestTelegramShowCancelFalseOmitsTheCancelButton(t *testing.T) {
	selection := &SelectionPrompt{
		Action:     "reply.set",
		Options:    []SelectionOption{{ID: "a", Label: "A"}},
		ShowCancel: BoolPtr(false),
	}
	markup := BuildSelectionReplyMarkup(selection)
	rows, _ := markup["inline_keyboard"].([]any)
	if len(rows) != 1 {
		t.Errorf("got %d rows, want 1 (no cancel button)", len(rows))
	}
}

// TestTelegramDownloadAttachment drives the two-step download (getFile, then the
// file endpoint) against two loopback servers: the Bot API fake and a plain byte
// server standing in for the Telegram CDN.
func TestTelegramDownloadAttachment(t *testing.T) {
	fileServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("file-bytes"))
	}))
	t.Cleanup(fileServer.Close)

	fake := newFakeTelegram(t, func(record recordedRequest) (int, string) {
		return http.StatusOK, `{"ok":true,"result":{"file_path":"docs/a.pdf","file_size":9}}`
	})
	credentials := newStubCredentials()
	credentials.values["bot:t1:credential"] = "TOKEN"
	provider := &TelegramProvider{
		Client: &ProviderClient{
			Doer: &routingDoer{
				apiBase: fake.server.URL,
				file:    fileServer.URL,
			},
		},
		Credential: credentials,
	}
	downloaded, err := provider.DownloadAttachment(context.Background(), telegramTestBot(),
		Attachment{ID: "f1", Kind: AttachmentFile, Filename: "a.pdf", ProviderFileID: "file-1"}, nil)
	if err != nil {
		t.Fatalf("DownloadAttachment: %v", err)
	}
	if downloaded == nil {
		t.Fatal("DownloadAttachment returned nil")
	}
	if string(downloaded.Data) != "file-bytes" {
		t.Errorf("data = %q", downloaded.Data)
	}
	if downloaded.Attachment.SizeBytes == nil || *downloaded.Attachment.SizeBytes != 9 {
		t.Errorf("sizeBytes = %v, want the 9 from getFile", downloaded.Attachment.SizeBytes)
	}
}

// routingDoer sends TelegramBaseURL requests to the Bot API fake and the
// /file/ requests to the CDN fake, so neither provider package variable is
// mutated from two parallel tests.
type routingDoer struct {
	apiBase string
	file    string
}

func (r *routingDoer) Do(req *http.Request) (*http.Response, error) {
	base := r.apiBase
	if strings.Contains(req.URL.Path, "/file/bot") {
		base = r.file
	}
	target := *req.URL
	target.Scheme = "http"
	target.Host = strings.TrimPrefix(base, "http://")
	cloned := req.Clone(req.Context())
	cloned.URL = &target
	cloned.Host = target.Host
	return http.DefaultClient.Do(cloned)
}

// TestTelegramDownloadAttachmentMissingID proves a nil result (not an error)
// when there is nothing to download.
func TestTelegramDownloadAttachmentMissingID(t *testing.T) {
	provider, _, _ := newTestTelegramProvider(t, nil)
	downloaded, err := provider.DownloadAttachment(context.Background(), telegramTestBot(),
		Attachment{ID: "f1", Kind: AttachmentFile}, nil)
	if err != nil {
		t.Fatalf("DownloadAttachment: %v", err)
	}
	if downloaded != nil {
		t.Errorf("expected nil for an attachment without a providerFileId, got %+v", downloaded)
	}
}
