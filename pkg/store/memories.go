package store

import (
	"context"
	"strings"
	"time"

	"github.com/google/uuid"
)

// MemoryRecord represents a persistent long-term user preference or project fact recalled across sessions.
type MemoryRecord struct {
	ID            string `json:"id"`
	Scope         string `json:"scope"` // "global" | "project"
	WorkspacePath string `json:"workspace_path,omitempty"`
	Category      string `json:"category"` // "preference" | "architecture" | "convention" | "fact"
	Title         string `json:"title"`
	Content       string `json:"content"`
	CreatedAt     int64  `json:"created_at"`
	UpdatedAt     int64  `json:"updated_at"`
}

// UpsertMemory inserts or updates a persistent memory record.
func (s *Store) UpsertMemory(ctx context.Context, rec MemoryRecord) (MemoryRecord, error) {
	now := time.Now().UnixMilli()
	if strings.TrimSpace(rec.ID) == "" {
		rec.ID = "mem_" + uuid.NewString()[:8]
	}
	if rec.Scope != "global" && rec.Scope != "project" {
		rec.Scope = "project"
	}
	if rec.Scope == "global" {
		rec.WorkspacePath = ""
	}
	if strings.TrimSpace(rec.Category) == "" {
		rec.Category = "fact"
	}
	if rec.CreatedAt == 0 {
		rec.CreatedAt = now
	}
	rec.UpdatedAt = now

	_, err := s.mainDB.ExecContext(ctx, `
		INSERT INTO memories (id, scope, workspace_path, category, title, content, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			scope = excluded.scope,
			workspace_path = excluded.workspace_path,
			category = excluded.category,
			title = excluded.title,
			content = excluded.content,
			updated_at = excluded.updated_at
	`, rec.ID, rec.Scope, rec.WorkspacePath, rec.Category, rec.Title, rec.Content, rec.CreatedAt, rec.UpdatedAt)
	return rec, err
}

// ListMemories returns all memories (when workspacePath == "") or global + matching project memories.
func (s *Store) ListMemories(ctx context.Context, workspacePath string) ([]MemoryRecord, error) {
	var (
		query string
		args  []any
	)
	ws := strings.TrimSpace(workspacePath)
	if ws == "" {
		query = `SELECT id, scope, workspace_path, category, title, content, created_at, updated_at FROM memories ORDER BY updated_at DESC`
	} else {
		query = `SELECT id, scope, workspace_path, category, title, content, created_at, updated_at FROM memories WHERE scope = 'global' OR workspace_path = '' OR workspace_path = ? ORDER BY scope ASC, updated_at DESC`
		args = append(args, ws)
	}

	rows, err := s.mainDB.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make([]MemoryRecord, 0)
	for rows.Next() {
		var m MemoryRecord
		if err := rows.Scan(&m.ID, &m.Scope, &m.WorkspacePath, &m.Category, &m.Title, &m.Content, &m.CreatedAt, &m.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// DeleteMemory deletes a persistent memory by ID.
func (s *Store) DeleteMemory(ctx context.Context, id string) error {
	_, err := s.mainDB.ExecContext(ctx, `DELETE FROM memories WHERE id = ?`, id)
	return err
}

// GetMemory fetches a single persistent memory by ID.
func (s *Store) GetMemory(ctx context.Context, id string) (*MemoryRecord, error) {
	var m MemoryRecord
	err := s.mainDB.QueryRowContext(ctx, `
		SELECT id, scope, workspace_path, category, title, content, created_at, updated_at
		FROM memories WHERE id = ?
	`, strings.TrimSpace(id)).Scan(&m.ID, &m.Scope, &m.WorkspacePath, &m.Category, &m.Title, &m.Content, &m.CreatedAt, &m.UpdatedAt)
	if err != nil {
		return nil, err
	}
	return &m, nil
}

// SearchMemories returns global + workspace-matching memories filtered by case-insensitive substring match
// against title, content, or category.
func (s *Store) SearchMemories(ctx context.Context, workspacePath, query string, limit int) ([]MemoryRecord, error) {
	all, err := s.ListMemories(ctx, workspacePath)
	if err != nil {
		return nil, err
	}
	if limit <= 0 || limit > 100 {
		limit = 20
	}
	q := strings.ToLower(strings.TrimSpace(query))
	out := make([]MemoryRecord, 0, min(len(all), limit))
	for _, m := range all {
		if q == "" ||
			strings.Contains(strings.ToLower(m.Title), q) ||
			strings.Contains(strings.ToLower(m.Content), q) ||
			strings.Contains(strings.ToLower(m.Category), q) ||
			strings.Contains(strings.ToLower(m.ID), q) {
			out = append(out, m)
			if len(out) >= limit {
				break
			}
		}
	}
	return out, nil
}

// ReplaceAllMemories atomically replaces the memories table with the consolidated set of records.
// Used both for Memory Consolidation (Dream) and exact Runner ConfigSnapshot reconciliation.
func (s *Store) ReplaceAllMemories(ctx context.Context, records []MemoryRecord) ([]MemoryRecord, error) {
	now := time.Now().UnixMilli()
	tx, err := s.mainDB.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := tx.ExecContext(ctx, `DELETE FROM memories`); err != nil {
		return nil, err
	}

	saved := make([]MemoryRecord, 0, len(records))
	seen := make(map[string]bool, len(records))
	for _, rec := range records {
		rec.Title = strings.TrimSpace(rec.Title)
		rec.Content = strings.TrimSpace(rec.Content)
		if rec.Title == "" || rec.Content == "" {
			continue
		}
		if strings.TrimSpace(rec.ID) == "" || seen[rec.ID] {
			rec.ID = "mem_" + uuid.NewString()[:8]
		}
		seen[rec.ID] = true
		if rec.Scope != "global" && rec.Scope != "project" {
			rec.Scope = "project"
		}
		if rec.Scope == "global" {
			rec.WorkspacePath = ""
		}
		if strings.TrimSpace(rec.Category) == "" {
			rec.Category = "fact"
		}
		if rec.CreatedAt == 0 {
			rec.CreatedAt = now
		}
		if rec.UpdatedAt == 0 {
			rec.UpdatedAt = now
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO memories (id, scope, workspace_path, category, title, content, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		`, rec.ID, rec.Scope, rec.WorkspacePath, rec.Category, rec.Title, rec.Content, rec.CreatedAt, rec.UpdatedAt); err != nil {
			return nil, err
		}
		saved = append(saved, rec)
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return saved, nil
}
