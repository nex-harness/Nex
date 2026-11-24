package store

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const (
	vaultKeyFileName  = "vault.key"
	vaultCipherPrefix = "v1:"
)

// loadOrCreateVaultKey loads or initializes a 32-byte AES-256 master key inside dataDir (0600).
func loadOrCreateVaultKey(dataDir string) ([]byte, error) {
	keyPath := filepath.Join(dataDir, vaultKeyFileName)
	if raw, err := os.ReadFile(keyPath); err == nil {
		trimmed := strings.TrimSpace(string(raw))
		if decoded, decErr := hex.DecodeString(trimmed); decErr == nil && len(decoded) == 32 {
			_ = os.Chmod(keyPath, 0o600)
			return decoded, nil
		}
	}
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return nil, fmt.Errorf("store: generate vault key: %w", err)
	}
	encoded := hex.EncodeToString(key) + "\n"
	tmpPath := keyPath + ".tmp"
	if err := os.WriteFile(tmpPath, []byte(encoded), 0o600); err != nil {
		return nil, fmt.Errorf("store: write vault key temp: %w", err)
	}
	if err := os.Rename(tmpPath, keyPath); err != nil {
		_ = os.Remove(tmpPath)
		return nil, fmt.Errorf("store: rename vault key: %w", err)
	}
	_ = os.Chmod(keyPath, 0o600)
	return key, nil
}

// EncryptSecret encrypts plaintext with AES-256-GCM using the Store's local vault master key.
func (s *Store) EncryptSecret(plaintext string) (string, error) {
	if plaintext == "" {
		return "", nil
	}
	if len(s.vaultKey) != 32 {
		return "", errors.New("store: vault key not initialized")
	}
	block, err := aes.NewCipher(s.vaultKey)
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	sealed := gcm.Seal(nonce, nonce, []byte(plaintext), nil)
	return vaultCipherPrefix + base64.StdEncoding.EncodeToString(sealed), nil
}

// DecryptSecret decrypts an AES-256-GCM ciphertext produced by EncryptSecret.
func (s *Store) DecryptSecret(ciphertext string) (string, error) {
	if ciphertext == "" {
		return "", nil
	}
	if !strings.HasPrefix(ciphertext, vaultCipherPrefix) {
		return "", errors.New("store: unrecognized secret ciphertext format")
	}
	if len(s.vaultKey) != 32 {
		return "", errors.New("store: vault key not initialized")
	}
	raw, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(ciphertext, vaultCipherPrefix))
	if err != nil {
		return "", err
	}
	block, err := aes.NewCipher(s.vaultKey)
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	nonceSize := gcm.NonceSize()
	if len(raw) < nonceSize {
		return "", errors.New("store: malformed secret ciphertext")
	}
	plain, err := gcm.Open(nil, raw[:nonceSize], raw[nonceSize:], nil)
	if err != nil {
		return "", err
	}
	return string(plain), nil
}

// MaskSecretHint returns a non-reversible safe display hint for a secret value.
func MaskSecretHint(value string) string {
	if value == "" {
		return ""
	}
	runes := []rune(value)
	if len(runes) < 10 {
		return "••••••••"
	}
	return string(runes[:4]) + "••••" + string(runes[len(runes)-4:])
}
