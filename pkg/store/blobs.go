package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/klauspost/compress/zstd"
)

func (s *Store) blobFilePath(hash string) string {
	if len(hash) < 4 {
		return filepath.Join(s.blobsDir, hash+".zst")
	}
	return filepath.Join(s.blobsDir, hash[0:2], hash[2:4], hash+".zst")
}

// PutBlob stores uncompressed data in the Zstd-compressed content-addressable blob store,
// deduplicating by SHA-256 and incrementing ref_count by addRefs.
func (s *Store) PutBlob(ctx context.Context, data []byte, mime string, addRefs int) (string, int, error) {
	if mime == "" {
		mime = "text/plain; charset=utf-8"
	}
	sum := sha256.Sum256(data)
	hash := hex.EncodeToString(sum[:])
	size := len(data)

	targetPath := s.blobFilePath(hash)
	if _, err := os.Stat(targetPath); os.IsNotExist(err) {
		if err := os.MkdirAll(filepath.Dir(targetPath), 0o700); err != nil {
			return "", 0, fmt.Errorf("store: mkdir blob shard: %w", err)
		}
		enc, err := zstd.NewWriter(nil)
		if err != nil {
			return "", 0, fmt.Errorf("store: zstd writer: %w", err)
		}
		compressed := enc.EncodeAll(data, make([]byte, 0, len(data)/2+64))
		_ = enc.Close()

		tmpPath := fmt.Sprintf("%s.%d.tmp", targetPath, time.Now().UnixNano())
		if err := os.WriteFile(tmpPath, compressed, 0o600); err != nil {
			return "", 0, fmt.Errorf("store: write temp blob: %w", err)
		}
		if err := os.Rename(tmpPath, targetPath); err != nil {
			_ = os.Remove(tmpPath)
			if _, statErr := os.Stat(targetPath); statErr != nil {
				return "", 0, fmt.Errorf("store: rename blob: %w", err)
			}
		}
	}

	now := time.Now().UnixMilli()
	_, err := s.mainDB.ExecContext(ctx, `
		INSERT INTO blob_refs (hash, size_bytes, mime, ref_count, created_at)
		VALUES (?, ?, ?, ?, ?)
		ON CONFLICT(hash) DO UPDATE SET ref_count = blob_refs.ref_count + excluded.ref_count
	`, hash, size, mime, addRefs, now)
	if err != nil {
		return "", 0, fmt.Errorf("store: upsert blob_refs: %w", err)
	}
	return hash, size, nil
}

// GetBlob reads and decompresses a content-addressable blob by SHA-256 hash.
func (s *Store) GetBlob(ctx context.Context, hash string) ([]byte, string, error) {
	if hash == "" {
		return nil, "", errors.New("store: empty blob hash")
	}
	var mime string
	err := s.mainDB.QueryRowContext(ctx, `SELECT mime FROM blob_refs WHERE hash = ?`, hash).Scan(&mime)
	if errors.Is(err, sql.ErrNoRows) {
		mime = "text/plain; charset=utf-8"
	} else if err != nil {
		return nil, "", fmt.Errorf("store: query blob_refs: %w", err)
	}

	rawCompressed, err := os.ReadFile(s.blobFilePath(hash))
	if err != nil {
		return nil, "", fmt.Errorf("store: read blob %s: %w", hash, err)
	}
	dec, err := zstd.NewReader(nil)
	if err != nil {
		return nil, "", fmt.Errorf("store: zstd reader: %w", err)
	}
	defer dec.Close()

	decompressed, err := dec.DecodeAll(rawCompressed, nil)
	if err != nil {
		return nil, "", fmt.Errorf("store: decompress blob %s: %w", hash, err)
	}
	return decompressed, mime, nil
}

// HasBlob checks whether a blob exists both in metadata and on disk.
func (s *Store) HasBlob(ctx context.Context, hash string) bool {
	if hash == "" {
		return false
	}
	if _, err := os.Stat(s.blobFilePath(hash)); err != nil {
		return false
	}
	var exists int
	if err := s.mainDB.QueryRowContext(ctx, `SELECT 1 FROM blob_refs WHERE hash = ?`, hash).Scan(&exists); err != nil {
		return false
	}
	return exists == 1
}

// IncrefBlobs increments reference counts for non-empty hashes inside an existing transaction.
func IncrefBlobs(ctx context.Context, tx *sql.Tx, hashes []string) error {
	for _, h := range hashes {
		if h == "" {
			continue
		}
		if _, err := tx.ExecContext(ctx, `UPDATE blob_refs SET ref_count = ref_count + 1 WHERE hash = ?`, h); err != nil {
			return err
		}
	}
	return nil
}

// DecrefBlobs decrements reference counts for hashes and physically deletes orphaned .zst files when ref_count <= 0.
func (s *Store) DecrefBlobs(ctx context.Context, hashes []string) error {
	var toDelete []string

	tx, err := s.mainDB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	for _, h := range hashes {
		if h == "" {
			continue
		}
		if _, err := tx.ExecContext(ctx, `UPDATE blob_refs SET ref_count = ref_count - 1 WHERE hash = ?`, h); err != nil {
			return err
		}
		var rc int
		err := tx.QueryRowContext(ctx, `SELECT ref_count FROM blob_refs WHERE hash = ?`, h).Scan(&rc)
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			return err
		}
		if rc <= 0 {
			if _, err := tx.ExecContext(ctx, `DELETE FROM blob_refs WHERE hash = ?`, h); err != nil {
				return err
			}
			toDelete = append(toDelete, h)
		}
	}

	if err := tx.Commit(); err != nil {
		return err
	}

	for _, h := range toDelete {
		_ = os.Remove(s.blobFilePath(h))
	}
	return nil
}
