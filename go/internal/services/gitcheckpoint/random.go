// random.go: the dependency-free UUID v4 + clock the checkpoint package needs
// (the TS uses crypto.randomUUID and Date.now; no new module deps for step 3.3).
package gitcheckpoint

import (
	"crypto/rand"
	"encoding/hex"
	"time"
)

func readRandom(b []byte) (int, error) { return rand.Read(b) }

func formatUUID(b []byte) string {
	// 8-4-4-4-12 canonical form.
	dst := make([]byte, 36)
	hex.Encode(dst[0:8], b[0:4])
	dst[8] = '-'
	hex.Encode(dst[9:13], b[4:6])
	dst[13] = '-'
	hex.Encode(dst[14:18], b[6:8])
	dst[18] = '-'
	hex.Encode(dst[19:23], b[8:10])
	dst[23] = '-'
	hex.Encode(dst[24:36], b[10:16])
	return string(dst)
}

// nowMillis is the checkpoint createdAt clock.
func nowMillis() int64 { return time.Now().UnixMilli() }
