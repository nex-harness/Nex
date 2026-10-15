package bots

import (
	"strconv"
	"strings"
)

// provider_telegram_parse.go holds the inbound half of the Telegram provider:
// turning an update (a private/group message, an inline-keyboard callback, or a
// message carrying photos/documents/video/audio/voice) into an InboundMessage,
// plus the callback_data codec the buttons round-trip through.
// ParseCallback ports the telegram parseCallback().
func (p *TelegramProvider) ParseCallback(payload map[string]any) []InboundMessage {
	if payload == nil {
		return nil
	}
	botID, _ := payload["botId"].(string)
	if botID == "" {
		return nil
	}
	update := payload
	if nested, ok := payload["update"].(map[string]any); ok {
		update = nested
	}
	message := readTelegramPrivateMessage(botID, update)
	if message == nil {
		message = readTelegramCallbackMessage(botID, update)
	}
	if message == nil {
		return nil
	}
	return []InboundMessage{*message}
}

// readTelegramPrivateMessage ports readTelegramPrivateMessage.
func readTelegramPrivateMessage(botID string, update map[string]any) *InboundMessage {
	message, ok := update["message"].(map[string]any)
	if !ok {
		return nil
	}
	chat, _ := message["chat"].(map[string]any)
	from, _ := message["from"].(map[string]any)
	text, _ := message["text"].(string)
	if text == "" {
		text, _ = message["caption"].(string)
	}
	userID := telegramIDString(from, "id")
	chatType := ChatGroup
	if chatTypeValue, _ := chat["type"].(string); chatTypeValue == "private" {
		chatType = ChatPrivate
	}
	attachments := readTelegramAttachments(message)
	if (text == "" && len(attachments) == 0) || userID == "" {
		return nil
	}
	inbound := &InboundMessage{
		BotID:       botID,
		Text:        text,
		Attachments: attachments,
		Actor: Actor{
			Provider:       ProviderTelegram,
			BotID:          botID,
			ProviderUserID: userID,
			ChatType:       chatType,
		},
	}
	if from != nil {
		if username, ok := from["username"].(string); ok && username != "" {
			inbound.Actor.DisplayName = username
		} else if firstName, ok := from["first_name"].(string); ok && firstName != "" {
			inbound.Actor.DisplayName = firstName
		}
	}
	if chatID := telegramIDString(chat, "id"); chatID != "" {
		inbound.Actor.ChatID = chatID
	}
	if messageID := telegramIDString(message, "message_id"); messageID != "" {
		inbound.Actor.ProviderMessageID = messageID
	}
	return inbound
}

// telegramIDString ports `typeof x?.id === "number" || "string" ? String(...)`.
func telegramIDString(record map[string]any, key string) string {
	if record == nil {
		return ""
	}
	switch value := record[key].(type) {
	case float64:
		return jsonNumberString(value)
	case string:
		return value
	}
	return ""
}

// readTelegramCallbackMessage ports readTelegramCallbackMessage.
func readTelegramCallbackMessage(botID string, update map[string]any) *InboundMessage {
	callbackQuery, ok := update["callback_query"].(map[string]any)
	if !ok {
		return nil
	}
	message, _ := callbackQuery["message"].(map[string]any)
	chat, _ := message["chat"].(map[string]any)
	from, _ := callbackQuery["from"].(map[string]any)
	data, _ := callbackQuery["data"].(string)
	userID := telegramIDString(from, "id")
	if data == "" || userID == "" {
		return nil
	}
	chatType := ChatGroup
	if chatTypeValue, _ := chat["type"].(string); chatTypeValue == "private" {
		chatType = ChatPrivate
	}
	inbound := &InboundMessage{
		BotID: botID,
		Text:  DecodeTelegramCallbackData(data),
		Actor: Actor{
			Provider:       ProviderTelegram,
			BotID:          botID,
			ProviderUserID: userID,
			ChatType:       chatType,
		},
	}
	if from != nil {
		if username, ok := from["username"].(string); ok && username != "" {
			inbound.Actor.DisplayName = username
		} else if firstName, ok := from["first_name"].(string); ok && firstName != "" {
			inbound.Actor.DisplayName = firstName
		}
	}
	if chatID := telegramIDString(chat, "id"); chatID != "" {
		inbound.Actor.ChatID = chatID
	}
	if callbackID, ok := callbackQuery["id"].(string); ok && callbackID != "" {
		inbound.Actor.ProviderMessageID = callbackID
	} else if messageID := telegramIDString(message, "message_id"); messageID != "" {
		inbound.Actor.ProviderMessageID = messageID
	}
	return inbound
}

// readTelegramCallbackID ports readTelegramCallbackId.
func readTelegramCallbackID(payload map[string]any) string {
	if payload == nil {
		return ""
	}
	update := payload
	if nested, ok := payload["update"].(map[string]any); ok {
		update = nested
	}
	callbackQuery, ok := update["callback_query"].(map[string]any)
	if !ok {
		return ""
	}
	id, _ := callbackQuery["id"].(string)
	return id
}

// TelegramMessageRef is the chat/message pair of a callback query.
type TelegramMessageRef struct {
	chatID    string
	messageID int
}

// readTelegramCallbackMessageRef ports readTelegramCallbackMessageRef. Only a
// numeric message_id qualifies (the TS checks `typeof === "number"`).
func readTelegramCallbackMessageRef(payload map[string]any) *TelegramMessageRef {
	if payload == nil {
		return nil
	}
	update := payload
	if nested, ok := payload["update"].(map[string]any); ok {
		update = nested
	}
	callbackQuery, ok := update["callback_query"].(map[string]any)
	if !ok {
		return nil
	}
	message, _ := callbackQuery["message"].(map[string]any)
	chat, _ := message["chat"].(map[string]any)
	chatID := telegramIDString(chat, "id")
	messageID, ok := message["message_id"].(float64)
	if chatID == "" || !ok {
		return nil
	}
	return &TelegramMessageRef{chatID: chatID, messageID: int(messageID)}
}

// buildSelectionCallbackData ports buildSelectionCallbackData. Only the list
// INDEX travels (Bugfix: callback_data 最多 64 字节，长 path/identity 会被拒收).
func buildSelectionCallbackData(selection *SelectionPrompt, optionID string, index int) string {
	switch selection.Action {
	case "permission.respond":
		return "zc:permission:" + strconv.Itoa(index+1)
	case "elicitation.respond":
		if selection.Token != "" {
			return "zc:e:" + selection.Token + ":" + strconv.Itoa(index+1)
		}
		return "zc:elicitation:" + strconv.Itoa(index+1)
	case "model.provider.set":
		return "zc:cmd:/model provider " + strconv.Itoa(index+1)
	case "model.set":
		return "zc:cmd:/model model " + strconv.Itoa(index+1)
	}
	return "zc:" + strings.TrimSuffix(selection.Action, ".set") + ":" + strconv.Itoa(index+1)
}

// buildSelectionReplyMarkup ports buildSelectionReplyMarkup.
func buildSelectionReplyMarkup(selection *SelectionPrompt) map[string]any {
	rows := make([]any, 0, len(selection.Options)+1)
	for index, option := range selection.Options {
		rows = append(rows, []any{map[string]any{
			"text":          option.Label,
			"callback_data": buildSelectionCallbackData(selection, option.ID, index),
		}})
	}
	// Bugfix (TS): Telegram 原生按钮以前没有取消入口，用户只能手敲 0 才能退出。
	if selection.ShowCancel == nil || *selection.ShowCancel {
		cancelLabel := selection.CancelLabel
		if cancelLabel == "" {
			cancelLabel = "Cancel"
		}
		rows = append(rows, []any{map[string]any{
			"text":          cancelLabel,
			"callback_data": "zc:cancel",
		}})
	}
	return map[string]any{"inline_keyboard": rows}
}

// BuildSelectionReplyMarkup exposes buildSelectionReplyMarkup for the markup
// golden tests.
func BuildSelectionReplyMarkup(selection *SelectionPrompt) map[string]any {
	return buildSelectionReplyMarkup(selection)
}

// DecodeTelegramCallbackData ports decodeTelegramCallbackData.
func DecodeTelegramCallbackData(data string) string {
	switch {
	case data == "zc:cancel":
		return "/cancel"
	case strings.HasPrefix(data, "zc:e:"):
		parts := strings.Split(data, ":")
		if len(parts) >= 4 && parts[2] != "" && parts[3] != "" {
			return "/elicitation " + parts[2] + " " + parts[3]
		}
		return "/elicitation"
	case strings.HasPrefix(data, "zc:cmd:"):
		return strings.TrimPrefix(data, "zc:cmd:")
	case strings.HasPrefix(data, "zc:"):
		return "/" + strings.Replace(strings.TrimPrefix(data, "zc:"), ":", " ", 1)
	}
	return data
}

// defaultMimeTypeForAttachmentKind ports defaultMimeTypeForAttachmentKind.
func defaultMimeTypeForAttachmentKind(kind AttachmentKind) string {
	switch kind {
	case AttachmentImage:
		return "image/jpeg"
	case AttachmentAudio:
		return "audio/mpeg"
	case AttachmentVideo:
		return "video/mp4"
	}
	return "application/octet-stream"
}

// readTelegramFileAttachment ports readTelegramFileAttachment.
func readTelegramFileAttachment(value any, kind AttachmentKind, fallbackName string) *Attachment {
	record, ok := value.(map[string]any)
	if !ok {
		return nil
	}
	providerFileID, _ := record["file_id"].(string)
	if providerFileID == "" {
		return nil
	}
	filename, _ := record["file_name"].(string)
	if strings.TrimSpace(filename) == "" {
		filename = fallbackName
	}
	mimeType, _ := record["mime_type"].(string)
	if strings.TrimSpace(mimeType) == "" {
		mimeType = defaultMimeTypeForAttachmentKind(kind)
	}
	attachment := &Attachment{
		ID:             providerFileID,
		Kind:           kind,
		Filename:       filename,
		MimeType:       mimeType,
		ProviderFileID: providerFileID,
	}
	if fileSize, ok := record["file_size"].(float64); ok {
		attachment.SizeBytes = &fileSize
	}
	return attachment
}

// readTelegramPhotoAttachment ports readTelegramPhotoAttachment: the LAST photo
// size is the largest, which is what Telegram orders them by.
func readTelegramPhotoAttachment(message map[string]any) *Attachment {
	photos, ok := message["photo"].([]any)
	if !ok || len(photos) == 0 {
		return nil
	}
	last := photos[len(photos)-1]
	if _, ok := last.(map[string]any); !ok {
		return nil
	}
	return readTelegramFileAttachment(last, AttachmentImage, "telegram-photo.jpg")
}

// readTelegramAttachments ports readTelegramAttachments: the fixed five slots,
// in order, with nils filtered.
func readTelegramAttachments(message map[string]any) []Attachment {
	candidates := []*Attachment{
		readTelegramPhotoAttachment(message),
		readTelegramFileAttachment(message["document"], AttachmentFile, "telegram-document"),
		readTelegramFileAttachment(message["video"], AttachmentVideo, "telegram-video.mp4"),
		readTelegramFileAttachment(message["audio"], AttachmentAudio, "telegram-audio"),
		readTelegramFileAttachment(message["voice"], AttachmentAudio, "telegram-voice.ogg"),
	}
	attachments := make([]Attachment, 0, len(candidates))
	for _, candidate := range candidates {
		if candidate != nil {
			attachments = append(attachments, *candidate)
		}
	}
	if len(attachments) == 0 {
		return nil
	}
	return attachments
}

// ignoreError ports `.catch(() => undefined)`: a best-effort Telegram call whose
// failure must not fail the operation that triggered it.
func ignoreError(err error) error { return nil }
