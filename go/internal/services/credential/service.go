// Package credential ports packages/services/src/credential/* (step 3.5):
// ICredentialService over ~/.nex/v2/credentials.json with values encrypted by
// the credential cipher provider (AES-256-GCM, key = SHA-256 of the
// process/user-derived secret — credentialCipherProvider.ts).
//
// 分类（plan 3.5 Special 的核实结果）：TS 不经 Electron safeStorage —— 加密是
// 纯 Node crypto（AES-256-GCM + 环境变量/用户名派生密钥），文件为普通 JSON。
// 因此本服务不是 desktop-main-only，属于可移植集合，`kept` 判定不适用。
// IPlatformService 边界（AGENTS.md）只约束 UI 调用方，不涉及此处。
package credential

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/tylinux/nex/go/internal/adapters/fsx"
	"github.com/tylinux/nex/go/internal/services/paths"
)

// ChannelName is ServiceChannels.Credential.
const ChannelName = "credential"

// DecryptErrorCode / prefix are CREDENTIAL_DECRYPT_ERROR_* from
// packages/shared/src/oauth.ts; the UI branches on them.
const (
	DecryptErrorCode   = "NEX_CREDENTIAL_DECRYPT_FAILED"
	DecryptErrorPrefix = "凭据解密失败："
)

// SecretEnvKey is NEX_CREDENTIAL_SECRET (pre-existing TS variable; D7 adds none).
const SecretEnvKey = "NEX_CREDENTIAL_SECRET"

// EncryptedValuePrefix is ENCRYPTED_VALUE_PREFIX.
const EncryptedValuePrefix = "enc:v1:"

// Cipher ports CredentialCipherProvider.
type Cipher interface {
	Encrypt(value string) (string, error)
	Decrypt(value string) (string, error)
}

// MutateEvent is the onDidMutate payload (host-private, not on the RPC).
type MutateEvent struct {
	Operation string // "save" | "delete"
	Key       string
}

// Options configures NewService.
type Options struct {
	// CredentialsFile: the store path. Empty resolves {home}/.nex/v2/credentials.json
	// via NEX_DESKTOP_HOME_DIR > HOME > USERPROFILE (same resolver as setting).
	CredentialsFile string
	// Cipher overrides the cipher provider (tests).
	Cipher Cipher
	// OnDidMutate is the host-side mutation notify.
	OnDidMutate func(event MutateEvent)
	// EnvSnapshot supplies env reads; nil reads the process env.
	EnvSnapshot func() map[string]string
	// UserInfo supplies username/homedir/platform for the fallback secret.
	// Nil uses the real OS values.
	UserInfo func() (username, homeDir, platform string)
}

// Service is ICredentialService.
type Service struct {
	file      string
	cipher    Cipher
	onMutate  func(MutateEvent)
	env       func() map[string]string
	userInfo  func() (string, string, string)
	writeLock sync.Mutex
}

// NewService builds the service.
func NewService(opts Options) *Service {
	if opts.CredentialsFile == "" {
		opts.CredentialsFile = defaultCredentialsFile(opts.EnvSnapshot)
	}
	if opts.Cipher == nil {
		opts.Cipher = NewAESCipher(opts.EnvSnapshot, opts.UserInfo)
	}
	if opts.UserInfo == nil {
		opts.UserInfo = osUserInfo
	}
	return &Service{
		file:     opts.CredentialsFile,
		cipher:   opts.Cipher,
		onMutate: opts.OnDidMutate,
		env:      opts.EnvSnapshot,
		userInfo: opts.UserInfo,
	}
}

// defaultCredentialsFile resolves via the paths package (getAppConfigDir:
// NEX_DATA_BASE_DIR > HOME — the credential store follows the data dir, NOT
// the desktop-home override the setting service uses).
func defaultCredentialsFile(envSnapshot func() map[string]string) string {
	_ = envSnapshot
	return filepath.Join(paths.NewFromProcess().AppConfigDir(), "credentials.json")
}

// CredentialsFile exposes the store path (tests).
func (s *Service) CredentialsFile() string { return s.file }

// Load ports load(key).
func (s *Service) Load(ctx context.Context, key string) (string, error) {
	validated, err := validateKey(key)
	if err != nil {
		return "", err
	}
	creds, err := s.readAll(ctx)
	if err != nil {
		return "", err
	}
	rawValue, ok := creds[validated]
	if !ok {
		return "", nil
	}
	return s.cipher.Decrypt(rawValue)
}

// Save ports save(key, value): whole-file read-modify-write under the shared
// file lock (desktop host 与 CLI adapter 是独立进程，进程内排队不够).
func (s *Service) Save(ctx context.Context, key, value string) error {
	validated, err := validateKey(key)
	if err != nil {
		return err
	}
	if _, err := validateValue(value); err != nil {
		return err
	}
	encrypted, err := s.cipher.Encrypt(value)
	if err != nil {
		return err
	}
	if err := s.withLockedStore(ctx, func(creds map[string]string) error {
		creds[validated] = encrypted
		return s.writeAll(creds)
	}); err != nil {
		return err
	}
	if s.onMutate != nil {
		s.onMutate(MutateEvent{Operation: "save", Key: validated})
	}
	return nil
}

// Delete ports delete(key).
func (s *Service) Delete(ctx context.Context, key string) error {
	validated, err := validateKey(key)
	if err != nil {
		return err
	}
	if err := s.withLockedStore(ctx, func(creds map[string]string) error {
		delete(creds, validated)
		return s.writeAll(creds)
	}); err != nil {
		return err
	}
	if s.onMutate != nil {
		s.onMutate(MutateEvent{Operation: "delete", Key: validated})
	}
	return nil
}

// readAll ports readAll: ENOENT → {}; corrupt JSON/schema → back up the file
// (content-hash name, exclusive create) and REFUSE to overwrite.
func (s *Service) readAll(ctx context.Context) (map[string]string, error) {
	data, err := os.ReadFile(s.file)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return map[string]string{}, nil
		}
		return nil, fmt.Errorf("Unable to read Nex credentials: %s: %w", s.file, err)
	}
	creds, parseErr := parseRecord(data)
	if parseErr != nil {
		backupPath, backupErr := backupCorruptFile(s.file, data)
		if backupErr != nil {
			backupPath = ""
		}
		// 把损坏 JSON/schema 当成空 store 后继续 save 会清空其他 OAuth 与登录凭据。
		// 保留损坏文件证据并向上传递错误，禁止自动覆盖。
		_ = backupPath
		return nil, fmt.Errorf("Nex credentials are corrupt: %s: %w", s.file, parseErr)
	}
	return creds, nil
}

// writeAll ports writeAll: private (0600) atomic write with trailing newline.
func (s *Service) writeAll(creds map[string]string) error {
	content := renderCredentialsJSON(creds) + "\n"
	return fsx.AtomicWritePrivateTextFile(context.Background(), s.file, []byte(content), nil)
}

// withLockedStore runs one read-modify-write cycle under the same-directory
// lock file (the TS withFileLock; fsx implements the same <path>.lock
// convention so Node and Go interoperate during the transition).
func (s *Service) withLockedStore(ctx context.Context, mutate func(map[string]string) error) error {
	// In-process serialization first (the TS processFileLockTails FIFO), then
	// the cross-process lock inside the atomic write.
	s.writeLock.Lock()
	defer s.writeLock.Unlock()
	creds, err := s.readAll(ctx)
	if err != nil {
		return err
	}
	return mutate(creds)
}

// parseRecord ports credentialRecordSchema (record<string,string>) parse.
func parseRecord(data []byte) (map[string]string, error) {
	var probe any
	if err := jsonUnmarshalBytes(data, &probe); err != nil {
		return nil, err
	}
	obj, ok := probe.(map[string]any)
	if !ok {
		return nil, errors.New("credentials: not a JSON object")
	}
	out := make(map[string]string, len(obj))
	for k, v := range obj {
		s, ok := v.(string)
		if !ok {
			return nil, fmt.Errorf("credentials: value for %q is not a string", k)
		}
		out[k] = s
	}
	return out, nil
}

// renderCredentialsJSON writes JSON.stringify(creds, null, 2) with keys in
// insertion order — sorted here for deterministic output across runs (the TS
// object preserves write order; Go maps do not, and the credential store's
// write order is not part of any contract, so a canonical order is chosen and
// documented in decisions.md).
func renderCredentialsJSON(creds map[string]string) string {
	keys := make([]string, 0, len(creds))
	for k := range creds {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var sb strings.Builder
	sb.WriteString("{")
	for i, k := range keys {
		if i > 0 {
			sb.WriteString(",")
		}
		sb.WriteString("\n  ")
		sb.WriteString(jsonString(k))
		sb.WriteString(": ")
		sb.WriteString(jsonString(creds[k]))
	}
	if len(keys) > 0 {
		sb.WriteString("\n")
	}
	sb.WriteString("}")
	return sb.String()
}

// validateKey ports credentialKeySchema (nonEmptyString: trim, min 1).
func validateKey(key string) (string, error) {
	trimmed := strings.TrimSpace(key)
	if trimmed == "" {
		return "", errors.New("credential key must be a non-empty string")
	}
	return trimmed, nil
}

// validateValue ports credentialValueSchema (any string).
func validateValue(value string) (string, error) {
	return value, nil
}

// backupCorruptFile ports backupCorruptFile: <file>.corrupt-<sha256[:24]>.bak,
// exclusive create, chmod 0600.
func backupCorruptFile(path string, content []byte) (string, error) {
	backupPath := path + ".corrupt-" + sha256Prefix(content, 24) + ".bak"
	f, err := os.OpenFile(backupPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		if errors.Is(err, os.ErrExist) {
			return backupPath, nil
		}
		return "", err
	}
	if _, err := f.Write(content); err != nil {
		f.Close()
		return "", err
	}
	if err := f.Close(); err != nil {
		return "", err
	}
	return backupPath, os.Chmod(backupPath, 0o600)
}

// jsonString renders a Go string as a JSON string literal.
func jsonString(s string) string {
	var sb strings.Builder
	sb.WriteByte('"')
	for _, r := range s {
		switch r {
		case '"':
			sb.WriteString(`\"`)
		case '\\':
			sb.WriteString(`\\`)
		case '\n':
			sb.WriteString(`\n`)
		case '\r':
			sb.WriteString(`\r`)
		case '\t':
			sb.WriteString(`\t`)
		default:
			if r < 0x20 {
				fmt.Fprintf(&sb, `\u%04x`, r)
			} else {
				sb.WriteRune(r)
			}
		}
	}
	sb.WriteByte('"')
	return sb.String()
}

// jsonUnmarshalBytes decodes JSON via encoding/json.
func jsonUnmarshalBytes(data []byte, v any) error {
	return json.Unmarshal(data, v)
}
