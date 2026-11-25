package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"
)

// --- Runners & Agent Profiles & Resource Registries ---

type RunnerRecord struct {
	ID               string   `json:"id"`
	Name             string   `json:"name"`
	Hostname         string   `json:"hostname"`
	OS               string   `json:"os"`
	Arch             string   `json:"arch"`
	Shell            string   `json:"shell"`
	Version          string   `json:"version"`
	Tags             []string `json:"tags"`
	Status           string   `json:"status"` // "online" | "offline"
	LastSeenAt       int64    `json:"last_seen_at"`
	SyncedConfigRev  int64    `json:"synced_config_rev"`
	AuthToken        string   `json:"auth_token,omitempty"`
	WorkspaceDefault string   `json:"workspace_default"`
	CreatedAt        int64    `json:"created_at"`

	// Live runtime / update metadata (enriched in-memory by Server from runner.hello & manifest)
	BuildSHA        string   `json:"build_sha,omitempty"`
	BinarySHA256    string   `json:"binary_sha256,omitempty"`
	ProtocolVersion int      `json:"protocol_version,omitempty"`
	Capabilities    []string `json:"capabilities,omitempty"`
	IsDev           bool     `json:"is_dev,omitempty"`
	Elevated        bool     `json:"elevated,omitempty"`
	UpdateStatus    string   `json:"update_status,omitempty"` // "up_to_date" | "update_available" | "update_required"
	UpdateReason    string   `json:"update_reason,omitempty"`
	TargetVersion   string   `json:"target_version,omitempty"`
	UpdatePhase     string   `json:"update_phase,omitempty"`
	UpdateProgress  int      `json:"update_progress,omitempty"`
	UpdateError     string   `json:"update_error,omitempty"`
	Incompatible    bool     `json:"incompatible,omitempty"`
}

func (s *Store) UpsertRunner(ctx context.Context, r RunnerRecord) error {
	now := time.Now().UnixMilli()
	if r.CreatedAt == 0 {
		r.CreatedAt = now
	}
	if r.Status == "" {
		r.Status = "offline"
	}
	tagsJSON, _ := json.Marshal(r.Tags)
	if len(tagsJSON) == 0 || string(tagsJSON) == "null" {
		tagsJSON = []byte("[]")
	}
	_, err := s.mainDB.ExecContext(ctx, `
		INSERT INTO runners (id, name, hostname, os, arch, shell, version, tags_json, status, last_seen_at, synced_config_rev, auth_token, workspace_default, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			name=excluded.name, hostname=excluded.hostname, os=excluded.os, arch=excluded.arch,
			shell=excluded.shell, version=excluded.version, tags_json=excluded.tags_json,
			status=excluded.status, last_seen_at=excluded.last_seen_at,
			synced_config_rev=excluded.synced_config_rev,
			auth_token=CASE WHEN excluded.auth_token != '' THEN excluded.auth_token ELSE runners.auth_token END,
			workspace_default=CASE WHEN excluded.workspace_default != '' THEN excluded.workspace_default ELSE runners.workspace_default END
	`, r.ID, r.Name, r.Hostname, r.OS, r.Arch, r.Shell, r.Version, string(tagsJSON), r.Status, r.LastSeenAt, r.SyncedConfigRev, r.AuthToken, r.WorkspaceDefault, r.CreatedAt)
	return err
}

func (s *Store) GetRunner(ctx context.Context, id string) (*RunnerRecord, error) {
	var r RunnerRecord
	var tagsStr string
	err := s.mainDB.QueryRowContext(ctx, `
		SELECT id, name, hostname, os, arch, shell, version, tags_json, status, last_seen_at, synced_config_rev, auth_token, workspace_default, created_at
		FROM runners WHERE id = ?
	`, id).Scan(&r.ID, &r.Name, &r.Hostname, &r.OS, &r.Arch, &r.Shell, &r.Version, &tagsStr, &r.Status, &r.LastSeenAt, &r.SyncedConfigRev, &r.AuthToken, &r.WorkspaceDefault, &r.CreatedAt)
	if err != nil {
		return nil, err
	}
	_ = json.Unmarshal([]byte(tagsStr), &r.Tags)
	return &r, nil
}

func (s *Store) ListRunners(ctx context.Context) ([]RunnerRecord, error) {
	rows, err := s.mainDB.QueryContext(ctx, `
		SELECT id, name, hostname, os, arch, shell, version, tags_json, status, last_seen_at, synced_config_rev, auth_token, workspace_default, created_at
		FROM runners ORDER BY created_at ASC
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []RunnerRecord
	for rows.Next() {
		var r RunnerRecord
		var tagsStr string
		if err := rows.Scan(&r.ID, &r.Name, &r.Hostname, &r.OS, &r.Arch, &r.Shell, &r.Version, &tagsStr, &r.Status, &r.LastSeenAt, &r.SyncedConfigRev, &r.AuthToken, &r.WorkspaceDefault, &r.CreatedAt); err != nil {
			return nil, err
		}
		_ = json.Unmarshal([]byte(tagsStr), &r.Tags)
		out = append(out, r)
	}
	return out, rows.Err()
}

// MarkAllRunnersOffline resets any stale 'online' rows left in SQLite from a previous server process.
func (s *Store) MarkAllRunnersOffline(ctx context.Context) error {
	_, err := s.mainDB.ExecContext(ctx, `UPDATE runners SET status = 'offline' WHERE status != 'offline'`)
	return err
}

type AgentProfile struct {
	ID             string   `json:"id"`
	Slug           string   `json:"slug"`
	DisplayName    string   `json:"display_name"`
	Description    string   `json:"description"`
	SystemPrompt   string   `json:"system_prompt"`
	ModelRef       string   `json:"model_ref"`
	ThinkingEffort string   `json:"thinking_effort"`
	ToolsAllow     []string `json:"tools_allow"`
	MaxTurns       int      `json:"max_turns"`
	IsolationMode  string   `json:"isolation_mode"`
	UpdatedAt      int64    `json:"updated_at"`
}

func (s *Store) UpsertAgentProfile(ctx context.Context, p AgentProfile) error {
	if p.ID == "" {
		p.ID = newID("agp")
	}
	if p.MaxTurns <= 0 {
		p.MaxTurns = 40
	}
	if p.IsolationMode == "" {
		p.IsolationMode = "shared"
	}
	p.UpdatedAt = time.Now().UnixMilli()
	toolsJSON, _ := json.Marshal(p.ToolsAllow)
	if len(toolsJSON) == 0 || string(toolsJSON) == "null" {
		toolsJSON = []byte("[]")
	}
	_, err := s.mainDB.ExecContext(ctx, `
		INSERT INTO agent_profiles (id, slug, display_name, description, system_prompt, model_ref, thinking_effort, tools_allow_json, max_turns, isolation_mode, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(slug) DO UPDATE SET
			display_name=excluded.display_name, description=excluded.description,
			system_prompt=excluded.system_prompt, model_ref=excluded.model_ref,
			thinking_effort=excluded.thinking_effort, tools_allow_json=excluded.tools_allow_json,
			max_turns=excluded.max_turns, isolation_mode=excluded.isolation_mode, updated_at=excluded.updated_at
	`, p.ID, p.Slug, p.DisplayName, p.Description, p.SystemPrompt, p.ModelRef, p.ThinkingEffort, string(toolsJSON), p.MaxTurns, p.IsolationMode, p.UpdatedAt)
	if err == nil {
		_, _ = s.BumpConfigRev(ctx)
	}
	return err
}

func (s *Store) ListAgentProfiles(ctx context.Context) ([]AgentProfile, error) {
	rows, err := s.mainDB.QueryContext(ctx, `SELECT id, slug, display_name, description, system_prompt, model_ref, thinking_effort, tools_allow_json, max_turns, isolation_mode, updated_at FROM agent_profiles ORDER BY slug`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []AgentProfile
	for rows.Next() {
		var p AgentProfile
		var toolsStr string
		if err := rows.Scan(&p.ID, &p.Slug, &p.DisplayName, &p.Description, &p.SystemPrompt, &p.ModelRef, &p.ThinkingEffort, &toolsStr, &p.MaxTurns, &p.IsolationMode, &p.UpdatedAt); err != nil {
			return nil, err
		}
		_ = json.Unmarshal([]byte(toolsStr), &p.ToolsAllow)
		out = append(out, p)
	}
	return out, rows.Err()
}

// --- Sessions, Session Events, Outbox & Turn Telemetry ---

type SessionRecord struct {
	ID                  string `json:"id"`
	RunnerID            string `json:"runner_id"`
	ParentSessionID     string `json:"parent_session_id,omitempty"`
	ParentToolCallID    string `json:"parent_tool_call_id,omitempty"`
	Depth               int    `json:"depth"`
	Title               string `json:"title"`
	WorkspacePath       string `json:"workspace_path"`
	AgentProfileSlug    string `json:"agent_profile_slug"`
	ModelID             string `json:"model_id"`
	ProviderID          string `json:"provider_id"`
	ThinkingEffort      string `json:"thinking_effort,omitempty"`
	PermissionMode      string `json:"permission_mode"`
	Status              string `json:"status"`
	LatestContextTokens int    `json:"latest_context_tokens"`
	ContextWindowLimit  int    `json:"context_window_limit"`
	CreatedAt           int64  `json:"created_at"`
	UpdatedAt           int64  `json:"updated_at"`
	LastActiveAt        int64  `json:"last_active_at"`
}

func (s *Store) CreateOrUpdateSession(ctx context.Context, sess SessionRecord) (SessionRecord, error) {
	now := time.Now().UnixMilli()
	if sess.ID == "" {
		sess.ID = newID("sess")
	}
	if sess.AgentProfileSlug == "" {
		sess.AgentProfileSlug = "main"
	}
	if sess.PermissionMode == "" {
		sess.PermissionMode = "interactive"
	}
	if sess.Status == "" {
		sess.Status = "idle"
	}
	if sess.ContextWindowLimit <= 0 {
		sess.ContextWindowLimit = 200000
	}
	if sess.CreatedAt == 0 {
		sess.CreatedAt = now
	}
	if sess.UpdatedAt == 0 {
		sess.UpdatedAt = now
	}
	if sess.LastActiveAt == 0 {
		sess.LastActiveAt = now
	}

	_, err := s.mainDB.ExecContext(ctx, `
		INSERT INTO sessions (
			id, runner_id, parent_session_id, parent_tool_call_id, depth, title, workspace_path,
			agent_profile_slug, model_id, provider_id, thinking_effort, permission_mode, status,
			latest_context_tokens, context_window_limit, created_at, updated_at, last_active_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			title=excluded.title, status=excluded.status,
			model_id=CASE WHEN excluded.model_id != '' THEN excluded.model_id ELSE sessions.model_id END,
			provider_id=CASE WHEN excluded.provider_id != '' THEN excluded.provider_id ELSE sessions.provider_id END,
			thinking_effort=CASE WHEN excluded.thinking_effort != '' THEN excluded.thinking_effort ELSE sessions.thinking_effort END,
			permission_mode=excluded.permission_mode,
			latest_context_tokens=CASE WHEN excluded.latest_context_tokens > 0 THEN excluded.latest_context_tokens ELSE sessions.latest_context_tokens END,
			context_window_limit=CASE WHEN excluded.context_window_limit > 0 THEN excluded.context_window_limit ELSE sessions.context_window_limit END,
			updated_at=excluded.updated_at, last_active_at=excluded.last_active_at
	`, sess.ID, sess.RunnerID, sess.ParentSessionID, sess.ParentToolCallID, sess.Depth, sess.Title, sess.WorkspacePath,
		sess.AgentProfileSlug, sess.ModelID, sess.ProviderID, sess.ThinkingEffort, sess.PermissionMode, sess.Status,
		sess.LatestContextTokens, sess.ContextWindowLimit, sess.CreatedAt, sess.UpdatedAt, sess.LastActiveAt)
	return sess, err
}

func (s *Store) GetSession(ctx context.Context, id string) (*SessionRecord, error) {
	return scanSessionRow(s.mainDB.QueryRowContext(ctx, `
		SELECT id, runner_id, parent_session_id, parent_tool_call_id, depth, title, workspace_path,
		       agent_profile_slug, model_id, provider_id, thinking_effort, permission_mode, status,
		       latest_context_tokens, context_window_limit, created_at, updated_at, last_active_at
		FROM sessions WHERE id = ?
	`, id))
}

// DeleteActiveSession deletes a session (and its child subagent sessions) from main.db and decrements blob refs.
func (s *Store) DeleteActiveSession(ctx context.Context, sessionID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	var hashes []string
	rows, err := s.mainDB.QueryContext(ctx, `
		SELECT blob_hashes_json FROM session_events
		WHERE session_id IN (SELECT id FROM sessions WHERE id = ? OR parent_session_id = ?)
	`, sessionID, sessionID)
	if err == nil {
		for rows.Next() {
			var raw string
			if rows.Scan(&raw) == nil {
				var hs []string
				_ = json.Unmarshal([]byte(raw), &hs)
				hashes = append(hashes, hs...)
			}
		}
		rows.Close()
	}

	tx, err := s.mainDB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	_, _ = tx.ExecContext(ctx, `DELETE FROM turn_telemetry WHERE session_id IN (SELECT id FROM sessions WHERE id = ? OR parent_session_id = ?)`, sessionID, sessionID)
	_, _ = tx.ExecContext(ctx, `DELETE FROM session_events WHERE session_id IN (SELECT id FROM sessions WHERE id = ? OR parent_session_id = ?)`, sessionID, sessionID)
	if _, err := tx.ExecContext(ctx, `DELETE FROM sessions WHERE id = ? OR parent_session_id = ?`, sessionID, sessionID); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	if len(hashes) > 0 {
		_ = s.DecrefBlobs(ctx, hashes)
	}
	return nil
}

// DeleteResourceByName deletes an entry from a named registry table by id or name and bumps config_rev.
func (s *Store) DeleteResourceByName(ctx context.Context, table, keyCol, val string) error {
	allowed := map[string]string{
		"runners":          "id",
		"providers":        "id",
		"agent_profiles":   "slug",
		"skills_registry":  "name",
		"plugins_registry": "name",
		"mcp_servers":      "name",
	}
	col, ok := allowed[table]
	if !ok || (keyCol != "" && keyCol != col) {
		return fmt.Errorf("unsupported registry delete target %s", table)
	}
	_, err := s.mainDB.ExecContext(ctx, fmt.Sprintf(`DELETE FROM %s WHERE %s = ?`, table, col), val)
	if err == nil {
		_, _ = s.BumpConfigRev(ctx)
	}
	return err
}

func (s *Store) ListSessions(ctx context.Context, runnerID string, includeChildren bool) ([]SessionRecord, error) {
	q := `SELECT id, runner_id, parent_session_id, parent_tool_call_id, depth, title, workspace_path,
	             agent_profile_slug, model_id, provider_id, thinking_effort, permission_mode, status,
	             latest_context_tokens, context_window_limit, created_at, updated_at, last_active_at
	      FROM sessions WHERE 1=1`
	var args []any
	if !includeChildren {
		q += ` AND parent_session_id = ''`
	}
	if runnerID != "" {
		q += ` AND runner_id = ?`
		args = append(args, runnerID)
	}
	q += ` ORDER BY last_active_at DESC`
	rows, err := s.mainDB.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []SessionRecord
	for rows.Next() {
		var r SessionRecord
		if err := rows.Scan(&r.ID, &r.RunnerID, &r.ParentSessionID, &r.ParentToolCallID, &r.Depth, &r.Title, &r.WorkspacePath,
			&r.AgentProfileSlug, &r.ModelID, &r.ProviderID, &r.ThinkingEffort, &r.PermissionMode, &r.Status,
			&r.LatestContextTokens, &r.ContextWindowLimit, &r.CreatedAt, &r.UpdatedAt, &r.LastActiveAt); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func scanSessionRow(row *sql.Row) (*SessionRecord, error) {
	var r SessionRecord
	err := row.Scan(&r.ID, &r.RunnerID, &r.ParentSessionID, &r.ParentToolCallID, &r.Depth, &r.Title, &r.WorkspacePath,
		&r.AgentProfileSlug, &r.ModelID, &r.ProviderID, &r.ThinkingEffort, &r.PermissionMode, &r.Status,
		&r.LatestContextTokens, &r.ContextWindowLimit, &r.CreatedAt, &r.UpdatedAt, &r.LastActiveAt)
	if err != nil {
		return nil, err
	}
	return &r, nil
}

type SessionEvent struct {
	SessionID  string          `json:"session_id"`
	Seq        uint64          `json:"seq"`
	TurnIdx    int             `json:"turn_idx"`
	EventType  string          `json:"event_type"`
	Payload    json.RawMessage `json:"payload"`
	BlobHashes []string        `json:"blob_hashes,omitempty"`
	CreatedAt  int64           `json:"created_at"`
}

type OutboxEntry struct {
	ID        int64           `json:"id"`
	SessionID string          `json:"session_id"`
	Seq       uint64          `json:"seq"`
	FrameType string          `json:"frame_type"`
	FrameJSON json.RawMessage `json:"frame_json"`
	CreatedAt int64           `json:"created_at"`
}

// AppendSessionEvent atomically assigns the next monotonic seq (if ev.Seq == 0), persists the event,
// increments referenced blob ref_counts, and optionally enqueues an Outbox entry for offline sync.
func (s *Store) AppendSessionEvent(ctx context.Context, ev SessionEvent, enqueueOutbox bool) (SessionEvent, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	tx, err := s.mainDB.BeginTx(ctx, nil)
	if err != nil {
		return ev, err
	}
	defer func() { _ = tx.Rollback() }()

	var maxSeq sql.NullInt64
	if err := tx.QueryRowContext(ctx, `SELECT MAX(seq) FROM session_events WHERE session_id = ?`, ev.SessionID).Scan(&maxSeq); err != nil {
		return ev, err
	}
	// Guarantee strictly monotonic sequence numbers even if Runner's local seq counter started lower than Server's history!
	if ev.Seq == 0 || ev.Seq <= uint64(maxSeq.Int64) {
		ev.Seq = uint64(maxSeq.Int64) + 1
	}
	if ev.CreatedAt == 0 {
		ev.CreatedAt = time.Now().UnixMilli()
	}
	if len(ev.Payload) == 0 {
		ev.Payload = json.RawMessage(`{}`)
	}
	hashesJSON, _ := json.Marshal(ev.BlobHashes)
	if len(hashesJSON) == 0 || string(hashesJSON) == "null" {
		hashesJSON = []byte("[]")
	}

	_, err = tx.ExecContext(ctx, `
		INSERT INTO session_events (session_id, seq, turn_idx, event_type, payload_json, blob_hashes_json, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(session_id, seq) DO NOTHING
	`, ev.SessionID, ev.Seq, ev.TurnIdx, ev.EventType, string(ev.Payload), string(hashesJSON), ev.CreatedAt)
	if err != nil {
		return ev, err
	}

	if err := IncrefBlobs(ctx, tx, ev.BlobHashes); err != nil {
		return ev, err
	}

	_, _ = tx.ExecContext(ctx, `UPDATE sessions SET updated_at = ?, last_active_at = ? WHERE id = ?`, ev.CreatedAt, ev.CreatedAt, ev.SessionID)

	if enqueueOutbox {
		frameBytes, _ := json.Marshal(ev)
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO outbox_events (session_id, seq, frame_type, frame_json, acked, created_at)
			VALUES (?, ?, 'session.event', ?, 0, ?)
		`, ev.SessionID, ev.Seq, string(frameBytes), ev.CreatedAt); err != nil {
			return ev, err
		}
	}

	if err := tx.Commit(); err != nil {
		return ev, err
	}
	return ev, nil
}

// ListSessionEventsAfter returns all events for sessionID with seq > afterSeq ordered by seq ASC.
func (s *Store) ListSessionEventsAfter(ctx context.Context, sessionID string, afterSeq uint64) ([]SessionEvent, error) {
	return queryEventsFromDB(ctx, s.mainDB, sessionID, afterSeq)
}

// TruncateSessionEventsFrom deletes all session_events with seq >= fromSeq (alongside
// corresponding turn_telemetry rows recorded at or after the truncated cutoff) and refreshes
// latest_context_tokens / updated_at so editing a message rolls back the timeline cleanly.
func (s *Store) TruncateSessionEventsFrom(ctx context.Context, sessionID string, fromSeq uint64) (int64, error) {
	if fromSeq == 0 {
		return 0, nil
	}
	var cutoffMs int64
	_ = s.mainDB.QueryRowContext(
		ctx,
		`SELECT COALESCE(MIN(created_at), 0) FROM session_events WHERE session_id = ? AND seq >= ?`,
		sessionID, fromSeq,
	).Scan(&cutoffMs)

	res, err := s.mainDB.ExecContext(ctx, `DELETE FROM session_events WHERE session_id = ? AND seq >= ?`, sessionID, fromSeq)
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()

	if cutoffMs > 0 {
		_, _ = s.mainDB.ExecContext(
			ctx,
			`DELETE FROM turn_telemetry WHERE session_id = ? AND recorded_at >= ?`,
			sessionID, cutoffMs/1000,
		)
	}
	var latestTokens int
	_ = s.mainDB.QueryRowContext(
		ctx,
		`SELECT COALESCE((SELECT total_context_tokens FROM turn_telemetry WHERE session_id = ? ORDER BY recorded_at DESC, rowid DESC LIMIT 1), 0)`,
		sessionID,
	).Scan(&latestTokens)
	_, _ = s.mainDB.ExecContext(
		ctx,
		`UPDATE sessions SET latest_context_tokens = ?, updated_at = ? WHERE id = ?`,
		latestTokens, time.Now().UnixMilli(), sessionID,
	)
	return n, nil
}

// ForkSession creates a new session copying events from sourceSessionID up to upToSeq
// (if upToSeq == 0, copies all events; if excludeAll is true, copies 0 events).
func (s *Store) ForkSession(ctx context.Context, sourceSessionID string, upToSeq uint64, excludeAll bool, newTitle string) (SessionRecord, []SessionEvent, error) {
	src, err := s.GetSession(ctx, sourceSessionID)
	if err != nil {
		return SessionRecord{}, nil, err
	}
	title := newTitle
	if title == "" {
		base := src.Title
		if base == "" {
			base = "Session"
		}
		title = "branch of " + base
	}

	now := time.Now().UnixMilli()
	forked := SessionRecord{
		ID:                 newID("sess"),
		RunnerID:           src.RunnerID,
		Title:              title,
		WorkspacePath:      src.WorkspacePath,
		AgentProfileSlug:   src.AgentProfileSlug,
		ModelID:            src.ModelID,
		ProviderID:         src.ProviderID,
		PermissionMode:     src.PermissionMode,
		Status:             "idle",
		ContextWindowLimit: src.ContextWindowLimit,
		CreatedAt:          now,
		UpdatedAt:          now,
		LastActiveAt:       now,
	}
	forked, err = s.CreateOrUpdateSession(ctx, forked)
	if err != nil {
		return SessionRecord{}, nil, err
	}

	if excludeAll {
		return forked, nil, nil
	}

	srcEvents, err := s.ListSessionEventsAfter(ctx, sourceSessionID, 0)
	if err != nil {
		return forked, nil, err
	}
	var copied []SessionEvent
	openAgentRun := false
	lastTurnIdx := 0
	for i, ev := range srcEvents {
		if upToSeq > 0 && ev.Seq > upToSeq {
			// Include immediately trailing turn_end / agent_end events that close the forked turn
			if ev.EventType == "turn_end" && (ev.TurnIdx == lastTurnIdx || lastTurnIdx == 0) {
				// allow copying closing turn_end
			} else if ev.EventType == "agent_end" && openAgentRun {
				// allow copying closing agent_end, then stop
			} else {
				break
			}
			_ = i
		}
		saved, aErr := s.AppendSessionEvent(ctx, SessionEvent{
			SessionID:  forked.ID,
			TurnIdx:    ev.TurnIdx,
			EventType:  ev.EventType,
			Payload:    ev.Payload,
			BlobHashes: ev.BlobHashes,
			CreatedAt:  ev.CreatedAt,
		}, false)
		if aErr != nil {
			return forked, copied, aErr
		}
		copied = append(copied, saved)
		if ev.TurnIdx > 0 {
			lastTurnIdx = ev.TurnIdx
		}
		switch ev.EventType {
		case "agent_start":
			openAgentRun = true
		case "agent_end":
			openAgentRun = false
		}
		if upToSeq > 0 && ev.Seq > upToSeq && !openAgentRun {
			break
		}
	}
	// Guarantee a forked session NEVER remains stuck in running/streaming state!
	if openAgentRun {
		savedEnd, aErr := s.AppendSessionEvent(ctx, SessionEvent{
			SessionID: forked.ID,
			TurnIdx:   lastTurnIdx,
			EventType: "agent_end",
			Payload:   json.RawMessage(`{"type":"agent_end"}`),
			CreatedAt: now,
		}, false)
		if aErr == nil {
			copied = append(copied, savedEnd)
		}
	}
	return forked, copied, nil
}

// ImportSessionSnapshot mirrors a forked session and its historical events into local SQLite (used by Runner sync).
func (s *Store) ImportSessionSnapshot(ctx context.Context, sess SessionRecord, events []SessionEvent) error {
	if _, err := s.CreateOrUpdateSession(ctx, sess); err != nil {
		return err
	}
	for _, ev := range events {
		ev.SessionID = sess.ID
		if _, err := s.AppendSessionEvent(ctx, ev, false); err != nil {
			return err
		}
	}
	return nil
}

func queryEventsFromDB(ctx context.Context, db *sql.DB, sessionID string, afterSeq uint64) ([]SessionEvent, error) {
	rows, err := db.QueryContext(ctx, `
		SELECT session_id, seq, turn_idx, event_type, payload_json, blob_hashes_json, created_at
		FROM session_events WHERE session_id = ? AND seq > ? ORDER BY seq ASC
	`, sessionID, afterSeq)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []SessionEvent
	for rows.Next() {
		var ev SessionEvent
		var payloadStr, hashesStr string
		if err := rows.Scan(&ev.SessionID, &ev.Seq, &ev.TurnIdx, &ev.EventType, &payloadStr, &hashesStr, &ev.CreatedAt); err != nil {
			return nil, err
		}
		ev.Payload = json.RawMessage(payloadStr)
		_ = json.Unmarshal([]byte(hashesStr), &ev.BlobHashes)
		out = append(out, ev)
	}
	return out, rows.Err()
}

func (s *Store) ListUnackedOutbox(ctx context.Context, limit int) ([]OutboxEntry, error) {
	if limit <= 0 {
		limit = 200
	}
	rows, err := s.mainDB.QueryContext(ctx, `
		SELECT id, session_id, seq, frame_type, frame_json, created_at
		FROM outbox_events WHERE acked = 0 ORDER BY id ASC LIMIT ?
	`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []OutboxEntry
	for rows.Next() {
		var e OutboxEntry
		var fStr string
		if err := rows.Scan(&e.ID, &e.SessionID, &e.Seq, &e.FrameType, &fStr, &e.CreatedAt); err != nil {
			return nil, err
		}
		e.FrameJSON = json.RawMessage(fStr)
		out = append(out, e)
	}
	return out, rows.Err()
}

func (s *Store) AckOutboxUpTo(ctx context.Context, maxID int64) error {
	_, err := s.mainDB.ExecContext(ctx, `UPDATE outbox_events SET acked = 1 WHERE id <= ?`, maxID)
	return err
}

// ContextCategoryItem represents one of the 6 Context Windows categories in UI & Trace.
type ContextCategoryItem struct {
	Category string `json:"category"` // "messages"|"system_tools"|"system_prompt"|"mcp_tools"|"skills"|"meta_context"
	Bytes    int    `json:"bytes"`
	Tokens   int    `json:"tokens"`
	Pct      float64 `json:"pct"`
	BlobHash string `json:"blob_hash,omitempty"`
}

type TurnTelemetry struct {
	ID                   string                `json:"id"`
	SessionID            string                `json:"session_id"`
	TurnIdx              int                   `json:"turn_idx"`
	RunnerID             string                `json:"runner_id"`
	ProviderID           string                `json:"provider_id"`
	ModelID              string                `json:"model_id"`
	TriggerSource        string                `json:"trigger_source"` // "chat" | "subagent" | "cron"
	ContextWindow        int                   `json:"context_window"`
	TotalContextTokens   int                   `json:"total_context_tokens"`
	WindowUtilizationPct float64               `json:"window_utilization_pct"`
	InputTokens          int                   `json:"input_tokens"`
	OutputTokens         int                   `json:"output_tokens"`
	ReasoningTokens      int                   `json:"reasoning_tokens"`
	CacheReadTokens      int                   `json:"cache_read_tokens"`
	CacheWriteTokens     int                   `json:"cache_write_tokens"`
	CacheHitRatePct      float64               `json:"cache_hit_rate_pct"`
	EstimatedCostUSD     float64               `json:"estimated_cost_usd"`
	CacheSavedUSD        float64               `json:"cache_saved_usd"`
	Breakdown            []ContextCategoryItem `json:"breakdown"`
	SystemPromptBlobHash string                `json:"system_prompt_blob_hash,omitempty"`
	RawTraceBlobHash     string                `json:"raw_trace_blob_hash,omitempty"`
	TTFTMS               int64                 `json:"ttft_ms"`
	DurationMS           int64                 `json:"duration_ms"`
	RecordedAt           int64                 `json:"recorded_at"` // Unix seconds
}

// RecordTurnTelemetry atomically writes turn_telemetry, increments referenced CAS blob ref_counts,
// updates session context utilization, and UPSERTs usage_rollups_hourly!
func (s *Store) RecordTurnTelemetry(ctx context.Context, t TurnTelemetry) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if t.ID == "" {
		t.ID = newID("tel")
	}
	if t.TriggerSource == "" {
		t.TriggerSource = "chat"
	}
	if t.RecordedAt == 0 {
		t.RecordedAt = time.Now().Unix()
	}

	bdBytes, err := json.Marshal(t.Breakdown)
	if err != nil {
		return fmt.Errorf("store: marshal breakdown: %w", err)
	}

	tx, err := s.mainDB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	res, err := tx.ExecContext(ctx, `
		INSERT INTO turn_telemetry (
			id, session_id, turn_idx, runner_id, provider_id, model_id, trigger_source,
			context_window, total_context_tokens, window_utilization_pct,
			input_tokens, output_tokens, reasoning_tokens, cache_read_tokens, cache_write_tokens, cache_hit_rate_pct,
			estimated_cost_usd, cache_saved_usd, breakdown_json, system_prompt_blob_hash, raw_trace_blob_hash,
			ttft_ms, duration_ms, recorded_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO NOTHING
	`, t.ID, t.SessionID, t.TurnIdx, t.RunnerID, t.ProviderID, t.ModelID, t.TriggerSource,
		t.ContextWindow, t.TotalContextTokens, t.WindowUtilizationPct,
		t.InputTokens, t.OutputTokens, t.ReasoningTokens, t.CacheReadTokens, t.CacheWriteTokens, t.CacheHitRatePct,
		t.EstimatedCostUSD, t.CacheSavedUSD, string(bdBytes), t.SystemPromptBlobHash, t.RawTraceBlobHash,
		t.TTFTMS, t.DurationMS, t.RecordedAt)
	if err != nil {
		return err
	}

	inserted, _ := res.RowsAffected()
	if inserted > 0 {
		var refHashes []string
		if t.SystemPromptBlobHash != "" {
			refHashes = append(refHashes, t.SystemPromptBlobHash)
		}
		if t.RawTraceBlobHash != "" {
			refHashes = append(refHashes, t.RawTraceBlobHash)
		}
		for _, item := range t.Breakdown {
			if item.BlobHash != "" {
				refHashes = append(refHashes, item.BlobHash)
			}
		}
		if err := IncrefBlobs(ctx, tx, refHashes); err != nil {
			return err
		}

		_, _ = tx.ExecContext(ctx, `
			UPDATE sessions SET latest_context_tokens = ?, context_window_limit = CASE WHEN ? > 0 THEN ? ELSE context_window_limit END
			WHERE id = ?
		`, t.TotalContextTokens, t.ContextWindow, t.ContextWindow, t.SessionID)

		hourBucket := t.RecordedAt - (t.RecordedAt % 3600)
		_, err = tx.ExecContext(ctx, `
			INSERT INTO usage_rollups_hourly (
				hour_bucket, runner_id, provider_id, model_id, trigger_source,
				turn_count, input_tokens, output_tokens, cache_read_tokens, cache_write_tokens,
				estimated_cost_usd, cache_saved_usd
			) VALUES (?, ?, ?, ?, ?, 1, ?, ?, ?, ?, ?, ?)
			ON CONFLICT(hour_bucket, runner_id, provider_id, model_id, trigger_source) DO UPDATE SET
				turn_count         = usage_rollups_hourly.turn_count + 1,
				input_tokens       = usage_rollups_hourly.input_tokens + excluded.input_tokens,
				output_tokens      = usage_rollups_hourly.output_tokens + excluded.output_tokens,
				cache_read_tokens  = usage_rollups_hourly.cache_read_tokens + excluded.cache_read_tokens,
				cache_write_tokens = usage_rollups_hourly.cache_write_tokens + excluded.cache_write_tokens,
				estimated_cost_usd = usage_rollups_hourly.estimated_cost_usd + excluded.estimated_cost_usd,
				cache_saved_usd    = usage_rollups_hourly.cache_saved_usd + excluded.cache_saved_usd
		`, hourBucket, t.RunnerID, t.ProviderID, t.ModelID, t.TriggerSource,
			t.InputTokens, t.OutputTokens, t.CacheReadTokens, t.CacheWriteTokens,
			t.EstimatedCostUSD, t.CacheSavedUSD)
		if err != nil {
			return err
		}
	}

	return tx.Commit()
}

func (s *Store) ListSessionTelemetry(ctx context.Context, sessionID string) ([]TurnTelemetry, error) {
	rows, err := s.mainDB.QueryContext(ctx, `
		SELECT id, session_id, turn_idx, runner_id, provider_id, model_id, trigger_source,
		       context_window, total_context_tokens, window_utilization_pct,
		       input_tokens, output_tokens, reasoning_tokens, cache_read_tokens, cache_write_tokens, cache_hit_rate_pct,
		       estimated_cost_usd, cache_saved_usd, breakdown_json, system_prompt_blob_hash, raw_trace_blob_hash,
		       ttft_ms, duration_ms, recorded_at
		FROM turn_telemetry WHERE session_id = ? ORDER BY recorded_at ASC, rowid ASC
	`, sessionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []TurnTelemetry
	for rows.Next() {
		var t TurnTelemetry
		var bdStr string
		if err := rows.Scan(&t.ID, &t.SessionID, &t.TurnIdx, &t.RunnerID, &t.ProviderID, &t.ModelID, &t.TriggerSource,
			&t.ContextWindow, &t.TotalContextTokens, &t.WindowUtilizationPct,
			&t.InputTokens, &t.OutputTokens, &t.ReasoningTokens, &t.CacheReadTokens, &t.CacheWriteTokens, &t.CacheHitRatePct,
			&t.EstimatedCostUSD, &t.CacheSavedUSD, &bdStr, &t.SystemPromptBlobHash, &t.RawTraceBlobHash,
			&t.TTFTMS, &t.DurationMS, &t.RecordedAt); err != nil {
			return nil, err
		}
		_ = json.Unmarshal([]byte(bdStr), &t.Breakdown)
		out = append(out, t)
	}
	return out, rows.Err()
}
