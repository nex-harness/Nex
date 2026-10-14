package mediapreview

// mediapreview_test.go: the createMediaPreviewService behaviors — format
// gating, kind mismatch, stat check, local-url vs inline, size cap, and the
// URL builder's encoding.

import (
	"context"
	"encoding/base64"
	"os"
	"path/filepath"
	"testing"

	"github.com/tylinux/nex/go/internal/services/file"
)

func newMediaService(t *testing.T) (*Service, string) {
	t.Helper()
	dir := t.TempDir()
	s := NewService(Options{FileService: file.NewService(nil)})
	return s, dir
}

func TestPrepareUnsupportedFormatErrors(t *testing.T) {
	s, dir := newMediaService(t)
	p := filepath.Join(dir, "x.txt")
	os.WriteFile(p, []byte("text"), 0o644)
	_, err := s.Prepare(context.Background(), p, KindVideo)
	if err == nil || err.Error() != "Unsupported media preview format: "+p {
		t.Fatalf("got %v", err)
	}
}

func TestPrepareKindMismatchErrors(t *testing.T) {
	s, dir := newMediaService(t)
	p := filepath.Join(dir, "v.mp4")
	os.WriteFile(p, []byte("data"), 0o644)
	_, err := s.Prepare(context.Background(), p, KindAudio)
	if err == nil {
		t.Fatal("mp4 with expectedKind=audio must fail")
	}
}

func TestPrepareMissingFileAndDirectoryErrors(t *testing.T) {
	s, dir := newMediaService(t)
	_, err := s.Prepare(context.Background(), filepath.Join(dir, "missing.mp4"), KindVideo)
	if err == nil {
		t.Fatal("missing file must fail")
	}
	sub := filepath.Join(dir, "d.mov")
	os.MkdirAll(sub, 0o755)
	_, err = s.Prepare(context.Background(), sub, KindVideo)
	if err == nil || err.Error() != "Path is not a media file: "+sub {
		t.Fatalf("got %v", err)
	}
}

func TestPrepareInlineReturnsBase64(t *testing.T) {
	s, dir := newMediaService(t)
	p := filepath.Join(dir, "a.mp3")
	os.WriteFile(p, []byte("ID3data"), 0o644)
	prep, err := s.Prepare(context.Background(), p, KindAudio)
	if err != nil {
		t.Fatal(err)
	}
	if prep.Kind != "inline" || prep.MediaType != "audio/mpeg" {
		t.Fatalf("prep: %+v", prep)
	}
	if prep.DataBase64 != base64.StdEncoding.EncodeToString([]byte("ID3data")) {
		t.Fatalf("base64: %s", prep.DataBase64)
	}
	if prep.Size != 7 {
		t.Fatalf("size: %d", prep.Size)
	}
}

func TestPrepareInlineSizeCap(t *testing.T) {
	dir := t.TempDir()
	s := NewService(Options{FileService: file.NewService(nil), InlineMaxBytes: 16})
	p := filepath.Join(dir, "big.wav")
	os.WriteFile(p, make([]byte, 32), 0o644)
	_, err := s.Prepare(context.Background(), p, KindAudio)
	if err == nil || err.Error() != "Media file is too large for inline preview: "+p {
		t.Fatalf("got %v", err)
	}
}

func TestPrepareLocalURLWhenAuthorized(t *testing.T) {
	s, dir := newMediaService(t)
	p := filepath.Join(dir, "v.webm")
	os.WriteFile(p, []byte("video"), 0o644)
	// desktop-main adapter: authorized path gets a nex-media:// URL
	authorized := map[string]string{p: filepath.Join("/canonical", "v.webm")}
	s.options.AuthorizeLocalMediaPreviewPath = func(ctx context.Context, path string) (string, error) {
		if c, ok := authorized[path]; ok {
			return c, nil
		}
		return path, nil
	}
	s.options.CreateLocalMediaPreviewURL = BuildLocalMediaPreviewURL

	prep, err := s.Prepare(context.Background(), p, KindVideo)
	if err != nil {
		t.Fatal(err)
	}
	if prep.Kind != "local-url" || prep.MediaType != "video/webm" {
		t.Fatalf("prep: %+v", prep)
	}
	if prep.Path != filepath.Join("/canonical", "v.webm") {
		t.Fatalf("canonical path: %q", prep.Path)
	}
	if prep.URL != "nex-media://local/preview?path=%2Fcanonical%2Fv.webm" {
		t.Fatalf("url: %q", prep.URL)
	}
	if prep.Size != 5 {
		t.Fatalf("size: %d", prep.Size)
	}
}

func TestGetMediaPreviewFormatTable(t *testing.T) {
	cases := []struct {
		path      string
		wantKind  MediaPreviewKind
		wantMedia string
	}{
		{"a/b/c.MP4", KindVideo, "video/mp4"},             // case-insensitive
		{`C:\x\y\clip.mov`, KindVideo, "video/quicktime"}, // windows separators
		{"song.opus", KindAudio, "audio/opus"},
		{"x.m4a", KindAudio, "audio/mp4"},
		{"x.flac", KindAudio, "audio/flac"},
		{"x.weba", KindAudio, "audio/webm"},
	}
	for _, tc := range cases {
		f, ok := GetMediaPreviewFormat(tc.path)
		if !ok || f.kind != tc.wantKind || f.mediaType != tc.wantMedia {
			t.Errorf("%s: got (%v,%v,%v)", tc.path, ok, f.kind, f.mediaType)
		}
	}
	if _, ok := GetMediaPreviewFormat("a.txt"); ok {
		t.Error("txt must not be a media format")
	}
	if _, ok := GetMediaPreviewFormat("mp4"); ok {
		t.Error("extension must be a suffix, not a substring")
	}
}

func TestRefreshPlaybackUrlAndReleaseAreAbsent(t *testing.T) {
	// the default TS factory provides neither refreshPlaybackUrl nor release;
	// the RPC contract is "Method not found"
	s, _ := newMediaService(t)
	if _, _, err := s.RefreshPlaybackUrl(context.Background(), "pid"); err == nil {
		t.Error("refreshPlaybackUrl must be absent")
	}
	if err := s.Release(context.Background(), "pid"); err == nil {
		t.Error("release must be absent")
	}
}
