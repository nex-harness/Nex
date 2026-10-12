package fsx

import (
	"math"
	"os"
	"strconv"
	"strings"
)

// readTextFileRangeFromNode mirrors readTextFileRangeFromNode(). Node picks a fast
// whole-file path for files up to FAST_PATH_MAX_BYTES and a streaming path above
// it; both compute the same content, startLine, lineCount, totalLines and line
// endings, so this port always reads the whole file (the streaming path exists in
// Node only to bound memory, not to change results). This is documented as a known
// divergence for files above 10 MiB, which no vector covers.
const fastPathMaxBytes = 10 * 1024 * 1024

// ReadTextRange mirrors readTextFileRangeFromNode for the tool range read.
func (fs *FS) ReadTextRange(path string, offsetLine, limitLines *int, maxBytes int64) (ReadTextRangeResult, error) {
	info, err := os.Stat(path)
	if err != nil {
		return ReadTextRangeResult{}, toError(err, path)
	}
	if info.IsDir() {
		return ReadTextRangeResult{}, &Error{Code: "is_directory", Path: path, Message: "Cannot read directory as text file: " + path}
	}
	if !info.Mode().IsRegular() {
		return ReadTextRangeResult{}, &Error{Code: "not_file", Path: path, Message: "Cannot read non-file path as text: " + path}
	}
	if maxBytes > 0 && info.Size() > maxBytes {
		return ReadTextRangeResult{}, &Error{
			Code:    "too_large",
			Path:    path,
			Message: "File content (" + formatFileSize(info.Size()) + ") exceeds maximum allowed size (" + formatFileSize(maxBytes) + "). Use offset and limit parameters to read specific portions of the file, or search for specific content instead of reading the whole file.",
		}
	}

	buf, err := os.ReadFile(path)
	if err != nil {
		return ReadTextRangeResult{}, toError(err, path)
	}
	decoded, encoding, err := decodeTextBuffer(buf, path)
	if err != nil {
		return ReadTextRangeResult{}, err
	}
	isText := shouldNormalizeLineEndings(encoding)
	rawContent := decoded
	content := rawContent
	lineEndings := ""
	if isText {
		content = normalizeLineEndings(rawContent)
		lineEndings = detectLineEndings(rawContent)
	}

	var lines []string
	if len(content) > 0 {
		lines = strings.Split(content, "\n")
	}
	off := normalizeOffsetLine(offsetLine)
	selected := selectLines(lines, off, limitLines)

	return ReadTextRangeResult{
		Content:     strings.Join(selected, "\n"),
		Encoding:    encoding,
		LineEndings: lineEndings,
		BytesRead:   int64(len(buf)),
		SizeBytes:   info.Size(),
		StartLine:   off + 1,
		LineCount:   len(selected),
		TotalLines:  len(lines),
		Revision:    Revision{ID: revisionID(mtimeMs(info), info.Size()), MtimeMs: mtimeMs(info), SizeBytes: info.Size()},
	}, nil
}

func normalizeOffsetLine(offsetLine *int) int {
	if offsetLine == nil {
		return 0
	}
	v := *offsetLine
	if v < 0 {
		return 0
	}
	return v
}

func normalizeLimitLines(limitLines *int) (int, bool) {
	if limitLines == nil {
		return 0, false
	}
	v := *limitLines
	if v < 0 {
		v = 0
	}
	return v, true
}

func selectLines(lines []string, offsetLine int, limitLines *int) []string {
	if offsetLine > len(lines) {
		offsetLine = len(lines)
	}
	rest := lines[offsetLine:]
	limit, has := normalizeLimitLines(limitLines)
	if !has {
		return rest
	}
	if limit > len(rest) {
		limit = len(rest)
	}
	return rest[:limit]
}

func formatFileSize(bytes int64) string {
	if bytes < 1024 {
		return strconv.FormatInt(bytes, 10) + "B"
	}
	if bytes < 1024*1024 {
		return formatUnit(float64(bytes)/1024) + "KB"
	}
	return formatUnit(float64(bytes)/(1024*1024)) + "MB"
}

// formatUnit mirrors formatUnit(): an integer value prints without a decimal,
// otherwise one fixed decimal with a trailing ".0" stripped.
func formatUnit(value float64) string {
	if value == math.Trunc(value) {
		return strconv.FormatInt(int64(value), 10)
	}
	return strings.TrimSuffix(strconv.FormatFloat(value, 'f', 1, 64), ".0")
}
