package rpcwire

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
)

// JSON bridge for the server's runner link.
//
// Buffers are not directly representable in JSON, so ToJSON encodes them as
//
//	{"$buf":"<standard base64>","$vs":true}
//
// with $vs reflecting the original wire tag (3 = VSBuffer, 2 = Uint8Array).
// Undefined becomes {"$undef":true}: a top-level bare `undefined` is not valid
// JSON and an absent key cannot be distinguished from a missing property, so
// the explicit sentinel keeps the mapping total and lossless.
//
// All other values map naturally: strings to JSON strings, tag 5 objects to
// their raw JSON text (never re-marshalled), arrays to JSON arrays, integers to
// JSON numbers. Object keys that collide with $buf/$vs/$undef are escaped by
// wrapping the whole object as {"$esc":{...}} so arbitrary wire JSON survives
// a round trip through the bridge.

const (
	bufKey   = "$buf"
	vsKey    = "$vs"
	undefKey = "$undef"
	escKey   = "$esc"
)

// ToJSON converts a decoded wire Value to JSON text for the runner link.
func ToJSON(v Value) (json.RawMessage, error) {
	encoded, err := toJSONValue(v)
	if err != nil {
		return nil, err
	}
	return json.RawMessage(encoded), nil
}

func toJSONValue(v Value) ([]byte, error) {
	switch x := v.(type) {
	case nil, Undefined:
		return []byte(`{"$undef":true}`), nil

	case string:
		return appendJSString(nil, x), nil

	case Buffer:
		var b bytes.Buffer
		b.WriteString(`{"$buf":"`)
		enc := base64.StdEncoding.EncodeToString(x.Data)
		b.WriteString(enc)
		b.WriteString(`","$vs":`)
		if x.VS {
			b.WriteString("true}")
		} else {
			b.WriteString("false}")
		}
		return b.Bytes(), nil

	case []byte:
		return toJSONValue(Buffer{Data: x})

	case []any:
		out := make([]byte, 0, 16)
		out = append(out, '[')
		for i, el := range x {
			if i > 0 {
				out = append(out, ',')
			}
			enc, err := toJSONValue(el)
			if err != nil {
				return nil, fmt.Errorf("array element %d: %w", i, err)
			}
			out = append(out, enc...)
		}
		return append(out, ']'), nil

	case json.RawMessage:
		if !json.Valid(x) {
			return nil, fmt.Errorf("%w: raw JSON is invalid", ErrInvalidValue)
		}
		if needsEscapeWrap(x) {
			return []byte(`{"$esc":` + string(x) + `}`), nil
		}
		return x, nil

	case bool:
		if x {
			return []byte("true"), nil
		}
		return []byte("false"), nil

	case int64:
		return appendIntJSON(nil, x), nil
	case int:
		return appendIntJSON(nil, int64(x)), nil
	case uint32:
		return appendIntJSON(nil, int64(x)), nil
	case uint64:
		return appendIntJSON(nil, int64(x)), nil
	}

	return nil, fmt.Errorf("%w: unsupported Go type %T", ErrInvalidValue, v)
}

func appendIntJSON(dst []byte, v int64) []byte {
	if v < 0 {
		dst = append(dst, '-')
		return appendUintJSON(dst, uint64(-v))
	}
	return appendUintJSON(dst, uint64(v))
}

func appendUintJSON(dst []byte, v uint64) []byte {
	var scratch [20]byte
	i := len(scratch)
	if v == 0 {
		return append(dst, '0')
	}
	for v > 0 {
		i--
		scratch[i] = byte('0' + v%10)
		v /= 10
	}
	return append(dst, scratch[i:]...)
}

// needsEscapeWrap reports whether raw JSON is a top-level object declaring one
// of the bridge's reserved keys.
func needsEscapeWrap(raw []byte) bool {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || trimmed[0] != '{' {
		return false
	}
	var probe map[string]json.RawMessage
	if err := json.Unmarshal(trimmed, &probe); err != nil {
		return false
	}
	for _, key := range []string{bufKey, vsKey, undefKey, escKey} {
		if _, ok := probe[key]; ok {
			return true
		}
	}
	return false
}

// FromJSON converts JSON text produced by ToJSON (or by the TypeScript side)
// back to a wire Value. Buffers and the undefined sentinel are recognised;
// every other value becomes a string, json.RawMessage (objects), []any or
// int64.
func FromJSON(raw json.RawMessage) (Value, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		return Undefined{}, nil
	}

	switch trimmed[0] {
	case '"':
		var s string
		if err := json.Unmarshal(trimmed, &s); err != nil {
			return nil, fmt.Errorf("%w: %v", ErrInvalidValue, err)
		}
		return s, nil
	case '[':
		var arr []json.RawMessage
		if err := json.Unmarshal(trimmed, &arr); err != nil {
			return nil, fmt.Errorf("%w: %v", ErrInvalidValue, err)
		}
		out := make([]any, len(arr))
		for i, el := range arr {
			v, err := FromJSON(el)
			if err != nil {
				return nil, fmt.Errorf("array element %d: %w", i, err)
			}
			out[i] = v
		}
		return out, nil
	case 't', 'f':
		var b bool
		if err := json.Unmarshal(trimmed, &b); err != nil {
			return nil, fmt.Errorf("%w: %v", ErrInvalidValue, err)
		}
		return b, nil
	case 'n':
		if string(trimmed) != "null" {
			return nil, fmt.Errorf("%w: invalid JSON literal %q", ErrInvalidValue, trimmed)
		}
		// JavaScript null is serialized by TS as a tag 5 body containing the
		// text "null"; Undefined uses the explicit {"$undef":true} sentinel,
		// so null stays distinct and lossless here.
		return json.RawMessage("null"), nil
	case '{':
		return fromJSONObject(trimmed)
	}

	var n int64
	if err := json.Unmarshal(trimmed, &n); err != nil {
		// Non-integer numbers stay as their raw JSON so no precision is lost.
		var f float64
		if err2 := json.Unmarshal(trimmed, &f); err2 != nil {
			return nil, fmt.Errorf("%w: %v", ErrInvalidValue, err)
		}
		return json.RawMessage(append([]byte(nil), trimmed...)), nil
	}
	return n, nil
}

func fromJSONObject(raw []byte) (Value, error) {
	var probe map[string]json.RawMessage
	if err := json.Unmarshal(raw, &probe); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidValue, err)
	}

	if esc, ok := probe[escKey]; ok && len(probe) == 1 {
		return json.RawMessage(append([]byte(nil), esc...)), nil
	}
	if _, ok := probe[undefKey]; ok && len(probe) == 1 {
		return Undefined{}, nil
	}
	if buf, ok := probe[bufKey]; ok && len(probe) >= 1 && len(probe) <= 2 {
		var text string
		if err := json.Unmarshal(buf, &text); err != nil {
			return nil, fmt.Errorf("%w: %s is not a string", ErrInvalidValue, bufKey)
		}
		data, err := base64.StdEncoding.DecodeString(text)
		if err != nil {
			return nil, fmt.Errorf("%w: invalid base64 buffer: %v", ErrInvalidValue, err)
		}
		vs := true
		if rawVS, ok := probe[vsKey]; ok {
			if err := json.Unmarshal(rawVS, &vs); err != nil {
				return nil, fmt.Errorf("%w: %s is not a boolean", ErrInvalidValue, vsKey)
			}
		}
		return Buffer{Data: data, VS: vs}, nil
	}

	return json.RawMessage(append([]byte(nil), raw...)), nil
}
