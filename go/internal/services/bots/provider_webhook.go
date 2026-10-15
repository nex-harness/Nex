// provider_webhook.go ports packages/services/src/bots/providers/webhookProvider.ts.
//
// webhook is the ONE provider with an HTTP callback route (the route returns
// `Provider X does not support HTTP callbacks.` for every other provider), so it
// is also the only one whose callback path the /api/bots route exercises.
package bots

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// WebhookProvider implements ProviderAdapter for the webhook provider.
type WebhookProvider struct {
	Client     *ProviderClient
	Credential CredentialLoader
	// Now stamps the outbound `sentAt`; nil uses time.Now.
	Now func() time.Time
	// Sleep is the retry backoff used by postWebhookWithRetry; nil sleeps for
	// real. A test injects a no-op to keep the retry ladder instant.
	Sleep func(ctx context.Context, d time.Duration) error
}

func (p *WebhookProvider) now() time.Time {
	if p.Now != nil {
		return p.Now()
	}
	return time.Now()
}

func (p *WebhookProvider) sleep(ctx context.Context, d time.Duration) error {
	if p.Sleep != nil {
		return p.Sleep(ctx, d)
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// postWebhookWithRetry ports postWebhookWithRetry: the delays are 0, 500, 1500
// ms; a 4xx is a terminal answer (do not retry a client's fault) and a network
// error or 5xx retries.
func (p *WebhookProvider) postWebhookWithRetry(ctx context.Context, url string, headers map[string]string, body any) (ProviderResponse, error) {
	delays := []time.Duration{0, 500 * time.Millisecond, 1500 * time.Millisecond}
	var lastResponse ProviderResponse
	var lastErr error
	haveResponse := false
	for _, delay := range delays {
		if delay > 0 {
			if err := p.sleep(ctx, delay); err != nil {
				return ProviderResponse{}, err
			}
		}
		response, err := p.client().postWithHeaders(ctx, url, headers, body)
		if err != nil {
			lastErr = err
			continue
		}
		if response.OK || (response.Status >= 400 && response.Status < 500) {
			return response, nil
		}
		lastResponse, lastErr, haveResponse = response, nil, true
	}
	if haveResponse {
		return lastResponse, nil
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("bots: webhook request failed")
	}
	return ProviderResponse{}, lastErr
}

func (p *WebhookProvider) client() *ProviderClient {
	if p.Client != nil {
		return p.Client
	}
	return &ProviderClient{}
}

// postWithHeaders is the one place a webhook request sets its own auth header
// (the TS builds a headers object per call, not on the shared client).
func (c *ProviderClient) postWithHeaders(ctx context.Context, url string, headers map[string]string, body any) (ProviderResponse, error) {
	timeout := c.Timeout
	if timeout <= 0 {
		timeout = ProviderRequestTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	encoded, err := json.Marshal(body)
	if err != nil {
		return ProviderResponse{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, strings.NewReader(string(encoded)))
	if err != nil {
		return ProviderResponse{}, err
	}
	req.Header.Set("content-type", "application/json")
	for name, value := range headers {
		req.Header.Set(name, value)
	}
	doer := c.Doer
	if doer == nil {
		doer = http.DefaultClient
	}
	resp, err := doer.Do(req)
	if err != nil {
		return ProviderResponse{}, err
	}
	defer resp.Body.Close()
	if _, err := io.Copy(io.Discard, resp.Body); err != nil {
		return ProviderResponse{}, err
	}
	return ProviderResponse{OK: resp.StatusCode >= 200 && resp.StatusCode < 300, Status: resp.StatusCode}, nil
}

// Test ports the webhook test().
func (p *WebhookProvider) Test(ctx context.Context, bot Config) (TestResult, error) {
	if !bot.Enabled {
		return TestResult{OK: false, Message: "Webhook bot is disabled."}, nil
	}
	if bot.WebhookSecretRef == "" {
		return TestResult{
			OK:      false,
			Message: "Webhook secret is missing. Configure a secret before exposing the callback endpoint.",
		}, nil
	}
	if bot.WebhookURL == "" {
		return TestResult{OK: true, Message: "Webhook bot is enabled for inbound callbacks."}, nil
	}
	secret, err := p.Credential.Load(ctx, bot.WebhookSecretRef)
	if err != nil {
		return TestResult{}, err
	}
	headers := map[string]string{p.webhookAuthHeaderName(bot): secret}
	response, err := p.postWebhookWithRetry(ctx, bot.WebhookURL, headers, map[string]any{
		"type":     "nex.bot.test",
		"botId":    bot.ID,
		"provider": "webhook",
		"sentAt":   p.now().UnixMilli(),
	})
	if err != nil {
		return TestResult{}, err
	}
	if response.OK {
		return TestResult{OK: true, Message: "Webhook outbound endpoint is reachable."}, nil
	}
	return TestResult{
		OK:      false,
		Message: fmt.Sprintf("Webhook outbound endpoint returned HTTP %d.", response.Status),
	}, nil
}

// webhookAuthHeaderName ports `bot.webhookAuthHeaderName || "x-nex-bot-secret"`.
// The header name is part of the inbound contract: the route reads the same
// header back into the callback payload.
func (p *WebhookProvider) webhookAuthHeaderName(bot Config) string {
	if bot.WebhookAuthHeaderName != "" {
		return bot.WebhookAuthHeaderName
	}
	return DefaultWebhookAuthHeader
}

// DefaultWebhookAuthHeader is the `x-nex-bot-secret` default shared by the
// provider (outbound header) and the /api/bots route (inbound header).
const DefaultWebhookAuthHeader = "x-nex-bot-secret"

// Send ports the webhook send().
func (p *WebhookProvider) Send(ctx context.Context, bot Config, message OutboundMessage) error {
	if bot.WebhookURL == "" {
		return nil
	}
	headers := map[string]string{}
	if bot.WebhookSecretRef != "" {
		secret, err := p.Credential.Load(ctx, bot.WebhookSecretRef)
		if err != nil {
			return err
		}
		if secret != "" {
			headers[p.webhookAuthHeaderName(bot)] = secret
		}
	}
	body := map[string]any{
		"type":     "nex.bot.message",
		"botId":    bot.ID,
		"provider": "webhook",
		"userId":   message.ProviderUserID,
		"text":     BuildSelectionText(message),
		"sentAt":   p.now().UnixMilli(),
	}
	if message.Selection != nil {
		body["selection"] = message.Selection
	}
	if message.Elicitation != nil {
		// Bugfix (TS): the type is overwritten to the elicitation marker so a
		// consumer can distinguish a question from a plain reply.
		body["type"] = "nex.bot.elicitation_request"
		body["elicitation"] = message.Elicitation
	}
	response, err := p.postWebhookWithRetry(ctx, bot.WebhookURL, headers, body)
	if err != nil {
		return err
	}
	if !response.OK {
		return fmt.Errorf("Webhook outbound endpoint returned HTTP %d", response.Status)
	}
	return nil
}

// BuildSelectionText ports buildSelectionText: the numbered option list plus the
// slash command that re-selects an entry. This is what makes a webhook consumer
// actionable without knowing the action enum.
func BuildSelectionText(message OutboundMessage) string {
	if message.Selection == nil {
		return message.Text
	}
	selection := message.Selection
	lines := make([]string, 0, len(selection.Options))
	for index, option := range selection.Options {
		description := ""
		if option.Description != "" {
			description = " - " + option.Description
		}
		lines = append(lines, fmt.Sprintf("%d. %s%s\n%s", index+1, option.Label, description,
			FormatSelectionCommand(selection, option.ID)))
	}
	return message.Text + "\n" + strings.Join(lines, "\n")
}

// FormatSelectionCommand ports formatSelectionCommand.
func FormatSelectionCommand(selection *SelectionPrompt, optionID string) string {
	switch selection.Action {
	case "permission.respond":
		return optionID
	case "elicitation.respond":
		if selection.Token != "" {
			return "/elicitation " + selection.Token + " " + optionID
		}
		return "/elicitation " + optionID
	case "model.provider.set":
		return "/model provider " + optionID
	case "model.set":
		return "/model model " + optionID
	}
	return "/" + strings.TrimSuffix(selection.Action, ".set") + " " + optionID
}

// ParseCallback ports the webhook parseCallback.
func (p *WebhookProvider) ParseCallback(payload map[string]any) []InboundMessage {
	if payload == nil {
		return nil
	}
	botID, _ := payload["botId"].(string)
	text, _ := payload["text"].(string)
	userID, _ := payload["userId"].(string)
	attachments := parseWebhookAttachments(payload)
	elicitationResponse := parseWebhookElicitationResponse(payload)
	if botID == "" || (text == "" && len(attachments) == 0 && elicitationResponse == nil) || userID == "" {
		return nil
	}
	chatType := ChatPrivate
	if payload["chatType"] == "group" {
		chatType = ChatGroup
	}
	actor := Actor{
		Provider:       ProviderWebhook,
		BotID:          botID,
		ProviderUserID: userID,
		ChatType:       chatType,
	}
	if displayName, ok := payload["displayName"].(string); ok {
		actor.DisplayName = displayName
	}
	if chatID, ok := payload["chatId"].(string); ok {
		actor.ChatID = chatID
	}
	// Bugfix (TS): webhook 回调也需要携带消息 id 进入通用幂等层。
	// 否则上游重试同一条消息时，Bot 会重复创建/发送任务。
	switch messageID := payload["messageId"].(type) {
	case string:
		actor.ProviderMessageID = messageID
	case nil:
		if id, ok := payload["id"].(string); ok {
			actor.ProviderMessageID = id
		}
	}
	message := InboundMessage{BotID: botID, Text: text, Actor: actor}
	if len(attachments) > 0 {
		message.Attachments = attachments
	}
	message.ElicitationResponse = elicitationResponse
	return []InboundMessage{message}
}

// parseWebhookAttachments ports parseWebhookAttachments.
func parseWebhookAttachments(payload map[string]any) []Attachment {
	values, ok := payload["attachments"].([]any)
	if !ok {
		return nil
	}
	attachments := make([]Attachment, 0, len(values))
	for index, value := range values {
		record, ok := value.(map[string]any)
		if !ok {
			continue
		}
		kind, _ := record["kind"].(string)
		switch AttachmentKind(kind) {
		case AttachmentImage, AttachmentAudio, AttachmentVideo, AttachmentFile:
		default:
			continue
		}
		attachmentKind := AttachmentKind(kind)
		id, _ := record["id"].(string)
		if strings.TrimSpace(id) == "" {
			id = fmt.Sprintf("webhook-%d", index+1)
		}
		filename, _ := record["filename"].(string)
		if strings.TrimSpace(filename) == "" {
			filename = id + "." + string(attachmentKind)
		}
		mimeType, _ := record["mimeType"].(string)
		if strings.TrimSpace(mimeType) == "" {
			mimeType = "application/octet-stream"
		}
		attachment := Attachment{ID: id, Kind: attachmentKind, Filename: filename, MimeType: mimeType}
		if sizeBytes, ok := record["sizeBytes"].(float64); ok {
			attachment.SizeBytes = &sizeBytes
		}
		if providerFileID, ok := record["providerFileId"].(string); ok {
			attachment.ProviderFileID = providerFileID
		}
		if downloadURL, ok := record["downloadUrl"].(string); ok {
			attachment.DownloadURL = downloadURL
		}
		if dataBase64, ok := record["dataBase64"].(string); ok {
			attachment.DataBase64 = dataBase64
		}
		if localPath, ok := record["localPath"].(string); ok {
			attachment.LocalPath = localPath
		}
		attachments = append(attachments, attachment)
	}
	return attachments
}

// parseWebhookElicitationResponse ports parseWebhookElicitationResponse.
func parseWebhookElicitationResponse(payload map[string]any) *StructuredElicitationResponse {
	if payload["type"] != "nex.bot.elicitation_response" {
		return nil
	}
	requestID, _ := payload["requestId"].(string)
	if strings.TrimSpace(requestID) == "" {
		return nil
	}
	action, _ := payload["action"].(string)
	switch action {
	case "accept", "decline", "cancel":
	default:
		return nil
	}
	response := &StructuredElicitationResponse{RequestID: requestID, Action: action}
	if content, ok := payload["content"].(map[string]any); ok {
		response.Content = content
	}
	return response
}
