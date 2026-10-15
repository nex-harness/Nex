package bots

import (
	"strconv"
	"strings"
	"time"
)

// provider_weixin_parse.go holds the inbound half of the WeChat iLink provider:
// reading a getUpdates response into inbound messages, and the attachment readers
// (item_list media, explicit attachments, and the AES key the CDN returns with).
// ParseCallback ports the weixin parseCallback: it reads the `msgs` list of a
// getUpdates RESPONSE, which is how the runtime feeds it.
func (p *WeixinProvider) ParseCallback(payload map[string]any) []InboundMessage {
	if payload == nil {
		return nil
	}
	botID := readWeixinString(payload, "botId")
	if botID == "" {
		return nil
	}
	messages := readWeixinMessages(payload)
	inbound := make([]InboundMessage, 0, len(messages))
	for _, rawMessage := range messages {
		if message := buildWeixinInboundMessage(botID, rawMessage); message != nil {
			inbound = append(inbound, *message)
		}
	}
	if len(inbound) == 0 {
		return nil
	}
	return inbound
}

// readWeixinMessages ports readWeixinMessages: the container may be wrapped in
// `data`, and the list may be named msgs/messages/updates/items/list.
func readWeixinMessages(payload map[string]any) []map[string]any {
	container := readMessagesContainer(payload)
	for _, key := range []string{"msgs", "messages", "updates", "items", "list"} {
		switch raw := container[key].(type) {
		case []any:
			result := make([]map[string]any, 0, len(raw))
			for _, item := range raw {
				if record, ok := item.(map[string]any); ok {
					result = append(result, record)
				}
			}
			return result
		case map[string]any:
			return []map[string]any{raw}
		}
	}
	return nil
}

// readMessagesContainer ports unwrapData + readMessagesContainer.
func readMessagesContainer(payload map[string]any) map[string]any {
	data, ok := payload["data"]
	if !ok {
		return payload
	}
	record, ok := data.(map[string]any)
	if !ok {
		return map[string]any{}
	}
	return record
}

// ReadWeixinNextBuf ports readNextBuf: the polling cursor the runtime persists.
// It must only advance after the batch has been processed (the same rule as the
// Telegram offset).
func ReadWeixinNextBuf(payload map[string]any) string {
	container := readMessagesContainer(payload)
	return firstNonEmptyString(
		readWeixinString(container, "get_updates_buf"),
		readWeixinString(container, "buf"),
		readWeixinString(container, "next_buf"),
		readWeixinString(container, "nextBuf"),
		readWeixinString(container, "getUpdatesBuf"),
		readWeixinString(container, "syncKey"),
	)
}

// buildWeixinInboundMessage ports buildInboundMessage.
func buildWeixinInboundMessage(botID string, rawMessage map[string]any) *InboundMessage {
	if messageType, ok := readNumber(rawMessage, "message_type"); ok && messageType == weixinMessageTypeBot {
		// message_type=2 is the BOT's own outbound echo; re-processing it would
		// loop the bot's own message back into the task.
		return nil
	}
	text := strings.TrimSpace(readWeixinText(rawMessage))
	attachments := readWeixinAttachments(rawMessage)
	userID := strings.TrimSpace(readWeixinUserID(rawMessage))
	if (text == "" && len(attachments) == 0) || userID == "" {
		return nil
	}
	chatID := readWeixinChatID(rawMessage)
	chatType := ChatPrivate
	if chatID != "" {
		chatType = ChatGroup
	}
	return &InboundMessage{
		BotID:       botID,
		Text:        text,
		Attachments: attachments,
		Actor: Actor{
			Provider:             ProviderWeixin,
			BotID:                botID,
			ProviderUserID:       userID,
			DisplayName:          readWeixinDisplayName(rawMessage),
			ChatType:             chatType,
			ChatID:               chatID,
			ProviderMessageID:    readWeixinMessageID(rawMessage),
			ProviderContextToken: readWeixinContextToken(rawMessage),
		},
	}
}

// readWeixinInner ports the repeated `msg ?? message` inner record.
func readWeixinInner(message map[string]any) map[string]any {
	if inner, ok := message["msg"].(map[string]any); ok {
		return inner
	}
	if inner, ok := message["message"].(map[string]any); ok {
		return inner
	}
	return nil
}

// readWeixinItemList ports the repeated item_list resolution.
func readWeixinItemList(message map[string]any) []any {
	if items, ok := message["item_list"].([]any); ok {
		return items
	}
	if inner := readWeixinInner(message); inner != nil {
		if items, ok := inner["item_list"].([]any); ok {
			return items
		}
	}
	return nil
}

// readWeixinTextItem ports readWeixinTextItem.
func readWeixinTextItem(item any) string {
	record, ok := item.(map[string]any)
	if !ok {
		return ""
	}
	textItem, _ := record["text_item"].(map[string]any)
	return firstNonEmptyString(
		readWeixinString(textItem, "text"),
		readWeixinString(record, "text"),
		readWeixinString(record, "content"),
	)
}

// readWeixinText ports readWeixinText.
func readWeixinText(message map[string]any) string {
	if text := firstNonEmptyString(
		readWeixinString(message, "text"),
		readWeixinString(message, "content"),
		readWeixinString(message, "message"),
	); text != "" {
		return text
	}
	var parts []string
	for _, item := range readWeixinItemList(message) {
		if text := readWeixinTextItem(item); text != "" {
			parts = append(parts, text)
		}
	}
	if len(parts) > 0 {
		return strings.Join(parts, "\n")
	}
	inner := readWeixinInner(message)
	return firstNonEmptyString(readWeixinString(inner, "text"), readWeixinString(inner, "content"))
}

// readWeixinUserID ports readWeixinUserId: a long ordered list of aliases.
func readWeixinUserID(message map[string]any) string {
	from, _ := message["from"].(map[string]any)
	sender, _ := message["sender"].(map[string]any)
	return firstNonEmptyString(
		readWeixinString(message, "from_user_id"),
		readWeixinString(message, "from"),
		readWeixinString(message, "from_user"),
		readWeixinString(message, "fromUser"),
		readWeixinString(message, "user"),
		readWeixinString(message, "user_id"),
		readWeixinString(message, "userId"),
		readWeixinString(from, "id"),
		readWeixinString(from, "wxid"),
		readWeixinString(sender, "id"),
		readWeixinString(sender, "wxid"),
	)
}

// readWeixinChatID ports readWeixinChatId: its presence is what makes a message a
// group message.
func readWeixinChatID(message map[string]any) string {
	return firstNonEmptyString(
		readWeixinString(message, "room"),
		readWeixinString(message, "room_id"),
		readWeixinString(message, "roomId"),
		readWeixinString(message, "chat"),
		readWeixinString(message, "chat_id"),
		readWeixinString(message, "chatId"),
	)
}

// readWeixinDisplayName ports readWeixinDisplayName.
func readWeixinDisplayName(message map[string]any) string {
	from, _ := message["from"].(map[string]any)
	sender, _ := message["sender"].(map[string]any)
	inner := readWeixinInner(message)
	return firstNonEmptyString(
		readWeixinString(message, "name"),
		readWeixinString(message, "displayName"),
		readWeixinString(message, "nickname"),
		readWeixinString(from, "name"),
		readWeixinString(from, "nickname"),
		readWeixinString(sender, "name"),
		readWeixinString(sender, "nickname"),
		readWeixinString(inner, "sender_name"),
	)
}

// readWeixinMessageID ports readWeixinMessageId.
func readWeixinMessageID(message map[string]any) string {
	inner := readWeixinInner(message)
	if messageID := firstNonEmptyString(
		readWeixinString(message, "id"),
		readWeixinString(message, "msgid"),
		readWeixinString(message, "msgId"),
		readNumberOrString(message, "message_id"),
		readWeixinString(inner, "id"),
		readWeixinString(inner, "msgid"),
		readWeixinString(inner, "msgId"),
		readNumberOrString(inner, "message_id"),
	); messageID != "" {
		return messageID
	}
	for _, record := range []map[string]any{message, inner} {
		for _, key := range []string{"id", "msgid", "msgId"} {
			if value, ok := readNumber(record, key); ok {
				return strconv.Itoa(value)
			}
		}
	}
	return ""
}

// readWeixinContextToken ports readWeixinContextToken.
func readWeixinContextToken(message map[string]any) string {
	inner := readWeixinInner(message)
	return firstNonEmptyString(
		readWeixinString(message, "context_token"),
		readWeixinString(message, "contextToken"),
		readWeixinString(message, "context"),
		readWeixinString(inner, "context_token"),
	)
}

// WeixinGetUpdatesTimeout exposes the long-poll timeout (90s, above the default
// client timeout, so the polling runtime overrides it per request).
func WeixinGetUpdatesTimeout() time.Duration { return weixinGetUpdatesTimeout }
