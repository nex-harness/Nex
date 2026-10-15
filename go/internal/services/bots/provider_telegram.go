// provider_telegram.go ports packages/services/src/bots/providers/telegramProvider.ts.
//
// Telegram has no webhook callback in Nex: the runtime long-polls getUpdates
// (telegramChannelRuntime.go). Everything here is therefore exercised from the
// polling loop, not from the /api/bots route — the route rejects telegram with
// `Provider telegram does not support HTTP callbacks.` before reaching this file.
package bots

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// TelegramBaseURL is the Telegram Bot API root. It is a variable so a test can
// point the provider at an httptest server instead of the real API; production
// leaves it at the default and no env var is introduced (D7).
var TelegramBaseURL = "https://api.telegram.org"

// AttachmentDownloadTimeout is TELEGRAM_ATTACHMENT_DOWNLOAD_TIMEOUT_MS.
const AttachmentDownloadTimeout = 30 * time.Second

// TelegramProvider implements ProviderAdapter against the Telegram Bot API.
type TelegramProvider struct {
	Client     *ProviderClient
	Credential CredentialLoader
}

func (p *TelegramProvider) client() *ProviderClient {
	if p.Client != nil {
		return p.Client
	}
	return &ProviderClient{}
}

func (p *TelegramProvider) token(ctx context.Context, bot Config) (string, error) {
	if bot.CredentialRef == "" {
		return "", nil
	}
	return p.Credential.Load(ctx, bot.CredentialRef)
}

// getMe ports getMe.
func (p *TelegramProvider) getMe(ctx context.Context, bot Config) (map[string]any, error) {
	token, err := p.token(ctx, bot)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(token) == "" {
		return nil, nil
	}
	response, err := p.client().FetchJSON(ctx, http.MethodPost, p.apiURL(token, "getMe"), nil)
	if err != nil {
		return nil, err
	}
	if !response.OK {
		return nil, nil
	}
	record, _ := response.Payload.(map[string]any)
	return record, nil
}

func (p *TelegramProvider) apiURL(token, method string) string {
	return TelegramBaseURL + "/bot" + token + "/" + method
}

// Test ports the telegram test().
func (p *TelegramProvider) Test(ctx context.Context, bot Config) (TestResult, error) {
	if bot.CredentialRef == "" {
		return TestResult{OK: false, Message: "Telegram bot token is missing."}, nil
	}
	payload, err := p.getMe(ctx, bot)
	if err != nil {
		return TestResult{}, err
	}
	if payload == nil {
		return TestResult{OK: false, Message: "Telegram getMe failed."}, nil
	}
	result, _ := payload["result"].(map[string]any)
	name := ""
	if result != nil {
		firstName, _ := result["first_name"].(string)
		username, _ := result["username"].(string)
		name = firstName
		if name == "" {
			name = username
		}
	}
	ok, _ := payload["ok"].(bool)
	description, _ := payload["description"].(string)
	if ok {
		return TestResult{OK: true, Name: name, Message: "Telegram bot is reachable."}, nil
	}
	if description == "" {
		description = "Telegram getMe failed."
	}
	return TestResult{OK: false, Name: name, Message: description}, nil
}

// ResolveName ports the telegram resolveName().
func (p *TelegramProvider) ResolveName(ctx context.Context, bot Config) (string, error) {
	payload, err := p.getMe(ctx, bot)
	if err != nil || payload == nil {
		return "", err
	}
	if ok, _ := payload["ok"].(bool); !ok {
		return "", nil
	}
	result, _ := payload["result"].(map[string]any)
	firstName, _ := result["first_name"].(string)
	username, _ := result["username"].(string)
	if firstName != "" {
		return firstName, nil
	}
	return username, nil
}

// telegramCommandNames / telegramCommandDescriptions map the internal menu keys
// to the wire command names. Only the ENGLISH menu is published (TS Bugfix: 中文
// 命令由 parser 支持，避免 Telegram 客户端菜单显示中英混杂).
var telegramCommandNames = map[string]string{
	"bind": "bind", "help": "help", "status": "status", "new": "new",
	"workspace": "project", "model": "model", "mode": "mode",
	"thoughtLevel": "think", "reply": "reply",
}

var telegramCommandDescriptions = map[string]string{
	"bind": "Bind this chat", "help": "Show help", "status": "Show current status",
	"new": "Create a new task", "workspace": "Select project", "model": "Select model",
	"mode": "Select mode", "thoughtLevel": "Select thinking level", "reply": "Select reply detail",
}

// BuildTelegramCommands ports buildTelegramCommands: BOT_MENU_COMMAND_ORDER
// filtered by the bot's policy flags. A flag that is absent counts as allowed,
// which is what `!== false` means.
func BuildTelegramCommands(bot Config) []map[string]string {
	commands := make([]map[string]string, 0, len(BotMenuCommandOrder))
	for _, key := range BotMenuCommandOrder {
		if key != "help" && key != "bind" && !IsUserCommandAllowed(bot, key) {
			continue
		}
		commands = append(commands, map[string]string{
			"command":     telegramCommandNames[key],
			"description": telegramCommandDescriptions[key],
		})
	}
	return commands
}

// SyncCommands ports the telegram syncCommands().
func (p *TelegramProvider) SyncCommands(ctx context.Context, bot Config) error {
	token, err := p.token(ctx, bot)
	if err != nil {
		return err
	}
	if strings.TrimSpace(token) == "" {
		return nil
	}
	if !bot.Enabled {
		_, err := p.client().Fetch(ctx, http.MethodPost, p.apiURL(token, "deleteMyCommands"), map[string]any{})
		return ignoreError(err)
	}
	_, err = p.client().Fetch(ctx, http.MethodPost, p.apiURL(token, "setMyCommands"),
		map[string]any{"commands": BuildTelegramCommands(bot)})
	return err
}

// splitTelegramText ports splitTelegramText: 3900-char chunks, and an empty text
// still produces one (empty) chunk.
func splitTelegramText(text string) []string {
	const limit = 3900
	if text == "" {
		return []string{""}
	}
	var chunks []string
	for index := 0; index < len(text); index += limit {
		end := index + limit
		if end > len(text) {
			end = len(text)
		}
		chunks = append(chunks, text[index:end])
	}
	return chunks
}

// Send ports the telegram send().
func (p *TelegramProvider) Send(ctx context.Context, bot Config, message OutboundMessage) error {
	token, err := p.token(ctx, bot)
	if err != nil {
		return err
	}
	if strings.TrimSpace(token) == "" {
		return nil
	}
	var replyMarkup map[string]any
	if message.Selection != nil {
		replyMarkup = buildSelectionReplyMarkup(message.Selection)
	}
	chunks := splitTelegramText(message.Text)
	for index, text := range chunks {
		// Bugfix (TS): 长 Plan 会被拆成多条消息，审批提示位于最后一条；
		// 按钮必须跟随最终决策上下文。
		attachMarkup := index == len(chunks)-1 && replyMarkup != nil
		body := map[string]any{
			"chat_id":    message.ProviderUserID,
			"text":       text,
			"parse_mode": "Markdown",
		}
		if attachMarkup {
			body["reply_markup"] = replyMarkup
		}
		response, err := p.client().Fetch(ctx, http.MethodPost, p.apiURL(token, "sendMessage"), body)
		if err != nil {
			return err
		}
		if !response.OK {
			// Bugfix (TS): Telegram Markdown 对未闭合的 `_*[]()` 很敏感；
			// 解析失败时退回纯文本重发，保证消息不会丢。
			plain := map[string]any{"chat_id": message.ProviderUserID, "text": text}
			if attachMarkup {
				plain["reply_markup"] = replyMarkup
			}
			if _, retryErr := p.client().Fetch(ctx, http.MethodPost, p.apiURL(token, "sendMessage"), plain); retryErr != nil {
				return retryErr
			}
		}
	}
	return nil
}

// SendTyping ports the telegram sendTyping().
func (p *TelegramProvider) SendTyping(ctx context.Context, bot Config, target TypingTarget) error {
	token, err := p.token(ctx, bot)
	if err != nil {
		return err
	}
	if strings.TrimSpace(token) == "" {
		return nil
	}
	_, err = p.client().Fetch(ctx, http.MethodPost, p.apiURL(token, "sendChatAction"),
		map[string]any{"chat_id": target.ProviderUserID, "action": "typing"})
	return err
}

// StopTyping is a no-op: Telegram typing expires on its own (sendChatAction lasts
// ~5s and the TS stops by not repeating it).
func (p *TelegramProvider) StopTyping(context.Context, Config, TypingTarget) error { return nil }

// truncateCallbackToast ports truncateCallbackToast (180-char toast).
func truncateCallbackToast(text string) string {
	normalized := collapseWhitespace(strings.TrimSpace(text))
	if len(normalized) > 180 {
		return normalized[:177] + "..."
	}
	return normalized
}

// AcknowledgeCallback ports the telegram acknowledgeCallback(): answerCallbackQuery
// with a toast, then strip the inline keyboard when the message ref is known.
func (p *TelegramProvider) AcknowledgeCallback(ctx context.Context, bot Config, payload map[string]any, text string, _ *OutboundMessage) (AcknowledgeResult, error) {
	token, err := p.token(ctx, bot)
	if err != nil {
		return AcknowledgeResult{}, err
	}
	callbackQueryID := readTelegramCallbackID(payload)
	if strings.TrimSpace(token) == "" || callbackQueryID == "" {
		return AcknowledgeResult{}, nil
	}
	body := map[string]any{"callback_query_id": callbackQueryID}
	if strings.TrimSpace(text) != "" {
		body["text"] = truncateCallbackToast(text)
	}
	if _, err := p.client().Fetch(ctx, http.MethodPost, p.apiURL(token, "answerCallbackQuery"), body); err != nil {
		return AcknowledgeResult{}, err
	}
	messageRef := readTelegramCallbackMessageRef(payload)
	if messageRef != nil && strings.TrimSpace(text) != "" {
		_, err := p.client().Fetch(ctx, http.MethodPost, p.apiURL(token, "editMessageReplyMarkup"), map[string]any{
			"chat_id":      messageRef.chatID,
			"message_id":   messageRef.messageID,
			"reply_markup": map[string]any{"inline_keyboard": []any{}},
		})
		return AcknowledgeResult{}, ignoreError(err)
	}
	return AcknowledgeResult{}, nil
}

// DownloadAttachment ports the telegram downloadAttachment(): getFile then the
// file endpoint, both under one 30s deadline.
func (p *TelegramProvider) DownloadAttachment(ctx context.Context, bot Config, attachment Attachment, _ *Actor) (*DownloadedAttachment, error) {
	token, err := p.token(ctx, bot)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(token) == "" || attachment.ProviderFileID == "" {
		return nil, nil
	}
	ctx, cancel := context.WithTimeout(ctx, AttachmentDownloadTimeout)
	defer cancel()
	fileResponse, err := p.client().FetchJSON(ctx, http.MethodPost, p.apiURL(token, "getFile"),
		map[string]any{"file_id": attachment.ProviderFileID})
	if err != nil {
		return nil, mapDownloadError(ctx, err)
	}
	if !fileResponse.OK {
		return nil, mapDownloadError(ctx, fmt.Errorf("Telegram getFile failed: HTTP %d", fileResponse.Status))
	}
	record, _ := fileResponse.Payload.(map[string]any)
	result, _ := record["result"].(map[string]any)
	filePath, _ := result["file_path"].(string)
	if ok, _ := record["ok"].(bool); !ok || filePath == "" {
		description, _ := record["description"].(string)
		if description == "" {
			description = "Telegram getFile did not return file_path."
		}
		return nil, mapDownloadError(ctx, fmt.Errorf("%s", description))
	}
	data, err := p.client().FetchRaw(ctx, TelegramBaseURL+"/file/bot"+token+"/"+filePath)
	if err != nil {
		return nil, mapDownloadError(ctx, err)
	}
	downloaded := attachment
	if fileSize, ok := result["file_size"].(float64); ok {
		downloaded.SizeBytes = &fileSize
	}
	return &DownloadedAttachment{Attachment: downloaded, Data: data}, nil
}

// mapDownloadError ports the AbortError branch: a hung Telegram file endpoint
// must fail fast rather than block the bot callback indefinitely.
func mapDownloadError(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		return fmt.Errorf("Telegram file download timed out.")
	}
	return err
}

// PrepareCallbackPayload is a passthrough for telegram (no envelope to unwrap).
func (p *TelegramProvider) PrepareCallbackPayload(_ context.Context, _ Config, payload map[string]any) (map[string]any, error) {
	return payload, nil
}

// HandleCallbackResponse returns nil: telegram has no synchronous HTTP answer,
// its ack is answerCallbackQuery.
func (p *TelegramProvider) HandleCallbackResponse(context.Context, Config, map[string]any) (*CallbackResponse, error) {
	return nil, nil
}

// CreateStreamingReplyCard / UpdateStreamingReplyCard are unsupported: Telegram
// edits the message text instead of maintaining a card (the TS has no card path
// for telegram either).
func (p *TelegramProvider) CreateStreamingReplyCard(context.Context, Config, StreamingCardState) (*CardHandle, error) {
	return nil, nil
}

func (p *TelegramProvider) UpdateStreamingReplyCard(context.Context, Config, CardHandle, StreamingCardState) error {
	return nil
}
