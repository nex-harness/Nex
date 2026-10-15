// digest.go: the revision digest used by the personal repository
// (createHash("sha256").update(content).digest("hex") in the TS).
package providerconfig

import (
	"crypto/sha256"
	"encoding/hex"
)

// SHA256Hex digests content into the hex form the TS snapshots use.
func SHA256Hex(content []byte) string {
	sum := sha256.Sum256(content)
	return hex.EncodeToString(sum[:])
}

// sha256Hex is the internal spelling.
func sha256Hex(content []byte) string { return SHA256Hex(content) }
