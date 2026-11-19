package tools

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/nex-harness/nex/pkg/ai"
)

type readArgs struct {
	FilePath string `json:"file_path"`
	Offset   int    `json:"offset,omitempty"`
	Limit    int    `json:"limit,omitempty"`
}

// ReadTool reads text files with line numbers or image files as base64 multimodal blocks.
type ReadTool struct{}

func NewReadTool() *ReadTool { return &ReadTool{} }

func (t *ReadTool) Name() string     { return "read" }
func (t *ReadTool) Category() string { return "system" }
func (t *ReadTool) IsReadOnly(_ json.RawMessage) bool {
	return true
}

func (t *ReadTool) Definition() ai.ToolDefinition {
	return ai.ToolDefinition{
		Name:        "read",
		Category:    "system",
		Description: "Read a file from the local filesystem. Returns up to 2000 lines by default in `cat -n` format: each line is prefixed with its 1-based line number and a tab, which is clickable when referenced as `file_path:line_number`.\n\n- Use `offset` and `limit` to slice large files; read the region you need instead of the whole file.\n- Supports PNG, JPG, GIF, and WEBP images, which are returned as multimodal image content.\n- Do NOT re-read a file immediately after `edit` or `write` to verify the change; those tools already report the result and a diff.\n- Binary and oversized files are rejected with an explanation.",
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"file_path": {"type": "string", "description": "Absolute or workspace-relative path to the file"},
				"offset": {"type": "integer", "description": "1-based line number to start reading from"},
				"limit": {"type": "integer", "description": "Maximum number of lines to return (default 2000)"}
			},
			"required": ["file_path"]
		}`),
	}
}

func detectImageMime(path string, data []byte) string {
	ext := strings.ToLower(filepath.Ext(path))
	switch {
	case len(data) >= 8 && bytes.Equal(data[:8], []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n'}):
		return "image/png"
	case len(data) >= 3 && data[0] == 0xff && data[1] == 0xd8 && data[2] == 0xff:
		return "image/jpeg"
	case len(data) >= 6 && (bytes.Equal(data[:6], []byte("GIF87a")) || bytes.Equal(data[:6], []byte("GIF89a"))):
		return "image/gif"
	case len(data) >= 12 && bytes.Equal(data[:4], []byte("RIFF")) && bytes.Equal(data[8:12], []byte("WEBP")):
		return "image/webp"
	}
	switch ext {
	case ".png":
		return "image/png"
	case ".jpg", ".jpeg":
		return "image/jpeg"
	case ".gif":
		return "image/gif"
	case ".webp":
		return "image/webp"
	default:
		return ""
	}
}

func isBinaryBytes(data []byte) bool {
	sniff := data
	if len(sniff) > 8192 {
		sniff = sniff[:8192]
	}
	return bytes.IndexByte(sniff, 0) >= 0
}

func (t *ReadTool) Execute(_ context.Context, workDir string, rawArgs json.RawMessage, _ UpdateFunc) (Result, error) {
	var args readArgs
	if err := json.Unmarshal(rawArgs, &args); err != nil {
		return Result{Output: "invalid read arguments: " + err.Error(), IsError: true}, nil
	}
	if strings.TrimSpace(args.FilePath) == "" {
		return Result{Output: "file_path is required", IsError: true}, nil
	}

	absPath := ResolvePath(workDir, args.FilePath)
	info, err := os.Stat(absPath)
	if err != nil {
		return Result{Output: fmt.Sprintf("cannot access %s: %v", args.FilePath, err), IsError: true}, nil
	}
	if info.IsDir() {
		return Result{Output: fmt.Sprintf("%s is a directory, not a file", args.FilePath), IsError: true}, nil
	}
	if info.Size() > 20*1024*1024 {
		return Result{Output: fmt.Sprintf("file %s is too large (%d bytes); use grep or bash head/tail", args.FilePath, info.Size()), IsError: true}, nil
	}

	raw, err := os.ReadFile(absPath)
	if err != nil {
		return Result{Output: fmt.Sprintf("read %s failed: %v", args.FilePath, err), IsError: true}, nil
	}

	if mime := detectImageMime(absPath, raw); mime != "" {
		b64 := base64.StdEncoding.EncodeToString(raw)
		return Result{
			Output:      fmt.Sprintf("Read image %s (%s, %d bytes)", args.FilePath, mime, len(raw)),
			ExtraBlocks: []ai.ContentBlock{ai.ImageBase64Block(mime, b64)},
		}, nil
	}

	if isBinaryBytes(raw) {
		return Result{
			Output:  fmt.Sprintf("cannot read binary file %s (%d bytes)", args.FilePath, len(raw)),
			IsError: true,
		}, nil
	}

	// Strip optional UTF-8 BOM for display
	raw = bytes.TrimPrefix(raw, []byte{0xEF, 0xBB, 0xBF})
	normalized := strings.ReplaceAll(string(raw), "\r\n", "\n")
	if normalized == "" {
		return Result{Output: "(empty file)"}, nil
	}

	lines := strings.Split(normalized, "\n")
	// Drop trailing empty element if file ended with newline
	if len(lines) > 1 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}

	start := args.Offset
	if start <= 0 {
		start = 1
	}
	limit := args.Limit
	if limit <= 0 {
		limit = 2000
	}
	if start > len(lines) {
		return Result{Output: fmt.Sprintf("offset %d exceeds file length (%d lines)", start, len(lines)), IsError: true}, nil
	}

	end := start - 1 + limit
	if end > len(lines) {
		end = len(lines)
	}

	var sb strings.Builder
	for i := start - 1; i < end; i++ {
		line := lines[i]
		if len(line) > 2000 {
			line = line[:2000] + "... [line truncated]"
		}
		fmt.Fprintf(&sb, "%6d\t%s\n", i+1, line)
	}
	if end < len(lines) {
		fmt.Fprintf(&sb, "\n... [%d more lines; total %d lines]", len(lines)-end, len(lines))
	}

	return Result{Output: strings.TrimRight(sb.String(), "\n")}, nil
}
