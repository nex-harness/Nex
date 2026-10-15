package bots

import (
	"regexp"
	"strconv"
	"strings"
)

// provider_weixin_attachment.go reads a WeChat iLink message's media.
//
// The reader is deliberately generous about field spellings (file_id / media_id /
// id / encrypt_query_param / media / md5): a plain image message carries ONLY
// `media` inside image_item, and reading just file_id would silently drop every
// picture a user sends.
var (
	weixinImageExtensionPattern = regexp.MustCompile(`(?i)\.(svg|png|jpe?g|gif|webp|heic|bmp)$`)
	weixinSVGPattern            = regexp.MustCompile(`(?i)\.svg$`)
	weixinPNGPattern            = regexp.MustCompile(`(?i)\.png$`)
	weixinJPEGPattern           = regexp.MustCompile(`(?i)\.jpe?g$`)
	weixinGIFPattern            = regexp.MustCompile(`(?i)\.gif$`)
	weixinWEBPPattern           = regexp.MustCompile(`(?i)\.webp$`)
)

// inferWeixinAttachmentKind ports inferWeixinAttachmentKind: an explicit kind
// wins, otherwise the mime/filename decides and file is the fallback.
func inferWeixinAttachmentKind(item map[string]any) AttachmentKind {
	if _, ok := item["image_item"].(map[string]any); ok {
		return AttachmentImage
	}
	explicit := firstNonEmptyString(
		readWeixinString(item, "kind"),
		readWeixinString(item, "media_type"),
		readWeixinString(item, "mediaType"),
		readWeixinString(item, "type_name"),
	)
	mimeType := firstNonEmptyString(readWeixinString(item, "mime_type"), readWeixinString(item, "mimeType"))
	filename := firstNonEmptyString(
		readWeixinString(item, "filename"),
		readWeixinString(item, "file_name"),
		readWeixinString(item, "name"),
	)
	normalized := strings.ToLower(explicit + " " + mimeType + " " + filename)
	switch {
	case strings.Contains(normalized, "image") || strings.Contains(normalized, "photo") ||
		strings.Contains(normalized, "picture") || weixinImageExtensionPattern.MatchString(filename):
		return AttachmentImage
	case strings.Contains(normalized, "audio") || strings.Contains(normalized, "voice"):
		return AttachmentAudio
	case strings.Contains(normalized, "video"):
		return AttachmentVideo
	}
	return AttachmentFile
}

// inferMimeTypeFromFilename ports inferMimeTypeFromFilename.
func inferMimeTypeFromFilename(filename string) string {
	switch {
	case weixinSVGPattern.MatchString(filename):
		return "image/svg+xml"
	case weixinPNGPattern.MatchString(filename):
		return "image/png"
	case weixinJPEGPattern.MatchString(filename):
		return "image/jpeg"
	case weixinGIFPattern.MatchString(filename):
		return "image/gif"
	case weixinWEBPPattern.MatchString(filename):
		return "image/webp"
	}
	return ""
}

// readWeixinAttachmentItem ports readWeixinAttachmentItem: the media payload is
// the FIRST present of image/file/video/audio/media item, flattened with its
// nested `media` object.
func readWeixinAttachmentItem(item any, index int) *Attachment {
	record, ok := item.(map[string]any)
	if !ok {
		return nil
	}
	if readWeixinTextItem(record) != "" {
		return nil
	}
	media := record
	for _, key := range []string{"image_item", "file_item", "video_item", "audio_item", "media_item"} {
		if nested, ok := record[key].(map[string]any); ok {
			media = nested
			break
		}
	}
	mediaSource := map[string]any{}
	for key, value := range media {
		mediaSource[key] = value
	}
	if mediaPayload, ok := media["media"].(map[string]any); ok {
		for key, value := range mediaPayload {
			mediaSource[key] = value
		}
	}
	providerFileID := ""
	for _, key := range []string{
		"file_id", "fileId", "media_id", "mediaId", "id",
		"encrypt_query_param", "encryptQueryParam",
		// 微信图片消息的 image_item 只返回 media 字段，没有 file_id/md5；
		// 这里将 media 作为后续下载和去重的资源标识。
		"media", "md5",
	} {
		if providerFileID = readNumberOrString(mediaSource, key); providerFileID != "" {
			break
		}
	}
	downloadURL := ""
	for _, key := range []string{
		"url", "download_url", "downloadUrl",
		// image_item.media 内的 full_url 是实际图片下载地址。
		"full_url", "fullUrl",
	} {
		if downloadURL = readWeixinString(mediaSource, key); downloadURL != "" {
			break
		}
	}
	dataBase64 := ""
	for _, key := range []string{"data_base64", "dataBase64", "base64"} {
		if dataBase64 = readWeixinString(mediaSource, key); dataBase64 != "" {
			break
		}
	}
	if providerFileID == "" && downloadURL == "" && dataBase64 == "" {
		return nil
	}
	merged := map[string]any{}
	for key, value := range record {
		merged[key] = value
	}
	for key, value := range mediaSource {
		merged[key] = value
	}
	kind := inferWeixinAttachmentKind(merged)
	filename := firstNonEmptyString(
		readWeixinString(mediaSource, "filename"),
		readWeixinString(mediaSource, "file_name"),
		readWeixinString(mediaSource, "name"),
	)
	if filename == "" {
		if kind == AttachmentImage {
			filename = "weixin-image-" + strconv.Itoa(index+1) + ".jpg"
		} else {
			filename = "weixin-attachment-" + strconv.Itoa(index+1)
		}
	}
	mimeType := firstNonEmptyString(
		readWeixinString(mediaSource, "mime_type"),
		readWeixinString(mediaSource, "mimeType"),
		inferMimeTypeFromFilename(filename),
	)
	if mimeType == "" {
		switch kind {
		case AttachmentImage:
			mimeType = "image/jpeg"
		case AttachmentAudio:
			mimeType = "audio/mpeg"
		case AttachmentVideo:
			mimeType = "video/mp4"
		default:
			mimeType = "application/octet-stream"
		}
	}
	attachment := &Attachment{
		ID:         firstNonEmptyString(providerFileID, downloadURL, "weixin-"+strconv.Itoa(index+1)),
		Kind:       kind,
		Filename:   filename,
		MimeType:   mimeType,
		DataBase64: dataBase64,
	}
	for _, key := range []string{"size", "sizeBytes", "file_size", "len", "mid_size"} {
		if size, ok := readNumber(mediaSource, key); ok && size != 0 {
			sizeValue := float64(size)
			attachment.SizeBytes = &sizeValue
			break
		}
	}
	if providerFileID != "" {
		attachment.ProviderFileID = providerFileID
	}
	if downloadURL != "" {
		attachment.DownloadURL = downloadURL
	}
	aesKey := firstNonEmptyString(
		readWeixinString(mediaSource, "aes_key"),
		readWeixinString(mediaSource, "aesKey"),
		readWeixinString(mediaSource, "aeskey"),
	)
	if aesKey != "" {
		attachment.ProviderMeta = map[string]string{weixinAesKey: aesKey}
	}
	return attachment
}

// readWeixinDirectAttachment ports readWeixinDirectAttachment: an explicitly
// typed attachment object.
func readWeixinDirectAttachment(item any, index int) *Attachment {
	record, ok := item.(map[string]any)
	if !ok {
		return nil
	}
	kind := AttachmentKind(readWeixinString(record, "kind"))
	switch kind {
	case AttachmentImage, AttachmentAudio, AttachmentVideo, AttachmentFile:
	default:
		return nil
	}
	id := readWeixinString(record, "id")
	if id == "" {
		id = "weixin-" + strconv.Itoa(index+1)
	}
	filename := readWeixinString(record, "filename")
	if filename == "" {
		filename = id + "." + string(kind)
	}
	mimeType := firstNonEmptyString(readWeixinString(record, "mimeType"), readWeixinString(record, "mime_type"))
	if mimeType == "" {
		switch kind {
		case AttachmentImage:
			mimeType = "image/jpeg"
		case AttachmentAudio:
			mimeType = "audio/mpeg"
		case AttachmentVideo:
			mimeType = "video/mp4"
		default:
			mimeType = "application/octet-stream"
		}
	}
	attachment := &Attachment{ID: id, Kind: kind, Filename: filename, MimeType: mimeType}
	if size, ok := readNumber(record, "sizeBytes"); ok {
		sizeValue := float64(size)
		attachment.SizeBytes = &sizeValue
	} else if size, ok := readNumber(record, "size"); ok {
		sizeValue := float64(size)
		attachment.SizeBytes = &sizeValue
	}
	attachment.ProviderFileID = firstNonEmptyString(readWeixinString(record, "providerFileId"), readWeixinString(record, "file_id"))
	attachment.DownloadURL = firstNonEmptyString(readWeixinString(record, "downloadUrl"), readWeixinString(record, "download_url"))
	attachment.DataBase64 = firstNonEmptyString(readWeixinString(record, "dataBase64"), readWeixinString(record, "data_base64"))
	if meta, ok := record["providerMetadata"].(map[string]any); ok {
		providerMeta := map[string]string{}
		for key, value := range meta {
			if text, ok := value.(string); ok {
				providerMeta[key] = text
			}
		}
		if len(providerMeta) > 0 {
			attachment.ProviderMeta = providerMeta
		}
	}
	return attachment
}

// readWeixinAttachments ports readWeixinAttachments: item_list media first, then
// explicit attachments.
func readWeixinAttachments(message map[string]any) []Attachment {
	inner := readWeixinInner(message)
	var direct []any
	switch value := message["attachments"].(type) {
	case []any:
		direct = value
	default:
		if inner != nil {
			if value, ok := inner["attachments"].([]any); ok {
				direct = value
			}
		}
	}
	var attachments []Attachment
	for index, item := range readWeixinItemList(message) {
		if attachment := readWeixinAttachmentItem(item, index); attachment != nil {
			attachments = append(attachments, *attachment)
		}
	}
	for index, item := range direct {
		if attachment := readWeixinDirectAttachment(item, index); attachment != nil {
			attachments = append(attachments, *attachment)
		}
	}
	return attachments
}
