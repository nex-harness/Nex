// provider_weixin.go ports packages/services/src/bots/providers/weixinProvider.ts.
//
// WeChat has NO public SDK: the TS talks to the Weixin iLink bot gateway over
// plain HTTPS (`https://ilinkai.weixin.qq.com/ilink/bot/<path>`) with a
// Bearer token, a random X-WECHAT-UIN header and an AES-128-ECB key for CDN
// attachments. There is therefore no Go SDK to evaluate (recorded in
// decisions.md): the Go port IS raw HTTP against the same endpoints.
package bots

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// WeixinILinkBaseURL is DEFAULT_WEIXIN_ILINK_BASE_URL.
const WeixinILinkBaseURL = "https://ilinkai.weixin.qq.com"

// weixinBotAPIPrefix is WEIXIN_BOT_API_PREFIX.
const weixinBotAPIPrefix = "/ilink/bot"

const (
	weixinChannelVersion     = "2.0.0"
	weixinMessageTypeBot     = 2
	weixinMessageStateFinish = 2
	weixinGetUpdatesTimeout  = 90 * time.Second
	// WeixinAesAlgorithm is WEIXIN_CDN_AES_ALGORITHM (AES-128-ECB, no IV).
	weixinAesAlgorithm = "aes-128-ecb"
	// weixinAesKey is the provider metadata key carrying the CDN AES key.
	weixinAesKey = "weixinAesKey"
)

// WeixinBaseURL is a variable so a test can point the provider at an httptest
// server; production leaves it at the built-in address (no env var, D7).
var WeixinBaseURL = WeixinILinkBaseURL

// WeixinProvider implements ProviderAdapter against the Weixin iLink gateway.
type WeixinProvider struct {
	Client     *ProviderClient
	Credential CredentialLoader
	// NewUUID mints the per-message client id; nil uses a random hex id.
	NewUUID func() string
}

func (p *WeixinProvider) client() *ProviderClient {
	if p.Client != nil {
		return p.Client
	}
	return &ProviderClient{}
}

func (p *WeixinProvider) token(ctx context.Context, bot Config) (string, error) {
	if bot.CredentialRef == "" {
		return "", nil
	}
	return p.Credential.Load(ctx, bot.CredentialRef)
}

// weixinAPIURL ports getWeixinApiBaseUrl + WEIXIN_BOT_API_PREFIX. It is a
// built-in channel address, NOT bot.webhookUrl (TS comment: 微信 iLink 是内置
// 通道地址，不应复用 webhookUrl).
func (p *WeixinProvider) weixinAPIURL(path string) string {
	base := strings.TrimRight(WeixinBaseURL, "/")
	return base + weixinBotAPIPrefix + path
}

// requestWeixinJSON ports requestWeixinJson: every body is wrapped in
// base_info.channel_version, a non-zero ret/errcode is an error, and a missing
// token is an error rather than a silent no-op.
func (p *WeixinProvider) requestWeixinJSON(ctx context.Context, bot Config, path string, body any) (any, error) {
	token, err := p.token(ctx, bot)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(token) == "" {
		return nil, fmt.Errorf("Weixin iLink bot token is missing. Scan the Weixin login QR code first.")
	}
	response, err := p.postWithHeaders(ctx, p.weixinAPIURL(path), map[string]string{
		"AuthorizationType": "ilink_bot_token",
		"Authorization":     "Bearer " + strings.TrimSpace(token),
		"X-WECHAT-UIN":      buildRandomWechatUIN(),
	}, appendBaseInfo(body))
	if err != nil {
		return nil, err
	}
	if !response.OK {
		return nil, fmt.Errorf("Weixin iLink %s failed: HTTP %d", path, response.Status)
	}
	payload := response.Payload
	record, _ := payload.(map[string]any)
	ret := readOptionalNumber(record, "ret")
	errcode := readOptionalNumber(record, "errcode")
	if (ret != nil && *ret != 0) || (errcode != nil && *errcode != 0) {
		message := firstNonEmptyString(readWeixinString(record, "errmsg"), readWeixinString(record, "message"))
		if message == "" {
			message = fmt.Sprintf("ret=%s errcode=%s", optionalNumberString(ret), optionalNumberString(errcode))
		}
		return nil, fmt.Errorf("Weixin iLink %s failed: %s", path, message)
	}
	return payload, nil
}

// postWithHeaders performs the iLink POST under its own timeout.
func (p *WeixinProvider) postWithHeaders(ctx context.Context, endpoint string, headers map[string]string, body any) (JSONResponse, error) {
	client := p.client()
	req, err := newAuthRequest(ctx, client, http.MethodPost, endpoint, "", body)
	if err != nil {
		return JSONResponse{}, err
	}
	for name, value := range headers {
		req.Header.Set(name, value)
	}
	if req.Header.Get("Authorization") == "Bearer " {
		req.Header.Del("Authorization")
	}
	doer := client.Doer
	if doer == nil {
		doer = http.DefaultClient
	}
	timeout := client.Timeout
	if timeout <= 0 {
		timeout = ProviderRequestTimeout
	}
	reqCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	resp, err := doer.Do(req.WithContext(reqCtx))
	if err != nil {
		return JSONResponse{}, err
	}
	defer resp.Body.Close()
	return readJSONResponse(resp)
}

// appendBaseInfo ports appendBaseInfo: every request carries the channel
// version, and the caller's own base_info (if any) wins.
func appendBaseInfo(body any) any {
	record, ok := body.(map[string]any)
	if !ok {
		return body
	}
	merged := map[string]any{"base_info": map[string]any{"channel_version": weixinChannelVersion}}
	for key, value := range record {
		merged[key] = value
	}
	return merged
}

// buildRandomWechatUIN ports buildRandomWechatUin: a base64 of a random uint32.
func buildRandomWechatUIN() string {
	value, err := rand.Int(rand.Reader, big.NewInt(0x1_0000_0000))
	if err != nil {
		return base64.StdEncoding.EncodeToString([]byte("nex"))
	}
	return base64.StdEncoding.EncodeToString([]byte(strconv.FormatUint(value.Uint64(), 10)))
}

func readWeixinString(record map[string]any, key string) string {
	if record == nil {
		return ""
	}
	value, _ := record[key].(string)
	return value
}

func readOptionalNumber(record map[string]any, key string) *int {
	if record == nil {
		return nil
	}
	value, ok := record[key].(float64)
	if !ok {
		return nil
	}
	converted := int(value)
	return &converted
}

func readNumber(record map[string]any, key string) (int, bool) {
	if record == nil {
		return 0, false
	}
	value, ok := record[key].(float64)
	if !ok {
		return 0, false
	}
	return int(value), true
}

func readNumberOrString(record map[string]any, key string) string {
	if record == nil {
		return ""
	}
	switch value := record[key].(type) {
	case string:
		return value
	case float64:
		return jsonNumberString(value)
	}
	return ""
}

func optionalNumberString(value *int) string {
	if value == nil {
		return ""
	}
	return strconv.Itoa(*value)
}

// Test ports the weixin test(): /getconfig proves the token works.
func (p *WeixinProvider) Test(ctx context.Context, bot Config) (TestResult, error) {
	if !bot.Enabled {
		return TestResult{OK: false, Message: "Weixin bot is disabled."}, nil
	}
	if bot.CredentialRef == "" {
		return TestResult{OK: false, Message: "Weixin iLink bot token is missing. Scan the Weixin login QR code first."}, nil
	}
	if _, err := p.requestWeixinJSON(ctx, bot, "/getconfig", nil); err != nil {
		return TestResult{}, err
	}
	return TestResult{OK: true, Message: "Weixin iLink API is reachable."}, nil
}

// Send ports the weixin send(): /sendmessage with the text inside
// msg.item_list, and LF normalised to CRLF (Bugfix: 微信 iLink 纯文本在不同
// 客户端上对 LF 的处理不完全一致).
func (p *WeixinProvider) Send(ctx context.Context, bot Config, message OutboundMessage) error {
	msg := map[string]any{
		"from_user_id":  bot.ProviderUserID,
		"to_user_id":    message.ProviderUserID,
		"client_id":     p.clientID(),
		"message_type":  weixinMessageTypeBot,
		"message_state": weixinMessageStateFinish,
		"item_list": []any{map[string]any{
			"type":      1,
			"text_item": map[string]any{"text": BuildWeixinText(message.Text)},
		}},
	}
	if message.ProviderContextToken != "" {
		msg["context_token"] = message.ProviderContextToken
	}
	_, err := p.requestWeixinJSON(ctx, bot, "/sendmessage", map[string]any{"msg": msg})
	return err
}

func (p *WeixinProvider) clientID() string {
	if p.NewUUID != nil {
		return p.NewUUID()
	}
	return "nex-weixin-" + randomHex(16)
}

func randomHex(bytesCount int) string {
	buffer := make([]byte, bytesCount)
	if _, err := rand.Read(buffer); err != nil {
		return "0000000000000000"
	}
	return hex.EncodeToString(buffer)
}

// BuildWeixinText ports buildWeixinText: CRLF hard line breaks.
func BuildWeixinText(text string) string {
	text = strings.ReplaceAll(text, "\r\n", "\n")
	text = strings.ReplaceAll(text, "\r", "\n")
	return strings.ReplaceAll(text, "\n", "\r\n")
}

// SendTyping ports the weixin sendTyping(): fetch a typing ticket, then send it.
func (p *WeixinProvider) SendTyping(ctx context.Context, bot Config, target TypingTarget) error {
	body := map[string]any{"ilink_user_id": target.ProviderUserID}
	if target.ProviderContextToken != "" {
		body["context_token"] = target.ProviderContextToken
	}
	payload, err := p.requestWeixinJSON(ctx, bot, "/getconfig", body)
	if err != nil {
		return err
	}
	record, _ := payload.(map[string]any)
	ticket := readWeixinString(record, "typing_ticket")
	if ticket == "" {
		return nil
	}
	_, err = p.requestWeixinJSON(ctx, bot, "/sendtyping", map[string]any{
		"ilink_user_id": target.ProviderUserID,
		"typing_ticket": ticket,
		"status":        1,
	})
	return err
}

// StopTyping is a no-op: the typing status expires on its own.
func (p *WeixinProvider) StopTyping(context.Context, Config, TypingTarget) error { return nil }

// SyncCommands is a no-op: iLink has no command menu.
func (p *WeixinProvider) SyncCommands(context.Context, Config) error { return nil }

// ResolveName: iLink does not expose the bot's own display name.
func (p *WeixinProvider) ResolveName(context.Context, Config) (string, error) { return "", nil }

// AcknowledgeCallback: iLink has no callback query to answer.
func (p *WeixinProvider) AcknowledgeCallback(context.Context, Config, map[string]any, string, *OutboundMessage) (AcknowledgeResult, error) {
	return AcknowledgeResult{}, nil
}

// PrepareCallbackPayload is a passthrough.
func (p *WeixinProvider) PrepareCallbackPayload(_ context.Context, _ Config, payload map[string]any) (map[string]any, error) {
	return payload, nil
}

// HandleCallbackResponse returns nil: weixin polls, it has no HTTP callback.
func (p *WeixinProvider) HandleCallbackResponse(context.Context, Config, map[string]any) (*CallbackResponse, error) {
	return nil, nil
}

// CreateStreamingReplyCard / UpdateStreamingReplyCard: iLink has no cards.
func (p *WeixinProvider) CreateStreamingReplyCard(context.Context, Config, StreamingCardState) (*CardHandle, error) {
	return nil, nil
}

func (p *WeixinProvider) UpdateStreamingReplyCard(context.Context, Config, CardHandle, StreamingCardState) error {
	return nil
}

// DownloadAttachment ports the weixin downloadAttachment(): fetch the CDN URL
// and decrypt it with the AES-128-ECB key carried in provider metadata. Saving
// the ciphertext would produce an unrecognisable file.
func (p *WeixinProvider) DownloadAttachment(ctx context.Context, _ Config, attachment Attachment, _ *Actor) (*DownloadedAttachment, error) {
	aesKey := attachment.ProviderMeta[weixinAesKey]
	if attachment.DownloadURL == "" || aesKey == "" {
		return nil, nil
	}
	data, err := fetchRawURL(ctx, p.client(), attachment.DownloadURL)
	if err != nil {
		return nil, fmt.Errorf("Weixin attachment download failed: %v", err)
	}
	plain, err := decryptWeixinCDNMedia(data, aesKey)
	if err != nil {
		return nil, err
	}
	return &DownloadedAttachment{Attachment: attachment, Data: plain}, nil
}

// fetchRawURL performs a GET and returns the bytes (the iLink CDN is a plain
// HTTPS host, not the Bot API prefix).
func fetchRawURL(ctx context.Context, client *ProviderClient, url string) ([]byte, error) {
	if client == nil {
		client = &ProviderClient{}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	doer := client.Doer
	if doer == nil {
		doer = http.DefaultClient
	}
	resp, err := doer.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	return io.ReadAll(resp.Body)
}
