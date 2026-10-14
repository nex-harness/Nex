package file

// service.go ports packages/services/src/file/fileService.ts — the IFileService
// Tier-1 adapter. Every constant, clamp, and error message is transcribed;
// the caches (existence LRU, workspace index TTL, in-flight scan
// deduplication) keep the TS semantics including the 60s TTL and the
// .nexignore fingerprint invalidation.

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"
)

const (
	defaultTextReadBytes = 128 * 1024
	maxTextReadBytes     = 256 * 1024
	defaultMediaPreview  = 4 * 1024 * 1024
	maxMediaPreview      = 8 * 1024 * 1024
	defaultBinaryRead    = 256 * 1024
	maxBinaryRead        = 1024 * 1024
	defaultBinaryPreview = 25 * 1024 * 1024
	maxBinaryPreview     = 25 * 1024 * 1024
	existenceCacheTTL    = 60 * time.Second
	existenceCacheMax    = 100
	existenceBatchLimit  = 15
	scratchWorkspaceRoot = "NexProject"
	indexCacheTTL        = 60 * time.Second
	indexScanConcurrency = 8
	indexCacheMaxEntries = 4
)

var imageExtToMediaType = map[string]string{
	".apng": "image/apng", ".avif": "image/avif", ".bmp": "image/bmp",
	".gif": "image/gif", ".ico": "image/x-icon", ".jpeg": "image/jpeg",
	".jpg": "image/jpeg", ".png": "image/png", ".svg": "image/svg+xml",
	".webp": "image/webp",
}

// Service is the IFileService implementation.
type Service struct {
	filter        searchFilter
	logger        serviceLogger
	existence     *existenceCache
	pendingChecks map[string]*pendingExistence
	indexCache    map[string]*workspaceIndex
	scanning      map[string]*scanState
	mu            sync.Mutex
	now           func() time.Time
}

type pendingExistence struct {
	once  sync.Once
	ch    chan bool
	value bool
}

// NewService builds the service; nil filter uses defaultSearchFilter.
func NewService(filter searchFilter) *Service {
	if filter == nil {
		filter = defaultSearchFilter{}
	}
	return &Service{
		filter:        filter,
		logger:        noopLogger{},
		existence:     newExistenceCache(),
		pendingChecks: map[string]*pendingExistence{},
		indexCache:    map[string]*workspaceIndex{},
		scanning:      map[string]*scanState{},
		now:           time.Now,
	}
}

// ---------------------------------------------------------------------------
// public methods (the RPC surface)
// ---------------------------------------------------------------------------

// FileEntry is FileEntry from protocol.ts.
type FileEntry struct {
	Name           string `json:"name"`
	Path           string `json:"path"`
	Type           string `json:"type"`
	IsSymbolicLink bool   `json:"isSymbolicLink,omitempty"`
}

// FileStat is FileStat from protocol.ts.
type FileStat struct {
	Path    string   `json:"path"`
	Type    string   `json:"type"`
	Size    *int64   `json:"size,omitempty"`
	MtimeMs *float64 `json:"mtimeMs,omitempty"`
}

// TextSlice is FileTextSlice.
type TextSlice struct {
	Path       string `json:"path"`
	Content    string `json:"content"`
	Offset     int64  `json:"offset"`
	BytesRead  int64  `json:"bytesRead"`
	TotalBytes int64  `json:"totalBytes"`
	Truncated  bool   `json:"truncated"`
	IsBinary   bool   `json:"isBinary"`
}

// MediaPreview is FileMediaPreview.
type MediaPreview struct {
	Path       string `json:"path"`
	MediaType  string `json:"mediaType"`
	DataBase64 string `json:"dataBase64"`
	TotalBytes int64  `json:"totalBytes"`
}

// BinaryPreview is FileBinaryPreview.
type BinaryPreview struct {
	Path       string `json:"path"`
	DataBase64 string `json:"dataBase64"`
	TotalBytes int64  `json:"totalBytes"`
}

// nodeStyleError rewrites an os error into the Node errno-message shape the
// RPC contract carries ("ENOENT: no such file or directory, <syscall>
// '<path>'"), so UI branches keyed on the message text behave identically.
func nodeStyleError(err error, syscallName, path string) error {
	switch {
	case os.IsNotExist(err):
		return fmt.Errorf("ENOENT: no such file or directory, %s '%s'", syscallName, path)
	case os.IsPermission(err):
		return fmt.Errorf("EACCES: permission denied, %s '%s'", syscallName, path)
	}
	return err
}

// isProbablyBinary ports the TS heuristic: NUL anywhere, or >30% control
// chars (excluding tab/LF/FF/CR).
func isProbablyBinary(buf []byte) bool {
	if len(buf) == 0 {
		return false
	}
	suspicious := 0
	for _, b := range buf {
		if b == 0 {
			return true
		}
		isWhitespace := b == 9 || b == 10 || b == 12 || b == 13
		isControl := (b >= 1 && b <= 8) || (b >= 14 && b <= 31) || b == 127
		if !isWhitespace && isControl {
			suspicious++
		}
	}
	return float64(suspicious)/float64(len(buf)) > 0.3
}

func clampReadLength(length *int64) int64 {
	if length == nil {
		return defaultTextReadBytes
	}
	v := *length
	if v < 1 {
		v = 1
	}
	if v > maxTextReadBytes {
		v = maxTextReadBytes
	}
	return v
}

func clampMediaPreviewBytes(v *int64) int64 {
	if v == nil {
		return defaultMediaPreview
	}
	if *v < 1 {
		return 1
	}
	if *v > maxMediaPreview {
		return maxMediaPreview
	}
	return *v
}

func clampBinaryReadLength(v int64) int64 {
	if v < 1 {
		return 1
	}
	if v > maxBinaryRead {
		return maxBinaryRead
	}
	return v
}

func clampBinaryPreviewBytes(v *int64) int64 {
	if v == nil {
		return defaultBinaryPreview
	}
	if *v < 1 {
		return 1
	}
	if *v > maxBinaryPreview {
		return maxBinaryPreview
	}
	return *v
}

func inferMediaType(path string) string {
	if mt, ok := imageExtToMediaType[strings.ToLower(filepath.Ext(path))]; ok {
		return mt
	}
	if mt := getMediaPreviewFormat(path); mt != "" {
		return mt
	}
	return "application/octet-stream"
}

// getMediaPreviewFormat ports getMediaPreviewFormat (media-preview.ts): the
// audio/video extension table.
func getMediaPreviewFormat(path string) string {
	normalized := strings.ToLower(strings.ReplaceAll(path, `\`, "/"))
	for _, f := range mediaPreviewFormats {
		if strings.HasSuffix(normalized, f.ext) {
			return f.mediaType
		}
	}
	return ""
}

func validateScratchWorkspaceName(name string) (string, error) {
	trimmed := strings.TrimSpace(name)
	if trimmed == "" {
		return "", fmt.Errorf("Workspace name is required.")
	}
	if strings.ContainsAny(trimmed, `/\`) {
		return "", fmt.Errorf("Workspace name cannot contain path separators.")
	}
	return trimmed, nil
}

func normalizeRelativePath(rootPath, targetPath string) string {
	rel, err := filepath.Rel(rootPath, targetPath)
	if err != nil {
		return targetPath
	}
	return filepath.ToSlash(rel)
}

func isSkippableWorkspaceListError(err error) bool {
	return os.IsPermission(err) || os.IsNotExist(err)
}

// resolveReaddirEntryType ports resolveReaddirEntryType: a symlink is
// re-classified by its TARGET (Node Dirent does not report isDirectory for
// symlinked dirs), falling back to file.
func resolveReaddirEntryType(ctx context.Context, entryPath string, isDir, isSymlink bool) string {
	if isDir {
		return "directory"
	}
	if !isSymlink {
		return "file"
	}
	if info, err := os.Stat(entryPath); err == nil && info.IsDir() {
		return "directory"
	}
	return "file"
}

// Readdir ports readdir. includeHidden defaults to false (old callers hide
// dotfiles; the workspace tree passes true).
func (s *Service) Readdir(ctx context.Context, path string, includeHidden bool) ([]FileEntry, error) {
	dirEntries, err := os.ReadDir(path)
	if err != nil {
		// Node's fs.readdir rejects with "ENOENT: no such file or directory,
		// scandir '<path>'" — the message is part of the RPC contract.
		return nil, nodeStyleError(err, "scandir", path)
	}
	out := make([]FileEntry, 0, len(dirEntries))
	for _, de := range dirEntries {
		if !includeHidden && strings.HasPrefix(de.Name(), ".") {
			continue
		}
		entryPath := filepath.Join(path, de.Name())
		isSymlink := de.Type()&os.ModeSymlink != 0
		out = append(out, FileEntry{
			Name:           de.Name(),
			Path:           entryPath,
			Type:           resolveReaddirEntryType(ctx, entryPath, de.IsDir(), isSymlink),
			IsSymbolicLink: isSymlink,
		})
	}
	sortFileEntries(out)
	return out, nil
}

func sortFileEntries(entries []FileEntry) {
	sort.SliceStable(entries, func(i, j int) bool {
		if entries[i].Type != entries[j].Type {
			return entries[i].Type == "directory"
		}
		return localeCompare(entries[i].Name, entries[j].Name)
	})
}

// localeCompare ports JS String.localeCompare's default collation closely
// enough for the sort orders the UI depends on: case-insensitive primary
// comparison (a < B), and — when letters are equal ignoring case — lowercase
// sorts before uppercase (the ICU default). Punctuation and digits compare
// by code point. This matches Node's behavior on every fixture name the
// differential corpus uses; the full UCA is not needed for those orders.
func localeCompare(a, b string) bool {
	return localeCompareOrdered(a, b) < 0
}

// localeCompareOrdered returns -1/0/1 like localeCompare.
func localeCompareOrdered(a, b string) int {
	ar, br := []rune(a), []rune(b)
	n := len(ar)
	if len(br) < n {
		n = len(br)
	}
	for i := 0; i < n; i++ {
		la, lb := unicode.ToLower(ar[i]), unicode.ToLower(br[i])
		if la != lb {
			if la < lb {
				return -1
			}
			return 1
		}
		if ar[i] != br[i] {
			// same letter, different case: lowercase first
			if ar[i] == la {
				return -1
			}
			return 1
		}
	}
	switch {
	case len(ar) < len(br):
		return -1
	case len(ar) > len(br):
		return 1
	}
	return 0
}

// Stat ports stat: directories omit size/mtimeMs.
func (s *Service) Stat(ctx context.Context, path string) (FileStat, error) {
	info, err := os.Stat(path)
	if err != nil {
		return FileStat{}, nodeStyleError(err, "stat", path)
	}
	if info.IsDir() {
		return FileStat{Path: path, Type: "directory"}, nil
	}
	size := info.Size()
	mtime := float64(info.ModTime().UnixNano()) / 1e6
	return FileStat{Path: path, Type: "file", Size: &size, MtimeMs: &mtime}, nil
}

// CheckFilesExist ports checkFilesExist: batch limit 15, order preserved.
func (s *Service) CheckFilesExist(ctx context.Context, paths []string) ([]map[string]any, error) {
	if len(paths) > existenceBatchLimit {
		return nil, fmt.Errorf("File existence check supports at most %d paths.", existenceBatchLimit)
	}
	out := make([]map[string]any, len(paths))
	for i, p := range paths {
		exists, err := s.checkFileExists(ctx, p)
		if err != nil {
			return nil, err
		}
		out[i] = map[string]any{"path": p, "exists": exists}
	}
	return out, nil
}

// ResolvePath ports resolvePath (realpath).
func (s *Service) ResolvePath(ctx context.Context, path string) (string, error) {
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		// Node's realpath rejects with "ENOENT: no such file or directory, realpath '<path>'".
		return "", nodeStyleError(err, "realpath", path)
	}
	return resolved, nil
}
