package bots

import (
	"strings"
)

// provider_feishu_parse.go holds the inbound half of the Feishu provider: the
// event-shape readers (a text/post message, a card action), the Card JSON 2.0
// update-token resolver, and the helpers the send side also uses.
//
// The two Bugfix comments carried here are load-bearing, so they are preserved
// verbatim: the node-sdk EventDispatcher may flatten `event` onto the top level,
// and a private-chat card button can carry open_chat_id — deriving "group" from
// the presence of a chat id rejected private buttons as unsupported.
// ParseCallback ports the feishu parseCallback: a text message event or a card
// action. The service injects nexProvider so a lark payload keeps its provider.
func (p *FeishuProvider) ParseCallback(payload map[string]any) []InboundMessage {
	if payload == nil {
		return nil
	}
	provider := ProviderFeishu
	if readFeishuString(payload, "nexProvider") == "lark" {
		provider = ProviderLark
	}
	if message := readFeishuTextMessage(payload, provider); message != nil {
		return []InboundMessage{*message}
	}
	if message := readFeishuCardAction(payload, provider); message != nil {
		return []InboundMessage{*message}
	}
	return nil
}

// readFeishuTextMessage ports readFeishuTextMessage.
func readFeishuTextMessage(payload map[string]any, provider Provider) *InboundMessage {
	// Bugfix (TS): the node-sdk EventDispatcher may flatten `event` onto the top
	// level depending on the event/version; reading only payload.event.message
	// made a genuinely received message parse to zero inbound messages.
	event, ok := payload["event"].(map[string]any)
	if !ok {
		event = payload
	}
	message, _ := event["message"].(map[string]any)
	if message == nil {
		return nil
	}
	sender, _ := event["sender"].(map[string]any)
	senderID, _ := sender["sender_id"].(map[string]any)
	content := parseJSONRecord(message["content"])
	msgType := firstNonEmptyString(readFeishuString(message, "message_type"), readFeishuString(message, "msg_type"))
	attachment := readFeishuAttachment(msgType, content)
	// Bugfix (TS): rich-text posts arrive as message_type=post; reading only
	// content.text made a normal-looking message parse to empty text and drop.
	rawText := firstNonEmptyString(
		readFeishuString(content, "text"),
		readFeishuPostText(content),
		readFeishuString(event, "text_without_at_bot"),
		readFeishuString(event, "text"),
	)
	text := stripFeishuMentions(rawText)
	userID := firstNonEmptyString(
		readFeishuString(senderID, "open_id"),
		readFeishuString(senderID, "user_id"),
		readFeishuString(senderID, "union_id"),
		readFeishuString(event, "open_id"),
		readFeishuString(event, "user_id"),
		readFeishuString(event, "union_id"),
	)
	chatID := firstNonEmptyString(readFeishuString(message, "chat_id"), readFeishuString(event, "open_chat_id"))
	messageID := readFeishuString(message, "message_id")
	if (text == "" && attachment == nil) || userID == "" {
		return nil
	}
	chatType := readFeishuChatType(firstNonEmptyString(
		readFeishuString(message, "chat_type"), readFeishuString(event, "chat_type")))
	actor := Actor{
		Provider:       provider,
		BotID:          readFeishuString(payload, "botId"),
		ProviderUserID: userID,
		ChatType:       chatType,
	}
	if actor.BotID == "" {
		actor.BotID = readFeishuString(payload, "bot_id")
	}
	// chatId is only carried for a group chat (TS): a private chat's chat id is
	// not a routable target for replies.
	if chatType == ChatGroup && chatID != "" {
		actor.ChatID = chatID
	}
	if messageID != "" {
		actor.ProviderMessageID = messageID
	}
	inbound := &InboundMessage{BotID: actor.BotID, Text: text, Actor: actor}
	if attachment != nil {
		inbound.Attachments = []Attachment{*attachment}
	}
	return inbound
}

// FeishuElicitationFormValuePrefix is FEISHU_ELICITATION_FORM_VALUE_PREFIX.
const FeishuElicitationFormValuePrefix = "__form__:"

// readFeishuCardAction ports readFeishuCardAction.
func readFeishuCardAction(payload map[string]any, provider Provider) *InboundMessage {
	event, ok := payload["event"].(map[string]any)
	if !ok {
		event = payload
	}
	action, _ := event["action"].(map[string]any)
	value, _ := action["value"].(map[string]any)
	formValue, _ := action["form_value"].(map[string]any)
	var behavior map[string]any
	if behaviors, ok := action["behaviors"].([]any); ok {
		for _, candidate := range behaviors {
			if record, ok := candidate.(map[string]any); ok {
				behavior = record
				break
			}
		}
	}
	behaviorValue, _ := behavior["value"].(map[string]any)
	rawCommand := firstNonEmptyString(
		readFeishuString(value, "command"),
		readFeishuString(value, "text"),
		readFeishuString(behaviorValue, "command"),
		readFeishuString(behaviorValue, "text"),
	)
	command := rawCommand
	if rawCommand != "" && strings.Contains(rawCommand, FeishuElicitationFormValuePrefix) {
		command = buildFeishuElicitationFormCommand(firstFieldOf(rawCommand), readFeishuString(formValue, "answer"))
	}
	operator, _ := event["operator"].(map[string]any)
	operatorID, _ := operator["operator_id"].(map[string]any)
	openID := firstNonEmptyString(
		readFeishuString(operatorID, "open_id"),
		readFeishuString(operator, "open_id"),
		readFeishuString(event, "open_id"),
		readFeishuString(event, "user_id"),
		readFeishuString(payload, "open_id"),
		readFeishuString(payload, "user_id"),
	)
	context, _ := event["context"].(map[string]any)
	contextChatType := firstNonEmptyString(
		readFeishuString(context, "chat_type"),
		readFeishuString(event, "chat_type"),
		readFeishuString(payload, "chat_type"),
	)
	chatID := firstNonEmptyString(readFeishuString(context, "open_chat_id"), readFeishuString(context, "chat_id"))
	message, _ := event["message"].(map[string]any)
	header, _ := payload["header"].(map[string]any)
	providerMessageID := firstNonEmptyString(
		readFeishuString(event, "event_id"),
		readFeishuString(header, "event_id"),
		readFeishuString(payload, "uuid"),
		readFeishuString(payload, "event_id"),
		readFeishuString(context, "open_message_id"),
		readFeishuString(context, "message_id"),
		readFeishuString(message, "message_id"),
		readFeishuString(action, "value_id"),
	)
	if command == "" || openID == "" {
		return nil
	}
	// Bugfix (TS): context.open_chat_id may also exist for a private-chat button,
	// so deriving "group" from the presence of a chat id rejected private card
	// buttons as unsupported.
	chatType := readFeishuChatType(contextChatType)
	botID := readFeishuString(payload, "botId")
	actor := Actor{Provider: provider, BotID: botID, ProviderUserID: openID, ChatType: chatType}
	if chatType == ChatGroup && chatID != "" {
		actor.ChatID = chatID
	}
	if providerMessageID != "" {
		actor.ProviderMessageID = providerMessageID
	}
	return &InboundMessage{BotID: botID, Text: command, Actor: actor}
}

// ReadFeishuCardUpdateToken ports readFeishuCardUpdateToken.
//
// Bugfix (TS, kept): Card JSON 2.0 回调的更新 token 不总在旧版 action.token 上。
// 新版按钮由 behaviors.callback 触发时，飞书/Lark 可能把 token 放在事件或 context 上；
// 漏读会让 card/update 静默跳过，按钮留在原卡片里。 So all THREE key spellings are
// read from all FOUR records, in the TS's precedence order.
func ReadFeishuCardUpdateToken(payload map[string]any) string {
	if payload == nil {
		return ""
	}
	event, ok := payload["event"].(map[string]any)
	if !ok {
		event = payload
	}
	action, _ := event["action"].(map[string]any)
	context, _ := event["context"].(map[string]any)
	return firstNonEmptyString(
		firstNonEmptyString(
			readFeishuString(event, "token"),
			readFeishuString(event, "card_update_token"),
			readFeishuString(event, "update_token"),
		),
		firstNonEmptyString(
			readFeishuString(payload, "token"),
			readFeishuString(payload, "card_update_token"),
			readFeishuString(payload, "update_token"),
		),
		firstNonEmptyString(
			readFeishuString(action, "token"),
			readFeishuString(action, "card_update_token"),
			readFeishuString(action, "update_token"),
		),
		firstNonEmptyString(
			readFeishuString(context, "token"),
			readFeishuString(context, "card_update_token"),
			readFeishuString(context, "update_token"),
		),
	)
}

// buildFeishuElicitationFormCommand ports buildFeishuElicitationFormCommand: the
// free-text answer field is re-encoded as the `__form__:` value the parser reads.
func buildFeishuElicitationFormCommand(token, answer string) string {
	command := "/elicitation"
	if token != "" {
		command += " " + token
	}
	return command + " " + FeishuElicitationFormValuePrefix + answer
}

// firstFieldOf ports `rawCommand.split(/\s+/u)[1] ?? ""`.
func firstFieldOf(command string) string {
	fields := strings.Fields(command)
	if len(fields) >= 2 {
		return fields[1]
	}
	return ""
}
