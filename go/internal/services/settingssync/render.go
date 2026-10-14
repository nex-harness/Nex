// render.go: JSON.stringify(value, null, 2) byte parity for the cli-config
// writes (mcp servers / plugin dirs). Values needing a specific key order use
// orderedMap; plain maps marshal with sorted keys (recorded in decisions.md).
package settingssync

import (
	"bytes"
	"encoding/json"
)

// orderedMap preserves insertion order for rendering.
type orderedMap struct {
	keys   []string
	values map[string]any
}

func newOrderedMap() *orderedMap {
	return &orderedMap{values: map[string]any{}}
}

func (m *orderedMap) set(key string, value any) {
	if _, exists := m.values[key]; !exists {
		m.keys = append(m.keys, key)
	}
	m.values[key] = value
}

func (m *orderedMap) get(key string) (any, bool) {
	v, ok := m.values[key]
	return v, ok
}

// MarshalJSON renders the ordered map preserving insertion order.
func (m *orderedMap) MarshalJSON() ([]byte, error) {
	var buf bytes.Buffer
	buf.WriteByte('{')
	for i, key := range m.keys {
		if i > 0 {
			buf.WriteByte(',')
		}
		keyJSON, err := json.Marshal(key)
		if err != nil {
			return nil, err
		}
		buf.Write(keyJSON)
		buf.WriteString(":")
		valueJSON, err := json.Marshal(m.values[key])
		if err != nil {
			return nil, err
		}
		buf.Write(valueJSON)
	}
	buf.WriteByte('}')
	return buf.Bytes(), nil
}

// renderJSONTwoSpace renders JSON.stringify(value, null, 2): 2-space indents,
// ": " separators, empty containers inline, no trailing newline.
func renderJSONTwoSpace(value any) string {
	var buf bytes.Buffer
	writeIndentedJSON(&buf, value, 0)
	return buf.String()
}

func writeIndentedJSON(buf *bytes.Buffer, value any, level int) {
	switch t := value.(type) {
	case *orderedMap:
		if len(t.keys) == 0 {
			buf.WriteString("{}")
			return
		}
		buf.WriteString("{\n")
		for i, key := range t.keys {
			if i > 0 {
				buf.WriteString(",\n")
			}
			writeIndent(buf, level+1)
			keyJSON, _ := json.Marshal(key)
			buf.Write(keyJSON)
			buf.WriteString(": ")
			writeIndentedJSON(buf, t.values[key], level+1)
		}
		buf.WriteString("\n")
		writeIndent(buf, level)
		buf.WriteString("}")
	case map[string]any:
		keys := make([]string, 0, len(t))
		for key := range t {
			keys = append(keys, key)
		}
		sortStringsInPlace(keys)
		if len(keys) == 0 {
			buf.WriteString("{}")
			return
		}
		buf.WriteString("{\n")
		for i, key := range keys {
			if i > 0 {
				buf.WriteString(",\n")
			}
			writeIndent(buf, level+1)
			keyJSON, _ := json.Marshal(key)
			buf.Write(keyJSON)
			buf.WriteString(": ")
			writeIndentedJSON(buf, t[key], level+1)
		}
		buf.WriteString("\n")
		writeIndent(buf, level)
		buf.WriteString("}")
	case []any:
		if len(t) == 0 {
			buf.WriteString("[]")
			return
		}
		buf.WriteString("[\n")
		for i, item := range t {
			if i > 0 {
				buf.WriteString(",\n")
			}
			writeIndent(buf, level+1)
			writeIndentedJSON(buf, item, level+1)
		}
		buf.WriteString("\n")
		writeIndent(buf, level)
		buf.WriteString("]")
	default:
		data, err := json.Marshal(value)
		if err != nil {
			buf.WriteString("null")
			return
		}
		buf.Write(data)
	}
}

func writeIndent(buf *bytes.Buffer, level int) {
	for i := 0; i < level; i++ {
		buf.WriteString("  ")
	}
}

func sortStringsInPlace(items []string) {
	for i := 1; i < len(items); i++ {
		for j := i; j > 0 && items[j] < items[j-1]; j-- {
			items[j], items[j-1] = items[j-1], items[j]
		}
	}
}
