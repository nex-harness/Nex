package emitters

import (
	"encoding/json"

	"github.com/tylinux/nex/go/internal/projection"
)

// payloadMap decodes an event payload into key -> raw JSON. A malformed payload
// yields an empty map (the TypeScript recordValue treats non-objects as {}).
func payloadMap(ev projection.Event) map[string]json.RawMessage {
	if len(ev.Payload) == 0 {
		return map[string]json.RawMessage{}
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(ev.Payload, &m); err != nil || m == nil {
		return map[string]json.RawMessage{}
	}
	return m
}

// str reads a string field. Present-but-not-a-string is treated as absent,
// matching the TS `typeof x === "string"` guards.
func str(m map[string]json.RawMessage, key string) (string, bool) {
	raw, ok := m[key]
	if !ok {
		return "", false
	}
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return "", false
	}
	return s, true
}

// nonEmpty reads a string field and requires length > 0 (optionalString).
func nonEmpty(m map[string]json.RawMessage, key string) (string, bool) {
	s, ok := str(m, key)
	if !ok || len(s) == 0 {
		return "", false
	}
	return s, true
}

func isJSONNull(raw json.RawMessage) bool {
	for _, b := range raw {
		switch b {
		case ' ', '\t', '\n', '\r':
			continue
		case 'n':
			return true
		default:
			return false
		}
	}
	return false
}

// num reads a finite number field (any numeric JSON value).
func num(m map[string]json.RawMessage, key string) (float64, bool) {
	raw, ok := m[key]
	if !ok || isJSONNull(raw) {
		return 0, false
	}
	var f float64
	if err := json.Unmarshal(raw, &f); err != nil {
		return 0, false
	}
	return f, true
}

// nonNeg mirrors the TS nonNegative: a finite number >= 0.
func nonNeg(m map[string]json.RawMessage, key string) (float64, bool) {
	f, ok := num(m, key)
	if !ok || f < 0 {
		return 0, false
	}
	return f, true
}

func integer(m map[string]json.RawMessage, key string) (int64, bool) {
	f, ok := num(m, key)
	if !ok {
		return 0, false
	}
	return int64(f), true
}

func boolField(m map[string]json.RawMessage, key string) (bool, bool) {
	raw, ok := m[key]
	if !ok || isJSONNull(raw) {
		return false, false
	}
	var b bool
	if err := json.Unmarshal(raw, &b); err != nil {
		return false, false
	}
	return b, true
}

// record decodes a nested object value; a missing/non-object value yields {}.
func record(m map[string]json.RawMessage, key string) map[string]json.RawMessage {
	raw, ok := m[key]
	if !ok || isJSONNull(raw) {
		return map[string]json.RawMessage{}
	}
	var out map[string]json.RawMessage
	if err := json.Unmarshal(raw, &out); err != nil || out == nil {
		return map[string]json.RawMessage{}
	}
	return out
}
