package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/nex-harness/nex/pkg/ai"
	_ "modernc.org/sqlite"
)

// Store manages main.db (hot), archive.db (cold), and content-addressable Zstd blobs.
type Store struct {
	dataDir     string
	mainPath    string
	archivePath string
	blobsDir    string
	mainDB      *sql.DB
	archiveDB   *sql.DB
	mu          sync.Mutex
}

// Open initializes the dual-SQLite + CAS blob store inside dataDir with 0700/0600 permissions.
func Open(dataDir string) (*Store, error) {
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return nil, fmt.Errorf("store: mkdir dataDir: %w", err)
	}
	blobsDir := filepath.Join(dataDir, "blobs")
	if err := os.MkdirAll(blobsDir, 0o700); err != nil {
		return nil, fmt.Errorf("store: mkdir blobsDir: %w", err)
	}

	mainPath := filepath.Join(dataDir, "main.db")
	archivePath := filepath.Join(dataDir, "archive.db")

	mainDB, err := openSQLite(mainPath, mainSchemaSQL)
	if err != nil {
		return nil, fmt.Errorf("store: init main.db: %w", err)
	}
	archiveDB, err := openSQLite(archivePath, archiveSchemaSQL)
	if err != nil {
		_ = mainDB.Close()
		return nil, fmt.Errorf("store: init archive.db: %w", err)
	}

	_ = os.Chmod(mainPath, 0o600)
	_ = os.Chmod(archivePath, 0o600)

	// Compact any legacy per-token transient delta rows so historical sessions open instantaneously.
	_, _ = mainDB.Exec(`DELETE FROM session_events WHERE event_type IN ('message_update', 'tool_execution_update')`)
	// Idempotent migrations for custom per-model reasoning levels and wire effort maps
	_, _ = mainDB.Exec(`ALTER TABLE models ADD COLUMN reasoning_levels_json TEXT NOT NULL DEFAULT ''`)
	_, _ = mainDB.Exec(`ALTER TABLE models ADD COLUMN reasoning_map_json TEXT NOT NULL DEFAULT ''`)

	return &Store{
		dataDir:     dataDir,
		mainPath:    mainPath,
		archivePath: archivePath,
		blobsDir:    blobsDir,
		mainDB:      mainDB,
		archiveDB:   archiveDB,
	}, nil
}

func openSQLite(path, ddl string) (*sql.DB, error) {
	dsn := fmt.Sprintf("file:%s?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)&_pragma=foreign_keys(ON)&_pragma=auto_vacuum(INCREMENTAL)", filepath.ToSlash(path))
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	// Single connection pool avoids SQLITE_BUSY write contention across concurrent goroutines.
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	if _, err := db.Exec(ddl); err != nil {
		_ = db.Close()
		return nil, err
	}
	return db, nil
}

// Close closes both hot and cold SQLite handles.
func (s *Store) Close() error {
	e1 := s.mainDB.Close()
	e2 := s.archiveDB.Close()
	if e1 != nil {
		return e1
	}
	return e2
}

// DataDir returns the underlying directory path.
func (s *Store) DataDir() string { return s.dataDir }

// --- Preferences & Config Revision ---

type UserPreferences struct {
	Theme                string `json:"theme"`                   // "dark" | "light" | "system"
	CodeTheme            string `json:"code_theme"`              // e.g. "github-dark"
	FontFamily           string `json:"font_family"`             // UI body font
	MonoFontFamily       string `json:"mono_font_family"`        // Code & terminal font
	ExpandThinking       bool   `json:"expand_thinking"`         // Auto-expand thinking blocks
	AutoArchiveAfterDays int    `json:"auto_archive_after_days"` // 0 = disabled
	MemoryAutoExtract    bool   `json:"memory_auto_extract"`     // Background auto-extract after turns
	MemoryProviderID     string `json:"memory_provider_id"`      // Optional dedicated provider for extract/dream
	MemoryModelID        string `json:"memory_model_id"`         // Optional dedicated fast model for extract/dream
}

func DefaultUserPreferences() UserPreferences {
	return UserPreferences{
		Theme:                "dark",
		CodeTheme:            "github-dark",
		FontFamily:           "Inter, system-ui, sans-serif",
		MonoFontFamily:       "JetBrains Mono, Menlo, monospace",
		ExpandThinking:       false,
		AutoArchiveAfterDays: 30,
		MemoryAutoExtract:    false,
		MemoryProviderID:     "",
		MemoryModelID:        "",
	}
}

func (s *Store) GetUserPreferences(ctx context.Context) (UserPreferences, error) {
	var raw string
	err := s.mainDB.QueryRowContext(ctx, `SELECT value_json FROM kv_settings WHERE key = 'user_preferences'`).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return DefaultUserPreferences(), nil
	}
	if err != nil {
		return UserPreferences{}, err
	}
	prefs := DefaultUserPreferences()
	if err := json.Unmarshal([]byte(raw), &prefs); err != nil {
		return prefs, err
	}
	return prefs, nil
}

func (s *Store) SaveUserPreferences(ctx context.Context, prefs UserPreferences) error {
	b, err := json.Marshal(prefs)
	if err != nil {
		return err
	}
	_, err = s.mainDB.ExecContext(ctx, `
		INSERT INTO kv_settings (key, value_json, updated_at) VALUES ('user_preferences', ?, ?)
		ON CONFLICT(key) DO UPDATE SET value_json = excluded.value_json, updated_at = excluded.updated_at
	`, string(b), time.Now().UnixMilli())
	return err
}

func (s *Store) BumpConfigRev(ctx context.Context) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rev, _ := s.GetConfigRev(ctx)
	rev++
	_, err := s.mainDB.ExecContext(ctx, `
		INSERT INTO kv_settings (key, value_json, updated_at) VALUES ('config_rev', ?, ?)
		ON CONFLICT(key) DO UPDATE SET value_json = excluded.value_json, updated_at = excluded.updated_at
	`, fmt.Sprintf("%d", rev), time.Now().UnixMilli())
	return rev, err
}

func (s *Store) GetConfigRev(ctx context.Context) (int64, error) {
	var raw string
	err := s.mainDB.QueryRowContext(ctx, `SELECT value_json FROM kv_settings WHERE key = 'config_rev'`).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	var rev int64
	_, _ = fmt.Sscanf(raw, "%d", &rev)
	return rev, nil
}

// GetKVSetting returns a raw JSON/string setting stored in kv_settings.
func (s *Store) GetKVSetting(ctx context.Context, key string) (string, error) {
	var raw string
	err := s.mainDB.QueryRowContext(ctx, `SELECT value_json FROM kv_settings WHERE key = ?`, key).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return raw, err
}

// SetKVSetting upserts a raw JSON/string setting in kv_settings.
func (s *Store) SetKVSetting(ctx context.Context, key, value string) error {
	_, err := s.mainDB.ExecContext(ctx, `
		INSERT INTO kv_settings (key, value_json, updated_at) VALUES (?, ?, ?)
		ON CONFLICT(key) DO UPDATE SET value_json = excluded.value_json, updated_at = excluded.updated_at
	`, key, value, time.Now().UnixMilli())
	return err
}

// --- Providers & Models ---

type ProviderRecord struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	Driver    ai.Driver `json:"driver"`
	BaseURL   string    `json:"base_url"`
	APIKey    string    `json:"api_key"`
	ProxyMode string    `json:"proxy_mode"` // "direct" | "server_proxy"
	Enabled   bool      `json:"enabled"`
	UpdatedAt int64     `json:"updated_at"`
}

func (s *Store) UpsertProvider(ctx context.Context, p ProviderRecord) error {
	if p.ProxyMode == "" {
		p.ProxyMode = "direct"
	}
	if p.UpdatedAt == 0 {
		p.UpdatedAt = time.Now().UnixMilli()
	}
	en := 0
	if p.Enabled {
		en = 1
	}
	_, err := s.mainDB.ExecContext(ctx, `
		INSERT INTO providers (id, name, driver, base_url, api_key, proxy_mode, enabled, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			name=excluded.name, driver=excluded.driver, base_url=excluded.base_url,
			api_key=excluded.api_key, proxy_mode=excluded.proxy_mode, enabled=excluded.enabled, updated_at=excluded.updated_at
	`, p.ID, p.Name, string(p.Driver), p.BaseURL, p.APIKey, p.ProxyMode, en, p.UpdatedAt)
	if err == nil {
		_, _ = s.BumpConfigRev(ctx)
	}
	return err
}

func (s *Store) GetProvider(ctx context.Context, id string) (*ProviderRecord, error) {
	var p ProviderRecord
	var drv string
	var en int
	err := s.mainDB.QueryRowContext(ctx, `SELECT id, name, driver, base_url, api_key, proxy_mode, enabled, updated_at FROM providers WHERE id = ?`, id).
		Scan(&p.ID, &p.Name, &drv, &p.BaseURL, &p.APIKey, &p.ProxyMode, &en, &p.UpdatedAt)
	if err != nil {
		return nil, err
	}
	p.Driver = ai.Driver(drv)
	p.Enabled = en == 1
	return &p, nil
}

func (s *Store) ListProviders(ctx context.Context) ([]ProviderRecord, error) {
	rows, err := s.mainDB.QueryContext(ctx, `SELECT id, name, driver, base_url, api_key, proxy_mode, enabled, updated_at FROM providers ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ProviderRecord
	for rows.Next() {
		var p ProviderRecord
		var drv string
		var en int
		if err := rows.Scan(&p.ID, &p.Name, &drv, &p.BaseURL, &p.APIKey, &p.ProxyMode, &en, &p.UpdatedAt); err != nil {
			return nil, err
		}
		p.Driver = ai.Driver(drv)
		p.Enabled = en == 1
		out = append(out, p)
	}
	return out, rows.Err()
}

// UpsertModelsBatch inserts or updates models. If overwriteCustom is false, rows with source='custom' are preserved.
func (s *Store) UpsertModelsBatch(ctx context.Context, models []ai.ModelMeta, overwriteCustom bool) error {
	tx, err := s.mainDB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	for _, m := range models {
		src := m.Source
		if src == "" {
			src = "models.dev"
		}
		if !overwriteCustom && src != "custom" {
			var existingSrc string
			err := tx.QueryRowContext(ctx, `SELECT source FROM models WHERE provider_id = ? AND id = ?`, m.ProviderID, m.ID).Scan(&existingSrc)
			if err == nil && existingSrc == "custom" {
				continue
			}
		}
		levelsJSON := ""
		if len(m.ReasoningLevels) > 0 {
			if b, jErr := json.Marshal(m.ReasoningLevels); jErr == nil {
				levelsJSON = string(b)
			}
		}
		mapJSON := ""
		if len(m.ReasoningMap) > 0 {
			if b, jErr := json.Marshal(m.ReasoningMap); jErr == nil {
				mapJSON = string(b)
			}
		}
		_, err := tx.ExecContext(ctx, `
			INSERT INTO models (
				id, provider_id, display_name, source, context_window, max_output_tokens,
				supports_vision, supports_pdf, supports_audio, supports_video, supports_tools, supports_reasoning, supports_caching,
				cost_input_per_1m, cost_output_per_1m, cost_cache_read_per_1m, cost_cache_write_per_1m,
				reasoning_levels_json, reasoning_map_json, enabled
			) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 1)
			ON CONFLICT(provider_id, id) DO UPDATE SET
				display_name=excluded.display_name, source=excluded.source,
				context_window=excluded.context_window, max_output_tokens=excluded.max_output_tokens,
				supports_vision=excluded.supports_vision, supports_pdf=excluded.supports_pdf,
				supports_audio=excluded.supports_audio, supports_video=excluded.supports_video,
				supports_tools=excluded.supports_tools, supports_reasoning=excluded.supports_reasoning,
				supports_caching=excluded.supports_caching,
				cost_input_per_1m=excluded.cost_input_per_1m, cost_output_per_1m=excluded.cost_output_per_1m,
				cost_cache_read_per_1m=excluded.cost_cache_read_per_1m, cost_cache_write_per_1m=excluded.cost_cache_write_per_1m,
				reasoning_levels_json=excluded.reasoning_levels_json, reasoning_map_json=excluded.reasoning_map_json
		`, m.ID, m.ProviderID, m.DisplayName, src, m.ContextWindow, m.MaxOutputTokens,
			b2i(m.SupportsVision), b2i(m.SupportsPDF), b2i(m.SupportsAudio), b2i(m.SupportsVideo),
			b2i(m.SupportsTools), b2i(m.SupportsReasoning), b2i(m.SupportsCaching),
			m.Pricing.InputPer1M, m.Pricing.OutputPer1M, m.Pricing.CacheReadPer1M, m.Pricing.CacheWritePer1M,
			levelsJSON, mapJSON,
		)
		if err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Store) GetModel(ctx context.Context, providerID, modelID string) (*ai.ModelMeta, error) {
	var m ai.ModelMeta
	var vis, pdf, aud, vid, tls, rsn, cch, en int
	var levelsJSON, mapJSON string
	err := s.mainDB.QueryRowContext(ctx, `
		SELECT id, provider_id, display_name, source, context_window, max_output_tokens,
		       supports_vision, supports_pdf, supports_audio, supports_video, supports_tools, supports_reasoning, supports_caching,
		       cost_input_per_1m, cost_output_per_1m, cost_cache_read_per_1m, cost_cache_write_per_1m,
		       reasoning_levels_json, reasoning_map_json, enabled
		FROM models WHERE provider_id = ? AND id = ?
	`, providerID, modelID).Scan(
		&m.ID, &m.ProviderID, &m.DisplayName, &m.Source, &m.ContextWindow, &m.MaxOutputTokens,
		&vis, &pdf, &aud, &vid, &tls, &rsn, &cch,
		&m.Pricing.InputPer1M, &m.Pricing.OutputPer1M, &m.Pricing.CacheReadPer1M, &m.Pricing.CacheWritePer1M,
		&levelsJSON, &mapJSON, &en,
	)
	if err != nil {
		return nil, err
	}
	m.SupportsVision = vis == 1
	m.SupportsPDF = pdf == 1
	m.SupportsAudio = aud == 1
	m.SupportsVideo = vid == 1
	m.SupportsTools = tls == 1
	m.SupportsReasoning = rsn == 1
	m.SupportsCaching = cch == 1
	if levelsJSON != "" {
		_ = json.Unmarshal([]byte(levelsJSON), &m.ReasoningLevels)
	}
	if mapJSON != "" {
		_ = json.Unmarshal([]byte(mapJSON), &m.ReasoningMap)
	}
	m.ReasoningLevels = ai.DefaultReasoningLevels(m)
	return &m, nil
}

func (s *Store) ListModels(ctx context.Context, providerID string) ([]ai.ModelMeta, error) {
	q := `SELECT id, provider_id, display_name, source, context_window, max_output_tokens,
	             supports_vision, supports_pdf, supports_audio, supports_video, supports_tools, supports_reasoning, supports_caching,
	             cost_input_per_1m, cost_output_per_1m, cost_cache_read_per_1m, cost_cache_write_per_1m,
	             reasoning_levels_json, reasoning_map_json
	      FROM models`
	var args []any
	if providerID != "" {
		q += ` WHERE provider_id = ?`
		args = append(args, providerID)
	}
	q += ` ORDER BY provider_id, id`
	rows, err := s.mainDB.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ai.ModelMeta
	for rows.Next() {
		var m ai.ModelMeta
		var vis, pdf, aud, vid, tls, rsn, cch int
		var levelsJSON, mapJSON string
		if err := rows.Scan(&m.ID, &m.ProviderID, &m.DisplayName, &m.Source, &m.ContextWindow, &m.MaxOutputTokens,
			&vis, &pdf, &aud, &vid, &tls, &rsn, &cch,
			&m.Pricing.InputPer1M, &m.Pricing.OutputPer1M, &m.Pricing.CacheReadPer1M, &m.Pricing.CacheWritePer1M,
			&levelsJSON, &mapJSON); err != nil {
			return nil, err
		}
		m.SupportsVision = vis == 1
		m.SupportsPDF = pdf == 1
		m.SupportsAudio = aud == 1
		m.SupportsVideo = vid == 1
		m.SupportsTools = tls == 1
		m.SupportsReasoning = rsn == 1
		m.SupportsCaching = cch == 1
		if levelsJSON != "" {
			_ = json.Unmarshal([]byte(levelsJSON), &m.ReasoningLevels)
		}
		if mapJSON != "" {
			_ = json.Unmarshal([]byte(mapJSON), &m.ReasoningMap)
		}
		m.ReasoningLevels = ai.DefaultReasoningLevels(m)
		out = append(out, m)
	}
	return out, rows.Err()
}

func (s *Store) DeleteModel(ctx context.Context, providerID, id string) error {
	var err error
	if providerID != "" {
		_, err = s.mainDB.ExecContext(ctx, `DELETE FROM models WHERE provider_id = ? AND id = ?`, providerID, id)
	} else {
		_, err = s.mainDB.ExecContext(ctx, `DELETE FROM models WHERE id = ?`, id)
	}
	if err == nil {
		_, _ = s.BumpConfigRev(ctx)
	}
	return err
}

func b2i(v bool) int {
	if v {
		return 1
	}
	return 0
}

func newID(prefix string) string {
	return prefix + "_" + uuid.NewString()
}
