// Package projection ports the TypeScript ProductProjection
// (apps/nex-cli/packages/bootstrap/src/nex-protocol-v4/product-projection.ts):
// it turns a stream of SessionEvent facts into the ConversationDelta ops that a
// v4 conversation subscriber receives, and exposes the ConversationSnapshot the
// projection maintains.
//
// The protocol model lives in packages/shared/src/nex-protocol-v4: deltas are a
// closed set (row.appended / row.upserted / row.removed / row.delta /
// state.updated and the two workflowRun.* ops), state is key-level replacement
// (never a deep merge), and the snapshot is the authority the deltas advance.
//
// Wire fidelity: the deltas this package emits must serialize byte-for-byte like
// the TypeScript ones, including JSON key order (TS object-literal insertion
// order). Go maps sort keys, so every object is built as an ordered key/value
// list (obj) rather than a struct or map; subtrees the projection does not model
// are carried through as json.RawMessage.
package projection

import (
	"bytes"
	"encoding/json"
)

// obj is a JSON object whose keys keep insertion order. It is the serialization
// unit of this package: field order mirrors the TypeScript object literals, which
// is what makes the output byte-identical.
type obj struct {
	keys []string
	vals []any
}

func newObj() *obj { return &obj{} }

// set appends a key/value pair. Callers must not set the same key twice; the
// TypeScript sources build each object literal once, and a duplicate would fold
// the fields in a way no literal does.
func (o *obj) set(key string, value any) *obj {
	o.keys = append(o.keys, key)
	o.vals = append(o.vals, value)
	return o
}

// put updates key in place when it already exists (keeping its position) and
// otherwise appends it. It models the `{...snapshot, key: value}` spread the TS
// uses to advance the snapshot: existing keys keep their original order.
func (o *obj) put(key string, value any) *obj {
	for i, k := range o.keys {
		if k == key {
			o.vals[i] = value
			return o
		}
	}
	return o.set(key, value)
}

// get returns the value stored under key, or nil.
func (o *obj) get(key string) any {
	for i, k := range o.keys {
		if k == key {
			return o.vals[i]
		}
	}
	return nil
}

// has reports whether key is present.
func (o *obj) has(key string) bool {
	for _, k := range o.keys {
		if k == key {
			return true
		}
	}
	return false
}

// del removes key, keeping the order of the remaining keys.
func (o *obj) del(key string) *obj {
	for i, k := range o.keys {
		if k == key {
			o.keys = append(o.keys[:i], o.keys[i+1:]...)
			o.vals = append(o.vals[:i], o.vals[i+1:]...)
			return o
		}
	}
	return o
}

// setIf appends key/value only when present is true. It models the
// `...(cond ? { key: value } : {})` spread pattern used throughout the TS.
func (o *obj) setIf(present bool, key string, value any) *obj {
	if present {
		o.set(key, value)
	}
	return o
}

func (o *obj) MarshalJSON() ([]byte, error) {
	var buf bytes.Buffer
	buf.WriteByte('{')
	for i, key := range o.keys {
		if i > 0 {
			buf.WriteByte(',')
		}
		k, err := json.Marshal(key)
		if err != nil {
			return nil, err
		}
		buf.Write(k)
		buf.WriteByte(':')
		v, err := marshalValue(o.vals[i])
		if err != nil {
			return nil, err
		}
		buf.Write(v)
	}
	buf.WriteByte('}')
	return buf.Bytes(), nil
}

// marshalValue marshals a value with the same escaping rules as JSON.stringify
// for the value kinds this package produces. json.RawMessage passes through
// unchanged (so carried subtrees keep their own byte order).
func marshalValue(v any) ([]byte, error) {
	switch value := v.(type) {
	case nil:
		return []byte("null"), nil
	case *obj:
		return value.MarshalJSON()
	case json.RawMessage:
		return value, nil
	default:
		return json.Marshal(value)
	}
}

// rawOrNull returns raw if it holds any bytes, otherwise the JSON null literal.
func rawOrNull(raw json.RawMessage) json.RawMessage {
	if len(raw) == 0 {
		return json.RawMessage("null")
	}
	return raw
}

// compactRaw re-serializes a carried subtree with no insignificant whitespace, so
// a pretty-printed source becomes the compact form JSON.stringify produces. Key
// order is preserved (json.Compact only removes whitespace).
func compactRaw(raw json.RawMessage) json.RawMessage {
	if len(raw) == 0 {
		return raw
	}
	var buf bytes.Buffer
	if err := json.Compact(&buf, raw); err != nil {
		return raw
	}
	return json.RawMessage(buf.Bytes())
}

// decodeValue reads one JSON value as an ordered obj, []any, or scalar. Numbers
// stay json.Number so their textual form round-trips unchanged.
func decodeValue(dec *json.Decoder) (any, error) {
	tok, err := dec.Token()
	if err != nil {
		return nil, err
	}
	switch t := tok.(type) {
	case json.Delim:
		switch t {
		case '{':
			o := newObj()
			for dec.More() {
				keyTok, err := dec.Token()
				if err != nil {
					return nil, err
				}
				key, _ := keyTok.(string)
				value, err := decodeValue(dec)
				if err != nil {
					return nil, err
				}
				o.set(key, value)
			}
			// consume closing '}'
			if _, err := dec.Token(); err != nil {
				return nil, err
			}
			return o, nil
		case '[':
			items := []any{}
			for dec.More() {
				value, err := decodeValue(dec)
				if err != nil {
					return nil, err
				}
				items = append(items, value)
			}
			if _, err := dec.Token(); err != nil {
				return nil, err
			}
			return items, nil
		}
		return nil, nil
	default:
		return tok, nil
	}
}
