package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/nex-harness/nex/pkg/ai"
)

type writeArgs struct {
	FilePath string `json:"file_path"`
	Content  string `json:"content"`
}

// WriteTool creates or atomically overwrites a file in the workspace.
type WriteTool struct{}

func NewWriteTool() *WriteTool { return &WriteTool{} }

func (t *WriteTool) Name() string                      { return "write" }
func (t *WriteTool) Category() string                  { return "system" }
func (t *WriteTool) IsReadOnly(_ json.RawMessage) bool { return false }

func (t *WriteTool) Definition() ai.ToolDefinition {
	return ai.ToolDefinition{
		Name:        "write",
		Category:    "system",
		Description: "Create a new file or overwrite an existing file with exact content, creating parent directories if needed.",
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"file_path": {"type": "string", "description": "Absolute or workspace-relative path to write"},
				"content": {"type": "string", "description": "Complete file content"}
			},
			"required": ["file_path", "content"]
		}`),
	}
}

func (t *WriteTool) Execute(_ context.Context, workDir string, rawArgs json.RawMessage, _ UpdateFunc) (Result, error) {
	var args writeArgs
	if err := json.Unmarshal(rawArgs, &args); err != nil {
		return Result{Output: "invalid write arguments: " + err.Error(), IsError: true}, nil
	}
	if strings.TrimSpace(args.FilePath) == "" {
		return Result{Output: "file_path is required", IsError: true}, nil
	}

	absPath := ResolvePath(workDir, args.FilePath)
	if err := os.MkdirAll(filepath.Dir(absPath), 0o755); err != nil {
		return Result{Output: fmt.Sprintf("mkdir parent failed: %v", err), IsError: true}, nil
	}

	perm := os.FileMode(0o644)
	var oldContent string
	if info, err := os.Stat(absPath); err == nil && !info.IsDir() {
		perm = info.Mode().Perm()
		if prev, err := os.ReadFile(absPath); err == nil && !isBinaryBytes(prev) {
			oldContent = strings.ReplaceAll(string(prev), "\r\n", "\n")
		}
	}

	tmpPath := absPath + ".nex.tmp"
	if err := os.WriteFile(tmpPath, []byte(args.Content), perm); err != nil {
		return Result{Output: fmt.Sprintf("write temp file failed: %v", err), IsError: true}, nil
	}
	if err := os.Rename(tmpPath, absPath); err != nil {
		_ = os.Remove(tmpPath)
		if err2 := os.WriteFile(absPath, []byte(args.Content), perm); err2 != nil {
			return Result{Output: fmt.Sprintf("write %s failed: %v", args.FilePath, err2), IsError: true}, nil
		}
	}

	newNorm := strings.ReplaceAll(args.Content, "\r\n", "\n")
	relName := args.FilePath
	if workDir != "" {
		if rel, err := filepath.Rel(workDir, absPath); err == nil && !strings.HasPrefix(rel, "..") {
			relName = filepath.ToSlash(rel)
		}
	}
	diff, adds, dels := GenerateUnifiedDiff(relName, oldContent, newNorm)
	lineCount := strings.Count(newNorm, "\n") + 1

	return Result{
		Output:    fmt.Sprintf("Wrote %s (%d bytes, %d lines)", relName, len(args.Content), lineCount),
		Diff:      diff,
		Additions: adds,
		Deletions: dels,
	}, nil
}
