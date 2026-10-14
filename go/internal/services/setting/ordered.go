// ordered.go: an insertion-ordered JSON object. setting.json bytes are part of
// the differential contract (JS JSON.stringify preserves insertion order), so
// the settings store writes members in the order the TS produces them: legacy
// retained fields first (probe16), then the schema field order.

package setting

import (
	"bytes"
	"encoding/json"
)

// Ordered is a string-keyed map that remembers insertion order.
type Ordered struct {
	keys   []string
	values map[string]any
}

// NewOrdered builds an empty ordered object.
func NewOrdered() *Ordered {
	return &Ordered{values: map[string]any{}}
}

// Set replaces an existing key in place or appends it.
func (o *Ordered) Set(key string, value any) {
	if _, exists := o.values[key]; !exists {
		o.keys = append(o.keys, key)
	}
	o.values[key] = value
}

// Get returns the value for key.
func (o *Ordered) Get(key string) (any, bool) {
	v, ok := o.values[key]
	return v, ok
}

// Has reports whether the key was set.
func (o *Ordered) Has(key string) bool {
	_, ok := o.values[key]
	return ok
}

// Delete removes the key (no-op when absent).
func (o *Ordered) Delete(key string) {
	if _, ok := o.values[key]; !ok {
		return
	}
	delete(o.values, key)
	for i, k := range o.keys {
		if k == key {
			o.keys = append(o.keys[:i], o.keys[i+1:]...)
			return
		}
	}
}

// Keys returns the insertion order.
func (o *Ordered) Keys() []string { return o.keys }

// Map exposes a plain map view (for validators that don't care about order).
func (o *Ordered) Map() map[string]any { return o.values }

// Clone deep-copies via JSON round-trip (settings values are JSON scalars).
func (o *Ordered) Clone() *Ordered {
	out := NewOrdered()
	for _, k := range o.keys {
		out.Set(k, deepCloneJSON(o.values[k]))
	}
	return out
}

// OrderedFromMap builds an Ordered from a map with keys in fieldOrder (fields
// outside fieldOrder keep map-iteration-independent sorted order at the tail,
// which cannot happen for validated settings but keeps the function total).
func OrderedFromMap(m map[string]any, fieldOrder []string) *Ordered {
	out := NewOrdered()
	seen := map[string]bool{}
	for _, k := range fieldOrder {
		if v, ok := m[k]; ok {
			out.Set(k, v)
			seen[k] = true
		}
	}
	// Deterministic tail for unknown keys (defensive; validation strips them).
	rest := make([]string, 0, len(m)-len(seen))
	for k := range m {
		if !seen[k] {
			rest = append(rest, k)
		}
	}
	sortStrings(rest)
	for _, k := range rest {
		out.Set(k, m[k])
	}
	return out
}

func sortStrings(s []string) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j] < s[j-1]; j-- {
			s[j], s[j-1] = s[j-1], s[j]
		}
	}
}

// MarshalJSON writes the object in insertion order, two-space indented by the
// store (the indent is applied by WriteJSONString, not here).
func (o *Ordered) MarshalJSON() ([]byte, error) {
	var buf bytes.Buffer
	buf.WriteByte('{')
	for i, k := range o.keys {
		if i > 0 {
			buf.WriteByte(',')
		}
		keyJSON, err := json.Marshal(k)
		if err != nil {
			return nil, err
		}
		buf.Write(keyJSON)
		buf.WriteByte(':')
		valJSON, err := json.Marshal(o.values[k])
		if err != nil {
			return nil, err
		}
		buf.Write(valJSON)
	}
	buf.WriteByte('}')
	return buf.Bytes(), nil
}

// WriteJSONString renders JSON.stringify(value, null, 2) byte-for-byte: JS
// separates members with ",\n  "-style indentation and no trailing newline.
func (o *Ordered) WriteJSONString() (string, error) {
	data, err := o.MarshalJSON()
	if err != nil {
		return "", err
	}
	var indented bytes.Buffer
	if err := indentJSON(&indented, data); err != nil {
		return "", err
	}
	return indented.String(), nil
}

// deepCloneJSON clones a JSON-shaped value.
func deepCloneJSON(v any) any {
	switch t := v.(type) {
	case *Ordered:
		out := NewOrdered()
		for _, k := range t.keys {
			out.Set(k, deepCloneJSON(t.values[k]))
		}
		return out
	case map[string]any:
		out := make(map[string]any, len(t))
		for k, item := range t {
			out[k] = deepCloneJSON(item)
		}
		return out
	case []any:
		out := make([]any, len(t))
		for i, item := range t {
			out[i] = deepCloneJSON(item)
		}
		return out
	default:
		return v
	}
}

// indentJSON re-indents compact JSON with two spaces like JSON.stringify(,,2).
func indentJSON(buf *bytes.Buffer, data []byte) error {
	var compact bytes.Buffer
	if err := json.Compact(&compact, data); err != nil {
		return err
	}
	level := 0
	inString := false
	escaped := false
	src := compact.Bytes()
	for i := 0; i < len(src); i++ {
		c := src[i]
		if inString {
			buf.WriteByte(c)
			if escaped {
				escaped = false
			} else if c == '\\' {
				escaped = true
			} else if c == '"' {
				inString = false
			}
			continue
		}
		switch c {
		case '"':
			inString = true
			buf.WriteByte(c)
		case '{', '[':
			buf.WriteByte(c)
			// Empty containers stay inline: {} / [].
			if i+1 < len(src) && (src[i+1] == '}' || src[i+1] == ']') {
				buf.WriteByte(src[i+1])
				i++
				continue
			}
			level++
			buf.WriteByte('\n')
			writeIndent(buf, level)
		case '}', ']':
			level--
			buf.WriteByte('\n')
			writeIndent(buf, level)
			buf.WriteByte(c)
		case ',':
			buf.WriteByte(',')
			buf.WriteByte('\n')
			writeIndent(buf, level)
		case ':':
			buf.WriteByte(':')
			buf.WriteByte(' ')
		default:
			buf.WriteByte(c)
		}
	}
	return nil
}

func writeIndent(buf *bytes.Buffer, level int) {
	for i := 0; i < level; i++ {
		buf.WriteString("  ")
	}
}
