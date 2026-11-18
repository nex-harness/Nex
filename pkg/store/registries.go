package store

import (
	"context"
	"encoding/json"
	"time"
)

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
	ID            string            `json:"id"`
	Name          string            `json:"name"`
	HostMode      string            `json:"host_mode"` // "runner_local" | "server_hosted"
	Transport     string            `json:"transport"` // "stdio" | "http"
	Command       string            `json:"command"`
	Args          []string          `json:"args"`
	Env           map[string]string `json:"env"`
	URL           string            `json:"url"`
	Scope         string            `json:"scope"`
	TargetRunners []string          `json:"target_runners"`
	IdleTTLSec    int               `json:"idle_ttl_sec"`
	Enabled       bool              `json:"enabled"`
	CatalogCache  json.RawMessage   `json:"catalog_cache"`
	UpdatedAt     int64             `json:"updated_at"`
}

func (s *Store) UpsertMCPServer(ctx context.Context, r MCPServerRecord) error {
	if r.ID == "" {
		r.ID = newID("mcp")
	}
	if r.HostMode == "" {
		r.HostMode = "runner_local"
	}
	if r.Transport == "" {
		r.Transport = "stdio"
	}
	if r.Scope == "" {
		r.Scope = "global"
	}
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
	trJSON, _ := json.Marshal(r.TargetRunners)
	if len(trJSON) == 0 || string(trJSON) == "null" {
		trJSON = []byte("[]")
	}
	cat := r.CatalogCache
	if len(cat) == 0 {
		cat = []byte("[]")
	}
	_, err := s.mainDB.ExecContext(ctx, `
		INSERT INTO mcp_servers (id, name, host_mode, transport, command, args_json, env_json, url, scope, target_runners_json, idle_ttl_sec, enabled, catalog_cache_json, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(name) DO UPDATE SET
			host_mode=excluded.host_mode, transport=excluded.transport, command=excluded.command,
			args_json=excluded.args_json, env_json=excluded.env_json, url=excluded.url,
			scope=excluded.scope, target_runners_json=excluded.target_runners_json,
			idle_ttl_sec=excluded.idle_ttl_sec, enabled=excluded.enabled,
			catalog_cache_json=excluded.catalog_cache_json, updated_at=excluded.updated_at
	`, r.ID, r.Name, r.HostMode, r.Transport, r.Command, string(argsJSON), string(envJSON), r.URL, r.Scope, string(trJSON), r.IdleTTLSec, b2i(r.Enabled), string(cat), r.UpdatedAt)
	if err == nil {
		_, _ = s.BumpConfigRev(ctx)
	}
	return err
}

func (s *Store) ListMCPServers(ctx context.Context) ([]MCPServerRecord, error) {
	rows, err := s.mainDB.QueryContext(ctx, `SELECT id, name, host_mode, transport, command, args_json, env_json, url, scope, target_runners_json, idle_ttl_sec, enabled, catalog_cache_json, updated_at FROM mcp_servers ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []MCPServerRecord
	for rows.Next() {
		var r MCPServerRecord
		var argsS, envS, trS, catS string
		var en int
		if err := rows.Scan(&r.ID, &r.Name, &r.HostMode, &r.Transport, &r.Command, &argsS, &envS, &r.URL, &r.Scope, &trS, &r.IdleTTLSec, &en, &catS, &r.UpdatedAt); err != nil {
			return nil, err
		}
		_ = json.Unmarshal([]byte(argsS), &r.Args)
		_ = json.Unmarshal([]byte(envS), &r.Env)
		_ = json.Unmarshal([]byte(trS), &r.TargetRunners)
		r.CatalogCache = json.RawMessage(catS)
		r.Enabled = en == 1
		out = append(out, r)
	}
	return out, rows.Err()
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
