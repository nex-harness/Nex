package bots

import (
	"context"
	"crypto/aes"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// fakeWeixinServer serves the iLink `/ilink/bot/<path>` protocol shape.
type fakeWeixinServer struct {
	server  *httptest.Server
	mu      chan struct{}
	records []recordedRequest
}

func newFakeWeixin(t *testing.T, handler func(recordedRequest) (int, string)) *fakeWeixinServer {
	t.Helper()
	fake := &fakeWeixinServer{mu: make(chan struct{}, 1)}
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
		status, payload := http.StatusOK, `{"ret":0,"errcode":0}`
		if handler != nil {
			status, payload = handler(record)
		}
		w.Header().Set("content-type", "application/json")
		w.WriteHeader(status)
		_, _ = io.WriteString(w, payload)
	}))
	fake.server = server
	t.Cleanup(server.Close)
	previous := WeixinBaseURL
	WeixinBaseURL = server.URL
	t.Cleanup(func() { WeixinBaseURL = previous })
	return fake
}

func (f *fakeWeixinServer) last(t *testing.T) recordedRequest {
	t.Helper()
	<-f.mu
	defer func() { f.mu <- struct{}{} }()
	if len(f.records) == 0 {
		t.Fatal("no request reached the fake iLink server")
	}
	return f.records[len(f.records)-1]
}

func newTestWeixinProvider(t *testing.T, handler func(recordedRequest) (int, string)) (*WeixinProvider, *fakeWeixinServer, *stubCredentials) {
	t.Helper()
	fake := newFakeWeixin(t, handler)
	credentials := newStubCredentials()
	credentials.values["bot:w1:credential"] = "ILINKTOKEN"
	provider := &WeixinProvider{
		Client:     &ProviderClient{Doer: fake.server.Client()},
		Credential: credentials,
		NewUUID:    func() string { return "nex-weixin-fixed" },
	}
	return provider, fake, credentials
}

func weixinTestBot() Config {
	return Config{
		ID: "w1", Provider: ProviderWeixin, Enabled: true,
		CredentialRef: BuildCredentialKey("w1"), ProviderUserID: "self",
		AllowedCommands: DefaultAllowedCommands(),
	}
}

// TestWeixinSendProtocol pins the sendmessage shape: /ilink/bot/sendmessage, the
// Bearer + AuthorizationType + X-WECHAT-UIN headers, the base_info envelope and
// the text inside msg.item_list.
func TestWeixinSendProtocol(t *testing.T) {
	provider, fake, _ := newTestWeixinProvider(t, nil)
	bot := weixinTestBot()
	bot.CredentialRef = "bot:w1:credential"
	err := provider.Send(context.Background(), bot, OutboundMessage{
		ProviderUserID: "u-1",
		Text:           "line one\nline two",
	})
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	record := fake.last(t)
	if !strings.HasSuffix(record.Path, "/ilink/bot/sendmessage") {
		t.Errorf("path = %q", record.Path)
	}
	if got := record.Header.Get("Authorization"); got != "Bearer ILINKTOKEN" {
		t.Errorf("Authorization = %q", got)
	}
	if got := record.Header.Get("AuthorizationType"); got != "ilink_bot_token" {
		t.Errorf("AuthorizationType = %q", got)
	}
	if record.Header.Get("X-WECHAT-UIN") == "" {
		t.Error("X-WECHAT-UIN must be present")
	}
	baseInfo, _ := record.Body["base_info"].(map[string]any)
	if baseInfo["channel_version"] != weixinChannelVersion {
		t.Errorf("base_info = %v", record.Body["base_info"])
	}
	msg, _ := record.Body["msg"].(map[string]any)
	if msg["message_type"] != float64(2) || msg["message_state"] != float64(2) {
		t.Errorf("msg flags = %v/%v", msg["message_type"], msg["message_state"])
	}
	if msg["client_id"] != "nex-weixin-fixed" {
		t.Errorf("client_id = %v", msg["client_id"])
	}
	items, _ := msg["item_list"].([]any)
	if len(items) != 1 {
		t.Fatalf("item_list has %d items, want 1", len(items))
	}
	item, _ := items[0].(map[string]any)
	textItem, _ := item["text_item"].(map[string]any)
	// Bugfix: LF is normalised to CRLF before sending.
	if textItem["text"] != "line one\r\nline two" {
		t.Errorf("text = %q, want CRLF hard breaks", textItem["text"])
	}
}

// TestWeixinErrorShapes covers the three non-ok iLink answers.
func TestWeixinErrorShapes(t *testing.T) {
	t.Run("a missing token is an error, not a silent no-op", func(t *testing.T) {
		provider, _, _ := newTestWeixinProvider(t, nil)
		bot := weixinTestBot()
		bot.CredentialRef = ""
		err := provider.Send(context.Background(), bot, OutboundMessage{ProviderUserID: "u"})
		if err == nil || !strings.Contains(err.Error(), "token is missing") {
			t.Fatalf("err = %v, want the missing-token error", err)
		}
	})

	t.Run("a non-zero ret surfaces the errmsg", func(t *testing.T) {
		provider, _, _ := newTestWeixinProvider(t, func(recordedRequest) (int, string) {
			return http.StatusOK, `{"ret":-1,"errmsg":"session expired"}`
		})
		err := provider.Send(context.Background(), weixinTestBot(), OutboundMessage{ProviderUserID: "u"})
		if err == nil || !strings.Contains(err.Error(), "session expired") {
			t.Fatalf("err = %v", err)
		}
	})

	t.Run("an HTTP failure names the path", func(t *testing.T) {
		provider, _, _ := newTestWeixinProvider(t, func(recordedRequest) (int, string) {
			return http.StatusBadGateway, `{}`
		})
		err := provider.Send(context.Background(), weixinTestBot(), OutboundMessage{ProviderUserID: "u"})
		if err == nil || !strings.Contains(err.Error(), "/sendmessage failed: HTTP 502") {
			t.Fatalf("err = %v", err)
		}
	})
}

// TestWeixinParseCallback covers the getUpdates response shape: the data
// container, the several list key spellings, the bot-echo filter and the
// group/private discriminator.
func TestWeixinParseCallback(t *testing.T) {
	provider, _, _ := newTestWeixinProvider(t, nil)
	cases := []struct {
		name      string
		payload   map[string]any
		wantCount int
		wantText  string
		wantChat  ChatType
	}{
		{
			name: "text message in a data container",
			payload: map[string]any{"botId": "w1", "data": map[string]any{
				"msgs": []any{map[string]any{
					"message_type": float64(1),
					"from_user_id": "u-1",
					"id":           "m-1",
					"content":      "hello",
					"nickname":     "Ada",
				}},
			}},
			wantCount: 1, wantText: "hello", wantChat: ChatPrivate,
		},
		{
			name: "room id makes it a group",
			payload: map[string]any{"botId": "w1", "data": map[string]any{
				"messages": []any{map[string]any{
					"message_type": float64(1),
					"from_user_id": "u-1",
					"msgid":        "m-2",
					"room":         "room-9",
					"text":         "hi team",
				}},
			}},
			wantCount: 1, wantText: "hi team", wantChat: ChatGroup,
		},
		{
			name: "item_list text items are joined",
			payload: map[string]any{"botId": "w1", "data": map[string]any{
				"list": []any{map[string]any{
					"from_user_id": "u-1",
					"item_list": []any{
						map[string]any{"text_item": map[string]any{"text": "first"}},
						map[string]any{"text_item": map[string]any{"text": "second"}},
					},
				}},
			}},
			wantCount: 1, wantText: "first\nsecond", wantChat: ChatPrivate,
		},
		{
			name: "the bot's own message_type=2 echo is dropped",
			payload: map[string]any{"botId": "w1", "data": map[string]any{
				"msgs": []any{map[string]any{
					"message_type": float64(2),
					"from_user_id": "u-1",
					"content":      "echo",
				}},
			}},
			wantCount: 0,
		},
		{
			name: "a message with no user id is dropped",
			payload: map[string]any{"botId": "w1", "data": map[string]any{
				"msgs": []any{map[string]any{"content": "orphan"}},
			}},
			wantCount: 0,
		},
		{
			name:      "no bot id drops the whole payload",
			payload:   map[string]any{"data": map[string]any{"msgs": []any{map[string]any{"from_user_id": "u", "content": "x"}}}},
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
			if messages[0].Text != testCase.wantText {
				t.Errorf("text = %q, want %q", messages[0].Text, testCase.wantText)
			}
			if messages[0].Actor.ChatType != testCase.wantChat {
				t.Errorf("chatType = %q, want %q", messages[0].Actor.ChatType, testCase.wantChat)
			}
		})
	}
}

// TestWeixinNextBufIsReadNotAdvanced proves the polling cursor is READ here and
// that its several spellings all resolve, because the runtime decides when to
// persist it (after the batch is processed).
func TestWeixinNextBufIsReadNotAdvanced(t *testing.T) {
	for _, key := range []string{"get_updates_buf", "buf", "next_buf", "nextBuf", "getUpdatesBuf", "syncKey"} {
		payload := map[string]any{"data": map[string]any{key: "cursor-" + key}}
		if got := ReadWeixinNextBuf(payload); got != "cursor-"+key {
			t.Errorf("ReadWeixinNextBuf with %q = %q", key, got)
		}
	}
}

// TestWeixinAesKeyParsing covers the three accepted spellings and the rejection.
func TestWeixinAesKeyParsing(t *testing.T) {
	hexKey := "00112233445566778899aabbccddeeff"
	if got := ParseWeixinAesKey(hexKey); len(got) != 16 {
		t.Errorf("hex key parsed to %d bytes, want 16", len(got))
	}
	if got := ParseWeixinAesKey(base64.StdEncoding.EncodeToString([]byte{0, 17, 34, 51, 68, 85, 102, 119, 136, 153, 170, 187, 204, 221, 238, 255})); len(got) != 16 {
		t.Errorf("base64 key parsed to %d bytes, want 16", len(got))
	}
	if got := ParseWeixinAesKey(base64.StdEncoding.EncodeToString([]byte(hexKey))); len(got) != 16 {
		t.Errorf("base64-of-hex key parsed to %d bytes, want 16", len(got))
	}
	if got := ParseWeixinAesKey("not-a-key"); got != nil {
		t.Errorf("an invalid key parsed to %x, want nil", got)
	}
}

// TestWeixinAttachmentDecryption proves the CDN AES-128-ECB round trip: an
// encrypted download must come back as the original plaintext, never as
// ciphertext saved with a .jpg name.
func TestWeixinAttachmentDecryption(t *testing.T) {
	key := []byte{0, 17, 34, 51, 68, 85, 102, 119, 136, 153, 170, 187, 204, 221, 238, 255}
	plaintext := []byte("the original image bytes")
	ciphertext := encryptECB(t, key, plaintext)

	fileServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(ciphertext)
	}))
	t.Cleanup(fileServer.Close)

	provider := &WeixinProvider{Client: &ProviderClient{Doer: fileServer.Client()}}
	downloaded, err := provider.DownloadAttachment(context.Background(), weixinTestBot(), Attachment{
		ID: "1", Kind: AttachmentImage, Filename: "a.jpg",
		DownloadURL: fileServer.URL, ProviderMeta: map[string]string{weixinAesKey: hex.EncodeToString(key)},
	}, nil)
	if err != nil {
		t.Fatalf("DownloadAttachment: %v", err)
	}
	if string(downloaded.Data) != string(plaintext) {
		t.Errorf("data = %q, want the decrypted plaintext", downloaded.Data)
	}
}

// TestWeixinAttachmentWithoutKeyIsNotDownloaded covers the guard.
func TestWeixinAttachmentWithoutKeyIsNotDownloaded(t *testing.T) {
	provider := &WeixinProvider{}
	downloaded, err := provider.DownloadAttachment(context.Background(), weixinTestBot(),
		Attachment{ID: "1", Kind: AttachmentImage, DownloadURL: "http://example.invalid/x"}, nil)
	if err != nil || downloaded != nil {
		t.Errorf("got %+v, %v; want nil, nil without an AES key", downloaded, err)
	}
}

func encryptECB(t *testing.T, key, plaintext []byte) []byte {
	t.Helper()
	block, err := aes.NewCipher(key)
	if err != nil {
		t.Fatal(err)
	}
	padding := aes.BlockSize - len(plaintext)%aes.BlockSize
	padded := append(append([]byte{}, plaintext...), bytesRepeat(byte(padding), padding)...)
	out := make([]byte, len(padded))
	for offset := 0; offset < len(padded); offset += aes.BlockSize {
		block.Encrypt(out[offset:offset+aes.BlockSize], padded[offset:offset+aes.BlockSize])
	}
	return out
}

func bytesRepeat(value byte, count int) []byte {
	out := make([]byte, count)
	for i := range out {
		out[i] = value
	}
	return out
}
