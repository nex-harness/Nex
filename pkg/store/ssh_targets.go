package store

import (
	"context"
	"strings"
	"time"

	"github.com/google/uuid"
)

// SSHTargetRecord represents a saved remote SSH server profile for testing, SCP deployment, or remote installation of nex-runner.
type SSHTargetRecord struct {
	ID             string `json:"id"`
	Name           string `json:"name"`
	SSHAlias       string `json:"ssh_alias,omitempty"`
	Host           string `json:"host"`
	Port           int    `json:"port"`
	Username       string `json:"username"`
	AuthMethod     string `json:"auth_method"` // "agent" | "key" | "password"
	KeyPath        string `json:"key_path,omitempty"`
	Password       string `json:"password,omitempty"`
	HasPassword    bool   `json:"has_password,omitempty"`
	WorkspacePath  string `json:"workspace_path,omitempty"`
	InstallDir     string `json:"install_dir,omitempty"`
	ServerWSURL    string `json:"server_ws_url,omitempty"`
	InstallMode    string `json:"install_mode"` // "scp" | "remote_install"
	DownloadURL    string `json:"download_url,omitempty"`
	LastStatus     string `json:"last_status"` // "idle" | "testing" | "deploying" | "connected" | "error"
	LastError      string `json:"last_error,omitempty"`
	LastLog        string `json:"last_log,omitempty"`
	LastDeployedAt int64  `json:"last_deployed_at,omitempty"`
	CreatedAt      int64  `json:"created_at"`
	UpdatedAt      int64  `json:"updated_at"`
}

func (s *Store) UpsertSSHTarget(ctx context.Context, rec SSHTargetRecord) (SSHTargetRecord, error) {
	now := time.Now().UnixMilli()
	if strings.TrimSpace(rec.ID) == "" {
		rec.ID = "ssh_" + uuid.NewString()[:8]
	}
	if rec.Port <= 0 || rec.Port > 65535 {
		rec.Port = 22
	}
	if rec.AuthMethod != "agent" && rec.AuthMethod != "key" && rec.AuthMethod != "password" {
		rec.AuthMethod = "key"
	}
	if rec.InstallMode != "scp" && rec.InstallMode != "remote_install" {
		rec.InstallMode = "scp"
	}
	if strings.TrimSpace(rec.InstallDir) == "" {
		rec.InstallDir = "~/.nex-runner"
	}
	if strings.TrimSpace(rec.LastStatus) == "" {
		rec.LastStatus = "idle"
	}
	if strings.TrimSpace(rec.Name) == "" {
		rec.Name = rec.Host
	}

	// Preserve existing password/status/created_at on partial edit when password is left blank
	if prev, err := s.GetSSHTarget(ctx, rec.ID); err == nil && prev != nil {
		if rec.Password == "" && prev.Password != "" && rec.AuthMethod == "password" {
			rec.Password = prev.Password
		}
		if rec.CreatedAt == 0 {
			rec.CreatedAt = prev.CreatedAt
		}
		if rec.LastLog == "" {
			rec.LastLog = prev.LastLog
		}
		if rec.LastDeployedAt == 0 {
			rec.LastDeployedAt = prev.LastDeployedAt
		}
	}
	if rec.CreatedAt == 0 {
		rec.CreatedAt = now
	}
	rec.UpdatedAt = now

	_, err := s.mainDB.ExecContext(ctx, `
		INSERT INTO ssh_targets (
			id, name, ssh_alias, host, port, username, auth_method, key_path, password,
			workspace_path, install_dir, server_ws_url, install_mode, download_url,
			last_status, last_error, last_log, last_deployed_at, created_at, updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			name = excluded.name,
			ssh_alias = excluded.ssh_alias,
			host = excluded.host,
			port = excluded.port,
			username = excluded.username,
			auth_method = excluded.auth_method,
			key_path = excluded.key_path,
			password = excluded.password,
			workspace_path = excluded.workspace_path,
			install_dir = excluded.install_dir,
			server_ws_url = excluded.server_ws_url,
			install_mode = excluded.install_mode,
			download_url = excluded.download_url,
			last_status = excluded.last_status,
			last_error = excluded.last_error,
			last_log = excluded.last_log,
			last_deployed_at = excluded.last_deployed_at,
			updated_at = excluded.updated_at
	`,
		rec.ID, rec.Name, rec.SSHAlias, rec.Host, rec.Port, rec.Username, rec.AuthMethod, rec.KeyPath, rec.Password,
		rec.WorkspacePath, rec.InstallDir, rec.ServerWSURL, rec.InstallMode, rec.DownloadURL,
		rec.LastStatus, rec.LastError, rec.LastLog, rec.LastDeployedAt, rec.CreatedAt, rec.UpdatedAt,
	)
	rec.HasPassword = rec.Password != ""
	return rec, err
}

func (s *Store) GetSSHTarget(ctx context.Context, id string) (*SSHTargetRecord, error) {
	var r SSHTargetRecord
	err := s.mainDB.QueryRowContext(ctx, `
		SELECT id, name, ssh_alias, host, port, username, auth_method, key_path, password,
		       workspace_path, install_dir, server_ws_url, install_mode, download_url,
		       last_status, last_error, last_log, last_deployed_at, created_at, updated_at
		FROM ssh_targets WHERE id = ?
	`, strings.TrimSpace(id)).Scan(
		&r.ID, &r.Name, &r.SSHAlias, &r.Host, &r.Port, &r.Username, &r.AuthMethod, &r.KeyPath, &r.Password,
		&r.WorkspacePath, &r.InstallDir, &r.ServerWSURL, &r.InstallMode, &r.DownloadURL,
		&r.LastStatus, &r.LastError, &r.LastLog, &r.LastDeployedAt, &r.CreatedAt, &r.UpdatedAt,
	)
	if err != nil {
		return nil, err
	}
	r.HasPassword = r.Password != ""
	return &r, nil
}

func (s *Store) ListSSHTargets(ctx context.Context) ([]SSHTargetRecord, error) {
	rows, err := s.mainDB.QueryContext(ctx, `
		SELECT id, name, ssh_alias, host, port, username, auth_method, key_path, password,
		       workspace_path, install_dir, server_ws_url, install_mode, download_url,
		       last_status, last_error, last_log, last_deployed_at, created_at, updated_at
		FROM ssh_targets ORDER BY updated_at DESC
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make([]SSHTargetRecord, 0)
	for rows.Next() {
		var r SSHTargetRecord
		if err := rows.Scan(
			&r.ID, &r.Name, &r.SSHAlias, &r.Host, &r.Port, &r.Username, &r.AuthMethod, &r.KeyPath, &r.Password,
			&r.WorkspacePath, &r.InstallDir, &r.ServerWSURL, &r.InstallMode, &r.DownloadURL,
			&r.LastStatus, &r.LastError, &r.LastLog, &r.LastDeployedAt, &r.CreatedAt, &r.UpdatedAt,
		); err != nil {
			return nil, err
		}
		r.HasPassword = r.Password != ""
		out = append(out, r)
	}
	return out, rows.Err()
}

func (s *Store) UpdateSSHTargetStatus(ctx context.Context, id, status, lastErr, lastLog string, deployedAt int64) error {
	now := time.Now().UnixMilli()
	if deployedAt > 0 {
		_, err := s.mainDB.ExecContext(ctx, `
			UPDATE ssh_targets SET last_status = ?, last_error = ?, last_log = ?, last_deployed_at = ?, updated_at = ?
			WHERE id = ?
		`, status, lastErr, lastLog, deployedAt, now, id)
		return err
	}
	_, err := s.mainDB.ExecContext(ctx, `
		UPDATE ssh_targets SET last_status = ?, last_error = ?, last_log = ?, updated_at = ?
		WHERE id = ?
	`, status, lastErr, lastLog, now, id)
	return err
}

func (s *Store) DeleteSSHTarget(ctx context.Context, id string) error {
	_, err := s.mainDB.ExecContext(ctx, `DELETE FROM ssh_targets WHERE id = ?`, id)
	return err
}
