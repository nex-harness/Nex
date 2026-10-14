// cipher.go ports credentialCipherProvider.ts: AES-256-GCM with a
// SHA-256(secret) key, base64url iv.tag.ciphertext parts, `enc:v1:` prefix.
// Values without the prefix pass through un-decrypted (migration headroom).
package credential

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"os"
	"runtime"
	"strings"
)

const (
	cipherIVBytes  = 12
	cipherTagBytes = 16
)

// DecryptError is the CREDENTIAL_DECRYPT_FAILED error the UI branches on.
type DecryptError struct{ Reason string }

func (e *DecryptError) Error() string { return DecryptErrorPrefix + e.Reason }
func (e *DecryptError) Code() string  { return DecryptErrorCode }

// NewAESCipher ports createCredentialCipherProvider with the default secret:
// NEX_CREDENTIAL_SECRET when set, else the
// `nex-credential-fallback:{platform}:{homedir}:{username}` composite.
func NewAESCipher(envSnapshot func() map[string]string, userInfo func() (string, string, string)) *AESCipher {
	env := envMapOf(envSnapshot)
	if userInfo == nil {
		userInfo = osUserInfo
	}
	secret := env[SecretEnvKey]
	if secret == "" {
		username, homeDir, platform := userInfo()
		if username == "" {
			// 部分运行环境可能拿不到系统用户，失败时退回默认占位值。
			username = "unknown"
		}
		if platform == "" {
			platform = "unknown"
		}
		secret = fmt.Sprintf("nex-credential-fallback:%s:%s:%s", platform, homeDir, username)
	}
	key := sha256.Sum256([]byte(secret))
	return &AESCipher{key: key[:]}
}

// AESCipher is the concrete Cipher.
type AESCipher struct{ key []byte }

// Encrypt ports encrypt.
func (c *AESCipher) Encrypt(value string) (string, error) {
	iv := make([]byte, cipherIVBytes)
	if _, err := rand.Read(iv); err != nil {
		return "", err
	}
	block, err := aes.NewCipher(c.key)
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	sealed := gcm.Seal(nil, iv, []byte(value), nil)
	ciphertext, tag := sealed[:len(sealed)-cipherTagBytes], sealed[len(sealed)-cipherTagBytes:]
	urlEnc := base64.RawURLEncoding
	return strings.Join([]string{
		EncryptedValuePrefix,
		urlEnc.EncodeToString(iv),
		".",
		urlEnc.EncodeToString(tag),
		".",
		urlEnc.EncodeToString(ciphertext),
	}, ""), nil
}

// Decrypt ports decrypt: un-prefixed values pass through (TS behavior).
func (c *AESCipher) Decrypt(value string) (string, error) {
	if !strings.HasPrefix(value, EncryptedValuePrefix) {
		return value, nil
	}
	payload := value[len(EncryptedValuePrefix):]
	parts := strings.Split(payload, ".")
	if len(parts) != 3 || parts[0] == "" || parts[1] == "" || parts[2] == "" {
		return "", &DecryptError{Reason: "密文格式非法"}
	}
	urlEnc := base64.RawURLEncoding
	iv, err := urlEnc.DecodeString(parts[0])
	if err != nil {
		return "", &DecryptError{Reason: "密文格式非法"}
	}
	tag, err := urlEnc.DecodeString(parts[1])
	if err != nil {
		return "", &DecryptError{Reason: "密文格式非法"}
	}
	ciphertext, err := urlEnc.DecodeString(parts[2])
	if err != nil {
		return "", &DecryptError{Reason: "密文格式非法"}
	}
	if len(iv) != cipherIVBytes {
		return "", &DecryptError{Reason: "IV 长度非法"}
	}
	if len(tag) != cipherTagBytes {
		return "", &DecryptError{Reason: "AuthTag 长度非法"}
	}
	block, err := aes.NewCipher(c.key)
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	plaintext, err := gcm.Open(nil, iv, append(ciphertext, tag...), nil)
	if err != nil {
		return "", &DecryptError{Reason: "密钥不匹配或密文已损坏"}
	}
	return string(plaintext), nil
}

// osUserInfo is the default UserInfo source.
func osUserInfo() (username, homeDir, platform string) {
	platform = runtime.GOOS
	home, homeErr := os.UserHomeDir()
	if homeErr != nil {
		home = ""
	}
	return os.Getenv("USER"), home, platform
}

func envMapOf(snapshot func() map[string]string) map[string]string {
	if snapshot != nil {
		return snapshot()
	}
	out := map[string]string{}
	for _, entry := range os.Environ() {
		for i := 0; i < len(entry); i++ {
			if entry[i] == '=' {
				out[entry[:i]] = entry[i+1:]
				break
			}
		}
	}
	return out
}

func sha256Prefix(data []byte, n int) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])[:n]
}
