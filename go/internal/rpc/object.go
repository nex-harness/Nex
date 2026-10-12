package rpc

// Object is an ordered JSON object. The TypeScript codec serializes objects with
// `JSON.stringify`, which preserves insertion order; a Go map cannot, so the Go codec uses
// this type wherever byte-for-byte compatibility matters.
type Object struct {
	Keys   []string
	Values []any
}

// NewObject builds an ordered object from alternating key/value pairs.
func NewObject(pairs ...any) Object {
	obj := Object{}
	for i := 0; i+1 < len(pairs); i += 2 {
		obj.Keys = append(obj.Keys, pairs[i].(string))
		obj.Values = append(obj.Values, pairs[i+1])
	}
	return obj
}

// Get returns the value for a key.
func (o Object) Get(key string) (any, bool) {
	for i, k := range o.Keys {
		if k == key {
			return o.Values[i], true
		}
	}
	return nil, false
}

// Set replaces or appends a key while preserving order.
func (o *Object) Set(key string, value any) {
	for i, k := range o.Keys {
		if k == key {
			o.Values[i] = value
			return
		}
	}
	o.Keys = append(o.Keys, key)
	o.Values = append(o.Values, value)
}

// Len reports the number of members.
func (o Object) Len() int { return len(o.Keys) }

// indexOf returns the slot of key, or -1. The decoder uses it to implement JSON.parse's
// duplicate-key rule: the later value wins but the original position is kept.
func (o Object) indexOf(key string) int {
	for i, k := range o.Keys {
		if k == key {
			return i
		}
	}
	return -1
}
