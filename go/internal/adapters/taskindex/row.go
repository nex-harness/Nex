package taskindex

// row.go — rowToMeta: the projection that reconciles meta_json with the scalar
// columns. The reconciliation order is the product rule, not a convenience.

import (
	"database/sql"
	"encoding/json"
	"strings"
)

// workspaceKey is the AGENTS.md identity rule, shared with the rest of the Go
// code: `workspaceIdentity?.trim() || workspacePath`.
func workspaceKey(workspacePath, workspaceIdentity string) string {
	if key := strings.TrimSpace(workspaceIdentity); key != "" {
		return key
	}
	return workspacePath
}

// resolveRowWorkspaceIdentity ports resolveTaskIndexRowWorkspaceIdentity.
//
// workspace_key is the real query/primary-key isolation boundary. When it is a
// well-formed remote identity, the projected identity must equal it: a stale
// workspace_identity column must not project the entity onto a different remote.
// When the two disagree and the key is not remote, no identity is returned —
// falling back to workspacePath would serialize different remotes that share a
// path during the sidebar activity join.
func resolveRowWorkspaceIdentity(row TaskRow) *string {
	columnIdentity := trimOrEmpty(row.WorkspaceIdentity)
	if columnIdentity != "" && columnIdentity == row.WorkspaceKey {
		return &columnIdentity
	}
	if IsRemoteWorkspaceIdentity(row.WorkspaceKey) {
		key := row.WorkspaceKey
		return &key
	}
	return nil
}

// rowToMeta ports rowToMeta. meta_json wins for everything it carries, except
// the fields the scalar columns own (identity projection, unread shell state,
// title override, and the cron/off-peak column fallback).
func rowToMeta(row TaskRow) TaskMeta {
	identity := resolveRowWorkspaceIdentity(row)
	meta := TaskMeta{
		TaskID:            row.TaskID,
		TraceID:           "nex-" + row.TaskID,
		Title:             row.Title,
		TitleOverridden:   row.TitleOverridden == 1,
		WorkspacePath:     row.WorkspacePath,
		WorkspaceIdentity: identity,
		CreatedAt:         row.CreatedAt,
		UpdatedAt:         row.UpdatedAt,
		Mode:              row.Mode,
	}
	if parsed, ok := decodeMetaJSON(row.MetaJSON); ok {
		parsed.TaskID = row.TaskID
		parsed.WorkspacePath = row.WorkspacePath
		parsed.WorkspaceIdentity = identity
		parsed.TitleOverridden = row.TitleOverridden == 1
		// unread is tasks-index product shell state; the scalar column must
		// override a meta_json copied from another Host.
		if row.UnreadAt.Valid {
			unread := row.UnreadAt.Int64
			parsed.UnreadAt = &unread
		} else {
			parsed.UnreadAt = nil
		}
		// cron identity: meta_json is authoritative, the column is only a
		// fallback for legacy rows whose meta predates the field; the next write
		// backfills meta_json.
		if parsed.CronAutomationID == nil {
			parsed.CronAutomationID = nullToPtr(row.CronAutomationID)
		}
		if parsed.OffPeakTaskID == nil {
			parsed.OffPeakTaskID = nullToPtr(row.OffPeakTaskID)
		}
		return *parsed
	}
	// 读取 task index meta_json 非法/失败时按列重建，行仍然可见（状态收敛优先于可读性）。
	meta.Model = nullToPtr(row.Model)
	if row.Provider.Valid && row.Provider.String == NexAgentProvider {
		provider := NexAgentProvider
		meta.Provider = &provider
	}
	meta.MigrationSource = nullToPtr(row.MigrationSource)
	meta.ForkedFromTaskID = nullToPtr(row.ForkedFromTaskID)
	meta.CronAutomationID = nullToPtr(row.CronAutomationID)
	meta.OffPeakTaskID = nullToPtr(row.OffPeakTaskID)
	if row.UnreadAt.Valid {
		unread := row.UnreadAt.Int64
		meta.UnreadAt = &unread
	}
	meta.Status = nullToPtr(row.TaskStatus)
	return meta
}

// decodeMetaJSON is the safeParse of nexTaskMetaSchema: a malformed or
// non-conforming meta_json is discarded and the row falls back to the columns.
func decodeMetaJSON(raw string) (*TaskMeta, bool) {
	var meta TaskMeta
	type wire struct {
		TaskMeta
		Target json.RawMessage `json:"target"`
	}
	var w wire
	if err := json.Unmarshal([]byte(raw), &w); err != nil {
		return nil, false
	}
	meta = w.TaskMeta
	if w.Target != nil {
		meta.SetTarget(w.Target)
	}
	// nonEmptyString / nonneg-int constraints of nexTaskMetaSchema
	if meta.TaskID == "" || meta.TraceID == "" || meta.WorkspacePath == "" {
		return nil, false
	}
	if meta.CreatedAt < 0 || meta.UpdatedAt < 0 {
		return nil, false
	}
	if meta.WorkspaceIdentity != nil && *meta.WorkspaceIdentity == "" {
		return nil, false
	}
	if meta.Mode == "" {
		return nil, false
	}
	return &meta, true
}

func nullToPtr(v sql.NullString) *string {
	if !v.Valid {
		return nil
	}
	s := v.String
	return &s
}
