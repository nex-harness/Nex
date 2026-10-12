package contracts

import (
	"bytes"
	"encoding/json"
	"fmt"
)

// ValidateJSON decodes raw JSON and validates it against the named schema.
//
// It uses encoding/json's default decoding, which matches what the Go agent sees on the
// wire (numbers as float64). Use `json.Decoder.UseNumber` callers should pre-decode and
// call Validate instead.
func ValidateJSON(schemaKey string, raw []byte) error {
	node, ok := Registry.Lookup(schemaKey)
	if !ok {
		return fmt.Errorf("unknown schema %q", schemaKey)
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return fmt.Errorf("invalid JSON: %w", err)
	}
	// A trailing token means the input is not a single JSON value.
	if decoder.More() {
		return fmt.Errorf("trailing data after JSON value")
	}
	return Validate(node, normalizeNumbers(value))
}

// normalizeNumbers converts json.Number to float64 so validation sees the same shape as
// a decoded protocol frame. Numbers outside float64 range become +Inf, which the number
// validator rejects (matching JSON.parse in JS, which yields Infinity for 1e400 tokens).
func normalizeNumbers(value any) any {
	switch v := value.(type) {
	case json.Number:
		f, err := v.Float64()
		if err != nil {
			return v.String()
		}
		return f
	case []any:
		for i := range v {
			v[i] = normalizeNumbers(v[i])
		}
		return v
	case map[string]any:
		for key := range v {
			v[key] = normalizeNumbers(v[key])
		}
		return v
	default:
		return value
	}
}
