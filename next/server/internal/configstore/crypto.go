package configstore

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"sync"
)

// SecretKeySize is the AES-256 key length of `<data>/keys/config.key`.
const SecretKeySize = 32

// ErrSecretIntegrity reports ciphertext that does not authenticate (wrong key,
// tampered data, or a different secret id).
var ErrSecretIntegrity = errors.New("secret integrity check failed")

// SecretBox is the secret store: a 32-byte AES-256-GCM key in
// `<data>/keys/config.key` (0600, created lazily) and the ciphertext map in
// `<data>/config/secrets.enc`.
//
// The key file lives outside the config file so a backup or copy of config.json
// never carries the key. AEAD = AES-256-GCM with the random 12-byte nonce
// prefixed to the ciphertext; the associated data is the secret id, so a
// ciphertext moved to another id fails to open (research §C2/C7).
type SecretBox struct {
	keyPath string
	path    string
	newKey  func() ([]byte, error)
	mu      sync.Mutex
	key     []byte
	loaded  bool
	entries map[string]string // id → base64(nonce‖ciphertext)
}

// NewSecretBox returns a box rooted at dataDir. Nothing is read or created until
// the first operation.
func NewSecretBox(dataDir string) *SecretBox {
	return &SecretBox{
		keyPath: filepath.Join(dataDir, "keys", "config.key"),
		path:    filepath.Join(dataDir, "config", "secrets.enc"),
		newKey:  randomKey,
	}
}

func randomKey() ([]byte, error) {
	key := make([]byte, SecretKeySize)
	if _, err := io.ReadFull(rand.Reader, key); err != nil {
		return nil, fmt.Errorf("generating config key: %w", err)
	}
	return key, nil
}

// Seal encrypts value for id and returns its ciphertext (base64).
func (b *SecretBox) Seal(id, value string) (string, error) {
	aead, err := b.aead()
	if err != nil {
		return "", err
	}
	nonce := make([]byte, aead.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", fmt.Errorf("generating secret nonce: %w", err)
	}
	sealed := aead.Seal(nonce, nonce, []byte(value), []byte(id))
	return base64.StdEncoding.EncodeToString(sealed), nil
}

// Open decrypts ciphertext for id.
func (b *SecretBox) Open(id, ciphertext string) (string, error) {
	aead, err := b.aead()
	if err != nil {
		return "", err
	}
	raw, err := base64.StdEncoding.DecodeString(ciphertext)
	if err != nil {
		return "", ErrSecretIntegrity
	}
	nonceSize := aead.NonceSize()
	if len(raw) < nonceSize+aead.Overhead() {
		return "", ErrSecretIntegrity
	}
	plain, err := aead.Open(nil, raw[:nonceSize], raw[nonceSize:], []byte(id))
	if err != nil {
		return "", ErrSecretIntegrity
	}
	return string(plain), nil
}

// Load reads the ciphertext map (created empty on first use).
func (b *SecretBox) Load() (map[string]string, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if err := b.loadLocked(); err != nil {
		return nil, err
	}
	out := make(map[string]string, len(b.entries))
	for id, value := range b.entries {
		out[id] = value
	}
	return out, nil
}

// Put stores value under id (replacing any previous ciphertext).
func (b *SecretBox) Put(id, value string) error {
	ciphertext, err := b.Seal(id, value)
	if err != nil {
		return err
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if err := b.loadLocked(); err != nil {
		return err
	}
	previous, existed := b.entries[id]
	b.entries[id] = ciphertext
	if err := b.persistLocked(); err != nil {
		if existed {
			b.entries[id] = previous
		} else {
			delete(b.entries, id)
		}
		return err
	}
	return nil
}

// Set stores or deletes (empty value) one secret.
func (b *SecretBox) Set(id, value string) error {
	if value == "" {
		return b.Delete(id)
	}
	return b.Put(id, value)
}

// Delete removes id; a missing id is not an error.
func (b *SecretBox) Delete(id string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if err := b.loadLocked(); err != nil {
		return err
	}
	previous, existed := b.entries[id]
	if !existed {
		return nil
	}
	delete(b.entries, id)
	if err := b.persistLocked(); err != nil {
		b.entries[id] = previous
		return err
	}
	return nil
}

// Get returns the plaintext for id, or ok=false when it is absent.
func (b *SecretBox) Get(id string) (string, bool, error) {
	b.mu.Lock()
	var ciphertext string
	var ok bool
	if err := b.loadLocked(); err != nil {
		b.mu.Unlock()
		return "", false, err
	}
	ciphertext, ok = b.entries[id]
	b.mu.Unlock()
	if !ok {
		return "", false, nil
	}
	plain, err := b.Open(id, ciphertext)
	if err != nil {
		return "", false, err
	}
	return plain, true, nil
}

// Fingerprint is a short, non-reversible hint for the UI (`hasSecret` plus a
// masked tail is built from the plaintext by the caller; this only proves
// presence). It never reveals the value.
func Fingerprint(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:4])
}

func (b *SecretBox) loadLocked() error {
	if b.loaded {
		return nil
	}
	b.entries = map[string]string{}
	raw, err := os.ReadFile(b.path)
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("reading secrets: %w", err)
		}
		b.loaded = true
		return nil
	}
	if len(raw) > 0 {
		var stored struct {
			Schema  int               `json:"schema"`
			Secrets map[string]string `json:"secrets"`
		}
		if err := json.Unmarshal(raw, &stored); err != nil {
			return fmt.Errorf("decoding secrets: %w", err)
		}
		for id, value := range stored.Secrets {
			b.entries[id] = value
		}
	}
	b.loaded = true
	return nil
}

func (b *SecretBox) persistLocked() error {
	if err := os.MkdirAll(filepath.Dir(b.path), 0o700); err != nil {
		return fmt.Errorf("creating config directory: %w", err)
	}
	ids := make([]string, 0, len(b.entries))
	for id := range b.entries {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	ordered := make(map[string]string, len(ids))
	for _, id := range ids {
		ordered[id] = b.entries[id]
	}
	encoded, err := json.MarshalIndent(struct {
		Schema  int               `json:"schema"`
		Secrets map[string]string `json:"secrets"`
	}{Schema: SchemaVersion, Secrets: ordered}, "", "  ")
	if err != nil {
		return fmt.Errorf("encoding secrets: %w", err)
	}
	return writeFileAtomic(b.path, encoded, 0o600)
}

func (b *SecretBox) aead() (cipher.AEAD, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if err := b.loadKeyLocked(); err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(b.key)
	if err != nil {
		return nil, fmt.Errorf("creating cipher: %w", err)
	}
	return cipher.NewGCM(block)
}

// loadKeyLocked reads `<data>/keys/config.key`, creating it (0600) on first use.
func (b *SecretBox) loadKeyLocked() error {
	if b.key != nil {
		return nil
	}
	raw, err := os.ReadFile(b.keyPath)
	if err == nil {
		if len(raw) != SecretKeySize {
			return fmt.Errorf("config key %s must be %d bytes, found %d", b.keyPath, SecretKeySize, len(raw))
		}
		b.key = raw
		return nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("reading config key: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(b.keyPath), 0o700); err != nil {
		return fmt.Errorf("creating key directory: %w", err)
	}
	generated, err := b.newKey()
	if err != nil {
		return err
	}
	if len(generated) != SecretKeySize {
		return fmt.Errorf("config key generator returned %d bytes", len(generated))
	}
	// O_EXCL: never overwrite a key another process just created (that would
	// orphan every secret sealed with it). On EEXIST, read the winner's key.
	file, err := os.OpenFile(b.keyPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		if errors.Is(err, os.ErrExist) {
			existing, readErr := os.ReadFile(b.keyPath)
			if readErr != nil {
				return fmt.Errorf("reading config key: %w", readErr)
			}
			b.key = existing
			return nil
		}
		return fmt.Errorf("creating config key: %w", err)
	}
	if _, err := file.Write(generated); err != nil {
		_ = file.Close()
		return fmt.Errorf("writing config key: %w", err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("closing config key: %w", err)
	}
	b.key = generated
	return nil
}

// SecretID returns the stable id of one provider's API key. Ids start with
// SecretRefPrefix so a stored value can also be referenced from a doc field.
func SecretID(providerID string) string {
	return SecretRefPrefix + fmt.Sprintf(secretIDProviderAPIKey, providerID)
}
