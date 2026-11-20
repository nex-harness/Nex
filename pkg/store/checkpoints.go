package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// FileCheckpoint captures the pre-modification state of a file immediately before a write/edit tool runs.
type FileCheckpoint struct {
	ID         string `json:"id"`
	SessionID  string `json:"session_id"`
	EventSeq   uint64 `json:"event_seq"`
	ToolCallID string `json:"tool_call_id"`
	FilePath   string `json:"file_path"`
	Existed    bool   `json:"existed"`
	BlobHash   string `json:"blob_hash,omitempty"`
	CreatedAt  int64  `json:"created_at"`
}

// RewindResult describes the files restored or deleted during a turn-level file rewind.
type RewindResult struct {
	SessionID     string   `json:"session_id"`
	BeforeSeq     uint64   `json:"before_seq"`
	RestoredFiles []string `json:"restored_files"`
	DeletedFiles  []string `json:"deleted_files"`
	Errors        []string `json:"errors,omitempty"`
}

// LatestSessionSeq returns the highest event seq recorded for sessionID (0 if none).
func (s *Store) LatestSessionSeq(ctx context.Context, sessionID string) (uint64, error) {
	if strings.TrimSpace(sessionID) == "" {
		return 0, nil
	}
	var seq sql.NullInt64
	if err := s.mainDB.QueryRowContext(ctx, `SELECT MAX(seq) FROM session_events WHERE session_id = ?`, sessionID).Scan(&seq); err != nil {
		return 0, err
	}
	if !seq.Valid || seq.Int64 < 0 {
		return 0, nil
	}
	return uint64(seq.Int64), nil
}

// RecordFileCheckpoint snapshots the current on-disk contents (or non-existence) of absPath
// before a write/edit tool executes at eventSeq. Uses ON CONFLICT DO NOTHING so the earliest
// snapshot for (session_id, event_seq, file_path) is preserved.
func (s *Store) RecordFileCheckpoint(ctx context.Context, sessionID string, eventSeq uint64, toolCallID, absPath string) error {
	if strings.TrimSpace(sessionID) == "" || strings.TrimSpace(absPath) == "" {
		return nil
	}
	cleanPath := filepath.Clean(absPath)
	existed := 1
	blobHash := ""

	data, err := os.ReadFile(cleanPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			existed = 0
		} else {
			return fmt.Errorf("store: read file for checkpoint %s: %w", cleanPath, err)
		}
	} else {
		hash, _, bErr := s.PutBlob(ctx, data, "application/octet-stream", 1)
		if bErr != nil {
			return fmt.Errorf("store: put blob for checkpoint %s: %w", cleanPath, bErr)
		}
		blobHash = hash
	}

	now := time.Now().UnixMilli()
	_, err = s.mainDB.ExecContext(ctx, `
		INSERT INTO file_checkpoints (id, session_id, event_seq, tool_call_id, file_path, existed, blob_hash, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(session_id, event_seq, file_path) DO NOTHING
	`, newID("cp"), sessionID, int64(eventSeq), toolCallID, cleanPath, existed, blobHash, now)
	return err
}

// ListFileCheckpoints returns all recorded file checkpoints for sessionID ordered by event_seq ASC.
func (s *Store) ListFileCheckpoints(ctx context.Context, sessionID string) ([]FileCheckpoint, error) {
	rows, err := s.mainDB.QueryContext(ctx, `
		SELECT id, session_id, event_seq, tool_call_id, file_path, existed, blob_hash, created_at
		FROM file_checkpoints
		WHERE session_id = ?
		ORDER BY event_seq ASC, created_at ASC
	`, sessionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []FileCheckpoint
	for rows.Next() {
		var cp FileCheckpoint
		var seq int64
		var existed int
		if err := rows.Scan(&cp.ID, &cp.SessionID, &seq, &cp.ToolCallID, &cp.FilePath, &existed, &cp.BlobHash, &cp.CreatedAt); err != nil {
			return nil, err
		}
		if seq >= 0 {
			cp.EventSeq = uint64(seq)
		}
		cp.Existed = existed != 0
		out = append(out, cp)
	}
	return out, rows.Err()
}

// RewindSessionFiles restores every file modified by sessionID at or after beforeSeq back to its
// exact state immediately prior to its earliest modification >= beforeSeq.
func (s *Store) RewindSessionFiles(ctx context.Context, sessionID string, beforeSeq uint64) (RewindResult, error) {
	res := RewindResult{
		SessionID:     sessionID,
		BeforeSeq:     beforeSeq,
		RestoredFiles: []string{},
		DeletedFiles:  []string{},
	}
	if strings.TrimSpace(sessionID) == "" {
		return res, errors.New("store: session_id is required")
	}

	rows, err := s.mainDB.QueryContext(ctx, `
		SELECT id, session_id, event_seq, tool_call_id, file_path, existed, blob_hash, created_at
		FROM file_checkpoints
		WHERE session_id = ? AND event_seq >= ?
		ORDER BY event_seq ASC, created_at ASC
	`, sessionID, int64(beforeSeq))
	if err != nil {
		return res, err
	}
	defer rows.Close()

	// Keep only the EARLIEST snapshot per distinct file_path >= beforeSeq
	earliestByFile := make(map[string]FileCheckpoint)
	for rows.Next() {
		var cp FileCheckpoint
		var seq int64
		var existed int
		if err := rows.Scan(&cp.ID, &cp.SessionID, &seq, &cp.ToolCallID, &cp.FilePath, &existed, &cp.BlobHash, &cp.CreatedAt); err != nil {
			return res, err
		}
		if seq >= 0 {
			cp.EventSeq = uint64(seq)
		}
		cp.Existed = existed != 0
		if _, seen := earliestByFile[cp.FilePath]; !seen {
			earliestByFile[cp.FilePath] = cp
		}
	}
	if err := rows.Err(); err != nil {
		return res, err
	}
	if len(earliestByFile) == 0 {
		return res, nil
	}

	paths := make([]string, 0, len(earliestByFile))
	for p := range earliestByFile {
		paths = append(paths, p)
	}
	sort.Strings(paths)

	var succeededPaths []string
	for _, p := range paths {
		cp := earliestByFile[p]
		if !cp.Existed {
			if err := os.Remove(cp.FilePath); err != nil && !errors.Is(err, os.ErrNotExist) {
				res.Errors = append(res.Errors, fmt.Sprintf("delete %s: %v", cp.FilePath, err))
				continue
			}
			res.DeletedFiles = append(res.DeletedFiles, cp.FilePath)
			succeededPaths = append(succeededPaths, cp.FilePath)
			continue
		}

		origBytes, _, err := s.GetBlob(ctx, cp.BlobHash)
		if err != nil {
			res.Errors = append(res.Errors, fmt.Sprintf("read snapshot %s: %v", cp.FilePath, err))
			continue
		}
		if err := os.MkdirAll(filepath.Dir(cp.FilePath), 0o755); err != nil {
			res.Errors = append(res.Errors, fmt.Sprintf("mkdir %s: %v", cp.FilePath, err))
			continue
		}
		if err := os.WriteFile(cp.FilePath, origBytes, 0o644); err != nil {
			res.Errors = append(res.Errors, fmt.Sprintf("restore %s: %v", cp.FilePath, err))
			continue
		}
		res.RestoredFiles = append(res.RestoredFiles, cp.FilePath)
		succeededPaths = append(succeededPaths, cp.FilePath)
	}

	for _, p := range succeededPaths {
		_, _ = s.mainDB.ExecContext(ctx, `
			DELETE FROM file_checkpoints WHERE session_id = ? AND event_seq >= ? AND file_path = ?
		`, sessionID, int64(beforeSeq), p)
	}
	return res, nil
}
