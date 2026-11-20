package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"github.com/nex-harness/nex/pkg/ai"
	"github.com/nex-harness/nex/pkg/store"
)

// MemoryStore abstracts persistent global/project memory storage for memory_* tools.
type MemoryStore interface {
	ListMemories(ctx context.Context, workspacePath string) ([]store.MemoryRecord, error)
	GetMemory(ctx context.Context, id string) (*store.MemoryRecord, error)
	SearchMemories(ctx context.Context, workspacePath, query string, limit int) ([]store.MemoryRecord, error)
	UpsertMemory(ctx context.Context, rec store.MemoryRecord) (store.MemoryRecord, error)
	DeleteMemory(ctx context.Context, id string) error
}

var secretTokenPatterns = []*regexp.Regexp{
	regexp.MustCompile(`-----BEGIN [A-Z0-9 ]*PRIVATE KEY-----`),
	regexp.MustCompile(`\bsk-(?:proj-|ant-)?[A-Za-z0-9_-]{20,}\b`),
	regexp.MustCompile(`\b(?:ghp|gho|ghu|ghs|ghr)_[A-Za-z0-9]{24,}\b`),
	regexp.MustCompile(`\bgithub_pat_[A-Za-z0-9_]{24,}\b`),
	regexp.MustCompile(`\bAKIA[0-9A-Z]{16}\b`),
	regexp.MustCompile(`\bxox[baprs]-[A-Za-z0-9-]{16,}\b`),
}

// ContainsSecretLikeMaterial detects API keys, tokens, or private keys that must never be persisted to memory.
func ContainsSecretLikeMaterial(text string) bool {
	for _, re := range secretTokenPatterns {
		if re.MatchString(text) {
			return true
		}
	}
	return false
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
			"that will be automatically recalled in future sessions. Never save API keys, tokens, passwords, or transient task noise.",
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
	if ContainsSecretLikeMaterial(title) || ContainsSecretLikeMaterial(content) {
		return Result{
			Output:  "refused to save memory: content appears to contain a secret, API key, or private key",
			IsError: true,
		}, nil
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

// NewMemoryListTool creates the `memory_list` tool returning the compact index of memories.
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
		Description: "List saved long-term memories (compact index of IDs, scopes, categories, and titles). Pass optional query to filter.",
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"query": {
					"type": "string",
					"description": "Optional keyword filter."
				}
			}
		}`),
	}
}

func (t *memoryListTool) Execute(ctx context.Context, _ string, rawArgs json.RawMessage, _ UpdateFunc) (Result, error) {
	if t.st == nil {
		return Result{Output: "memory store unavailable", IsError: true}, nil
	}
	var args struct {
		Query string `json:"query"`
	}
	_ = json.Unmarshal(rawArgs, &args)

	var (
		list []store.MemoryRecord
		err  error
	)
	if strings.TrimSpace(args.Query) != "" {
		list, err = t.st.SearchMemories(ctx, t.workspacePath, args.Query, 50)
	} else {
		list, err = t.st.ListMemories(ctx, t.workspacePath)
	}
	if err != nil {
		return Result{Output: "failed to list memories: " + err.Error(), IsError: true}, nil
	}
	if len(list) == 0 {
		return Result{Output: "No persistent memories saved yet."}, nil
	}
	var b strings.Builder
	for _, m := range list {
		fmt.Fprintf(&b, "- [%s] (%s/%s) %s: %s\n", m.ID, m.Scope, m.Category, m.Title, m.Content)
	}
	return Result{Output: strings.TrimSpace(b.String())}, nil
}

type memoryReadTool struct {
	st            MemoryStore
	workspacePath string
}

// NewMemoryReadTool creates the `memory_read` tool for progressive-disclosure on-demand recall.
func NewMemoryReadTool(st MemoryStore, workspacePath string) Tool {
	return &memoryReadTool{st: st, workspacePath: workspacePath}
}

func (t *memoryReadTool) Name() string                  { return "memory_read" }
func (t *memoryReadTool) Category() string              { return "system" }
func (t *memoryReadTool) IsReadOnly(json.RawMessage) bool { return true }

func (t *memoryReadTool) Definition() ai.ToolDefinition {
	return ai.ToolDefinition{
		Name:        "memory_read",
		Category:    "system",
		Description: "Read full details of one or more long-term memories by ID (from the `# Long-term memories` index) or by keyword query.",
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"ids": {
					"type": "array",
					"items": { "type": "string" },
					"description": "Specific memory IDs to load (e.g. [\"mem_a1b2c3d4\"])."
				},
				"query": {
					"type": "string",
					"description": "Keyword search across titles, categories, and contents."
				}
			}
		}`),
	}
}

func (t *memoryReadTool) Execute(ctx context.Context, _ string, rawArgs json.RawMessage, _ UpdateFunc) (Result, error) {
	if t.st == nil {
		return Result{Output: "memory store unavailable", IsError: true}, nil
	}
	var args struct {
		IDs   []string `json:"ids"`
		Query string   `json:"query"`
	}
	if err := json.Unmarshal(rawArgs, &args); err != nil {
		return Result{Output: "invalid memory_read arguments: " + err.Error(), IsError: true}, nil
	}

	seen := make(map[string]bool)
	var records []store.MemoryRecord
	for _, id := range args.IDs {
		id = strings.TrimSpace(id)
		if id == "" || seen[id] {
			continue
		}
		if m, err := t.st.GetMemory(ctx, id); err == nil && m != nil {
			seen[m.ID] = true
			records = append(records, *m)
		}
	}
	if q := strings.TrimSpace(args.Query); q != "" {
		found, err := t.st.SearchMemories(ctx, t.workspacePath, q, 20)
		if err == nil {
			for _, m := range found {
				if !seen[m.ID] {
					seen[m.ID] = true
					records = append(records, m)
				}
			}
		}
	}
	if len(records) == 0 {
		return Result{Output: "No matching memories found."}, nil
	}
	var b strings.Builder
	for _, m := range records {
		fmt.Fprintf(&b, "### [%s] %s (%s/%s)\n%s\n\n", m.ID, m.Title, m.Scope, m.Category, m.Content)
	}
	return Result{Output: strings.TrimSpace(b.String())}, nil
}

type memoryDeleteTool struct {
	st MemoryStore
}

// NewMemoryDeleteTool creates the `memory_delete` tool to remove obsolete or incorrect memories.
func NewMemoryDeleteTool(st MemoryStore) Tool {
	return &memoryDeleteTool{st: st}
}

func (t *memoryDeleteTool) Name() string                  { return "memory_delete" }
func (t *memoryDeleteTool) Category() string              { return "system" }
func (t *memoryDeleteTool) IsReadOnly(json.RawMessage) bool { return true }

func (t *memoryDeleteTool) Definition() ai.ToolDefinition {
	return ai.ToolDefinition{
		Name:        "memory_delete",
		Category:    "system",
		Description: "Delete an obsolete, duplicate, or incorrect long-term memory by its ID.",
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"id": {
					"type": "string",
					"description": "Memory ID to delete (e.g. mem_a1b2c3d4)."
				}
			},
			"required": ["id"]
		}`),
	}
}

func (t *memoryDeleteTool) Execute(ctx context.Context, _ string, rawArgs json.RawMessage, _ UpdateFunc) (Result, error) {
	if t.st == nil {
		return Result{Output: "memory store unavailable", IsError: true}, nil
	}
	var args struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(rawArgs, &args); err != nil {
		return Result{Output: "invalid memory_delete arguments: " + err.Error(), IsError: true}, nil
	}
	id := strings.TrimSpace(args.ID)
	if id == "" {
		return Result{Output: "id is required", IsError: true}, nil
	}
	if err := t.st.DeleteMemory(ctx, id); err != nil {
		return Result{Output: "failed to delete memory: " + err.Error(), IsError: true}, nil
	}
	return Result{Output: fmt.Sprintf("Deleted memory [%s].", id)}, nil
}
