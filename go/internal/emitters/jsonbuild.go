package emitters

import (
	"encoding/json"

	"github.com/tylinux/nex/go/internal/v4pub"
)

// objBuilder accumulates ordered members for a JSON object, mirroring the
// TypeScript object-literal-then-spread construction order. Members are
// appended in the order the TypeScript literal lists them, so the bytes match
// JSON.stringify of the same literal.
type objBuilder struct {
	keys []string
	vals []v4pub.JSONValue
}

func newObj() *objBuilder { return &objBuilder{} }

// set appends a member. Callers must not set the same key twice (the TS builds
// each literal once).
func (o *objBuilder) set(key string, v v4pub.JSONValue) *objBuilder {
	o.keys = append(o.keys, key)
	o.vals = append(o.vals, v)
	return o
}

// setIf appends key/value only when present (models `...(cond ? {k:v} : {})`).
func (o *objBuilder) setIf(present bool, key string, v v4pub.JSONValue) *objBuilder {
	if present {
		o.set(key, v)
	}
	return o
}

// setStr appends a string member only when present and non-empty.
func (o *objBuilder) setStr(present bool, key, s string) *objBuilder {
	if present {
		o.set(key, v4pub.Str(s))
	}
	return o
}

func (o *objBuilder) boolean(present bool, key string, b bool) *objBuilder {
	if present {
		o.set(key, v4pub.Bool(b))
	}
	return o
}

func (o *objBuilder) integer(present bool, key string, n int64) *objBuilder {
	if present {
		o.set(key, v4pub.Int(n))
	}
	return o
}

func (o *objBuilder) number(present bool, key string, f float64) *objBuilder {
	if present {
		o.set(key, v4pub.Float(f))
	}
	return o
}

// value assembles the object as a v4pub.JSONValue (ordered, via raw bytes).
func (o *objBuilder) value() v4pub.JSONValue {
	return v4pub.RawMessageOf(objFromPairs(o.keys, o.vals))
}

// raw renders the object with JSON.stringify-compatible bytes.
func (o *objBuilder) raw() json.RawMessage {
	return objFromPairs(o.keys, o.vals)
}

// objFromPairs serialises an ordered key/value list as a JSON object using the
// JSONValue marshaller for values and JSON-compatible key escaping. Keys are
// ASCII object-literal fields in every call site, so json.Marshal(key) matches
// JSON.stringify for them.
func objFromPairs(keys []string, vals []v4pub.JSONValue) json.RawMessage {
	buf := make([]byte, 0, 64)
	buf = append(buf, '{')
	for i, k := range keys {
		if i > 0 {
			buf = append(buf, ',')
		}
		kb, _ := json.Marshal(k)
		buf = append(buf, kb...)
		buf = append(buf, ':')
		vb, err := vals[i].MarshalJSON()
		if err != nil {
			vb = []byte("null")
		}
		buf = append(buf, vb...)
	}
	buf = append(buf, '}')
	return buf
}

// arr builds an ordered array value.
func arr(items ...v4pub.JSONValue) v4pub.JSONValue { return v4pub.Arr(items...) }

func rawValue(raw json.RawMessage) v4pub.JSONValue { return v4pub.RawMessageOf(raw) }
