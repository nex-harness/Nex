package bots

import (
	"strings"
)

// provider_feishu_content.go holds the content readers of the Feishu provider:
// the mention stripper, the rich-text (post) reader, the attachment reader and
// the 4000-char text splitter.
//
// The mention stripper is two passes on purpose — first `<at ...>…</at>` tags,
// then bare `@name` runs — because Feishu emits both and removing the tag alone
// leaves the visible handle in the text the model sees.
func readFeishuChatType(value string) ChatType {
	if value == "group" || value == "group_chat" {
		return ChatGroup
	}
	return ChatPrivate
}

// stripFeishuMentions ports stripFeishuMentions: `<at ...>...</at>` then bare
// `@name`.
func stripFeishuMentions(text string) string {
	text = removeFeishuAtTags(text)
	return strings.TrimSpace(stripAtMentions(text))
}

// removeFeishuAtTags removes `<at ...>...</at>` and self-closing `<at ... />`.
func removeFeishuAtTags(text string) string {
	var builder strings.Builder
	rest := text
	for {
		start := strings.Index(rest, "<at")
		if start < 0 {
			builder.WriteString(rest)
			return builder.String()
		}
		builder.WriteString(rest[:start])
		afterTag := rest[start:]
		closing := strings.Index(afterTag, "</at>")
		if closing >= 0 {
			rest = afterTag[closing+len("</at>"):]
			continue
		}
		// Self-closing form: `<at id="x"/>` or `<at id="x">`.
		end := strings.Index(afterTag, ">")
		if end < 0 {
			return builder.String()
		}
		rest = afterTag[end+1:]
	}
}

// stripAtMentions removes `@word` runs, mirroring the `@\S+` global replace.
func stripAtMentions(text string) string {
	var builder strings.Builder
	runes := []rune(text)
	for i := 0; i < len(runes); i++ {
		if runes[i] != '@' {
			builder.WriteRune(runes[i])
			continue
		}
		// Consume the non-space run after '@' exactly like `\S+`.
		j := i + 1
		for j < len(runes) && !unicodeIsSpaceRune(runes[j]) {
			j++
		}
		i = j - 1
	}
	return builder.String()
}

func readFeishuString(record map[string]any, key string) string {
	if record == nil {
		return ""
	}
	value, _ := record[key].(string)
	return value
}

// parseJSONRecord ports parseJsonRecord: an object, or a JSON string holding one.
func parseJSONRecord(value any) map[string]any {
	if record, ok := value.(map[string]any); ok {
		return record
	}
	text, ok := value.(string)
	if !ok {
		return nil
	}
	decoded, ok := DecodeJSONValue([]byte(text))
	if !ok {
		return nil
	}
	record, _ := decoded.(map[string]any)
	return record
}

// readFeishuPostText ports readFeishuPostLocaleContent + readFeishuPostText.
func readFeishuPostText(content map[string]any) string {
	value := readFeishuPostLocaleContent(content)
	items, ok := value.([]any)
	if !ok {
		return ""
	}
	tokens := make([]string, 0, len(items))
	for _, item := range items {
		record, ok := item.(map[string]any)
		if !ok {
			continue
		}
		if tag, _ := record["tag"].(string); tag != "text" {
			continue
		}
		text, _ := record["text"].(string)
		tokens = append(tokens, text)
	}
	return strings.TrimSpace(strings.Join(tokens, ""))
}

// readFeishuPostLocaleContent ports readFeishuPostLocaleContent.
func readFeishuPostLocaleContent(content map[string]any) any {
	if content == nil {
		return nil
	}
	if value, ok := content["content"]; ok {
		return value
	}
	post, _ := content["post"].(map[string]any)
	for _, key := range []string{"zh_cn", "en_us"} {
		if locale, ok := content[key].(map[string]any); ok {
			return locale["content"]
		}
		if locale, ok := post[key].(map[string]any); ok {
			return locale["content"]
		}
	}
	return nil
}

// readFeishuAttachment ports readFeishuAttachment.
func readFeishuAttachment(msgType string, content map[string]any) *Attachment {
	kind := inferFeishuAttachmentKind(msgType)
	if kind == "" {
		return nil
	}
	rawKey := string(kind)
	if key, ok := content[rawKey].(map[string]any); ok {
		if fileKey, ok := key["file_key"].(string); ok && fileKey != "" {
			return &Attachment{
				ID:             fileKey,
				Kind:           kind,
				Filename:       fileKey,
				MimeType:       defaultFeishuMimeType(kind),
				ProviderFileID: fileKey,
			}
		}
	}
	return nil
}

// inferFeishuAttachmentKind ports inferFeishuAttachmentKind.
func inferFeishuAttachmentKind(msgType string) AttachmentKind {
	switch msgType {
	case "image":
		return AttachmentImage
	case "audio":
		return AttachmentAudio
	case "video":
		return AttachmentVideo
	case "file":
		return AttachmentFile
	}
	return ""
}

// defaultFeishuMimeType ports defaultFeishuMimeType.
func defaultFeishuMimeType(kind AttachmentKind) string {
	switch kind {
	case AttachmentImage:
		return "image/png"
	case AttachmentAudio:
		return "audio/mpeg"
	case AttachmentVideo:
		return "video/mp4"
	}
	return "application/octet-stream"
}

// splitFeishuText ports splitFeishuText: 4000-char chunks, and an empty text
// still produces one chunk.
func splitFeishuText(text string) []string {
	const limit = 4000
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
