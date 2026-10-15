// provider_feishu.go ports the payload parsing and HTTP paths of
// packages/services/src/bots/providers/feishuProvider.ts.
//
// Scope note, and the reason this file is not a straight port of all 1902 TS
// lines: the TS uses the Lark node-sdk for exactly ONE thing — the WebSocket
// long connection (`await import("@larksuiteoapi/node-sdk")`, line 1514). Every
// HTTP call in the file is hand-written fetch (tenant_access_token, im/v1/messages,
// interactive/v1/card/update, contact/v3/users). Those are ported here against the
// same REST endpoints, so the wire behaviour is identical without adopting a
// second SDK whose long-connection protocol version is not pinned by the TS
// either. The WebSocket client itself is Phase-4 scope: it needs the Go agent's
// long-connection plumbing and a bot runtime lifecycle, and no Go caller in this
// step starts one.
//
// Feishu events arrive over the WebSocket connection, not over /api/bots — the
// route rejects feishu with `Provider feishu does not support HTTP callbacks.` —
// so the parse half here is exercised by unit tests and the send half by the
// httptest fake below.
package bots

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// FeishuFeishuBaseURL / LarkBaseURL mirror getFeishuBaseUrl.
const (
	FeishuFeishuBaseURL = "https://open.feishu.cn"
	LarkBaseURL         = "https://open.larksuite.com"
)

// FeishuProvider implements ProviderAdapter for feishu and lark.
type FeishuProvider struct {
	Client     *ProviderClient
	Credential CredentialLoader
	// Now stamps nothing today (Feishu carries no sentAt); it exists so a test
	// can pin time without touching the struct shape later.
	Now func() time.Time
}

func (p *FeishuProvider) client() *ProviderClient {
	if p.Client != nil {
		return p.Client
	}
	return &ProviderClient{}
}

// feishuBaseURLOverride replaces the API host in tests. It is a package
// variable rather than an env var so no new configuration surface is introduced
// (D7) and a production build cannot be pointed at a third-party host by
// accident; the test restores it through Cleanup.
var feishuBaseURLOverrideValue string

// baseURL ports getFeishuBaseUrl: lark uses open.larksuite.com, everything else
// open.feishu.cn.
func (p *FeishuProvider) baseURL(bot Config) string {
	if feishuBaseURLOverrideValue != "" {
		return feishuBaseURLOverrideValue
	}
	if bot.Provider == ProviderLark {
		return LarkBaseURL
	}
	return FeishuFeishuBaseURL
}

// feishuBaseURLOverride sets the test host and returns the restore function.
func feishuBaseURLOverride(baseURL string) func() {
	previous := feishuBaseURLOverrideValue
	feishuBaseURLOverrideValue = baseURL
	return func() { feishuBaseURLOverrideValue = previous }
}

func (p *FeishuProvider) appSecret(ctx context.Context, bot Config) (string, error) {
	// The Feishu app secret is stored under the bot's credential ref, exactly
	// like telegram's token (readFeishuAppSecret → deps.loadCredential).
	if bot.CredentialRef == "" {
		return "", nil
	}
	return p.Credential.Load(ctx, bot.CredentialRef)
}

// tenantAccessToken ports readTenantAccessToken.
func (p *FeishuProvider) tenantAccessToken(ctx context.Context, bot Config) (string, error) {
	secret, err := p.appSecret(ctx, bot)
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(secret) == "" || strings.TrimSpace(bot.FeishuAppID) == "" {
		return "", nil
	}
	response, err := p.client().FetchJSON(ctx, http.MethodPost,
		p.baseURL(bot)+"/open-apis/auth/v3/tenant_access_token/internal",
		map[string]any{"app_id": bot.FeishuAppID, "app_secret": secret})
	if err != nil {
		return "", err
	}
	if !response.OK {
		return "", fmt.Errorf("Feishu tenant_access_token failed: HTTP %d", response.Status)
	}
	record, _ := response.Payload.(map[string]any)
	if code, _ := record["code"].(float64); int(code) != 0 {
		message, _ := record["msg"].(string)
		if message == "" {
			message = "Feishu tenant_access_token failed."
		}
		return "", fmt.Errorf("%s", message)
	}
	token, _ := record["tenant_access_token"].(string)
	if token == "" {
		return "", fmt.Errorf("Feishu tenant_access_token failed.")
	}
	return token, nil
}

// resolveReceiveIDType ports resolveFeishuReceiveIdType: an "ou_"/"on_" open id
// addresses a user, anything else is a chat id.
func resolveReceiveIDType(receiveID string) string {
	if strings.HasPrefix(receiveID, "ou_") || strings.HasPrefix(receiveID, "on_") {
		return "open_id"
	}
	return "chat_id"
}

// Test ports the feishu test(): read the app display name, which proves both
// credentials and the im scope.
func (p *FeishuProvider) Test(ctx context.Context, bot Config) (TestResult, error) {
	if strings.TrimSpace(bot.FeishuAppID) == "" {
		return TestResult{OK: false, Message: "Feishu App ID is missing."}, nil
	}
	token, err := p.tenantAccessToken(ctx, bot)
	if err != nil {
		return TestResult{}, err
	}
	if token == "" {
		return TestResult{OK: false, Message: "Feishu App ID and App Secret are required."}, nil
	}
	name, err := p.appDisplayName(ctx, bot, token)
	if err != nil {
		return TestResult{}, err
	}
	if name == "" {
		name = bot.DisplayName
	}
	return TestResult{OK: true, Name: name, Message: "Feishu bot is reachable."}, nil
}

// appDisplayName ports fetchFeishuAppDisplayName.
func (p *FeishuProvider) appDisplayName(ctx context.Context, bot Config, token string) (string, error) {
	if strings.TrimSpace(bot.FeishuAppID) == "" {
		return "", nil
	}
	endpoint := fmt.Sprintf("%s/open-apis/application/v6/applications/%s?lang=zh_cn",
		p.baseURL(bot), url.PathEscape(bot.FeishuAppID))
	response, err := p.authenticatedFetchJSON(ctx, bot, token, http.MethodGet, endpoint, nil)
	if err != nil {
		return "", err
	}
	if !response.OK {
		return "", nil
	}
	record, _ := response.Payload.(map[string]any)
	if code, _ := record["code"].(float64); int(code) != 0 {
		return "", nil
	}
	data, _ := record["data"].(map[string]any)
	app, _ := data["app"].(map[string]any)
	name, _ := app["app_name"].(string)
	return name, nil
}

// ResolveName ports readFeishuAppDisplayName.
func (p *FeishuProvider) ResolveName(ctx context.Context, bot Config) (string, error) {
	token, err := p.tenantAccessToken(ctx, bot)
	if err != nil || token == "" {
		return "", err
	}
	return p.appDisplayName(ctx, bot, token)
}

func (p *FeishuProvider) authenticatedFetchJSON(ctx context.Context, bot Config, token, method, endpoint string, body any) (JSONResponse, error) {
	client := p.client()
	req, err := newAuthRequest(ctx, client, method, endpoint, token, body)
	if err != nil {
		return JSONResponse{}, err
	}
	doer := client.Doer
	if doer == nil {
		doer = http.DefaultClient
	}
	timeout := client.Timeout
	if timeout <= 0 {
		timeout = ProviderRequestTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	resp, err := doer.Do(req.WithContext(ctx))
	if err != nil {
		return JSONResponse{}, err
	}
	defer resp.Body.Close()
	return readJSONResponse(resp)
}

// Send ports the feishu send(): a plain text message, or an interactive card
// when the message carries a selection/elicitation.
func (p *FeishuProvider) Send(ctx context.Context, bot Config, message OutboundMessage) error {
	token, err := p.tenantAccessToken(ctx, bot)
	if err != nil {
		return err
	}
	if token == "" {
		return nil
	}
	if message.Selection == nil && message.Elicitation == nil {
		return p.sendPlainText(ctx, bot, token, message)
	}
	return p.sendCard(ctx, bot, token, message)
}

func (p *FeishuProvider) sendPlainText(ctx context.Context, bot Config, token string, message OutboundMessage) error {
	// Feishu text chunks at 4000 chars, below the platform limit.
	for _, chunk := range splitFeishuText(message.Text) {
		body := map[string]any{
			"receive_id": message.ProviderUserID,
			"msg_type":   "text",
			"content":    string(jsonMustMarshal(map[string]any{"text": chunk})),
		}
		endpoint := fmt.Sprintf("%s/open-apis/im/v1/messages?receive_id_type=%s",
			p.baseURL(bot), resolveReceiveIDType(message.ProviderUserID))
		if err := p.postMessage(ctx, bot, token, endpoint, body); err != nil {
			return err
		}
	}
	return nil
}

func (p *FeishuProvider) postMessage(ctx context.Context, bot Config, token, endpoint string, body any) error {
	response, err := p.authenticatedFetchJSON(ctx, bot, token, http.MethodPost, endpoint, body)
	if err != nil {
		return err
	}
	if !response.OK {
		return fmt.Errorf("Feishu message failed: HTTP %d", response.Status)
	}
	record, _ := response.Payload.(map[string]any)
	if code, _ := record["code"].(float64); int(code) != 0 {
		message, _ := record["msg"].(string)
		return fmt.Errorf("Feishu message failed: %s", message)
	}
	return nil
}

// sendCard ports sendFeishuInteractiveCard.
func (p *FeishuProvider) sendCard(ctx context.Context, bot Config, token string, message OutboundMessage) error {
	body := map[string]any{
		"receive_id": message.ProviderUserID,
		"msg_type":   "interactive",
		"content":    string(jsonMustMarshal(BuildFeishuCardPayload(message))),
	}
	endpoint := fmt.Sprintf("%s/open-apis/im/v1/messages?receive_id_type=%s",
		p.baseURL(bot), resolveReceiveIDType(message.ProviderUserID))
	return p.postMessage(ctx, bot, token, endpoint, body)
}

// SyncCommands: Feishu has no command menu to publish.
func (p *FeishuProvider) SyncCommands(context.Context, Config) error { return nil }

// SendTyping ports the Feishu "typing" reaction (a message reaction stands in
// for the typing indicator).
func (p *FeishuProvider) SendTyping(ctx context.Context, bot Config, target TypingTarget) error {
	token, err := p.tenantAccessToken(ctx, bot)
	if err != nil || token == "" || target.ProviderMessageID == "" {
		return err
	}
	endpoint := fmt.Sprintf("%s/open-apis/im/v1/messages/%s/reactions",
		p.baseURL(bot), url.PathEscape(target.ProviderMessageID))
	_, err = p.authenticatedFetchJSON(ctx, bot, token, http.MethodPost, endpoint, map[string]any{
		"reaction_type": map[string]any{"emoji_type": "Typing"},
	})
	return ignoreError(err)
}

// StopTyping removes the typing reaction again.
func (p *FeishuProvider) StopTyping(ctx context.Context, bot Config, target TypingTarget) error {
	token, err := p.tenantAccessToken(ctx, bot)
	if err != nil || token == "" || target.ProviderMessageID == "" {
		return err
	}
	endpoint := fmt.Sprintf("%s/open-apis/im/v1/messages/%s/reactions/%s",
		p.baseURL(bot), url.PathEscape(target.ProviderMessageID), url.PathEscape("Typing"))
	_, err = p.authenticatedFetchJSON(ctx, bot, token, http.MethodDelete, endpoint, nil)
	return ignoreError(err)
}

// AcknowledgeCallback: Feishu acknowledges a card click by updating the SAME
// card (patch the message in place), which the TS does through
// updateTransientInteractionCard rather than answerCallbackQuery. Returning
// handled=false keeps the common callback path from sending a second answer.
func (p *FeishuProvider) AcknowledgeCallback(context.Context, Config, map[string]any, string, *OutboundMessage) (AcknowledgeResult, error) {
	return AcknowledgeResult{}, nil
}

// PrepareCallbackPayload ports the feishu prepare: nothing to unwrap, the
// service adds nexProvider itself.
func (p *FeishuProvider) PrepareCallbackPayload(_ context.Context, _ Config, payload map[string]any) (map[string]any, error) {
	return payload, nil
}

// HandleCallbackResponse returns nil: Feishu events arrive over the WebSocket,
// so no HTTP status answer is produced.
func (p *FeishuProvider) HandleCallbackResponse(context.Context, Config, map[string]any) (*CallbackResponse, error) {
	return nil, nil
}

// DownloadAttachment ports the Feishu message-resource download.
func (p *FeishuProvider) DownloadAttachment(ctx context.Context, bot Config, attachment Attachment, actor *Actor) (*DownloadedAttachment, error) {
	token, err := p.tenantAccessToken(ctx, bot)
	if err != nil || token == "" || attachment.ProviderFileID == "" {
		return nil, err
	}
	if actor == nil || actor.ProviderMessageID == "" {
		return nil, nil
	}
	resourceType := string(attachment.Kind)
	switch attachment.Kind {
	case AttachmentImage:
		resourceType = "image"
	case AttachmentVideo:
		resourceType = "media"
	}
	endpoint := fmt.Sprintf("%s/open-apis/im/v1/messages/%s/resources/%s?type=%s",
		p.baseURL(bot), url.PathEscape(actor.ProviderMessageID),
		url.PathEscape(attachment.ProviderFileID), resourceType)
	client := p.client()
	req, err := newAuthRequest(ctx, client, http.MethodGet, endpoint, token, nil)
	if err != nil {
		return nil, err
	}
	doer := client.Doer
	if doer == nil {
		doer = http.DefaultClient
	}
	timeout := client.Timeout
	if timeout <= 0 {
		timeout = ProviderRequestTimeout
	}
	downloadCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	resp, err := doer.Do(req.WithContext(downloadCtx))
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("Feishu resource download failed: HTTP %d", resp.StatusCode)
	}
	data, err := readAllBody(resp)
	if err != nil {
		return nil, err
	}
	return &DownloadedAttachment{Attachment: attachment, Data: data}, nil
}
