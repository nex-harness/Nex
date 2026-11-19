package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"

	"github.com/nex-harness/nex/pkg/ai"
)

// TodoItem represents a single structured task item written via todo_write.
type TodoItem struct {
	Content  string `json:"content"`
	Status   string `json:"status"`   // "pending" | "in_progress" | "completed"
	Priority string `json:"priority"` // "high" | "medium" | "low"
}

// TodoStore holds the per-session todo list shared between todo_write and todo_read.
type TodoStore struct {
	mu    sync.RWMutex
	items []TodoItem
}

func NewTodoStore() *TodoStore {
	return &TodoStore{}
}

func (s *TodoStore) Set(items []TodoItem) {
	cp := make([]TodoItem, len(items))
	copy(cp, items)
	s.mu.Lock()
	s.items = cp
	s.mu.Unlock()
}

func (s *TodoStore) Get() []TodoItem {
	s.mu.RLock()
	defer s.mu.RUnlock()
	cp := make([]TodoItem, len(s.items))
	copy(cp, s.items)
	return cp
}

// --- todo_write tool ---

type todoWriteArgs struct {
	Todos []TodoItem `json:"todos"`
}

type TodoWriteTool struct {
	store *TodoStore
}

func NewTodoWriteTool(st *TodoStore) *TodoWriteTool {
	if st == nil {
		st = NewTodoStore()
	}
	return &TodoWriteTool{store: st}
}

func (t *TodoWriteTool) Name() string                      { return "todo_write" }
func (t *TodoWriteTool) Category() string                  { return "system" }
func (t *TodoWriteTool) IsReadOnly(_ json.RawMessage) bool { return true }

func (t *TodoWriteTool) Definition() ai.ToolDefinition {
	return ai.ToolDefinition{
		Name:        "todo_write",
		Category:    "system",
		Description: "Maintain the session task list rendered live in the user's Progress panel.\n\n- Send the complete updated todo array on every call; this replaces the previous list rather than appending to it.\n- Keep exactly one item `in_progress` at a time, and mark an item `completed` only once it is genuinely done.\n- Use it to plan multi-step work and to keep the user informed as steps finish. For a single quick task, skip it.",
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"todos": {
					"type": "array",
					"description": "Complete updated todo list for the current session",
					"items": {
						"type": "object",
						"properties": {
							"content": {"type": "string", "description": "Concise task description"},
							"status": {"type": "string", "enum": ["pending", "in_progress", "completed"]},
							"priority": {"type": "string", "enum": ["high", "medium", "low"]}
						},
						"required": ["content", "status", "priority"]
					}
				}
			},
			"required": ["todos"]
		}`),
	}
}

func (t *TodoWriteTool) Execute(_ context.Context, _ string, rawArgs json.RawMessage, _ UpdateFunc) (Result, error) {
	var args todoWriteArgs
	if err := json.Unmarshal(rawArgs, &args); err != nil {
		return Result{Output: "invalid todo_write arguments: " + err.Error(), IsError: true}, nil
	}

	normalized := make([]TodoItem, 0, len(args.Todos))
	completed, inProg, pending := 0, 0, 0
	for _, it := range args.Todos {
		content := strings.TrimSpace(it.Content)
		if content == "" {
			continue
		}
		st := it.Status
		switch st {
		case "completed":
			completed++
		case "in_progress":
			inProg++
		default:
			st = "pending"
			pending++
		}
		prio := it.Priority
		if prio != "high" && prio != "medium" && prio != "low" {
			prio = "medium"
		}
		normalized = append(normalized, TodoItem{
			Content:  content,
			Status:   st,
			Priority: prio,
		})
	}

	t.store.Set(normalized)

	return Result{
		Output: fmt.Sprintf("Updated todo list (%d total: %d completed, %d in_progress, %d pending)", len(normalized), completed, inProg, pending),
		Metadata: map[string]any{
			"todos":       normalized,
			"completed":   completed,
			"in_progress": inProg,
			"pending":     pending,
		},
	}, nil
}

// --- todo_read tool ---

type TodoReadTool struct {
	store *TodoStore
}

func NewTodoReadTool(st *TodoStore) *TodoReadTool {
	if st == nil {
		st = NewTodoStore()
	}
	return &TodoReadTool{store: st}
}

func (t *TodoReadTool) Name() string                      { return "todo_read" }
func (t *TodoReadTool) Category() string                  { return "system" }
func (t *TodoReadTool) IsReadOnly(_ json.RawMessage) bool { return true }

func (t *TodoReadTool) Definition() ai.ToolDefinition {
	return ai.ToolDefinition{
		Name:        "todo_read",
		Category:    "system",
		Description: "Read the current session task list exactly as last written by `todo_write`. Takes no arguments.\n\nUse it to re-check your plan after a long detour; do not use it as a substitute for making progress.",
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {}
		}`),
	}
}

func (t *TodoReadTool) Execute(_ context.Context, _ string, _ json.RawMessage, _ UpdateFunc) (Result, error) {
	items := t.store.Get()
	b, _ := json.MarshalIndent(items, "", "  ")
	return Result{
		Output:   string(b),
		Metadata: map[string]any{"todos": items},
	}, nil
}
