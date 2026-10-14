package file

// formats.go holds the media-preview extension table (the port of
// MEDIA_PREVIEW_FORMATS in packages/shared/src/media-preview.ts) used by
// inferMediaType for the readMediaPreview media-type inference.

type mediaPreviewFormat struct {
	ext       string
	kind      string
	mediaType string
}

var mediaPreviewFormats = []mediaPreviewFormat{
	{".mp4", "video", "video/mp4"}, {".mov", "video", "video/quicktime"},
	{".webm", "video", "video/webm"}, {".m4v", "video", "video/x-m4v"},
	{".mp3", "audio", "audio/mpeg"}, {".wav", "audio", "audio/wav"},
	{".m4a", "audio", "audio/mp4"}, {".ogg", "audio", "audio/ogg"},
	{".opus", "audio", "audio/opus"}, {".flac", "audio", "audio/flac"},
	{".weba", "audio", "audio/webm"},
}
