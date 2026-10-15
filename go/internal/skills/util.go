// util.go: small shared helpers.
package skills

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strconv"
	"strings"
)

// strings_TrimSpaceToLower keeps the normalizeSkillNameKey body in one place.
func strings_TrimSpaceToLower(value string) string {
	return strings.ToLower(strings.TrimSpace(value))
}

// HashStableIDPart ports hashStableIdPart: first 12 hex chars of sha256.
func HashStableIDPart(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])[:12]
}

// BuildSkillID ports buildSkillId: provider:scope:name:hash12(path).
func BuildSkillID(provider, scope, name, path string) string {
	return fmt.Sprintf("%s:%s:%s:%s", provider, scope, name, HashStableIDPart(path))
}

// NormalizeSkillNameKey lowercases and trims (normalizeSkillNameKey).
func NormalizeSkillNameKey(name string) string {
	return strings_TrimSpaceToLower(name)
}

// CandidateID is the full sha256 of the directory path (createCandidateId).
func CandidateID(skillDirectoryPath string) string {
	sum := sha256.Sum256([]byte(skillDirectoryPath))
	return hex.EncodeToString(sum[:])
}

// ShortHash is the full sha256 hex (createShortHash).
func ShortHash(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

// scalarToString mirrors JS String(value) for the YAML scalars that reach
// readFrontmatterString.
func scalarToString(value any) string {
	switch v := value.(type) {
	case bool:
		if v {
			return "true"
		}
		return "false"
	case int:
		return strconv.Itoa(v)
	case int64:
		return strconv.FormatInt(v, 10)
	case float64:
		return formatJSNumber(v)
	case uint64:
		return strconv.FormatUint(v, 10)
	case string:
		return v
	default:
		return fmt.Sprintf("%v", v)
	}
}

// formatJSNumber formats a float the way JS String(number) does (integral
// floats print without the decimal part).
func formatJSNumber(v float64) string {
	if v == float64(int64(v)) && v > -1e21 && v < 1e21 {
		return strconv.FormatInt(int64(v), 10)
	}
	return strconv.FormatFloat(v, 'g', -1, 64)
}
