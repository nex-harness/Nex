package tools

import (
	"encoding/json"
	"fmt"
	"math"
	"strconv"
	"strings"
)

// Limits mirror the Read contract constants.
const (
	ReadMaxFileSizeBytes = 256 * 1024
	ReadMaxOutputTokens  = 25000
	ReadDefaultMaxLines  = 2000
)

const fileUnchangedStub = "Wasted call — file unchanged since your last Read. Refer to that earlier tool_result instead."

// ReadOutput is the Read handler output (text and refusal shapes the Go port
// supports; image/video/pdf/notebook are out of scope — see tools.md).
type ReadOutput struct {
	Type       string `json:"type"`
	FilePath   string `json:"filePath"`
	Content    string `json:"content"`
	NumLines   int    `json:"numLines"`
	StartLine  int    `json:"startLine"`
	TotalLines int    `json:"totalLines"`
	SizeBytes  *int64 `json:"sizeBytes,omitempty"`
	BytesRead  *int64 `json:"bytesRead,omitempty"`
	Truncated  bool   `json:"truncated"`
}

// MarshalJSON emits the exact shape of the TypeScript ReadOutput union: a
// file_unchanged result carries only type+filePath, a text result carries every
// text field (including truncated:false).
func (o ReadOutput) MarshalJSON() ([]byte, error) {
	if o.Type == "file_unchanged" {
		return json.Marshal(struct {
			Type     string `json:"type"`
			FilePath string `json:"filePath"`
		}{Type: o.Type, FilePath: o.FilePath})
	}
	type textOut struct {
		Type       string `json:"type"`
		FilePath   string `json:"filePath"`
		Content    string `json:"content"`
		NumLines   int    `json:"numLines"`
		StartLine  int    `json:"startLine"`
		TotalLines int    `json:"totalLines"`
		SizeBytes  *int64 `json:"sizeBytes,omitempty"`
		BytesRead  *int64 `json:"bytesRead,omitempty"`
		Truncated  bool   `json:"truncated"`
	}
	return json.Marshal(textOut{o.Type, o.FilePath, o.Content, o.NumLines, o.StartLine, o.TotalLines, o.SizeBytes, o.BytesRead, o.Truncated})
}

// Read reads a text file, mirroring handlers/read.ts + read-text.ts. It returns
// either a ReadOutput, a *Failure (a message the model sees) or an error.
func Read(input map[string]any, c *Context) (*ReadOutput, error) {
	filePath, err := stringField(input, "file_path")
	if err != nil {
		return nil, err
	}
	offset, err := optIntField(input, "offset")
	if err != nil {
		return nil, err
	}
	limit, err := optIntField(input, "limit")
	if err != nil {
		return nil, err
	}

	resolved, err := ResolveWorkspacePath(filePath, c.WorkingDirectory, c.WorkspaceRoot, "read")
	if err != nil {
		return nil, err
	}

	// Preflight mirrors Read's runtime schema superRefine: device files that would
	// block, and extensions the tool cannot decode as text.
	if preflightErr := readPathPreflight(resolved); preflightErr != nil {
		return nil, preflightErr
	}

	stat, err := c.FS.Stat(c.Ctx, resolved)
	if err != nil {
		return nil, readStatError(err, resolved, c)
	}

	if c.ReadState != nil {
		cacheOffset := 1
		if offset != nil {
			cacheOffset = *offset
		}
		key := CreateReadStateKey(resolved, intPtrOrNil(cacheOffset), limit)
		if cached, ok := c.ReadState.entries[key]; ok && cachedReadFresh(cached, stat) {
			return &ReadOutput{Type: "file_unchanged", FilePath: resolved}, nil
		}
	}

	var maxBytes int64
	if limit == nil {
		maxBytes = ReadMaxFileSizeBytes
	}
	rr, err := c.FS.ReadTextRange(c.Ctx, resolved, offsetLineForLimit(offset), limit, maxBytes)
	if err != nil {
		return nil, readRangeError(err, resolved, c)
	}

	out := readTextToOutput(resolved, rr, offset)
	updateReadState(c.ReadState, resolved, rr, offset, limit, out)
	return out, nil
}

// offsetLineForLimit mirrors toRangeOffsetLine: offset<=1 becomes 0, else offset-1.
func offsetLineForLimit(offset *int) *int {
	if offset == nil || *offset <= 1 {
		return ToIntPtr(0)
	}
	return ToIntPtr(*offset - 1)
}

func readTextToOutput(filePath string, rr ReadRange, offset *int) *ReadOutput {
	startLine := rr.StartLine
	if offset != nil && *offset == 0 {
		startLine = 0
	}
	size := rr.SizeBytes
	read := rr.BytesRead
	out := &ReadOutput{
		Type:       "text",
		FilePath:   filePath,
		Content:    rr.Content,
		NumLines:   rr.LineCount,
		StartLine:  startLine,
		TotalLines: rr.TotalLines,
		SizeBytes:  &size,
		BytesRead:  &read,
		Truncated:  false,
	}
	if out.Content == "" && out.NumLines == 0 && out.StartLine == 1 && out.TotalLines == 0 {
		out.NumLines = 1
		out.TotalLines = 1
	}
	return out
}

func updateReadState(state *ReadState, filePath string, rr ReadRange, offset, limit *int, out *ReadOutput) {
	if state == nil {
		return
	}
	key := CreateReadStateKey(filePath, intPtrOrNil(cacheOffset(offset)), limit)
	mtime := NormalizeMtimeMs(&rr.Revision.MtimeMs)
	size := rr.SizeBytes
	state.set(key, ReadStateEntry{
		Path:          filePath,
		Content:       out.Content,
		Offset:        offset,
		Limit:         limit,
		IsPartialView: false,
		ReadAt:        nowUTC(),
		SourceTool:    "Read",
		RevisionID:    rr.Revision.ID,
		MtimeMs:       mtime,
		SizeBytes:     &size,
	})
}

func cacheOffset(offset *int) int {
	if offset == nil {
		return 1
	}
	return *offset
}

func intPtrOrNil(v int) *int { return &v }

func cachedReadFresh(entry ReadStateEntry, stat Stat) bool {
	if entry.IsPartialView {
		return false
	}
	mtimeMs := stat.MtimeMs
	if stat.Revision != nil {
		mtimeMs = stat.Revision.MtimeMs
	}
	if entry.MtimeMs != nil {
		return *NormalizeMtimeMs(&mtimeMs) == *entry.MtimeMs && entry.SizeBytes != nil && *entry.SizeBytes == stat.SizeBytes
	}
	if entry.RevisionID != "" && stat.Revision != nil && stat.Revision.ID != "" {
		return entry.RevisionID == stat.Revision.ID
	}
	return entry.SizeBytes != nil && *entry.SizeBytes == stat.SizeBytes
}

func stringField(input map[string]any, key string) (string, error) {
	v, ok := input[key]
	if !ok || v == nil {
		return "", fmt.Errorf("missing %s", key)
	}
	s, ok := v.(string)
	if !ok {
		return "", fmt.Errorf("%s must be a string", key)
	}
	return s, nil
}

func optIntField(input map[string]any, key string) (*int, error) {
	v, ok := input[key]
	if !ok || v == nil {
		return nil, nil
	}
	switch n := v.(type) {
	case int:
		return &n, nil
	case int64:
		i := int(n)
		return &i, nil
	case float64:
		i := int(n)
		return &i, nil
	case json.Number:
		// The runtime decodes tool input with UseNumber so large integers and key order
		// survive; the schema validator has already accepted this value as an integer.
		if i64, err := n.Int64(); err == nil {
			i := int(i64)
			return &i, nil
		}
		if f, err := n.Float64(); err == nil && f == math.Trunc(f) {
			i := int(f)
			return &i, nil
		}
		return nil, fmt.Errorf("%s must be an integer", key)
	default:
		return nil, fmt.Errorf("%s must be an integer", key)
	}
}

func itoaInt(v int) string { return strconv.Itoa(v) }

// unsupportedReadBinaryExtensions mirrors UNSUPPORTED_BINARY_EXTENSIONS.
var unsupportedReadBinaryExtensions = map[string]bool{
	".7z": true, ".a": true, ".bin": true, ".bz2": true, ".class": true, ".dll": true,
	".dmg": true, ".dylib": true, ".exe": true, ".gz": true, ".jar": true, ".o": true,
	".pyc": true, ".rar": true, ".so": true, ".tar": true, ".tgz": true, ".wasm": true, ".zip": true,
}

// blockingDevicePaths mirrors BLOCKING_DEVICE_PATHS, lowercased.
var blockingDevicePaths = map[string]bool{
	"/dev/console": true, "/dev/fd/0": true, "/dev/fd/1": true, "/dev/fd/2": true,
	"/dev/full": true, "/dev/random": true, "/dev/stderr": true, "/dev/stdin": true,
	"/dev/stdout": true, "/dev/tty": true, "/dev/urandom": true, "/dev/zero": true,
}

// readPathPreflight mirrors validateReadPathSemantics; the failure text is the
// <tool_use_error> message the model sees.
func readPathPreflight(filePath string) error {
	lowerPath := strings.ToLower(filePath)
	if blockingDevicePaths[lowerPath] {
		return &Failure{ErrorCode: errorCodeReadInputPreflight, Message: "<tool_use_error>Cannot read '" + filePath + "': this device file would block or produce infinite output.</tool_use_error>"}
	}
	ext := readFileExtension(lowerPath)
	if ext == "" || !unsupportedReadBinaryExtensions[ext] {
		return nil
	}
	return &Failure{ErrorCode: errorCodeReadInputPreflight, Message: "<tool_use_error>This tool cannot read binary files. The file appears to be a binary " + ext + " file. Please use appropriate tools for binary file analysis.</tool_use_error>"}
}

const errorCodeReadInputPreflight = 3

// readFileExtension mirrors readFileExtension(): the extension of the basename
// including the dot, or "" when there is no dot after position 0.
func readFileExtension(filePath string) string {
	lastSlash := strings.LastIndexByte(filePath, '/')
	if bs := strings.LastIndexByte(filePath, '\\'); bs > lastSlash {
		lastSlash = bs
	}
	base := filePath[lastSlash+1:]
	dot := strings.LastIndexByte(base, '.')
	if dot <= 0 {
		return ""
	}
	return base[dot:]
}
