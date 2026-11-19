package tools

import (
	"context"
	"encoding/json"
	"path/filepath"

	"github.com/nex-harness/nex/pkg/ai"
)

// UpdateFunc streams live execution output (e.g. bash stdout chunks) to the Agent Loop.
type UpdateFunc func(delta string)

// Result captures the structured output of a tool invocation.
type Result struct {
	Output      string            `json:"output"`
	ExtraBlocks []ai.ContentBlock `json:"extra_blocks,omitempty"`
	IsError     bool              `json:"is_error,omitempty"`
	Diff        string            `json:"diff,omitempty"`
	Additions   int               `json:"additions,omitempty"`
	Deletions   int               `json:"deletions,omitempty"`
	Metadata    map[string]any    `json:"metadata,omitempty"`
}

// Tool defines the unified interface for built-in, plugin, and MCP-backed tools.
type Tool interface {
	Name() string
	Category() string // "system" | "mcp" | "plugin"
	Definition() ai.ToolDefinition
	IsReadOnly(rawArgs json.RawMessage) bool
	Execute(ctx context.Context, workDir string, rawArgs json.RawMessage, onUpdate UpdateFunc) (Result, error)
}

// ResolvePath resolves targetPath relative to workDir when not already absolute.
func ResolvePath(workDir, targetPath string) string {
	if filepath.IsAbs(targetPath) {
		return filepath.Clean(targetPath)
	}
	if workDir == "" {
		return filepath.Clean(targetPath)
	}
	return filepath.Clean(filepath.Join(workDir, targetPath))
}

// DefaultCodingTools returns the core orthogonal coding tools inspired by pi-agent (including per-session TodoWrite/TodoRead).
func DefaultCodingTools() []Tool {
	todoStore := NewTodoStore()
	return []Tool{
		NewReadTool(),
		NewWriteTool(),
		NewEditTool(),
		NewBashTool(),
		NewGrepTool(),
		NewGlobTool(),
		NewTodoWriteTool(todoStore),
		NewTodoReadTool(todoStore),
	}
}
