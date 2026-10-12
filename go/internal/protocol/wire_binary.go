package protocol

import (
	"encoding/base64"
	"fmt"
	"hash/crc32"
	"regexp"
)

// CRC32Hex returns the checksum string used on the wire: CRC-32/IEEE
// (reflected polynomial 0xEDB88320) as 8 lowercase hex digits, identical to
// crc32WireBytes in wire-binary.ts.
func CRC32Hex(b []byte) string { return fmt.Sprintf("%08x", crc32.ChecksumIEEE(b)) }

// EncodeBase64 is standard base64 with padding.
func EncodeBase64(b []byte) string { return base64.StdEncoding.EncodeToString(b) }

// wireBase64 mirrors topicWireBase64Schema: at least 4 characters, whole
// quads, padding only at the end.
var wireBase64 = regexp.MustCompile(`^(?:[A-Za-z0-9+/]{4})*(?:[A-Za-z0-9+/]{2}==|[A-Za-z0-9+/]{3}=)?$`)

// DecodeBase64 returns nil, false for anything decodeWireBase64 rejects.
// Like the TypeScript decoder it ignores non-zero padding bits.
func DecodeBase64(s string) ([]byte, bool) {
	if len(s) < 4 || !wireBase64.MatchString(s) {
		return nil, false
	}
	out, err := base64.StdEncoding.DecodeString(s)
	if err != nil {
		return nil, false
	}
	return out, true
}
