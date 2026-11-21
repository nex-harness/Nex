package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"time"
)

// SkillVisibilityRule controls whether a discovered skill is visible globally or in specific workspaces.
type SkillVisibilityRule struct {
	GlobalEnabled      *bool           `json:"global_enabled,omitempty"`
	WorkspaceOverrides map[string]bool `json:"workspace_overrides,omitempty"`
}

func NormalizeWorkspaceKey(workspacePath string) string {
	p := strings.TrimSpace(workspacePath)
	if p == "" {
		return ""
	}
	return filepath.Clean(p)
}

// IsSkillVisibleInWorkspace evaluates effective visibility: workspace override > global toggle > default true.
func IsSkillVisibleInWorkspace(rules map[string]SkillVisibilityRule, skillName, workspacePath string) (visible bool, overridden bool) {
	rule, ok := rules[skillName]
	if !ok {
		return true, false
	}
	ws := NormalizeWorkspaceKey(workspacePath)
	if ws != "" && rule.WorkspaceOverrides != nil {
		if v, exists := rule.WorkspaceOverrides[ws]; exists {
			return v, true
		}
	}
	if rule.GlobalEnabled != nil {
		return *rule.GlobalEnabled, false
	}
	return true, false
}

func (s *Store) GetSkillVisibilityRules(ctx context.Context) (map[string]SkillVisibilityRule, error) {
	var raw string
	err := s.mainDB.QueryRowContext(ctx, `SELECT value_json FROM kv_settings WHERE key = 'skill_visibility_rules'`).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return map[string]SkillVisibilityRule{}, nil
	}
	if err != nil {
		return nil, err
	}
	out := map[string]SkillVisibilityRule{}
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		return map[string]SkillVisibilityRule{}, nil
	}
	return out, nil
}

func (s *Store) SaveSkillVisibilityRules(ctx context.Context, rules map[string]SkillVisibilityRule) error {
	if rules == nil {
		rules = map[string]SkillVisibilityRule{}
	}
	b, err := json.Marshal(rules)
	if err != nil {
		return err
	}
	_, err = s.mainDB.ExecContext(ctx, `
		INSERT INTO kv_settings (key, value_json, updated_at) VALUES ('skill_visibility_rules', ?, ?)
		ON CONFLICT(key) DO UPDATE SET value_json = excluded.value_json, updated_at = excluded.updated_at
	`, string(b), time.Now().UnixMilli())
	if err == nil {
		_, _ = s.BumpConfigRev(ctx)
	}
	return err
}

// SetSkillVisibility updates either global default visibility or a per-workspace override.
// Passing enabled == nil with scopeTarget == "workspace" clears the workspace override back to inheriting global.
func (s *Store) SetSkillVisibility(ctx context.Context, skillName, scopeTarget, workspacePath string, enabled *bool) (map[string]SkillVisibilityRule, error) {
	skillName = strings.TrimSpace(skillName)
	if skillName == "" {
		return nil, errors.New("skill name is required")
	}
	rules, err := s.GetSkillVisibilityRules(ctx)
	if err != nil {
		return nil, err
	}
	rule := rules[skillName]
	switch scopeTarget {
	case "workspace":
		ws := NormalizeWorkspaceKey(workspacePath)
		if ws == "" {
			return nil, errors.New("workspace_path is required for workspace visibility override")
		}
		if rule.WorkspaceOverrides == nil {
			rule.WorkspaceOverrides = map[string]bool{}
		}
		if enabled == nil {
			delete(rule.WorkspaceOverrides, ws)
			if len(rule.WorkspaceOverrides) == 0 {
				rule.WorkspaceOverrides = nil
			}
		} else {
			rule.WorkspaceOverrides[ws] = *enabled
		}
	default: // "global"
		if enabled == nil {
			rule.GlobalEnabled = nil
		} else {
			v := *enabled
			rule.GlobalEnabled = &v
		}
	}
	if rule.GlobalEnabled == nil && len(rule.WorkspaceOverrides) == 0 {
		delete(rules, skillName)
	} else {
		rules[skillName] = rule
	}
	if err := s.SaveSkillVisibilityRules(ctx, rules); err != nil {
		return nil, err
	}
	return rules, nil
}

type SkillRecord struct {
	ID            string   `json:"id"`
	Name          string   `json:"name"`
	Description   string   `json:"description"`
	ContentMD     string   `json:"content_md"`
	BundleHash    string   `json:"bundle_hash"`
	Scope         string   `json:"scope"` // "global" | "runner"
	TargetRunners []string `json:"target_runners"`
	Enabled       bool     `json:"enabled"`
	UpdatedAt     int64    `json:"updated_at"`
}

func (s *Store) UpsertSkill(ctx context.Context, r SkillRecord) error {
	if r.ID == "" {
		r.ID = newID("skl")
	}
	if r.Scope == "" {
		r.Scope = "global"
	}
	r.UpdatedAt = time.Now().UnixMilli()
	trJSON, _ := json.Marshal(r.TargetRunners)
	if len(trJSON) == 0 || string(trJSON) == "null" {
		trJSON = []byte("[]")
	}
	_, err := s.mainDB.ExecContext(ctx, `
		INSERT INTO skills_registry (id, name, description, content_md, bundle_hash, scope, target_runners_json, enabled, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(name) DO UPDATE SET
			description=excluded.description, content_md=excluded.content_md, bundle_hash=excluded.bundle_hash,
			scope=excluded.scope, target_runners_json=excluded.target_runners_json, enabled=excluded.enabled, updated_at=excluded.updated_at
	`, r.ID, r.Name, r.Description, r.ContentMD, r.BundleHash, r.Scope, string(trJSON), b2i(r.Enabled), r.UpdatedAt)
	if err == nil {
		_, _ = s.BumpConfigRev(ctx)
	}
	return err
}

func (s *Store) ListSkills(ctx context.Context) ([]SkillRecord, error) {
	rows, err := s.mainDB.QueryContext(ctx, `SELECT id, name, description, content_md, bundle_hash, scope, target_runners_json, enabled, updated_at FROM skills_registry ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []SkillRecord
	for rows.Next() {
		var r SkillRecord
		var trStr string
		var en int
		if err := rows.Scan(&r.ID, &r.Name, &r.Description, &r.ContentMD, &r.BundleHash, &r.Scope, &trStr, &en, &r.UpdatedAt); err != nil {
			return nil, err
		}
		_ = json.Unmarshal([]byte(trStr), &r.TargetRunners)
		r.Enabled = en == 1
		out = append(out, r)
	}
	return out, rows.Err()
}

type PluginRecord struct {
	ID             string   `json:"id"`
	Name           string   `json:"name"`
	SourceTS       string   `json:"source_ts"`
	CompiledJS     string   `json:"compiled_js"`
	CompiledJSHash string   `json:"compiled_js_hash"`
	Scope          string   `json:"scope"`
	TargetRunners  []string `json:"target_runners"`
	Enabled        bool     `json:"enabled"`
	UpdatedAt      int64    `json:"updated_at"`
}

func (s *Store) UpsertPlugin(ctx context.Context, r PluginRecord) error {
	if r.ID == "" {
		r.ID = newID("plg")
	}
	if r.Scope == "" {
		r.Scope = "global"
	}
	r.UpdatedAt = time.Now().UnixMilli()
	trJSON, _ := json.Marshal(r.TargetRunners)
	if len(trJSON) == 0 || string(trJSON) == "null" {
		trJSON = []byte("[]")
	}
	_, err := s.mainDB.ExecContext(ctx, `
		INSERT INTO plugins_registry (id, name, source_ts, compiled_js, compiled_js_hash, scope, target_runners_json, enabled, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(name) DO UPDATE SET
			source_ts=excluded.source_ts, compiled_js=excluded.compiled_js, compiled_js_hash=excluded.compiled_js_hash,
			scope=excluded.scope, target_runners_json=excluded.target_runners_json, enabled=excluded.enabled, updated_at=excluded.updated_at
	`, r.ID, r.Name, r.SourceTS, r.CompiledJS, r.CompiledJSHash, r.Scope, string(trJSON), b2i(r.Enabled), r.UpdatedAt)
	if err == nil {
		_, _ = s.BumpConfigRev(ctx)
	}
	return err
}

func (s *Store) ListPlugins(ctx context.Context) ([]PluginRecord, error) {
	rows, err := s.mainDB.QueryContext(ctx, `SELECT id, name, source_ts, compiled_js, compiled_js_hash, scope, target_runners_json, enabled, updated_at FROM plugins_registry ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []PluginRecord
	for rows.Next() {
		var r PluginRecord
		var trStr string
		var en int
		if err := rows.Scan(&r.ID, &r.Name, &r.SourceTS, &r.CompiledJS, &r.CompiledJSHash, &r.Scope, &trStr, &en, &r.UpdatedAt); err != nil {
			return nil, err
		}
		_ = json.Unmarshal([]byte(trStr), &r.TargetRunners)
		r.Enabled = en == 1
		out = append(out, r)
	}
	return out, rows.Err()
}

type MCPServerRecord struct {
	ID                 string            `json:"id"`
	Name               string            `json:"name"`
	Description        string            `json:"description,omitempty"`
	HostMode           string            `json:"host_mode"` // "runner_local" | "server_hosted"
	Transport          string            `json:"transport"` // "stdio" | "http"
	Command            string            `json:"command"`
	Args               []string          `json:"args"`
	Env                map[string]string `json:"env"`
	URL                string            `json:"url"`
	Headers            map[string]string `json:"headers,omitempty"`
	Exposure           string            `json:"exposure"` // "codemode" | "deferred" | "direct" | "hidden"
	Scope              string            `json:"scope"`    // "global" | "workspace" | "runner"
	WorkspacePath      string            `json:"workspace_path,omitempty"`
	WorkspaceOverrides map[string]bool   `json:"workspace_overrides,omitempty"`
	TargetRunners      []string          `json:"target_runners"`
	IdleTTLSec         int               `json:"idle_ttl_sec"`
	Enabled            bool              `json:"enabled"`
	CatalogCache       json.RawMessage   `json:"catalog_cache"`
	LastStatus         string            `json:"last_status,omitempty"` // "connected" | "disconnected" | "error" | "disabled"
	LastError          string            `json:"last_error,omitempty"`
	LastCheckedAt      int64             `json:"last_checked_at,omitempty"`
	UpdatedAt          int64             `json:"updated_at"`
}

func NormalizeMCPExposure(exp string) string {
	switch strings.ToLower(strings.TrimSpace(exp)) {
	case "codemode", "codemode-deferred", "":
		return "codemode"
	case "deferred":
		return "deferred"
	case "direct":
		return "direct"
	case "hidden":
		return "hidden"
	default:
		return "codemode"
	}
}

// IsMCPVisibleInWorkspace determines whether an MCP server is enabled & visible for a given workspace.
func IsMCPVisibleInWorkspace(r MCPServerRecord, workspacePath string) (visible bool, overridden bool) {
	ws := NormalizeWorkspaceKey(workspacePath)
	if ws != "" && r.WorkspaceOverrides != nil {
		if v, ok := r.WorkspaceOverrides[ws]; ok {
			return v, true
		}
	}
	if r.Scope == "workspace" && strings.TrimSpace(r.WorkspacePath) != "" {
		if ws == "" {
			return false, false
		}
		if NormalizeWorkspaceKey(r.WorkspacePath) != ws {
			return false, false
		}
	}
	return r.Enabled, false
}

func (s *Store) UpsertMCPServer(ctx context.Context, r MCPServerRecord) error {
	if r.ID == "" {
		r.ID = newID("mcp")
	}
	if r.HostMode == "" {
		r.HostMode = "runner_local"
	}
	if r.Transport == "" {
		if strings.TrimSpace(r.URL) != "" && strings.TrimSpace(r.Command) == "" {
			r.Transport = "http"
		} else {
			r.Transport = "stdio"
		}
	}
	if r.Scope == "" {
		r.Scope = "global"
	}
	r.Exposure = NormalizeMCPExposure(r.Exposure)
	r.WorkspacePath = NormalizeWorkspaceKey(r.WorkspacePath)
	if r.IdleTTLSec <= 0 {
		r.IdleTTLSec = 300
	}
	r.UpdatedAt = time.Now().UnixMilli()
	argsJSON, _ := json.Marshal(r.Args)
	if len(argsJSON) == 0 || string(argsJSON) == "null" {
		argsJSON = []byte("[]")
	}
	envJSON, _ := json.Marshal(r.Env)
	if len(envJSON) == 0 || string(envJSON) == "null" {
		envJSON = []byte("{}")
	}
	headersJSON, _ := json.Marshal(r.Headers)
	if len(headersJSON) == 0 || string(headersJSON) == "null" {
		headersJSON = []byte("{}")
	}
	wsOvJSON, _ := json.Marshal(r.WorkspaceOverrides)
	if len(wsOvJSON) == 0 || string(wsOvJSON) == "null" {
		wsOvJSON = []byte("{}")
	}
	trJSON, _ := json.Marshal(r.TargetRunners)
	if len(trJSON) == 0 || string(trJSON) == "null" {
		trJSON = []byte("[]")
	}
	cat := r.CatalogCache
	if len(cat) == 0 {
		cat = []byte("[]")
	}
	_, err := s.mainDB.ExecContext(ctx, `
		INSERT INTO mcp_servers (
			id, name, description, host_mode, transport, command, args_json, env_json, url, headers_json,
			exposure, scope, workspace_path, workspace_overrides_json, target_runners_json, idle_ttl_sec,
			enabled, catalog_cache_json, last_status, last_error, last_checked_at, updated_at
		)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(name) DO UPDATE SET
			description=excluded.description, host_mode=excluded.host_mode, transport=excluded.transport,
			command=excluded.command, args_json=excluded.args_json, env_json=excluded.env_json,
			url=excluded.url, headers_json=excluded.headers_json, exposure=excluded.exposure,
			scope=excluded.scope, workspace_path=excluded.workspace_path,
			workspace_overrides_json=excluded.workspace_overrides_json, target_runners_json=excluded.target_runners_json,
			idle_ttl_sec=excluded.idle_ttl_sec, enabled=excluded.enabled,
			catalog_cache_json=CASE WHEN excluded.catalog_cache_json = '[]' AND mcp_servers.catalog_cache_json != '[]' THEN mcp_servers.catalog_cache_json ELSE excluded.catalog_cache_json END,
			last_status=CASE WHEN excluded.last_status = '' THEN mcp_servers.last_status ELSE excluded.last_status END,
			last_error=CASE WHEN excluded.last_status = '' THEN mcp_servers.last_error ELSE excluded.last_error END,
			last_checked_at=CASE WHEN excluded.last_checked_at = 0 THEN mcp_servers.last_checked_at ELSE excluded.last_checked_at END,
			updated_at=excluded.updated_at
	`, r.ID, r.Name, r.Description, r.HostMode, r.Transport, r.Command, string(argsJSON), string(envJSON),
		r.URL, string(headersJSON), r.Exposure, r.Scope, r.WorkspacePath, string(wsOvJSON), string(trJSON),
		r.IdleTTLSec, b2i(r.Enabled), string(cat), r.LastStatus, r.LastError, r.LastCheckedAt, r.UpdatedAt)
	if err == nil {
		_, _ = s.BumpConfigRev(ctx)
	}
	return err
}

func scanMCPServerRow(scan func(dest ...any) error) (MCPServerRecord, error) {
	var r MCPServerRecord
	var argsS, envS, headersS, wsOvS, trS, catS string
	var en int
	if err := scan(
		&r.ID, &r.Name, &r.Description, &r.HostMode, &r.Transport, &r.Command, &argsS, &envS,
		&r.URL, &headersS, &r.Exposure, &r.Scope, &r.WorkspacePath, &wsOvS, &trS,
		&r.IdleTTLSec, &en, &catS, &r.LastStatus, &r.LastError, &r.LastCheckedAt, &r.UpdatedAt,
	); err != nil {
		return r, err
	}
	_ = json.Unmarshal([]byte(argsS), &r.Args)
	_ = json.Unmarshal([]byte(envS), &r.Env)
	_ = json.Unmarshal([]byte(headersS), &r.Headers)
	_ = json.Unmarshal([]byte(wsOvS), &r.WorkspaceOverrides)
	_ = json.Unmarshal([]byte(trS), &r.TargetRunners)
	r.Exposure = NormalizeMCPExposure(r.Exposure)
	r.CatalogCache = json.RawMessage(catS)
	r.Enabled = en == 1
	return r, nil
}

const mcpSelectColumns = `id, name, description, host_mode, transport, command, args_json, env_json, url, headers_json, exposure, scope, workspace_path, workspace_overrides_json, target_runners_json, idle_ttl_sec, enabled, catalog_cache_json, last_status, last_error, last_checked_at, updated_at`

func (s *Store) GetMCPServer(ctx context.Context, name string) (*MCPServerRecord, error) {
	row := s.mainDB.QueryRowContext(ctx, `SELECT `+mcpSelectColumns+` FROM mcp_servers WHERE name = ?`, name)
	r, err := scanMCPServerRow(row.Scan)
	if err != nil {
		return nil, err
	}
	return &r, nil
}

func (s *Store) ListMCPServers(ctx context.Context) ([]MCPServerRecord, error) {
	rows, err := s.mainDB.QueryContext(ctx, `SELECT `+mcpSelectColumns+` FROM mcp_servers ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []MCPServerRecord
	for rows.Next() {
		r, err := scanMCPServerRow(rows.Scan)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func (s *Store) UpdateMCPServerProbeResult(ctx context.Context, name, status, lastErr string, catalog json.RawMessage) error {
	now := time.Now().UnixMilli()
	if len(catalog) == 0 {
		catalog = []byte("[]")
	}
	_, err := s.mainDB.ExecContext(ctx, `
		UPDATE mcp_servers
		SET catalog_cache_json = CASE WHEN ? != '[]' THEN ? ELSE catalog_cache_json END,
		    last_status = ?, last_error = ?, last_checked_at = ?, updated_at = ?
		WHERE name = ?
	`, string(catalog), string(catalog), status, lastErr, now, now, name)
	if err == nil {
		_, _ = s.BumpConfigRev(ctx)
	}
	return err
}

// SetMCPServerVisibility updates global enabled, a per-workspace override, and/or exposure policy.
func (s *Store) SetMCPServerVisibility(ctx context.Context, name, scopeTarget, workspacePath string, enabled *bool, exposure string) (*MCPServerRecord, error) {
	rec, err := s.GetMCPServer(ctx, name)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(exposure) != "" {
		rec.Exposure = NormalizeMCPExposure(exposure)
	}
	switch scopeTarget {
	case "workspace":
		ws := NormalizeWorkspaceKey(workspacePath)
		if ws == "" {
			return nil, errors.New("workspace_path is required for workspace override")
		}
		if rec.WorkspaceOverrides == nil {
			rec.WorkspaceOverrides = map[string]bool{}
		}
		if enabled == nil {
			delete(rec.WorkspaceOverrides, ws)
			if len(rec.WorkspaceOverrides) == 0 {
				rec.WorkspaceOverrides = nil
			}
		} else {
			rec.WorkspaceOverrides[ws] = *enabled
		}
	case "global":
		if enabled != nil {
			rec.Enabled = *enabled
		}
	}
	if err := s.UpsertMCPServer(ctx, *rec); err != nil {
		return nil, err
	}
	return s.GetMCPServer(ctx, name)
}

type CronJobRecord struct {
	ID             string `json:"id"`
	RunnerID       string `json:"runner_id"`
	Title          string `json:"title"`
	CronExpr       string `json:"cron_expr"`
	Timezone       string `json:"timezone"`
	Prompt         string `json:"prompt"`
	WorkspacePath  string `json:"workspace_path"`
	PermissionMode string `json:"permission_mode"`
	Recurring      bool   `json:"recurring"`
	MaxRuns        int    `json:"max_runs"`
	RunCount       int    `json:"run_count"`
	Enabled        bool   `json:"enabled"`
	LastRunAt      int64  `json:"last_run_at"`
	NextRunAt      int64  `json:"next_run_at"`
	UpdatedAt      int64  `json:"updated_at"`
}

func (s *Store) UpsertCronJob(ctx context.Context, j CronJobRecord) (CronJobRecord, error) {
	if j.ID == "" {
		j.ID = newID("cron")
	}
	if j.Timezone == "" {
		j.Timezone = "Local"
	}
	if j.PermissionMode == "" {
		j.PermissionMode = "autonomous"
	}
	j.UpdatedAt = time.Now().UnixMilli()
	_, err := s.mainDB.ExecContext(ctx, `
		INSERT INTO cron_jobs (id, runner_id, title, cron_expr, timezone, prompt, workspace_path, permission_mode, recurring, max_runs, run_count, enabled, last_run_at, next_run_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			runner_id=excluded.runner_id, title=excluded.title, cron_expr=excluded.cron_expr,
			timezone=excluded.timezone, prompt=excluded.prompt, workspace_path=excluded.workspace_path,
			permission_mode=excluded.permission_mode, recurring=excluded.recurring, max_runs=excluded.max_runs,
			run_count=excluded.run_count, enabled=excluded.enabled, last_run_at=excluded.last_run_at,
			next_run_at=excluded.next_run_at, updated_at=excluded.updated_at
	`, j.ID, j.RunnerID, j.Title, j.CronExpr, j.Timezone, j.Prompt, j.WorkspacePath, j.PermissionMode,
		b2i(j.Recurring), j.MaxRuns, j.RunCount, b2i(j.Enabled), j.LastRunAt, j.NextRunAt, j.UpdatedAt)
	if err == nil {
		_, _ = s.BumpConfigRev(ctx)
	}
	return j, err
}

func (s *Store) DeleteCronJob(ctx context.Context, id string) error {
	_, err := s.mainDB.ExecContext(ctx, `DELETE FROM cron_jobs WHERE id = ?`, id)
	if err == nil {
		_, _ = s.BumpConfigRev(ctx)
	}
	return err
}

func (s *Store) ListCronJobs(ctx context.Context, runnerID string) ([]CronJobRecord, error) {
	q := `SELECT id, runner_id, title, cron_expr, timezone, prompt, workspace_path, permission_mode, recurring, max_runs, run_count, enabled, last_run_at, next_run_at, updated_at FROM cron_jobs`
	var args []any
	if runnerID != "" {
		q += ` WHERE runner_id = ?`
		args = append(args, runnerID)
	}
	q += ` ORDER BY updated_at DESC`
	rows, err := s.mainDB.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []CronJobRecord
	for rows.Next() {
		var j CronJobRecord
		var rec, en int
		if err := rows.Scan(&j.ID, &j.RunnerID, &j.Title, &j.CronExpr, &j.Timezone, &j.Prompt, &j.WorkspacePath, &j.PermissionMode,
			&rec, &j.MaxRuns, &j.RunCount, &en, &j.LastRunAt, &j.NextRunAt, &j.UpdatedAt); err != nil {
			return nil, err
		}
		j.Recurring = rec == 1
		j.Enabled = en == 1
		out = append(out, j)
	}
	return out, rows.Err()
}
