package rpc

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strconv"
)

// marshalWithMarker encodes a value the way `JSON.stringify(value, encodeRpcJsonValue)`
// does: a Uint8Array anywhere in the tree becomes
// `{"__nex_rpc_nested_uint8array_v1":true,"base64":"<base64>"}`.
//
// Go's encoding/json has no replacer hook, so the tree is walked explicitly. Key order
// matches JSON.stringify: object keys are emitted in insertion/sorted order — the TS
// objects reaching this path come from protocol messages whose key order is fixed by the
// sender; the vectors pin the exact bytes the Go side must match.
func marshalWithMarker(value any) ([]byte, error) {
	var buf bytes.Buffer
	if err := writeJSON(&buf, value); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func writeJSON(buf *bytes.Buffer, value any) error {
	switch v := value.(type) {
	case nil:
		buf.WriteString("null")
		return nil
	case []byte:
		return writeMarker(buf, v)
	case string:
		encoded, _ := json.Marshal(v)
		buf.Write(encoded)
		return nil
	case bool:
		if v {
			buf.WriteString("true")
		} else {
			buf.WriteString("false")
		}
		return nil
	case json.Number:
		buf.WriteString(v.String())
		return nil
	case float64:
		writeNumber(buf, v)
		return nil
	case int:
		buf.WriteString(strconv.Itoa(v))
		return nil
	case int32:
		buf.WriteString(strconv.FormatInt(int64(v), 10))
		return nil
	case int64:
		buf.WriteString(strconv.FormatInt(v, 10))
		return nil
	case []any:
		return writeArray(buf, v)
	case Object:
		return writeOrderedObjectJSON(buf, v)
	case map[string]any:
		return writeMap(buf, v)
	default:
		// Fall back to encoding/json for shapes we do not model explicitly.
		encoded, err := json.Marshal(v)
		if err != nil {
			return err
		}
		buf.Write(encoded)
		return nil
	}
}

func writeNumber(buf *bytes.Buffer, v float64) {
	if math.IsNaN(v) || math.IsInf(v, 0) {
		buf.WriteString("null")
		return
	}
	encoded, _ := json.Marshal(v)
	buf.Write(encoded)
}

func writeMarker(buf *bytes.Buffer, data []byte) error {
	buf.WriteString(`{"` + nestedMarkerKey + `":true,"` + nestedBase64Key + `":"`)
	buf.WriteString(base64.StdEncoding.EncodeToString(data))
	buf.WriteString(`"}`)
	return nil
}

func writeArray(buf *bytes.Buffer, items []any) error {
	buf.WriteByte('[')
	for i, item := range items {
		if i > 0 {
			buf.WriteByte(',')
		}
		if err := writeJSON(buf, item); err != nil {
			return err
		}
	}
	buf.WriteByte(']')
	return nil
}

// writeOrderedObjectJSON emits an Object's members in insertion order.
func writeOrderedObjectJSON(buf *bytes.Buffer, obj Object) error {
	buf.WriteByte('{')
	for i, key := range obj.Keys {
		if i > 0 {
			buf.WriteByte(',')
		}
		encodedKey, _ := json.Marshal(key)
		buf.Write(encodedKey)
		buf.WriteByte(':')
		if err := writeJSON(buf, obj.Values[i]); err != nil {
			return err
		}
	}
	buf.WriteByte('}')
	return nil
}

func writeMap(buf *bytes.Buffer, obj map[string]any) error {
	// JSON.stringify preserves object insertion order; Go maps do not. Sort keys so the
	// output is deterministic. The wire vectors were produced from objects whose keys
	// happened to be ordered; any mismatch is surfaced by the vector test rather than
	// hidden (see rpc_test.go, which compares decoded values, not object byte order, for
	// objects).
	keys := make([]string, 0, len(obj))
	for key := range obj {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	buf.WriteByte('{')
	for i, key := range keys {
		if i > 0 {
			buf.WriteByte(',')
		}
		encodedKey, _ := json.Marshal(key)
		buf.Write(encodedKey)
		buf.WriteByte(':')
		if err := writeJSON(buf, obj[key]); err != nil {
			return err
		}
	}
	buf.WriteByte('}')
	return nil
}

// decodeObjectJSON parses an Object-tag payload, preserving member order, and applies the
// inverse of the nested marker exactly as `decodeRpcJsonValue` does: any object with
// exactly the two marker keys (marker === true and a string base64) becomes a []byte.
func decodeObjectJSON(raw []byte) (any, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	value, err := decodeValue(decoder)
	if err != nil {
		return nil, err
	}
	return value, nil
}

// decodeValue reads one JSON value, returning Object (ordered) for objects.
func decodeValue(decoder *json.Decoder) (any, error) {
	token, err := decoder.Token()
	if err != nil {
		return nil, err
	}
	switch t := token.(type) {
	case json.Delim:
		switch t {
		case '{':
			return decodeObjectToken(decoder)
		case '[':
			return decodeArrayToken(decoder)
		}
		return nil, fmt.Errorf("rpc: unexpected delimiter %v", t)
	default:
		return normalizeScalar(token), nil
	}
}

func decodeObjectToken(decoder *json.Decoder) (any, error) {
	obj := Object{}
	for decoder.More() {
		keyToken, err := decoder.Token()
		if err != nil {
			return nil, err
		}
		key, _ := keyToken.(string)
		value, err := decodeValue(decoder)
		if err != nil {
			return nil, err
		}
		obj.Keys = append(obj.Keys, key)
		obj.Values = append(obj.Values, value)
	}
	if _, err := decoder.Token(); err != nil { // consume '}'
		return nil, err
	}
	if data, ok := markerToBytesOrdered(obj); ok {
		return data, nil
	}
	return obj, nil
}

func decodeArrayToken(decoder *json.Decoder) (any, error) {
	out := []any{}
	for decoder.More() {
		value, err := decodeValue(decoder)
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

func normalizeScalar(token any) any {
	switch v := token.(type) {
	case json.Number:
		// The TS decoder returns a JS number for every JSON number. Go keeps float64 to
		// match that shape (int32 is reserved for the Int tag).
		f, err := v.Float64()
		if err != nil {
			return v.String()
		}
		return f
	default:
		return token
	}
}

// markerToBytesOrdered reports whether an ordered object is exactly the nested marker.
func markerToBytesOrdered(obj Object) ([]byte, bool) {
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
	decoded, err := base64.StdEncoding.DecodeString(s)
	if err != nil {
		return nil, false
	}
	return decoded, true
}
