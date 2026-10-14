// Package mediapreview ports packages/services/src/media-preview/mediaPreview.ts
// (step 3.2): the IMediaPreviewService.prepare flow — format check against the
// audio/video table, stat check, and the local-url vs inline split. The
// host-range-url branch is Desktop-main-only in the TS (refreshPlaybackUrl and
// release are optional methods that the default factory does not provide), so
// the Go port exposes prepare (+ hooks for the desktop-main adapter) and
// rejects the optional methods with the TS's absent-method behavior.
package mediapreview

import (
	"context"
	"fmt"
	"strings"

	"github.com/tylinux/nex/go/internal/services/file"
)

// ChannelName is ServiceChannels.MediaPreview.
const ChannelName = "media-preview"

// MediaPreviewKind is MediaPreviewKind.
type MediaPreviewKind string

const (
	KindAudio MediaPreviewKind = "audio"
	KindVideo MediaPreviewKind = "video"
)

// formatInfo is MediaPreviewFormat.
type formatInfo struct {
	extension string
	kind      MediaPreviewKind
	mediaType string
}

var mediaFormats = []formatInfo{
	{".mp4", KindVideo, "video/mp4"}, {".mov", KindVideo, "video/quicktime"},
	{".webm", KindVideo, "video/webm"}, {".m4v", KindVideo, "video/x-m4v"},
	{".mp3", KindAudio, "audio/mpeg"}, {".wav", KindAudio, "audio/wav"},
	{".m4a", KindAudio, "audio/mp4"}, {".ogg", KindAudio, "audio/ogg"},
	{".opus", KindAudio, "audio/opus"}, {".flac", KindAudio, "audio/flac"},
	{".weba", KindAudio, "audio/webm"},
}

// GetMediaPreviewFormat ports getMediaPreviewFormat: extension table lookup
// on the lowercased, forward-slashed path.
func GetMediaPreviewFormat(path string) (formatInfo, bool) {
	normalized := strings.ToLower(strings.ReplaceAll(path, `\`, "/"))
	for _, f := range mediaFormats {
		if strings.HasSuffix(normalized, f.extension) {
			return f, true
		}
	}
	return formatInfo{}, false
}

// Preparation is MediaPreviewPreparation.
type Preparation struct {
	Kind         string `json:"kind"`
	MediaType    string `json:"mediaType"`
	Path         string `json:"path"`
	Size         int64  `json:"size"`
	DataBase64   string `json:"dataBase64,omitempty"`
	URL          string `json:"url,omitempty"`
	PreviewID    string `json:"previewId,omitempty"`
	URLExpiresAt int64  `json:"urlExpiresAt,omitempty"`
}

// Options mirror createMediaPreviewService options.
type Options struct {
	FileService *file.Service
	// AuthorizeLocalMediaPreviewPath + CreateLocalMediaPreviewURL: set by the
	// desktop-main adapter to return nex-media://local/preview URLs instead
	// of inline base64.
	AuthorizeLocalMediaPreviewPath func(ctx context.Context, path string) (string, error)
	CreateLocalMediaPreviewURL     func(path string) string
	InlineMaxBytes                 int64
}

// Service is the IMediaPreviewService implementation.
type Service struct {
	options Options
}

// NewService builds the media preview service.
func NewService(options Options) *Service {
	if options.InlineMaxBytes == 0 {
		options.InlineMaxBytes = 8 * 1024 * 1024
	}
	return &Service{options: options}
}

// Prepare ports prepare.
func (s *Service) Prepare(ctx context.Context, path string, expectedKind MediaPreviewKind) (Preparation, error) {
	format, ok := GetMediaPreviewFormat(path)
	if !ok || format.kind != expectedKind {
		return Preparation{}, fmt.Errorf("Unsupported media preview format: %s", path)
	}
	st, err := s.options.FileService.Stat(ctx, path)
	if err != nil {
		return Preparation{}, err
	}
	if st.Type != "file" || st.Size == nil {
		return Preparation{}, fmt.Errorf("Path is not a media file: %s", path)
	}
	if s.options.AuthorizeLocalMediaPreviewPath != nil && s.options.CreateLocalMediaPreviewURL != nil {
		canonicalPath, err := s.options.AuthorizeLocalMediaPreviewPath(ctx, path)
		if err != nil {
			return Preparation{}, err
		}
		return Preparation{
			Kind:      "local-url",
			MediaType: format.mediaType,
			Path:      canonicalPath,
			Size:      *st.Size,
			URL:       s.options.CreateLocalMediaPreviewURL(canonicalPath),
		}, nil
	}
	if *st.Size > s.options.InlineMaxBytes {
		return Preparation{}, fmt.Errorf("Media file is too large for inline preview: %s", path)
	}
	preview, err := s.options.FileService.ReadMediaPreview(ctx, path, &s.options.InlineMaxBytes)
	if err != nil {
		return Preparation{}, err
	}
	return Preparation{
		Kind:       "inline",
		DataBase64: preview.DataBase64,
		MediaType:  preview.MediaType,
		Path:       path,
		Size:       preview.TotalBytes,
	}, nil
}

// RefreshPlaybackUrl: the default TS service does not implement it (the
// method is optional on the interface). Calling it over RPC in TS gives
// "Method not found" from ProxyChannel; the Go port keeps that contract.
func (s *Service) RefreshPlaybackUrl(ctx context.Context, previewID string) (url string, expiresAt int64, err error) {
	return "", 0, fmt.Errorf("Method not found: refreshPlaybackUrl")
}

// Release mirrors the TS optional method absence.
func (s *Service) Release(ctx context.Context, previewID string) error {
	return fmt.Errorf("Method not found: release")
}

// localMediaPreviewScheme is LOCAL_MEDIA_PREVIEW_SCHEME (platform.ts).
const localMediaPreviewScheme = "nex-media"

// BuildLocalMediaPreviewURL ports buildLocalMediaPreviewUrl:
// nex-media://local/preview?path=<encoded>.
func BuildLocalMediaPreviewURL(path string) string {
	return fmt.Sprintf("%s://local/preview?path=%s", localMediaPreviewScheme, queryEscape(path))
}

// queryEscape matches JS URLSearchParams.set encoding for typical paths
// (encodeURIComponent with '+' for spaces — Go's QueryEscape uses '+'
// too, and encodes the same printable set).
func queryEscape(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		if (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') ||
			c == '-' || c == '_' || c == '.' || c == '!' || c == '~' || c == '*' || c == '\'' || c == '(' || c == ')' {
			b.WriteByte(c)
		} else if c == ' ' {
			b.WriteByte('+')
		} else {
			b.WriteString(fmt.Sprintf("%%%02X", c))
		}
	}
	return b.String()
}
