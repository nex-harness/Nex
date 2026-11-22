package store

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"

	"github.com/google/uuid"
)

// PairedDevice represents an authorized remote desktop or mobile client trusted by this Server.
type PairedDevice struct {
	ID            string `json:"id"`
	Name          string `json:"name"`
	Platform      string `json:"platform"`
	PubKeyFP      string `json:"pubkey_fp"`
	PubKeyPEM     string `json:"pubkey_pem,omitempty"`
	TokenHash     string `json:"-"`
	CreatedAt     int64  `json:"created_at"`
	LastSeenAt    int64  `json:"last_seen_at"`
	LastTransport string `json:"last_transport"`
	RevokedAt     int64  `json:"revoked_at"`
}

const remoteHostConfigKVKey = "remote_access_config"

// UpsertPairedDevice inserts or re-activates a paired device keyed by pubkey_fp.
func (s *Store) UpsertPairedDevice(ctx context.Context, dev PairedDevice) (PairedDevice, error) {
	now := time.Now().UnixMilli()
	if strings.TrimSpace(dev.ID) == "" {
		// Preserve existing device ID if pubkey_fp was previously paired
		var existingID string
		if err := s.mainDB.QueryRowContext(ctx, `SELECT id FROM paired_devices WHERE pubkey_fp = ?`, dev.PubKeyFP).Scan(&existingID); err == nil && existingID != "" {
			dev.ID = existingID
		} else {
			dev.ID = "dev_" + uuid.NewString()
		}
	}
	if dev.CreatedAt == 0 {
		dev.CreatedAt = now
	}
	if dev.LastSeenAt == 0 {
		dev.LastSeenAt = now
	}
	dev.RevokedAt = 0

	_, err := s.mainDB.ExecContext(ctx, `
		INSERT INTO paired_devices (id, name, platform, pubkey_fp, pubkey_pem, token_hash, created_at, last_seen_at, last_transport, revoked_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, 0)
		ON CONFLICT(pubkey_fp) DO UPDATE SET
			name = excluded.name,
			platform = excluded.platform,
			pubkey_pem = excluded.pubkey_pem,
			token_hash = excluded.token_hash,
			last_seen_at = excluded.last_seen_at,
			last_transport = excluded.last_transport,
			revoked_at = 0`,
		dev.ID, dev.Name, dev.Platform, dev.PubKeyFP, dev.PubKeyPEM, dev.TokenHash, dev.CreatedAt, dev.LastSeenAt, dev.LastTransport,
	)
	return dev, err
}

// ListPairedDevices lists paired devices, optionally including revoked records.
func (s *Store) ListPairedDevices(ctx context.Context, includeRevoked bool) ([]PairedDevice, error) {
	query := `SELECT id, name, platform, pubkey_fp, pubkey_pem, token_hash, created_at, last_seen_at, last_transport, revoked_at
		FROM paired_devices`
	if !includeRevoked {
		query += ` WHERE revoked_at = 0`
	}
	query += ` ORDER BY last_seen_at DESC, created_at DESC`

	rows, err := s.mainDB.QueryContext(ctx, query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []PairedDevice{}
	for rows.Next() {
		var d PairedDevice
		if err := rows.Scan(&d.ID, &d.Name, &d.Platform, &d.PubKeyFP, &d.PubKeyPEM, &d.TokenHash, &d.CreatedAt, &d.LastSeenAt, &d.LastTransport, &d.RevokedAt); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// GetPairedDeviceByFP returns an active (non-revoked) paired device matching pubkeyFP.
func (s *Store) GetPairedDeviceByFP(ctx context.Context, pubkeyFP string) (*PairedDevice, error) {
	var d PairedDevice
	err := s.mainDB.QueryRowContext(ctx, `
		SELECT id, name, platform, pubkey_fp, pubkey_pem, token_hash, created_at, last_seen_at, last_transport, revoked_at
		FROM paired_devices WHERE pubkey_fp = ? AND revoked_at = 0`, strings.ToLower(strings.TrimSpace(pubkeyFP))).
		Scan(&d.ID, &d.Name, &d.Platform, &d.PubKeyFP, &d.PubKeyPEM, &d.TokenHash, &d.CreatedAt, &d.LastSeenAt, &d.LastTransport, &d.RevokedAt)
	if err != nil {
		return nil, err
	}
	return &d, nil
}

// GetPairedDeviceByTokenHash returns an active (non-revoked) paired device matching tokenHash.
func (s *Store) GetPairedDeviceByTokenHash(ctx context.Context, tokenHash string) (*PairedDevice, error) {
	var d PairedDevice
	err := s.mainDB.QueryRowContext(ctx, `
		SELECT id, name, platform, pubkey_fp, pubkey_pem, token_hash, created_at, last_seen_at, last_transport, revoked_at
		FROM paired_devices WHERE token_hash = ? AND revoked_at = 0`, strings.ToLower(strings.TrimSpace(tokenHash))).
		Scan(&d.ID, &d.Name, &d.Platform, &d.PubKeyFP, &d.PubKeyPEM, &d.TokenHash, &d.CreatedAt, &d.LastSeenAt, &d.LastTransport, &d.RevokedAt)
	if err != nil {
		return nil, err
	}
	return &d, nil
}

// TouchPairedDevice updates last_seen_at and last_transport for an active device.
func (s *Store) TouchPairedDevice(ctx context.Context, id, transport string) error {
	now := time.Now().UnixMilli()
	if strings.TrimSpace(transport) != "" {
		_, err := s.mainDB.ExecContext(ctx, `UPDATE paired_devices SET last_seen_at = ?, last_transport = ? WHERE id = ?`, now, transport, id)
		return err
	}
	_, err := s.mainDB.ExecContext(ctx, `UPDATE paired_devices SET last_seen_at = ? WHERE id = ?`, now, id)
	return err
}

// RevokePairedDevice marks a paired device revoked and returns its pubkey_fp so active tunnels can be closed.
func (s *Store) RevokePairedDevice(ctx context.Context, id string) (string, error) {
	var fp string
	err := s.mainDB.QueryRowContext(ctx, `SELECT pubkey_fp FROM paired_devices WHERE id = ?`, id).Scan(&fp)
	if err != nil {
		return "", err
	}
	now := time.Now().UnixMilli()
	if _, err := s.mainDB.ExecContext(ctx, `UPDATE paired_devices SET revoked_at = ? WHERE id = ?`, now, id); err != nil {
		return "", err
	}
	return fp, nil
}

// GetRemoteHostConfigJSON loads raw remote_access_config JSON from kv_settings ("" if unset).
func (s *Store) GetRemoteHostConfigJSON(ctx context.Context) (string, error) {
	var raw string
	err := s.mainDB.QueryRowContext(ctx, `SELECT value_json FROM kv_settings WHERE key = ?`, remoteHostConfigKVKey).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return raw, err
}

// SaveRemoteHostConfigJSON persists raw remote_access_config JSON into kv_settings.
func (s *Store) SaveRemoteHostConfigJSON(ctx context.Context, raw string) error {
	_, err := s.mainDB.ExecContext(ctx, `
		INSERT INTO kv_settings (key, value_json, updated_at) VALUES (?, ?, ?)
		ON CONFLICT(key) DO UPDATE SET value_json = excluded.value_json, updated_at = excluded.updated_at`,
		remoteHostConfigKVKey, raw, time.Now().UnixMilli())
	return err
}
