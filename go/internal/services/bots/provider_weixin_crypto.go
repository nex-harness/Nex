package bots

import (
	"crypto/aes"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"strings"
)

// provider_weixin_crypto.go decrypts the Weixin iLink CDN payloads.
//
// The CDN returns AES-128-ECB with PKCS7 padding and no IV. Go's stdlib does not
// export an ECB mode, so the block transform is written out here; it is plain
// independent block decryption. Saving the ciphertext instead of the plaintext
// would produce an unrecognisable file, which is the bug this code exists to fix.
// ParseWeixinAesKey ports parseWeixinAesKey: a 32-char hex string, or a base64
// string that decodes to 16 bytes, or a base64 string whose decoded TEXT is hex.
func ParseWeixinAesKey(value string) []byte {
	trimmed := strings.TrimSpace(value)
	if len(trimmed) == 32 && isHexString(trimmed) {
		decoded, err := hex.DecodeString(trimmed)
		if err == nil {
			return decoded
		}
	}
	if decoded, err := base64.StdEncoding.DecodeString(trimmed); err == nil {
		if len(decoded) == 16 {
			return decoded
		}
		decodedText := strings.TrimSpace(string(decoded))
		if len(decodedText) == 32 && isHexString(decodedText) {
			if key, err := hex.DecodeString(decodedText); err == nil {
				return key
			}
		}
	}
	return nil
}

func isHexString(value string) bool {
	for i := 0; i < len(value); i++ {
		c := value[i]
		if (c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F') {
			continue
		}
		return false
	}
	return true
}

// decryptWeixinCDNMedia ports decryptWeixinCdnMedia: AES-128-ECB with PKCS7
// padding, no IV.
func decryptWeixinCDNMedia(data []byte, aesKey string) ([]byte, error) {
	key := ParseWeixinAesKey(aesKey)
	if key == nil {
		return nil, fmt.Errorf("Weixin attachment AES key is invalid.")
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("Weixin attachment AES key is invalid.")
	}
	if len(data) == 0 || len(data)%aes.BlockSize != 0 {
		return nil, fmt.Errorf("Weixin attachment decryption failed: ciphertext is not block aligned.")
	}
	// Go's stdlib does not export an ECB mode; a no-IV ECB transform is just
	// independent block decryption, so it is written out here.
	decrypted := make([]byte, len(data))
	for offset := 0; offset < len(data); offset += aes.BlockSize {
		block.Decrypt(decrypted[offset:offset+aes.BlockSize], data[offset:offset+aes.BlockSize])
	}
	// PKCS7 unpadding. Node's decipher throws on a malformed pad, so an
	// invalid length surfaces as an error rather than as trailing garbage.
	padding := int(decrypted[len(decrypted)-1])
	if padding == 0 || padding > aes.BlockSize || padding > len(decrypted) {
		return nil, fmt.Errorf("Weixin attachment decryption failed: invalid padding.")
	}
	for _, b := range decrypted[len(decrypted)-padding:] {
		if int(b) != padding {
			return nil, fmt.Errorf("Weixin attachment decryption failed: invalid padding.")
		}
	}
	return decrypted[:len(decrypted)-padding], nil
}
