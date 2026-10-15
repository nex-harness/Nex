// integrity.go ports conversationShareIntegrity.ts: the canonical JSON form
// the two sha256 digests are computed over, the confirm-request builder, and
// the integrity verification done on the server's RAW values.
//
// The canonical form is JSON.stringify-compatible per VALUE but with keys
// SORTED — that sorting is the whole point of the digest (it makes the hash
// independent of key order). Numbers must serialize the JS way (shortest
// round-trip, integral values without a decimal point) because the server
// recomputes the same hash with Node.
package conversationshare

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"unicode/utf16"
	"unicode/utf8"
)

// canonicalJSONString writes a JSON string with JS-level minimal escaping
// (only `"`, `\`, control chars; non-ASCII raw; lone surrogates rejected
// earlier by assertValidUnicode).
func canonicalJSONString(builder *strings.Builder, value string) {
	builder.WriteByte('"')
	for _, r := range value {
		switch r {
		case '"':
			builder.WriteString(`\"`)
		case '\\':
			builder.WriteString(`\\`)
		case '\b':
			builder.WriteString(`\b`)
		case '\f':
			builder.WriteString(`\f`)
		case '\n':
			builder.WriteString(`\n`)
		case '\r':
			builder.WriteString(`\r`)
		case '\t':
			builder.WriteString(`\t`)
		default:
			if r < 0x20 {
				builder.WriteString(`\u`)
				hexDigits := strconv.FormatInt(int64(r), 16)
				builder.WriteString(strings.Repeat("0", 4-len(hexDigits)))
				builder.WriteString(hexDigits)
			} else {
				builder.WriteRune(r)
			}
		}
	}
	builder.WriteByte('"')
}

// assertValidUnicode ports assertValidUnicode: a lone surrogate must fail the
// digest rather than silently re-encode (Go strings are UTF-8; a lone
// surrogate survives decoding as utf8.RuneError and we detect it via a
// round-trip check).
func assertValidUnicode(value string) error {
	if !utf8.ValidString(value) {
		return fmt.Errorf("Conversation share JSON contains invalid Unicode")
	}
	for _, r := range value {
		if utf16.IsSurrogate(r) {
			return fmt.Errorf("Conversation share JSON contains invalid Unicode")
		}
	}
	return nil
}

// canonicalJSONNumber formats a float as JSON.stringify does: integral values
// (below 1e21) print without a decimal part; others use shortest round-trip.
func canonicalJSONNumber(v float64) (string, error) {
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return "", fmt.Errorf("Conversation share JSON numbers must be finite")
	}
	if v == math.Trunc(v) && math.Abs(v) < 1e21 {
		return strconv.FormatInt(int64(v), 10), nil
	}
	return strconv.FormatFloat(v, 'g', -1, 64), nil
}

// canonicalizeValue ports canonicalizeValue (the sorted-key form).
func canonicalizeValue(builder *strings.Builder, value any) error {
	switch v := value.(type) {
	case nil:
		builder.WriteString("null")
	case bool:
		if v {
			builder.WriteString("true")
		} else {
			builder.WriteString("false")
		}
	case float64:
		text, err := canonicalJSONNumber(v)
		if err != nil {
			return err
		}
		builder.WriteString(text)
	case string:
		if err := assertValidUnicode(v); err != nil {
			return err
		}
		canonicalJSONString(builder, v)
	case []any:
		builder.WriteByte('[')
		for index, entry := range v {
			if index > 0 {
				builder.WriteByte(',')
			}
			if err := canonicalizeValue(builder, entry); err != nil {
				return err
			}
		}
		builder.WriteByte(']')
	case map[string]any:
		keys := make([]string, 0, len(v))
		for key := range v {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		builder.WriteByte('{')
		for index, key := range keys {
			if index > 0 {
				builder.WriteByte(',')
			}
			if err := assertValidUnicode(key); err != nil {
				return err
			}
			canonicalJSONString(builder, key)
			builder.WriteByte(':')
			if err := canonicalizeValue(builder, v[key]); err != nil {
				return err
			}
		}
		builder.WriteByte('}')
	default:
		// int/int64 enter here from hand-built payloads.
		switch n := value.(type) {
		case int:
			builder.WriteString(strconv.Itoa(n))
		case int64:
			builder.WriteString(strconv.FormatInt(n, 10))
		default:
			return fmt.Errorf("Conversation share JSON cannot contain %T", value)
		}
	}
	return nil
}

// sha256CanonicalJSON ports sha256ConversationShareJson.
func sha256CanonicalJSON(value any) (string, error) {
	var builder strings.Builder
	if err := canonicalizeValue(&builder, value); err != nil {
		return "", err
	}
	digest := sha256.Sum256([]byte(builder.String()))
	return hex.EncodeToString(digest[:]), nil
}

// artifactURLKeys ports ARTIFACT_URL_KEYS: 服务端签发 signed URL 时附加的字段，
// 不参与 artifact set 摘要。这是一份「服务端在读取时附加的字段」名单，不是本端
// 字段白名单（方向相反，见 TS 注释）。
var artifactURLKeys = []string{
	"download_url", "download_url_expires_at", "url", "url_expires_at",
}

// stripArtifactURLs ports stripArtifactUrls.
func stripArtifactURLs(value any) any {
	record, ok := value.(map[string]any)
	if !ok {
		return value
	}
	stripped := make(map[string]any, len(record))
	for key, entry := range record {
		stripped[key] = entry
	}
	for _, key := range artifactURLKeys {
		delete(stripped, key)
	}
	return stripped
}

// artifactIDOf ports artifactIdOf.
func artifactIDOf(value any) string {
	record, ok := value.(map[string]any)
	if !ok {
		return ""
	}
	id, _ := record["artifact_id"].(string)
	return id
}

// sortArtifactsByPortedID sorts a raw artifact array by artifact_id the way
// the TS localeCompare does for these identifier shapes (they are ASCII;
// byte compare equals locale order here).
func sortArtifactsByPortedID(artifacts []any) {
	sort.SliceStable(artifacts, func(i, j int) bool {
		return artifactIDOf(artifacts[i]) < artifactIDOf(artifacts[j])
	})
}

// verifyIntegrity ports verifyConversationShareIntegrity: 用服务端原样发来的
// rows / artifacts 复核两个摘要（不对 zod 解析产物重算——schema 认知必须与
// 完整性解耦，见 TS 注释）。
func verifyIntegrity(rawRows, rawArtifacts any, integrity struct{ Projection, ArtifactSet string }) bool {
	projectionDigest, err := sha256CanonicalJSON(rawRows)
	if err != nil || projectionDigest != integrity.Projection {
		return false
	}
	artifacts := rawArtifacts
	if list, ok := rawArtifacts.([]any); ok {
		sorted := make([]any, len(list))
		copy(sorted, list)
		sortArtifactsByPortedID(sorted)
		for index, entry := range sorted {
			sorted[index] = stripArtifactURLs(entry)
		}
		artifacts = sorted
	}
	artifactDigest, err := sha256CanonicalJSON(artifacts)
	return err == nil && artifactDigest == integrity.ArtifactSet
}
