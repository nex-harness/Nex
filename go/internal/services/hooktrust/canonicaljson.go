// canonicaljson.go: the JSON.stringify-compatible serializer the digests
// depend on. Node's JSON.stringify emits: no whitespace, double quotes with
// minimal escaping (only `"` `\` and control chars), object keys in
// insertion order, numbers via the JS shortest-round-trip format. The digest
// payloads are arrays/scalars built in this package, so only the scalar and
// array cases are needed; the map case exists for the trust-store writer.
package hooktrust

import (
	"math"
	"path/filepath"
	"strconv"
	"strings"
)

// canonicalJSON serializes value the way JSON.stringify does.
func canonicalJSON(value any) string {
	var builder strings.Builder
	writeCanonicalJSON(&builder, value)
	return builder.String()
}

func writeCanonicalJSON(builder *strings.Builder, value any) {
	switch v := value.(type) {
	case *string:
		// A present-but-nil pointer is JSON null; a set pointer is its value
		// (JSON.stringify unwraps the TS `matcher: string | null`).
		if v == nil {
			builder.WriteString("null")
		} else {
			writeJSONString(builder, *v)
		}
	case nil:
		builder.WriteString("null")
	case bool:
		if v {
			builder.WriteString("true")
		} else {
			builder.WriteString("false")
		}
	case string:
		writeJSONString(builder, v)
	case int:
		builder.WriteString(strconv.Itoa(v))
	case int64:
		builder.WriteString(strconv.FormatInt(v, 10))
	case uint64:
		builder.WriteString(strconv.FormatUint(v, 10))
	case float64:
		builder.WriteString(jsJSONNumber(v))
	case []any:
		builder.WriteByte('[')
		for index, item := range v {
			if index > 0 {
				builder.WriteByte(',')
			}
			writeCanonicalJSON(builder, item)
		}
		builder.WriteByte(']')
	case map[string]any:
		// Key order: JSON.stringify uses insertion order; for our payloads
		// the maps come from decoded JSON where Node preserves file order —
		// the trust-store writer only writes structs, so this map path is
		// used only by non-digest writers that sort for determinism.
		builder.WriteByte('{')
		first := true
		for _, key := range sortedKeys(v) {
			if !first {
				builder.WriteByte(',')
			}
			first = false
			writeJSONString(builder, key)
			builder.WriteByte(':')
			writeCanonicalJSON(builder, v[key])
		}
		builder.WriteByte('}')
	default:
		builder.WriteString("null")
	}
}

func sortedKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for key := range m {
		keys = append(keys, key)
	}
	for i := 1; i < len(keys); i++ {
		for j := i; j > 0 && keys[j-1] > keys[j]; j-- {
			keys[j-1], keys[j] = keys[j], keys[j-1]
		}
	}
	return keys
}

// writeJSONString emits a JSON string with JS-level minimal escaping: only
// `"`, `\`, and control characters (as \b \f \n \r \t or \u00XX) are
// escaped; non-ASCII stays raw (UTF-8), lone surrogates become \uXXXX.
func writeJSONString(builder *strings.Builder, value string) {
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
				hex := strconv.FormatInt(int64(r), 16)
				builder.WriteString(strings.Repeat("0", 4-len(hex)))
				builder.WriteString(hex)
			} else {
				builder.WriteRune(r)
			}
		}
	}
	builder.WriteByte('"')
}

// jsJSONNumber formats a float as JS: integral values print without the
// decimal part; others use the shortest round-trip representation.
func jsJSONNumber(v float64) string {
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return "null"
	}
	if v == math.Trunc(v) && math.Abs(v) < 1e21 {
		return strconv.FormatInt(int64(v), 10)
	}
	return strconv.FormatFloat(v, 'g', -1, 64)
}

// jsNumber converts to the any-typed JSON number (kept float64).
func jsNumber(v float64) any { return v }

func absClean(path string) (string, error) { return filepath.Abs(path) }

func relPath(base, target string) (string, error) { return filepath.Rel(base, target) }

func strconvItoa(value int) string { return strconv.Itoa(value) }
