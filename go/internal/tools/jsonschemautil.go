package tools

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"strconv"
	"strings"
)

func createMissingPropertyIssues(propertySchema map[string]any, path []any) []ValidationIssue {
	if propertySchema != nil {
		cc := &collector{}
		validateNode(undefinedValue{}, propertySchema, "$", path, cc)
		if len(cc.issues) > 0 {
			return cc.issues
		}
	}
	return []ValidationIssue{createInvalidTypeIssue(undefinedValue{}, inferSchemaType(propertySchema), path)}
}

func inferSchemaType(schema map[string]any) string {
	if schema == nil {
		return "unknown"
	}
	if t, ok := schema["type"].(string); ok {
		return t
	}
	if _, ok := schema["properties"].(map[string]any); ok {
		return "object"
	}
	if _, ok := schema["required"].([]any); ok {
		return "object"
	}
	if _, ok := schema["items"].(map[string]any); ok {
		return "array"
	}
	return "unknown"
}

func matchesType(value any, typeVal any) bool {
	if arr, ok := typeVal.([]any); ok {
		for _, t := range arr {
			if matchesType(value, t) {
				return true
			}
		}
		return false
	}
	name, _ := typeVal.(string)
	switch name {
	case "array":
		_, ok := value.([]any)
		return ok
	case "boolean":
		_, ok := value.(bool)
		return ok
	case "integer":
		f, ok := asFloat(value)
		return ok && f == math.Trunc(f)
	case "null":
		return value == nil
	case "number":
		f, ok := asFloat(value)
		return ok && !math.IsInf(f, 0) && !math.IsNaN(f)
	case "object":
		_, ok := value.(map[string]any)
		return ok
	case "string":
		_, ok := value.(string)
		return ok
	default:
		return true
	}
}

func asFloat(value any) (float64, bool) {
	switch n := value.(type) {
	case float64:
		return n, true
	case int:
		return float64(n), true
	case json.Number:
		f, err := n.Float64()
		return f, err == nil
	default:
		return 0, false
	}
}

func asSchemaArray(value any) ([]map[string]any, bool) {
	arr, ok := value.([]any)
	if !ok {
		return nil, false
	}
	out := make([]map[string]any, len(arr))
	for i, item := range arr {
		m, ok := item.(map[string]any)
		if !ok {
			return nil, false
		}
		out[i] = m
	}
	return out, true
}

func formatSchemaType(typeVal any) string {
	if arr, ok := typeVal.([]any); ok {
		parts := make([]string, len(arr))
		for i, t := range arr {
			parts[i] = fmt.Sprint(t)
		}
		return joinOr(parts)
	}
	return fmt.Sprint(typeVal)
}

func jsonValueEqual(a, b any) bool {
	af, aok := asFloat(a)
	bf, bok := asFloat(b)
	if aok && bok {
		return af == bf
	}
	ab, _ := json.Marshal(a)
	bb, _ := json.Marshal(b)
	return string(ab) == string(bb)
}

func jsonString(v any) string {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(v)
	return strings.TrimSuffix(buf.String(), "\n")
}

func stringSlice(v any) []string {
	arr, ok := v.([]any)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(arr))
	for _, item := range arr {
		if s, ok := item.(string); ok {
			out = append(out, s)
		}
	}
	return out
}

func clonePath(p []any) []any {
	out := make([]any, len(p))
	copy(out, p)
	return out
}

func appendPath(p []any, seg any) []any {
	out := make([]any, len(p)+1)
	copy(out, p)
	out[len(p)] = seg
	return out
}

func joinComma(parts []string) string {
	out := ""
	for i, p := range parts {
		if i > 0 {
			out += ", "
		}
		out += p
	}
	return out
}

func joinOr(parts []string) string {
	out := ""
	for i, p := range parts {
		if i > 0 {
			out += " or "
		}
		out += p
	}
	return out
}

func jsStringLength(s string) int {
	n := 0
	for _, r := range s {
		if r > 0xFFFF {
			n += 2
		} else {
			n++
		}
	}
	return n
}

func formatNumber(f float64) string {
	if f == math.Trunc(f) {
		return strconv.FormatInt(int64(f), 10)
	}
	return strconv.FormatFloat(f, 'g', -1, 64)
}
