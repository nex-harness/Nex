package store

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

type ArchivedSessionRecord struct {
	SessionRecord
	ArchivedAt int64 `json:"archived_at"`
}

func escapeSQLString(s string) string {
	return strings.ReplaceAll(s, "'", "''")
}

// ArchiveSession atomically migrates sessionID (and all child subagent sessions) from main.db into archive.db,
// removing them from main.db while leaving usage_rollups_hourly untouched.
func (s *Store) ArchiveSession(ctx context.Context, sessionID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	attachStmt := fmt.Sprintf("ATTACH DATABASE '%s' AS cold_archive", escapeSQLString(s.archivePath))
	if _, err := s.mainDB.ExecContext(ctx, attachStmt); err != nil {
		return fmt.Errorf("store: attach archive.db: %w", err)
	}
	defer func() {
		_, _ = s.mainDB.ExecContext(context.Background(), "DETACH DATABASE cold_archive")
	}()

	tx, err := s.mainDB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	now := time.Now().UnixMilli()
	res, err := tx.ExecContext(ctx, `
		INSERT OR REPLACE INTO cold_archive.sessions (
			id, runner_id, parent_session_id, parent_tool_call_id, depth, title, workspace_path,
			agent_profile_slug, model_id, provider_id, thinking_effort, permission_mode, status,
			latest_context_tokens, context_window_limit, created_at, updated_at, last_active_at, archived_at
		)
		SELECT id, runner_id, parent_session_id, parent_tool_call_id, depth, title, workspace_path,
		       agent_profile_slug, model_id, provider_id, thinking_effort, permission_mode, 'archived',
		       latest_context_tokens, context_window_limit, created_at, updated_at, last_active_at, ?
		FROM main.sessions
		WHERE id = ? OR parent_session_id = ?
	`, now, sessionID, sessionID)
	if err != nil {
		return fmt.Errorf("store: copy sessions to archive: %w", err)
	}
	affected, _ := res.RowsAffected()
	if affected == 0 {
		return fmt.Errorf("store: session %s not found in main.db", sessionID)
	}

	if _, err := tx.ExecContext(ctx, `
		INSERT OR REPLACE INTO cold_archive.session_events
		SELECT * FROM main.session_events
		WHERE session_id IN (SELECT id FROM main.sessions WHERE id = ? OR parent_session_id = ?)
	`, sessionID, sessionID); err != nil {
		return fmt.Errorf("store: copy events to archive: %w", err)
	}

	if _, err := tx.ExecContext(ctx, `
		INSERT OR REPLACE INTO cold_archive.turn_telemetry
		SELECT * FROM main.turn_telemetry
		WHERE session_id IN (SELECT id FROM main.sessions WHERE id = ? OR parent_session_id = ?)
	`, sessionID, sessionID); err != nil {
		return fmt.Errorf("store: copy telemetry to archive: %w", err)
	}

	// Remove from hot main.db (usage_rollups_hourly remains intact!)
	if _, err := tx.ExecContext(ctx, `
		DELETE FROM main.turn_telemetry WHERE session_id IN (SELECT id FROM main.sessions WHERE id = ? OR parent_session_id = ?)
	`, sessionID, sessionID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `
		DELETE FROM main.session_events WHERE session_id IN (SELECT id FROM main.sessions WHERE id = ? OR parent_session_id = ?)
	`, sessionID, sessionID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `
		DELETE FROM main.sessions WHERE id = ? OR parent_session_id = ?
	`, sessionID, sessionID); err != nil {
		return err
	}

	if err := tx.Commit(); err != nil {
		return err
	}
	_, _ = s.mainDB.ExecContext(ctx, `PRAGMA incremental_vacuum(64)`)
	return nil
}

// UnarchiveSession atomically restores sessionID (and any child subagent sessions) from archive.db back into main.db.
func (s *Store) UnarchiveSession(ctx context.Context, sessionID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	attachStmt := fmt.Sprintf("ATTACH DATABASE '%s' AS cold_archive", escapeSQLString(s.archivePath))
	if _, err := s.mainDB.ExecContext(ctx, attachStmt); err != nil {
		return fmt.Errorf("store: attach archive.db: %w", err)
	}
	defer func() {
		_, _ = s.mainDB.ExecContext(context.Background(), "DETACH DATABASE cold_archive")
	}()

	tx, err := s.mainDB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	now := time.Now().UnixMilli()
	res, err := tx.ExecContext(ctx, `
		INSERT OR REPLACE INTO main.sessions (
			id, runner_id, parent_session_id, parent_tool_call_id, depth, title, workspace_path,
			agent_profile_slug, model_id, provider_id, thinking_effort, permission_mode, status,
			latest_context_tokens, context_window_limit, created_at, updated_at, last_active_at
		)
		SELECT id, runner_id, parent_session_id, parent_tool_call_id, depth, title, workspace_path,
		       agent_profile_slug, model_id, provider_id, thinking_effort, permission_mode, 'idle',
		       latest_context_tokens, context_window_limit, created_at, ?, ?
		FROM cold_archive.sessions
		WHERE id = ? OR parent_session_id = ?
	`, now, now, sessionID, sessionID)
	if err != nil {
		return fmt.Errorf("store: restore sessions from archive: %w", err)
	}
	affected, _ := res.RowsAffected()
	if affected == 0 {
		return fmt.Errorf("store: archived session %s not found", sessionID)
	}

	if _, err := tx.ExecContext(ctx, `
		INSERT OR REPLACE INTO main.session_events
		SELECT * FROM cold_archive.session_events
		WHERE session_id IN (SELECT id FROM cold_archive.sessions WHERE id = ? OR parent_session_id = ?)
	`, sessionID, sessionID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT OR REPLACE INTO main.turn_telemetry
		SELECT * FROM cold_archive.turn_telemetry
		WHERE session_id IN (SELECT id FROM cold_archive.sessions WHERE id = ? OR parent_session_id = ?)
	`, sessionID, sessionID); err != nil {
		return err
	}

	if _, err := tx.ExecContext(ctx, `
		DELETE FROM cold_archive.turn_telemetry WHERE session_id IN (SELECT id FROM cold_archive.sessions WHERE id = ? OR parent_session_id = ?)
	`, sessionID, sessionID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `
		DELETE FROM cold_archive.session_events WHERE session_id IN (SELECT id FROM cold_archive.sessions WHERE id = ? OR parent_session_id = ?)
	`, sessionID, sessionID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `
		DELETE FROM cold_archive.sessions WHERE id = ? OR parent_session_id = ?
	`, sessionID, sessionID); err != nil {
		return err
	}

	return tx.Commit()
}

// ListArchivedSessions lists root sessions stored in cold archive.db.
func (s *Store) ListArchivedSessions(ctx context.Context, limit, offset int) ([]ArchivedSessionRecord, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := s.archiveDB.QueryContext(ctx, `
		SELECT id, runner_id, parent_session_id, parent_tool_call_id, depth, title, workspace_path,
		       agent_profile_slug, model_id, provider_id, thinking_effort, permission_mode, status,
		       latest_context_tokens, context_window_limit, created_at, updated_at, last_active_at, archived_at
		FROM sessions WHERE parent_session_id = '' ORDER BY archived_at DESC LIMIT ? OFFSET ?
	`, limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ArchivedSessionRecord
	for rows.Next() {
		var r ArchivedSessionRecord
		if err := rows.Scan(&r.ID, &r.RunnerID, &r.ParentSessionID, &r.ParentToolCallID, &r.Depth, &r.Title, &r.WorkspacePath,
			&r.AgentProfileSlug, &r.ModelID, &r.ProviderID, &r.ThinkingEffort, &r.PermissionMode, &r.Status,
			&r.LatestContextTokens, &r.ContextWindowLimit, &r.CreatedAt, &r.UpdatedAt, &r.LastActiveAt, &r.ArchivedAt); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// GetArchivedSessionEvents reads events of an archived session for read-only inspection.
func (s *Store) GetArchivedSessionEvents(ctx context.Context, sessionID string) ([]SessionEvent, error) {
	return queryEventsFromDB(ctx, s.archiveDB, sessionID, 0)
}

// DeleteArchivedSessionPermanently deletes an archived session (and child sessions) from archive.db
// and decrements CAS blob reference counts so unreferenced .zst files are reclaimed.
func (s *Store) DeleteArchivedSessionPermanently(ctx context.Context, sessionID string) error {
	var allBlobHashes []string

	// Collect event blob references
	evRows, err := s.archiveDB.QueryContext(ctx, `
		SELECT blob_hashes_json FROM session_events
		WHERE session_id IN (SELECT id FROM sessions WHERE id = ? OR parent_session_id = ?)
	`, sessionID, sessionID)
	if err != nil {
		return err
	}
	for evRows.Next() {
		var raw string
		if err := evRows.Scan(&raw); err == nil {
			var hs []string
			_ = json.Unmarshal([]byte(raw), &hs)
			allBlobHashes = append(allBlobHashes, hs...)
		}
	}
	evRows.Close()

	// Collect telemetry blob references
	telRows, err := s.archiveDB.QueryContext(ctx, `
		SELECT breakdown_json, system_prompt_blob_hash, raw_trace_blob_hash FROM turn_telemetry
		WHERE session_id IN (SELECT id FROM sessions WHERE id = ? OR parent_session_id = ?)
	`, sessionID, sessionID)
	if err != nil {
		return err
	}
	for telRows.Next() {
		var bdStr, sysH, rawH string
		if err := telRows.Scan(&bdStr, &sysH, &rawH); err == nil {
			if sysH != "" {
				allBlobHashes = append(allBlobHashes, sysH)
			}
			if rawH != "" {
				allBlobHashes = append(allBlobHashes, rawH)
			}
			var items []ContextCategoryItem
			_ = json.Unmarshal([]byte(bdStr), &items)
			for _, it := range items {
				if it.BlobHash != "" {
					allBlobHashes = append(allBlobHashes, it.BlobHash)
				}
			}
		}
	}
	telRows.Close()

	tx, err := s.archiveDB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := tx.ExecContext(ctx, `
		DELETE FROM turn_telemetry WHERE session_id IN (SELECT id FROM sessions WHERE id = ? OR parent_session_id = ?)
	`, sessionID, sessionID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `
		DELETE FROM session_events WHERE session_id IN (SELECT id FROM sessions WHERE id = ? OR parent_session_id = ?)
	`, sessionID, sessionID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM sessions WHERE id = ? OR parent_session_id = ?`, sessionID, sessionID); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}

	if len(allBlobHashes) > 0 {
		return s.DecrefBlobs(ctx, allBlobHashes)
	}
	return nil
}

// ArchiveIdleSessions automatically archives idle root sessions older than idleThreshold relative to now.
func (s *Store) ArchiveIdleSessions(ctx context.Context, idleThreshold time.Duration, now time.Time) (int, error) {
	if idleThreshold <= 0 {
		return 0, nil
	}
	cutoffMS := now.Add(-idleThreshold).UnixMilli()
	rows, err := s.mainDB.QueryContext(ctx, `
		SELECT id FROM sessions
		WHERE parent_session_id = '' AND status != 'running' AND last_active_at < ?
	`, cutoffMS)
	if err != nil {
		return 0, err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err == nil {
			ids = append(ids, id)
		}
	}
	rows.Close()

	archived := 0
	for _, id := range ids {
		if err := s.ArchiveSession(ctx, id); err == nil {
			archived++
		}
	}
	return archived, nil
}
