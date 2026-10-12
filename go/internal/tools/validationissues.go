package tools

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"strings"
)

func createInvalidTypeIssue(value any, schemaType any, path []any) ValidationIssue {
	expected, format := expectedType(value, schemaType)
	received := specialNumberReceived(value)
	message := "Invalid input: expected " + expected + ", received " + formatReceivedType(value)
	issue := ValidationIssue{Code: "invalid_type", Expected: expected, Path: clonePath(path), Message: message}
	if format != "" {
		issue.Format = format
		return issue
	}
	if received != "" {
		issue.Received = received
	}
	return issue
}

func createInvalidValueIssue(values []any, path []any) ValidationIssue {
	var message string
	if len(values) == 1 {
		message = "Invalid input: expected " + formatIssueValue(values[0])
	} else {
		parts := make([]string, len(values))
		for i, v := range values {
			parts[i] = formatIssueValue(v)
		}
		message = "Invalid option: expected one of " + strings.Join(parts, "|")
	}
	return ValidationIssue{Code: "invalid_value", Values: values, Path: clonePath(path), Message: message}
}

func createUnrecognizedKeysIssue(keys []string, path []any) ValidationIssue {
	parts := make([]string, len(keys))
	for i, k := range keys {
		parts[i] = formatIssueValue(k)
	}
	label := "Unrecognized key"
	if len(keys) > 1 {
		label = "Unrecognized keys"
	}
	return ValidationIssue{Code: "unrecognized_keys", Keys: keys, Path: clonePath(path), Message: label + ": " + strings.Join(parts, ", ")}
}

func createTooSmallIssue(origin string, minimum int, path []any) ValidationIssue {
	inclusive := true
	m := float64(minimum)
	return ValidationIssue{Code: "too_small", Origin: origin, Minimum: &m, Inclusive: &inclusive, Path: clonePath(path), Message: formatSizeIssueMessage("too_small", origin, minimum, inclusive)}
}

func createTooBigIssue(origin string, maximum int, path []any) ValidationIssue {
	inclusive := true
	m := float64(maximum)
	return ValidationIssue{Code: "too_big", Origin: origin, Maximum: &m, Inclusive: &inclusive, Path: clonePath(path), Message: formatSizeIssueMessage("too_big", origin, maximum, inclusive)}
}

func createCustomIssue(message string, path []any) ValidationIssue {
	return ValidationIssue{Code: "custom", Path: clonePath(path), Message: message}
}

func expectedType(value any, schemaType any) (string, string) {
	if s, ok := schemaType.(string); ok && s == "integer" {
		if f, ok := asFloat(value); ok && !math.IsInf(f, 0) && !math.IsNaN(f) {
			return "int", "safeint"
		}
		return "number", ""
	}
	return fmt.Sprint(schemaType), ""
}

func specialNumberReceived(value any) string {
	f, ok := asFloat(value)
	if !ok {
		return ""
	}
	if math.IsNaN(f) {
		return "NaN"
	}
	if math.IsInf(f, 0) {
		return "Infinity"
	}
	return ""
}

func formatSizeIssueMessage(code, origin string, limit int, inclusive bool) string {
	comparison := ">="
	if code == "too_big" {
		if inclusive {
			comparison = "<="
		} else {
			comparison = "<"
		}
	} else if !inclusive {
		comparison = ">"
	}
	label := "Too small"
	if code == "too_big" {
		label = "Too big"
	}
	unit := ""
	if origin == "string" {
		unit = "characters"
	} else if origin == "array" {
		unit = "items"
	}
	if unit != "" {
		return label + ": expected " + origin + " to have " + comparison + fmt.Sprint(limit) + " " + unit
	}
	return label + ": expected " + origin + " to be " + comparison + fmt.Sprint(limit)
}

func formatIssueValue(value any) string {
	switch v := value.(type) {
	case string:
		return `"` + v + `"`
	default:
		return fmt.Sprint(value)
	}
}

func formatReceivedType(value any) string {
	switch v := value.(type) {
	case undefinedValue:
		return "undefined"
	case nil:
		return "null"
	case float64:
		if math.IsNaN(v) {
			return "NaN"
		}
		return "number"
	// A UseNumber decoder produces json.Number for the model's raw argument
	// JSON; JS has a single number type, so it reports the same name.
	case json.Number:
		return "number"
	case int:
		return "number"
	case bool:
		return "boolean"
	case string:
		return "string"
	case []any:
		return "array"
	case map[string]any:
		return "object"
	default:
		return "object"
	}
}

// undefinedValue marks a missing property so formatReceivedType reports
// "undefined" exactly like a JS object with an absent key.
type undefinedValue struct{}

// MarshalJSON mirrors the TypeScript issue field insertion order, which the
// provider-visible JSON fallback prints verbatim. encoding/json cannot express a
// per-type key order from struct tags, so build it here.
func (o ValidationIssue) MarshalJSON() ([]byte, error) {
	var buf bytes.Buffer
	buf.WriteByte('{')
	first := true
	writeKey := func(k string) {
		if !first {
			buf.WriteByte(',')
		}
		first = false
		buf.WriteString(jsonString(k))
		buf.WriteByte(':')
	}
	if o.Origin != "" {
		writeKey("origin")
		buf.WriteString(jsonString(o.Origin))
	}
	writeKey("code")
	buf.WriteString(jsonString(o.Code))
	if o.Expected != "" {
		writeKey("expected")
		buf.WriteString(jsonString(o.Expected))
	}
	if o.Format != "" {
		writeKey("format")
		buf.WriteString(jsonString(o.Format))
	}
	if o.Received != "" {
		writeKey("received")
		buf.WriteString(jsonString(o.Received))
	}
	if o.Values != nil {
		writeKey("values")
		writeJSONValue(&buf, o.Values)
	}
	if o.Keys != nil {
		writeKey("keys")
		writeJSONValue(&buf, o.Keys)
	}
	if o.Minimum != nil {
		writeKey("minimum")
		writeJSONValue(&buf, *o.Minimum)
	}
	if o.Maximum != nil {
		writeKey("maximum")
		writeJSONValue(&buf, *o.Maximum)
	}
	if o.Inclusive != nil {
		writeKey("inclusive")
		writeJSONValue(&buf, *o.Inclusive)
	}
	if o.Exact {
		writeKey("exact")
		writeJSONValue(&buf, o.Exact)
	}
	if o.Errors != nil {
		writeKey("errors")
		writeJSONValue(&buf, o.Errors)
	}
	if o.Path != nil {
		writeKey("path")
		writeJSONValue(&buf, o.Path)
	} else {
		writeKey("path")
		buf.WriteString("[]")
	}
	writeKey("message")
	buf.WriteString(jsonString(o.Message))
	buf.WriteByte('}')
	return buf.Bytes(), nil
}

func writeJSONValue(buf *bytes.Buffer, v any) {
	enc := json.NewEncoder(buf)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(v)
	// Encode appends a newline; strip it.
	b := buf.Bytes()
	if len(b) > 0 && b[len(b)-1] == '\n' {
		buf.Truncate(len(b) - 1)
	}
}
