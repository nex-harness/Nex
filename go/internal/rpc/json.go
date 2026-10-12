package rpc

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"sort"
	"unicode/utf8"
)

// Tag 5 is the JSON fallback of the TS codec. Writing goes through
// `JSON.stringify(data, encodeRpcJsonValue)`, whose replacer turns every Uint8Array in the
// tree into `{"__nex_rpc_nested_uint8array_v1":true,"base64":"…"}` and whose number and
// string formatting is ECMAScript's, not Go's. The decode half lives in json_decode.go.

// nestedMarkerKey marks a Uint8Array embedded inside an object's JSON fallback, exactly as
// RPC_NESTED_UINT8_ARRAY_MARKER in the TypeScript codec.
const (
	nestedMarkerKey = "__nex_rpc_nested_uint8array_v1"
	nestedBase64Key = "base64"
)

// marshalJSONValue renders value the way JSON.stringify does. Object members keep insertion
// order (JSON.stringify semantics); a Go map has none, so its keys are sorted and the
// difference shows in the bytes rather than being hidden.
func marshalJSONValue(value Value) ([]byte, error) {
	var buf bytes.Buffer
	if err := writeJSONValue(&buf, value, 0); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func writeJSONValue(buf *bytes.Buffer, value Value, depth int) error {
	if depth > DefaultMaxDepth {
		return ErrTooDeep
	}
	switch v := value.(type) {
	case nil:
		buf.WriteString("null")
		return nil
	case undefinedValue:
		// JSON.stringify(undefined) has no JSON spelling: inside an array it becomes null,
		// as an object member the key is dropped (handled by the object writers). A
		// top-level Undefined never reaches here — Serialize tags it 0 before the fallback.
		buf.WriteString("null")
		return nil
	case []byte:
		writeNestedMarker(buf, v)
		return nil
	case Buffer:
		// VSBuffer is not a Uint8Array in JS, so it is NOT marked; JSON.stringify emits its
		// two own enumerable properties instead. Reached only when a Buffer is nested
		// inside a larger structure rather than serialized on its own.
		return writeOrderedObject(buf, Object{Keys: []string{"buffer", "byteLength"},
			Values: []Value{[]byte(v), int64(len(v))}}, depth+1)
	case string:
		writeJSONString(buf, v)
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
	case float32:
		buf.Write(appendJSNumber(nil, float64(v)))
		return nil
	case float64:
		buf.Write(appendJSNumber(nil, v))
		return nil
	case int:
		buf.Write(appendJSNumber(nil, float64(v)))
		return nil
	case int32:
		buf.Write(appendJSNumber(nil, float64(v)))
		return nil
	case int64:
		buf.Write(appendJSNumber(nil, float64(v)))
		return nil
	case []Value:
		return writeJSONArray(buf, v, depth+1)
	case Object:
		return writeOrderedObject(buf, v, depth+1)
	case map[string]any:
		return writeJSONMap(buf, v, depth+1)
	}
	// Unknown Go shapes (structs, typed slices, pointers) go through encoding/json so the
	// codec stays total instead of panicking on an unsupported type.
	encoded, err := json.Marshal(value)
	if err != nil {
		return err
	}
	buf.Write(encoded)
	return nil
}

func writeNestedMarker(buf *bytes.Buffer, data []byte) {
	buf.WriteString(`{"` + nestedMarkerKey + `":true,"` + nestedBase64Key + `":"`)
	buf.WriteString(base64.StdEncoding.EncodeToString(data))
	buf.WriteString(`"}`)
}

// writeJSONString emits a JSON string literal with JS escaping.
//
// Go's encoding/json additionally escapes <, >, & and U+2028/U+2029 (HTML escaping), which
// would NOT match the bytes JSON.stringify produced, so the literal is built by hand:
// escape ", \\, \b, \f, \n, \r, \t and C0 controls; everything else is copied verbatim.
func writeJSONArray(buf *bytes.Buffer, items []Value, depth int) error {
	if depth > DefaultMaxDepth {
		return ErrTooDeep
	}
	buf.WriteByte('[')
	for i, item := range items {
		if i > 0 {
			buf.WriteByte(',')
		}
		if err := writeJSONValue(buf, item, depth+1); err != nil {
			return err
		}
	}
	buf.WriteByte(']')
	return nil
}

// writeOrderedObject emits an Object's members in insertion order, which is what
// JSON.stringify does and what the recorded vectors pin.
func writeOrderedObject(buf *bytes.Buffer, obj Object, depth int) error {
	if depth > DefaultMaxDepth {
		return ErrTooDeep
	}
	buf.WriteByte('{')
	first := true
	for i, key := range obj.Keys {
		if IsUndefined(obj.Values[i]) {
			// JSON.stringify drops an object member whose value is undefined.
			continue
		}
		if !first {
			buf.WriteByte(',')
		}
		first = false
		writeJSONString(buf, key)
		buf.WriteByte(':')
		if err := writeJSONValue(buf, obj.Values[i], depth+1); err != nil {
			return err
		}
	}
	buf.WriteByte('}')
	return nil
}

// writeJSONMap emits a map's members in sorted key order: a Go map has no insertion order,
// so the result is deterministic but may differ from the JS bytes for a map that mirrors a
// differently-ordered JS object. Callers that need JS parity use Object.
func writeJSONMap(buf *bytes.Buffer, obj map[string]any, depth int) error {
	if depth > DefaultMaxDepth {
		return ErrTooDeep
	}
	keys := make([]string, 0, len(obj))
	for key := range obj {
		if IsUndefined(obj[key]) {
			continue
		}
		keys = append(keys, key)
	}
	sort.Strings(keys)
	buf.WriteByte('{')
	for i, key := range keys {
		if i > 0 {
			buf.WriteByte(',')
		}
		writeJSONString(buf, key)
		buf.WriteByte(':')
		if err := writeJSONValue(buf, obj[key], depth+1); err != nil {
			return err
		}
	}
	buf.WriteByte('}')
	return nil
}

// writeJSONString emits a JSON string literal with JS escaping.
//
// Go's encoding/json additionally escapes <, >, & and U+2028/U+2029 (HTML escaping), which
// would NOT match the bytes JSON.stringify produced, so the literal is built by hand:
// escape ", \, \b, \f, \n, \r, \t and C0 controls; everything else is copied verbatim.
func writeJSONString(buf *bytes.Buffer, s string) {
	buf.WriteByte('"')
	const hexDigits = "0123456789abcdef"
	for i := 0; i < len(s); {
		c := s[i]
		if c < utf8.RuneSelf {
			switch c {
			case '"', '\\':
				buf.WriteByte('\\')
				buf.WriteByte(c)
			case '\b':
				buf.WriteString(`\b`)
			case '\f':
				buf.WriteString(`\f`)
			case '\n':
				buf.WriteString(`\n`)
			case '\r':
				buf.WriteString(`\r`)
			case '\t':
				buf.WriteString(`\t`)
			default:
				if c < 0x20 {
					buf.WriteString(`\u00`)
					buf.WriteByte(hexDigits[c>>4])
					buf.WriteByte(hexDigits[c&0xf])
				} else {
					buf.WriteByte(c)
				}
			}
			i++
			continue
		}
		r, size := utf8.DecodeRuneInString(s[i:])
		if r == utf8.RuneError && size == 1 {
			// Invalid UTF-8 (a lone surrogate once it reaches Go as a []byte) becomes the
			// replacement character, matching what JSON.stringify emits.
			buf.WriteRune(utf8.RuneError)
			i++
			continue
		}
		buf.WriteString(s[i : i+size])
		i += size
	}
	buf.WriteByte('"')
}
