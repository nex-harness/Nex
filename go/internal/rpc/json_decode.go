package rpc

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
	"unicode/utf8"
)

// decodeObjectJSON parses an Object-tag payload, preserving member order, and applies the
// inverse of the nested marker exactly as `decodeRpcJsonValue` does: an object with exactly
// the two marker keys (marker === true and a string base64) becomes a []byte.
//
// Numbers stay json.Number (decoder.UseNumber), so a JSON integer is not silently widened
// into a float64: 1e400 keeps its literal text and re-marshals to the same bytes, which a
// float64 (either Inf or 0) could not.
func decodeObjectJSON(raw []byte, maxDepth, prealloc int) (Value, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	return decodeJSONValue(decoder, maxDepth, prealloc, 0)
}

func decodeJSONValue(decoder *json.Decoder, maxDepth, prealloc, depth int) (Value, error) {
	if depth >= maxDepth {
		return nil, ErrTooDeep
	}
	token, err := decoder.Token()
	if err != nil {
		return nil, err
	}
	delim, isDelim := token.(json.Delim)
	if !isDelim {
		// Scalars arrive verbatim: string, bool, nil, or json.Number.
		return token, nil
	}
	switch delim {
	case '{':
		return decodeJSONObject(decoder, maxDepth, prealloc, depth)
	case '[':
		return decodeJSONArray(decoder, maxDepth, prealloc, depth)
	}
	return nil, fmt.Errorf("rpc: unexpected delimiter %v", delim)
}

func decodeJSONObject(decoder *json.Decoder, maxDepth, prealloc, depth int) (Value, error) {
	var obj Object
	for decoder.More() {
		keyToken, err := decoder.Token()
		if err != nil {
			return nil, err
		}
		key, _ := keyToken.(string)
		value, err := decodeJSONValue(decoder, maxDepth, prealloc, depth+1)
		if err != nil {
			return nil, err
		}
		// A duplicate key overwrites the earlier member but keeps its slot, which is how
		// JSON.parse builds the object — and Object.keys then reports the deduped count,
		// on which isRpcEncodedUint8Array depends.
		if existing := obj.indexOf(key); existing >= 0 {
			obj.Values[existing] = value
			continue
		}
		obj.Keys = append(obj.Keys, key)
		obj.Values = append(obj.Values, value)
	}
	if _, err := decoder.Token(); err != nil { // consume '}'
		return nil, err
	}
	if data, ok := markerToBytes(obj); ok {
		return data, nil
	}
	return obj, nil
}

func decodeJSONArray(decoder *json.Decoder, maxDepth, prealloc, depth int) (Value, error) {
	capacity := prealloc
	if capacity > 16 {
		capacity = 16
	}
	out := make([]Value, 0, capacity)
	for decoder.More() {
		value, err := decodeJSONValue(decoder, maxDepth, prealloc, depth+1)
		if err != nil {
			return nil, err
		}
		out = append(out, value)
	}
	if _, err := decoder.Token(); err != nil { // consume ']'
		return nil, err
	}
	return out, nil
}

// markerToBytes reports whether an ordered object is exactly the nested marker, matching
// `isRpcEncodedUint8Array`: two members, marker === true, base64 a string.
func markerToBytes(obj Object) ([]byte, bool) {
	if obj.Len() != 2 {
		return nil, false
	}
	marker, ok := obj.Get(nestedMarkerKey)
	if !ok {
		return nil, false
	}
	if b, ok := marker.(bool); !ok || !b {
		return nil, false
	}
	encoded, ok := obj.Get(nestedBase64Key)
	if !ok {
		return nil, false
	}
	s, ok := encoded.(string)
	if !ok {
		return nil, false
	}
	// Node's Buffer.from(s, "base64") is lenient: it skips characters outside the
	// alphabet (whitespace, '-', '_') and tolerates missing or excess padding, so the
	// marker round-trips instead of turning into an error.
	decoded, ok := LenientBase64(s)
	if !ok {
		return nil, false
	}
	return decoded, true
}

// LenientBase64 decodes base64 the way Node's `Buffer.from(input, "base64")` does:
// characters outside the alphabet are skipped (and '-'/'_' are accepted as the URL-safe
// aliases of '+' and '/'), padding is optional, and a trailing partial group is truncated
// rather than rejected.
func LenientBase64(s string) ([]byte, bool) {
	cleaned := make([]byte, 0, len(s))
	for i := 0; i < len(s); i++ {
		switch c := s[i]; {
		case c >= 'A' && c <= 'Z', c >= 'a' && c <= 'z', c >= '0' && c <= '9',
			c == '+', c == '/':
			cleaned = append(cleaned, c)
		case c == '-':
			cleaned = append(cleaned, '+')
		case c == '_':
			cleaned = append(cleaned, '/')
		case c == '=':
			// Padding ends the payload in Node: "QQ==QQ" decodes to "A", not to two
			// separate groups, so stop instead of skipping the '=' like noise.
			i = len(s)
		}
		// Any other byte (whitespace, control) is dropped, as Node does.
	}
	if len(cleaned) == 0 {
		return []byte{}, true
	}
	// RawStdEncoding (not StdEncoding) because Node tolerates missing padding. A trailing
	// group of a single character carries no whole byte and is dropped, which is what
	// Node does for "ABCDE" and "A"; RawStdEncoding rejects it, so it is trimmed first.
	if remainder := len(cleaned) % 4; remainder == 1 {
		cleaned = cleaned[:len(cleaned)-1]
		if len(cleaned) == 0 {
			return []byte{}, true
		}
	}
	out, err := base64.RawStdEncoding.DecodeString(string(cleaned))
	if err != nil {
		return nil, false
	}
	return out, true
}

// decodeUTF8 converts bytes to a string the way TextDecoder (used by VSBuffer.toString)
// does: invalid sequences become U+FFFD rather than failing the whole value.
func decodeUTF8(raw []byte) string {
	if utf8.Valid(raw) {
		return string(raw)
	}
	var sb strings.Builder
	sb.Grow(len(raw))
	for i := 0; i < len(raw); {
		r, size := utf8.DecodeRune(raw[i:])
		sb.WriteRune(r)
		i += size
	}
	return sb.String()
}
