package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/nex-harness/nex/pkg/ai"
	"github.com/nex-harness/nex/pkg/store"
)

// MemoryStore abstracts persistent global/project memory storage for the `memory_save` and `memory_list` tools.
type MemoryStore interface {
	ListMemories(ctx context.Context, workspacePath string) ([]store.MemoryRecord, error)
	UpsertMemory(ctx context.Context, rec store.MemoryRecord) (store.MemoryRecord, error)
	DeleteMemory(ctx context.Context, id string) error
}

type memorySaveTool struct {
	st            MemoryStore
	workspacePath string
}

// NewMemorySaveTool creates the `memory_save` tool for persisting durable user preferences or project facts.
func NewMemorySaveTool(st MemoryStore, workspacePath string) Tool {
	return &memorySaveTool{st: st, workspacePath: workspacePath}
}

func (t *memorySaveTool) Name() string                  { return "memory_save" }
func (t *memorySaveTool) Category() string              { return "system" }
func (t *memorySaveTool) IsReadOnly(json.RawMessage) bool { return true }

func (t *memorySaveTool) Definition() ai.ToolDefinition {
	return ai.ToolDefinition{
		Name:     "memory_save",
		Category: "system",
		Description: "Save or update a durable long-term memory (user preference, project convention, or architecture fact) " +
			"that will be automatically recalled in future sessions.",
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"id": {
					"type": "string",
					"description": "Optional existing memory ID to update."
				},
				"scope": {
					"type": "string",
					"enum": ["global", "project"],
					"description": "Use 'global' for cross-project user preferences, or 'project' for workspace-specific facts."
				},
				"category": {
					"type": "string",
					"enum": ["preference", "architecture", "convention", "fact"],
					"description": "Memory category."
				},
				"title": {
					"type": "string",
					"description": "Short descriptive title."
				},
				"content": {
					"type": "string",
					"description": "Concise, self-contained memory content."
				}
			},
			"required": ["title", "content"]
		}`),
	}
}

func (t *memorySaveTool) Execute(ctx context.Context, _ string, rawArgs json.RawMessage, _ UpdateFunc) (Result, error) {
	if t.st == nil {
		return Result{Output: "memory store unavailable", IsError: true}, nil
	}
	var args struct {
		ID       string `json:"id"`
		Scope    string `json:"scope"`
		Category string `json:"category"`
		Title    string `json:"title"`
		Content  string `json:"content"`
	}
	if err := json.Unmarshal(rawArgs, &args); err != nil {
		return Result{Output: "invalid memory_save arguments: " + err.Error(), IsError: true}, nil
	}
	title := strings.TrimSpace(args.Title)
	content := strings.TrimSpace(args.Content)
	if title == "" || content == "" {
		return Result{Output: "title and content are required", IsError: true}, nil
	}
	scope := strings.TrimSpace(args.Scope)
	if scope != "global" && scope != "project" {
		scope = "project"
	}
	ws := ""
	if scope == "project" {
		ws = t.workspacePath
	}
	cat := strings.TrimSpace(args.Category)
	if cat == "" {
		cat = "fact"
	}
	saved, err := t.st.UpsertMemory(ctx, store.MemoryRecord{
		ID:            strings.TrimSpace(args.ID),
		Scope:         scope,
		WorkspacePath: ws,
		Category:      cat,
		Title:         title,
		Content:       content,
	})
	if err != nil {
		return Result{Output: "failed to save memory: " + err.Error(), IsError: true}, nil
	}
	return Result{
		Output:   fmt.Sprintf("Saved %s memory [%s] (%s): %s", saved.Scope, saved.ID, saved.Category, saved.Title),
		Metadata: map[string]any{"memory_id": saved.ID, "scope": saved.Scope},
	}, nil
}

type memoryListTool struct {
	st            MemoryStore
	workspacePath string
}

// NewMemoryListTool creates the `memory_list` tool to inspect saved global and project memories.
func NewMemoryListTool(st MemoryStore, workspacePath string) Tool {
	return &memoryListTool{st: st, workspacePath: workspacePath}
}

func (t *memoryListTool) Name() string                  { return "memory_list" }
func (t *memoryListTool) Category() string              { return "system" }
func (t *memoryListTool) IsReadOnly(json.RawMessage) bool { return true }

func (t *memoryListTool) Definition() ai.ToolDefinition {
	return ai.ToolDefinition{
		Name:        "memory_list",
		Category:    "system",
		Description: "List saved long-term memories (global preferences and current project facts).",
		InputSchema: json.RawMessage(`{"type":"object","properties":{}}`),
	}
}

func (t *memoryListTool) Execute(ctx context.Context, _ string, _ json.RawMessage, _ UpdateFunc) (Result, error) {
	if t.st == nil {
		return Result{Output: "memory store unavailable", IsError: true}, nil
	}
	list, err := t.st.ListMemories(ctx, t.workspacePath)
	if err != nil {
		return Result{Output: "failed to list memories: " + err.Error(), IsError: true}, nil
	}
	if len(list) == 0 {
		return Result{Output: "(no long-term memories saved yet)"}, nil
	}
	var sb strings.Builder
	for _, m := range list {
		fmt.Fprintf(&sb, "- [%s] (%s/%s) **%s**: %s\n", m.ID, m.Scope, m.Category, m.Title, m.Content)
	}
	return Result{Output: strings.TrimSpace(sb.String())}, nil
}
